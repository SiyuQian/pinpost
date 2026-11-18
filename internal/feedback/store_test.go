package feedback

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SiyuQian/pinpost/internal/attachment"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type allMembers struct{}

func (allMembers) IsMember(context.Context, string, string) (bool, error) { return true, nil }

func newService(t *testing.T) (*Service, *pgxpool.Pool, string, string) {
	t.Helper()
	db := pgtest.New(t)
	orgID, projectID := pgtest.Tenant(t, db)
	return &Service{DB: db, Attachment: &attachment.Service{DB: db}, Members: allMembers{}}, db, orgID, projectID
}

func input(orgID, projectID, key string) *CreateInput {
	return &CreateInput{OrganizationID: orgID, ProjectID: projectID, IdempotencyKey: key,
		Type: "bug", Message: "it broke"}
}

func status(err error) int {
	if apiErr, ok := err.(*platform.APIError); ok {
		return apiErr.Status
	}
	return 0
}

// Retries of one submission — sequential or racing — must produce one record.
func TestCreateIsIdempotentUnderConcurrency(t *testing.T) {
	svc, db, orgID, projectID := newService(t)
	ctx := context.Background()
	key := uuid.NewString()

	var wg sync.WaitGroup
	ids := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := svc.Create(ctx, input(orgID, projectID, key))
			if err != nil {
				t.Error(err)
				return
			}
			ids <- out.FeedbackID
		}()
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("replays returned different records: %s and %s", first, id)
		}
	}
	var rows, acts int
	db.QueryRow(ctx, `SELECT count(*) FROM feedback`).Scan(&rows)
	db.QueryRow(ctx, `SELECT count(*) FROM activities`).Scan(&acts)
	if rows != 1 || acts != 1 {
		t.Fatalf("feedback rows = %d, activities = %d; want 1 and 1", rows, acts)
	}
}

// updated_at is the optimistic-concurrency token. It is read back from the
// database, so the value a client holds compares equal to the stored one —
// a nanosecond Go timestamp against a microsecond column would 409 forever.
func TestUpdateConcurrencyToken(t *testing.T) {
	svc, _, orgID, projectID := newService(t)
	ctx := context.Background()
	out, err := svc.Create(ctx, input(orgID, projectID, uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := svc.Get(ctx, orgID, out.FeedbackID)
	if err != nil {
		t.Fatal(err)
	}
	token := cur.UpdatedAt.Format(time.RFC3339Nano)
	next := "in_progress"
	if _, err := svc.Update(ctx, orgID, out.FeedbackID, uuid.NewString(),
		&UpdateInput{Status: &next, UpdatedAt: token}); err != nil {
		t.Fatalf("update with a fresh token: %v", err)
	}
	back := "open"
	if _, err := svc.Update(ctx, orgID, out.FeedbackID, uuid.NewString(),
		&UpdateInput{Status: &back, UpdatedAt: token}); status(err) != 409 {
		t.Fatalf("update with a stale token: err %v, want 409", err)
	}
	// Another organization cannot reach the record at all.
	if _, err := svc.Update(ctx, uuid.NewString(), out.FeedbackID, uuid.NewString(),
		&UpdateInput{Status: &back}); status(err) != 404 {
		t.Fatalf("cross-org update: err %v, want 404", err)
	}
}

// A bad attachment id is the submitter's mistake, never a 500, and a refused
// claim must take the whole submission with it.
func TestCreateRejectsBadAttachmentsAtomically(t *testing.T) {
	svc, db, orgID, projectID := newService(t)
	ctx := context.Background()
	for name, id := range map[string]string{"not a uuid": "att-1", "unknown": uuid.NewString()} {
		in := input(orgID, projectID, uuid.NewString())
		in.AttachmentIDs = []string{id}
		if _, err := svc.Create(ctx, in); status(err) != 422 {
			t.Fatalf("%s: err %v, want 422", name, err)
		}
	}
	var rows int
	db.QueryRow(ctx, `SELECT count(*) FROM feedback`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("feedback rows = %d after refused submissions, want 0", rows)
	}
}

func TestListPagesNewestFirstAndMatchesLiterally(t *testing.T) {
	svc, _, orgID, projectID := newService(t)
	ctx := context.Background()
	var ids []string
	for _, msg := range []string{"first", "100% broken", "third"} {
		in := input(orgID, projectID, uuid.NewString())
		in.Message = msg
		out, err := svc.Create(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, out.FeedbackID)
	}

	page, next, err := svc.List(ctx, ListFilter{OrganizationID: orgID, Limit: 2})
	if err != nil || len(page) != 2 || next == "" || page[0].ID != ids[2] || page[1].ID != ids[1] {
		t.Fatalf("page 1 = %+v, next %q, err %v", page, next, err)
	}
	page, next, err = svc.List(ctx, ListFilter{OrganizationID: orgID, Limit: 2, Cursor: next})
	if err != nil || len(page) != 1 || next != "" || page[0].ID != ids[0] {
		t.Fatalf("page 2 = %+v, next %q, err %v", page, next, err)
	}
	// `%` in q is a character, not a wildcard.
	page, _, err = svc.List(ctx, ListFilter{OrganizationID: orgID, Query: "0%"})
	if err != nil || len(page) != 1 || page[0].ID != ids[1] {
		t.Fatalf("q=0%% matched %+v, err %v", page, err)
	}
	if _, _, err := svc.List(ctx, ListFilter{OrganizationID: orgID, Cursor: "garbage"}); status(err) != 422 {
		t.Fatalf("bad cursor: err %v, want 422", err)
	}
	if page, _, _ := svc.List(ctx, ListFilter{OrganizationID: uuid.NewString()}); len(page) != 0 {
		t.Fatalf("another organization listed %d rows", len(page))
	}
}
