# Cadence — Data Schema

Source of truth: [`server/migrations/`](../server/migrations). The schema is
designed for **multi-tenant density**, **tenant isolation by default**, and
**partition-ready scale**.

## Entity model

```
 tenants ──1:N── users
    │ 1            │
    │ N            │ (project_members.user_id)
 projects ──1:N── project_members
    │ 1
    │ N
  tasks            outbox   (append-only event log; routed by tenant_id)
```

A **task** is the heart of the model. One normalized row drives Today, Plan and
Board — the views are projections of its fields (see
[`selectors.ts`](../src/state/selectors.ts)).

## Tables

### `tenants`
The unit of isolation.

| column | type | notes |
|---|---|---|
| `id` | `uuid` PK | |
| `name` | `text` | |
| `created_at` | `timestamptz` | |

RLS: `id = NULLIF(current_setting('app.tenant_id', true), '')::uuid`.

### `users`
Members of a tenant (assignees, avatars).

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` |
| `id` | `uuid` | |
| `email` | `text` | **the only stored PII** — name/initial/avatar colour are derived (migration `0004`) |
| `created_at` | `timestamptz` | |
| PK | `(tenant_id, id)` | tenant-leading |
| UNIQUE | `(tenant_id, email)` | per-tenant unique email |

### Auth tables (migration `0004`, **not** RLS-scoped)
Identity must be resolvable *before* a tenant context exists, so these live
outside RLS and hold only the email + opaque token hashes.

| table | purpose |
|---|---|
| `accounts(email PK, tenant_id, user_id)` | email → personal tenant/user directory |
| `login_tokens(token_hash PK, email, expires_at, consumed_at)` | one-time magic-link tokens |
| `sessions(token_hash PK, user_id, tenant_id, email, expires_at)` | active cookie sessions |

### `projects`
Outcome-oriented grouping with a due date.

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` |
| `id` | `uuid` | |
| `name`, `subtitle`, `due`, `color` | `text` | |
| `version` | `integer` | optimistic-concurrency token |
| `created_at`, `updated_at` | `timestamptz` | `updated_at` kept by trigger |
| PK | `(tenant_id, id)` | |

`archived_at timestamptz` is added by migration `0002` (see below).

### `project_members`
The avatar stack on a project.

| column | type | notes |
|---|---|---|
| `tenant_id`, `project_id`, `user_id` | `uuid` | |
| `initial`, `color` | `text` | |
| PK | `(tenant_id, project_id, user_id)` | |
| FK | `(tenant_id, project_id)` → `projects(tenant_id, id)` | tenant-local |
| FK | `(tenant_id, user_id)` → `users(tenant_id, id)` | tenant-local |

### `tasks`
The normalized record behind every view.

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` |
| `id` | `uuid` | UUIDv7 (time-ordered) |
| `project_id` | `uuid` NULL | FK → `projects` (tenant-local; `ON DELETE SET NULL`) |
| `title` | `text` | |
| `kind` | `task_kind` enum | `deep \| light \| admin \| meet \| personal` |
| `status` | `task_status` enum | `backlog \| scheduled \| focus \| done` (→ Board columns) |
| `effort_minutes` | `integer` | `CHECK >= 0` |
| `urgent` | `boolean` | |
| `note` | `text` | |
| `place` | `text` NULL | Home / Office / Café |
| `scheduled_at` | `timestamptz` NULL | absolute datetime the task is planned for (migration `0005`). Today / Plan-week / Plan-month and recurrence all derive from it |
| `position` | `double precision` | ordering within a list/column |
| `done_at` | `timestamptz` NULL | set when `status='done'` |
| `version` | `integer` | optimistic-concurrency token |
| `created_at`, `updated_at` | `timestamptz` | `updated_at` kept by trigger |
| `deadline` | `timestamptz` NULL | soft due date (migration `0003`) |
| `recurrence` | `text` | `none\|daily\|weekdays\|weekly\|monthly` (migration `0003`) |
| `links` | `jsonb` | `[{label,url}]` — attached references (migration `0003`) |
| `subtasks` | `jsonb` | `[{id,title,done}]` — checklist (migration `0003`) |
| `assignees` | `jsonb` | `[{userId,initial,color}]` — "With" (migration `0003`) |
| PK | `(tenant_id, id)` | tenant-leading |
| FK | `(tenant_id, project_id)` → `projects(tenant_id, id)` | composite, nullable |

> `links`/`subtasks`/`assignees` are JSONB on the row by design — they are
> lightweight, always loaded with the parent, and never queried independently,
> which keeps reads single-row and memory/Postgres parity trivial. Normalize
> them into child tables only if they ever need their own queries.

**Indexes** — chosen for the exact access patterns of the three views:

| index | serves |
|---|---|
| `tasks_tenant_status_idx (tenant_id, status)` | Board columns, status filters |
| `tasks_tenant_project_idx (tenant_id, project_id) WHERE project_id IS NOT NULL` | a project's board |
| `tasks_tenant_sched_at_idx (tenant_id, scheduled_at) WHERE scheduled_at IS NOT NULL` | Today / week / month date-range scans (partial) |
| `tasks_tenant_deadline_idx (tenant_id, deadline) WHERE deadline IS NOT NULL` | deadline-aware sorting |

### `outbox`
Transactional realtime + durable replay log. **Not** RLS-restricted: a relay
reads across tenants (access controlled at the role level); rows still carry
`tenant_id` for routing.

| column | type | notes |
|---|---|---|
| `id` | `uuid` PK | |
| `tenant_id` | `uuid` | routing |
| `type` | `text` | `task.created`, `task.updated`, … |
| `entity_id` | `uuid` | the changed row (covers deletes) |
| `actor_id` | `uuid` NULL | who made the change |
| `payload` | `jsonb` | full event, < 8 KB (NOTIFY limit) |
| `created_at`, `published_at` | `timestamptz` | `published_at` NULL = pending |
| index | `(created_at) WHERE published_at IS NULL` | relay scan |

An `AFTER INSERT` trigger calls `pg_notify('cadence_events', payload)`.

## Row-Level Security

Every tenant-scoped table:

```sql
ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;
ALTER TABLE <t> FORCE  ROW LEVEL SECURITY;   -- applies even to the owner
CREATE POLICY <t>_isolation ON <t>
  USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
```

- `USING` filters reads/updates/deletes; `WITH CHECK` prevents writing another
  tenant's `tenant_id`.
- `NULLIF(…, '')` makes isolation **fail closed** — unset/empty → no rows.
- The app sets the tenant once per transaction via
  `SELECT set_config('app.tenant_id', $1, true)` (transaction-local, pooler-safe).

## Design rationale

- **Tenant-leading composite PKs `(tenant_id, id)`** keep every lookup, foreign
  key and unique constraint tenant-local. This is the prerequisite for `HASH`
  partitioning `tasks` by `tenant_id` later — the partition key must be in every
  unique constraint — so we can shard without touching application code.
- **UUIDv7 ids** are time-ordered, so primary-key inserts stay append-mostly
  (good b-tree locality) and ids sort by creation — friendlier to range
  partitioning and pagination than v4.
- **Native enums** (`task_kind`, `task_status`) give integrity and compact storage.
- **`version` columns** enable optimistic concurrency (`If-Match` → `409`).
- **`double precision` hours** scan cleanly into the app's decimal hour model
  (9.5 = 9:30) without numeric-codec friction.
- **Outbox over dual-write** makes the change and its event atomic, and is the
  seam for swapping NOTIFY for a real bus at scale.

## Partitioning plan (when `tasks` gets large)

```sql
-- tasks becomes a partitioned parent; PK already leads with tenant_id
CREATE TABLE tasks (...) PARTITION BY HASH (tenant_id);
CREATE TABLE tasks_p0 PARTITION OF tasks FOR VALUES WITH (MODULUS 8, REMAINDER 0);
-- … p1..p7
```

Because the application only ever queries within a tenant, every query carries
`tenant_id` and prunes to a single partition. Whale tenants can be split out:
`FOR VALUES IN (<tenant_id>)` on a `LIST`-partitioned variant.
