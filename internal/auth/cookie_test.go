package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Secure flag is decided by a forwarded header rather than r.TLS, because
// the TLS-terminating reverse proxy hands this server a plain HTTP request.
func TestSessionCookieSecure(t *testing.T) {
	for _, c := range []struct {
		name  string
		proto string
		want  bool
	}{
		{"behind the function url", "https", true},
		{"local development", "", false},
		{"plain http proxy", "http", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/auth/login", nil)
			if c.proto != "" {
				r.Header.Set("X-Forwarded-Proto", c.proto)
			}
			w := httptest.NewRecorder()
			SetSessionCookie(w, r, "tok")
			got := w.Result().Cookies()[0]
			if got.Secure != c.want {
				t.Errorf("Secure = %v, want %v", got.Secure, c.want)
			}
			if !got.HttpOnly || got.SameSite != http.SameSiteLaxMode {
				t.Errorf("lost HttpOnly/SameSite: %+v", got)
			}

			// Clearing must repeat those attributes or the browser keeps both.
			w = httptest.NewRecorder()
			ClearSessionCookie(w, r)
			cleared := w.Result().Cookies()[0]
			if cleared.Secure != c.want || !cleared.HttpOnly || cleared.MaxAge >= 0 {
				t.Errorf("clear cookie mismatch: %+v", cleared)
			}
		})
	}
}
