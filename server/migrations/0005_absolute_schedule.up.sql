-- 0005_absolute_schedule — schedule tasks at an absolute datetime.
--
-- The relative "day 0..6 within the current week" model can't express
-- long-term plans, week navigation, a month view, or recurrence. We replace it
-- with a single `scheduled_at timestamptz`. The Today timeline is now derived
-- (scheduled_at on the current date) rather than a separate on_today flag.
--
-- No backfill: tenants are user-created and start empty.

ALTER TABLE tasks
  ADD COLUMN scheduled_at timestamptz,
  DROP COLUMN scheduled_day,
  DROP COLUMN scheduled_hour,
  DROP COLUMN on_today,
  DROP COLUMN today_hour;

DROP INDEX IF EXISTS tasks_tenant_today_idx;
DROP INDEX IF EXISTS tasks_tenant_sched_idx;
CREATE INDEX tasks_tenant_sched_at_idx ON tasks (tenant_id, scheduled_at) WHERE scheduled_at IS NOT NULL;
