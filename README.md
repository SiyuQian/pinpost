# Pinpost

Pinpost is a self-hosted feedback inbox for web and mobile apps. Embed a feedback widget or use the API to collect bug reports, suggestions, ratings and attachments. Your team can search, assign and resolve reports in a shared dashboard.

This repository contains the Pinpost server, admin dashboard and HTTP API contract.

- **Collect feedback** from your app using the HTTP API or a client SDK. Each project has its own write key and SDK settings.
- **Triage reports** with inbox filters, bulk edits, internal notes, an activity timeline and submission trends.
- **Connect other tools** with signed webhooks, delivery history and replay.

## Quick start

You need Git and Docker with Compose. Run these commands on your machine to build Pinpost and start it with PostgreSQL and RustFS.

```bash
git clone https://github.com/SiyuQian/pinpost.git
cd pinpost
export PINPOST_ADMIN_EMAIL=you@example.com
export PINPOST_ADMIN_PASSWORD='replace-with-your-own-password'
docker compose up -d --build
```

Choose your own password before running the command. On a fresh database, Pinpost applies migrations and creates the first owner, organization and project. The Compose stack uses local ports **8787** for Pinpost, **5432** for PostgreSQL and **9000** for RustFS.

1. Sign in to the [admin dashboard](http://localhost:8787/admin/) with the account you configured.
2. Open **嵌入与集成** (Embed & Integrations) and copy your project's write key.
3. Replace the placeholder below and submit a report. It should appear under **反馈** (Feedback) in the dashboard.

```bash
curl -fsS http://localhost:8787/v1/feedback \
  -H 'X-Pinpost-Write-Key: paste-your-project-write-key' \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: readme-first-feedback' \
  --data '{"type":"bug","message":"My first Pinpost report"}'
```

Use a new idempotency key for each report. Reusing a key for the same project returns the original report. The write key is intended for client-side use and grants access to the ingestion API. Dashboard access uses a separate member session.

Run `docker compose down` to stop the stack and keep its data volumes.

For the demo tenant used by the SDK integration tests, follow [Development](#development).

## Integrate Pinpost

- **HTTP API.** Use the [OpenAPI contract](api/openapi.yaml) to integrate from any language. The [quick start](#quick-start) includes a complete submission example.
- **Client SDKs.** See [Pinpost SDK](https://github.com/SiyuQian/pinpost-sdk) for setup guides and examples for `@pinpost/core`, `@pinpost/react` and `@pinpost/react-native`. Open **嵌入与集成** (Embed & Integrations) in the dashboard for snippets with your Pinpost URL and project write key.

### Attachments

Clients request a presigned URL and upload directly to object storage. Pinpost accepts JPEG, PNG, WebP and plain text files up to 10 MiB each. The background worker uses signature-based scanning, and the dashboard allows downloads once the scan marks a file clean. Unclaimed upload records are swept after 24 hours. The corresponding objects remain in storage.

### Webhooks

Owners and admins can manage subscriptions in the dashboard. Available events are `feedback.created.v1`, `feedback.status_changed.v1`, `feedback.assigned.v1` and `feedback.note_added.v1`.

- Verify `X-Pinpost-Signature` against `v1=<hex HMAC-SHA256>`, computed with the endpoint secret over `<X-Pinpost-Timestamp>.<raw request body>`. Pinpost returns the secret when you create an endpoint.
- Dedupe deliveries using `X-Pinpost-Event-Id`. Delivery is at least once, and retries and manual replays keep the same event ID.
- Network errors and HTTP 408, 429 and 5xx responses are retried up to six times. Ten consecutive failures pause an endpoint. Already queued deliveries wait for it to resume. Events that occur during the pause are skipped. Delivery history is retained for 30 days.

## Self-hosting

Pinpost runs as one application container with PostgreSQL and an S3-compatible object store. The container serves the API and dashboard and runs the webhook and attachment workers. The supplied [Compose file](docker-compose.yml) bundles PostgreSQL and RustFS for a local installation.

For an internet-facing installation:

- Put Pinpost behind an HTTPS reverse proxy and restrict direct access to the app and database ports. Have the proxy set `X-Forwarded-Proto: https` for secure session cookies and overwrite `X-Forwarded-For` with the trusted client address for rate limiting.
- Replace the sample database and RustFS credentials. Use your own owner password and leave demo seeding disabled.
- Set `S3_PUBLIC_ENDPOINT_URL` to the HTTPS object storage endpoint the browser can reach. Configure allowed app origins in both `CORS_ALLOWED_ORIGINS` and your object store's CORS rules.
- Back up PostgreSQL and the attachment bucket together. The database stores references to the objects.

Check [`/healthz`](http://localhost:8787/healthz) to confirm the server can reach PostgreSQL.

### Environment variables

These variables configure the Pinpost process. The supplied Compose file sets or forwards the variables listed under the `pinpost` service. Use a Compose override to pass additional settings to the container.

| Variable | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | Required | PostgreSQL connection URL. Migrations run on startup. |
| `ADDR` | `:8787` | HTTP listen address. |
| `PINPOST_ADMIN_EMAIL`, `PINPOST_ADMIN_PASSWORD` | — | First owner credentials. They apply only before the database has been bootstrapped. |
| `PINPOST_ORG_NAME`, `PINPOST_PROJECT_NAME` | `Acme`, `Acme App` | Names for the first organization and project. |
| `PINPOST_WRITE_KEY` | Generated | Optional initial project write key. |
| `PINPOST_BUCKET` | `pinpost-attachments` | Bucket for attachments. With a custom endpoint, Pinpost attempts to create the bucket at startup. |
| `S3_ENDPOINT_URL` | AWS S3 | Server-side S3-compatible endpoint. Setting it enables path-style addressing. |
| `S3_PUBLIC_ENDPOINT_URL` | Server endpoint | Browser-facing endpoint used to sign upload and download URLs. |
| `AWS_REGION` | `us-east-1` | Object storage region. Credentials use the AWS SDK's standard provider chain. |
| `CORS_ALLOWED_ORIGINS` | Any origin | Comma-separated browser origins for the ingestion API. Object storage has its own CORS settings. |
| `PINPOST_RATE_LIMIT` | `60` | Ingestion requests per project and IP per minute, counted separately by each replica. |
| `PINPOST_GOOGLE_CLIENT_ID`, `PINPOST_GOOGLE_ALLOWED_EMAILS` | — | Google OAuth web client ID and comma-separated sign-in allowlist. For Google-only setup, leave the owner credentials unset. The first allowed address becomes the owner on a fresh database. |
| `PINPOST_SEED_DEMO` | `0` | Set to `1` for the local demo tenant and development scan endpoint. |

## Development

Use Go 1.25.6 or newer, Node.js 22 as used in CI, and Docker Compose. Run the commands from the repository root. Stop the full Compose stack first if it is running locally on port 8787.

```bash
docker compose up -d --wait postgres s3
npm --prefix web/admin ci
npm --prefix web/admin run build
source ./local.env
PINPOST_SEED_DEMO=1 go run ./cmd/server
```

On a fresh local database, demo mode creates `admin@example.com` with password `pinpost-demo`. Submit feedback at [`/demo/`](http://localhost:8787/demo/) and sign in at [`/admin/`](http://localhost:8787/admin/) to see it. The demo uses a fixed write key and is intended for local development.

For dashboard hot reload, keep the Go server running and use `npm --prefix web/admin run dev`. Open [`localhost:5473/admin/`](http://localhost:5473/admin/). Vite proxies API requests to port 8787 by default. Set `PINPOST_API_URL` to use another API origin.

| Path | Contents |
| --- | --- |
| [`cmd/server/`](cmd/server/) | Go entry point, static pages and contract tests |
| [`internal/`](internal/) | Go feature packages and [PostgreSQL migrations](internal/platform/migrations/) |
| [`web/admin/`](web/admin/) and [`web/demo/`](web/demo/) | React dashboard and browser submission demo |
| [`api/openapi.yaml`](api/openapi.yaml) | API contract for SDK ingestion and dashboard operations |
| [System design](docs/feedback-system-design.md) | Architecture, design decisions and roadmap (Chinese) |

For tests and contribution steps, see [CONTRIBUTING.md](CONTRIBUTING.md). If you change the OpenAPI spec, regenerate the Go router and types with `go generate ./internal/api`.

## License

Pinpost is © DE TECHNOLOGY LIMITED. This repository is licensed under [AGPL-3.0-only](LICENSE). The client SDKs are licensed under [MIT](https://github.com/SiyuQian/pinpost-sdk/blob/main/LICENSE) and can be embedded in closed-source apps.

Contributions require a [Contributor License Agreement](CLA.md). Report vulnerabilities privately using the instructions in [SECURITY.md](SECURITY.md).
