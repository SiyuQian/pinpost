package attachment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Claim's WHERE clause is what stands between a submission and another
// project's attachment, so each way it must refuse is pinned here; the last
// test checks the foreign key that backs it.

type fixture struct {
	db                *pgxpool.Pool
	orgID, projA, fbA string
	projB, fbB        string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := pgtest.New(t)
	f := fixture{db: db}
	f.orgID, f.projA = pgtest.Tenant(t, db)
	_, f.projB = pgtest.Tenant(t, db)
	f.fbA, f.fbB = f.feedback(t, f.projA), f.feedback(t, f.projB)
	return f
}

// feedback inserts a bare feedback row in the given project's organization.
func (f fixture) feedback(t *testing.T, projectID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.db.Exec(context.Background(), `INSERT INTO feedback
		(id, organization_id, project_id, idempotency_key, type, message)
		SELECT $1, organization_id, id, $3, 'bug', 'x' FROM projects WHERE id = $2`, id, projectID, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// pending inserts an unclaimed attachment created `age` ago.
func (f fixture) pending(t *testing.T, projectID string, age time.Duration) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.db.Exec(context.Background(), `INSERT INTO attachments
		(id, organization_id, project_id, object_key, original_name, mime_type, size, created_at)
		SELECT $1, organization_id, id, $4, 'shot.png', 'image/png', 12, $3
		FROM projects WHERE id = $2`, id, projectID, time.Now().Add(-age), projectID+"/"+id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f fixture) claim(t *testing.T, attID, projectID, feedbackID string) bool {
	t.Helper()
	var ok bool
	err := pgx.BeginFunc(context.Background(), f.db, func(tx pgx.Tx) error {
		var err error
		ok, err = Claim(context.Background(), tx, attID, projectID, feedbackID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestClaimLinksAndQueuesTheScan(t *testing.T) {
	f := newFixture(t)
	att := f.pending(t, f.projA, time.Minute)
	if !f.claim(t, att, f.projA, f.fbA) {
		t.Fatal("claiming an unclaimed attachment of the same project failed")
	}
	var queued bool
	if err := f.db.QueryRow(context.Background(),
		`SELECT next_scan_at IS NOT NULL FROM attachments WHERE id = $1`, att).Scan(&queued); err != nil || !queued {
		t.Fatalf("claimed attachment not queued for scanning (err %v)", err)
	}
}

func TestClaimRejectsForeignProject(t *testing.T) {
	f := newFixture(t)
	att := f.pending(t, f.projA, time.Minute)
	if f.claim(t, att, f.projB, f.fbB) {
		t.Fatal("an attachment of project A was claimed by project B")
	}
}

func TestClaimRejectsDoubleClaim(t *testing.T) {
	f := newFixture(t)
	att := f.pending(t, f.projA, time.Minute)
	if !f.claim(t, att, f.projA, f.fbA) {
		t.Fatal("first claim failed")
	}
	if f.claim(t, att, f.projA, f.feedback(t, f.projA)) {
		t.Fatal("an attachment was claimed twice")
	}
}

func TestClaimRejectsUnknownAttachment(t *testing.T) {
	f := newFixture(t)
	if f.claim(t, uuid.NewString(), f.projA, f.fbA) {
		t.Fatal("an unknown attachment id was claimed")
	}
}

func TestClaimRejectsExpiredAttachment(t *testing.T) {
	f := newFixture(t)
	att := f.pending(t, f.projA, pendingTTL+time.Hour)
	if f.claim(t, att, f.projA, f.fbA) {
		t.Fatal("an attachment past its claim window was claimed")
	}
}

// Even a write that skips Claim cannot attach a file to another project's
// feedback: the (project_id, feedback_id) foreign key refuses it.
func TestForeignKeyRefusesCrossProjectLink(t *testing.T) {
	f := newFixture(t)
	att := f.pending(t, f.projA, time.Minute)
	_, err := f.db.Exec(context.Background(), `UPDATE attachments SET feedback_id = $2 WHERE id = $1`, att, f.fbB)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("err = %v, want a foreign-key violation (23503)", err)
	}
}

func TestSweepUnclaimedKeepsClaimedAndFreshRows(t *testing.T) {
	f := newFixture(t)
	stale := f.pending(t, f.projA, pendingTTL+time.Hour)
	fresh := f.pending(t, f.projA, time.Minute)
	claimed := f.pending(t, f.projA, time.Minute)
	if !f.claim(t, claimed, f.projA, f.fbA) {
		t.Fatal("claim failed")
	}
	if _, err := f.db.Exec(context.Background(),
		`UPDATE attachments SET created_at = now() - interval '48 hours' WHERE id = $1`, claimed); err != nil {
		t.Fatal(err)
	}
	if err := (&Service{DB: f.db}).SweepUnclaimed(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{stale: false, fresh: true, claimed: true} {
		var exists bool
		f.db.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM attachments WHERE id = $1)`, id).Scan(&exists)
		if exists != want {
			t.Errorf("attachment %s exists = %v, want %v", id, exists, want)
		}
	}
}
