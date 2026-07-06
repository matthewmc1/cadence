# Cadence — Architecture

Cadence is an energy-aware task manager. It learns when you do your best work
and arranges tasks around that rhythm. This document describes how the running
system is put together and how it scales.

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
| Web | React + TypeScript + Vite | The five views, optimistic CRUD, realtime sync |
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

## Consistency & concurrency

- Every row carries a monotonically increasing `version`.
- Clients may send `If-Match: <version>` for **optimistic concurrency**; a stale
  token returns `409 Conflict` (verified in tests). Without it, writes are
  last-write-wins.
- The UI updates **optimistically** and rolls back on API error, so it feels
  instant while staying correct.

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
