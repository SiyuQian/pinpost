-- Webhook endpoints and their deliveries. A pending delivery row is the
-- outbox: it is inserted in the same transaction as the change it reports,
-- and the dispatcher in cmd/server sends it.

CREATE TABLE webhook_endpoints (
  id                   uuid PRIMARY KEY,
  organization_id      uuid NOT NULL REFERENCES organizations (id),
  url                  text NOT NULL,
  secret               text NOT NULL,
  event_types          text[] NOT NULL DEFAULT '{}', -- empty means every event
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
  consecutive_failures int NOT NULL DEFAULT 0,
  created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_endpoints_org_idx ON webhook_endpoints (organization_id);

CREATE TABLE webhook_deliveries (
  id              uuid PRIMARY KEY,
  endpoint_id     uuid NOT NULL REFERENCES webhook_endpoints (id) ON DELETE CASCADE,
  organization_id uuid NOT NULL,
  -- Shared by every endpoint's copy of one event, and unchanged across
  -- retries and replays, so consumers can deduplicate on it.
  event_id        uuid NOT NULL,
  event_type      text NOT NULL,
  payload         jsonb NOT NULL,
  status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed')),
  attempt         int NOT NULL DEFAULT 0,
  -- When the dispatcher should next send a pending row; also its lease.
  next_attempt_at timestamptz DEFAULT now(),
  response_status int,
  response_body   text NOT NULL DEFAULT '',
  latency_ms      int,
  created_at      timestamptz NOT NULL DEFAULT now(), -- when the event happened
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (endpoint_id, event_id)
);
CREATE INDEX webhook_deliveries_endpoint_idx ON webhook_deliveries (endpoint_id, created_at DESC);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at) WHERE status = 'pending';
