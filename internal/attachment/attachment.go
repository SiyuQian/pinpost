// Package attachment handles upload authorization, S3 object storage and
// metadata. Bytes never pass through this process: the SDK PUTs straight to a
// presigned S3 URL (Lambda's 6 MB payload ceiling would reject a 10 MiB file),
// and verification happens afterwards in the scan handler.
package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamotypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

const (
	maxSize = 10 << 20 // 10 MiB
	// pendingTTL reclaims attachments that were authorized but never claimed
	// by a feedback submission, so the table never needs a cleanup scan.
	pendingTTL   = 24 * time.Hour
	uploadExpiry = 15 * time.Minute
	// downloadExpiry keeps a member's download link short-lived; the bucket
	// itself is never public.
	downloadExpiry = 60 * time.Second
)

var allowedMime = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/webp": true, "text/plain": true,
}

// Item is the attachment record. It sits in its own partition (downloads know
// only the attachment id) and carries org_id so the download check is a single
// GetItem rather than a join onto projects.
type Item struct {
	platform.Key
	GSI1PK         string    `dynamodbav:"gsi1pk,omitempty"`
	GSI1SK         string    `dynamodbav:"gsi1sk,omitempty"`
	AttachmentID   string    `dynamodbav:"attachment_id"`
	OrganizationID string    `dynamodbav:"org_id"`
	ProjectID      string    `dynamodbav:"project_id"`
	FeedbackID     string    `dynamodbav:"feedback_id,omitempty"`
	ObjectKey      string    `dynamodbav:"object_key"`
	OriginalName   string    `dynamodbav:"original_name"`
	MimeType       string    `dynamodbav:"mime_type"`
	Size           int64     `dynamodbav:"size"`
	UploadStatus   string    `dynamodbav:"upload_status"`
	ScanStatus     string    `dynamodbav:"scan_status"`
	Checksum       string    `dynamodbav:"checksum"`
	CreatedAt      time.Time `dynamodbav:"created_at"`
	ExpiresAt      int64     `dynamodbav:"expires_at,omitempty"`
}

func itemKey(attID string) platform.Key {
	return platform.Key{PK: "ATT#" + attID, SK: platform.Meta}
}

// Meta is the attachment view the dashboard sees.
type Meta struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	ScanStatus   string `json:"scan_status"`
}

type Service struct {
	Store   *platform.Store
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
// by S3 itself; the real content type is verified later by the scan handler.
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
	if err := s.Store.Put(ctx, &Item{
		Key:            itemKey(attID),
		AttachmentID:   attID,
		OrganizationID: orgID,
		ProjectID:      projectID,
		ObjectKey:      objectKey,
		OriginalName:   name,
		MimeType:       in.MimeType,
		Size:           in.Size,
		UploadStatus:   "pending",
		ScanStatus:     "pending",
		CreatedAt:      time.Now().UTC(),
		ExpiresAt:      platform.TTL(pendingTTL),
	}); err != nil {
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

// LinkTransactItem claims an attachment for a feedback record. It is part of
// the submission transaction: the condition replaces the old
// "UPDATE ... WHERE feedback_id IS NULL" row-count check, and removing the TTL
// turns a self-expiring pending object into one that lives with its feedback.
func LinkTransactItem(store *platform.Store, attID, projectID, feedbackID string) dynamotypes.TransactWriteItem {
	k := itemKey(attID)
	return dynamotypes.TransactWriteItem{Update: &dynamotypes.Update{
		TableName:        &store.Table,
		Key:              platform.KeyAV(k.PK, k.SK),
		UpdateExpression: aws.String("SET feedback_id = :fb, gsi1pk = :gpk, gsi1sk = :gsk REMOVE expires_at"),
		ConditionExpression: aws.String(
			"attribute_exists(pk) AND attribute_not_exists(feedback_id) AND project_id = :proj"),
		ExpressionAttributeValues: map[string]dynamotypes.AttributeValue{
			":fb":   platform.S(feedbackID),
			":gpk":  platform.S("FB#" + feedbackID),
			":gsk":  platform.S("ATT#" + attID),
			":proj": platform.S(projectID),
		},
	}}
}

// ListForFeedback returns a feedback's attachments through GSI1.
func (s *Service) ListForFeedback(ctx context.Context, feedbackID string) ([]Meta, error) {
	var items []Item
	if err := s.Store.QueryAll(ctx, &dynamodb.QueryInput{
		IndexName:              aws.String("GSI1"),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		ExpressionAttributeValues: map[string]dynamotypes.AttributeValue{
			":pk": platform.S("FB#" + feedbackID),
		},
	}, &items); err != nil {
		return nil, err
	}
	out := []Meta{}
	for _, it := range items {
		out = append(out, Meta{ID: it.AttachmentID, OriginalName: it.OriginalName,
			MimeType: it.MimeType, Size: it.Size, ScanStatus: it.ScanStatus})
	}
	return out, nil
}

// ServeDownload redirects a member to a short-lived presigned URL after
// checking tenant ownership and scan status. The bytes never traverse Lambda,
// whose response payload ceiling is smaller than the upload limit.
func (s *Service) ServeDownload(w http.ResponseWriter, r *http.Request, orgID, attID string) error {
	var it Item
	k := itemKey(attID)
	found, err := s.Store.Get(r.Context(), k.PK, k.SK, &it)
	if err != nil {
		return err
	}
	if !found || it.OrganizationID != orgID {
		return platform.Errf(404, "not_found", "Attachment not found")
	}
	if it.UploadStatus != "uploaded" || it.ScanStatus != "clean" {
		return platform.Errf(409, "not_available", "Attachment is not scanned or not uploaded")
	}
	signed, err := s.Presign.PresignGetObject(r.Context(), &s3.GetObjectInput{
		Bucket:                     &s.Bucket,
		Key:                        &it.ObjectKey,
		ResponseContentType:        aws.String(it.MimeType),
		ResponseContentDisposition: aws.String(fmt.Sprintf("inline; filename=%q", it.OriginalName)),
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
// scans it and records the verdict. It is the body of the S3 event handler and
// is called directly by tests — there is no event system to simulate.
func (s *Service) Verify(ctx context.Context, objectKey string, size int64) error {
	_, attID, ok := strings.Cut(objectKey, "/")
	if !ok {
		return fmt.Errorf("unexpected object key %q", objectKey)
	}
	k := itemKey(attID)
	var it Item
	found, err := s.Store.Get(ctx, k.PK, k.SK, &it)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("attachment %s not found", attID)
	}

	status := "clean"
	var checksum string
	switch {
	case size > maxSize || size != it.Size:
		// The presigned URL signs the declared length, so this only fires if
		// the object was replaced out of band.
		status = "rejected"
	default:
		body, err := s.readObject(ctx, objectKey)
		if err != nil {
			return err
		}
		sniffed := http.DetectContentType(body)
		if !mimeMatches(it.MimeType, sniffed) {
			// Was a 400 on upload before the bytes bypassed us; now it is a
			// verdict recorded after the fact. Download still refuses it.
			status = "rejected"
		} else {
			status = scan(it.MimeType, body)
		}
		sum := sha256.Sum256(body)
		checksum = hex.EncodeToString(sum[:])
	}

	_, err = s.Store.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.Store.Table,
		Key:       platform.KeyAV(k.PK, k.SK),
		UpdateExpression: aws.String(
			"SET upload_status = :uploaded, scan_status = :scan, checksum = :sum, #sz = :size"),
		// Without this, an update against an item the TTL already removed
		// would recreate it — with no org_id and no expires_at, so a row that
		// belongs to nobody and never goes away.
		ConditionExpression:      aws.String("attribute_exists(pk)"),
		ExpressionAttributeNames: map[string]string{"#sz": "size"},
		ExpressionAttributeValues: map[string]dynamotypes.AttributeValue{
			":uploaded": platform.S("uploaded"),
			":scan":     platform.S(status),
			":sum":      platform.S(checksum),
			":size":     platform.N(strconv.FormatInt(size, 10)),
		},
	})
	if platform.ConditionFailed(err) {
		slog.Info("attachment expired before its scan finished", "attachment_id", attID)
		return nil
	}
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
