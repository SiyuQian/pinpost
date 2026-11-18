package outbox

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func av(v string) events.DynamoDBAttributeValue { return events.NewStringAttribute(v) }

// TestDerive pins the mapping from item changes to webhook events. It is the
// piece that replaced the explicit outbox insert, so it is the piece that can
// silently stop emitting.
func TestDerive(t *testing.T) {
	feedback := map[string]events.DynamoDBAttributeValue{
		"sk": av("FB#01"), "org_id": av("org-1"), "feedback_id": av("fb-1"),
		"project_id": av("proj-1"), "type": av("bug"), "message": av("it broke"),
		"status": av("open"),
	}
	clone := func(overrides map[string]events.DynamoDBAttributeValue) map[string]events.DynamoDBAttributeValue {
		out := map[string]events.DynamoDBAttributeValue{}
		for k, v := range feedback {
			out[k] = v
		}
		for k, v := range overrides {
			out[k] = v
		}
		return out
	}

	cases := []struct {
		name   string
		record events.DynamoDBEventRecord
		want   []string
	}{
		{"created", record("INSERT", nil, feedback), []string{"feedback.created.v1"}},
		{"status changed", record("MODIFY", feedback,
			clone(map[string]events.DynamoDBAttributeValue{"status": av("in_progress"), "last_actor_id": av("m-1")})),
			[]string{"feedback.status_changed.v1"}},
		{"status and assignee in one write", record("MODIFY", feedback,
			clone(map[string]events.DynamoDBAttributeValue{"status": av("resolved"), "assignee_id": av("m-2")})),
			[]string{"feedback.status_changed.v1", "feedback.assigned.v1"}},
		{"untouched fields emit nothing", record("MODIFY", feedback,
			clone(map[string]events.DynamoDBAttributeValue{"priority": av("high")})), nil},
		{"note", record("INSERT", nil, map[string]events.DynamoDBAttributeValue{
			"sk": av("ACT#01"), "org_id": av("org-1"), "feedback_id": av("fb-1"),
			"activity_type": av("note_added"), "actor_id": av("m-1"),
		}), []string{"feedback.note_added.v1"}},
		{"other activities stay internal", record("INSERT", nil, map[string]events.DynamoDBAttributeValue{
			"sk": av("ACT#02"), "org_id": av("org-1"), "activity_type": av("tags_changed"),
		}), nil},
		{"attachments are not events", record("INSERT", nil, map[string]events.DynamoDBAttributeValue{
			"sk": av("#META"), "org_id": av("org-1"),
		}), nil},
	}
	for _, c := range cases {
		got := derive(c.record)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %d events, want %d", c.name, len(got), len(c.want))
			continue
		}
		for i, ev := range got {
			if ev.Type != c.want[i] {
				t.Errorf("%s: event %d = %q, want %q", c.name, i, ev.Type, c.want[i])
			}
			if ev.OrgID != "org-1" {
				t.Errorf("%s: event %d lost its organization", c.name, i)
			}
			if !json.Valid([]byte(ev.Payload)) {
				t.Errorf("%s: event %d payload is not JSON: %s", c.name, i, ev.Payload)
			}
		}
	}
}

// TestDeriveCarriesSource pins the field a consumer routes on: an internal
// report must not reach a customer-facing Slack channel or issue tracker
// looking like a customer report, and records written before the field existed
// have to keep reading as "widget" rather than as an empty string.
func TestDeriveCarriesSource(t *testing.T) {
	base := map[string]events.DynamoDBAttributeValue{
		"sk": av("FB#01"), "org_id": av("org-1"), "feedback_id": av("fb-1"),
		"project_id": av("proj-1"), "type": av("bug"), "message": av("it broke"),
	}
	for _, c := range []struct{ name, stored, want string }{
		{"internal", "internal", "internal"},
		{"widget", "widget", "widget"},
		{"legacy row with no source attribute", "", "widget"},
	} {
		img := map[string]events.DynamoDBAttributeValue{}
		for k, v := range base {
			img[k] = v
		}
		if c.stored != "" {
			img["source"] = av(c.stored)
		}
		got := derive(record("INSERT", nil, img))
		if len(got) != 1 {
			t.Fatalf("%s: got %d events, want 1", c.name, len(got))
		}
		var payload struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal([]byte(got[0].Payload), &payload); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if payload.Source != c.want {
			t.Errorf("%s: source = %q, want %q", c.name, payload.Source, c.want)
		}
	}
}

func record(name string, old, new map[string]events.DynamoDBAttributeValue) events.DynamoDBEventRecord {
	return events.DynamoDBEventRecord{
		EventID:   "ev-" + name,
		EventName: name,
		Change:    events.DynamoDBStreamRecord{OldImage: old, NewImage: new},
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
