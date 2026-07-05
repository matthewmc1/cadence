ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_client_fk;
DROP INDEX IF EXISTS projects_client_idx;
ALTER TABLE projects DROP COLUMN IF EXISTS client_id;
DROP TABLE IF EXISTS clients;
