-- 0011_requirements — what a project must deliver.
--
-- A Requirement is one thing a project has to satisfy: a short title, an
-- optional description and acceptance note, a 1–5 weight (how much it
-- matters), and a status that moves open → met | dropped. Work items derive
-- from requirements the way they derive from signals — a requirement is never
-- a task, it is what tasks are FOR.
--
-- Requirements belong to exactly one project (NOT NULL project_id) and go
-- with it: ON DELETE CASCADE, not SET NULL — a requirement without a project
-- is meaningless. The FK is tenant-local (tenant_id, project_id) so a
-- requirement can never point at another tenant's project even if the app
-- layer forgets to check.
--
-- Follows the 0009 tenancy boilerplate: composite PK led by tenant_id, RLS +
-- FORCE RLS with the fail-closed NULLIF policy, set_updated_at trigger and a
-- version column for optimistic concurrency. Enumerations are text + CHECK
-- (never ALTER TYPE … ADD VALUE — the runner wraps each file in a transaction).

CREATE TABLE requirements (
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  id          uuid NOT NULL,
  project_id  uuid NOT NULL,
  title       text NOT NULL,
  description text NOT NULL DEFAULT '',
  -- 1 = nice to have … 5 = the project fails without it
  weight      integer NOT NULL DEFAULT 3 CHECK (weight BETWEEN 1 AND 5),
  -- how we will know it is met (free text; empty until someone writes it)
  acceptance  text NOT NULL DEFAULT '',
  status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'met', 'dropped')),
  position    double precision NOT NULL DEFAULT 0,
  archived_at timestamptz,
  version     integer NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE requirements ENABLE ROW LEVEL SECURITY;
ALTER TABLE requirements FORCE  ROW LEVEL SECURITY;
CREATE POLICY requirements_isolation ON requirements
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE TRIGGER requirements_set_updated BEFORE UPDATE ON requirements
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- a project's requirements (the only access path besides the PK)
CREATE INDEX requirements_project_idx ON requirements (tenant_id, project_id);
