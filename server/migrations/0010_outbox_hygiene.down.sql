DROP INDEX IF EXISTS outbox_tenant_idx;
DROP INDEX IF EXISTS outbox_created_idx;
ALTER TABLE outbox DROP COLUMN IF EXISTS entity_type;
