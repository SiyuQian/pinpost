package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/project"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"
)

func newStore(t *testing.T) *platform.Store {
	t.Helper()
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		os.Setenv("AWS_ENDPOINT_URL", "http://localhost:8000")
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" {
		os.Setenv("AWS_ACCESS_KEY_ID", "pinpost")
		os.Setenv("AWS_SECRET_ACCESS_KEY", "pinpost-secret")
	}
	cfg, err := platform.AWSConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	name := "pinpost-api-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	store := platform.NewStore(cfg, name)
	if err := platform.EnsureTable(context.Background(), store); err != nil {
		t.Skipf("dynamodb unreachable: %v", err)
	}
	t.Cleanup(func() {
		store.DB.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: &name})
	})
	return store
}

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
	store := newStore(t)

	hash, err := auth.HashPassword("hunter2-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	member := auth.NewMemberItem("org-1", "mem-1", "ada@example.com", "Ada", "owner", hash)
	if err := store.Put(context.Background(), member); err != nil {
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
		cfg, err := platform.AWSConfig(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// A store pointed at a table that does not exist: authentication
		// succeeds, then session() fails reading the organization.
		broken := platform.NewStore(cfg, "pinpost-missing-"+strings.ReplaceAll(uuid.NewString(), "-", "")[:12])
		w := login(&Server{
			Auth:    &auth.Service{Store: store},
			Project: &project.Service{Store: broken},
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
			Auth:    &auth.Service{Store: store},
			Project: &project.Service{Store: store},
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
