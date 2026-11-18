// Command webhook delivers feedback events to registered endpoints. It is
// driven by the table's change stream, which replaced the outbox table and its
// polling worker; retries and the dead-letter queue belong to the event source.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/SiyuQian/pinpost/internal/outbox"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-lambda-go/lambda"
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
	svc := &outbox.Service{Store: platform.NewStore(cfg, env("PINPOST_TABLE", "pinpost"))}
	lambda.Start(svc.HandleStream)
}
