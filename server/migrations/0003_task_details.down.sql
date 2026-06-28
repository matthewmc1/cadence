-- 0003_task_details (down)
DROP INDEX IF EXISTS tasks_tenant_deadline_idx;
ALTER TABLE tasks
  DROP COLUMN IF EXISTS assignees,
  DROP COLUMN IF EXISTS subtasks,
  DROP COLUMN IF EXISTS links,
  DROP COLUMN IF EXISTS recurrence,
  DROP COLUMN IF EXISTS deadline;
