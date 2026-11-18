package attachment

import (
	"context"
	"log/slog"
	"net/url"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// HandleS3Event verifies every newly uploaded object. Wired to the bucket's
// ObjectCreated notification; a returned error makes Lambda retry the record.
func (s *Service) HandleS3Event(ctx context.Context, e events.S3Event) error {
	for _, r := range e.Records {
		key, err := url.QueryUnescape(r.S3.Object.Key)
		if err != nil {
			slog.Error("undecodable object key", "key", r.S3.Object.Key, "error", err)
			continue
		}
		if err := s.Verify(ctx, key, r.S3.Object.Size); err != nil {
			return err
		}
	}
	return nil
}

// VerifyByID re-runs verification for a single attachment, reading the
// object's current size itself. The deployed stack never calls it — the S3
// event carries the size — but local development has no event wiring, so
// cmd/server exposes it on a dev-only route.
func (s *Service) VerifyByID(ctx context.Context, attID string) error {
	var it Item
	k := itemKey(attID)
	found, err := s.Store.Get(ctx, k.PK, k.SK, &it)
	if err != nil {
		return err
	}
	if !found {
		return platform.Errf(404, "not_found", "Attachment not found")
	}
	head, err := s.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.Bucket, Key: &it.ObjectKey})
	if err != nil {
		return platform.Errf(409, "not_uploaded", "Attachment object is not in the bucket yet")
	}
	return s.Verify(ctx, it.ObjectKey, aws.ToInt64(head.ContentLength))
}
