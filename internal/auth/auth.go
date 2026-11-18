// Package auth covers Member sessions, roles and SDK Write Keys.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// ---- records ----

// MemberItem is a members row. Login reads the password hash, so it is kept
// apart from Member, the view the rest of the code passes around.
type MemberItem struct {
	MemberID       string    `db:"id"`
	OrganizationID string    `db:"organization_id"`
	Email          string    `db:"email"`
	Name           string    `db:"name"`
	Role           string    `db:"role"`
	Picture        string    `db:"picture"`
	Status         string    `db:"status"`
	PasswordHash   string    `db:"password_hash"`
	CreatedAt      time.Time `db:"created_at"`
}

const memberCols = `id, organization_id, email, name, role, picture, status, password_hash, created_at`

func NewMemberItem(orgID, memberID, email, name, role, passwordHash string) *MemberItem {
	return &MemberItem{
		MemberID:       memberID,
		OrganizationID: orgID,
		Email:          NormalizeEmail(email),
		Name:           name,
		Role:           role,
		Status:         "active",
		PasswordHash:   passwordHash,
		CreatedAt:      time.Now().UTC(),
	}
}

// InsertMember writes a new member, inside the caller's transaction or not.
func InsertMember(ctx context.Context, db platform.DBTX, m *MemberItem) error {
	_, err := db.Exec(ctx, `INSERT INTO members (`+memberCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		m.MemberID, m.OrganizationID, m.Email, m.Name, m.Role, m.Picture, m.Status, m.PasswordHash, m.CreatedAt)
	return err
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
type Service struct{ DB *pgxpool.Pool }

// ProjectByWriteKey resolves the tenant scope for an ingestion request. It is
// one of the few unscoped reads: the write key is what establishes the tenant.
func (s *Service) ProjectByWriteKey(ctx context.Context, key string) (*ProjectCtx, error) {
	if !strings.HasPrefix(key, "wk_") {
		return nil, platform.Errf(401, "invalid_write_key", "Missing or malformed write key")
	}
	var p ProjectCtx
	err := s.DB.QueryRow(ctx, `SELECT id, organization_id, environment, settings
		FROM projects WHERE write_key = $1`, key).
		Scan(&p.ProjectID, &p.OrganizationID, &p.Environment, &p.Settings)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, platform.Errf(401, "invalid_write_key", "Unknown write key")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// MembersByEmail returns every member with this address. There can be more
// than one: nothing stops the same person from being a member of two
// organizations, and returning only the first would make one of those
// accounts silently unreachable. Unscoped by design — login has no tenant yet.
func (s *Service) MembersByEmail(ctx context.Context, email string) ([]MemberItem, error) {
	rows, _ := s.DB.Query(ctx, `SELECT `+memberCols+` FROM members WHERE email = $1 ORDER BY id`,
		NormalizeEmail(email))
	return pgx.CollectRows(rows, pgx.RowToStructByName[MemberItem])
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
	// account-enumeration oracle, and the indexed read is sub-millisecond.
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

// newSession mints a session token for an already-authenticated member. Only
// the token's digest is stored.
func (s *Service) newSession(ctx context.Context, item *MemberItem) (string, error) {
	buf := make([]byte, 24)
	rand.Read(buf)
	token := hex.EncodeToString(buf)
	_, err := s.DB.Exec(ctx, `INSERT INTO sessions (token_hash, member_id, organization_id, expires_at)
		VALUES ($1, $2, $3, $4)`, sha256hex(token), item.MemberID, item.OrganizationID, time.Now().Add(sessionTTL))
	return token, err
}

func (s *Service) Logout(ctx context.Context, token string) {
	if token == "" {
		return
	}
	s.DB.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, sha256hex(token))
}

// MemberFromRequest resolves the session cookie into an active member. Expired
// rows linger until the janitor removes them, so the expiry is checked here.
func (s *Service) MemberFromRequest(r *http.Request) (*Member, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, platform.Errf(401, "unauthenticated", "Sign in required")
	}
	var m Member
	err = s.DB.QueryRow(r.Context(), `SELECT m.id, m.organization_id, m.email, m.name, m.role, m.picture
		FROM sessions s JOIN members m ON m.id = s.member_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND m.status = 'active'`, sha256hex(c.Value)).
		Scan(&m.ID, &m.OrganizationID, &m.Email, &m.Name, &m.Role, &m.Picture)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, platform.Errf(401, "unauthenticated", "Session expired")
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
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

// SweepExpiredSessions deletes sessions past their expiry. Lookups already
// ignore them; this only keeps the table from growing.
func (s *Service) SweepExpiredSessions(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}
