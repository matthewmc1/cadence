-- 0005_absolute_schedule (down)
DROP INDEX IF EXISTS tasks_tenant_sched_at_idx;
ALTER TABLE tasks
  DROP COLUMN IF EXISTS scheduled_at,
  ADD COLUMN scheduled_day integer CHECK (scheduled_day BETWEEN 0 AND 6),
  ADD COLUMN scheduled_hour double precision CHECK (scheduled_hour >= 0 AND scheduled_hour < 24),
  ADD COLUMN on_today boolean NOT NULL DEFAULT false,
  ADD COLUMN today_hour double precision CHECK (today_hour >= 0 AND today_hour < 24);
CREATE INDEX tasks_tenant_today_idx ON tasks (tenant_id, today_hour) WHERE on_today;
CREATE INDEX tasks_tenant_sched_idx ON tasks (tenant_id, scheduled_day) WHERE scheduled_day IS NOT NULL;
