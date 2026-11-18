// Package feedback owns creation, querying and workflow of feedback records.
package feedback

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/SiyuQian/pinpost/internal/attachment"
	"github.com/SiyuQian/pinpost/internal/outbox"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var validTypes = map[string]bool{"bug": true, "suggestion": true, "other": true}
var validStatus = map[string]bool{"open": true, "in_progress": true, "resolved": true, "spam": true}
var validPriority = map[string]bool{"none": true, "low": true, "medium": true, "high": true, "urgent": true}

// Where a report came from. SourceWidget is everything the public ingest
// endpoint accepts; SourceInternal is a report a member filed from the
// dashboard, so the inbox can tell staff notes apart from customer reports.
// It is chosen by the handler from the authenticated caller, never read off
// the request body, and no update path can change it afterwards.
const (
	SourceWidget   = "widget"
	SourceInternal = "internal"
)

var validSource = map[string]bool{SourceWidget: true, SourceInternal: true}

// statusTransitions implements the four-state machine from the design doc.
var statusTransitions = map[string][]string{
	"open":        {"in_progress", "resolved", "spam"},
	"in_progress": {"open", "resolved", "spam"},
	"resolved":    {"open"},
	"spam":        {"open"},
}

// contextBlacklist keys are dropped from custom context on both SDK and server.
var contextBlacklist = map[string]bool{"password": true, "authorization": true, "cookie": true, "token": true, "secret": true}

type Service struct {
	DB         *pgxpool.Pool
	Attachment *attachment.Service
	Members    interface {
		IsMember(ctx context.Context, orgID, memberID string) (bool, error)
	}
}

type CreateInput struct {
	ProjectID      string
	OrganizationID string
	IdempotencyKey string
	Type           string `json:"type"`
	Message        string `json:"message"`
	Rating         *int   `json:"rating"`
	Contact        struct {
		Email string `json:"email"`
	} `json:"contact"`
	ExternalUserID string         `json:"external_user_id"`
	Context        map[string]any `json:"context"`
	AttachmentIDs  []string       `json:"attachment_ids"`

	// Set by the handler from the authenticated caller, not decoded from the
	// body — both are deliberately untagged so a client cannot post its own
	// provenance. Empty Source means SourceWidget; ActorID is the member who
	// filed an internal report, and drives the "created" activity's actor.
	Source  string `json:"-"`
	ActorID string `json:"-"`
}

type Created struct {
	FeedbackID string    `json:"feedback_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Service) Validate(in *CreateInput) *platform.APIError {
	fields := map[string]string{}
	if !validTypes[in.Type] {
		fields["type"] = "must be one of bug, suggestion, other"
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		fields["message"] = "required"
	} else if len(msg) > 10000 {
		fields["message"] = "too long"
	}
	if in.Rating != nil && (*in.Rating < 1 || *in.Rating > 5) {
		fields["rating"] = "must be between 1 and 5"
	}
	if in.Contact.Email != "" && (!strings.Contains(in.Contact.Email, "@") || len(in.Contact.Email) > 320) {
		fields["contact.email"] = "invalid email"
	}
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > 128 {
		fields["idempotency_key"] = "required"
	}
	if in.Source != "" && !validSource[in.Source] {
		fields["source"] = "must be one of widget, internal"
	}
	if len(in.AttachmentIDs) > 5 {
		fields["attachment_ids"] = "too many attachments"
	} else {
		// A duplicate would be claimed twice and fail as "already claimed";
		// a non-UUID would reach the uuid column and surface as a 500.
		seen := make(map[string]bool, len(in.AttachmentIDs))
		for _, id := range in.AttachmentIDs {
			if uuid.Validate(id) != nil {
				fields["attachment_ids"] = "must be UUIDs"
				break
			}
			if seen[id] {
				fields["attachment_ids"] = "must be unique"
				break
			}
			seen[id] = true
		}
	}
	if len(fields) > 0 {
		return platform.ValidationErr(fields)
	}
	return nil
}

// Context caps, per docs/feedback-system-design.md §6.6: 最大深度、键数量、
// 单值长度和总大小. The key count and value length were enforced from the
// start; depth and total size were not, and a nested object recursed with a
// fresh key counter, so one submission could carry an arbitrarily deep or
// large blob up to the 256 KB request limit.
const (
	contextMaxKeys  = 40
	contextMaxValue = 1024
	contextMaxDepth = 4
	contextMaxBytes = 16 << 10
)

// sourceKeys are the subset of the SDK's default fields that says who sent the
// report and from where. They survive the total-size cap even when the rest of
// the context does not: a report whose custom context was too large should
// still render a source subline. Matched case-insensitively, like the
// blacklist — the write key is public, so key spelling is caller-chosen.
var sourceKeys = map[string]bool{"url": true, "user_agent": true, "language": true, "referrer": true}

// addressKeys carry a URL, so their values get the query, fragment and
// userinfo stripped wherever they appear.
var addressKeys = map[string]bool{"url": true, "referrer": true}

// sanitizeContext drops blacklisted keys (top level and nested) and enforces
// the caps above. Trimming beats rejecting: the context is a debugging aid
// attached to a user's report, and losing the report because its context was
// too deep would be the worse failure.
func sanitizeContext(m map[string]any) map[string]any {
	out := sanitizeDepth(m, contextMaxDepth)
	// Total size is checked last: it depends on what the other caps left. Over
	// the ceiling the custom payload goes and the source fields stay, rather
	// than truncating mid-value and leaving invalid JSON in the record.
	if encoded, err := json.Marshal(out); err != nil || len(encoded) > contextMaxBytes {
		kept := map[string]any{}
		for k, v := range out {
			// Only short scalars are carried over. The rescue keys are
			// caller-chosen, and a nested list parked under `url` would put
			// the whole payload back over the ceiling that just rejected it.
			if s, ok := v.(string); ok && sourceKeys[strings.ToLower(k)] {
				kept[k] = truncate(s)
			}
		}
		return kept
	}
	return out
}

// stripAddress removes the query, fragment and userinfo from a URL. Ingest is the trust
// boundary, not the SDK: the write key is public by design, so an old SDK
// build, the demo page's inline client or a direct POST can all put a reset or
// SSO token in `url`/`referrer` — and both sanitisers filter by key, never by
// value. Anything unparseable is left alone; a stored address that no one can
// parse is less harmful than a dropped report.
func stripAddress(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	// Userinfo is a credential like any query token; u.String() would
	// re-serialise `user:secret@` verbatim.
	u.User = nil
	return u.String()
}

func sanitizeDepth(m map[string]any, depth int) map[string]any {
	if m == nil || depth <= 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	count := 0
	for k, v := range m {
		// One normalisation for every key-based rule below: `URL`, `Url` and
		// `uRl` all reach ListItem.URL, because encoding/json matches tags
		// case-insensitively — so they must all be stripped too.
		lk := strings.ToLower(k)
		if contextBlacklist[lk] || count >= contextMaxKeys {
			continue
		}
		switch tv := v.(type) {
		case string:
			if addressKeys[lk] {
				tv = stripAddress(tv)
			}
			out[k] = truncate(tv)
		case map[string]any:
			// Past the cap the key is dropped, not kept as an empty husk.
			if depth <= 1 {
				continue
			}
			out[k] = sanitizeDepth(tv, depth-1)
		case []any:
			// A breadcrumb or event list is an ordinary custom-context shape,
			// and without this arm one array level routes around the blacklist
			// and every cap.
			if depth <= 1 {
				continue
			}
			out[k] = sanitizeList(tv, depth-1, addressKeys[lk])
		default:
			out[k] = v
		}
		count++
	}
	return out
}

// sanitizeList walks an array the way sanitizeDepth walks a map. `addresses`
// is inherited from the key the list hangs off: `{"url": ["…?token=live"]}` is
// still a url, and stripping only the top-level string form would leave the
// array shape as a way past it.
func sanitizeList(list []any, depth int, addresses bool) []any {
	out := []any{}
	for _, v := range list {
		if len(out) >= contextMaxKeys {
			break
		}
		switch tv := v.(type) {
		case string:
			if addresses {
				tv = stripAddress(tv)
			}
			out = append(out, truncate(tv))
		case map[string]any:
			if depth <= 1 {
				continue
			}
			out = append(out, sanitizeDepth(tv, depth-1))
		case []any:
			if depth <= 1 {
				continue
			}
			out = append(out, sanitizeList(tv, depth-1, addresses))
		default:
			out = append(out, v)
		}
	}
	return out
}

func truncate(v string) string {
	if len(v) > contextMaxValue {
		return v[:contextMaxValue]
	}
	return v
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// Create writes the feedback, its first activity and the attachment claims in
// one transaction. A replayed idempotency key inserts nothing and returns the
// record created the first time; a concurrent request with the same key waits
// on the unique index until the first commits, then takes the same path.
func (s *Service) Create(ctx context.Context, in *CreateInput) (*Created, error) {
	if apiErr := s.Validate(in); apiErr != nil {
		return nil, apiErr
	}
	ctxJSON, _ := json.Marshal(sanitizeContext(in.Context))
	source := in.Source
	if source == "" {
		source = SourceWidget
	}
	// An internal report is attributed to the member who filed it, so the
	// timeline reads "X created this" rather than crediting an end user.
	actorType, actorID := "end_user", (*string)(nil)
	if in.ActorID != "" {
		actorType, actorID = "member", &in.ActorID
	}

	out := &Created{FeedbackID: uuid.Must(uuid.NewV7()).String()}
	var invalidAttachment string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO feedback
			(id, organization_id, project_id, idempotency_key, external_user_id, contact_email,
			 type, message, source, rating, context)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (project_id, idempotency_key) DO NOTHING
			RETURNING created_at`,
			out.FeedbackID, in.OrganizationID, in.ProjectID, in.IdempotencyKey,
			strPtr(in.ExternalUserID), strPtr(in.Contact.Email), in.Type,
			strings.TrimSpace(in.Message), source, in.Rating, string(ctxJSON)).Scan(&out.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			// Idempotent replay: hand back the feedback created earlier.
			return tx.QueryRow(ctx, `SELECT id, created_at FROM feedback
				WHERE project_id = $1 AND idempotency_key = $2`, in.ProjectID, in.IdempotencyKey).
				Scan(&out.FeedbackID, &out.CreatedAt)
		}
		if err != nil {
			return err
		}
		if err := insertActivity(ctx, tx, out.FeedbackID, actorType, actorID, "created", nil); err != nil {
			return err
		}
		for _, attID := range in.AttachmentIDs {
			ok, err := attachment.Claim(ctx, tx, attID, in.ProjectID, out.FeedbackID)
			if err != nil {
				return err
			}
			if !ok {
				invalidAttachment = attID
				return errRollback
			}
		}
		// source lets a consumer route a report the team filed itself away
		// from customer traffic.
		return outbox.Enqueue(ctx, tx, in.OrganizationID, "feedback.created.v1", map[string]any{
			"feedback_id": out.FeedbackID,
			"project_id":  in.ProjectID,
			"type":        in.Type,
			"message":     strings.TrimSpace(in.Message),
			"source":      source,
			"rating":      in.Rating,
		})
	})
	if invalidAttachment != "" {
		return nil, platform.ValidationErr(map[string]string{
			"attachment_ids": fmt.Sprintf(
				"attachment %s is unknown, already claimed, or belongs to another project", invalidAttachment)})
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// errRollback aborts a transaction whose outcome the caller has already
// recorded elsewhere.
var errRollback = errors.New("rollback")

// insertActivity appends one entry to a feedback timeline.
func insertActivity(ctx context.Context, tx pgx.Tx, feedbackID, actorType string, actorID *string,
	activityType string, data map[string]any) error {
	b, _ := json.Marshal(data)
	if data == nil {
		b = []byte("{}")
	}
	_, err := tx.Exec(ctx, `INSERT INTO activities (id, feedback_id, actor_type, actor_id, activity_type, data)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		uuid.Must(uuid.NewV7()).String(), feedbackID, actorType, actorID, activityType, string(b))
	return err
}

// ---- reads ----

// ListItem carries the submitter and the two context fields the inbox renders
// on a row — not the whole context blob. A page is 50 rows and one project's
// contexts are bounded only by the ingest body limit, so widening the row to
// the full object would let one noisy project inflate every inbox page in the
// organization. The complete context stays on Detail, read one record at a
// time.
type ListItem struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Message      string  `json:"message"`
	ContactEmail *string `json:"contact_email"`
	// Page address with the query, fragment and userinfo removed — scheme,
	// host and path are kept verbatim so deep links survive. Both the SDK and
	// ingest enforce it; ingest is the trust boundary, since the write key is
	// public by design.
	URL       *string `json:"url"`
	UserAgent *string `json:"user_agent"`
	// "widget" for a customer report, "internal" for one a member filed from
	// the dashboard.
	Source     string    `json:"source"`
	Rating     *int      `json:"rating"`
	Status     string    `json:"status"`
	Priority   string    `json:"priority"`
	AssigneeID *string   `json:"assignee_id"`
	Tags       []string  `json:"tags"`
	ProjectID  string    `json:"project_id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// source pulls just the two fields the inbox row renders out of the stored
// context. Anything unparseable yields no source rather than an error: a row
// that cannot say where it came from is still a row worth showing.
func (i *Item) source() (url, userAgent *string) {
	if i.Context == "" {
		return nil, nil
	}
	// Decoded as a map, not a struct: a context whose `url` is a list or a
	// number would fail a typed decode and blank `user_agent` along with it.
	var ctx map[string]any
	if err := json.Unmarshal([]byte(i.Context), &ctx); err != nil {
		return nil, nil
	}
	str := func(key string) *string {
		for k, v := range ctx {
			if !strings.EqualFold(k, key) {
				continue
			}
			if s, ok := v.(string); ok {
				return strPtr(s)
			}
		}
		return nil
	}
	return str("url"), str("user_agent")
}

func (i *Item) list() ListItem {
	tags := i.Tags
	if tags == nil {
		tags = []string{}
	}
	url, userAgent := i.source()
	return ListItem{ID: i.FeedbackID, Type: i.Type, Message: i.Message,
		ContactEmail: i.ContactEmail, URL: url, UserAgent: userAgent, Source: i.Source, Rating: i.Rating,
		Status: i.Status, Priority: i.Priority, AssigneeID: i.AssigneeID, Tags: tags,
		ProjectID: i.ProjectID, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
}

type ListFilter struct {
	OrganizationID string
	ProjectID      string
	Status         string
	Type           string
	Source         string
	AssigneeID     string
	Query          string
	Cursor         string
	Limit          int
}

// The cursor is the id of the last row returned; UUIDv7 sorts by time, so
// "id below the cursor, newest first" resumes exactly where the previous page
// stopped.
func encodeCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("FB#" + id))
}

func decodeCursor(c string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	id, ok := strings.CutPrefix(string(raw), "FB#")
	if err != nil || !ok || uuid.Validate(id) != nil {
		return "", fmt.Errorf("bad cursor")
	}
	return id, nil
}

// likePattern matches q anywhere, with q's own wildcards taken literally.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// List returns a tenant-scoped, cursor-paginated inbox page. Every filter is a
// WHERE clause on top of the organization; only (organization_id, id) is
// indexed, so the cost of a narrow filter is bounded by one tenant's rows
// (design §8.4 says when to index more).
func (s *Service) List(ctx context.Context, f ListFilter) ([]ListItem, string, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 25
	}
	args := []any{f.OrganizationID}
	where := []string{"organization_id = $1"}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.ProjectID != "" {
		add("project_id = $%d", f.ProjectID)
	}
	if f.Status != "" {
		if !validStatus[f.Status] {
			return nil, "", platform.ValidationErr(map[string]string{"status": "invalid"})
		}
		add("status = $%d", f.Status)
	}
	if f.Type != "" {
		if !validTypes[f.Type] {
			return nil, "", platform.ValidationErr(map[string]string{"type": "invalid"})
		}
		add("type = $%d", f.Type)
	}
	if f.Source != "" {
		if !validSource[f.Source] {
			return nil, "", platform.ValidationErr(map[string]string{"source": "invalid"})
		}
		add("source = $%d", f.Source)
	}
	if f.AssigneeID != "" {
		add("assignee_id = $%d", f.AssigneeID)
	}
	if f.Query != "" {
		add("message ILIKE $%d", "%"+likeEscaper.Replace(f.Query)+"%")
	}
	if f.Cursor != "" {
		id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", platform.ValidationErr(map[string]string{"cursor": "invalid"})
		}
		add("id < $%d", id)
	}
	args = append(args, f.Limit+1)
	rows, _ := s.DB.Query(ctx, `SELECT `+itemCols+` FROM feedback WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args)), args...)
	page, err := pgx.CollectRows(rows, pgx.RowToStructByName[Item])
	if err != nil {
		return nil, "", err
	}

	items := make([]ListItem, 0, len(page))
	for i := range page {
		items = append(items, page[i].list())
	}
	next := ""
	if len(items) > f.Limit {
		items = items[:f.Limit]
		next = encodeCursor(items[len(items)-1].ID)
	}
	return items, next, nil
}

type Detail struct {
	ListItem
	ExternalUserID *string           `json:"external_user_id"`
	Context        json.RawMessage   `json:"context"`
	Attachments    []attachment.Meta `json:"attachments"`
}

// get loads one record of this organization.
func (s *Service) get(ctx context.Context, orgID, id string) (*Item, error) {
	rows, _ := s.DB.Query(ctx, `SELECT `+itemCols+` FROM feedback
		WHERE organization_id = $1 AND id = $2`, orgID, id)
	it, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Item])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, platform.Errf(404, "not_found", "Feedback not found")
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

func (s *Service) Get(ctx context.Context, orgID, id string) (*Detail, error) {
	it, err := s.get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	atts, err := s.Attachment.ListForFeedback(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	body := it.Context
	if body == "" {
		body = "{}"
	}
	return &Detail{ListItem: it.list(), ExternalUserID: it.ExternalUserID,
		Context: json.RawMessage(body), Attachments: atts}, nil
}

type UpdateInput struct {
	Status     *string  `json:"status"`
	Priority   *string  `json:"priority"`
	AssigneeID *string  `json:"assignee_id"` // empty string clears the assignee
	Tags       []string `json:"tags"`
	UpdatedAt  string   `json:"updated_at"` // optimistic concurrency token, optional
}

type activity struct {
	typ  string
	data map[string]any
}

type event struct {
	typ  string
	data map[string]any
}

// Update applies workflow changes, enforcing the state machine and recording
// activity. The write is conditional on updated_at still holding the value
// this call read: a concurrent edit commits first, this UPDATE then matches no
// row, and the dashboard gets 409.
func (s *Service) Update(ctx context.Context, orgID, id, actorID string, in *UpdateInput) (*Detail, error) {
	cur, err := s.get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if in.UpdatedAt != "" {
		want, err := time.Parse(time.RFC3339Nano, in.UpdatedAt)
		if err != nil || !want.Equal(cur.UpdatedAt) {
			return nil, platform.Errf(409, "conflict", "Feedback was modified by someone else")
		}
	}

	args := []any{orgID, id, cur.UpdatedAt}
	sets := []string{"updated_at = now()"}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	var activities []activity
	var events []event

	if in.Status != nil && *in.Status != cur.Status {
		if !validStatus[*in.Status] {
			return nil, platform.ValidationErr(map[string]string{"status": "invalid"})
		}
		allowed := false
		for _, next := range statusTransitions[cur.Status] {
			if next == *in.Status {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, platform.Errf(422, "invalid_transition",
				fmt.Sprintf("Cannot move from %s to %s", cur.Status, *in.Status))
		}
		set("status", *in.Status)
		activities = append(activities, activity{"status_changed", map[string]any{"from": cur.Status, "to": *in.Status}})
		events = append(events, event{"feedback.status_changed.v1", map[string]any{
			"feedback_id": id, "from": cur.Status, "to": *in.Status, "actor_id": actorID}})
	}
	if in.Priority != nil && *in.Priority != cur.Priority {
		if !validPriority[*in.Priority] {
			return nil, platform.ValidationErr(map[string]string{"priority": "invalid"})
		}
		set("priority", *in.Priority)
		activities = append(activities, activity{"priority_changed", map[string]any{"from": cur.Priority, "to": *in.Priority}})
	}
	if in.AssigneeID != nil {
		if *in.AssigneeID == "" {
			set("assignee_id", nil)
		} else {
			// Assignee must be an active member of the same organization.
			ok, err := s.Members.IsMember(ctx, orgID, *in.AssigneeID)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, platform.ValidationErr(map[string]string{"assignee_id": "not a member of this organization"})
			}
			set("assignee_id", *in.AssigneeID)
		}
		activities = append(activities, activity{"assigned", map[string]any{"assignee_id": in.AssigneeID}})
		if from := cur.AssigneeID; (from == nil) != (*in.AssigneeID == "") || (from != nil && *from != *in.AssigneeID) {
			events = append(events, event{"feedback.assigned.v1", map[string]any{
				"feedback_id": id, "assignee_id": *in.AssigneeID, "actor_id": actorID}})
		}
	}
	if in.Tags != nil {
		if len(in.Tags) > 20 {
			return nil, platform.ValidationErr(map[string]string{"tags": "too many tags"})
		}
		set("tags", in.Tags)
		activities = append(activities, activity{"tags_changed", map[string]any{"tags": in.Tags}})
	}
	if len(activities) == 0 {
		return s.Get(ctx, orgID, id)
	}

	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE feedback SET `+strings.Join(sets, ", ")+`
			WHERE organization_id = $1 AND id = $2 AND updated_at = $3`, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return platform.Errf(409, "conflict", "Feedback was modified by someone else")
		}
		for _, a := range activities {
			if err := insertActivity(ctx, tx, id, "member", &actorID, a.typ, a.data); err != nil {
				return err
			}
		}
		for _, e := range events {
			if err := outbox.Enqueue(ctx, tx, orgID, e.typ, e.data); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

// AddNote records an internal member note on the feedback timeline.
func (s *Service) AddNote(ctx context.Context, orgID, id, actorID, note string) error {
	note = strings.TrimSpace(note)
	if note == "" || len(note) > 5000 {
		return platform.ValidationErr(map[string]string{"note": "required, max 5000 chars"})
	}
	if _, err := s.get(ctx, orgID, id); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if err := insertActivity(ctx, tx, id, "member", &actorID, "note_added", map[string]any{"note": note}); err != nil {
			return err
		}
		// The note itself stays internal; the event only says one was added.
		return outbox.Enqueue(ctx, tx, orgID, "feedback.note_added.v1", map[string]any{
			"feedback_id": id, "actor_id": actorID})
	})
}

type Activity struct {
	ID           string          `json:"id" db:"id"`
	ActorType    string          `json:"actor_type" db:"actor_type"`
	ActorID      *string         `json:"actor_id" db:"actor_id"`
	ActivityType string          `json:"activity_type" db:"activity_type"`
	Data         json.RawMessage `json:"data" db:"data"`
	CreatedAt    time.Time       `json:"created_at" db:"created_at"`
}

func (s *Service) Activities(ctx context.Context, orgID, id string) ([]Activity, error) {
	if _, err := s.get(ctx, orgID, id); err != nil {
		return nil, err
	}
	rows, _ := s.DB.Query(ctx, `SELECT id, actor_type, actor_id, activity_type, data, created_at
		FROM activities WHERE feedback_id = $1 ORDER BY id`, id)
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[Activity])
	if out == nil {
		out = []Activity{}
	}
	return out, err
}

type Overview struct {
	Total    int              `json:"total"`
	ByStatus map[string]int   `json:"by_status"`
	ByType   map[string]int   `json:"by_type"`
	Trend    []map[string]any `json:"trend"`
}

// Overview aggregates the organization's feedback. An empty projectID counts
// the whole organization; a set one counts just that project, so the
// dashboard tiles agree with the inbox they sit above.
func (s *Service) Overview(ctx context.Context, orgID, projectID string) (*Overview, error) {
	var project any
	if projectID != "" {
		project = projectID
	}
	o := &Overview{ByStatus: map[string]int{}, ByType: map[string]int{}, Trend: []map[string]any{}}
	rows, _ := s.DB.Query(ctx, `SELECT status, type, count(*) FROM feedback
		WHERE organization_id = $1 AND ($2::uuid IS NULL OR project_id = $2)
		GROUP BY status, type`, orgID, project)
	var status, typ string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&status, &typ, &n}, func() error {
		o.Total += n
		o.ByStatus[status] += n
		o.ByType[typ] += n
		return nil
	}); err != nil {
		return nil, err
	}

	var day string
	rows, _ = s.DB.Query(ctx, `SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day, count(*)
		FROM feedback
		WHERE organization_id = $1 AND ($2::uuid IS NULL OR project_id = $2) AND created_at > $3
		GROUP BY day ORDER BY day`, orgID, project, time.Now().UTC().AddDate(0, 0, -14))
	if _, err := pgx.ForEachRow(rows, []any{&day, &n}, func() error {
		o.Trend = append(o.Trend, map[string]any{"day": day, "count": n})
		return nil
	}); err != nil {
		return nil, err
	}
	return o, nil
}
