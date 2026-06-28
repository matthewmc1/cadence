-- 0002_project_archive (down)
DROP INDEX IF EXISTS projects_active_idx;
ALTER TABLE projects DROP COLUMN IF EXISTS archived_at;
