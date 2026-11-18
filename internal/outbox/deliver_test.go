package outbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type harness struct {
	svc   *Service
	db    *pgxpool.Pool
	orgID string
}

func newHarness(t *testing.T) harness {
	t.Helper()
	db := pgtest.New(t)
	orgID, _ := pgtest.Tenant(t, db)
	return harness{svc: &Service{DB: db}, db: db, orgID: orgID}
}

// subscriber is a stand-in webhook consumer answering with status().
func subscriber(t *testing.T, status func() int) (*httptest.Server, chan *http.Request, chan []byte) {
	t.Helper()
	reqs, bodies := make(chan *http.Request, 100), make(chan []byte, 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqs <- r
		bodies <- b
		w.WriteHeader(status())
	}))
	t.Cleanup(srv.Close)
	return srv, reqs, bodies
}

func always(code int) func() int { return func() int { return code } }

func (h harness) endpoint(t *testing.T, url string, types ...string) *Endpoint {
	t.Helper()
	ep, err := h.svc.CreateEndpoint(context.Background(), h.orgID, url, types)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func (h harness) enqueue(t *testing.T, eventType string) {
	t.Helper()
	if err := pgx.BeginFunc(context.Background(), h.db, func(tx pgx.Tx) error {
		return Enqueue(context.Background(), tx, h.orgID, eventType, map[string]any{"feedback_id": "fb-1"})
	}); err != nil {
		t.Fatal(err)
	}
}

func (h harness) dispatch(t *testing.T) int {
	t.Helper()
	n, err := h.svc.DispatchDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// makeDue pulls every pending delivery's next attempt to now, standing in for
// the passage of time between retries.
func (h harness) makeDue(t *testing.T) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(),
		`UPDATE webhook_deliveries SET next_attempt_at = now() WHERE status = 'pending'`); err != nil {
		t.Fatal(err)
	}
}

func (h harness) deliveries(t *testing.T, endpointID string) []Delivery {
	t.Helper()
	ds, err := h.svc.ListDeliveries(context.Background(), h.orgID, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func TestDeliverSignsAndRecords(t *testing.T) {
	h := newHarness(t)
	sub, reqs, bodies := subscriber(t, always(200))
	ep := h.endpoint(t, sub.URL)
	h.enqueue(t, "feedback.created.v1")

	if n := h.dispatch(t); n != 1 {
		t.Fatalf("dispatched %d, want 1", n)
	}
	got, body := <-reqs, <-bodies
	ts, _ := strconv.ParseInt(got.Header.Get("X-Pinpost-Timestamp"), 10, 64)
	if want := "v1=" + Sign(ep.Secret, ts, body); got.Header.Get("X-Pinpost-Signature") != want {
		t.Errorf("signature = %q, want %q", got.Header.Get("X-Pinpost-Signature"), want)
	}
	var env struct {
		EventID   string          `json:"event_id"`
		EventType string          `json:"event_type"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.EventType != "feedback.created.v1" ||
		env.EventID != got.Header.Get("X-Pinpost-Event-Id") || string(env.Data) == "" {
		t.Fatalf("body = %s (err %v)", body, err)
	}
	ds := h.deliveries(t, ep.ID)
	if len(ds) != 1 || ds[0].Status != "succeeded" || ds[0].Attempt != 1 || ds[0].NextAttemptAt != nil {
		t.Fatalf("deliveries = %+v", ds)
	}
}

func TestEnqueueRespectsSubscriptionsAndPause(t *testing.T) {
	h := newHarness(t)
	sub, _, _ := subscriber(t, always(200))
	all := h.endpoint(t, sub.URL)
	notes := h.endpoint(t, sub.URL, "feedback.note_added.v1")
	paused := h.endpoint(t, sub.URL)
	if err := h.svc.SetEndpointStatus(context.Background(), h.orgID, paused.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	h.enqueue(t, "feedback.created.v1")
	for ep, want := range map[string]int{all.ID: 1, notes.ID: 0, paused.ID: 0} {
		if got := len(h.deliveries(t, ep)); got != want {
			t.Errorf("endpoint %s got %d deliveries, want %d", ep, got, want)
		}
	}
}

// An event exists if and only if the change it reports commits.
func TestEnqueueRollsBackWithItsTransaction(t *testing.T) {
	h := newHarness(t)
	sub, _, _ := subscriber(t, always(200))
	ep := h.endpoint(t, sub.URL)
	_ = pgx.BeginFunc(context.Background(), h.db, func(tx pgx.Tx) error {
		if err := Enqueue(context.Background(), tx, h.orgID, "feedback.created.v1", nil); err != nil {
			t.Fatal(err)
		}
		return context.Canceled // any error rolls back
	})
	if ds := h.deliveries(t, ep.ID); len(ds) != 0 {
		t.Fatalf("a rolled-back event left %d deliveries", len(ds))
	}
}

func TestDeliverRetryClassification(t *testing.T) {
	for _, c := range []struct {
		status int
		want   string
	}{
		{500, "pending"}, {503, "pending"}, {408, "pending"}, {429, "pending"},
		{400, "failed"}, {404, "failed"}, {410, "failed"},
	} {
		t.Run(strconv.Itoa(c.status), func(t *testing.T) {
			h := newHarness(t)
			sub, _, _ := subscriber(t, always(c.status))
			ep := h.endpoint(t, sub.URL)
			h.enqueue(t, "feedback.created.v1")
			h.dispatch(t)
			d := h.deliveries(t, ep.ID)[0]
			if d.Status != c.want {
				t.Fatalf("status %d recorded as %q, want %q", c.status, d.Status, c.want)
			}
			if c.want == "pending" && (d.NextAttemptAt == nil || time.Until(*d.NextAttemptAt) < 30*time.Second) {
				t.Fatalf("retryable failure scheduled at %v, want about a minute out", d.NextAttemptAt)
			}
		})
	}
}

// A retry re-sends the same event id, and the ladder ends in "failed".
func TestRetriesKeepTheEventIDAndGiveUp(t *testing.T) {
	h := newHarness(t)
	sub, reqs, _ := subscriber(t, always(500))
	ep := h.endpoint(t, sub.URL)
	h.enqueue(t, "feedback.created.v1")

	var ids []string
	for i := 0; i <= len(retryLadder); i++ {
		h.makeDue(t)
		if n := h.dispatch(t); n != 1 {
			t.Fatalf("attempt %d dispatched %d deliveries", i+1, n)
		}
		ids = append(ids, (<-reqs).Header.Get("X-Pinpost-Event-Id"))
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("event ids changed across retries: %v", ids)
		}
	}
	d := h.deliveries(t, ep.ID)[0]
	if d.Status != "failed" || d.Attempt != len(retryLadder)+1 {
		t.Fatalf("after the ladder: %+v, want failed after %d attempts", d, len(retryLadder)+1)
	}
	h.makeDue(t)
	if n := h.dispatch(t); n != 0 {
		t.Fatalf("a failed delivery was sent again")
	}
}

func TestEndpointAutoPausesAndResumeDeliversTheBacklog(t *testing.T) {
	h := newHarness(t)
	var code atomic.Int32
	code.Store(500)
	sub, _, _ := subscriber(t, func() int { return int(code.Load()) })
	ep := h.endpoint(t, sub.URL)
	for i := 0; i < pauseAfter; i++ {
		h.enqueue(t, "feedback.created.v1")
	}
	h.dispatch(t) // ten deliveries, ten failures

	eps, _ := h.svc.ListEndpoints(context.Background(), h.orgID)
	if eps[0].Status != "paused" || eps[0].ConsecutiveFailures != pauseAfter {
		t.Fatalf("endpoint = %+v, want paused after %d failures", eps[0], pauseAfter)
	}
	h.makeDue(t)
	if n := h.dispatch(t); n != 0 {
		t.Fatalf("a paused endpoint was sent %d deliveries", n)
	}

	code.Store(200)
	if err := h.svc.SetEndpointStatus(context.Background(), h.orgID, ep.ID, "active"); err != nil {
		t.Fatal(err)
	}
	h.makeDue(t)
	if n := h.dispatch(t); n != pauseAfter {
		t.Fatalf("resume delivered %d of the %d queued deliveries", n, pauseAfter)
	}
}

// One bad subscriber must not hold back or re-send to a healthy one.
func TestOneFailingEndpointDoesNotAffectOthers(t *testing.T) {
	h := newHarness(t)
	bad, _, _ := subscriber(t, always(500))
	var goodHits atomic.Int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
	}))
	t.Cleanup(good.Close)
	badEp, goodEp := h.endpoint(t, bad.URL), h.endpoint(t, good.URL)
	h.enqueue(t, "feedback.created.v1")
	h.dispatch(t)
	h.makeDue(t)
	h.dispatch(t)

	if goodHits.Load() != 1 {
		t.Fatalf("healthy endpoint received %d requests, want exactly 1", goodHits.Load())
	}
	if d := h.deliveries(t, goodEp.ID)[0]; d.Status != "succeeded" {
		t.Fatalf("healthy delivery = %+v", d)
	}
	if d := h.deliveries(t, badEp.ID)[0]; d.Status != "pending" || d.Attempt != 2 {
		t.Fatalf("failing delivery = %+v, want pending after 2 attempts", d)
	}
}

// Replicas dispatching at once must not send one delivery twice.
func TestConcurrentDispatchersSendOnce(t *testing.T) {
	h := newHarness(t)
	var hits atomic.Int32
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(50 * time.Millisecond)
	}))
	t.Cleanup(sub.Close)
	h.endpoint(t, sub.URL)
	for i := 0; i < 5; i++ {
		h.enqueue(t, "feedback.created.v1")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.svc.DispatchDue(context.Background())
		}()
	}
	wg.Wait()
	if hits.Load() != 5 {
		t.Fatalf("subscriber received %d requests for 5 deliveries", hits.Load())
	}
}

func TestReplayResendsWithTheSameEventID(t *testing.T) {
	h := newHarness(t)
	sub, reqs, _ := subscriber(t, always(200))
	ep := h.endpoint(t, sub.URL)
	h.enqueue(t, "feedback.created.v1")
	h.dispatch(t)
	first := (<-reqs).Header.Get("X-Pinpost-Event-Id")
	d := h.deliveries(t, ep.ID)[0]
	if err := h.svc.ReplayDelivery(context.Background(), h.orgID, d.ID); err != nil {
		t.Fatal(err)
	}
	if again := (<-reqs).Header.Get("X-Pinpost-Event-Id"); again != first {
		t.Fatalf("replay event id %q, want %q", again, first)
	}
	if err := h.svc.ReplayDelivery(context.Background(), "00000000-0000-0000-0000-000000000000", d.ID); err == nil {
		t.Fatal("another organization replayed this delivery")
	}
}

func TestSignIsStable(t *testing.T) {
	// A consumer verifies with "<timestamp>.<body>"; drift here breaks every
	// existing subscriber silently.
	if got := Sign("whsec_test", 1700000000, []byte(`{"a":1}`)); got !=
		"38877139021993b830af32feea6e18a8da83eb2f6e49ee50bd9e4cf4ca4d3789" {
		t.Errorf("signature changed: %s", got)
	}
}

// Postgres text rejects invalid UTF-8 and NUL; the outcome must still be recorded.
func TestDeliverRecordsNonUTF8ResponseBody(t *testing.T) {
	body := "\x00" + strings.Repeat("a", 1022) + "世" + "tail"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	h := newHarness(t)
	ep := h.endpoint(t, srv.URL)
	h.enqueue(t, "feedback.created.v1")
	h.dispatch(t)
	d := h.deliveries(t, ep.ID)[0]
	if d.Status != "succeeded" || d.Attempt != 1 {
		t.Fatalf("status=%s attempt=%d, want succeeded/1", d.Status, d.Attempt)
	}
	if !utf8.ValidString(d.ResponseBody) || strings.Contains(d.ResponseBody, "\x00") {
		t.Fatalf("response_body not clean: %q", d.ResponseBody)
	}
	if !strings.HasPrefix(d.ResponseBody, strings.Repeat("a", 1022)) {
		t.Fatalf("response_body content lost")
	}
}
