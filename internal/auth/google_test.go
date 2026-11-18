package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SiyuQian/pinpost/internal/platform"
)

func status(err error) int {
	var apiErr *platform.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

type fakeTransport struct {
	status int
	body   string
}

func (f fakeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     http.Header{},
	}, nil
}

// stubGoogle makes tokeninfo return a fixed payload for the duration of a test.
func stubGoogle(t *testing.T, body string) {
	t.Helper()
	prev := googleClient
	googleClient = &http.Client{Transport: fakeTransport{status: 200, body: body}}
	t.Cleanup(func() { googleClient = prev })
}

// Every case here is rejected before the store is touched, so a Service with no
// store is enough — and if a rejection ever stops short, the nil store panics
// instead of quietly signing someone in.
func TestLoginWithGoogleRejects(t *testing.T) {
	cfg := NewGoogleConfig("client-1", " Alice@Example.com , bob@example.com ")
	if len(cfg.Allowed) != 2 || cfg.Allowed[0] != "alice@example.com" {
		t.Fatalf("allowlist not normalized: %#v", cfg.Allowed)
	}

	cases := []struct {
		name string
		cfg  GoogleConfig
		body string
		want int
	}{
		{"disabled", NewGoogleConfig("", "a@b.com"), `{}`, 404},
		{"no allowlist", NewGoogleConfig("client-1", ""), `{}`, 404},
		{"wrong audience", cfg,
			`{"aud":"other-client","email":"alice@example.com","email_verified":"true"}`, 401},
		{"unverified email", cfg,
			`{"aud":"client-1","email":"alice@example.com","email_verified":"false"}`, 401},
		{"not on the allowlist", cfg,
			`{"aud":"client-1","email":"stranger@gmail.com","email_verified":"true"}`, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubGoogle(t, c.body)
			s := &Service{}
			_, _, err := s.LoginWithGoogle(context.Background(), c.cfg, "credential")
			if status(err) != c.want {
				t.Fatalf("got %v (status %d), want status %d", err, status(err), c.want)
			}
		})
	}
}

// A member seeded without a password must not be reachable with an empty one.
func TestEmptyPasswordIsUnusable(t *testing.T) {
	h, err := HashPassword("")
	if err != nil || h != "" {
		t.Fatalf("HashPassword(\"\") = %q, %v", h, err)
	}
	item := NewMemberItem("org", "mem", "a@b.com", "A", "admin", h)
	if item.PasswordHash != "" {
		t.Fatalf("stored hash %q", item.PasswordHash)
	}
}
