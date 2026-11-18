package feedback

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"
)

func newOverviewStore(t *testing.T) *platform.Store {
	t.Helper()
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		os.Setenv("AWS_ENDPOINT_URL", "http://localhost:8000")
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" {
		os.Setenv("AWS_ACCESS_KEY_ID", "pinpost")
		os.Setenv("AWS_SECRET_ACCESS_KEY", "pinpost-secret")
	}
	cfg, err := platform.AWSConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	name := "pinpost-overview-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	store := platform.NewStore(cfg, name)
	if err := platform.EnsureTable(context.Background(), store); err != nil {
		pgtest.Unavailable(t, "dynamodb unreachable: %v", err)
	}
	t.Cleanup(func() {
		store.DB.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: &name})
	})
	return store
}

// The aggregate is read through a ProjectionExpression with name placeholders
// for the reserved words `status` and `type`, and the counting fields are
// unmarshalled straight into `counted`. A wrong attribute name there does not
// error — it yields zero-valued rows and a silently empty dashboard — so the
// counts are asserted against seeded records rather than the shape alone.
func TestOverviewCountsProjectedRows(t *testing.T) {
	store := newOverviewStore(t)
	svc := &Service{Store: store}
	orgID := uuid.NewString()
	other, mine := uuid.NewString(), uuid.NewString()

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
		item := &Item{
			Key: itemKey(orgID, id), FeedbackID: id, OrganizationID: orgID,
			ProjectID: s.projectID, Type: s.kind, Message: "seeded", Status: s.status,
			Priority: "none", CreatedAt: time.Now().UTC().Add(-s.age),
			UpdatedAt: time.Now().UTC(),
		}
		if err := store.Put(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}

	all, err := svc.Overview(context.Background(), orgID, "")
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 5 {
		t.Fatalf("total = %d, want 5", all.Total)
	}
	if all.ByStatus["open"] != 3 || all.ByStatus["resolved"] != 1 || all.ByStatus["spam"] != 1 {
		t.Fatalf("by_status = %v, want the projected status attribute counted", all.ByStatus)
	}
	if all.ByType["bug"] != 3 || all.ByType["suggestion"] != 1 || all.ByType["other"] != 1 {
		t.Fatalf("by_type = %v, want the projected type attribute counted", all.ByType)
	}
	// Four rows inside the 14-day window, three of them today.
	if len(all.Trend) == 0 {
		t.Fatalf("trend = %v, want the projected created_at bucketed", all.Trend)
	}
	var trended int
	for _, day := range all.Trend {
		trended += day["count"].(int)
	}
	if trended != 4 {
		t.Fatalf("trend total = %d, want 4 rows inside the window", trended)
	}

	// The scope is applied to the cached rows, not by re-reading the partition.
	scoped, err := svc.Overview(context.Background(), orgID, mine)
	if err != nil {
		t.Fatal(err)
	}
	if scoped.Total != 4 || scoped.ByStatus["open"] != 2 {
		t.Fatalf("scoped = %+v, want only the four rows of one project", scoped)
	}
}
