package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// APIError is the stable machine-readable error envelope.
type APIError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
	Status  int               `json:"-"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func Errf(status int, code, message string) *APIError {
	return &APIError{Code: code, Message: message, Status: status}
}

func ValidationErr(fields map[string]string) *APIError {
	return &APIError{Code: "validation_failed", Message: "The request is invalid", Status: 422, Fields: fields}
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	apiErr, ok := err.(*APIError)
	if !ok {
		slog.Error("internal error", "error", err, "request_id", RequestID(r.Context()))
		apiErr = Errf(500, "internal_error", "Something went wrong")
	}
	WriteJSON(w, apiErr.Status, map[string]any{"error": map[string]any{
		"code":       apiErr.Code,
		"message":    apiErr.Message,
		"request_id": RequestID(r.Context()),
		"fields":     apiErr.Fields,
	}})
}

// Handler adapts an error-returning handler into http.Handler.
func Handler(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			WriteError(w, r, err)
		}
	}
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8)
		rand.Read(buf)
		id := "req_" + hex.EncodeToString(buf)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		w.Header().Set("X-Request-Id", id)
		start := time.Now()
		next.ServeHTTP(w, r.WithContext(ctx))
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "request_id", id, "duration_ms", time.Since(start).Milliseconds())
	})
}

// RateLimiter counts hits per key in a fixed one-minute window, in memory.
// It is abuse control, not a security boundary, so a restart forgetting the
// counts is fine.
//
// ponytail: per process — N replicas behind a load balancer allow N× the
// limit. Move the counter into Postgres if that ever matters.
type RateLimiter struct {
	Limit int

	mu     sync.Mutex
	window int64
	hits   map[string]int
}

func NewRateLimiter(limit int) *RateLimiter {
	return &RateLimiter{Limit: limit}
}

func (rl *RateLimiter) Allow(key string) bool {
	window := time.Now().Unix() / 60
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if window != rl.window {
		// A new minute: the old counts are simply dropped, which is also
		// what keeps the map from growing.
		rl.window, rl.hits = window, map[string]int{}
	}
	rl.hits[key]++
	return rl.hits[key] <= rl.Limit
}

// ClientIP reports the address to attribute a request to. Behind a reverse
// proxy, RemoteAddr is the proxy and is the same for every request, so the
// leftmost X-Forwarded-For hop is the only thing that identifies the caller.
// The header is only trustworthy when nothing reaches this server except
// through the proxy (README, Self-hosting); a direct caller can forge it,
// which is acceptable for abuse counting and not for authorization.
func ClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		first, _, _ := strings.Cut(fwd, ",")
		if ip := strings.TrimSpace(first); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// publicIngest reports whether a request targets one of the ingestion
// endpoints the browser SDK calls. Those run on customer origins; every other
// route belongs to the admin console, which this server hosts same-origin.
// Attachment bytes are not among them: they go straight to object storage,
// whose own CORS configuration governs them.
func publicIngest(method, path string) bool {
	switch method {
	case http.MethodGet:
		return path == "/v1/sdk/config"
	case http.MethodPost:
		return path == "/v1/feedback" || path == "/v1/attachment-uploads"
	}
	return false
}

// CORS answers preflights and tags responses for the public ingestion
// endpoints. An empty allowlist permits any origin, which is the honest
// default: the write key is public by design and CORS never stopped anyone
// from posting with curl. Set an allowlist to keep casual embedders out.
//
// Retry-After is exposed because the SDK's backoff reads it, and cross-origin
// JS cannot see a non-safelisted response header otherwise.
func CORS(origins []string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range origins {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			allowed[o] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
		method := r.Method
		if preflight {
			method = r.Header.Get("Access-Control-Request-Method")
		}
		if origin == "" || !publicIngest(method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Add("Vary", "Origin")
		if len(allowed) > 0 && !allowed[origin] {
			// No CORS headers: the browser blocks it. Preflights end here so a
			// rejected origin never reaches the handler at all.
			if preflight {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Expose-Headers", "Retry-After, X-Request-Id")
		if preflight {
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Pinpost-Write-Key, Idempotency-Key")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
