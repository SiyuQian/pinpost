package main

// Cross-tenant authorization test (Phase 1 exit criterion: 关键越权测试).
// Seeds a second organization and verifies that neither its write key nor its
// dashboard session can read, modify or link anything belonging to the demo org.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SiyuQian/pinpost/internal/project"
	"github.com/google/uuid"
)

const (
	orgBWriteKey = "wk_orgb_authz_test_key_0001"
	orgBEmail    = "admin@orgb.test"
	orgBPassword = "orgb-pass"
)

// seedOrgB creates a second tenant with its own project, write key and owner.
// Fixed identifiers keep reruns against a persistent local table idempotent.
func seedOrgB(t *testing.T, c *contractClient) {
	t.Helper()
	if _, err := project.Provision(context.Background(), c.db, project.Params{
		OrgID:     "01900000-0000-7000-8000-0000000000b1",
		ProjectID: "01900000-0000-7000-8000-0000000000b2",
		MemberID:  "01900000-0000-7000-8000-0000000000b3",
		OrgName:   "Org B (authz test)", ProjectName: "Org B Project", Environment: "development",
		Email: orgBEmail, Password: orgBPassword, WriteKey: orgBWriteKey,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCrossTenantIsolation(t *testing.T) {
	c := newContractClient(t)
	seedOrgB(t, c)
	wkA := map[string]string{"X-Pinpost-Write-Key": project.DemoWriteKey}
	wkB := map[string]string{"X-Pinpost-Write-Key": orgBWriteKey}

	// Org A: one feedback with an uploaded attachment.
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("a", 40))
	attA := c.upload(wkA, "a.png", "image/png", png)
	s, body := c.do("POST", "/v1/feedback", withIdem(wkA, uuid.NewString()),
		[]byte(fmt.Sprintf(`{"type":"bug","message":"org A secret feedback","attachment_ids":[%q]}`, attA)))
	if s != 201 {
		t.Fatalf("org A feedback: %d %s", s, body)
	}
	fbA := jsonField(t, body, "feedback_id")

	// Org B's write key cannot link Org A's (already linked or not) attachment,
	// nor a fresh unlinked one authorized under Org A.
	attA2 := c.upload(wkA, "a2.png", "image/png", png)
	if s, _ := c.do("POST", "/v1/feedback", withIdem(wkB, uuid.NewString()),
		[]byte(fmt.Sprintf(`{"type":"bug","message":"steal attachment","attachment_ids":[%q]}`, attA2))); s != 422 {
		t.Fatalf("cross-project attachment link: want 422, got %d", s)
	}

	// The same idempotency key on two projects must create two distinct feedbacks
	// (idempotency is scoped per project, never shared across tenants).
	sharedIdem := uuid.NewString()
	_, bA := c.do("POST", "/v1/feedback", withIdem(wkA, sharedIdem), []byte(`{"type":"other","message":"idem scope A"}`))
	_, bB := c.do("POST", "/v1/feedback", withIdem(wkB, sharedIdem), []byte(`{"type":"other","message":"idem scope B"}`))
	if jsonField(t, bA, "feedback_id") == jsonField(t, bB, "feedback_id") {
		t.Fatalf("idempotency key leaked across projects: both returned %s", jsonField(t, bA, "feedback_id"))
	}
	fbB := jsonField(t, bB, "feedback_id")

	// Org A: one webhook endpoint with one terminal delivery (inserted directly,
	// so no dispatcher runs and no HTTP request leaves the test).
	c.do("POST", "/v1/auth/login", nil,
		[]byte(fmt.Sprintf(`{"Email":%q,"Password":%q}`, project.DemoEmail, project.DemoPassword)))
	s, body = c.do("POST", "/v1/webhook-endpoints", nil,
		[]byte(`{"url":"https://example.com/hook","event_types":["feedback.created.v1"]}`))
	if s != 201 {
		t.Fatalf("org A webhook endpoint: %d %s", s, body)
	}
	epA := jsonField(t, body, "id")
	delA := uuid.NewString()
	t.Cleanup(func() { // the 10-endpoint cap would otherwise fill up on a persistent database
		c.db.Exec(context.Background(), `DELETE FROM webhook_deliveries WHERE endpoint_id = $1::uuid`, epA)
		c.db.Exec(context.Background(), `DELETE FROM webhook_endpoints WHERE id = $1::uuid`, epA)
	})
	if _, err := c.db.Exec(context.Background(), `INSERT INTO webhook_deliveries
		(id, endpoint_id, organization_id, event_id, event_type, payload, status, attempt)
		SELECT $1::uuid, $2::uuid, organization_id, $3::uuid, 'feedback.created.v1', '{}'::jsonb, 'succeeded', 1
		FROM webhook_endpoints WHERE id = $2::uuid`, delA, epA, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	c.do("POST", "/v1/auth/logout", nil, nil)

	// Org B dashboard session must see none of Org A's data.
	if s, b := c.do("POST", "/v1/auth/login", nil,
		[]byte(fmt.Sprintf(`{"Email":%q,"Password":%q}`, orgBEmail, orgBPassword))); s != 200 {
		t.Fatalf("org B login: %d %s", s, b)
	}

	if _, b := c.do("GET", "/v1/feedback?limit=50", nil, nil); strings.Contains(string(b), fbA) ||
		strings.Contains(string(b), "org A secret") {
		t.Fatalf("org B feedback list leaks org A data: %s", b)
	}
	for _, probe := range []struct {
		method, path string
		payload      []byte
		want         int
	}{
		{"GET", "/v1/feedback/" + fbA, nil, 404},
		{"PATCH", "/v1/feedback/" + fbA, []byte(`{"status":"spam"}`), 404},
		{"POST", "/v1/feedback/" + fbA + "/notes", []byte(`{"Note":"intrusion"}`), 404},
		{"GET", "/v1/feedback/" + fbA + "/activities", nil, 404},
		{"GET", "/v1/attachments/" + attA + "/download", nil, 404},
		{"PATCH", "/v1/webhook-endpoints/" + epA, []byte(`{"status":"paused"}`), 404},
		{"DELETE", "/v1/webhook-endpoints/" + epA, nil, 404},
		{"GET", "/v1/webhook-endpoints/" + epA + "/deliveries", nil, 404},
		{"POST", "/v1/webhook-deliveries/" + delA + "/replay", nil, 404},
	} {
		if s, b := c.do(probe.method, probe.path, nil, probe.payload); s != probe.want {
			t.Errorf("org B %s %s: want %d, got %d %s", probe.method, probe.path, probe.want, s, b)
		}
	}
	// Org B can still see its own feedback (the isolation is not just "deny all").
	if s, _ := c.do("GET", "/v1/feedback/"+fbB, nil, nil); s != 200 {
		t.Errorf("org B own feedback: want 200, got %d", s)
	}
	if s, b := c.do("GET", "/v1/webhook-endpoints", nil, nil); s != 200 || strings.Contains(string(b), epA) {
		t.Errorf("org B webhook endpoint list: want 200 without org A's endpoint, got %d %s", s, b)
	}
	// Projects and members listings must be scoped to Org B.
	if _, b := c.do("GET", "/v1/projects", nil, nil); strings.Contains(string(b), "Demo Project") {
		t.Errorf("org B project list leaks demo project: %s", b)
	}
	if _, b := c.do("GET", "/v1/members", nil, nil); strings.Contains(string(b), project.DemoEmail) {
		t.Errorf("org B member list leaks demo members: %s", b)
	}
	c.do("POST", "/v1/auth/logout", nil, nil)

	// And the mirror image: Org A's admin cannot touch Org B's feedback.
	c.do("POST", "/v1/auth/login", nil,
		[]byte(fmt.Sprintf(`{"Email":%q,"Password":%q}`, project.DemoEmail, project.DemoPassword)))
	if s, _ := c.do("GET", "/v1/feedback/"+fbB, nil, nil); s != 404 {
		t.Errorf("org A reading org B feedback: want 404, got %d", s)
	}
	// Nor rotate org B's write key: that would lock org B's SDKs out.
	if s, _ := c.do("POST", "/v1/projects/01900000-0000-7000-8000-0000000000b2/write-key/rotate", nil, nil); s != 404 {
		t.Errorf("org A rotating org B write key: want 404, got %d", s)
	}
	if s, _ := c.do("GET", "/v1/sdk/config", wkB, nil); s != 200 {
		t.Errorf("org B write key after refused rotate: want 200, got %d", s)
	}
	// Org B's webhook probes changed nothing: endpoint still active, delivery still listed.
	_, b := c.do("GET", "/v1/webhook-endpoints", nil, nil)
	var eps struct {
		Endpoints []struct{ ID, Status string } `json:"endpoints"`
	}
	if err := json.Unmarshal(b, &eps); err != nil {
		t.Fatalf("org A endpoint list: %v %s", err, b)
	}
	found := false
	for _, e := range eps.Endpoints {
		if e.ID == epA {
			found = true
			if e.Status != "active" {
				t.Errorf("org A endpoint status after org B probes: want active, got %q", e.Status)
			}
		}
	}
	if !found {
		t.Errorf("org A endpoint missing after org B probes: %s", b)
	}
	if _, b := c.do("GET", "/v1/webhook-endpoints/"+epA+"/deliveries", nil, nil); !strings.Contains(string(b), delA) {
		t.Errorf("org A delivery missing after org B probes: %s", b)
	}
}
