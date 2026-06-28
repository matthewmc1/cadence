-- 0001_init — Cadence core schema.
--
-- Design notes
--   * Multi-tenancy: every row carries tenant_id and is fenced by Row-Level
--     Security. FORCE ROW LEVEL SECURITY makes the policy apply even to the
--     table owner, so isolation holds regardless of the connecting role.
--   * Partition-ready: composite primary keys lead with tenant_id, and every
--     foreign key is tenant-local (tenant_id, id). This lets tasks be HASH
--     partitioned by tenant_id later with zero application change.
--   * Realtime: writes append to a transactional `outbox`; an AFTER INSERT
--     trigger pg_notify()s 'cadence_events' on commit, which the app fans out
--     over WebSockets. The outbox row is the durable record for replay /
--     external relay (at-least-once delivery).

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---- enums -----------------------------------------------------------------
CREATE TYPE task_kind   AS ENUM ('deep', 'light', 'admin', 'meet', 'personal');
CREATE TYPE task_status AS ENUM ('backlog', 'scheduled', 'focus', 'done');

-- ---- helper: keep updated_at honest ----------------------------------------
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ---- tenants ---------------------------------------------------------------
CREATE TABLE tenants (
  id         uuid PRIMARY KEY,
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenants_isolation ON tenants
  USING (id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- ---- users -----------------------------------------------------------------
CREATE TABLE users (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  id        uuid NOT NULL,
  name      text NOT NULL,
  email     text NOT NULL,
  initial   text NOT NULL,
  color     text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, email)
);
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE  ROW LEVEL SECURITY;
CREATE POLICY users_isolation ON users
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- ---- projects --------------------------------------------------------------
CREATE TABLE projects (
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  id         uuid NOT NULL,
  name       text NOT NULL,
  subtitle   text NOT NULL DEFAULT '',
  due        text,
  color      text NOT NULL DEFAULT '#C2743D',
  version    integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
ALTER TABLE projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects FORCE  ROW LEVEL SECURITY;
CREATE POLICY projects_isolation ON projects
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE TRIGGER projects_set_updated BEFORE UPDATE ON projects
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---- project_members (avatar stack) ----------------------------------------
CREATE TABLE project_members (
  tenant_id  uuid NOT NULL,
  project_id uuid NOT NULL,
  user_id    uuid NOT NULL,
  initial    text NOT NULL,
  color      text NOT NULL,
  PRIMARY KEY (tenant_id, project_id, user_id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, user_id)    REFERENCES users(tenant_id, id)    ON DELETE CASCADE
);
ALTER TABLE project_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_members FORCE  ROW LEVEL SECURITY;
CREATE POLICY project_members_isolation ON project_members
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- ---- tasks -----------------------------------------------------------------
CREATE TABLE tasks (
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  id             uuid NOT NULL,
  project_id     uuid,
  title          text NOT NULL,
  kind           task_kind   NOT NULL DEFAULT 'light',
  status         task_status NOT NULL DEFAULT 'backlog',
  effort_minutes integer NOT NULL DEFAULT 30 CHECK (effort_minutes >= 0),
  urgent         boolean NOT NULL DEFAULT false,
  note           text NOT NULL DEFAULT '',
  place          text,
  scheduled_day  integer CHECK (scheduled_day BETWEEN 0 AND 6),
  scheduled_hour double precision CHECK (scheduled_hour >= 0 AND scheduled_hour < 24),
  on_today       boolean NOT NULL DEFAULT false,
  today_hour     double precision CHECK (today_hour >= 0 AND today_hour < 24),
  position       double precision NOT NULL DEFAULT 0,
  done_at        timestamptz,
  version        integer NOT NULL DEFAULT 1,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id) ON DELETE SET NULL
);
ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks FORCE  ROW LEVEL SECURITY;
CREATE POLICY tasks_isolation ON tasks
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE TRIGGER tasks_set_updated BEFORE UPDATE ON tasks
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- access paths the three views actually use
CREATE INDEX tasks_tenant_status_idx  ON tasks (tenant_id, status);
CREATE INDEX tasks_tenant_project_idx ON tasks (tenant_id, project_id) WHERE project_id IS NOT NULL;
CREATE INDEX tasks_tenant_today_idx   ON tasks (tenant_id, today_hour) WHERE on_today;
CREATE INDEX tasks_tenant_sched_idx   ON tasks (tenant_id, scheduled_day) WHERE scheduled_day IS NOT NULL;

-- ---- outbox (transactional realtime + replay log) --------------------------
-- Intentionally NOT tenant-RLS'd: a relay/worker reads across tenants. Access
-- is controlled at the role level. Rows still carry tenant_id for routing.
CREATE TABLE outbox (
  id           uuid PRIMARY KEY,
  tenant_id    uuid NOT NULL,
  type         text NOT NULL,
  entity_id    uuid NOT NULL,
  actor_id     uuid,
  payload      jsonb NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);
CREATE INDEX outbox_unpublished_idx ON outbox (created_at) WHERE published_at IS NULL;

CREATE OR REPLACE FUNCTION outbox_notify() RETURNS trigger AS $$
BEGIN
  -- Notify only the outbox row id (tiny, always < 8000-byte NOTIFY limit). The
  -- listener fetches the full payload by id, so an arbitrarily large note/title
  -- can never make NOTIFY error and abort the write transaction.
  PERFORM pg_notify('cadence_events', NEW.id::text);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER outbox_notify_trg AFTER INSERT ON outbox
  FOR EACH ROW EXECUTE FUNCTION outbox_notify();
