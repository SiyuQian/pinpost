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
	"strings"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

// event is one webhook-visible change derived from the table's change stream.
// The id is derived from the stream record id, so a Lambda retry re-sends the
// same event id and a consumer deduping on X-Pinpost-Event-Id sees one event.
type event struct {
	ID        string
	Type      string
	OrgID     string
	Payload   string
	CreatedAt time.Time
}

// HandleStream is the DynamoDB Streams consumer. Shard consumption gives each
// record to one invocation, which is what the old FOR UPDATE SKIP LOCKED lease
// was for; retries and the dead-letter queue are the event source's job.
//
// Failures are reported per record, not per batch. Returning a plain error
// would make Lambda retry all BatchSize records, re-POSTing every delivery
// that already succeeded to every healthy endpoint — one bad subscriber would
// amplify traffic to all the others. The event source sets
// FunctionResponseTypes: [ReportBatchItemFailures] so this response is honoured.
func (s *Service) HandleStream(ctx context.Context, e events.DynamoDBEvent) (events.DynamoDBEventResponse, error) {
	var resp events.DynamoDBEventResponse
	// One lookup per org per invocation: a batch is usually one org, and the
	// common case is an org with no endpoints at all.
	eps := map[string][]EndpointItem{}
	for _, record := range e.Records {
		recordFailed := false
		for _, ev := range derive(record) {
			if _, ok := eps[ev.OrgID]; !ok {
				found, err := s.endpoints(ctx, ev.OrgID)
				if err != nil {
					return resp, err
				}
				eps[ev.OrgID] = found
			}
			for i := range eps[ev.OrgID] {
				ep := eps[ev.OrgID][i]
				if ep.Status != "active" || !subscribed(ep, ev.Type) {
					continue
				}
				if err := s.deliver(ctx, &ep, ev, "", 0); err != nil {
					recordFailed = true
				}
			}
		}
		if recordFailed {
			resp.BatchItemFailures = append(resp.BatchItemFailures,
				events.DynamoDBBatchItemFailure{ItemIdentifier: record.Change.SequenceNumber})
		}
	}
	return resp, nil
}

func subscribed(ep EndpointItem, eventType string) bool {
	if len(ep.EventTypes) == 0 {
		return true // empty means "all events"
	}
	for _, t := range ep.EventTypes {
		if t == eventType {
			return true
		}
	}
	return false
}

// derive turns one item change into zero or more webhook events. This replaces
// the explicit outbox insert that used to ride along in every transaction.
func derive(r events.DynamoDBEventRecord) []event {
	img, old := r.Change.NewImage, r.Change.OldImage
	sk := str(img, "sk")
	orgID := str(img, "org_id")
	// From the record, not the clock: a Lambda retry re-derives the same record
	// and must produce a byte-identical event, id and timestamp alike. This is
	// when the change happened, which is what the old outbox row recorded — not
	// the item's own created_at, which for a MODIFY is the original submission.
	created := r.Change.ApproximateCreationDateTime.UTC()
	if created.IsZero() {
		created = time.Now().UTC()
	}
	mk := func(typ string, data map[string]any) event {
		b, _ := json.Marshal(data)
		return event{ID: r.EventID + ":" + typ, Type: typ, OrgID: orgID,
			Payload: string(b), CreatedAt: created}
	}

	switch {
	case strings.HasPrefix(sk, "FB#") && r.EventName == "INSERT":
		// source lets a consumer route a report the team filed itself away
		// from customer traffic — a Slack channel or an issue tracker usually
		// wants the two treated differently. Records written before the field
		// existed have no attribute, and str yields "" for them, so the
		// default is spelled out rather than shipped as an empty string.
		source := str(img, "source")
		if source == "" {
			source = "widget"
		}
		return []event{mk("feedback.created.v1", map[string]any{
			"feedback_id": str(img, "feedback_id"),
			"project_id":  str(img, "project_id"),
			"type":        str(img, "type"),
			"message":     str(img, "message"),
			"source":      source,
			"rating":      number(img, "rating"),
		})}
	case strings.HasPrefix(sk, "FB#") && r.EventName == "MODIFY":
		var out []event
		actor := str(img, "last_actor_id")
		if from, to := str(old, "status"), str(img, "status"); from != to {
			out = append(out, mk("feedback.status_changed.v1", map[string]any{
				"feedback_id": str(img, "feedback_id"), "from": from, "to": to, "actor_id": actor,
			}))
		}
		if from, to := str(old, "assignee_id"), str(img, "assignee_id"); from != to {
			out = append(out, mk("feedback.assigned.v1", map[string]any{
				"feedback_id": str(img, "feedback_id"), "assignee_id": to, "actor_id": actor,
			}))
		}
		return out
	case strings.HasPrefix(sk, "ACT#") && r.EventName == "INSERT" && str(img, "activity_type") == "note_added":
		return []event{mk("feedback.note_added.v1", map[string]any{
			"feedback_id": str(img, "feedback_id"),
			"actor_id":    str(img, "actor_id"),
		})}
	}
	return nil
}

func str(img map[string]events.DynamoDBAttributeValue, key string) string {
	v, ok := img[key]
	if !ok || v.DataType() != events.DataTypeString {
		return ""
	}
	return v.String()
}

func number(img map[string]events.DynamoDBAttributeValue, key string) any {
	v, ok := img[key]
	if !ok || v.DataType() != events.DataTypeNumber {
		return nil
	}
	n, err := v.Integer()
	if err != nil {
		return nil
	}
	return n
}

// Sign computes the webhook signature: hex(HMAC-SHA256(secret, "<ts>.<body>")).
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// deliver posts one event to one endpoint and records the attempt. A returned
// error means "retryable": the caller surfaces it so the Lambda event source
// retries the batch and eventually parks it in the dead-letter queue.
func (s *Service) deliver(ctx context.Context, ep *EndpointItem, ev event, deliveryID string, attempt int) error {
	body, _ := json.Marshal(map[string]any{
		"event_id":   ev.ID,
		"event_type": ev.Type,
		"created_at": ev.CreatedAt.UTC().Format(time.RFC3339Nano),
		"data":       json.RawMessage(ev.Payload),
	})
	if deliveryID == "" {
		deliveryID = uuid.Must(uuid.NewV7()).String()
	}
	now := time.Now().UTC()
	rec := &DeliveryItem{
		Key:            deliveryKey(deliveryID),
		GSI1PK:         "WH#" + ep.EndpointID,
		GSI1SK:         "DLV#" + deliveryID,
		DeliveryID:     deliveryID,
		EndpointID:     ep.EndpointID,
		OrganizationID: ep.OrganizationID,
		EventID:        ev.ID,
		EventType:      ev.Type,
		Payload:        ev.Payload,
		Attempt:        attempt + 1,
		CreatedAt:      now,
		UpdatedAt:      now,
		ExpiresAt:      platform.TTL(deliveryTTL),
	}

	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		rec.Status, rec.ResponseBody = "failed", err.Error()
		s.record(ctx, rec, ep, false)
		return nil // a malformed URL will never succeed; no point retrying
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pinpost-Event-Id", ev.ID)
	req.Header.Set("X-Pinpost-Delivery-Id", deliveryID)
	req.Header.Set("X-Pinpost-Event-Type", ev.Type)
	req.Header.Set("X-Pinpost-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Pinpost-Signature", "v1="+Sign(ep.Secret, ts, body))

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())
	rec.LatencyMs = &latency
	if err != nil {
		rec.Status, rec.ResponseBody = "failed", err.Error()
		s.record(ctx, rec, ep, false)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	rec.ResponseStatus, rec.ResponseBody = &resp.StatusCode, string(respBody)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		rec.Status = "succeeded"
		s.record(ctx, rec, ep, true)
		return nil
	}
	rec.Status = "failed"
	s.record(ctx, rec, ep, false)
	// Retry 5xx, 408, 429; other 4xx are permanent (design §11.2).
	if resp.StatusCode >= 500 || resp.StatusCode == 408 || resp.StatusCode == 429 {
		return fmt.Errorf("webhook %s returned %d", ep.URL, resp.StatusCode)
	}
	return nil
}

// record stores the attempt and moves the endpoint's failure counter, which
// auto-pauses an endpoint that keeps rejecting deliveries.
func (s *Service) record(ctx context.Context, rec *DeliveryItem, ep *EndpointItem, ok bool) {
	if err := s.Store.Put(ctx, rec); err != nil {
		slog.Error("delivery record failed", "error", err)
	}
	k := endpointKey(ep.OrganizationID, ep.EndpointID)
	if ok {
		s.updateEndpoint(ctx, k, "SET consecutive_failures = :zero", nil,
			map[string]types.AttributeValue{":zero": platform.N("0")})
		return
	}
	res, err := s.Store.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &s.Store.Table,
		Key:                       platform.KeyAV(k.PK, k.SK),
		UpdateExpression:          aws.String("ADD consecutive_failures :one"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":one": platform.N("1")},
		ReturnValues:              types.ReturnValueUpdatedNew,
	})
	if err != nil {
		slog.Error("endpoint counter update failed", "error", err)
		return
	}
	n, _ := res.Attributes["consecutive_failures"].(*types.AttributeValueMemberN)
	if n == nil {
		return
	}
	if failures, _ := strconv.Atoi(n.Value); failures >= pauseAfter {
		s.updateEndpoint(ctx, k, "SET #s = :paused", map[string]string{"#s": "status"},
			map[string]types.AttributeValue{":paused": platform.S("paused")})
	}
}

func (s *Service) updateEndpoint(ctx context.Context, k platform.Key, expr string,
	names map[string]string, values map[string]types.AttributeValue) {
	if _, err := s.Store.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &s.Store.Table,
		Key:                       platform.KeyAV(k.PK, k.SK),
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	}); err != nil {
		slog.Error("endpoint update failed", "error", err)
	}
}

func unmarshalList(items []map[string]types.AttributeValue, out any) error {
	return attributevalue.UnmarshalListOfMaps(items, out)
}
