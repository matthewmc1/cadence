# Cadence

**An energy-aware task manager that arranges your work around how your day
actually flows.** Cadence learns when you do your best thinking and places deep
work in your peak windows, light work in the dips, and protects the rest.

Built from the `Cadence.dc.html` design as a full-stack product: a polished
React front end backed by a Go API with realtime sync, multi-tenant Postgres,
and zero-downtime migrations.

## What it does

- **Today** — your day along a live energy curve, derived from the real clock,
  with a protected deep-focus window.
- **Plan** — a real calendar with **Week and Month** views, **navigation across
  weeks/months**, and a backlog Cadence places into the slot that fits each
  task's energy. Schedule anything on **any date** — and **repeats**
  (daily / weekdays / weekly / monthly) expand across every week and month.
- **Board** — a project kanban (Backlog · This week · In focus · Done) that
  remembers where work got done. Create and switch between projects inline.
- **Insights** — computed from your **real completions**: peak window, the dip,
  where you work, and which days you ship.

**The task editor** (New task, or click any task on Plan/Board/Today) edits
every field — type, effort, **date + time**, place, project, deadline,
**repeat**, urgency, notes, **subtasks, links, and who it's shared with**.
Cadence pre-infers a sensible shape and best slot, all of it editable.

Sign in is **passwordless** (magic link); each account gets its own private,
multi-tenant workspace. Everything is **live**: create/edit/delete persist
through the API and stream to your other open sessions over WebSockets (watch
the **Live** indicator in the header).

## Layout

```
.
├── index.html, src/            # React + TypeScript + Vite web app
│   ├── api/                    # typed API client + realtime (WS) client
│   ├── state/                  # store (optimistic CRUD + realtime), selectors
│   ├── components/, views/     # the design system and the five views
│   └── lib/                    # energy model + curve/heatmap math
├── server/                     # Go API + realtime backend
│   ├── cmd/cadence-server/     # entrypoint
│   ├── internal/
│   │   ├── domain/             # Task/Project/User model, auth, ids (UUIDv7)
│   │   ├── store/              # Store interface + memory & postgres adapters
│   │   ├── httpapi/            # REST + WebSocket + magic-link auth, middleware
│   │   └── config/             # env config
│   ├── migrations/             # SQL migrations (RLS, outbox, auth, indexes)
│   └── scripts/                # app-role.sql (non-superuser RLS role)
└── docs/                       # ARCHITECTURE · SCHEMA · MIGRATIONS
```

## Quickstart

**Prereqs:** Node 18+, Go 1.22+. (Postgres optional — see below.)

```bash
# 1) backend (in-memory; runs anywhere, no external services)
cd server
go run ./cmd/cadence-server          # serves http://localhost:8088

# 2) web app (in another terminal, from the repo root)
npm install
npm run dev                          # serves http://localhost:5173
```

Open http://localhost:5173. You'll see a **sign-in** screen — enter any email
and click **"Open magic link →"** (in dev the link is shown right there; no
email is sent). That creates your personal workspace and you're in. Open it in
two tabs to watch changes sync live.

> The web app talks to `http://localhost:8088` by default (override with
> `VITE_API_URL`). The magic link automatically points back to whichever origin
> served the app (`5173` for `npm run dev`, `4173` for `npm run preview`), as
> long as it's in `CADENCE_WEB_ORIGINS`.

## Run on Postgres (production mode)

```bash
docker run -d --name cadence-pg -e POSTGRES_PASSWORD=cadence -e POSTGRES_DB=cadence \
  -p 5433:5432 postgres:16-alpine

cd server
CADENCE_BACKEND=postgres \
DATABASE_URL='postgres://postgres:cadence@localhost:5433/cadence?sslmode=disable' \
go run ./cmd/cadence-server
```

On boot the server applies the embedded migrations (there's no demo data —
sign in to create your workspace). Realtime now flows through the transactional
outbox and Postgres `LISTEN/NOTIFY`, so it fans out across multiple instances.

> **Production note:** connect as a **non-superuser** role (superusers bypass
> Row-Level Security). Create it with
> [`server/scripts/app-role.sql`](./server/scripts/app-role.sql) and point
> `DATABASE_URL` at `cadence_app`. See
> [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md#multi-tenancy).
>
> **Auth is real (session-based magic links).** The only stub is email
> *delivery* — no SMTP/provider is wired, so in dev the link is logged and
> returned (`CADENCE_DEV_AUTH`). For production, send `link` by email, set
> `CADENCE_DEV_AUTH=false`, and serve over HTTPS with `CADENCE_COOKIE_SECURE=true`.

## Configuration

| env | default | meaning |
|---|---|---|
| `CADENCE_ADDR` | `:8088` | listen address |
| `CADENCE_BACKEND` | `memory` | `memory` or `postgres` |
| `DATABASE_URL` | — | required for `postgres` |
| `CADENCE_AUTO_MIGRATE` | `true` | apply migrations on boot |
| `CADENCE_WEB_URL` | `http://localhost:4173` | where the SPA lives (used in magic links) |
| `CADENCE_WEB_ORIGINS` | `localhost:*,127.0.0.1:*` | CORS + WebSocket origin allowlist |
| `CADENCE_DEV_AUTH` | `true` | return/log the magic link (no email transport wired) |
| `CADENCE_COOKIE_SECURE` | `false` | set `true` behind HTTPS |
| `VITE_API_URL` (web) | `http://localhost:8088` | API base URL |

## Auth (magic link)

Cadence has native, passwordless auth — **we store only your email**:

1. Enter your email → `POST /auth/request` issues a one-time link.
2. The link (`/auth?token=…`) is emailed in production; in dev (`CADENCE_DEV_AUTH=true`)
   it's logged and returned so you can click it immediately.
3. Opening it → `POST /auth/verify` creates your **personal tenant** on first
   sign-in, starts a session, and sets an **HttpOnly cookie**.
4. Every API/WS call is then scoped to your tenant by that session — no
   spoofable headers. Name + avatar are derived from the email, never stored.

## API

Identity comes from the session **cookie** (set at `/auth/verify`); protected
routes return `401` without one.

| method | path | |
|---|---|---|
| `GET` | `/api/v1/health` | liveness (public) |
| `POST` | `/api/v1/auth/request` | issue a magic link (public) |
| `POST` | `/api/v1/auth/verify` | consume link → session cookie (public) |
| `GET` | `/api/v1/auth/me` | current user, or 401 |
| `POST` | `/api/v1/auth/logout` | end session |
| `GET` | `/api/v1/bootstrap` | one-shot hydrate (tenant, user, projects, tasks) |
| `GET/POST` | `/api/v1/tasks` | list / create |
| `GET/PATCH/DELETE` | `/api/v1/tasks/{id}` | read / update / delete |
| `GET/POST` | `/api/v1/projects` | list / create |
| `PATCH/DELETE` | `/api/v1/projects/{id}` | update / delete |
| `GET` | `/api/v1/realtime` | WebSocket event stream |

- `PATCH` is a partial update; send `If-Match: <version>` for optimistic
  concurrency (stale → `409`).
- WebSocket frames are `task.*` / `project.*` events; clients apply them
  idempotently by `id`+`version`.

## Build & check

```bash
npm run build                 # type-check + bundle the web app (repo root)
cd server && go build ./... && go vet ./...   # build + vet the backend
gofmt -l server                # should print nothing
```

The backend has no runtime dependencies beyond Go for the in-memory mode; the
Postgres mode pulls `pgx` and `coder/websocket` (already in `go.mod`).

## Tech

- **Web:** React 18, TypeScript, Vite. Newsreader + Hanken Grotesk; a warm
  paper-and-ink design system driven by an energy model (Catmull-Rom curves,
  heatmap, best-slot scheduling).
- **API:** Go, `net/http` (1.22 routing), `coder/websocket`, `pgx/v5`.
- **Data:** PostgreSQL — shared-schema multi-tenancy with Row-Level Security,
  a transactional outbox, partition-ready composite keys.

## Documentation

- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) — distributed design, realtime,
  multi-tenancy, scaling, failure modes.
- [docs/SCHEMA.md](./docs/SCHEMA.md) — tables, RLS, indexes, partitioning.
- [docs/MIGRATIONS.md](./docs/MIGRATIONS.md) — zero-downtime expand/contract
  playbook.
