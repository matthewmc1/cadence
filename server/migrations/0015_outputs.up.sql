-- 0015_outputs — what a work item produced (R0.5).
--
-- An Output is the artefact a work item leaves behind: a link, a document, a
-- pull request, an email sent, a decision taken, a file, a note. Where signals
-- (0014) answer "where did this come from", outputs answer "what came of it" —
-- together they give a work item its provenance in both directions.
--
-- An output belongs to exactly one work item and goes with it (tenant-local
-- composite FK, ON DELETE CASCADE): an output without its work item is
-- meaningless. It is created and deleted, never edited — so like audit_log
-- there is no version column, no updated_at and no set_updated_at trigger;
-- the rest of the 0009 tenancy boilerplate (tenant-leading PK, CASCADE to
-- tenants, RLS + FORCE RLS with the fail-closed NULLIF policy, text + CHECK
-- enumeration) applies unchanged. created_by is attribution with no FK (see
-- tasks.created_by).

CREATE TABLE outputs (
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  id         uuid NOT NULL,
  task_id    uuid NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('link', 'doc', 'pr', 'email', 'decision', 'file', 'note')),
  title      text NOT NULL,
  url        text NOT NULL DEFAULT '',
  detail     text NOT NULL DEFAULT '',
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, task_id) REFERENCES tasks(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE outputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE outputs FORCE  ROW LEVEL SECURITY;
CREATE POLICY outputs_isolation ON outputs
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- a work item's outputs (the only access path besides the PK)
CREATE INDEX outputs_task_idx ON outputs (tenant_id, task_id);
