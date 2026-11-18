package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })

	do := func(h http.Handler, method, path, origin, preflightFor string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if preflightFor != "" {
			r.Header.Set("Access-Control-Request-Method", preflightFor)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	open := CORS(nil, ok)
	list := CORS([]string{"https://app.example.com/", " https://other.example.com "}, ok)

	// Preflight for the SDK's submit call is answered, not passed to the handler.
	w := do(open, http.MethodOptions, "/v1/feedback", "https://anywhere.test", http.MethodPost)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://anywhere.test" {
		t.Fatalf("allow-origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("preflight must allow the SDK's request headers")
	}

	// The real request reaches the handler and can read Retry-After.
	w = do(open, http.MethodPost, "/v1/feedback", "https://anywhere.test", "")
	if w.Code != http.StatusTeapot {
		t.Fatalf("actual request status = %d, want handler to run", w.Code)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got != "Retry-After, X-Request-Id" {
		t.Fatalf("expose-headers = %q", got)
	}

	// Attachment bytes go straight to object storage, so this server no longer
	// answers for them; the bucket's own CORS rules apply instead.
	if w := do(open, http.MethodOptions, "/v1/uploads/abc", "https://anywhere.test", http.MethodPut); w.Code == http.StatusNoContent {
		t.Fatal("upload route is gone; CORS must not claim it")
	}

	// Admin routes stay same-origin: no CORS headers, handler still runs.
	w = do(open, http.MethodGet, "/v1/me", "https://anywhere.test", "")
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("admin route must not be exposed cross-origin")
	}
	if w.Code != http.StatusTeapot {
		t.Fatalf("admin route status = %d, want handler to run", w.Code)
	}

	// Allowlist: trimmed entries match, everything else is refused at preflight.
	if w := do(list, http.MethodOptions, "/v1/feedback", "https://app.example.com", http.MethodPost); w.Code != http.StatusNoContent {
		t.Fatalf("allowlisted origin status = %d, want 204", w.Code)
	}
	if w := do(list, http.MethodOptions, "/v1/feedback", "https://other.example.com", http.MethodPost); w.Code != http.StatusNoContent {
		t.Fatalf("second allowlisted origin status = %d, want 204", w.Code)
	}
	w = do(list, http.MethodOptions, "/v1/feedback", "https://evil.test", http.MethodPost)
	if w.Code != http.StatusForbidden {
		t.Fatalf("rejected origin status = %d, want 403", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("rejected origin must not get allow-origin")
	}
}

// Behind CloudFront and the Lambda Web Adapter, RemoteAddr is the same
// loopback address for every request; without the header the per-IP rate limit
// collapses into one bucket per project.
func TestClientIPPrefersForwardedFor(t *testing.T) {
	for _, c := range []struct {
		name, xff, remote, want string
	}{
		{"no header falls back to the peer", "", "203.0.113.9:51234", "203.0.113.9"},
		{"single hop", "198.51.100.7", "127.0.0.1:8080", "198.51.100.7"},
		{"leftmost hop wins", "198.51.100.7, 70.41.3.18, 127.0.0.1", "127.0.0.1:8080", "198.51.100.7"},
		{"surrounding space is trimmed", "  198.51.100.7 , 70.41.3.18", "127.0.0.1:8080", "198.51.100.7"},
		{"empty leftmost hop falls back", " , 70.41.3.18", "127.0.0.1:8080", "127.0.0.1"},
		{"unparseable peer is returned whole", "", "not-a-host-port", "not-a-host-port"},
	} {
		r := httptest.NewRequest("POST", "/v1/feedback", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(r); got != c.want {
			t.Errorf("%s: ClientIP() = %q, want %q", c.name, got, c.want)
		}
	}
}
