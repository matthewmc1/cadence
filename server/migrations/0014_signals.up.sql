-- 0014_signals — captured things, and where work items come from (R0.4).
--
-- A Signal is something that arrived: meeting notes, a pasted email, a link, a
-- chat message, a document. It is NEVER a task. Work items derive from signals
-- (through work_item_signals below) the way they derive from requirements; the
-- signal itself is the immutable record of what was captured, kept so the work
-- item can always answer "where did this come from".
--
-- Five tables, all on the 0009 tenancy boilerplate (tenant-leading composite
-- PK, CASCADE to tenants, RLS + FORCE RLS with the fail-closed NULLIF policy,
-- text + CHECK enumerations — never ALTER TYPE … ADD VALUE, the runner wraps
-- each file in a transaction):
--
--   sources             where signals come from. Every tenant gets one implicit
--                       'manual' source (the paste box), created lazily by the
--                       store; connectors add their own. NO credentials here —
--                       config is non-secret; a separate source_credentials
--                       table comes in a later slice so a source row can be
--                       listed freely.
--   signals             the captured record. UNIQUE (tenant_id, source_id,
--                       external_id) makes connector re-syncs idempotent; for
--                       manual captures external_id is the row's own id.
--   signal_bodies       the full text, one row per signal, NEVER selected with
--                       the signal row (a list of 100 signals must not drag
--                       100 meeting transcripts through the API); fetched by
--                       GET /signals/{id}/body only.
--   signal_participants who was on it (from/to/cc/attendee/speaker). This is
--                       PII, so it has its own (tenant_id, email) index for
--                       data-subject requests and is never put in an event.
--   work_item_signals   the origin join: which signals a work item derives from.
--
-- IMMUTABILITY. A signal is a record of something that happened, so its
-- content columns never change after insert. The BEFORE UPDATE trigger below
-- is an ALLOW-list: it strips the disposition columns (stage, snoozed_until,
-- disposition_at, extracted, project_hint, retention_until) and the
-- bookkeeping ones (version, updated_at) from the OLD and NEW rows and raises
-- if anything else differs — so a column added later is frozen by default
-- until it is deliberately listed. DELETE stays allowed: purge and retention
-- need it.
--
-- THE ORIGIN TRADE-OFF (work_item_signals). The join row must outlive the
-- signal: a signal can be deleted (retention, a data-subject request, a
-- mistaken paste) while the work item it produced lives on, and that work item
-- should still be able to say what it came from. ON DELETE SET NULL is
-- impossible on a PK column, and ON DELETE CASCADE would erase the row — and
-- with it the snapshot — the moment the signal went, defeating the purpose.
-- So the signal side of the join deliberately has NO foreign key (like
-- tasks.created_by, it is a reference that may outlive its target), and the
-- row carries origin_snapshot: the signal's title/kind/occurred_at/body_ref
-- copied at attach time. A live signal is the source of truth; a purged one
-- leaves the snapshot behind. The app checks the signal exists in the tenant
-- at attach time, and tenant_id is in the PK so RLS still fences the row. The
-- task side IS a CASCADE FK: an origin without a work item means nothing.
--
-- Access paths: the inbox feed is (tenant_id, occurred_at DESC) — the keyset
-- the paged reader walks (store/paging.go) — with a (tenant_id, stage,
-- occurred_at DESC) variant for the stage filter, and a partial index on
-- snoozed_until for "what wakes up next".

-- ---- sources ---------------------------------------------------------------
CREATE TABLE sources (
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  id             uuid NOT NULL,
  kind           text NOT NULL CHECK (kind IN ('manual', 'calendar', 'email', 'notes', 'docs', 'chat')),
  name           text NOT NULL,
  owner_id       uuid,
  -- connector settings that are safe to list (a calendar id, a folder name).
  -- Secrets NEVER go here; see the header.
  config         jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
  -- when the owner consented to this source being read; NULL = not yet
  consent_at     timestamptz,
  -- signals from this source expire this many days after they occurred
  -- (copied onto signals.retention_until at capture); NULL = keep
  retention_days integer CHECK (retention_days IS NULL OR retention_days > 0),
  disabled_at    timestamptz,
  version        integer NOT NULL DEFAULT 1,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, owner_id) REFERENCES users(tenant_id, id) ON DELETE SET NULL (owner_id)
);
ALTER TABLE sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE sources FORCE  ROW LEVEL SECURITY;
CREATE POLICY sources_isolation ON sources
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE TRIGGER sources_set_updated BEFORE UPDATE ON sources
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- exactly one implicit manual source per tenant; the store's lazy create is
-- INSERT … ON CONFLICT DO NOTHING against this
CREATE UNIQUE INDEX sources_manual_uniq ON sources (tenant_id) WHERE kind = 'manual';

-- ---- signals ---------------------------------------------------------------
CREATE TABLE signals (
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  id              uuid NOT NULL,
  source_id       uuid NOT NULL,
  -- the source's own identifier for this thing (message id, event id); the
  -- signal's id for manual captures
  external_id     text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('meeting', 'email', 'note', 'doc', 'chat', 'link', 'text')),
  title           text NOT NULL,
  -- the first 2000 characters, enough to triage from the list without the body
  excerpt         text NOT NULL DEFAULT '' CHECK (char_length(excerpt) <= 2000),
  -- source-native locator for the full thing (a URL, a message id); may be ''
  body_ref        text NOT NULL DEFAULT '',
  occurred_at     timestamptz NOT NULL,
  captured_by     uuid,                        -- attribution only, no FK (see tasks.created_by)
  -- disposition: the only thing about a signal that changes
  stage           text NOT NULL DEFAULT 'inbox'
                  CHECK (stage IN ('inbox', 'snoozed', 'promoted', 'attached', 'dismissed')),
  snoozed_until   timestamptz,
  disposition_at  timestamptz,
  -- facts pulled out of the body (dates, people, links, deadlines); a later
  -- slice fills it, manual capture seeds {"links": […]}
  extracted       jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(extracted) = 'object'),
  project_hint    uuid,
  retention_until timestamptz,
  version         integer NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source_id, external_id),
  FOREIGN KEY (tenant_id, source_id)    REFERENCES sources(tenant_id, id)  ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, project_hint) REFERENCES projects(tenant_id, id) ON DELETE SET NULL (project_hint)
);
ALTER TABLE signals ENABLE ROW LEVEL SECURITY;
ALTER TABLE signals FORCE  ROW LEVEL SECURITY;
CREATE POLICY signals_isolation ON signals
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
CREATE TRIGGER signals_set_updated BEFORE UPDATE ON signals
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE OR REPLACE FUNCTION signals_immutable() RETURNS trigger AS $$
DECLARE
  mutable text[] := ARRAY['stage', 'snoozed_until', 'disposition_at', 'extracted',
                          'project_hint', 'retention_until', 'version', 'updated_at'];
BEGIN
  IF (to_jsonb(NEW) - mutable) IS DISTINCT FROM (to_jsonb(OLD) - mutable) THEN
    RAISE EXCEPTION 'signals are immutable: only disposition columns may change'
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER signals_immutable BEFORE UPDATE ON signals
  FOR EACH ROW EXECUTE FUNCTION signals_immutable();

CREATE INDEX signals_tenant_occurred_idx       ON signals (tenant_id, occurred_at DESC, id DESC);
CREATE INDEX signals_tenant_stage_occurred_idx ON signals (tenant_id, stage, occurred_at DESC, id DESC);
CREATE INDEX signals_tenant_snoozed_idx        ON signals (tenant_id, snoozed_until) WHERE snoozed_until IS NOT NULL;
CREATE INDEX signals_tenant_source_idx         ON signals (tenant_id, source_id);
CREATE INDEX signals_tenant_project_hint_idx   ON signals (tenant_id, project_hint) WHERE project_hint IS NOT NULL;

-- ---- signal_bodies ---------------------------------------------------------
-- No version / updated_at: a body is written once with its signal and never
-- edited (it goes when the signal goes).
CREATE TABLE signal_bodies (
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  signal_id  uuid NOT NULL,
  body       text NOT NULL,
  fetched_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, signal_id),
  FOREIGN KEY (tenant_id, signal_id) REFERENCES signals(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE signal_bodies ENABLE ROW LEVEL SECURITY;
ALTER TABLE signal_bodies FORCE  ROW LEVEL SECURITY;
CREATE POLICY signal_bodies_isolation ON signal_bodies
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- ---- signal_participants ---------------------------------------------------
-- Written with the signal, immutable like it. person_id is reserved for the
-- people table (later slice); no FK until it exists.
CREATE TABLE signal_participants (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  signal_id uuid NOT NULL,
  idx       integer NOT NULL CHECK (idx >= 0),
  name      text NOT NULL,
  email     text,
  role      text NOT NULL CHECK (role IN ('from', 'to', 'cc', 'attendee', 'speaker')),
  person_id uuid,
  PRIMARY KEY (tenant_id, signal_id, idx),
  FOREIGN KEY (tenant_id, signal_id) REFERENCES signals(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE signal_participants ENABLE ROW LEVEL SECURITY;
ALTER TABLE signal_participants FORCE  ROW LEVEL SECURITY;
CREATE POLICY signal_participants_isolation ON signal_participants
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- "every signal this address appears on" — the data-subject request path
CREATE INDEX signal_participants_email_idx ON signal_participants (tenant_id, email) WHERE email IS NOT NULL;

-- ---- work_item_signals (origins) -------------------------------------------
-- See THE ORIGIN TRADE-OFF in the header: task side cascades, signal side has
-- no FK and is backed by origin_snapshot.
CREATE TABLE work_item_signals (
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  task_id         uuid NOT NULL,
  signal_id       uuid NOT NULL,
  -- {title, kind, occurredAt, bodyRef} copied from the signal at attach time
  origin_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(origin_snapshot) = 'object'),
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, task_id, signal_id),
  FOREIGN KEY (tenant_id, task_id) REFERENCES tasks(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE work_item_signals ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_item_signals FORCE  ROW LEVEL SECURITY;
CREATE POLICY work_item_signals_isolation ON work_item_signals
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- "which work items came from this signal" (the PK already serves the task side)
CREATE INDEX work_item_signals_signal_idx ON work_item_signals (tenant_id, signal_id);
