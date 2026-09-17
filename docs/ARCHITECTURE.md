# Cadence — Architecture

Cadence is a private, self-hosted work hub. Signals — meetings, email, notes,
docs — are triaged into work items that carry their context, and finished with
proof. It still schedules around the rhythm it learns from your completions;
that is a feature it has, not the headline. This document describes how the
running system is put together and how it scales.

```
                 ┌──────────────────────────────────────────────┐
                 │                   Browsers                    │
                 │   React SPA (Vite)  ·  optimistic UI  ·  WS    │
                 └───────▲───────────────────────────▲───────────┘
                  REST   │ (CRUD)            WebSocket │ (live events)
                 ┌───────┴───────────────────────────┴───────────┐
                 │            cadence-server  (Go, stateless)     │
                 │   httpapi · store.Store · realtime fan-out     │
                 │   N interchangeable instances behind an LB     │
                 └───────▲───────────────────────────▲───────────┘
            SQL (RLS tx) │                LISTEN/NOTIFY │ cadence_events
                 ┌───────┴───────────────────────────┴───────────┐
                 │                  PostgreSQL                     │
                 │  tenants · users · projects · tasks · outbox   │
                 │  Row-Level Security · transactional outbox      │
                 └────────────────────────────────────────────────┘
```

## Components

| Layer | Tech | Responsibility |
|-------|------|----------------|
| Web | React + TypeScript + Vite | The views, optimistic CRUD, realtime sync |
| API | Go (`net/http`, `coder/websocket`) | Tenant-scoped REST + WebSocket over a `Store` |
| Data | PostgreSQL (`pgx`) | Source of truth, isolation (RLS), durable events (outbox) |

The server is written against a single interface, [`store.Store`](../server/internal/store/store.go).
Two adapters implement it:

- **`memory`** — goroutine-safe in-memory store + in-process broker. The default;
  runs anywhere with zero dependencies. Used for local dev and the demo.
- **`postgres`** — `pgx` pool, RLS-scoped transactions, a transactional outbox,
  and LISTEN/NOTIFY fan-out. The production target.

Because both satisfy the same contract and route writes through the same
[`ApplyTaskPatch`](../server/internal/store/patch.go), behaviour is identical
regardless of backend. You develop against memory and deploy on Postgres.
That parity is executable: the [CRUD parity suite](../server/internal/store/storetest/parity.go)
walks every entity through create → get → list → paged list → update (with
and without a version token) → invalid patches → delete → not-found on both
adapters, demanding identical errors, version bumps, a strictly advancing
`updatedAt`, `[]`-not-`null` JSON, and exactly one realtime event per write.
It is table-driven — a new entity is one appended case.

## The write path

A mutation (`POST /tasks`, `PATCH /tasks/{id}`, …) is handled in one
RLS-scoped transaction:

1. Resolve identity from the **session cookie** (magic-link sign-in) or a
   `Bearer cdnc_…` personal access token — never a client-supplied header — into
   `tenantId`, `actorId` in the request context.
2. `BEGIN` and `SELECT set_config('app.tenant_id', $tenant, true)` — every
   subsequent statement is now fenced by Row-Level Security to that tenant.
3. Apply the change (`INSERT`/`UPDATE`/`DELETE`), bumping the row `version`.
4. **In the same transaction**, append the change to the `outbox` table.
5. `COMMIT`. An `AFTER INSERT` trigger on `outbox` fires `pg_notify('cadence_events', payload)`
   — delivered to listeners only on commit, so the event is atomic with the change.

There is no dual-write window: either the row change and its event both commit,
or neither does.

## Realtime fan-out (works across instances)

Each server instance holds one dedicated connection that `LISTEN`s on
`cadence_events`. On a notification it decodes the event and fans it out to the
WebSocket clients subscribed to that tenant (an in-process, per-tenant
[`fanout`](../server/internal/store/postgres/broker.go)).

Because **every** instance listens, a write served by instance A reaches a
WebSocket client connected to instance B — no sticky sessions, no shared
in-memory bus, no message broker required to get started.

```
client→A: PATCH /tasks/42 ──► A: INSERT outbox ──► COMMIT ──► NOTIFY
                                                                 │
              ┌──────────────────────────────────────┬──────────┘
       A.listener                              B.listener
              │                                        │
       A.fanout → A's WS clients          B.fanout → B's WS clients
```

The memory adapter collapses this to a direct in-process broker — same API,
same client behaviour.

### Delivery semantics & recovery

`pg_notify` is best-effort and not durable for a disconnected listener, so the
`outbox` row is the source of truth:

- **At-least-once:** a relay/worker can poll `outbox WHERE published_at IS NULL`
  (covered by `outbox_unpublished_idx`) to push to external sinks, marking rows
  published. Re-delivery is safe because the client applies events idempotently.
- **Missed-while-offline:** a reconnecting client re-runs `GET /bootstrap` (a
  full, consistent snapshot) or, with the outbox cursor exposed, replays events
  after its last-seen id.
- **Idempotent apply:** the web client upserts by `id` and only accepts an event
  whose `version` is `>=` the local one, so self-echoes and duplicates are
  harmless no-ops and out-of-order delivery can't regress state.

### Event envelope

[`domain.Event`](../server/internal/domain/models.go) is a generic envelope:
`entityType` (`task | project | client | …`) plus `entity` (JSON body), with
`entityId` always set so deletes are addressable. The three original types also
populate the typed `task` / `project` / `client` fields, which is what the web
client reads today; entity types added later use `entityType` + `entity` only,
through the adapters' one-line `emitEntity` helper.

**Payloads are metadata only.** An event reaches every subscriber in the tenant
and is retained in the outbox, so it may carry id, version, kind, `occurredAt`,
title — never a signal body (meeting notes, pasted email) or participant PII.
Consumers that need the body fetch it through the tenant-fenced API.

### Outbox retention & tenant purge

The outbox is bounded: the postgres adapter prunes rows older than
`CADENCE_OUTBOX_RETENTION_DAYS` (default 7) on an hourly ticker started in
`Open` and stopped in `Close`. Retention is by `created_at`, not
`published_at` — the LISTEN/NOTIFY listener reads a row at commit and does not
mark it published (see [`outbox.go`](../server/internal/store/postgres/outbox.go)
for why it must not), so `published_at` stays reserved for an external relay. A
client offline longer than the window re-bootstraps.

`Store.PurgeTenant` hard-deletes a tenant across every table — data rows, the
tenant's auth material (sessions, API tokens, login tokens, account), its outbox
rows, and the tenant row — with an explicit tenant predicate on every statement.
The cross-adapter [purge suite](../server/internal/store/storetest/purge.go)
raw-counts every table to zero for the purged tenant and proves a neighbouring
tenant is untouched; both adapters run it.

## Consistency & concurrency

- Every row carries a monotonically increasing `version`.
- Clients may send `If-Match: <version>` for **optimistic concurrency**; a stale
  token returns `409 Conflict` (verified in tests). Without it, writes are
  last-write-wins.
- The UI updates **optimistically** and rolls back on API error, so it feels
  instant while staying correct.

## Reads: a bounded bootstrap and keyset paging

`GET /bootstrap` is deliberately **bounded**. It hydrates the small, hot working
set — tenant, user, clients, projects, tasks — in one consistent snapshot, and
nothing that can grow without bound is ever added to it. Signals (captured
meeting notes, pasted email, links) will never be in the bootstrap; they, and
any later unbounded entity, are reachable only through a paged list.

Paged reads share one contract, [`store/paging.go`](../server/internal/store/paging.go):

- **Keyset, never OFFSET.** A page is "the next N rows after this position".
  The position is `(time, id)` — the entity's own time column (`created_at`
  for tasks, `occurred_at` for signals) with the id as tie-breaker. Ids are
  UUIDv7, so the pair is a total, stable sort key: no row is skipped or
  repeated when rows are inserted between page fetches, and the SQL row-value
  comparison `(time_col, id) < ($at, $id)` walks a `(tenant_id, time_col DESC,
  id DESC)` index straight to the page.
- **Newest first.** `ORDER BY time_col DESC, id DESC`, so a feed renders as it
  arrives and older pages are fetched on demand.
- **Opaque cursor.** `PageResult.NextCursor` encodes the last row's key
  (base64url of Unix-nanos + id); `""` means last page. A malformed cursor is a
  `400` (`ValidationError` on `cursor`), never a `500`.
- **Bounded pages.** `Page.Limit` defaults to 100 and is clamped to 1000 — a
  client cannot turn a list endpoint into a full-table read.
- **`items` is always `[]`**, never `null`, including for an empty page.
- **Same tenant fence, same filter** as the unpaged list it sits beside.

Adapters fetch `limit+1` rows and let `store.Paginate` trim the page and set
the cursor — the spare row is the "has more" signal, with no `COUNT` and no
second query. The helpers (`DecodeCursor`, `Cursor.Admits`, `SortNewestFirst`,
`Paginate`, and the Postgres `keysetWhere`) are entity-agnostic;
`ListTasksPaged` is the first consumer and `ListSignals` reuses them unchanged.

## Signals

A **signal** is a captured thing — meeting notes, a pasted email, a link, a
chat message, a document ([`domain.Signal`](../server/internal/domain/signal.go),
migration `0014`). It is **never a task**: a work item *derives* from one or
more signals (its origins, `work_item_signals`) and leaves outputs behind
(`outputs`, migration `0015`), so provenance runs in both directions —
"where did this come from" and "what came of it". The rules that shape the
surface:

- **Never in Bootstrap.** Signals grow without bound, so they are reachable
  only through the keyset-paged `GET /api/v1/signals` (newest first by
  `(occurred_at, id)`, the same contract as above) and a `count` endpoint for
  the inbox badge.
- **Immutable.** After capture only the *disposition* changes — `stage`
  (`inbox → snoozed | promoted | attached | dismissed`), `snoozedUntil`,
  `projectHint`, `extracted`, `retentionUntil`. `store.ApplySignalPatch`
  refuses every other key on both adapters, and a Postgres `BEFORE UPDATE`
  trigger (an allow-list over the row) is the backstop below the app.
- **Bodies are separate.** The signal row carries a 2000-character excerpt;
  the full text lives in `signal_bodies` and is fetched by
  `GET /api/v1/signals/{id}/body` only — never joined into a list, and every
  read is an audit row. Participants (PII) return with the signal but are
  indexed for data-subject requests and never leave in an event.
- **Events are metadata only.** `signal.*` events carry `SignalMeta` (id,
  version, kind, title, occurredAt, stage); the parity suite panics if an
  excerpt, body or participant ever appears in one.
- **Origins outlive signals.** The join row snapshots the signal's
  title/kind/occurredAt/bodyRef at attach time and has no FK on the signal
  side, so a signal deleted for retention or a data-subject request still
  leaves the work item knowing what it came from (see the `0014` header for
  the trade-off).
- **Retention is per source, and it is real.** A source's `retentionDays`
  stamps `retentionUntil` on each capture, and an hourly sweep on both
  adapters (`PruneSignals`) deletes every signal past it — body and
  participants with it, origins keeping their snapshot. Connectors and their
  credentials are still a later slice; today the one source every tenant has
  is the implicit `manual` paste box, created lazily — and the one source
  that cannot be deleted, since deleting a source cascades every signal filed
  under it.
- **Every deletion leaves a trace.** A single delete is `signal.delete`;
  arming the sweep through `retentionUntil` is `signal.retention`; the sweep
  itself writes one `signal.prune` row per tenant per pass with the count;
  deleting a source writes `source.delete` with the number of signals the
  cascade took. Counts and ids only — never a title, a body or a participant.

### Extraction

`extracted` is filled by the [`extract`](../server/internal/extract) package:
dates, deadlines, people, links and a suggested title, each with a
confidence, plus `model`/`promptVersion` saying how it was produced. Two
extractors sit behind one interface. **Heuristic** is deterministic regex
work (ISO and natural dates, "by <date>" cues, emails, URLs, capitalised
name pairs; confidence ≤ 0.6) and always runs. **LLM** asks the deployment's
own Ollama (the same server the AI gateway forwards to) with a strict
JSON-format prompt (`facts-v1`, 60 s timeout), validates the answer against
the text — an email, URL or name the model invented is dropped — and merges
it over the heuristic result keeping the higher confidence per fact; any
failure degrades to the heuristic facts, never to an error. Which one runs
follows `CADENCE_AI_POLICY`: under either policy the only provider today is
local Ollama, so it is the LLM when `OLLAMA_URL` is set, else the heuristic.
Extraction runs in the background after every `POST /signals` (a bounded
worker pool, so a paste storm queues rather than fanning out) and on demand
via `POST /signals/{id}/extract`; both store the facts through the
disposition path — `extracted` is a permitted mutable column — overlaying
whatever a client seeded there, and append an `ai.extract` audit row (model,
prompt version, signal id; never the text or the facts). The stored object
never contains the body: only what was pulled out of it.

**A correction is final.** The client can rewrite a fact chip inline; the
correction goes back through `PATCH /signals/{id}` as `corrected: true` on
that one entry, and every later extraction merges *around* it — the corrected
text, and the flag, survive whatever the model now reads (`extract.Merge`,
`keepFromExisting`). Facts nobody has touched are still restated freely, so a
re-run remains worth running.

Both adapters run the same signal walk in the parity suite
([`storetest/signals.go`](../server/internal/store/storetest/signals.go)) and
the isolation suite proves tenant B cannot read A's signal, its body, its
origins or its outputs, nor attach A's signal to B's work item.

## Multi-tenancy

Shared schema, `tenant_id` on every row, enforced by PostgreSQL **Row-Level
Security** — the densest, most operable model and the one with the cheapest
zero-downtime migrations (one schema to migrate, not N).

Defense in depth — **two independent layers**, either of which alone fences a
tenant off:

- **Explicit `tenant_id` predicates in the query layer.** Every tenant-scoped
  read and write carries an explicit `WHERE tenant_id = $1` (and every
  `UPDATE`/`DELETE` an `AND tenant_id = …`), and cross-tenant references are
  rejected outright — a project can't point at another tenant's client, a task
  can't point at another tenant's project (Postgres enforces this with
  tenant-local composite FKs; the memory adapter checks ownership). This holds
  **even if the connection is a superuser**, which silently bypasses RLS.
- **Row-Level Security as the second layer.** `ENABLE` **and** `FORCE ROW LEVEL
  SECURITY` (applies even to the table owner). The app connects as a
  **non-superuser, `NOBYPASSRLS`** role. Isolation **fails closed**: the policy
  is `tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid`, so
  an unset/empty setting selects zero rows instead of erroring or leaking.

Realtime is tenant-scoped too: the per-tenant fanout only delivers a tenant's
events to that tenant's subscribers — one tenant's writes never reach another's
WebSocket.

Verified by an adversarial [cross-adapter isolation suite](../server/internal/store/storetest/isolation.go)
that **both** the memory and Postgres backends run: holding tenant A's exact row
ids, tenant B is denied every read/mutate/delete, sees nothing in list/bootstrap,
can't forge a cross-tenant reference, and receives none of A's realtime events —
while A's own data is proven intact. The Postgres suite runs under the restricted
role against a live database (`TEST_DATABASE_URL`); it caught a real leak when the
app relied on RLS alone and a superuser connection bypassed it — which is why the
explicit predicates exist.

See [SCHEMA.md](./SCHEMA.md) for the policies and [MIGRATIONS.md](./MIGRATIONS.md)
for why this model keeps migrations cheap.

## Scaling

The system is built to scale horizontally without re-architecting:

- **Stateless API.** No session affinity; scale instances behind a load
  balancer. Realtime keeps working because fan-out goes through the database.
- **Connection pooling.** Front Postgres with PgBouncer (transaction pooling);
  the per-transaction `set_config(... true)` is transaction-local and pooler-safe.
- **Read scaling.** Route read-only endpoints (`bootstrap`, lists) to read
  replicas; keep writes + the `LISTEN` connection on the primary.
- **Partition-ready storage.** Composite primary keys lead with `tenant_id` and
  all foreign keys are tenant-local `(tenant_id, …)`, so `tasks` can be `HASH`
  partitioned by `tenant_id` (or large tenants peeled into their own partitions)
  with no application change.
- **Sharding the long tail.** Because the access pattern is always
  tenant-scoped, very large deployments can shard tenants across clusters; the
  `Store` interface is the seam where a routing adapter would live.
- **Realtime at scale.** A single `cadence_events` channel is fine into the
  thousands of writes/sec. Beyond that, shard the channel by tenant hash
  (`cadence_events_<n>`), or move fan-out to logical replication / Debezium / a
  dedicated bus (Kafka/NATS) fed by the same outbox — the outbox makes this swap
  non-breaking.

## Failure modes

| Failure | Behaviour |
|---|---|
| Server instance dies | LB drops it; clients reconnect to another; no state lost (stateless) |
| WebSocket drops | Client reconnects with capped exponential backoff; re-bootstraps to catch up |
| DB primary failover | Pool reconnects; `LISTEN` loop reconnects with backoff; in-flight tx retried by client |
| Slow/abandoned WS client | Per-subscriber buffered channel; non-blocking publish drops to the slow client only; it recovers via re-bootstrap |
| Malformed input | Validated at the edge; `400` with field + message; body capped at 1 MiB |

## Security

- Tenant isolation by RLS (above).
- All input validated; partial `PATCH` semantics centralized and type-checked.
- Request size limits, CORS allowlist, structured errors that don't leak internals.
- Identity is header-based in the demo; the resolution seam
  ([`resolveIdentity`](../server/internal/httpapi/middleware.go)) is where JWT
  verification drops in — the rest of the stack is unchanged.

## Observability

- Structured `slog` request logs with method, path, status, duration.
- `X-Request-ID` propagated/echoed for tracing.
- `GET /api/v1/health` pings the store for liveness/readiness probes.

## Local dev vs production parity

| | Local (memory) | Production (postgres) |
|---|---|---|
| Store | in-memory maps | `pgx` + RLS |
| Realtime | in-process broker | outbox + LISTEN/NOTIFY |
| Migrations | n/a | embedded, auto-applied |
| External deps | none | PostgreSQL |

Same `Store` contract, same HTTP/WS surface, same patch semantics — so what you
build locally behaves the same in production.
