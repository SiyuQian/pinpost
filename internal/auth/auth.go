// Package auth covers Member sessions, roles and SDK Write Keys.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "pinpost_session"
	sessionTTL    = 12 * time.Hour
)

func sha256hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// HashWriteKey stores only a digest of the public write key.
func HashWriteKey(key string) string { return sha256hex(key) }

func NewWriteKey() string {
	buf := make([]byte, 18)
	rand.Read(buf)
	return "wk_" + hex.EncodeToString(buf)
}

// enumerationDecoyHash is a well-known published bcrypt vector. Its plaintext
// does not matter and it guards nothing — it exists only so the unknown-address
// login path spends the same time as a wrong password, instead of returning
// fast enough to answer "does this account exist?".
var enumerationDecoyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// HashPassword returns "" for an empty password, which is an unusable hash:
// bcrypt fails against it for every candidate, so a member seeded without a
// password can only sign in through Google. Hashing "" instead would make the
// empty string a working password.
func HashPassword(pw string) (string, error) {
	if pw == "" {
		return "", nil
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// ---- items ----

// ProjectItem is written twice: once under the organization partition for
// listing, once under WK#<digest> so an ingestion request authenticates with a
// single GetItem instead of a lookup plus a join.
type ProjectItem struct {
	platform.Key
	OrganizationID string `dynamodbav:"org_id"`
	ProjectID      string `dynamodbav:"project_id"`
	Name           string `dynamodbav:"name"`
	Environment    string `dynamodbav:"environment"`
	WriteKeyHash   string `dynamodbav:"write_key_hash"`
	// WriteKey is the plaintext key. It is a public identifier that ships in
	// client bundles (design §9.1), not a secret, so it is kept readable for
	// the console's install page. Empty on projects created before it was
	// stored; one rotation fills it in.
	WriteKey  string    `dynamodbav:"write_key,omitempty"`
	Settings  string    `dynamodbav:"settings"`
	CreatedAt time.Time `dynamodbav:"created_at"`
}

func ProjectKey(orgID, projID string) platform.Key {
	return platform.Key{PK: "ORG#" + orgID, SK: "PROJ#" + projID}
}

func WriteKeyKey(writeKey string) platform.Key {
	return platform.Key{PK: "WK#" + HashWriteKey(writeKey), SK: platform.Meta}
}

// MemberItem lives in the organization partition; GSI1 indexes it by email so
// login is a single Query.
type MemberItem struct {
	platform.Key
	GSI1PK         string    `dynamodbav:"gsi1pk"`
	GSI1SK         string    `dynamodbav:"gsi1sk"`
	MemberID       string    `dynamodbav:"member_id"`
	OrganizationID string    `dynamodbav:"org_id"`
	Email          string    `dynamodbav:"email"`
	Name           string    `dynamodbav:"name"`
	Role           string    `dynamodbav:"role"`
	Picture        string    `dynamodbav:"picture,omitempty"`
	Status         string    `dynamodbav:"status"`
	PasswordHash   string    `dynamodbav:"password_hash"`
	CreatedAt      time.Time `dynamodbav:"created_at"`
}

func NewMemberItem(orgID, memberID, email, name, role, passwordHash string) *MemberItem {
	email = NormalizeEmail(email)
	return &MemberItem{
		Key:            platform.Key{PK: "ORG#" + orgID, SK: "MEM#" + memberID},
		GSI1PK:         "EMAIL#" + email,
		GSI1SK:         "MEM#" + memberID,
		MemberID:       memberID,
		OrganizationID: orgID,
		Email:          email,
		Name:           name,
		Role:           role,
		Status:         "active",
		PasswordHash:   passwordHash,
		CreatedAt:      time.Now().UTC(),
	}
}

type sessionItem struct {
	platform.Key
	MemberID       string `dynamodbav:"member_id"`
	OrganizationID string `dynamodbav:"org_id"`
	ExpiresAt      int64  `dynamodbav:"expires_at"`
}

// ---- service ----

type ProjectCtx struct {
	ProjectID      string
	OrganizationID string
	Environment    string
	Settings       []byte
}

type Member struct {
	ID             string
	OrganizationID string
	Email          string
	Name           string
	Role           string
	Picture        string `json:",omitempty"`
}

// Service authenticates SDK write keys and dashboard members.
type Service struct{ Store *platform.Store }

// ProjectByWriteKey resolves the tenant scope for an ingestion request.
func (s *Service) ProjectByWriteKey(ctx context.Context, key string) (*ProjectCtx, error) {
	if !strings.HasPrefix(key, "wk_") {
		return nil, platform.Errf(401, "invalid_write_key", "Missing or malformed write key")
	}
	k := WriteKeyKey(key)
	var p ProjectItem
	found, err := s.Store.Get(ctx, k.PK, k.SK, &p)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, platform.Errf(401, "invalid_write_key", "Unknown write key")
	}
	settings := p.Settings
	if settings == "" {
		settings = "{}"
	}
	return &ProjectCtx{
		ProjectID:      p.ProjectID,
		OrganizationID: p.OrganizationID,
		Environment:    p.Environment,
		Settings:       json.RawMessage(settings),
	}, nil
}

// MembersByEmail returns every member with this address, through GSI1. There
// can be more than one: the index is keyed on the address alone, and nothing
// stops the same person from being a member of two organizations. Returning
// only the first would make one of those accounts silently unreachable.
func (s *Service) MembersByEmail(ctx context.Context, email string) ([]MemberItem, error) {
	var items []MemberItem
	err := s.Store.QueryAll(ctx, &dynamodb.QueryInput{
		IndexName:              aws.String("GSI1"),
		KeyConditionExpression: aws.String("gsi1pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": platform.S("EMAIL#" + NormalizeEmail(email)),
		},
	}, &items)
	return items, err
}

func (s *Service) Login(ctx context.Context, email, password string) (string, *Member, error) {
	invalid := platform.Errf(401, "invalid_credentials", "Email or password is incorrect")
	items, err := s.MembersByEmail(ctx, email)
	if err != nil {
		return "", nil, err
	}
	// Compare against every account on this address, so a person who belongs
	// to two organizations can reach both. Unknown addresses still pay for one
	// bcrypt against a fixed hash: skipping it turns response time into an
	// account-enumeration oracle, and the GSI read is sub-millisecond.
	var item *MemberItem
	for i := range items {
		if items[i].Status != "active" {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(items[i].PasswordHash), []byte(password)) == nil {
			item = &items[i]
			break
		}
	}
	if item == nil {
		bcrypt.CompareHashAndPassword(enumerationDecoyHash, []byte(password))
		return "", nil, invalid
	}
	token, err := s.newSession(ctx, item)
	if err != nil {
		return "", nil, err
	}
	return token, memberOf(item), nil
}

func memberOf(item *MemberItem) *Member {
	return &Member{ID: item.MemberID, OrganizationID: item.OrganizationID,
		Email: item.Email, Name: item.Name, Role: item.Role, Picture: item.Picture}
}

// newSession mints a session token for an already-authenticated member. The
// session lives in the table, not in process memory: two Lambda instances must
// see the same login.
func (s *Service) newSession(ctx context.Context, item *MemberItem) (string, error) {
	buf := make([]byte, 24)
	rand.Read(buf)
	token := hex.EncodeToString(buf)
	return token, s.Store.Put(ctx, &sessionItem{
		Key:            platform.Key{PK: "SESS#" + sha256hex(token), SK: platform.Meta},
		MemberID:       item.MemberID,
		OrganizationID: item.OrganizationID,
		ExpiresAt:      platform.TTL(sessionTTL),
	})
}

func (s *Service) Logout(ctx context.Context, token string) {
	if token == "" {
		return
	}
	s.Store.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &s.Store.Table,
		Key:       platform.KeyAV("SESS#"+sha256hex(token), platform.Meta),
	})
}

// MemberFromRequest resolves the session cookie into an active member.
func (s *Service) MemberFromRequest(r *http.Request) (*Member, error) {
	expired := platform.Errf(401, "unauthenticated", "Session expired")
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, platform.Errf(401, "unauthenticated", "Sign in required")
	}
	ctx := r.Context()
	var sess sessionItem
	// TTL deletion is eventual, so the expiry is still checked here.
	found, err := s.Store.Get(ctx, "SESS#"+sha256hex(c.Value), platform.Meta, &sess)
	if err != nil {
		return nil, err
	}
	if !found || sess.ExpiresAt < time.Now().Unix() {
		return nil, expired
	}
	var item MemberItem
	found, err = s.Store.Get(ctx, "ORG#"+sess.OrganizationID, "MEM#"+sess.MemberID, &item)
	if err != nil {
		return nil, err
	}
	if !found || item.Status != "active" {
		return nil, expired
	}
	return memberOf(&item), nil
}

// CanWrite reports whether the role may modify feedback.
func (m *Member) CanWrite() bool {
	return m.Role == "owner" || m.Role == "admin" || m.Role == "agent"
}

// secureCookies reports whether the session cookie may carry the Secure flag.
// The Lambda Web Adapter speaks plain HTTP to this server even when the caller
// used HTTPS, so r.TLS is always nil in a deployment; the Function URL sets
// X-Forwarded-Proto, and its absence means local http development. A direct
// caller forging the header only makes its own cookie stricter.
func secureCookies(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureCookies(r),
		MaxAge: int(sessionTTL.Seconds()),
	})
}

// ClearSessionCookie must repeat the attributes the cookie was set with, or the
// browser keeps the original alongside this one.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureCookies(r),
		MaxAge: -1,
	})
}

func SessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

// ConstantTimeEqual compares two secrets without leaking timing.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
