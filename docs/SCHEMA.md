# Cadence — Data Schema

Source of truth: [`server/migrations/`](../server/migrations). The schema is
designed for **multi-tenant density**, **tenant isolation by default**, and
**partition-ready scale**.

## Entity model

```
 tenants ──1:N── users
    │ 1            │
    │ N            │
 clients           │ (project_members.user_id)
    │ 1            │
    │ N            │
 projects ──1:N── project_members
    │ 1    └──1:N── requirements   (what the project must deliver; never tasks)
    │ N
  tasks ──1:N── outputs            (what the work item produced)
    │ N
    └── work_item_signals ── signals ──1:1── signal_bodies      (the captured record, immutable;
         (origins; snapshot     │ N   └──1:N── signal_participants   never a task, never in Bootstrap)
          outlives the signal)  │ 1
                             sources        (where signals come from; one implicit 'manual' per tenant)

                   outbox     (bounded event log; routed by tenant_id, pruned by age)
                   audit_log  (append-only; who did what, per tenant)
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
| `api_tokens(token_hash PK, id UNIQUE, tenant_id, user_id, name, created_at, last_used_at)` | personal access tokens for the MCP server / API clients (migration `0006`) |

### `clients` (migration `0009`)
The strategic spine: who a body of work is ultimately for. Projects belong to a
client; tasks inherit their client **through** their project (there is
deliberately no `client_id` on tasks — one source of truth).

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` `ON DELETE CASCADE` |
| `id` | `uuid` | |
| `name` | `text` | |
| `tier` | `text` | `a \| b \| c` (CHECK) — how much attention it warrants |
| `kind` | `text` | `client \| internal` (CHECK) |
| `color` | `text` | |
| `expected_touch_days` | `integer` NULL | cadence target; flag "underserved" when untouched longer. NULL = no expectation |
| `archived_at` | `timestamptz` NULL | |
| `version` | `integer` | optimistic-concurrency token |
| `created_at`, `updated_at` | `timestamptz` | `updated_at` kept by trigger |
| PK | `(tenant_id, id)` | tenant-leading |
| index | `clients_active_idx (tenant_id) WHERE archived_at IS NULL` | |

### `projects`
Outcome-oriented grouping with a due date.

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` |
| `id` | `uuid` | |
| `client_id` | `uuid` NULL | FK → `clients` (tenant-local; `ON DELETE SET NULL (client_id)`) — migration `0009` |
| `name`, `subtitle`, `due`, `color` | `text` | |
| `version` | `integer` | optimistic-concurrency token |
| `created_at`, `updated_at` | `timestamptz` | `updated_at` kept by trigger |
| PK | `(tenant_id, id)` | |
| FK | `(tenant_id, client_id)` → `clients(tenant_id, id)` | composite, nullable |
| index | `projects_client_idx (tenant_id, client_id) WHERE client_id IS NOT NULL` | a client's projects |

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
| `status` | `task_status` enum | `backlog \| scheduled \| focus \| done` (→ Board columns). **Being replaced by `stage`** — kept coherent for one release, see below |
| `stage` | `text` | `todo \| doing \| waiting \| done` (CHECK), default `todo` — the work-item lifecycle (migration `0013`) |
| `owner_id` | `uuid` NULL | FK → `users` (tenant-local; `ON DELETE SET NULL (owner_id)`) — who owns the item (migration `0013`) |
| `created_by` | `uuid` NULL | the actor at create; attribution only, **no FK** (like `audit_log.actor_id`), never patchable (migration `0013`) |
| `requirement_id` | `uuid` NULL | FK → `requirements` (tenant-local; `ON DELETE SET NULL (requirement_id)`) — the requirement this item serves (migration `0013`) |
| `definition_of_done` | `text` | what "done" means, in the owner's words (migration `0013`) |
| `waiting_on_person_id` | `uuid` NULL | FK → `users` for now (tenant-local; `ON DELETE SET NULL`); a `people` table takes over later (migration `0013`) |
| `waiting_on_reason` | `text` | `stage='waiting'` **requires** a non-empty reason or a `waiting_on_person_id` — app-enforced, not a CHECK (a CHECK would break the SET NULL) |
| `waiting_on_since` | `timestamptz` NULL | stamped entering `waiting`, cleared leaving it; server-owned |
| `ask` | `jsonb` | bounded `{what, forWhom, why}` (≤ 2000 chars each, app-enforced); `{}` when empty; `CHECK jsonb_typeof = 'object'` |
| `ask_by` | `timestamptz` NULL | when the ask is needed by — a deadline by another name, hoisted out of `ask` so it indexes like one |
| `effort_minutes` | `integer` | `CHECK >= 0` |
| `urgent` | `boolean` | |
| `important` | `boolean` | the second Eisenhower axis — protects deep, non-urgent work (migration `0007`) |
| `note` | `text` | |
| `reflection` | `text` | "what did this advance?" — captured at the done transition (migration `0008`) |
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
| FK | `(tenant_id, owner_id)` → `users(tenant_id, id)` | `ON DELETE SET NULL (owner_id)` |
| FK | `(tenant_id, requirement_id)` → `requirements(tenant_id, id)` | `ON DELETE SET NULL (requirement_id)` |
| FK | `(tenant_id, waiting_on_person_id)` → `users(tenant_id, id)` | `ON DELETE SET NULL (waiting_on_person_id)` |

**`status` → `stage`.** The physical table is never renamed and `status` is a
native enum (so it cannot grow `waiting` inside the migration transaction);
`stage` is the `text + CHECK` successor added alongside it. Migration `0013`
backfills `stage` once, and the server then keeps the pair coherent on every
write through the single mapping `domain.DeriveLifecycle` (used by both
create paths and `store.ApplyTaskPatch`): set either field and the other is
derived; if a patch carries both, `stage` wins. `status` is removed one
release later.

| `status` → `stage` | `stage` → `status` |
|---|---|
| `backlog` → `todo` | `todo` → `scheduled` if `scheduled_at` is set, else `backlog` |
| `scheduled` → `todo` | `doing` → `focus` |
| `focus` → `doing` | `waiting` → `backlog` |
| `done` → `done` | `done` → `done` |

**`originCount` / `outputCount` — derived, never stored.** Every task the API
returns (list, get, bootstrap, create, update, and the `task.*` event body)
carries two counts: how many signals it derives from (`work_item_signals`) and
how many artefacts it produced (`outputs`). They are **not columns**: Postgres
computes them as two correlated sub-selects in `taskCols`, the memory adapter
counts its maps, and a patch that names them is ignored like any unknown key.
They exist so a work-item list can show provenance in both directions without
one request per row; the lists themselves stay at `/tasks/{id}/origins` and
`/tasks/{id}/outputs`.

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
| `tasks_tenant_important_idx (tenant_id, important) WHERE important` | backlog prioritisation (migration `0007`) |
| `tasks_tenant_stage_idx (tenant_id, stage)` | stage columns — the successor to the status index (migration `0013`) |
| `tasks_tenant_requirement_idx (tenant_id, requirement_id) WHERE requirement_id IS NOT NULL` | a requirement's work items; also what the SET NULL cascade walks (migration `0013`) |
| `tasks_tenant_ask_by_idx (tenant_id, ask_by) WHERE ask_by IS NOT NULL` | asks due soon (partial, migration `0013`) |

### `requirements` (migration `0011`)
What a project must deliver. A requirement is **never a task** — work items
derive from requirements the way they derive from signals. It belongs to
exactly one project and is deleted with it (`ON DELETE CASCADE`, not `SET
NULL`: a requirement without a project is meaningless). Small and always
needed, so it hydrates in `Bootstrap`.

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` `ON DELETE CASCADE` |
| `id` | `uuid` | |
| `project_id` | `uuid` | NOT NULL; FK → `projects` (tenant-local composite, `ON DELETE CASCADE`) |
| `title` | `text` | |
| `description` | `text` | |
| `weight` | `integer` | `CHECK 1..5`, default 3 — 1 nice-to-have … 5 project fails without it |
| `acceptance` | `text` | how we will know it is met |
| `status` | `text` | `open \| met \| dropped` (CHECK), default `open` |
| `position` | `double precision` | ordering within the project |
| `archived_at` | `timestamptz` NULL | |
| `version` | `integer` | optimistic-concurrency token |
| `created_at`, `updated_at` | `timestamptz` | `updated_at` kept by trigger |
| PK | `(tenant_id, id)` | tenant-leading |
| FK | `(tenant_id, project_id)` → `projects(tenant_id, id)` | composite, NOT NULL |
| index | `requirements_project_idx (tenant_id, project_id)` | a project's requirements |

Events ride the generic envelope (`entityType: "requirement"`, body in
`entity`). Patch semantics live in `store.ApplyRequirementPatch`, shared by both
adapters.

### `audit_log` (migration `0012`)
The tenant's own record of who did what: magic links issued/verified, personal
access tokens minted/revoked, AI gateway calls, and (reserved) MCP reads,
exports, purges, connector syncs. Read-only over HTTP (`GET /api/v1/audit`,
keyset-paged newest first); rows are produced server-side only.

**Append-only.** A `BEFORE UPDATE OR DELETE` trigger raises on every row
change. The single exception is a `DELETE` while the transaction-local setting
`app.purging = '1'` is set — only `PurgeTenant` sets it, so a purge (which
takes the whole tenant) is the one way history disappears. Because rows are
immutable there is no `updated_at`, no `set_updated_at` trigger and no
`version` column; the rest of the tenancy boilerplate applies.

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` `ON DELETE CASCADE` (the cascade also needs `app.purging`) |
| `id` | `uuid` | UUIDv7 |
| `at` | `timestamptz` | when |
| `actor_id` | `uuid` NULL | the `users` row; NULL for system-initiated rows |
| `kind` | `text` | `auth.login.issued \| auth.login.verified \| auth.token.create \| auth.token.revoke \| ai.chat \| ai.extract \| signal.capture \| signal.body.read \| signal.delete \| signal.retention \| signal.prune \| source.delete \| mcp.read \| export \| purge \| connector.*` — deliberately **not** CHECKed (a log must never refuse a new producer's kind) |
| `entity_type`, `entity_id` | `text` NULL, `uuid` NULL | the thing the row is about (e.g. `api_token`) |
| `detail` | `jsonb` | **bounded (4 KiB, app-enforced) and PII-free** — see below |
| `ip_hash` | `text` NULL | salted SHA-256 prefix of the client address (auth rows) |
| PK | `(tenant_id, id)` | tenant-leading |
| index | `audit_log_tenant_at_idx (tenant_id, at DESC, id DESC)` | the paged reader's keyset |

**No PII in an audit row.** `detail` carries only what is needed to interpret
the row — counts, a model name, a token's display name, an entity id. Never an
email, an address, a prompt, a signal body, a credential. Identity is by
`actor_id`; the only trace of the network peer is `ip_hash`, a digest that lets
an operator correlate "same source" without the address being stored (it is a
correlation key, not an anonymisation — it stays inside the tenant-fenced log
and is never exported). `store.NormalizeAuditEntry` enforces the bound and the
JSON shape on both adapters. Appending emits **no** realtime event.

**Who writes what.** `auth.*` and `ai.chat` come from the auth handlers and the
AI gateway; `ai.extract` from every fact extraction (model, prompt version,
signal id); `signal.capture` (`{kind, textChars, participants}`),
`signal.body.read` (`{bodyChars}`) and `signal.delete` from the signal handlers.
The three bulk deletions carry their size: `signal.retention`
(`{retentionUntil}`, a deletion scheduled through a PATCH), `source.delete`
(`{signals: n}`, the cascade) and `signal.prune` (`{signals: n}`, one row per
tenant per hourly sweep — the only row here with no actor, since no user asked
for it). Counts and ids only, never a title, a body or an address.

### Signals (migration `0014`)
A **signal** is something that arrived — meeting notes, a pasted email, a link,
a chat message, a document. It is **never a task**: work items derive from
signals (through `work_item_signals`) the way they derive from requirements,
and the signal stays behind as the immutable record of what was captured.
Signals are unbounded, so they are **never in `Bootstrap`** and are reachable
only through the keyset-paged `GET /api/v1/signals` (newest first by
`(occurred_at, id)`) and `GET /api/v1/signals/count?stage=inbox` (the badge).

Five tables share the `0009` boilerplate (tenant-leading PK, CASCADE to
tenants, RLS + FORCE RLS with the fail-closed NULLIF policy, `text + CHECK`
enumerations):

#### `sources`
Where signals come from. Every tenant has exactly one implicit `manual` source
(the paste box), created lazily by `store.GetOrCreateManualSource` and pinned
by the partial unique index `sources_manual_uniq (tenant_id) WHERE kind =
'manual'`; connectors add their own. **No credentials here** — `config` is
non-secret settings and a source row is safe to list; a `source_credentials`
table comes in a later slice.

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` `ON DELETE CASCADE` |
| `id` | `uuid` | |
| `kind` | `text` | `manual \| calendar \| email \| notes \| docs \| chat` (CHECK) |
| `name` | `text` | |
| `owner_id` | `uuid` NULL | FK → `users` (tenant-local; `ON DELETE SET NULL (owner_id)`) |
| `config` | `jsonb` | non-secret settings; `{}` default; `CHECK jsonb_typeof = 'object'`; ≤ 16 KiB (app) |
| `consent_at` | `timestamptz` NULL | when the owner consented to this source being read |
| `retention_days` | `integer` NULL | `CHECK > 0`; copied onto `signals.retention_until` at capture. NULL = keep |
| `disabled_at` | `timestamptz` NULL | |
| `version` | `integer` | optimistic-concurrency token |
| `created_at`, `updated_at` | `timestamptz` | `updated_at` kept by trigger |
| PK | `(tenant_id, id)` | |

#### `signals`
The captured record. `UNIQUE (tenant_id, source_id, external_id)` makes
connector re-syncs idempotent (a manual capture's `external_id` is its own
`id`). The **body is not here** — see `signal_bodies`.

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` `ON DELETE CASCADE` |
| `id` | `uuid` | UUIDv7 |
| `source_id` | `uuid` | NOT NULL; FK → `sources` (tenant-local, `ON DELETE CASCADE` — a source takes its signals with it) |
| `external_id` | `text` | the source's own id for this thing |
| `kind` | `text` | `meeting \| email \| note \| doc \| chat \| link \| text` (CHECK) |
| `title` | `text` | falls back to the first line of the text |
| `excerpt` | `text` | first 2000 characters of the body (`CHECK char_length <= 2000`) — enough to triage from the list |
| `body_ref` | `text` | source-native locator for the full thing; may be `''` |
| `occurred_at` | `timestamptz` | when it happened — the paging key |
| `captured_by` | `uuid` NULL | attribution only, **no FK** (like `tasks.created_by`) |
| `stage` | `text` | `inbox \| snoozed \| promoted \| attached \| dismissed` (CHECK), default `inbox` |
| `snoozed_until` | `timestamptz` NULL | required while `snoozed`, cleared otherwise |
| `disposition_at` | `timestamptz` NULL | when it left `inbox`; NULL while in inbox |
| `extracted` | `jsonb` | facts pulled from the body (dates, people, links, deadlines); `{}` default; manual capture seeds `{"links": […]}`; an entry a human rewrote carries `"corrected": true` and no later extraction overwrites it; ≤ 16 KiB (app) |
| `project_hint` | `uuid` NULL | FK → `projects` (tenant-local; `ON DELETE SET NULL (project_hint)`) |
| `retention_until` | `timestamptz` NULL | from the source's `retention_days` unless given; swept hourly (below) |
| `version` | `integer` | optimistic-concurrency token |
| `created_at`, `updated_at` | `timestamptz` | `updated_at` kept by trigger |
| PK | `(tenant_id, id)` | |
| UNIQUE | `(tenant_id, source_id, external_id)` | idempotent re-sync |
| index | `signals_tenant_occurred_idx (tenant_id, occurred_at DESC, id DESC)` | the paged reader's keyset |
| index | `signals_tenant_stage_occurred_idx (tenant_id, stage, occurred_at DESC, id DESC)` | the inbox feed |
| index | `signals_tenant_snoozed_idx (tenant_id, snoozed_until) WHERE snoozed_until IS NOT NULL` | what wakes up next |
| index | `signals_tenant_source_idx`, `signals_tenant_project_hint_idx` | a source's / a project's signals |

**Immutable.** Everything above `stage` never changes after insert. The
`signals_immutable` `BEFORE UPDATE` trigger is an **allow-list**: it strips
`stage, snoozed_until, disposition_at, extracted, project_hint,
retention_until, version, updated_at` from the OLD and NEW rows (`to_jsonb(row)
- text[]`) and raises `insufficient_privilege` if anything else differs — so a
column added later is frozen by default. `store.ApplySignalPatch` refuses the
same keys on both adapters with a 400 naming the field, so the trigger is the
backstop, not the error path. `DELETE` stays allowed (purge, retention, a
data-subject request).

**The retention sweep.** `retention_until` is not decoration: both adapters
run `PruneSignals` **hourly** (Postgres from `pruneLoop` alongside the outbox
prune, memory from its own ticker) and it deletes every signal past it —
`signal_bodies` and `signal_participants` cascade with it, `work_item_signals`
rows keep their snapshot. So `retentionDays` on a source, or a
`PATCH /signals/{id} {"retentionUntil": …}`, is a *scheduled deletion*: set it
to a past instant and the row is gone within the hour. No realtime event is
emitted (housekeeping, not a user action), but the sweep is **audited**: one
`signal.prune` row per tenant per pass, written in the same transaction as the
delete, carrying `{"signals": n}` and nothing about them. Arming it through a
PATCH writes a `signal.retention` row, and deleting a source — which cascades
every signal filed under it — writes a `source.delete` row with the count.
The implicit `manual` source is refused by both adapters (400 on `id`): it is
where every pasted capture is filed, and nobody deletes it by accident.

#### `signal_bodies`
The full text, one row per signal, **never selected with the signal row** — a
list of 100 signals must not drag 100 transcripts through the API. Fetched by
`GET /api/v1/signals/{id}/body` only, which appends a `signal.body.read` audit
row. Written once with the signal; no `version`/`updated_at`.

| column | type | notes |
|---|---|---|
| `tenant_id`, `signal_id` | `uuid` | PK; FK → `signals` (tenant-local, `ON DELETE CASCADE`) |
| `body` | `text` | ≤ 200 KB at capture (app-enforced) |
| `fetched_at` | `timestamptz` | |

#### `signal_participants`
Who was on it. **PII** — stored with the signal, returned with it (they are
needed to triage), indexed by email for data-subject requests, and never put
in an event or an audit row. `person_id` is reserved for the `people` table.

| column | type | notes |
|---|---|---|
| `tenant_id`, `signal_id`, `idx` | `uuid`, `uuid`, `integer` | PK; FK → `signals` (tenant-local, `ON DELETE CASCADE`) |
| `name` | `text` | a participant needs a name or an email |
| `email` | `text` NULL | lower-cased, trimmed |
| `role` | `text` | `from \| to \| cc \| attendee \| speaker` (CHECK), default `attendee` |
| `person_id` | `uuid` NULL | no FK until `people` exists |
| index | `signal_participants_email_idx (tenant_id, email) WHERE email IS NOT NULL` | "every signal this address appears on" |

#### `work_item_signals` (origins)
Which signals a work item derives from: `POST/GET/DELETE
/api/v1/tasks/{id}/origins`. Attaching an `inbox`/`snoozed` signal moves it to
`attached`; attaching twice is idempotent.

| column | type | notes |
|---|---|---|
| `tenant_id`, `task_id`, `signal_id` | `uuid` | PK |
| `origin_snapshot` | `jsonb` | `{title, kind, occurredAt, bodyRef}` copied from the signal at attach time |
| `created_at` | `timestamptz` | |
| FK | `(tenant_id, task_id)` → `tasks(tenant_id, id)` | `ON DELETE CASCADE` — an origin without its work item means nothing |
| index | `work_item_signals_signal_idx (tenant_id, signal_id)` | "which work items came from this signal" |

**The origin trade-off.** The join row must outlive the signal: a signal can be
deleted (retention, a data-subject request, a mistaken paste) while the work
item it produced lives on, and that work item should still say what it came
from. `ON DELETE SET NULL` is impossible on a PK column, and `ON DELETE
CASCADE` would erase the row — and its snapshot — the moment the signal went,
defeating the purpose. So the signal side deliberately has **no foreign key**
(like `tasks.created_by`, a reference that may outlive its target) and
`origin_snapshot` carries the provenance. A live signal is the source of truth;
a purged one leaves the snapshot behind. The app checks the signal exists in
the tenant at attach time (400 on `signalId` otherwise), and `tenant_id` in the
PK keeps the row under RLS. The memory adapter mirrors every cascade here by
hand (task delete → origins and outputs; source delete → signals; project
delete → `project_hint` nulled; signal delete → body only).

### `outputs` (migration `0015`)
What a work item produced — a link, a document, a PR, an email sent, a
decision, a file, a note. The other end of provenance from origins. Belongs to
exactly one work item and goes with it; created and deleted, never edited, so
like `audit_log` there is no `version`, `updated_at` or `set_updated_at`
trigger. `POST/GET/DELETE /api/v1/tasks/{id}/outputs`.

| column | type | notes |
|---|---|---|
| `tenant_id` | `uuid` | FK → `tenants(id)` `ON DELETE CASCADE` |
| `id` | `uuid` | |
| `task_id` | `uuid` | NOT NULL; FK → `tasks` (tenant-local, `ON DELETE CASCADE`) |
| `kind` | `text` | `link \| doc \| pr \| email \| decision \| file \| note` (CHECK) |
| `title` | `text` | |
| `url` | `text` | `''` when none |
| `detail` | `text` | ≤ 4000 chars (app-enforced) |
| `created_by` | `uuid` NULL | attribution only, no FK |
| `created_at` | `timestamptz` | |
| PK | `(tenant_id, id)` | |
| index | `outputs_task_idx (tenant_id, task_id)` | a work item's outputs |

### PARA columns (migration `0016`)

PARA (Projects · Areas · Resources · Archives) adds no table:

| where | column | notes |
|---|---|---|
| `projects` | `outcome text NOT NULL DEFAULT ''` | why the project exists / what finishing looks like; its work items inherit it as their "why" (≤ 16 KiB, app-enforced) |
| `projects` | `archived_at` (from `0002`) | now read and written: `PATCH {archived: bool}`. Archived projects stay in bootstrap; the web app keeps them — and their open items — off every active surface |
| `clients` | `kind` CHECK widened to `client \| internal \| area` | an **area** is a standing responsibility; a client is one flavour of it |
| `clients` | `standard text NOT NULL DEFAULT ''` | what the area is held to, beside `expected_touch_days` (how often) |

Resources are derived (the links on a project's work items), not stored.
A work item's why · when · where is resolved client-side (`actionContext` in
`src/state/selectors.ts`): why = requirement → ask.why → project outcome →
area standard; when = `scheduled_at` / `deadline` / `ask_by`; where = `place`
(context) + first link (tool).

**Events on the signal surface** ride the generic envelope: `source.*`
(body: the source), `signal.*` (body: `SignalMeta` = id, version, kind, title,
occurredAt, stage — **never** excerpt, body or participants; the parity suite
panics if one leaks), `origin.attached` / `origin.detached` (`entityId` = the
task; body: the origin row / `{taskId, signalId}`), `output.*` (body: the
output). Cascaded rows (a source's signals, a task's outputs and origins, a
project's `project_hint`s) emit nothing, on both adapters.

**Audit.** `POST /signals` → `signal.capture` (kind, text length, participant
count), `DELETE /signals/{id}` → `signal.delete`, `GET /signals/{id}/body` →
`signal.body.read` (body length). Never the content.

### `outbox`
Transactional realtime + bounded replay log. **Not** RLS-restricted: a relay
reads across tenants (access controlled at the role level); rows still carry
`tenant_id` for routing and purge.

| column | type | notes |
|---|---|---|
| `id` | `uuid` PK | |
| `tenant_id` | `uuid` | routing / purge |
| `type` | `text` | `task.created`, `task.updated`, … |
| `entity_type` | `text` | generic envelope: `task \| project \| client \| …` (migration `0010`) |
| `entity_id` | `uuid` | the changed row (covers deletes) |
| `actor_id` | `uuid` NULL | who made the change |
| `payload` | `jsonb` | the full `domain.Event` — **metadata only**, see below |
| `created_at`, `published_at` | `timestamptz` | `published_at` NULL = not yet pushed by an external relay (reserved; the LISTEN/NOTIFY path never sets it) |
| index | `outbox_unpublished_idx (created_at) WHERE published_at IS NULL` | relay scan |
| index | `outbox_created_idx (created_at)` | retention prune (migration `0010`) |
| index | `outbox_tenant_idx (tenant_id)` | tenant purge (migration `0010`) |

An `AFTER INSERT` trigger calls `pg_notify('cadence_events', <row id>)`; the
listener fetches the payload by id, so NOTIFY's 8 KB limit never applies.

**Retention.** The outbox is not append-forever. The server prunes rows whose
`created_at` is older than `CADENCE_OUTBOX_RETENTION_DAYS` (default 7) on an
hourly ticker. Pruning is by age, not `published_at`: the only consumer is the
listener, which reads a row at commit and deliberately does not mark it
published (with N instances "published" would mean nothing, and a future relay
polling `published_at IS NULL` would then miss it). A client offline longer than
the window re-bootstraps rather than replaying.

**No bodies or PII in event payloads.** Events fan out to every subscriber in
the tenant and sit in the outbox for the retention window, so `payload` (and
`domain.Event.Entity`) carries **metadata only** — id, version, kind,
`occurredAt`, title. Signal bodies (meeting notes, pasted email) and participant
PII never go in an event; a subscriber that needs them fetches through the
tenant-fenced API. Both adapters' `emitEntity` helpers document the rule at the
call site.

## Tenant purge

`store.Store.PurgeTenant(tenantID)` hard-deletes a tenant across **every**
table — `audit_log` (under `app.purging = '1'`, the append-only trigger's only
escape hatch), the signal surface leaves-first (`outputs`,
`work_item_signals`, `signal_participants`, `signal_bodies`, `signals`,
`sources`), `requirements`, `tasks`, `project_members`, `projects`,
`clients`, `users`, then the
non-RLS auth tables (`api_tokens`, `sessions` by `tenant_id`; `login_tokens` by
the tenant's emails; `accounts`), the tenant's `outbox` rows, and finally the
`tenants` row. Every statement carries an explicit tenant predicate, so the
purge is exactly as scoped under a superuser as under the RLS role. The
cross-adapter suite (`storetest.AssertTenantPurge`) enumerates the tables by
hand in `storetest.PurgeTables` and raw-counts each one to zero after a purge
while proving a second tenant is untouched — **add every new tenant-scoped
table to that list**, and to both adapters' `PurgeTenant`.

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
- **Native enums** (`task_kind`, `task_status`) give integrity and compact storage
  — but note the migration runner wraps each file in a transaction, and
  `ALTER TYPE … ADD VALUE` cannot run inside one. New enumerations (e.g.
  `clients.tier`/`kind`) therefore use `text + CHECK`, and the existing enums are
  extended by adding a `text + CHECK` column alongside rather than altering the type.
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
