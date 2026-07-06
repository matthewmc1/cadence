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
  weeks/months**, and a backlog spanning **every project** that Cadence places
  into the slot that fits each task's energy. Prioritisation runs on the full
  **Eisenhower** matrix — **important** work (deep, long-term) outranks
  merely-**urgent** work, and the energy **peak is a defended lane** reserved for
  important/deep tasks so loud-but-shallow work can't evict it. Schedule anything
  on **any date** — and **repeats** (daily / weekdays / weekly / monthly) expand
  across every week and month. **"Plan my week with AI"** schedules the whole
  backlog with an on-device LLM (see
  [On-device AI scheduling](#on-device-ai-scheduling)).
- **Board** — a project kanban (Backlog · This week · In focus · Done) that
  remembers where work got done. Create and switch between projects inline.
- **Insights** — computed from your **real completions**: peak window, the dip,
  where you work, and which days you ship. This isn't just a report — the peak
  and dip it measures are the **same windows scheduling places work into**, so
  Cadence learns *when* you do your best thinking and plans around it.

**The task editor** (New task, or click any task on Plan/Board/Today) edits
every field — type, effort, **date + time**, place, project, deadline,
**repeat**, **importance & urgency**, notes, **subtasks, links, and who it's
shared with**. Cadence pre-infers a sensible shape and best slot, all of it
editable.

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
│   ├── ai/                     # on-device LLM (WebGPU + Ollama) + scheduler
│   ├── components/, views/     # the design system and the views
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
├── mcp/                        # MCP server — AI access to tasks/projects/outcomes
└── docs/                       # ARCHITECTURE · SCHEMA · MIGRATIONS
```

## Quickstart

**Prereqs:** Node 18+, Go 1.22+, Docker (for the database).

```bash
./dev.sh
```

One script is the whole local stack: it brings up Postgres in Docker (a **named
volume**, so your data survives restarts), applies the migrations, builds and
runs the API on `http://localhost:8088`, and starts the web app on
`http://localhost:5173`. Stop with `Ctrl-C` — the database keeps running (and
keeps your data), so the next `./dev.sh` is instant. (`make dev` is equivalent.)

Open http://localhost:5173. You'll see a **sign-in** screen — enter your email.
With no mail provider configured the magic link is shown right there (dev mode),
so you can click straight through; wire up Resend (below) to receive it by email
instead. Signing in creates your personal workspace. Open it in two tabs to
watch changes sync live.

> **Your data is durable by default.** `./dev.sh` runs the Postgres backend
> backed by the `cadence_pgdata` Docker volume, so nothing is lost on restart.
> To run the throwaway in-memory backend instead (no Docker, wiped on exit):
> `CADENCE_BACKEND=memory ./dev.sh`.
>
> The web app talks to `http://localhost:8088` by default (override with
> `VITE_API_URL`). The magic link automatically points back to whichever origin
> served the app (`5173` for `npm run dev`, `4173` for `npm run preview`), as
> long as it's in `CADENCE_WEB_ORIGINS`.

### Email delivery (Resend)

Magic-link email is delivered through [Resend](https://resend.com). Set an API
key and delivery goes live; leave it unset and links are logged/returned for dev.

```bash
cp .env.example .env
# edit .env → RESEND_API_KEY=re_...   (optionally MAIL_FROM="Cadence <you@yourdomain>")
./dev.sh    # reads .env automatically
```

## Run Postgres yourself

`./dev.sh` manages the database for you via [`docker-compose.yml`](./docker-compose.yml)
(port `55432` by default, override with `CADENCE_DB_PORT`). To point at an
existing Postgres instead:

```bash
cd server
CADENCE_BACKEND=postgres \
DATABASE_URL='postgres://cadence:cadence@localhost:55432/cadence?sslmode=disable' \
go run ./cmd/cadence-server
```

On boot the server applies the embedded migrations (there's no demo data — sign
in to create your workspace). Realtime flows through the transactional outbox and
Postgres `LISTEN/NOTIFY`, so it fans out across multiple instances.

> **Production note:** connect as a **non-superuser** role (superusers bypass
> Row-Level Security). Create it with
> [`server/scripts/app-role.sql`](./server/scripts/app-role.sql) and point
> `DATABASE_URL` at `cadence_app`. Tenant isolation is **defense-in-depth** —
> explicit `tenant_id` predicates on every query *and* RLS — so a row never
> leaks even if a query is run as a superuser. See
> [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md#multi-tenancy).
>
> For production also set `CADENCE_DEV_AUTH=false` and serve over HTTPS with
> `CADENCE_COOKIE_SECURE=true`.

## Configuration

| env | default | meaning |
|---|---|---|
| `CADENCE_ADDR` | `:8088` | listen address |
| `CADENCE_BACKEND` | `memory` | `memory` or `postgres` |
| `DATABASE_URL` | — | required for `postgres` |
| `CADENCE_AUTO_MIGRATE` | `true` | apply migrations on boot |
| `CADENCE_WEB_URL` | `http://localhost:4173` | where the SPA lives (used in magic links) |
| `CADENCE_WEB_ORIGINS` | `localhost:*,127.0.0.1:*` | CORS + WebSocket origin allowlist |
| `RESEND_API_KEY` | — | [Resend](https://resend.com) key; unset ⇒ links logged, not emailed |
| `MAIL_FROM` | `Cadence <onboarding@resend.dev>` | magic-link sender address |
| `CADENCE_DEV_AUTH` | `true` | return/log the magic link when no live mailer is configured |
| `CADENCE_COOKIE_SECURE` | `false` | set `true` behind HTTPS |
| `CADENCE_DB_PORT` | `55432` | host port the Compose Postgres binds |
| `VITE_API_URL` (web) | `http://localhost:8088` | API base URL |

## Auth (magic link)

Cadence has native, passwordless auth — **we store only your email**:

1. Enter your email → `POST /auth/request` issues a one-time link.
2. The link (`/auth?token=…`) is emailed via **Resend** when `RESEND_API_KEY` is
   set; with no provider (dev) it's logged and returned so you can click it
   immediately. The endpoint is rate-limited per IP and per email (magic-link
   email is a bombing / cost-amplification target).
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
| `GET/POST` | `/api/v1/auth/tokens` | list / mint personal access tokens (Bearer) |
| `DELETE` | `/api/v1/auth/tokens/{id}` | revoke a token |
| `GET` | `/api/v1/bootstrap` | one-shot hydrate (tenant, user, projects, tasks) |
| `GET/POST` | `/api/v1/tasks` | list / create |
| `GET/PATCH/DELETE` | `/api/v1/tasks/{id}` | read / update / delete |
| `GET/POST` | `/api/v1/projects` | list / create |
| `PATCH/DELETE` | `/api/v1/projects/{id}` | update / delete |
| `GET` | `/api/v1/realtime` | WebSocket event stream |

- Identity is the session **cookie** (browser) **or** an `Authorization: Bearer
  cdnc_…` personal access token (programmatic clients like the MCP server).
- `PATCH` is a partial update; send `If-Match: <version>` for optimistic
  concurrency (stale → `409`).
- WebSocket frames are `task.*` / `project.*` events; clients apply them
  idempotently by `id`+`version`.

## AI access (MCP server)

[`mcp/`](./mcp) is a [Model Context Protocol](https://modelcontextprotocol.io)
server that connects an AI assistant (Claude Desktop/Code, …) to your Cadence
workspace, so it can help you **focus** and reason about how tasks ladder up to
**projects and outcomes**. Tools include `whats_next` (the focus view),
`list_tasks`, `create_task`/`update_task`/`complete_task`, `list_projects`, and
`project_status`, plus `daily-focus` / `weekly-review` prompts.

Mint a token in the app (avatar → **API tokens** — the dialog hands you a
ready-to-paste config), then see [`mcp/README.md`](./mcp/README.md) to wire it
up. Auth is the personal access token above; everything the assistant does shows
up live in the web app.

## On-device AI scheduling

The Plan view's **"Plan my week with AI"** hands your whole backlog to a small
LLM that places each task around your energy — deep/important work in your
**learned peak**, light/admin in the post-lunch dip, **important before merely
urgent**, deadlines respected, personal tasks on the weekend, spread out and
collision-free. The peak/dip windows are **learned from your own completion
history** (falling back to a sensible default until there's enough signal), and
a deterministic reconcile step **guarantees the peak stays reserved for deep /
important work**. **Everything runs on your machine — no task data leaves the
device.** Two interchangeable backends (pattern borrowed from `~/loam`):

- **WebGPU (primary)** — a model runs fully in-browser via
  [`@mlc-ai/web-llm`](https://github.com/mlc-ai/web-llm) (Gemma 2 2B / Gemma 3
  1B). Zero install; lazy-loaded so the app bundle stays small.
- **Ollama (fallback)** — a local [Ollama](https://ollama.com) server for bigger
  / newer models (Gemma 3 4B, Qwen 2.5 7B). Cadence detects what you have
  installed; pick a model from the picker under the button.

Pick the backend/model from the **picker beside the button**; the choice is
saved locally. The model's output is reconciled into a guaranteed-valid,
in-range, collision-free schedule, so even a 1–2B model can't produce a broken
week. A **"quick arrange"** heuristic (no model) is always available as an
instant fallback.

> The AI code lives in [`src/ai/`](./src/ai) (`config.ts`, `llm.ts`,
> `llm.worker.ts`, `scheduler.ts`). WebGPU needs a Chromium-based browser or
> Safari Technology Preview; otherwise the picker defaults to Ollama.

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

## License

[MIT](./LICENSE) © Matthew McGibbon
