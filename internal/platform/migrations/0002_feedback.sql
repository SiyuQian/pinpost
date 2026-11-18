-- Feedback, its timeline and its attachments.

CREATE TABLE feedback (
  id               uuid PRIMARY KEY, -- UUIDv7: id order is creation order
  organization_id  uuid NOT NULL,
  project_id       uuid NOT NULL,
  -- Replays of a submission are deduplicated for good, not for a day: the
  -- SDK generates a fresh key per report, so a match is always a replay.
  idempotency_key  text NOT NULL,
  external_user_id text,
  contact_email    text,
  type             text NOT NULL CHECK (type IN ('bug', 'suggestion', 'other')),
  message          text NOT NULL,
  -- Provenance: "widget" for the public ingestion endpoint, "internal" for
  -- a report a member filed from the dashboard.
  source           text NOT NULL DEFAULT 'widget' CHECK (source IN ('widget', 'internal')),
  rating           smallint CHECK (rating BETWEEN 1 AND 5),
  status           text NOT NULL DEFAULT 'open'
                   CHECK (status IN ('open', 'in_progress', 'resolved', 'spam')),
  priority         text NOT NULL DEFAULT 'none'
                   CHECK (priority IN ('none', 'low', 'medium', 'high', 'urgent')),
  assignee_id      uuid,
  tags             text[] NOT NULL DEFAULT '{}',
  context          jsonb NOT NULL DEFAULT '{}',
  created_at       timestamptz NOT NULL DEFAULT now(),
  -- Also the optimistic-concurrency token. Always set by the database, so
  -- the value a client reads back is exactly the value it compares against.
  updated_at       timestamptz NOT NULL DEFAULT now(),
  -- The project and the assignee must belong to the feedback's own
  -- organization; the database refuses anything else.
  FOREIGN KEY (organization_id, project_id) REFERENCES projects (organization_id, id),
  FOREIGN KEY (organization_id, assignee_id) REFERENCES members (organization_id, id),
  UNIQUE (project_id, idempotency_key),
  UNIQUE (project_id, id)
);
-- The inbox: newest first within one organization, keyset-paginated on id.
CREATE INDEX feedback_org_idx ON feedback (organization_id, id);

CREATE TABLE activities (
  id            uuid PRIMARY KEY, -- UUIDv7: the timeline is ordered by id
  feedback_id   uuid NOT NULL REFERENCES feedback (id) ON DELETE CASCADE,
  actor_type    text NOT NULL,
  actor_id      uuid,
  activity_type text NOT NULL,
  data          jsonb NOT NULL DEFAULT '{}',
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX activities_feedback_idx ON activities (feedback_id, id);

CREATE TABLE attachments (
  id              uuid PRIMARY KEY,
  organization_id uuid NOT NULL,
  project_id      uuid NOT NULL,
  -- Null until a submission claims it. Unclaimed rows are swept after a day.
  feedback_id     uuid,
  object_key      text NOT NULL,
  original_name   text NOT NULL,
  mime_type       text NOT NULL,
  size            bigint NOT NULL,
  upload_status   text NOT NULL DEFAULT 'pending',
  scan_status     text NOT NULL DEFAULT 'pending'
                  CHECK (scan_status IN ('pending', 'clean', 'flagged', 'rejected')),
  checksum        text NOT NULL DEFAULT '',
  -- When the scan worker should next look at a claimed, still-pending row.
  next_scan_at    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (organization_id, project_id) REFERENCES projects (organization_id, id),
  -- An attachment can only ever belong to feedback of its own project.
  FOREIGN KEY (project_id, feedback_id) REFERENCES feedback (project_id, id) ON DELETE CASCADE
);
CREATE INDEX attachments_feedback_idx ON attachments (feedback_id);
CREATE INDEX attachments_scan_idx ON attachments (next_scan_at)
  WHERE scan_status = 'pending' AND feedback_id IS NOT NULL;
CREATE INDEX attachments_unclaimed_idx ON attachments (created_at) WHERE feedback_id IS NULL;
