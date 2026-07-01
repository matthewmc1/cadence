-- 0007_task_importance (down)
DROP INDEX IF EXISTS tasks_tenant_important_idx;
ALTER TABLE tasks DROP COLUMN IF EXISTS important;
