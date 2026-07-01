-- 0007_task_importance — add the "important" axis to tasks.
--
-- Urgency alone made loud, time-sensitive work always win, crowding out the
-- deep, important-but-not-urgent work Cadence exists to protect. `important`
-- is the second Eisenhower axis: prioritisation now sorts on importance×urgency
-- and the scheduler reserves the energy peak for important/deep work.
--
-- No backfill: tenants are user-created and start empty.

ALTER TABLE tasks
  ADD COLUMN important boolean NOT NULL DEFAULT false;

-- Backlog prioritisation reads important/urgent for unscheduled work.
CREATE INDEX tasks_tenant_important_idx ON tasks (tenant_id, important) WHERE important;
