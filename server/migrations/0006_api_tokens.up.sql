-- 0006_api_tokens — personal access tokens for programmatic clients (e.g. the
-- Cadence MCP server). Like sessions, these establish identity before a tenant
-- context exists, so they are NOT RLS-scoped and store only the token hash.
-- `id` is a public handle for listing/revoking without exposing the secret.

CREATE TABLE api_tokens (
  token_hash   text PRIMARY KEY,
  id           uuid NOT NULL,
  tenant_id    uuid NOT NULL,
  user_id      uuid NOT NULL,
  name         text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz
);
CREATE INDEX api_tokens_owner_idx ON api_tokens (tenant_id, user_id);
CREATE UNIQUE INDEX api_tokens_id_idx ON api_tokens (id);
