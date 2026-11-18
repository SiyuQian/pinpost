package feedback

import (
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
)

// Item is a feedback record. It lives in its organization's partition with a
// UUIDv7 sort key, so "newest first, cursor paginated" is a plain reverse
// Query and the cursor is just a sort key.
type Item struct {
	platform.Key
	FeedbackID     string  `dynamodbav:"feedback_id"`
	OrganizationID string  `dynamodbav:"org_id"`
	ProjectID      string  `dynamodbav:"project_id"`
	ExternalUserID *string `dynamodbav:"external_user_id"`
	ContactEmail   *string `dynamodbav:"contact_email"`
	Type           string  `dynamodbav:"type"`
	Message        string  `dynamodbav:"message"`
	// Source is provenance, not a label: who filed the report, decided by
	// which endpoint accepted it. Absent on rows written before the field
	// existed, which read back as SourceWidget.
	Source         string    `dynamodbav:"source"`
	Rating         *int      `dynamodbav:"rating"`
	Status         string    `dynamodbav:"status"`
	Priority       string    `dynamodbav:"priority"`
	AssigneeID     *string   `dynamodbav:"assignee_id"`
	Tags           []string  `dynamodbav:"tags"`
	Context        string    `dynamodbav:"context"`
	IdempotencyKey string    `dynamodbav:"idempotency_key"`
	LastActorID    string    `dynamodbav:"last_actor_id"`
	CreatedAt      time.Time `dynamodbav:"created_at"`
	UpdatedAt      time.Time `dynamodbav:"updated_at"`
}

func itemKey(orgID, feedbackID string) platform.Key {
	return platform.Key{PK: "ORG#" + orgID, SK: "FB#" + feedbackID}
}

// ActivityItem is the feedback timeline; it hangs off the feedback's own
// partition so the whole timeline is one Query. org_id is carried along
// because the webhook stream consumer only sees the item, never a join.
type ActivityItem struct {
	platform.Key
	ActivityID     string    `dynamodbav:"activity_id"`
	FeedbackID     string    `dynamodbav:"feedback_id"`
	OrganizationID string    `dynamodbav:"org_id"`
	ProjectID      string    `dynamodbav:"project_id"`
	ActorType      string    `dynamodbav:"actor_type"`
	ActorID        *string   `dynamodbav:"actor_id"`
	ActivityType   string    `dynamodbav:"activity_type"`
	Data           string    `dynamodbav:"data"`
	CreatedAt      time.Time `dynamodbav:"created_at"`
}

// IdemItem dedupes submissions; the conditional write on its key replaces the
// UNIQUE (project_id, idempotency_key) constraint. Expires after 24 hours.
type IdemItem struct {
	platform.Key
	FeedbackID string    `dynamodbav:"feedback_id"`
	CreatedAt  time.Time `dynamodbav:"created_at"`
	ExpiresAt  int64     `dynamodbav:"expires_at"`
}

func idemKey(projectID, key string) platform.Key {
	return platform.Key{PK: "IDEM#" + projectID + "#" + key, SK: platform.Meta}
}
