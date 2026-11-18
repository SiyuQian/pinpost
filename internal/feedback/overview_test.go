package feedback

import (
	"context"
	"testing"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/google/uuid"
)

// The overview is two GROUP BY queries; the counts are asserted against
// seeded rows, including one outside the 14-day trend window and one in
// another project.
func TestOverviewCounts(t *testing.T) {
	ctx := context.Background()
	db := pgtest.New(t)
	svc := &Service{DB: db}
	orgID, mine := pgtest.Tenant(t, db)
	other := uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO projects (id, organization_id, name, environment, write_key)
		VALUES ($1, $2, 'Other', 'development', $3)`, other, orgID, "wk_"+other); err != nil {
		t.Fatal(err)
	}

	seed := []struct {
		projectID, status, kind string
		age                     time.Duration
	}{
		{mine, "open", "bug", time.Hour},
		{mine, "open", "suggestion", 3 * time.Hour},
		{mine, "resolved", "bug", 24 * time.Hour},
		{other, "open", "bug", time.Hour},
		// Older than the 14-day trend window: counted in the totals, absent
		// from the trend.
		{mine, "spam", "other", 20 * 24 * time.Hour},
	}
	for _, s := range seed {
		id := uuid.Must(uuid.NewV7()).String()
		if _, err := db.Exec(ctx, `INSERT INTO feedback
			(id, organization_id, project_id, idempotency_key, type, message, status, created_at)
			VALUES ($1, $2, $3, $4, $5, 'seeded', $6, $7)`,
			id, orgID, s.projectID, id, s.kind, s.status, time.Now().Add(-s.age)); err != nil {
			t.Fatal(err)
		}
	}

	all, err := svc.Overview(ctx, orgID, "")
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 5 {
		t.Fatalf("total = %d, want 5", all.Total)
	}
	if all.ByStatus["open"] != 3 || all.ByStatus["resolved"] != 1 || all.ByStatus["spam"] != 1 {
		t.Fatalf("by_status = %v", all.ByStatus)
	}
	if all.ByType["bug"] != 3 || all.ByType["suggestion"] != 1 || all.ByType["other"] != 1 {
		t.Fatalf("by_type = %v", all.ByType)
	}
	var trended int
	for _, day := range all.Trend {
		trended += day["count"].(int)
	}
	if trended != 4 {
		t.Fatalf("trend total = %d (%v), want 4 rows inside the window", trended, all.Trend)
	}

	scoped, err := svc.Overview(ctx, orgID, mine)
	if err != nil {
		t.Fatal(err)
	}
	if scoped.Total != 4 || scoped.ByStatus["open"] != 2 {
		t.Fatalf("scoped = %+v, want only the four rows of one project", scoped)
	}

	// Another organization sees nothing.
	if none, err := svc.Overview(ctx, uuid.NewString(), ""); err != nil || none.Total != 0 {
		t.Fatalf("foreign org overview = %+v, err %v", none, err)
	}
}
