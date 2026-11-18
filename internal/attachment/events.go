package attachment

import (
	"context"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// VerifyByID re-runs verification for a single attachment, reading the
// object's current size itself. Local development has no event wiring, so
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
