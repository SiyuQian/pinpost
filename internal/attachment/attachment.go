// Package attachment handles upload authorization, S3 object storage and
// metadata. Bytes never pass through this process: the SDK PUTs straight to a
// presigned S3 URL, and verification happens afterwards in the scan worker.
package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxSize = 10 << 20 // 10 MiB
	// pendingTTL is how long an authorized upload may wait for a submission
	// to claim it. Unclaimed rows past it cannot be claimed and are swept.
	pendingTTL   = 24 * time.Hour
	uploadExpiry = 15 * time.Minute
	// downloadExpiry keeps a member's download link short-lived; the bucket
	// itself is never public.
	downloadExpiry = 60 * time.Second
	// scanRetry is how long a claimed attachment waits between scan attempts
	// while its object has not landed, and the lease a crashed worker leaves.
	scanRetry = 30 * time.Second
	// missingAfter gives up on an object that never arrived. The presigned
	// URL expires long before this, so nothing can still upload it.
	missingAfter = time.Hour
)

var allowedMime = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/webp": true, "text/plain": true,
}

// Meta is the attachment view the dashboard sees.
type Meta struct {
	ID           string `json:"id" db:"id"`
	OriginalName string `json:"original_name" db:"original_name"`
	MimeType     string `json:"mime_type" db:"mime_type"`
	Size         int64  `json:"size" db:"size"`
	ScanStatus   string `json:"scan_status" db:"scan_status"`
}

type Service struct {
	DB      *pgxpool.Pool
	S3      *s3.Client
	Presign *s3.PresignClient
	Bucket  string
}

type CreateUploadInput struct {
	OriginalName string `json:"original_name"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
}

type CreateUploadOutput struct {
	AttachmentID string `json:"attachment_id"`
	UploadURL    string `json:"upload_url"`
	ExpiresAt    string `json:"expires_at"`
}

// CreateUpload registers pending metadata and returns a presigned S3 PUT URL.
// The declared length is part of the signature, so the size limit is enforced
// by S3 itself; the real content type is verified later by the scan worker.
func (s *Service) CreateUpload(ctx context.Context, orgID, projectID string, in *CreateUploadInput) (*CreateUploadOutput, error) {
	fields := map[string]string{}
	name := strings.TrimSpace(in.OriginalName)
	if name == "" || len(name) > 255 || strings.ContainsAny(name, "/\\") {
		fields["original_name"] = "required, no path separators, max 255 chars"
	}
	if !allowedMime[in.MimeType] {
		fields["mime_type"] = "allowed: image/jpeg, image/png, image/webp, text/plain"
	}
	if in.Size <= 0 || in.Size > maxSize {
		fields["size"] = "must be between 1 byte and 10 MiB"
	}
	if len(fields) > 0 {
		return nil, platform.ValidationErr(fields)
	}

	attID := uuid.Must(uuid.NewV7()).String()
	objectKey := projectID + "/" + attID
	if _, err := s.DB.Exec(ctx, `INSERT INTO attachments
		(id, organization_id, project_id, object_key, original_name, mime_type, size)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		attID, orgID, projectID, objectKey, name, in.MimeType, in.Size); err != nil {
		return nil, err
	}

	signed, err := s.Presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        &s.Bucket,
		Key:           &objectKey,
		ContentLength: aws.Int64(in.Size),
	}, s3.WithPresignExpires(uploadExpiry))
	if err != nil {
		return nil, err
	}
	return &CreateUploadOutput{
		AttachmentID: attID,
		UploadURL:    signed.URL,
		ExpiresAt:    time.Now().Add(uploadExpiry).UTC().Format(time.RFC3339),
	}, nil
}

// Claim links an attachment to a feedback record inside the submission's
// transaction, and queues it for the scan worker. It reports false when the
// attachment is unknown, already claimed, expired or belongs to another
// project — the WHERE clause is what stands between a submission and another
// tenant's attachment, and the (project_id, feedback_id) foreign key backs it.
func Claim(ctx context.Context, tx pgx.Tx, attID, projectID, feedbackID string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE attachments SET feedback_id = $3, next_scan_at = now()
		WHERE id = $1 AND project_id = $2 AND feedback_id IS NULL AND created_at > $4`,
		attID, projectID, feedbackID, time.Now().Add(-pendingTTL))
	return tag.RowsAffected() == 1, err
}

// ListForFeedback returns a feedback's attachments.
func (s *Service) ListForFeedback(ctx context.Context, orgID, feedbackID string) ([]Meta, error) {
	rows, _ := s.DB.Query(ctx, `SELECT id, original_name, mime_type, size, scan_status
		FROM attachments WHERE organization_id = $1 AND feedback_id = $2 ORDER BY id`, orgID, feedbackID)
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[Meta])
	if out == nil {
		out = []Meta{}
	}
	return out, err
}

// ServeDownload redirects a member to a short-lived presigned URL after
// checking tenant ownership and scan status. The bytes never traverse this
// process.
func (s *Service) ServeDownload(w http.ResponseWriter, r *http.Request, orgID, attID string) error {
	var objectKey, mime, name, upload, scan string
	err := s.DB.QueryRow(r.Context(), `SELECT object_key, mime_type, original_name, upload_status, scan_status
		FROM attachments WHERE id = $1 AND organization_id = $2`, attID, orgID).
		Scan(&objectKey, &mime, &name, &upload, &scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return platform.Errf(404, "not_found", "Attachment not found")
	}
	if err != nil {
		return err
	}
	if upload != "uploaded" || scan != "clean" {
		return platform.Errf(409, "not_available", "Attachment is not scanned or not uploaded")
	}
	signed, err := s.Presign.PresignGetObject(r.Context(), &s3.GetObjectInput{
		Bucket:                     &s.Bucket,
		Key:                        &objectKey,
		ResponseContentType:        aws.String(mime),
		ResponseContentDisposition: aws.String(fmt.Sprintf("inline; filename=%q", name)),
	}, s3.WithPresignExpires(downloadExpiry))
	if err != nil {
		return err
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.Redirect(w, r, signed.URL, http.StatusFound)
	return nil
}

// Verify reads an uploaded object back, checks that it is what was declared,
// scans it and records the verdict. The scan worker calls it; tests call it
// directly.
func (s *Service) Verify(ctx context.Context, objectKey string, size int64) error {
	_, attID, ok := strings.Cut(objectKey, "/")
	if !ok || uuid.Validate(attID) != nil {
		return fmt.Errorf("unexpected object key %q", objectKey)
	}
	var declaredMime string
	var declaredSize int64
	err := s.DB.QueryRow(ctx, `SELECT mime_type, size FROM attachments WHERE id = $1`, attID).
		Scan(&declaredMime, &declaredSize)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("attachment %s not found", attID)
	}
	if err != nil {
		return err
	}

	status := "clean"
	var checksum string
	switch {
	case size > maxSize || size != declaredSize:
		// The presigned URL signs the declared length, so this only fires if
		// the object was replaced out of band.
		status = "rejected"
	default:
		body, err := s.readObject(ctx, objectKey)
		if err != nil {
			return err
		}
		sniffed := http.DetectContentType(body)
		if !mimeMatches(declaredMime, sniffed) {
			// Was a 400 on upload before the bytes bypassed us; now it is a
			// verdict recorded after the fact. Download still refuses it.
			status = "rejected"
		} else {
			status = scan(declaredMime, body)
		}
		sum := sha256.Sum256(body)
		checksum = hex.EncodeToString(sum[:])
	}

	// An unclaimed row may have been swept meanwhile; updating nothing is fine.
	_, err = s.DB.Exec(ctx, `UPDATE attachments
		SET upload_status = 'uploaded', scan_status = $2, checksum = $3, size = $4
		WHERE id = $1`, attID, status, checksum, size)
	return err
}

// VerifyByID re-runs verification for a single attachment, reading the
// object's current size itself. cmd/server exposes it on a dev-only route so
// local tooling need not wait for the scan worker.
func (s *Service) VerifyByID(ctx context.Context, attID string) error {
	if uuid.Validate(attID) != nil {
		return platform.Errf(404, "not_found", "Attachment not found")
	}
	var objectKey string
	err := s.DB.QueryRow(ctx, `SELECT object_key FROM attachments WHERE id = $1`, attID).Scan(&objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return platform.Errf(404, "not_found", "Attachment not found")
	}
	if err != nil {
		return err
	}
	head, err := s.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.Bucket, Key: &objectKey})
	if err != nil {
		return platform.Errf(409, "not_uploaded", "Attachment object is not in the bucket yet")
	}
	return s.Verify(ctx, objectKey, aws.ToInt64(head.ContentLength))
}

// ScanDue verifies claimed attachments whose scan is due and reports how many
// it looked at. Only claimed rows are scanned: an unclaimed attachment is
// reachable from nowhere, and download refuses anything not scanned clean.
//
// Rows are leased rather than locked across the S3 reads: the claim pushes
// next_scan_at forward and commits, so a slow object holds no connection and
// a crashed worker's rows come back by themselves. SKIP LOCKED keeps
// replicas off each other's rows. At most five objects (50 MiB) per call.
//
// ponytail: fixed 30s retry while an object is missing; back off
// exponentially if HEADs on never-uploaded objects ever add up.
func (s *Service) ScanDue(ctx context.Context) (int, error) {
	rows, _ := s.DB.Query(ctx, `UPDATE attachments SET next_scan_at = now() + $1::interval
		WHERE id IN (
			SELECT id FROM attachments
			WHERE scan_status = 'pending' AND feedback_id IS NOT NULL AND next_scan_at <= now()
			ORDER BY next_scan_at LIMIT 5
			FOR UPDATE SKIP LOCKED)
		RETURNING id, object_key, created_at`, scanRetry.String())
	type due struct {
		ID        string    `db:"id"`
		ObjectKey string    `db:"object_key"`
		CreatedAt time.Time `db:"created_at"`
	}
	batch, err := pgx.CollectRows(rows, pgx.RowToStructByName[due])
	if err != nil {
		return 0, err
	}
	for _, d := range batch {
		head, err := s.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.Bucket, Key: &d.ObjectKey})
		var missing *s3types.NotFound
		switch {
		case errors.As(err, &missing) && time.Since(d.CreatedAt) > missingAfter:
			// The upload never happened. Record that rather than retry forever;
			// download refuses the row either way.
			if _, err := s.DB.Exec(ctx, `UPDATE attachments SET scan_status = 'rejected' WHERE id = $1`, d.ID); err != nil {
				return 0, err
			}
		case err != nil:
			// Not there yet, or storage is unreachable: the lease retries it.
			slog.Info("attachment not scannable yet", "attachment_id", d.ID, "error", err)
		default:
			if err := s.Verify(ctx, d.ObjectKey, aws.ToInt64(head.ContentLength)); err != nil {
				slog.Error("attachment scan failed", "attachment_id", d.ID, "error", err)
			}
		}
	}
	return len(batch), nil
}

// SweepUnclaimed deletes attachment rows no submission claimed in time. Their
// objects stay in the bucket (README, Boundaries).
func (s *Service) SweepUnclaimed(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM attachments WHERE feedback_id IS NULL AND created_at < $1`,
		time.Now().Add(-pendingTTL))
	return err
}

func (s *Service) readObject(ctx context.Context, key string) ([]byte, error) {
	out, err := s.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.Bucket, Key: &key})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(io.LimitReader(out.Body, maxSize+1))
}

func mimeMatches(declared, sniffed string) bool {
	if strings.HasPrefix(sniffed, declared) {
		return true
	}
	// DetectContentType reports text/plain with charset for logs.
	if declared == "text/plain" && strings.HasPrefix(sniffed, "text/plain") {
		return true
	}
	return false
}
