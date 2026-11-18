package platform_test

import (
	"context"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
)

func TestMigrateAppliesOnceAndInOrder(t *testing.T) {
	pool := pgtest.Schema(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		// 0002 depends on 0001, so name order is load-bearing.
		"0002_b.sql": {Data: []byte(`ALTER TABLE a ADD COLUMN y int; CREATE TABLE b (id int);`)},
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (x int);`)},
		"notes.txt":  {Data: []byte(`not a migration`)},
	}
	for i := 0; i < 2; i++ {
		if err := platform.Migrate(ctx, pool, fsys); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("schema_migrations rows = %d, err %v; want 2", n, err)
	}
}

func TestMigrateConcurrentBootsApplyOnce(t *testing.T) {
	pool := pgtest.Schema(t)
	ctx := context.Background()
	// Not idempotent on purpose: a second application would fail.
	fsys := fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE a (x int);`)}}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- platform.Migrate(ctx, pool, fsys)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateFailureRollsBackTheBatch(t *testing.T) {
	pool := pgtest.Schema(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (x int);`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE broken (`)},
	}
	if err := platform.Migrate(ctx, pool, fsys); err == nil {
		t.Fatal("want an error from the broken migration")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('a') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("table a exists = %v (err %v); a failed batch must leave nothing behind", exists, err)
	}
}
