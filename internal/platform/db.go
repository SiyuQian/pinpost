package platform

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations is the schema, one append-only file per change.
var Migrations, _ = fs.Sub(migrationFiles, "migrations")

// DBTX is what both a pool and a transaction offer, for the few helpers that
// run either inside a caller's transaction or on their own.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// OpenDB connects to PostgreSQL and checks the connection, so a bad
// DATABASE_URL fails at boot rather than on the first request.
func OpenDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, ScanUTC(cfg))
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// ScanUTC makes timestamps come back in UTC, whatever the server's or this
// process's time zone. The API has always rendered them with a Z suffix, and
// an updated_at echoed back as a concurrency token compares by instant anyway.
func ScanUTC(cfg *pgxpool.Config) *pgxpool.Config {
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
		return nil
	}
	return cfg
}

// Migrate applies every *.sql file in fsys that has not been applied yet, in
// name order, inside one transaction: PostgreSQL DDL is transactional, so a
// failing file leaves the schema exactly as it was. Files are append-only —
// an applied file is never re-run, so editing one changes nothing.
//
// The advisory lock serialises concurrent boots (several replicas starting at
// once): the second waits, then finds the first one's rows and applies
// nothing. It is keyed by schema so tests, one schema each, do not queue
// behind one another.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	names, err := fs.Glob(fsys, "*.sql") // sorted
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtext('pinpost.migrate.' || current_schema()))`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			name text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		rows, _ := tx.Query(ctx, `SELECT name FROM schema_migrations`)
		applied, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		done := make(map[string]bool, len(applied))
		for _, n := range applied {
			done[n] = true
		}
		for _, name := range names {
			if done[name] {
				continue
			}
			sql, err := fs.ReadFile(fsys, name)
			if err != nil {
				return err
			}
			// No arguments, so pgx sends it over the simple protocol, which
			// accepts a whole file of statements.
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
				return err
			}
		}
		return nil
	})
}
