package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testPassword = "correct horse"

// authFixture is a database with one organization and a way to add members.
type authFixture struct {
	t     *testing.T
	db    *pgxpool.Pool
	s     *Service
	orgID string
	hash  string
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	db := pgtest.New(t)
	orgID, _ := pgtest.Tenant(t, db)
	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return &authFixture{t: t, db: db, s: &Service{DB: db}, orgID: orgID, hash: hash}
}

// member inserts an active member and returns its id.
func (f *authFixture) member(email string) string {
	f.t.Helper()
	id := uuid.NewString()
	if err := InsertMember(context.Background(), f.db,
		NewMemberItem(f.orgID, id, email, "Pat", "admin", f.hash)); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *authFixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(context.Background(), q, args...); err != nil {
		f.t.Fatal(err)
	}
}

func assertAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var e *platform.APIError
	if !errors.As(err, &e) || e.Status != status || e.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
}

func cookieReq(token string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	return r
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	ctx := context.Background()
	f := newAuthFixture(t)
	id := f.member("pat@example.com")
	disabledID := f.member("gone@example.com")
	f.exec(`UPDATE members SET status = 'disabled' WHERE id = $1`, disabledID)

	for _, tc := range []struct{ name, email, password string }{
		{"wrong password", "pat@example.com", "wrong"},
		{"unknown email", "nobody@example.com", testPassword},
		{"disabled member", "gone@example.com", testPassword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, m, err := f.s.Login(ctx, tc.email, tc.password)
			assertAPIError(t, err, 401, "invalid_credentials")
			if token != "" || m != nil {
				t.Fatalf("rejected login returned token %q member %v", token, m)
			}
		})
	}

	token, m, err := f.s.Login(ctx, "pat@example.com", testPassword)
	if err != nil || token == "" || m == nil || m.ID != id {
		t.Fatalf("valid login: token %q member %v err %v", token, m, err)
	}
}

func TestMemberFromRequestRejectsDeadSessions(t *testing.T) {
	ctx := context.Background()
	f := newAuthFixture(t)

	for _, tc := range []struct {
		name string
		kill func(t *testing.T, token, memberID string)
	}{
		{"expired", func(_ *testing.T, token, _ string) {
			f.exec(`UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE token_hash = $1`, sha256hex(token))
		}},
		{"disabled member", func(_ *testing.T, _, memberID string) {
			f.exec(`UPDATE members SET status = 'disabled' WHERE id = $1`, memberID)
		}},
		{"logged out", func(t *testing.T, token, _ string) {
			f.s.Logout(ctx, token)
			var n int
			if err := f.db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE token_hash = $1`, sha256hex(token)).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("session rows after logout = %d, want 0", n)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A member per case keeps disabling one from affecting the others.
			email := uuid.NewString() + "@example.com"
			id := f.member(email)
			token, _, err := f.s.Login(ctx, email, testPassword)
			if err != nil {
				t.Fatal(err)
			}
			m, err := f.s.MemberFromRequest(cookieReq(token))
			if err != nil || m.ID != id {
				t.Fatalf("live session: member %v err %v, want %s", m, err, id)
			}
			tc.kill(t, token, id)
			_, err = f.s.MemberFromRequest(cookieReq(token))
			assertAPIError(t, err, 401, "unauthenticated")
		})
	}
}

func TestSweepExpiredSessionsKeepsLiveOnes(t *testing.T) {
	ctx := context.Background()
	f := newAuthFixture(t)
	f.member("pat@example.com")
	expired, _, err := f.s.Login(ctx, "pat@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	live, _, err := f.s.Login(ctx, "pat@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE token_hash = $1`, sha256hex(expired))

	if err := f.s.SweepExpiredSessions(ctx); err != nil {
		t.Fatal(err)
	}
	for token, want := range map[string]int{expired: 0, live: 1} {
		var n int
		if err := f.db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE token_hash = $1`, sha256hex(token)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("session rows = %d, want %d", n, want)
		}
	}
}
