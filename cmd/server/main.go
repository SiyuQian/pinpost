package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SiyuQian/pinpost/internal/api"
	"github.com/SiyuQian/pinpost/internal/attachment"
	"github.com/SiyuQian/pinpost/internal/auth"
	"github.com/SiyuQian/pinpost/internal/feedback"
	"github.com/SiyuQian/pinpost/internal/outbox"
	"github.com/SiyuQian/pinpost/internal/platform"
	"github.com/SiyuQian/pinpost/internal/project"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newServer wires the domain slices behind the generated spec router.
// Shared with the contract test, which also reaches into the slices directly.
func newServer(store *platform.Store, db *pgxpool.Pool, s3c *s3.Client, bucket string) *api.Server {
	att := &attachment.Service{Store: store, S3: s3c, Presign: s3.NewPresignClient(s3c), Bucket: bucket}
	proj := &project.Service{DB: db}
	return &api.Server{
		Auth:          &auth.Service{DB: db},
		Project:       proj,
		Feedback:      &feedback.Service{Store: store, Attachment: att, Members: proj},
		Attachment:    att,
		Outbox:        &outbox.Service{Store: store},
		IngestLimiter: platform.NewRateLimiter(store, ingestLimit()),
		Google:        googleConfig(),
	}
}

// googleConfig reads the Google Sign-In allowlist. Both variables must be set:
// without a client id the token cannot be checked, and without an allowlist
// there is nobody to let in.
func googleConfig() auth.GoogleConfig {
	return auth.NewGoogleConfig(env("PINPOST_GOOGLE_CLIENT_ID", ""), env("PINPOST_GOOGLE_ALLOWED_EMAILS", ""))
}

// ingestLimit is the per project+IP ingestion budget for a one-minute window.
func ingestLimit() int {
	n, err := strconv.Atoi(env("PINPOST_RATE_LIMIT", "60"))
	if err != nil || n <= 0 {
		return 60
	}
	return n
}

// newMux mounts the API plus the static apps.
func newMux(srv *api.Server) *http.ServeMux {
	mux := http.NewServeMux()
	srv.Handler(mux)

	// Static apps: the admin SPA (built by Vite) and the SDK demo. In the
	// deployed stack CloudFront serves these from S3 and never reaches here.
	mux.Handle("GET /admin/", http.StripPrefix("/admin/", spaHandler(adminDist)))
	mux.Handle("GET /demo/", http.StripPrefix("/demo/", http.FileServer(http.Dir("web/demo"))))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	// Local development has no S3 event notification, so attachments would
	// stay pending forever. Off in any deployed stack, where the scan Lambda
	// does this.
	if os.Getenv("S3_ENDPOINT_URL") != "" {
		mux.Handle("POST /dev/scan/{id}", platform.Handler(func(w http.ResponseWriter, r *http.Request) error {
			if err := srv.Attachment.VerifyByID(r.Context(), r.PathValue("id")); err != nil {
				return err
			}
			platform.WriteJSON(w, 200, map[string]string{"status": "scanned"})
			return nil
		}))
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	return mux
}

// adminDist holds the Vite build output for web/admin. It is served from disk
// rather than embedded so `go build` never depends on a node toolchain.
const adminDist = "web/admin/dist"

// spaHandler serves static assets and falls back to index.html for unknown
// paths, which is what a client-routed SPA needs. When the app has not been
// built yet it says so instead of returning a bare 404.
func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(index); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "The admin app has not been built yet.\n\nRun:\n  npm --prefix web/admin ci\n  npm --prefix web/admin run build\n\nOr for live reload during development:\n  npm --prefix web/admin run dev\n")
			return
		}

		// Serve real files (hashed assets, favicon); anything else is a route.
		clean := path.Clean("/" + r.URL.Path)
		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(clean))); err == nil && info.Mode().IsRegular() {
			files.ServeHTTP(w, r)
			return
		}
		// Unknown path: hand the SPA its entry point and let the client route.
		http.ServeFile(w, r, index)
	})
}

// seed provisions the first tenant on an empty database. The demo tenant ships
// with credentials published in the README, so a deployment must opt into it;
// otherwise the owner comes from the environment.
func seed(ctx context.Context, db *pgxpool.Pool) error {
	if env("PINPOST_SEED_DEMO", "0") == "1" {
		return project.SeedDemo(ctx, db)
	}
	email, password := env("PINPOST_ADMIN_EMAIL", ""), env("PINPOST_ADMIN_PASSWORD", "")
	switch c := googleConfig(); {
	case email != "" && password != "":
		// An explicit owner still needs its password: seeding one without a
		// usable hash and without Google would leave nobody able to sign in.
	case email == "" && c.Enabled():
		// A Google-only deployment sets no password: seed the first allowed
		// address as owner with an unusable hash, so the organization exists
		// and the rest of the allowlist can join itself on first sign-in.
		// Gated on Enabled(), not on the allowlist alone — an allowlist with no
		// client id admits nobody, and the marker this writes is permanent.
		email, password = c.Allowed[0], ""
	default:
		slog.Info("no seed configured; set PINPOST_GOOGLE_CLIENT_ID and PINPOST_GOOGLE_ALLOWED_EMAILS for Google sign-in, PINPOST_ADMIN_EMAIL and PINPOST_ADMIN_PASSWORD for a password owner, or PINPOST_SEED_DEMO=1 for the demo tenant")
		return nil
	}
	return project.Seed(ctx, db, project.Params{
		OrgName: env("PINPOST_ORG_NAME", "Acme"), ProjectName: env("PINPOST_PROJECT_NAME", "Acme App"),
		Environment: "production", Email: email, Password: password,
		WriteKey: env("PINPOST_WRITE_KEY", ""),
	})
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx := context.Background()

	cfg, err := platform.AWSConfig(ctx)
	if err != nil {
		slog.Error("aws config failed", "error", err)
		os.Exit(1)
	}
	store := platform.NewStore(cfg, env("PINPOST_TABLE", "pinpost"))
	// A custom endpoint means DynamoDB Local. The container is in-memory, so the table survives server
	// restarts but not `docker compose down` — which is why seed.go pins the
	// demo UUIDs instead of generating them.
	if os.Getenv("AWS_ENDPOINT_URL") != "" {
		if err := platform.EnsureTable(ctx, store); err != nil {
			slog.Error("table setup failed", "error", err)
			os.Exit(1)
		}
	}
	db, err := platform.OpenDB(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		slog.Error("database connection failed; set DATABASE_URL", "error", err)
		os.Exit(1)
	}
	if err := platform.Migrate(ctx, db, platform.Migrations); err != nil {
		slog.Error("database migration failed", "error", err)
		os.Exit(1)
	}
	if err := seed(ctx, db); err != nil {
		slog.Error("seed failed", "error", err)
		os.Exit(1)
	}

	s3c, bucket := platform.NewS3(cfg), env("PINPOST_BUCKET", "pinpost-attachments")
	if os.Getenv("S3_ENDPOINT_URL") != "" {
		if err := platform.EnsureBucket(ctx, s3c, bucket); err != nil {
			slog.Error("bucket setup failed", "error", err)
			os.Exit(1)
		}
	}
	mux := newMux(newServer(store, db, s3c, bucket))

	// The browser SDK posts from customer origins; the admin console does not.
	origins := strings.Split(env("CORS_ALLOWED_ORIGINS", ""), ",")

	addr := env("ADDR", ":8787")
	srv := &http.Server{
		Addr:              addr,
		Handler:           platform.Middleware(platform.CORS(origins, mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	slog.Info("pinpost server listening", "addr", addr, "admin", "http://localhost"+addr+"/admin/", "demo", "http://localhost"+addr+"/demo/")
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}
