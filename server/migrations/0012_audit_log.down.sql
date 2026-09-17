-- DDL is not gated by the row-level append-only trigger; drop in dependency
-- order (trigger before the function it calls).
DROP INDEX IF EXISTS audit_log_tenant_at_idx;
DROP TRIGGER IF EXISTS audit_log_append_only ON audit_log;
DROP FUNCTION IF EXISTS audit_log_append_only();
DROP TABLE IF EXISTS audit_log;
