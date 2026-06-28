-- 0003_task_details — richer task fields for "how work actually gets done".
--
-- All additive: NULLable / constant-default columns only, so this is a clean
-- zero-downtime EXPAND step — old app instances ignore the new columns while
-- the new editor (deadline, recurrence, links, subtasks, assignees) rolls out.
--
-- links / subtasks / assignees are stored as JSONB on the task row: they are
-- lightweight, always loaded with their parent, and never queried independently,
-- so denormalizing keeps reads single-row and keeps memory/Postgres parity
-- trivial. (A normalized split is the path if they ever need their own queries.)

ALTER TABLE tasks
  ADD COLUMN deadline   timestamptz,
  ADD COLUMN recurrence text  NOT NULL DEFAULT 'none'
    CHECK (recurrence IN ('none', 'daily', 'weekdays', 'weekly', 'monthly')),
  ADD COLUMN links     jsonb  NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN subtasks  jsonb  NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN assignees jsonb  NOT NULL DEFAULT '[]'::jsonb;

-- tasks due soon, for deadline-aware scheduling/sorting
CREATE INDEX tasks_tenant_deadline_idx ON tasks (tenant_id, deadline) WHERE deadline IS NOT NULL;
