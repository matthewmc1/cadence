-- 0002_project_archive — example of a zero-downtime EXPAND step.
--
-- Adding a NULLable column with no default takes only a brief metadata lock in
-- modern Postgres (no table rewrite), so it is safe to run while old app
-- instances — which don't know about archived_at — keep serving traffic. The
-- new column is read/written only once the new code is fully rolled out. See
-- docs/MIGRATIONS.md for the full expand → backfill → contract playbook.

ALTER TABLE projects ADD COLUMN archived_at timestamptz;

CREATE INDEX projects_active_idx ON projects (tenant_id) WHERE archived_at IS NULL;
