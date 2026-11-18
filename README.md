# Pinpost

An embeddable cross-platform feedback system. See the design doc at [docs/feedback-system-design.md](docs/feedback-system-design.md) — note that its persistence, attachment and webhook-delivery chapters describe the original PostgreSQL design and are superseded by this README; see the banner at the top of that file.

This repository implements **Phase 1** end to end on a serverless stack: a Go
modular monolith running on Lambda behind CloudFront, a **DynamoDB single
table**, **S3 direct-upload attachments**, an admin dashboard, client SDKs
(`@pinpost/core`, `@pinpost/react`, `@pinpost/react-native`) and attachment
malware scanning with quarantine.

## Quick start

```bash
docker compose up -d --wait          # Postgres (:5432), DynamoDB Local (:8000), RustFS (:9000)
npm --prefix web/admin ci            # admin console dependencies (first run only)
npm --prefix web/admin run build     # build the admin console into web/admin/dist
source ./local.env                   # point the AWS SDK at those two containers
PINPOST_SEED_DEMO=1 go run ./cmd/server   # API + static pages (localhost:8787)
```

`cmd/server` is an ordinary HTTP server. The table and bucket are created on
boot when the endpoints point at the local containers.

The Go server serves the admin console from `web/admin/dist`, so build it once
before the first boot. If you skip the build, `/admin/` explains what to run
rather than 404ing. `go build` itself never needs Node.

`PINPOST_SEED_DEMO=1` seeds the demo tenant below — its credentials are
published here, so it is opt-in and must never be enabled in a deployment.

| Item | Value |
|---|---|
| Submission demo | http://localhost:8787/demo/ |
| Admin dashboard | http://localhost:8787/admin/ |
| Admin account | `admin@example.com` / `pinpost-demo` |
| SDK Write Key | `wk_demo_5f8a2c91d4e7b3a6f0c1` |

## Configuration

All optional; the defaults suit local development.

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | — | **Required.** PostgreSQL connection URL, e.g. `postgres://pinpost:pinpost@localhost:5432/pinpost?sslmode=disable`. Migrations run on boot. |
| `PINPOST_TABLE` | `pinpost` | DynamoDB table name |
| `PINPOST_BUCKET` | `pinpost-attachments` | S3 bucket for attachments |
| `AWS_ENDPOINT_URL` | *(real AWS)* | DynamoDB endpoint. Setting it means local development: the table is created on boot. |
| `S3_ENDPOINT_URL` | *(real AWS)* | Object storage endpoint. Setting it means local S3 (RustFS): path-style addressing and the bucket created on boot. |
| `ADDR` | `:8787` | Listen address |
| `PINPOST_RATE_LIMIT` | `60` | Ingestion requests per project+IP per minute. The IP is the leftmost `X-Forwarded-For` hop, falling back to the socket peer. |
| `CORS_ALLOWED_ORIGINS` | *(any origin)* | Comma-separated origin allowlist for the public ingestion endpoints. The write key is public by design, so this is hygiene rather than a security boundary. Attachment bytes are not covered — they go straight to S3, whose own CORS configuration governs them. |
| `PINPOST_SEED_DEMO` | `0` | Set to `1` for the demo tenant above. It also mounts `POST /dev/scan/{id}`, which scans one attachment immediately. Local development only. |
| `PINPOST_GOOGLE_CLIENT_ID` | — | Google OAuth **web** client id. Empty disables Google sign-in and hides the button. |
| `PINPOST_GOOGLE_ALLOWED_EMAILS` | — | Comma-separated Google addresses allowed to sign in. This is the access control: an allowed address gets an `admin` member record on first sign-in, anything else is rejected. Empty means nobody. |
| `PINPOST_ADMIN_EMAIL` / `PINPOST_ADMIN_PASSWORD` | — | Provisions the first owner on an empty database. Ignored once seeding has run (the one row in the `bootstrap` table records it; `DELETE FROM bootstrap` allows a re-seed). With no `PINPOST_ADMIN_EMAIL`, the first allowlisted Google address is seeded as owner with no usable password. |
| `PINPOST_ORG_NAME` / `PINPOST_PROJECT_NAME` | `Acme` / `Acme App` | Names for that first tenant |
| `PINPOST_WRITE_KEY` | generated | Write key for that first project; the generated one is logged once at startup |

## Layout

Vertical slices: each feature package owns its HTTP handlers, business logic
and data access; `cmd/server` only wires slices together.

```
api/                 # openapi.yaml (source of truth) + codegen config
cmd/server/          # API entry point: store, seed, wiring, static files
internal/
  api/               # generated router/models (gen.go) + ServerInterface impl delegating to slices
  auth/              # member sessions, roles, write keys
  project/           # project/member queries, demo seed
  feedback/          # creation, idempotency, cursor list, state machine, activities
  attachment/        # presigned S3 uploads, post-upload verification, scanning
  outbox/            # webhook endpoints, stream-derived events, delivery
  platform/          # shared kernel: single-table store, error envelope, rate limiter
web/admin/           # admin console: Vite + React + TypeScript + Tailwind + shadcn/ui
web/demo/            # embedded feedback widget example (inline mini SDK)
sdk/                 # TypeScript SDK monorepo (npm workspaces)
  packages/core/           # @pinpost/core — protocol client, zero runtime deps
  packages/react/          # @pinpost/react — Provider, usePinpost(), <FeedbackWidget/>
  packages/react-native/   # @pinpost/react-native — same API, RN Modal widget
```

## Admin console

`web/admin/` is a Vite + React SPA styled with [shadcn/ui](https://ui.shadcn.com)
(New York style, slate base): a data-table workbench over the feedback inbox,
with each item's detail pane hosted in a Sheet so triage never loses the table
behind it.

Every piece of UI is composed from `src/components/ui/` — the shadcn registry
components, added with `npx shadcn@latest add <name>` and owned in-tree. Feature
components layer only tokens and copy on top: status and priority pills are
`Badge` variants, the trend card is `Chart`, the login form is `Form` + `zod`,
and destructive bulk edits go through `AlertDialog`. When something needs a new
primitive, pull it from the registry rather than hand-rolling the markup.

```bash
cd web/admin
npm ci
npm run dev      # localhost:5473, proxies /v1 to localhost:8787
npm run build    # type-check + production build into dist/
```

Point the dev proxy elsewhere with `PINPOST_API_URL`. What it does today:

- **Overview** — totals by status and type plus the 14-day submission trend,
  from `/v1/analytics/overview`. The trend card is the only thing that pulls in
  recharts (~100 kB gzipped), so it loads as its own chunk after the tiles paint.
- **Inbox table** — cursor pagination, faceted filters (status / type /
  assignee) with live counts, debounced full-text search, column visibility,
  and client-side reordering of loaded rows by priority or rating.
- **Bulk edits** — multi-select rows to change status or assignee. The API has
  no batch endpoint yet, so these fan out to one `PATCH` per row and the toast
  reports per-row successes and failures rather than assuming success. Only
  status transitions legal for *every* selected row are offered.
- **Detail sheet** — message, context, attachments with scan status, the
  status / priority / assignee / tag controls, an internal note composer, and
  the activity timeline. Saves send `updated_at` for optimistic concurrency; a
  409 reloads the record and asks you to confirm instead of clobbering someone
  else's edit.
- **嵌入与集成** — the project's write key, an install snippet per SDK package
  with the key and this origin filled in, and a live "waiting for the first
  submission" check. The write key is a public identifier, so `GET
  /v1/projects` returns it; owners/admins can rotate it with `POST
  /v1/projects/{id}/write-key/rotate`, which revokes the old key in the same
  write (every SDK still holding it gets 401).

The status dropdowns only offer transitions the server state machine accepts
(`open ⇄ in_progress → resolved → reopen; spam → open`), so the UI does not
offer moves the API would reject.

## Client SDKs

`sdk/` holds the three packages from design §6. `@pinpost/core` owns validation, identity/context merging (with the privacy key blacklist), attachment upload, and idempotent submit with backoff retry; the React and React Native packages are thin UI layers over it.

```bash
cd sdk && npm install && npm test   # builds all packages, runs unit + smoke tests
```

With the server running, the core test suite also executes a live end-to-end check (presigned upload → scan → submit → verify via the dashboard API); it skips automatically when the server is down. Point it elsewhere with `PINPOST_API_URL`.

## Webhooks

Feedback events (`feedback.created.v1`, `feedback.status_changed.v1`, `feedback.assigned.v1`, `feedback.note_added.v1`) are derived from the table's change stream by `cmd/webhook` and delivered to registered endpoints. Owners/admins manage endpoints via `POST/GET/PATCH/DELETE /v1/webhook-endpoints`.

- Requests are signed: `X-Pinpost-Signature: v1=hex(HMAC-SHA256(secret, "<X-Pinpost-Timestamp>.<body>"))`; the secret is returned once at creation.
- Retries on 5xx/408/429/network errors are the Lambda event source's job (6 attempts — the initial delivery plus 5 retries — over at most an hour, then the SQS dead-letter queue, which nothing drains yet). Only the record that failed is retried, not its whole batch. Delivery is at-least-once, so consumers must be idempotent (dedupe on `X-Pinpost-Event-Id`). An endpoint down for longer than that hour loses those events; the Postgres-era ladder that retried for 24h is gone.
- 10 consecutive failures auto-pause an endpoint; `PATCH {"status":"active"}` resumes it, and `POST /v1/webhook-deliveries/{id}/replay` re-sends a finished delivery without changing the event ID.
- Nothing is emitted explicitly: `internal/outbox/stream.go` maps item changes to events, and `TestDerive` pins that mapping.

## API contract

The API is spec-driven: [api/openapi.yaml](api/openapi.yaml) is the source of truth.

- Routing and parameter parsing are generated by oapi-codegen (`internal/api/gen.go`); handlers implement the generated `ServerInterface`, so a spec change that isn't implemented fails to compile. Regenerate with `go generate ./internal/api`.
- `go test ./cmd/server/` runs a contract test that exercises every spec operation against DynamoDB Local and RustFS, validates requests and responses with kin-openapi, and fails if any operation is left uncovered (skips if DynamoDB is down).
- CI blocks PRs that introduce breaking spec changes (`oasdiff breaking` vs the base branch) and rejects stale generated code.
- Change flow: edit the spec first, regenerate, then implement — drift is caught at compile time or by the contract test.

## API overview

Ingestion (`X-Pinpost-Write-Key` header):

- `GET  /v1/sdk/config` — fetch the published SDK config
- `POST /v1/attachment-uploads` — request a presigned S3 PUT URL (≤10 MiB, jpeg/png/webp/txt); the client uploads straight to object storage
- `POST /v1/feedback` — submit feedback; `Idempotency-Key` dedupes and returns the existing record

Dashboard (session cookie):

- `POST /v1/auth/login` `/logout`, `GET /v1/me`
- `POST /v1/auth/google` (Google ID token → session; allowlisted addresses only), `GET /v1/auth/config` (public client id; empty means Google sign-in is off)
- `GET  /v1/feedback` (status/type/assignee/q filters + cursor pagination)
- `GET/PATCH /v1/feedback/{id}` (state machine open→in_progress→resolved / spam; optimistic concurrency via `updated_at`, 409 on conflict)
- `POST /v1/feedback/{id}/notes`, `GET /v1/feedback/{id}/activities`
- `GET  /v1/attachments/{id}/download` (only scan=clean; 302 to a 60-second presigned URL)
- `GET  /v1/projects` `/members` `/analytics/overview`

Tenant isolation is structural rather than a `WHERE` clause everyone must remember: every organization-scoped item lives under the partition key `ORG#<orgId>`, so a cross-tenant read cannot be expressed. Reads outside the caller's organization return 404.

## Data model

One table, one sparse index, no scans anywhere.

| Entity | PK | SK |
|---|---|---|
| Organization | `ORG#<orgId>` | `#META` |
| Project | `ORG#<orgId>` | `PROJ#<projId>` |
| Write key | `WK#<sha256>` | `#META` |
| Member | `ORG#<orgId>` | `MEM#<memberId>` |
| Session | `SESS#<tokenHash>` | `#META` |
| Feedback | `ORG#<orgId>` | `FB#<uuidv7>` |
| Activity | `FB#<feedbackId>` | `ACT#<uuidv7>` |
| Attachment | `ATT#<attId>` | `#META` |
| Idempotency key | `IDEM#<projId>#<key>` | `#META` |
| Rate-limit counter | `RL#<project\|ip>#<minute>` | `#META` |
| Webhook endpoint | `ORG#<orgId>` | `WH#<id>` |
| Delivery | `DLV#<id>` | `#META` |

`GSI1` is sparse and covers three lookups: members by email (`EMAIL#<email>`),
attachments by feedback (`FB#<feedbackId>`) and deliveries by endpoint
(`WH#<endpointId>`). Sort keys are UUIDv7, whose lexical order is time order,
so "newest first with a cursor" is a reverse Query and the cursor is a sort
key. `expires_at` is the TTL attribute: sessions, idempotency keys, rate-limit
counters, unclaimed attachments and delivery records all expire on their own,
so no cleanup job — and no cleanup scan — exists.

Two reads are deliberate read-amplification rather than an index: filtering by
status/type/assignee uses a `FilterExpression`, and `q` search plus the
analytics overview aggregate a single organization's partition in memory (the
overview is cached 60 seconds per instance). Cost is bounded by one tenant's
data, not by the table. Above roughly 50k feedback items in one organization,
add the sparse `GSI2PK = ORG#<org>#STATUS#<s>` index.

## Boundaries

- Attachment scanning is a signature scan (EICAR, executables, image polyglots), not an AV engine; the server's background worker runs it within a few seconds of a submission claiming the attachment, and the interface is where a real engine would slot in. Flagged and mislabeled files are stored but blocked from download. An attachment whose object never arrives is marked `rejected` after an hour.
- **Verification is now after the fact.** A file whose real type does not match the declared one used to be refused with a 400 during upload; it now lands in the bucket and is marked `rejected`. Attachments are briefly `pending` — a status the dashboard shows and the download endpoint refuses.
- Attachments are linked to feedback whether or not the scan has finished. The design gates the link on `upload_status = "uploaded"`, but the SDK can submit within milliseconds of the upload, and losing a user's feedback to that race is worse than showing a `pending` attachment; download is still gated on `clean`.
- Objects whose feedback submission never arrived stay in the bucket. Their row is swept after 24 hours, so they are invisible and unreferenced; deleting the object in the same sweep is the upgrade when the orphan bytes cost something.
- Email / one-tap screenshot / cross-restart offline queue are Phase 2.

## Deploying

Pinpost is moving from AWS Lambda + DynamoDB to a single container backed by
PostgreSQL and any S3-compatible object store, started with `docker compose up`.
Until that lands there is no supported deployment path. The serverless
Terraform stack is preserved at the `aws-lambda-dynamodb` git tag.

## License

Pinpost is © DE TECHNOLOGY LIMITED.

- The server, admin console and everything outside `sdk/` are licensed under the
  [GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0-only).
- The client SDKs in [`sdk/`](sdk/) are licensed under the [MIT License](sdk/LICENSE),
  so you can embed them in closed-source apps.

Contributions require signing the [Contributor License Agreement](CLA.md); see
[CONTRIBUTING.md](CONTRIBUTING.md).
