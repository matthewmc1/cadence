-- 0012_audit_log — the tenant's own record of who did what.
--
-- One row per security-relevant action: a magic link issued or verified, a
-- personal access token minted or revoked, an AI call through the gateway, an
-- MCP read, an export, a purge, a connector sync. It is what lets a user (or
-- an operator) answer "what touched my workspace, and when" without trusting
-- server logs that rotate.
--
-- APPEND-ONLY. The BEFORE UPDATE OR DELETE trigger below raises on every row
-- change, with exactly one exception: a DELETE while the transaction-local
-- setting app.purging = '1' is set. Only PurgeTenant sets it (see
-- postgres/purge.go) — a purge is the one legitimate reason a tenant's history
-- disappears, and it takes the whole tenant with it. The row-level trigger
-- also fires for the ON DELETE CASCADE from tenants, so the tenant row itself
-- cannot be deleted without the flag either; PurgeTenant clears audit_log
-- explicitly first and holds the flag for the whole transaction. Because the
-- log is immutable there is no updated_at, no set_updated_at trigger and no
-- version column — the rest of the 0009 tenancy boilerplate (tenant-leading
-- PK, CASCADE to tenants, RLS + FORCE RLS with the fail-closed NULLIF policy)
-- applies unchanged.
--
-- NO PII RULE. `detail` is bounded (the app enforces 4 KiB) and carries only
-- what is needed to interpret the row: counts, a model name, a token's display
-- name, an entity id. Never an email, an IP, a prompt, a signal body, a token
-- secret. Identity is by actor_id (a users row) and, for auth events, ip_hash —
-- a salted SHA-256 of the client address that lets an operator correlate
-- "same source" without storing the address. `kind` is deliberately not
-- CHECKed: a log must never refuse a write because a new producer used a kind
-- the schema had not heard of (connector.* is open-ended); the domain package
-- holds the known constants.
--
-- The index leads with tenant_id and orders by (at DESC, id DESC) — exactly
-- the keyset the paged reader walks (store/paging.go), so "the next page of my
-- history" is one index range scan.

CREATE TABLE audit_log (
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  id          uuid NOT NULL,
  at          timestamptz NOT NULL DEFAULT now(),
  actor_id    uuid,                        -- NULL for system-initiated rows
  kind        text NOT NULL CHECK (kind <> ''),
  entity_type text,                        -- api_token | task | … when the row is about one thing
  entity_id   uuid,
  detail      jsonb NOT NULL DEFAULT '{}'::jsonb,
  ip_hash     text,
  PRIMARY KEY (tenant_id, id)
);
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE  ROW LEVEL SECURITY;
CREATE POLICY audit_log_isolation ON audit_log
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

CREATE OR REPLACE FUNCTION audit_log_append_only() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' AND current_setting('app.purging', true) = '1' THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'audit_log is append-only: % denied', TG_OP
    USING ERRCODE = 'insufficient_privilege';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();

CREATE INDEX audit_log_tenant_at_idx ON audit_log (tenant_id, at DESC, id DESC);
