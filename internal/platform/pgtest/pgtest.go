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
	"github.com/google/uuid"
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
	pool, err := pgxpool.NewWithConfig(ctx, platform.ScanUTC(cfg))
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

// Tenant inserts an organization with one project and returns both ids, for
// tests that need somewhere for feedback to live.
func Tenant(t testing.TB, db *pgxpool.Pool) (orgID, projectID string) {
	t.Helper()
	orgID, projectID = uuid.NewString(), uuid.NewString()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1, 'Test Org')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO projects (id, organization_id, name, environment, write_key)
		VALUES ($1, $2, 'Test Project', 'development', $3)`, projectID, orgID, "wk_"+projectID); err != nil {
		t.Fatal(err)
	}
	return orgID, projectID
}
