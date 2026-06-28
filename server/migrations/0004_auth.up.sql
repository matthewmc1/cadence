-- 0004_auth — native magic-link auth + email-only users.
--
-- We store the minimum: a user is just an email inside a tenant. Display name,
-- initial and avatar colour are DERIVED from the email at read time, never
-- stored. New sign-ups get their own (personal) tenant.
--
-- accounts / login_tokens / sessions are infrastructure tables used to
-- establish identity *before* a tenant context exists, so they are NOT
-- RLS-scoped (a session lookup must work without already knowing the tenant).
-- They hold only the email + opaque token hashes.

ALTER TABLE users
  DROP COLUMN name,
  DROP COLUMN initial,
  DROP COLUMN color,
  ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();

-- email → (tenant, user) directory, queryable pre-auth
CREATE TABLE accounts (
  email      text PRIMARY KEY,
  tenant_id  uuid NOT NULL,
  user_id    uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- one-time magic-link tokens (only the hash is stored)
CREATE TABLE login_tokens (
  token_hash  text PRIMARY KEY,
  email       text NOT NULL,
  expires_at  timestamptz NOT NULL,
  consumed_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_tokens_email_idx ON login_tokens (email);

-- active sessions (cookie carries the token; only the hash is stored)
CREATE TABLE sessions (
  token_hash text PRIMARY KEY,
  user_id    uuid NOT NULL,
  tenant_id  uuid NOT NULL,
  email      text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);
