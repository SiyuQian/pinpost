-- Organizations, projects, members and sessions. Every organization-scoped
-- table carries organization_id; reads filter on it explicitly, and the
-- composite UNIQUE (organization_id, id) constraints let later tables point
-- at "a project/member of this same organization" with a foreign key.

CREATE TABLE organizations (
  id         uuid PRIMARY KEY,
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- One row at most: its presence means "this deployment has been seeded", and
-- it names the organization a first-time Google sign-in joins.
CREATE TABLE bootstrap (
  singleton       boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  organization_id uuid NOT NULL REFERENCES organizations (id)
);

CREATE TABLE projects (
  id              uuid PRIMARY KEY,
  organization_id uuid NOT NULL REFERENCES organizations (id),
  name            text NOT NULL,
  environment     text NOT NULL,
  -- The write key is a public identifier that ships in client bundles
  -- (design §9.1), so it is stored as is and looked up directly.
  write_key       text NOT NULL UNIQUE,
  settings        jsonb NOT NULL DEFAULT '{}',
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, id)
);

CREATE TABLE members (
  id              uuid PRIMARY KEY,
  organization_id uuid NOT NULL REFERENCES organizations (id),
  email           text NOT NULL, -- normalised (trimmed, lower-case) by the application
  name            text NOT NULL,
  picture         text NOT NULL DEFAULT '',
  role            text NOT NULL CHECK (role IN ('owner', 'admin', 'agent', 'viewer')),
  status          text NOT NULL DEFAULT 'active',
  -- Empty means unusable: bcrypt rejects every candidate against it, so the
  -- member can only sign in through Google.
  password_hash   text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, email),
  UNIQUE (organization_id, id)
);
-- Login looks members up by address alone: one person may belong to several
-- organizations.
CREATE INDEX members_email_idx ON members (email);

CREATE TABLE sessions (
  token_hash      text PRIMARY KEY,
  member_id       uuid NOT NULL REFERENCES members (id) ON DELETE CASCADE,
  organization_id uuid NOT NULL,
  expires_at      timestamptz NOT NULL
);
