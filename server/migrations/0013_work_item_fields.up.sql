-- 0013_work_item_fields — tasks grow into work items (R0.3).
--
-- Cadence is pivoting from an energy calendar to a context-first work hub. A
-- work item is what a task becomes when it carries the context it derives
-- from: who owns it, which requirement it serves, what "done" means, who it
-- is waiting on, and the ask it makes of someone else. The physical table is
-- NEVER renamed — it stays `tasks`, this is an additive EXPAND, and every new
-- column is NULLable or has a default so old app instances keep serving.
--
-- stage replaces status. status is a native enum and ALTER TYPE … ADD VALUE
-- cannot run inside the runner's per-file transaction, so stage is a new
-- text + CHECK column alongside it (the rule from 0009). Both are kept
-- coherent by the server for one release, then status goes:
--
--   status     → stage        stage    → status
--   backlog    → todo         todo     → scheduled if scheduled_at set, else backlog
--   scheduled  → todo         doing    → focus
--   focus      → doing        waiting  → backlog
--   done       → done         done     → done
--
-- The mapping lives in exactly one place in the app (domain.DeriveLifecycle);
-- the backfill below is its status→stage half applied once to existing rows.
--
-- References are tenant-local composite FKs with the column-subset
-- ON DELETE SET NULL form from 0009, so nulling one reference never tries to
-- null tenant_id. owner_id and waiting_on_person_id point at users for now
-- (a people table comes later and will take over waiting_on_person_id).
-- created_by deliberately has NO FK: it is attribution, like
-- audit_log.actor_id, and should survive the actor leaving the tenant.
--
-- The waiting rule (stage = 'waiting' needs a reason or a person) is enforced
-- in the app's patch validation, not as a CHECK: a CHECK would make the
-- SET NULL on waiting_on_person_id fail when that user is deleted.

ALTER TABLE tasks
  ADD COLUMN stage                text NOT NULL DEFAULT 'todo'
                                  CHECK (stage IN ('todo', 'doing', 'waiting', 'done')),
  ADD COLUMN owner_id             uuid,
  ADD COLUMN created_by           uuid,
  ADD COLUMN requirement_id       uuid,
  -- what "done" means for this item, in the owner's words
  ADD COLUMN definition_of_done   text NOT NULL DEFAULT '',
  -- stage = 'waiting': on whom / why / since when
  ADD COLUMN waiting_on_person_id uuid,
  ADD COLUMN waiting_on_reason    text NOT NULL DEFAULT '',
  ADD COLUMN waiting_on_since     timestamptz,
  -- the ask this item makes of someone: bounded {what, forWhom, why}
  ADD COLUMN ask                  jsonb NOT NULL DEFAULT '{}'::jsonb
                                  CHECK (jsonb_typeof(ask) = 'object'),
  -- when the ask is needed by — a deadline by another name, hoisted so it
  -- can be indexed and scanned like one
  ADD COLUMN ask_by               timestamptz;

-- backfill stage from status (the status→stage half of the mapping above)
--
-- tasks is FORCE ROW LEVEL SECURITY with a fail-closed policy, and the runner
-- sets no app.tenant_id — so for any migrating role that is not a superuser
-- or BYPASSRLS (the table OWNER included: that is what FORCE means) the
-- UPDATE below would silently match zero rows and the migration would still
-- be recorded as applied. FORCE is lifted for the backfill and put back in
-- the same transaction: the owner may ALTER its own table, and an owner
-- without FORCE bypasses RLS; a role that owns nothing fails here loudly
-- instead of half-applying. The DO block is the postcondition — if any row
-- still disagrees with its status the whole migration rolls back.
-- (docs/MIGRATIONS.md: backfills must run with RLS bypassed.)
ALTER TABLE tasks NO FORCE ROW LEVEL SECURITY;

UPDATE tasks SET stage = CASE status
  WHEN 'focus' THEN 'doing'
  WHEN 'done'  THEN 'done'
  ELSE              'todo'   -- backlog, scheduled
END;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM tasks
    WHERE (status = 'done'  AND stage <> 'done')
       OR (status = 'focus' AND stage <> 'doing')
  ) THEN
    RAISE EXCEPTION '0013: stage backfill incomplete — was the UPDATE fenced by row-level security?';
  END IF;
END $$;

ALTER TABLE tasks FORCE ROW LEVEL SECURITY;

ALTER TABLE tasks ADD CONSTRAINT tasks_owner_fk
  FOREIGN KEY (tenant_id, owner_id) REFERENCES users(tenant_id, id) ON DELETE SET NULL (owner_id);
ALTER TABLE tasks ADD CONSTRAINT tasks_requirement_fk
  FOREIGN KEY (tenant_id, requirement_id) REFERENCES requirements(tenant_id, id) ON DELETE SET NULL (requirement_id);
ALTER TABLE tasks ADD CONSTRAINT tasks_waiting_on_person_fk
  FOREIGN KEY (tenant_id, waiting_on_person_id) REFERENCES users(tenant_id, id) ON DELETE SET NULL (waiting_on_person_id);

-- access paths: stage columns (successor to tasks_tenant_status_idx), a
-- requirement's work items (also what the SET NULL cascade walks), and asks
-- due soon
CREATE INDEX tasks_tenant_stage_idx       ON tasks (tenant_id, stage);
CREATE INDEX tasks_tenant_requirement_idx ON tasks (tenant_id, requirement_id) WHERE requirement_id IS NOT NULL;
CREATE INDEX tasks_tenant_ask_by_idx      ON tasks (tenant_id, ask_by) WHERE ask_by IS NOT NULL;
