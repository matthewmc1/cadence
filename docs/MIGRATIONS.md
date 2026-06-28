# Cadence — Migrations & Zero-Downtime Changes

Cadence is built so the schema can evolve **while the app keeps serving traffic**
and **while old and new code run side by side** during a rolling deploy. This
doc covers the runner, the rules, and the expand/contract playbook with a worked
example.

## The migration runner

[`server/internal/store/postgres/migrate.go`](../server/internal/store/postgres/migrate.go)
applies the SQL files embedded from [`server/migrations/`](../server/migrations):

- Files are named `NNNN_name.up.sql` (+ a matching `.down.sql`) and applied in
  lexical order.
- A global **advisory lock** (`pg_advisory_lock`) serializes migrators, so a
  rolling deploy with many pods can't race the same migration.
- Each migration runs in **its own transaction** and is recorded in
  `schema_migrations(version, applied_at)`. Re-running is a no-op.
- `CADENCE_AUTO_MIGRATE=true` (default) applies pending migrations on boot; set
  it false to run migrations as a separate gated step in CI/CD.

Verified: a fresh database applies `0001_init` then `0002_project_archive`, and
re-boot applies nothing.

> **Caveat — `CREATE INDEX CONCURRENTLY`.** It cannot run inside a transaction,
> but the runner wraps each file in one. For a concurrent index, ship it as a
> migration that is run outside the transactional runner (a one-off job, or a
> file the runner is taught to execute with autocommit). The schema's startup
> indexes are small and built normally; large-table indexes added later should
> use `CONCURRENTLY`.

## The one rule: every migration is backward-compatible

During a rolling deploy, version N-1 and version N of the app run at the same
time against the **same** schema. So a migration must never break the code
that's still running. That means:

- **Add before you use.** New columns/tables land (and backfill) before any code
  reads or writes them.
- **Stop using before you drop.** Columns/tables are removed only after no
  running code references them.
- **Never** rename or retype a column in place, add a `NOT NULL` column with no
  default to a large table, or drop a column the old code still selects.

This is the **expand → contract** pattern.

## Expand → contract, in phases

A breaking change (rename, retype, split, tighten a constraint) becomes a
sequence of individually-safe steps, each deployable on its own:

1. **Expand (schema).** Add the new shape additively — a nullable column, a new
   table — no rewrite, no blocking lock.
2. **Dual-write (code).** Deploy code that writes **both** old and new shapes.
   Old instances still only know the old shape; that's fine.
3. **Backfill (data).** Copy existing rows old→new in **batches** with a
   `lock_timeout`, so you never hold a long lock or a giant transaction.
4. **Read-new (code).** Once backfill is complete and verified, deploy code that
   reads the new shape (still writing both).
5. **Contract (code).** Deploy code that no longer touches the old shape.
6. **Contract (schema).** Drop the old column/table. (Optionally add the
   `NOT NULL`/constraint now that data is guaranteed clean.)

Each step is reversible by rolling back the previous app version — nothing is
destroyed until step 6, by which point the old shape is provably unused.

## Worked example: rename `tasks.note` → `tasks.summary`

A rename is the canonical "can't do it in place" change.

**Migration A — expand (additive, instant):**
```sql
ALTER TABLE tasks ADD COLUMN summary text NOT NULL DEFAULT '';
```
Adding a NULLable column, or one with a constant default, is metadata-only in
modern Postgres — a brief lock, no rewrite. Old code ignores `summary`.

**Deploy app v+1 — dual-write:** writes set both `note` and `summary`.

**Migration B — backfill in batches** (run as a job, not a blocking migration):
```sql
-- repeat until 0 rows; keep each batch short
WITH batch AS (
  SELECT tenant_id, id FROM tasks
  WHERE summary = '' AND note <> ''
  LIMIT 5000 FOR UPDATE SKIP LOCKED
)
UPDATE tasks t SET summary = t.note
FROM batch b WHERE t.tenant_id = b.tenant_id AND t.id = b.id;
```

**Deploy app v+2 — read-new:** reads `summary`, still writes both.

**Deploy app v+3 — contract (code):** stops referencing `note`.

**Migration C — contract (schema):**
```sql
ALTER TABLE tasks DROP COLUMN note;
```

At no point did the app stop serving, and at every point a rollback to the
previous app version was safe.

## Migration `0002` as a live example

[`0002_project_archive`](../server/migrations/0002_project_archive.up.sql) is a
real expand step:

```sql
ALTER TABLE projects ADD COLUMN archived_at timestamptz;        -- nullable, instant
CREATE INDEX projects_active_idx ON projects (tenant_id) WHERE archived_at IS NULL;
```

It adds `archived_at` without a rewrite and a partial index for active projects.
Old instances that don't know about `archived_at` keep working unchanged — the
column is simply unused until the soft-delete feature ships.

## Lock-aware DDL checklist

- Set a short `SET lock_timeout = '3s'` around DDL so a migration **fails fast**
  instead of queuing behind (and then blocking) live queries.
- Adding a column: nullable or constant-default only on big tables.
- Adding a constraint: add `NOT VALID`, then `VALIDATE CONSTRAINT` (takes a
  weaker lock and can be done online).
- New index on a large table: `CREATE INDEX CONCURRENTLY` (outside a tx).
- Backfills: batch with `LIMIT … FOR UPDATE SKIP LOCKED`; never one giant
  `UPDATE`.

## Rollback

- Every migration ships a `.down.sql`. `down` is useful in dev and for the most
  recent migration.
- In production, **prefer rolling forward** (a new corrective migration) over
  `down` for anything that has been live — `down`-ing a dropped column can't
  bring data back. Expand/contract makes forward-fix the natural path.

## Why shared-schema multi-tenancy makes this cheap

With one shared schema, a migration runs **once** for all tenants. The
alternatives pay a tax on every change:

- **Schema-per-tenant:** every migration fans out across N schemas — slow,
  partially-applied states, and orchestration to track which tenant is on which
  version.
- **Database-per-tenant:** the same fan-out across N databases/clusters.

Cadence chose shared-schema + RLS (see [ARCHITECTURE.md](./ARCHITECTURE.md))
precisely so schema evolution stays a single, fast, atomic-per-step operation.

## Zero-downtime deploy sequence (summary)

```
apply EXPAND migration ─► roll out dual-write code ─► backfill (batched)
   ─► roll out read-new code ─► roll out contract code ─► apply CONTRACT migration
```

Each arrow is independently deployable and independently reversible.
