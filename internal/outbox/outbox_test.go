package outbox

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/google/uuid"
)

func TestCreateEndpointCapsPerOrganization(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		h.endpoint(t, "https://example.com/hook/"+strconv.Itoa(i))
	}
	_, err := h.svc.CreateEndpoint(ctx, h.orgID, "https://example.com/hook/10", nil)
	var ae *platform.APIError
	if !errors.As(err, &ae) || ae.Status != 422 || ae.Code != "limit_exceeded" {
		t.Fatalf("11th endpoint: want 422 limit_exceeded, got %v", err)
	}
	other, _ := pgtest.Tenant(t, h.db)
	if _, err := h.svc.CreateEndpoint(ctx, other, "https://example.com/hook/0", nil); err != nil {
		t.Fatalf("other organization must not share the cap: %v", err)
	}
}

func TestSweepDeliveriesKeepsRecentHistory(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ep := h.endpoint(t, "https://example.com/hook")
	ids := [2]string{uuid.NewString(), uuid.NewString()} // old, recent
	for _, id := range ids {
		if _, err := h.db.Exec(ctx, `INSERT INTO webhook_deliveries
			(id, endpoint_id, organization_id, event_id, event_type, payload, status, attempt)
			SELECT $1::uuid, $2::uuid, organization_id, $3::uuid, 'feedback.created.v1', '{}'::jsonb, 'succeeded', 1
			FROM webhook_endpoints WHERE id = $2::uuid`, id, ep.ID, uuid.NewString()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.db.Exec(ctx, `UPDATE webhook_deliveries SET created_at = now() - interval '31 days' WHERE id = $1::uuid`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SweepDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	for i, want := range [2]int{0, 1} {
		var n int
		if err := h.db.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE id = $1::uuid`, ids[i]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("delivery %d: want %d rows after sweep, got %d", i, want, n)
		}
	}
}
