// Command scan verifies attachments after they land in object storage.
// Triggered by the bucket's ObjectCreated notification: the bytes never pass
// through the API, so the size/MIME check and the signature scan happen here.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/SiyuQian/pinpost/internal/attachment"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx := context.Background()
	cfg, err := platform.AWSConfig(ctx)
	if err != nil {
		slog.Error("aws config failed", "error", err)
		os.Exit(1)
	}
	s3c := platform.NewS3(cfg)
	svc := &attachment.Service{
		Store:   platform.NewStore(cfg, env("PINPOST_TABLE", "pinpost")),
		S3:      s3c,
		Presign: s3.NewPresignClient(s3c),
		Bucket:  env("PINPOST_BUCKET", "pinpost-attachments"),
	}
	lambda.Start(svc.HandleS3Event)
}
