-- 0009_clients — the strategic spine.
--
-- A Client (or internal initiative) sits above Project, so work rolls up to
-- who it is ultimately for. Tasks inherit their client THROUGH their project —
-- there is deliberately no client_id on tasks, keeping one source of truth and
-- letting a task move clients simply by moving projects.
--
-- Follows the 0001 tenancy boilerplate (composite PK led by tenant_id, RLS +
-- FORCE RLS) and the 0002 EXPAND pattern for the new projects.client_id column.

CREATE TABLE clients (
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  id                  uuid NOT NULL,
  name                text NOT NULL,
  tier                text NOT NULL DEFAULT 'b'      CHECK (tier IN ('a', 'b', 'c')),
  kind                text NOT NULL DEFAULT 'client' CHECK (kind IN ('client', 'internal')),
  color               text NOT NULL DEFAULT '#6E7E91',
  -- cadence target: flag the client "underserved" when untouched for this many
  -- days. NULL means no expectation is set (never flagged).
  expected_touch_days integer CHECK (expected_touch_days IS NULL OR expected_touch_days > 0),
  archived_at         timestamptz,
  version             integer NOT NULL DEFAULT 1,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
ALTER TABLE clients ENABLE ROW LEVEL SECURITY;
ALTER TABLE clients FORCE  ROW LEVEL SECURITY;
CREATE POLICY clients_isolation ON clients
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE TRIGGER clients_set_updated BEFORE UPDATE ON clients
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX clients_active_idx ON clients (tenant_id) WHERE archived_at IS NULL;

-- ---- projects.client_id (EXPAND: nullable, tenant-local FK) -----------------
-- A NULLable column with no default is a metadata-only change (no rewrite), so
-- old app instances that don't know about client_id keep serving safely. The
-- composite FK is MATCH SIMPLE, so a NULL client_id is never constraint-checked.
--
-- ON DELETE SET NULL (client_id): the column-subset form (PG15+) nulls ONLY
-- client_id when a client is deleted. Without the (client_id) subset, Postgres
-- would try to null EVERY referencing column — including tenant_id, which is
-- NOT NULL — and deleting a client that owns any project would error out.
ALTER TABLE projects ADD COLUMN client_id uuid;
ALTER TABLE projects ADD CONSTRAINT projects_client_fk
  FOREIGN KEY (tenant_id, client_id) REFERENCES clients(tenant_id, id) ON DELETE SET NULL (client_id);
CREATE INDEX projects_client_idx ON projects (tenant_id, client_id) WHERE client_id IS NOT NULL;
