package outbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// retryLadder is the wait after each failed attempt: seven attempts over
// about 20 hours, then the delivery is marked failed.
var retryLadder = []time.Duration{
	time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour,
}

// lease is how long a claimed delivery stays invisible to other dispatchers.
// Sending takes at most the client's 10s timeout; a dispatcher that dies
// mid-send leaves the row to come back after this.
const lease = 2 * time.Minute

// due is one claimed delivery and the endpoint it goes to.
type due struct {
	ID         string    `db:"id"`
	EventID    string    `db:"event_id"`
	EventType  string    `db:"event_type"`
	Payload    string    `db:"payload"`
	Attempt    int       `db:"attempt"`
	CreatedAt  time.Time `db:"created_at"`
	EndpointID string    `db:"endpoint_id"`
	URL        string    `db:"url"`
	Secret     string    `db:"secret"`
}

const dueCols = `d.id, d.event_id, d.event_type, d.payload, d.attempt, d.created_at,
	e.id AS endpoint_id, e.url, e.secret`

// DispatchDue sends every delivery that is due, up to ten at a time, and
// reports how many it claimed.
//
// Claiming pushes next_attempt_at forward by the lease and commits before any
// request goes out, so no transaction or connection is held across a slow
// subscriber, and FOR UPDATE SKIP LOCKED keeps concurrent dispatchers (other
// replicas) off the same rows. Delivery is at-least-once: a dispatcher that
// dies after sending but before recording sends again once the lease ends.
// Deliveries of a paused endpoint wait until it is resumed.
func (s *Service) DispatchDue(ctx context.Context) (int, error) {
	rows, _ := s.DB.Query(ctx, `UPDATE webhook_deliveries d SET next_attempt_at = now() + $1::interval
		FROM webhook_endpoints e
		WHERE e.id = d.endpoint_id AND d.id IN (
			SELECT dd.id FROM webhook_deliveries dd
			JOIN webhook_endpoints ee ON ee.id = dd.endpoint_id AND ee.status = 'active'
			WHERE dd.status = 'pending' AND dd.next_attempt_at <= now()
			ORDER BY dd.next_attempt_at LIMIT 10
			FOR UPDATE OF dd SKIP LOCKED)
		RETURNING `+dueCols, lease.String())
	batch, err := pgx.CollectRows(rows, pgx.RowToStructByName[due])
	if err != nil {
		return 0, err
	}
	// Concurrently, so one slow subscriber does not hold up the others.
	var wg sync.WaitGroup
	for _, d := range batch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.send(ctx, d, true)
		}()
	}
	wg.Wait()
	return len(batch), nil
}

// Sign computes the webhook signature: hex(HMAC-SHA256(secret, "<ts>.<body>")).
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// result is what one attempt observed.
type result struct {
	ok        bool
	retryable bool
	status    *int
	body      string
	latency   *int
}

// send posts one delivery and records the outcome. With retry set, a
// retryable failure (5xx, 408, 429, network) is rescheduled along
// retryLadder; anything else is final.
func (s *Service) send(ctx context.Context, d due, retry bool) {
	r := s.post(ctx, d)
	attempt := d.Attempt + 1
	status, next := "failed", (*time.Time)(nil)
	switch {
	case r.ok:
		status = "succeeded"
	case r.retryable && retry && attempt <= len(retryLadder):
		status = "pending"
		at := time.Now().Add(retryLadder[attempt-1])
		next = &at
	}
	if _, err := s.DB.Exec(ctx, `UPDATE webhook_deliveries
		SET status = $2, attempt = $3, next_attempt_at = $4, response_status = $5,
		    response_body = $6, latency_ms = $7, updated_at = now()
		WHERE id = $1`, d.ID, status, attempt, next, r.status, r.body, r.latency); err != nil {
		slog.Error("delivery record failed", "delivery_id", d.ID, "error", err)
	}
	// The failure counter auto-pauses an endpoint that keeps rejecting
	// deliveries; a success resets it.
	sql := `UPDATE webhook_endpoints SET consecutive_failures = 0 WHERE id = $1`
	if !r.ok {
		sql = `UPDATE webhook_endpoints SET consecutive_failures = consecutive_failures + 1,
			status = CASE WHEN consecutive_failures + 1 >= ` + strconv.Itoa(pauseAfter) + ` THEN 'paused' ELSE status END
			WHERE id = $1`
	}
	if _, err := s.DB.Exec(ctx, sql, d.EndpointID); err != nil {
		slog.Error("endpoint counter update failed", "endpoint_id", d.EndpointID, "error", err)
	}
}

// post makes the HTTP request for one delivery.
func (s *Service) post(ctx context.Context, d due) result {
	body, _ := json.Marshal(map[string]any{
		"event_id":   d.EventID,
		"event_type": d.EventType,
		"created_at": d.CreatedAt.UTC().Format(time.RFC3339Nano),
		"data":       json.RawMessage(d.Payload),
	})
	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
	if err != nil {
		return result{body: err.Error()} // a malformed URL will never succeed
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pinpost-Event-Id", d.EventID)
	req.Header.Set("X-Pinpost-Delivery-Id", d.ID)
	req.Header.Set("X-Pinpost-Event-Type", d.EventType)
	req.Header.Set("X-Pinpost-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Pinpost-Signature", "v1="+Sign(d.Secret, ts, body))

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		return result{retryable: true, body: err.Error(), latency: &latency}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	code := resp.StatusCode
	return result{
		ok: code >= 200 && code < 300,
		// Retry 5xx, 408, 429; other 4xx are permanent (design §11.2).
		retryable: code >= 500 || code == 408 || code == 429,
		status:    &code, body: string(respBody), latency: &latency,
	}
}
