-- 0013_work_item_fields (down)
DROP INDEX IF EXISTS tasks_tenant_ask_by_idx;
DROP INDEX IF EXISTS tasks_tenant_requirement_idx;
DROP INDEX IF EXISTS tasks_tenant_stage_idx;
ALTER TABLE tasks
  DROP CONSTRAINT IF EXISTS tasks_waiting_on_person_fk,
  DROP CONSTRAINT IF EXISTS tasks_requirement_fk,
  DROP CONSTRAINT IF EXISTS tasks_owner_fk,
  DROP COLUMN IF EXISTS ask_by,
  DROP COLUMN IF EXISTS ask,
  DROP COLUMN IF EXISTS waiting_on_since,
  DROP COLUMN IF EXISTS waiting_on_reason,
  DROP COLUMN IF EXISTS waiting_on_person_id,
  DROP COLUMN IF EXISTS definition_of_done,
  DROP COLUMN IF EXISTS requirement_id,
  DROP COLUMN IF EXISTS created_by,
  DROP COLUMN IF EXISTS owner_id,
  DROP COLUMN IF EXISTS stage;
