// Package outbox owns webhook endpoint configuration and delivery. The
// transactional outbox table and its polling worker are gone: DynamoDB Streams
// is the change feed, so events are derived from the item changes themselves
// (see stream.go) and delivery records exist only so a delivery can be replayed.
package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

// deliveryTTL keeps delivery history replayable for a month, then lets
// DynamoDB reclaim it.
const deliveryTTL = 30 * 24 * time.Hour

// pauseAfter is the consecutive-failure threshold that auto-pauses an endpoint.
const pauseAfter = 10

var validEventTypes = map[string]bool{
	"feedback.created.v1":        true,
	"feedback.status_changed.v1": true,
	"feedback.assigned.v1":       true,
	"feedback.note_added.v1":     true,
}

type EndpointItem struct {
	platform.Key
	EndpointID          string    `dynamodbav:"endpoint_id"`
	OrganizationID      string    `dynamodbav:"org_id"`
	URL                 string    `dynamodbav:"url"`
	Secret              string    `dynamodbav:"secret"`
	EventTypes          []string  `dynamodbav:"event_types"`
	Status              string    `dynamodbav:"status"`
	ConsecutiveFailures int       `dynamodbav:"consecutive_failures"`
	CreatedAt           time.Time `dynamodbav:"created_at"`
}

func endpointKey(orgID, id string) platform.Key {
	return platform.Key{PK: "ORG#" + orgID, SK: "WH#" + id}
}

// DeliveryItem records one attempt so the replay endpoint keeps working; GSI1
// lists them per endpoint.
type DeliveryItem struct {
	platform.Key
	GSI1PK         string    `dynamodbav:"gsi1pk"`
	GSI1SK         string    `dynamodbav:"gsi1sk"`
	DeliveryID     string    `dynamodbav:"delivery_id"`
	EndpointID     string    `dynamodbav:"endpoint_id"`
	OrganizationID string    `dynamodbav:"org_id"`
	EventID        string    `dynamodbav:"event_id"`
	EventType      string    `dynamodbav:"event_type"`
	Payload        string    `dynamodbav:"payload"`
	Attempt        int       `dynamodbav:"attempt"`
	Status         string    `dynamodbav:"status"`
	ResponseStatus *int      `dynamodbav:"response_status"`
	ResponseBody   string    `dynamodbav:"response_body"`
	LatencyMs      *int      `dynamodbav:"latency_ms"`
	CreatedAt      time.Time `dynamodbav:"created_at"`
	UpdatedAt      time.Time `dynamodbav:"updated_at"`
	ExpiresAt      int64     `dynamodbav:"expires_at"`
}

func deliveryKey(id string) platform.Key {
	return platform.Key{PK: "DLV#" + id, SK: platform.Meta}
}

// Service owns webhook endpoint configuration and delivery history.
type Service struct {
	Store *platform.Store
	// Client sends webhook requests; nil means a 10s-timeout default.
	Client *http.Client
}

// ---- endpoints ----

type Endpoint struct {
	ID                  string    `json:"id"`
	URL                 string    `json:"url"`
	EventTypes          []string  `json:"event_types"`
	Status              string    `json:"status"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	CreatedAt           time.Time `json:"created_at"`
	// Secret is only populated on create; list responses omit it.
	Secret string `json:"secret,omitempty"`
}

func (s *Service) endpoints(ctx context.Context, orgID string) ([]EndpointItem, error) {
	var items []EndpointItem
	err := s.Store.QueryAll(ctx, &dynamodb.QueryInput{
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": platform.S("ORG#" + orgID), ":prefix": platform.S("WH#"),
		},
	}, &items)
	return items, err
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
	existing, err := s.endpoints(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if len(existing) >= 10 {
		return nil, platform.Errf(422, "limit_exceeded", "At most 10 webhook endpoints per organization")
	}
	raw := make([]byte, 32)
	rand.Read(raw)
	item := EndpointItem{
		EndpointID: uuid.Must(uuid.NewV7()).String(), OrganizationID: orgID,
		URL: rawURL, Secret: "whsec_" + hex.EncodeToString(raw), EventTypes: eventTypes,
		Status: "active", CreatedAt: time.Now().UTC(),
	}
	item.Key = endpointKey(orgID, item.EndpointID)
	if err := s.Store.Put(ctx, &item); err != nil {
		return nil, err
	}
	return &Endpoint{ID: item.EndpointID, URL: item.URL, EventTypes: item.EventTypes,
		Status: item.Status, CreatedAt: item.CreatedAt, Secret: item.Secret}, nil
}

func (s *Service) ListEndpoints(ctx context.Context, orgID string) ([]Endpoint, error) {
	items, err := s.endpoints(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := []Endpoint{}
	for _, e := range items {
		types := e.EventTypes
		if types == nil {
			types = []string{}
		}
		out = append(out, Endpoint{ID: e.EndpointID, URL: e.URL, EventTypes: types,
			Status: e.Status, ConsecutiveFailures: e.ConsecutiveFailures, CreatedAt: e.CreatedAt})
	}
	return out, nil
}

func (s *Service) DeleteEndpoint(ctx context.Context, orgID, id string) error {
	k := endpointKey(orgID, id)
	_, err := s.Store.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:           &s.Store.Table,
		Key:                 platform.KeyAV(k.PK, k.SK),
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if platform.ConditionFailed(err) {
		return platform.Errf(404, "not_found", "Webhook endpoint not found")
	}
	return err
}

// SetEndpointStatus resumes a paused endpoint (or pauses one manually);
// resuming resets the failure counter.
func (s *Service) SetEndpointStatus(ctx context.Context, orgID, id, status string) error {
	if status != "active" && status != "paused" {
		return platform.ValidationErr(map[string]string{"status": "must be active or paused"})
	}
	k := endpointKey(orgID, id)
	update := "SET #s = :status"
	values := map[string]types.AttributeValue{":status": platform.S(status)}
	if status == "active" {
		update += ", consecutive_failures = :zero"
		values[":zero"] = platform.N("0")
	}
	_, err := s.Store.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &s.Store.Table,
		Key:                       platform.KeyAV(k.PK, k.SK),
		UpdateExpression:          aws.String(update),
		ConditionExpression:       aws.String("attribute_exists(pk)"),
		ExpressionAttributeNames:  map[string]string{"#s": "status"},
		ExpressionAttributeValues: values,
	})
	if platform.ConditionFailed(err) {
		return platform.Errf(404, "not_found", "Webhook endpoint not found")
	}
	return err
}

// ---- deliveries ----

type Delivery struct {
	ID             string     `json:"id"`
	EventID        string     `json:"event_id"`
	EventType      string     `json:"event_type"`
	Attempt        int        `json:"attempt"`
	Status         string     `json:"status"`
	NextAttemptAt  *time.Time `json:"next_attempt_at"`
	ResponseStatus *int       `json:"response_status"`
	ResponseBody   string     `json:"response_body"`
	LatencyMs      *int       `json:"latency_ms"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (s *Service) ListDeliveries(ctx context.Context, orgID, endpointID string) ([]Delivery, error) {
	k := endpointKey(orgID, endpointID)
	var ep EndpointItem
	found, err := s.Store.Get(ctx, k.PK, k.SK, &ep)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, platform.Errf(404, "not_found", "Webhook endpoint not found")
	}
	res, err := s.Store.DB.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.Store.Table,
		IndexName:              aws.String("GSI1"),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": platform.S("WH#" + endpointID),
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(50),
	})
	if err != nil {
		return nil, err
	}
	var items []DeliveryItem
	if err := unmarshalList(res.Items, &items); err != nil {
		return nil, err
	}
	out := []Delivery{}
	for _, d := range items {
		out = append(out, Delivery{ID: d.DeliveryID, EventID: d.EventID, EventType: d.EventType,
			Attempt: d.Attempt, Status: d.Status, ResponseStatus: d.ResponseStatus,
			ResponseBody: d.ResponseBody, LatencyMs: d.LatencyMs,
			CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt})
	}
	return out, nil
}

// ReplayDelivery re-sends a finished delivery with its original event id, so a
// consumer that deduped on X-Pinpost-Event-Id still sees the same event.
// Without a worker to queue it for, the send happens inline.
// ponytail: inline re-send; hand it to the webhook Lambda via an event if a
// replay ever needs the same retry ladder as a first delivery.
func (s *Service) ReplayDelivery(ctx context.Context, orgID, deliveryID string) error {
	k := deliveryKey(deliveryID)
	var d DeliveryItem
	found, err := s.Store.Get(ctx, k.PK, k.SK, &d)
	if err != nil {
		return err
	}
	if !found || d.OrganizationID != orgID || (d.Status != "succeeded" && d.Status != "failed") {
		return platform.Errf(404, "not_found", "Delivery not found or still in progress")
	}
	var ep EndpointItem
	epKey := endpointKey(orgID, d.EndpointID)
	found, err = s.Store.Get(ctx, epKey.PK, epKey.SK, &ep)
	if err != nil {
		return err
	}
	if !found {
		return platform.Errf(404, "not_found", "Webhook endpoint not found")
	}
	s.deliver(ctx, &ep, event{ID: d.EventID, Type: d.EventType, Payload: d.Payload,
		CreatedAt: d.CreatedAt}, d.DeliveryID, d.Attempt)
	return nil
}
