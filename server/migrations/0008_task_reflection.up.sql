-- 0008_task_reflection — capture "what did this advance?" at completion.
--
-- Completion previously recorded only a done_at timestamp, so Cadence knew a
-- task was done but never WHY / what outcome it advanced. `reflection` is an
-- optional one-line note captured at the done transition, so the weekly review
-- and per-project rollups can answer done-and-why.
--
-- No backfill: tenants are user-created and start empty.

ALTER TABLE tasks
  ADD COLUMN reflection text NOT NULL DEFAULT '';
