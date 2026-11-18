package outbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
)

// Delivery integration test: runs the stream consumer against DynamoDB Local
// and a stand-in subscriber. Skips when DynamoDB is unreachable.
func newTestService(t *testing.T) *Service {
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
	store := platform.NewStore(cfg, "pinpost-test")
	if err := platform.EnsureTable(context.Background(), store); err != nil {
		pgtest.Unavailable(t, "dynamodb unreachable: %v", err)
	}
	return &Service{Store: store}
}

func createdRecord(orgID string) events.DynamoDBEventRecord {
	return events.DynamoDBEventRecord{
		EventID:   "ev-" + uuid.NewString(),
		EventName: "INSERT",
		Change: events.DynamoDBStreamRecord{
			SequenceNumber:              "seq-" + uuid.NewString(),
			ApproximateCreationDateTime: events.SecondsEpochTime{Time: time.Unix(1755000000, 0).UTC()},
			NewImage: map[string]events.DynamoDBAttributeValue{
				"sk": av("FB#01"), "org_id": av(orgID), "feedback_id": av("fb-1"),
				"project_id": av("proj-1"), "type": av("bug"), "message": av("it broke"),
				"created_at": av(time.Now().UTC().Format(time.RFC3339Nano)),
			},
		},
	}
}

func TestDeliverSignsAndRecords(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	orgID := "org-" + uuid.NewString()

	var got *http.Request
	var body []byte
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer sub.Close()

	ep, err := svc.CreateEndpoint(ctx, orgID, sub.URL, []string{"feedback.created.v1"})
	if err != nil {
		t.Fatal(err)
	}
	record := createdRecord(orgID)
	if _, err := svc.HandleStream(ctx, events.DynamoDBEvent{Records: []events.DynamoDBEventRecord{record}}); err != nil {
		t.Fatalf("handle stream: %v", err)
	}
	if got == nil {
		t.Fatal("subscriber never called")
	}

	ts, _ := strconv.ParseInt(got.Header.Get("X-Pinpost-Timestamp"), 10, 64)
	if want := "v1=" + Sign(ep.Secret, ts, body); got.Header.Get("X-Pinpost-Signature") != want {
		t.Errorf("signature = %q, want %q", got.Header.Get("X-Pinpost-Signature"), want)
	}
	var payload struct {
		EventID   string          `json:"event_id"`
		EventType string          `json:"event_type"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.EventType != "feedback.created.v1" || !strings.Contains(string(payload.Data), "it broke") {
		t.Errorf("unexpected payload: %s", body)
	}
	// The event id is derived from the stream record, so a Lambda retry of the
	// same record delivers the same id and a consumer can dedupe on it.
	if payload.EventID != record.EventID+":feedback.created.v1" {
		t.Errorf("event id = %q, not derived from the stream record", payload.EventID)
	}

	deliveries, err := svc.ListDeliveries(ctx, orgID, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].Status != "succeeded" {
		t.Fatalf("deliveries = %+v", deliveries)
	}

	// Replay re-sends the recorded delivery without minting a new event id.
	got, body = nil, nil
	if err := svc.ReplayDelivery(ctx, orgID, deliveries[0].ID); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got == nil {
		t.Fatal("replay never reached the subscriber")
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.EventID != record.EventID+":feedback.created.v1" {
		t.Errorf("replay changed the event id to %q", payload.EventID)
	}
}

func TestDeliverRetryClassification(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	for _, c := range []struct {
		name       string
		status     int
		wantReplay bool
		subscribed []string
	}{
		{"5xx is retryable", 500, true, nil},
		{"4xx is permanent", 400, false, nil},
		{"unsubscribed event types are skipped", 500, false, []string{"feedback.note_added.v1"}},
	} {
		orgID := "org-" + uuid.NewString()
		sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
		}))
		ep, err := svc.CreateEndpoint(ctx, orgID, sub.URL, c.subscribed)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := svc.HandleStream(ctx, events.DynamoDBEvent{
			Records: []events.DynamoDBEventRecord{createdRecord(orgID)}})
		if err != nil {
			t.Fatalf("%s: handle stream: %v", c.name, err)
		}
		// A retryable failure asks Lambda to replay that one record; a
		// permanent one must not, or the whole batch comes back.
		if got := len(resp.BatchItemFailures) > 0; got != c.wantReplay {
			t.Errorf("%s: replay requested = %v, want %v", c.name, got, c.wantReplay)
		}
		deliveries, err := svc.ListDeliveries(ctx, orgID, ep.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.subscribed != nil && len(deliveries) != 0 {
			t.Errorf("%s: delivered %d despite not subscribing", c.name, len(deliveries))
		}
		if c.subscribed == nil && (len(deliveries) != 1 || deliveries[0].Status != "failed") {
			t.Errorf("%s: deliveries = %+v", c.name, deliveries)
		}
		sub.Close()
	}
}

func TestEndpointAutoPauses(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	orgID := "org-" + uuid.NewString()
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer sub.Close()

	ep, err := svc.CreateEndpoint(ctx, orgID, sub.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pauseAfter; i++ {
		if _, err := svc.HandleStream(ctx, events.DynamoDBEvent{
			Records: []events.DynamoDBEventRecord{createdRecord(orgID)}}); err != nil {
			t.Fatal(err)
		}
	}
	eps, err := svc.ListEndpoints(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	if eps[0].Status != "paused" {
		t.Fatalf("endpoint %s status = %q after %d failures, want paused", ep.ID, eps[0].Status, pauseAfter)
	}
	// A paused endpoint stops receiving; resuming clears the counter.
	if err := svc.SetEndpointStatus(ctx, orgID, ep.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if eps, _ = svc.ListEndpoints(ctx, orgID); eps[0].ConsecutiveFailures != 0 {
		t.Errorf("resume left the failure counter at %d", eps[0].ConsecutiveFailures)
	}
}

// One failing endpoint must not drag the rest of the batch back through
// Lambda's retry: only the record that failed is reported.
func TestBatchFailureIsPerRecord(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	badOrg := "org-" + uuid.NewString()
	goodOrg := "org-" + uuid.NewString()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer bad.Close()
	var goodHits int
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
	}))
	defer good.Close()
	if _, err := svc.CreateEndpoint(ctx, badOrg, bad.URL, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateEndpoint(ctx, goodOrg, good.URL, nil); err != nil {
		t.Fatal(err)
	}

	badRecord, goodRecord := createdRecord(badOrg), createdRecord(goodOrg)
	resp, err := svc.HandleStream(ctx, events.DynamoDBEvent{
		Records: []events.DynamoDBEventRecord{badRecord, goodRecord}})
	if err != nil {
		t.Fatalf("handle stream: %v", err)
	}
	if len(resp.BatchItemFailures) != 1 {
		t.Fatalf("BatchItemFailures = %+v, want exactly the failing record", resp.BatchItemFailures)
	}
	if got := resp.BatchItemFailures[0].ItemIdentifier; got != badRecord.Change.SequenceNumber {
		t.Errorf("reported %q, want the failing record %q", got, badRecord.Change.SequenceNumber)
	}
	if goodHits != 1 {
		t.Errorf("healthy subscriber called %d times, want 1", goodHits)
	}
}

// A retry re-derives the same record, so both the event id and its timestamp
// have to come out identical — consumers dedupe on them.
func TestDeriveIsStableAcrossRetries(t *testing.T) {
	record := createdRecord("org-1")
	first, second := derive(record), derive(record)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("derive returned %d and %d events, want 1", len(first), len(second))
	}
	if first[0] != second[0] {
		t.Errorf("re-deriving changed the event:\n first  = %+v\n second = %+v", first[0], second[0])
	}
}
