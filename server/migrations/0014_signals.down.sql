-- 0014_signals (down)
DROP INDEX IF EXISTS work_item_signals_signal_idx;
DROP TABLE IF EXISTS work_item_signals;
DROP INDEX IF EXISTS signal_participants_email_idx;
DROP TABLE IF EXISTS signal_participants;
DROP TABLE IF EXISTS signal_bodies;
DROP INDEX IF EXISTS signals_tenant_project_hint_idx;
DROP INDEX IF EXISTS signals_tenant_source_idx;
DROP INDEX IF EXISTS signals_tenant_snoozed_idx;
DROP INDEX IF EXISTS signals_tenant_stage_occurred_idx;
DROP INDEX IF EXISTS signals_tenant_occurred_idx;
DROP TABLE IF EXISTS signals;
DROP FUNCTION IF EXISTS signals_immutable();
DROP INDEX IF EXISTS sources_manual_uniq;
DROP TABLE IF EXISTS sources;
