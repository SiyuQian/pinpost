package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/SiyuQian/pinpost/internal/platform/pgtest"
	"github.com/SiyuQian/pinpost/internal/project"
)

// TestLoginSetsCookieOnlyOnSuccess pins the contract for Login: the response
// body is built before SetSessionCookie, so nothing that can fail runs after
// the browser is authenticated. A store error while reading the organization
// must produce a plain failure — no cookie — never 登录失败 on a response that
// already set one.
//
// GoogleLogin is not exercised here: it shares session() and the same ordering,
// but reaching it needs a Google-signed ID token that verifyGoogleIDToken
// checks against Google's own endpoint, and there is no seam to stub. If that
// seam ever appears, this is the case to copy.
func TestLoginSetsCookieOnlyOnSuccess(t *testing.T) {
	db := pgtest.New(t)
	if _, err := project.Provision(context.Background(), db, project.Params{
		OrgName: "Ada Org", ProjectName: "Default", Environment: "development",
		Email: "ada@example.com", Password: "hunter2-long-enough",
	}); err != nil {
		t.Fatal(err)
	}

	login := func(s *Server) *httptest.ResponseRecorder {
		t.Helper()
		body := strings.NewReader(`{"email":"ada@example.com","password":"hunter2-long-enough"}`)
		r := httptest.NewRequest("POST", "/v1/auth/login", body)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Login(w, r)
		return w
	}

	t.Run("a failed organization read sets no cookie", func(t *testing.T) {
		// A pool on an empty schema, where the organizations table does not
		// exist: authentication succeeds, then session() fails reading the
		// organization.
		w := login(&Server{
			Auth:    &auth.Service{DB: db},
			Project: &project.Service{DB: pgtest.Schema(t)},
		})

		if w.Code == 200 {
			t.Fatalf("status = 200, want a failure when the organization read fails")
		}
		if c := w.Result().Header.Get("Set-Cookie"); c != "" {
			t.Fatalf("Set-Cookie = %q, want none on a failed response", c)
		}
	})

	t.Run("success sets the cookie and answers the shared session shape", func(t *testing.T) {
		w := login(&Server{
			Auth:    &auth.Service{DB: db},
			Project: &project.Service{DB: db},
		})

		if w.Code != 200 {
			t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
		}
		if c := w.Result().Header.Get("Set-Cookie"); !strings.Contains(c, "pinpost_session=") {
			t.Fatalf("Set-Cookie = %q, want the session cookie", c)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"member", "organization"} {
			if _, ok := got[k]; !ok {
				t.Fatalf("body %s missing %q", w.Body.String(), k)
			}
		}
	})
}
