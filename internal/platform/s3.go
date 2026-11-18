package platform

import (
	"context"
	"errors"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// AWSConfig loads the object-storage credentials and region the standard AWS
// way (environment, shared config, instance role), defaulting the region.
func AWSConfig(ctx context.Context) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if os.Getenv("AWS_REGION") == "" {
		opts = append(opts, awsconfig.WithRegion("us-east-1"))
	}
	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

// EnsureBucket creates the attachment bucket if it is missing; this is for the
// local RustFS container.
func EnsureBucket(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket})
	var owned *s3types.BucketAlreadyOwnedByYou
	var exists *s3types.BucketAlreadyExists
	if errors.As(err, &owned) || errors.As(err, &exists) {
		return nil
	}
	return err
}

// NewS3 builds the object-storage client. S3_ENDPOINT_URL points it at an
// S3-compatible store (RustFS, MinIO, R2), addressed path-style.
func NewS3(cfg aws.Config) *s3.Client {
	endpoint := os.Getenv("S3_ENDPOINT_URL")
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		// Presigned URLs are handed to browsers, which will not send the
		// checksum headers the SDK would otherwise sign — the signature then
		// fails to match. Only compute checksums where the API requires them.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
			o.UsePathStyle = true
		}
	})
}
