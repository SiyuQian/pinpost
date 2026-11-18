// Package pgtest gives integration tests an isolated PostgreSQL schema.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Unavailable skips a test whose backing service is down — unless CI is set
// (GitHub Actions always sets it), where it fails instead. A skipped
// integration test in CI looks exactly like a passing one, which is how a
// suite quietly stops guarding anything.
func Unavailable(t testing.TB, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if os.Getenv("CI") != "" {
		t.Fatalf("%s (CI is set, so this fails instead of skipping)", msg)
	}
	t.Skip(msg)
}

// Schema returns a pool whose search_path is a fresh, empty schema, dropped
// when the test ends. One schema per test rather than a rolled-back
// transaction: the code under test opens its own transactions and the
// contract test reaches the pool through an HTTP server on other goroutines.
func Schema(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		Unavailable(t, "DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("DATABASE_URL: %v", err)
	}
	admin, err := pgx.ConnectConfig(ctx, cfg.ConnConfig.Copy())
	if err != nil {
		Unavailable(t, "postgres unreachable: %v", err)
	}
	buf := make([]byte, 6)
	rand.Read(buf)
	schema := "t_" + hex.EncodeToString(buf)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatalf("create schema: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(ctx)
	})
	return pool
}

// New returns a pool on a fresh schema with every migration applied.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := Schema(t)
	if err := platform.Migrate(context.Background(), pool, platform.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}
