// Package outbox owns webhook endpoint configuration and delivery. Business
// writes call Enqueue inside their own transaction, which inserts one pending
// delivery per subscribed endpoint; the dispatcher (deliver.go) sends them.
package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deliveryRetention keeps delivery history replayable for a month.
const deliveryRetention = 30 * 24 * time.Hour

// pauseAfter is the consecutive-failure threshold that auto-pauses an endpoint.
const pauseAfter = 10

var validEventTypes = map[string]bool{
	"feedback.created.v1":        true,
	"feedback.status_changed.v1": true,
	"feedback.assigned.v1":       true,
	"feedback.note_added.v1":     true,
}

// Service owns webhook endpoint configuration and delivery history.
type Service struct {
	DB *pgxpool.Pool
	// Client sends webhook requests; nil means a 10s-timeout default.
	Client *http.Client
}

// Enqueue records one event for every active endpoint of the organization that
// subscribes to it, inside the caller's transaction: the event exists if and
// only if the change it describes commits. Paused endpoints get nothing.
func Enqueue(ctx context.Context, tx pgx.Tx, orgID, eventType string, data map[string]any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO webhook_deliveries
		(id, endpoint_id, organization_id, event_id, event_type, payload)
		SELECT gen_random_uuid(), e.id, e.organization_id, $2, $3, $4
		FROM webhook_endpoints e
		WHERE e.organization_id = $1 AND e.status = 'active'
		  AND (cardinality(e.event_types) = 0 OR $3 = ANY (e.event_types))`,
		orgID, uuid.Must(uuid.NewV7()).String(), eventType, string(payload))
	return err
}

// ---- endpoints ----

type Endpoint struct {
	ID                  string    `json:"id" db:"id"`
	URL                 string    `json:"url" db:"url"`
	EventTypes          []string  `json:"event_types" db:"event_types"`
	Status              string    `json:"status" db:"status"`
	ConsecutiveFailures int       `json:"consecutive_failures" db:"consecutive_failures"`
	CreatedAt           time.Time `json:"created_at" db:"created_at"`
	// Secret is only populated on create; list responses omit it.
	Secret string `json:"secret,omitempty" db:"-"`
}

func (s *Service) CreateEndpoint(ctx context.Context, orgID, rawURL string, eventTypes []string) (*Endpoint, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, platform.ValidationErr(map[string]string{"url": "must be a valid http(s) URL"})
	}
	if eventTypes == nil {
		eventTypes = []string{}
	}
	for _, et := range eventTypes {
		if !validEventTypes[et] {
			return nil, platform.ValidationErr(map[string]string{"event_types": "unknown event type " + et})
		}
	}
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM webhook_endpoints WHERE organization_id = $1`,
		orgID).Scan(&n); err != nil {
		return nil, err
	}
	// ponytail: count-then-insert can overshoot by a racing request or two.
	if n >= 10 {
		return nil, platform.Errf(422, "limit_exceeded", "At most 10 webhook endpoints per organization")
	}
	raw := make([]byte, 32)
	rand.Read(raw)
	ep := &Endpoint{ID: uuid.Must(uuid.NewV7()).String(), URL: rawURL, EventTypes: eventTypes,
		Status: "active", Secret: "whsec_" + hex.EncodeToString(raw)}
	if err := s.DB.QueryRow(ctx, `INSERT INTO webhook_endpoints (id, organization_id, url, secret, event_types)
		VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		ep.ID, orgID, ep.URL, ep.Secret, ep.EventTypes).Scan(&ep.CreatedAt); err != nil {
		return nil, err
	}
	return ep, nil
}

func (s *Service) ListEndpoints(ctx context.Context, orgID string) ([]Endpoint, error) {
	rows, _ := s.DB.Query(ctx, `SELECT id, url, event_types, status, consecutive_failures, created_at
		FROM webhook_endpoints WHERE organization_id = $1 ORDER BY id`, orgID)
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[Endpoint])
	if out == nil {
		out = []Endpoint{}
	}
	return out, err
}

func (s *Service) DeleteEndpoint(ctx context.Context, orgID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM webhook_endpoints WHERE organization_id = $1 AND id = $2`, orgID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return platform.Errf(404, "not_found", "Webhook endpoint not found")
	}
	return err
}

// SetEndpointStatus resumes a paused endpoint (or pauses one manually);
// resuming resets the failure counter. Deliveries queued while it was paused
// are sent once it resumes.
func (s *Service) SetEndpointStatus(ctx context.Context, orgID, id, status string) error {
	if status != "active" && status != "paused" {
		return platform.ValidationErr(map[string]string{"status": "must be active or paused"})
	}
	tag, err := s.DB.Exec(ctx, `UPDATE webhook_endpoints
		SET status = $3, consecutive_failures = CASE WHEN $3 = 'active' THEN 0 ELSE consecutive_failures END
		WHERE organization_id = $1 AND id = $2`, orgID, id, status)
	if err == nil && tag.RowsAffected() == 0 {
		return platform.Errf(404, "not_found", "Webhook endpoint not found")
	}
	return err
}

// ---- deliveries ----

type Delivery struct {
	ID             string     `json:"id" db:"id"`
	EventID        string     `json:"event_id" db:"event_id"`
	EventType      string     `json:"event_type" db:"event_type"`
	Attempt        int        `json:"attempt" db:"attempt"`
	Status         string     `json:"status" db:"status"`
	NextAttemptAt  *time.Time `json:"next_attempt_at" db:"next_attempt_at"`
	ResponseStatus *int       `json:"response_status" db:"response_status"`
	ResponseBody   string     `json:"response_body" db:"response_body"`
	LatencyMs      *int       `json:"latency_ms" db:"latency_ms"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at" db:"updated_at"`
}

func (s *Service) ListDeliveries(ctx context.Context, orgID, endpointID string) ([]Delivery, error) {
	var exists bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM webhook_endpoints
		WHERE organization_id = $1 AND id = $2)`, orgID, endpointID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, platform.Errf(404, "not_found", "Webhook endpoint not found")
	}
	rows, _ := s.DB.Query(ctx, `SELECT id, event_id, event_type, attempt, status, next_attempt_at,
			response_status, response_body, latency_ms, created_at, updated_at
		FROM webhook_deliveries WHERE organization_id = $1 AND endpoint_id = $2
		ORDER BY created_at DESC, id LIMIT 50`, orgID, endpointID)
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[Delivery])
	if out == nil {
		out = []Delivery{}
	}
	return out, err
}

// ReplayDelivery re-sends a finished delivery inline with its original event
// id, so a consumer that deduped on X-Pinpost-Event-Id still sees the same
// event. A failed replay is recorded but not retried: it was a manual send.
func (s *Service) ReplayDelivery(ctx context.Context, orgID, deliveryID string) error {
	rows, _ := s.DB.Query(ctx, `SELECT `+dueCols+`
		FROM webhook_deliveries d JOIN webhook_endpoints e ON e.id = d.endpoint_id
		WHERE d.organization_id = $1 AND d.id = $2 AND d.status IN ('succeeded', 'failed')`, orgID, deliveryID)
	d, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[due])
	if errors.Is(err, pgx.ErrNoRows) {
		return platform.Errf(404, "not_found", "Delivery not found or still in progress")
	}
	if err != nil {
		return err
	}
	s.send(ctx, d, false)
	return nil
}

// SweepDeliveries deletes delivery history past its retention.
func (s *Service) SweepDeliveries(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM webhook_deliveries WHERE created_at < $1`,
		time.Now().Add(-deliveryRetention))
	return err
}
