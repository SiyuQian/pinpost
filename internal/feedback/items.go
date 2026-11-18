package feedback

import "time"

// Item is a feedback row. Its id is a UUIDv7, so "newest first, cursor
// paginated" is ORDER BY id DESC and the cursor is just an id.
type Item struct {
	FeedbackID     string  `db:"id"`
	OrganizationID string  `db:"organization_id"`
	ProjectID      string  `db:"project_id"`
	ExternalUserID *string `db:"external_user_id"`
	ContactEmail   *string `db:"contact_email"`
	Type           string  `db:"type"`
	Message        string  `db:"message"`
	// Source is provenance, not a label: who filed the report, decided by
	// which endpoint accepted it.
	Source     string    `db:"source"`
	Rating     *int      `db:"rating"`
	Status     string    `db:"status"`
	Priority   string    `db:"priority"`
	AssigneeID *string   `db:"assignee_id"`
	Tags       []string  `db:"tags"`
	Context    string    `db:"context"`
	CreatedAt  time.Time `db:"created_at"`
	UpdatedAt  time.Time `db:"updated_at"`
}

const itemCols = `id, organization_id, project_id, external_user_id, contact_email, type, message,
	source, rating, status, priority, assignee_id, tags, context, created_at, updated_at`
