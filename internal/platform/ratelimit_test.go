package platform

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
)

// The limiter is the only abuse control on the public ingestion path, and a
// regression that makes Allow always return true is otherwise silent.
func newLimiterStore(t *testing.T) *Store {
	t.Helper()
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		os.Setenv("AWS_ENDPOINT_URL", "http://localhost:8000")
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" {
		os.Setenv("AWS_ACCESS_KEY_ID", "pinpost")
		os.Setenv("AWS_SECRET_ACCESS_KEY", "pinpost-secret")
	}
	cfg, err := AWSConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(cfg, "pinpost-test")
	if err := EnsureTable(context.Background(), store); err != nil {
		// pgtest.Unavailable, inlined: pgtest imports this package.
		if os.Getenv("CI") != "" {
			t.Fatalf("dynamodb unreachable: %v (CI is set, so this fails instead of skipping)", err)
		}
		t.Skipf("dynamodb unreachable: %v", err)
	}
	return store
}

func TestRateLimiterIsPerKey(t *testing.T) {
	ctx := context.Background()
	rl := NewRateLimiter(newLimiterStore(t), 3)
	a, b := "key-"+uuid.NewString(), "key-"+uuid.NewString()

	for i := 1; i <= rl.Limit; i++ {
		if !rl.Allow(ctx, a) {
			t.Fatalf("request %d of %d on key A was rejected", i, rl.Limit)
		}
	}
	if rl.Allow(ctx, a) {
		t.Error("key A was allowed past its limit")
	}
	// Exhausting one key must not spend another key's budget — this is what
	// breaks when the key degenerates to a single shared bucket.
	if !rl.Allow(ctx, b) {
		t.Error("key B was rejected because key A had been exhausted")
	}
}

func TestRateLimiterFailsOpen(t *testing.T) {
	ctx := context.Background()
	store := newLimiterStore(t)
	// Abuse control, not a security boundary: a limiter outage must not take
	// ingestion down with it.
	rl := NewRateLimiter(&Store{DB: store.DB, Table: "pinpost-no-such-table"}, 1)
	if !rl.Allow(ctx, "any") {
		t.Error("Allow returned false when the table was unreachable; it must fail open")
	}
}
