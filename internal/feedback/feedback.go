// Package feedback owns creation, querying and workflow of feedback records.
package feedback

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SiyuQian/pinpost/internal/attachment"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
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

// idemTTL matches the design: replays are deduped for a day, then the key expires.
const idemTTL = 24 * time.Hour

type Service struct {
	Store      *platform.Store
	Attachment *attachment.Service
	Members    interface {
		IsMember(ctx context.Context, orgID, memberID string) (bool, error)
	}

	overviewMu    sync.Mutex
	overviewCache map[string]cachedItems
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
		// Two identical ids become two operations on one item, which
		// TransactWriteItems rejects as a ValidationException — an input
		// error that would otherwise surface as a 500.
		seen := make(map[string]bool, len(in.AttachmentIDs))
		for _, id := range in.AttachmentIDs {
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

// Create writes the feedback, its idempotency key, its first activity and the
// attachment claims in one TransactWriteItems. A duplicate idempotency key
// fails the whole transaction, and the existing record is returned instead.
func (s *Service) Create(ctx context.Context, in *CreateInput) (*Created, error) {
	if apiErr := s.Validate(in); apiErr != nil {
		return nil, apiErr
	}
	ctxJSON, _ := json.Marshal(sanitizeContext(in.Context))
	id := uuid.Must(uuid.NewV7()).String()
	now := time.Now().UTC()

	source := in.Source
	if source == "" {
		source = SourceWidget
	}
	item := &Item{
		Key:            itemKey(in.OrganizationID, id),
		FeedbackID:     id,
		OrganizationID: in.OrganizationID,
		ProjectID:      in.ProjectID,
		ExternalUserID: strPtr(in.ExternalUserID),
		ContactEmail:   strPtr(in.Contact.Email),
		Type:           in.Type,
		Message:        strings.TrimSpace(in.Message),
		Source:         source,
		Rating:         in.Rating,
		Status:         "open",
		Priority:       "none",
		Tags:           []string{},
		Context:        string(ctxJSON),
		IdempotencyKey: in.IdempotencyKey,
		LastActorID:    in.ActorID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	fbAV, err := platform.Marshal(item)
	if err != nil {
		return nil, err
	}
	idemK := idemKey(in.ProjectID, in.IdempotencyKey)
	idemAV, err := platform.Marshal(&IdemItem{
		Key: idemK, FeedbackID: id, CreatedAt: now, ExpiresAt: platform.TTL(idemTTL),
	})
	if err != nil {
		return nil, err
	}
	// An internal report is attributed to the member who filed it, so the
	// timeline reads "X created this" rather than crediting an end user.
	actorType, actorID := "end_user", (*string)(nil)
	if in.ActorID != "" {
		actorType, actorID = "member", &in.ActorID
	}
	actAV, err := platform.Marshal(s.newActivity(item, actorType, actorID, "created", nil))
	if err != nil {
		return nil, err
	}

	// Order matters: cancellation reasons come back positionally, and that is
	// how a duplicate key is told apart from an unavailable attachment.
	const idemIndex = 1
	const firstAttachmentIndex = 3
	writes := []types.TransactWriteItem{
		{Put: &types.Put{TableName: &s.Store.Table, Item: fbAV}},
		{Put: &types.Put{TableName: &s.Store.Table, Item: idemAV,
			ConditionExpression: aws.String("attribute_not_exists(pk)")}},
		{Put: &types.Put{TableName: &s.Store.Table, Item: actAV}},
	}
	for _, attID := range in.AttachmentIDs {
		writes = append(writes, attachment.LinkTransactItem(s.Store, attID, in.ProjectID, id))
	}

	_, err = s.Store.DB.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes})
	if err != nil {
		var cancelled *types.TransactionCanceledException
		if !errors.As(err, &cancelled) {
			return nil, err
		}
		for i, reason := range cancelled.CancellationReasons {
			if reason.Code == nil {
				continue
			}
			if *reason.Code == "TransactionConflict" {
				// Another writer touched one of these items mid-transaction —
				// the scan Lambda recording a verdict on an attachment is the
				// common one. Retriable, so say so instead of returning 500.
				return nil, platform.Errf(409, "conflict", "Concurrent write, retry the request")
			}
			if *reason.Code != "ConditionalCheckFailed" {
				continue
			}
			if i == idemIndex {
				// Idempotent replay: hand back the feedback created earlier.
				var existing IdemItem
				found, err := s.Store.Get(ctx, idemK.PK, idemK.SK, &existing)
				if err != nil {
					return nil, err
				}
				if found {
					return &Created{FeedbackID: existing.FeedbackID, CreatedAt: existing.CreatedAt}, nil
				}
				return nil, platform.Errf(409, "conflict", "Concurrent submission with the same idempotency key")
			}
			if i >= firstAttachmentIndex {
				attID := in.AttachmentIDs[i-firstAttachmentIndex]
				return nil, platform.ValidationErr(map[string]string{
					"attachment_ids": fmt.Sprintf(
						"attachment %s is unknown, already claimed, or belongs to another project", attID)})
			}
		}
		return nil, err
	}
	return &Created{FeedbackID: id, CreatedAt: now}, nil
}

func (s *Service) newActivity(fb *Item, actorType string, actorID *string, activityType string, data map[string]any) *ActivityItem {
	b, _ := json.Marshal(data)
	if data == nil {
		b = []byte("{}")
	}
	actID := uuid.Must(uuid.NewV7()).String()
	return &ActivityItem{
		Key:            platform.Key{PK: "FB#" + fb.FeedbackID, SK: "ACT#" + actID},
		ActivityID:     actID,
		FeedbackID:     fb.FeedbackID,
		OrganizationID: fb.OrganizationID,
		ProjectID:      fb.ProjectID,
		ActorType:      actorType,
		ActorID:        actorID,
		ActivityType:   activityType,
		Data:           string(b),
		CreatedAt:      time.Now().UTC(),
	}
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
	// the dashboard. Always populated: rows written before the field existed
	// report as "widget".
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
	source := i.Source
	if source == "" {
		source = SourceWidget
	}
	return ListItem{ID: i.FeedbackID, Type: i.Type, Message: i.Message,
		ContactEmail: i.ContactEmail, URL: url, UserAgent: userAgent, Source: source, Rating: i.Rating,
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

// The cursor is the sort key of the last row returned; UUIDv7 sorts by time,
// so a reverse Query resumes exactly where the previous page stopped.
func encodeCursor(sk string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(sk))
}

func decodeCursor(c string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || !strings.HasPrefix(string(raw), "FB#") {
		return "", fmt.Errorf("bad cursor")
	}
	return string(raw), nil
}

// List returns a tenant-scoped, cursor-paginated inbox page. Status/type/
// assignee narrow the read with a FilterExpression and `q` is matched in
// memory: both read the organization's partition, never the whole table.
func (s *Service) List(ctx context.Context, f ListFilter) ([]ListItem, string, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 25
	}
	values := map[string]types.AttributeValue{
		":pk":     platform.S("ORG#" + f.OrganizationID),
		":prefix": platform.S("FB#"),
	}
	names := map[string]string{}
	var filters []string
	add := func(attr, placeholder, value string) {
		names["#"+attr] = attr
		values[placeholder] = platform.S(value)
		filters = append(filters, fmt.Sprintf("#%s = %s", attr, placeholder))
	}
	if f.ProjectID != "" {
		add("project_id", ":project", f.ProjectID)
	}
	if f.Status != "" {
		if !validStatus[f.Status] {
			return nil, "", platform.ValidationErr(map[string]string{"status": "invalid"})
		}
		add("status", ":status", f.Status)
	}
	if f.Type != "" {
		if !validTypes[f.Type] {
			return nil, "", platform.ValidationErr(map[string]string{"type": "invalid"})
		}
		add("type", ":type", f.Type)
	}
	if f.Source != "" {
		if !validSource[f.Source] {
			return nil, "", platform.ValidationErr(map[string]string{"source": "invalid"})
		}
		names["#source"] = "source"
		values[":source"] = platform.S(f.Source)
		clause := "#source = :source"
		if f.Source == SourceWidget {
			// Rows written before the field existed carry no `source`
			// attribute at all, and a bare equality would hide every one of
			// them from the default "customer feedback" filter.
			clause = "(attribute_not_exists(#source) OR #source = :source)"
		}
		filters = append(filters, clause)
	}
	if f.AssigneeID != "" {
		add("assignee_id", ":assignee", f.AssigneeID)
	}

	in := &dynamodb.QueryInput{
		TableName:                 &s.Store.Table,
		KeyConditionExpression:    aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: values,
		ScanIndexForward:          aws.Bool(false),
	}
	if len(filters) > 0 {
		in.FilterExpression = aws.String(strings.Join(filters, " AND "))
		in.ExpressionAttributeNames = names
	} else if f.Query == "" {
		// Nothing is dropped after the read, so the page size is exact.
		in.Limit = aws.Int32(int32(f.Limit + 1))
	}
	if f.Cursor != "" {
		sk, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", platform.ValidationErr(map[string]string{"cursor": "invalid"})
		}
		in.ExclusiveStartKey = platform.KeyAV("ORG#"+f.OrganizationID, sk)
	}

	needle := strings.ToLower(f.Query)
	items := []ListItem{}
	for len(items) <= f.Limit {
		res, err := s.Store.DB.Query(ctx, in)
		if err != nil {
			return nil, "", err
		}
		var page []Item
		if err := attributevalue.UnmarshalListOfMaps(res.Items, &page); err != nil {
			return nil, "", err
		}
		for _, it := range page {
			if needle != "" && !strings.Contains(strings.ToLower(it.Message), needle) {
				continue
			}
			items = append(items, it.list())
			if len(items) > f.Limit {
				break
			}
		}
		if len(res.LastEvaluatedKey) == 0 || len(items) > f.Limit {
			break
		}
		in.ExclusiveStartKey = res.LastEvaluatedKey
	}

	next := ""
	if len(items) > f.Limit {
		items = items[:f.Limit]
		next = encodeCursor("FB#" + items[len(items)-1].ID)
	}
	return items, next, nil
}

type Detail struct {
	ListItem
	ExternalUserID *string           `json:"external_user_id"`
	Context        json.RawMessage   `json:"context"`
	Attachments    []attachment.Meta `json:"attachments"`
}

// get loads one record, scoped to the organization by its partition key.
func (s *Service) get(ctx context.Context, orgID, id string) (*Item, error) {
	k := itemKey(orgID, id)
	var it Item
	found, err := s.Store.Get(ctx, k.PK, k.SK, &it)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, platform.Errf(404, "not_found", "Feedback not found")
	}
	return &it, nil
}

func (s *Service) Get(ctx context.Context, orgID, id string) (*Detail, error) {
	it, err := s.get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	atts, err := s.Attachment.ListForFeedback(ctx, id)
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

// Update applies workflow changes, enforcing the state machine and recording
// activity. The row lock is gone: the write is conditional on updated_at still
// holding the value this call read, which is the same 409 to the dashboard.
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

	sets := []string{"updated_at = :now", "last_actor_id = :actor"}
	names := map[string]string{}
	now := time.Now().UTC()
	nowAV, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, err
	}
	expectedAV, err := attributevalue.Marshal(cur.UpdatedAt)
	if err != nil {
		return nil, err
	}
	values := map[string]types.AttributeValue{
		":now":      nowAV,
		":actor":    platform.S(actorID),
		":expected": expectedAV,
	}
	var activities []*ActivityItem

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
		names["#status"] = "status"
		sets = append(sets, "#status = :status")
		values[":status"] = platform.S(*in.Status)
		activities = append(activities, s.newActivity(cur, "member", &actorID, "status_changed",
			map[string]any{"from": cur.Status, "to": *in.Status}))
	}
	if in.Priority != nil && *in.Priority != cur.Priority {
		if !validPriority[*in.Priority] {
			return nil, platform.ValidationErr(map[string]string{"priority": "invalid"})
		}
		sets = append(sets, "priority = :priority")
		values[":priority"] = platform.S(*in.Priority)
		activities = append(activities, s.newActivity(cur, "member", &actorID, "priority_changed",
			map[string]any{"from": cur.Priority, "to": *in.Priority}))
	}
	if in.AssigneeID != nil {
		if *in.AssigneeID == "" {
			sets = append(sets, "assignee_id = :assignee")
			values[":assignee"] = &types.AttributeValueMemberNULL{Value: true}
		} else {
			// Assignee must be a member of the same organization.
			ok, err := s.Members.IsMember(ctx, orgID, *in.AssigneeID)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, platform.ValidationErr(map[string]string{"assignee_id": "not a member of this organization"})
			}
			sets = append(sets, "assignee_id = :assignee")
			values[":assignee"] = platform.S(*in.AssigneeID)
		}
		activities = append(activities, s.newActivity(cur, "member", &actorID, "assigned",
			map[string]any{"assignee_id": in.AssigneeID}))
	}
	if in.Tags != nil {
		if len(in.Tags) > 20 {
			return nil, platform.ValidationErr(map[string]string{"tags": "too many tags"})
		}
		tagsAV, err := attributevalue.Marshal(in.Tags)
		if err != nil {
			return nil, err
		}
		sets = append(sets, "tags = :tags")
		values[":tags"] = tagsAV
		activities = append(activities, s.newActivity(cur, "member", &actorID, "tags_changed",
			map[string]any{"tags": in.Tags}))
	}
	if len(activities) == 0 {
		return s.Get(ctx, orgID, id)
	}

	k := itemKey(orgID, id)
	writes := []types.TransactWriteItem{{Update: &types.Update{
		TableName:                 &s.Store.Table,
		Key:                       platform.KeyAV(k.PK, k.SK),
		UpdateExpression:          aws.String("SET " + strings.Join(sets, ", ")),
		ConditionExpression:       aws.String("updated_at = :expected"),
		ExpressionAttributeValues: values,
	}}}
	if len(names) > 0 {
		writes[0].Update.ExpressionAttributeNames = names
	}
	for _, a := range activities {
		av, err := platform.Marshal(a)
		if err != nil {
			return nil, err
		}
		writes = append(writes, types.TransactWriteItem{Put: &types.Put{TableName: &s.Store.Table, Item: av}})
	}
	if _, err := s.Store.DB.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: writes}); err != nil {
		var cancelled *types.TransactionCanceledException
		if errors.As(err, &cancelled) {
			return nil, platform.Errf(409, "conflict", "Feedback was modified by someone else")
		}
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
	cur, err := s.get(ctx, orgID, id)
	if err != nil {
		return err
	}
	return s.Store.Put(ctx, s.newActivity(cur, "member", &actorID, "note_added",
		map[string]any{"note": note}))
}

type Activity struct {
	ID           string          `json:"id"`
	ActorType    string          `json:"actor_type"`
	ActorID      *string         `json:"actor_id"`
	ActivityType string          `json:"activity_type"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    time.Time       `json:"created_at"`
}

func (s *Service) Activities(ctx context.Context, orgID, id string) ([]Activity, error) {
	if _, err := s.get(ctx, orgID, id); err != nil {
		return nil, err
	}
	var items []ActivityItem
	if err := s.Store.QueryAll(ctx, &dynamodb.QueryInput{
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": platform.S("FB#" + id), ":prefix": platform.S("ACT#"),
		},
	}, &items); err != nil {
		return nil, err
	}
	out := []Activity{}
	for _, a := range items {
		data := a.Data
		if data == "" {
			data = "{}"
		}
		out = append(out, Activity{ID: a.ActivityID, ActorType: a.ActorType, ActorID: a.ActorID,
			ActivityType: a.ActivityType, Data: json.RawMessage(data), CreatedAt: a.CreatedAt})
	}
	return out, nil
}

type Overview struct {
	Total    int              `json:"total"`
	ByStatus map[string]int   `json:"by_status"`
	ByType   map[string]int   `json:"by_type"`
	Trend    []map[string]any `json:"trend"`
}

// counted is all Overview reads of a row, and all the query asks DynamoDB for.
// An Item carries the message and the raw context (up to contextMaxBytes
// each): materialising a tenant's whole partition to count it — let alone
// keeping it resident for the process lifetime — trades a read-capacity win
// for an OOM.
type counted struct {
	ProjectID string    `dynamodbav:"project_id"`
	Status    string    `dynamodbav:"status"`
	Type      string    `dynamodbav:"type"`
	CreatedAt time.Time `dynamodbav:"created_at"`
}

type cachedItems struct {
	at   time.Time
	rows []counted
}

// overviewTTL bounds how often the organization partition is read. The cache
// is per instance, so two Lambdas can disagree for up to a minute.
// ponytail: in-instance cache; move the counters into Streams-maintained
// aggregate items if the partition read ever gets slow.
const overviewTTL = time.Minute

// Overview aggregates the organization's partition in memory: DynamoDB has no
// GROUP BY, and the read is bounded by one tenant's data. An empty projectID
// counts the whole organization; a set one counts just that project, so the
// dashboard tiles agree with the inbox they sit above.
//
// The cache holds the rows, not the aggregate, and is keyed by organization
// alone: scoping by project must not turn one partition read into one per
// project every time someone uses the sidebar's switcher. Aggregating a slice
// already in memory is cheap; re-reading the partition is not.
func (s *Service) Overview(ctx context.Context, orgID, projectID string) (*Overview, error) {
	rows, err := s.orgRows(ctx, orgID)
	if err != nil {
		return nil, err
	}

	o := &Overview{ByStatus: map[string]int{}, ByType: map[string]int{}, Trend: []map[string]any{}}
	perDay := map[string]int{}
	cutoff := time.Now().UTC().AddDate(0, 0, -14)
	for _, it := range rows {
		if projectID != "" && it.ProjectID != projectID {
			continue
		}
		o.Total++
		o.ByStatus[it.Status]++
		o.ByType[it.Type]++
		if it.CreatedAt.After(cutoff) {
			perDay[it.CreatedAt.Format("2006-01-02")]++
		}
	}
	days := make([]string, 0, len(perDay))
	for d := range perDay {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days {
		o.Trend = append(o.Trend, map[string]any{"day": d, "count": perDay[d]})
	}
	return o, nil
}

// orgRows returns the counting fields of the organization's feedback, cached
// for overviewTTL.
func (s *Service) orgRows(ctx context.Context, orgID string) ([]counted, error) {
	s.overviewMu.Lock()
	if c, ok := s.overviewCache[orgID]; ok && time.Since(c.at) < overviewTTL {
		s.overviewMu.Unlock()
		return c.rows, nil
	}
	s.overviewMu.Unlock()

	// Projected in the query, not after it: without this the whole partition —
	// every message plus up to contextMaxBytes of context per row — is
	// materialised in memory once a minute just to be counted, and paid for in
	// read capacity. status and type are DynamoDB reserved words, hence the
	// name placeholders.
	var rows []counted
	if err := s.Store.QueryAll(ctx, &dynamodb.QueryInput{
		KeyConditionExpression:   aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ProjectionExpression:     aws.String("project_id, #st, #ty, created_at"),
		ExpressionAttributeNames: map[string]string{"#st": "status", "#ty": "type"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": platform.S("ORG#" + orgID), ":prefix": platform.S("FB#"),
		},
	}, &rows); err != nil {
		return nil, err
	}

	s.overviewMu.Lock()
	if s.overviewCache == nil {
		s.overviewCache = map[string]cachedItems{}
	}
	// Sweep while the lock is held: entries are only ever overwritten on read
	// of the same key, so a tenant that stops asking would otherwise stay
	// resident for the life of the process.
	for org, c := range s.overviewCache {
		if time.Since(c.at) >= overviewTTL {
			delete(s.overviewCache, org)
		}
	}
	s.overviewCache[orgID] = cachedItems{at: time.Now(), rows: rows}
	s.overviewMu.Unlock()
	return rows, nil
}
