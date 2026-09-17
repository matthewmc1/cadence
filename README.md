# Cadence

**A private, self-hosted work hub.** Signals come in — meetings, email, notes,
docs — get triaged into work items that carry the context they came from, and
get finished with proof. Nothing about your work leaves your host.

Scheduling is still here (dates, deadlines, repeats, and energy-aware placement
learned from your own completions) — a feature the work sits on, not the
headline.

Built from the `Cadence.dc.html` design as a full-stack product: a polished
React front end backed by a Go API with realtime sync, multi-tenant Postgres,
and zero-downtime migrations.

## What it does

Three tabs — **Inbox · Work · Insights**.

- **Inbox** — everything captured (the paste box, connectors) lands here as a
  *signal*, never as a task. Each one is triaged with a single key — promote,
  attach, snooze, dismiss, hand over — and every disposition is undoable. It is
  the landing view while it has items.
- **Work** — the home surface, and where work gets **done**: every work item
  across every project, with stage (to do · doing · waiting · done), owner, the
  requirement it serves (or an explicit **unscoped** mark), where it came from,
  and who it is waiting on. Filter, sort, group — or switch to **Board**, the
  selected project's kanban. **When** an item is due or scheduled is a
  lightweight control here (and in the task editor) rather than a calendar you
  go and visit: what is on today sits at the top, and an unscheduled item can be
  dropped onto a day.
- **Insights** — computed from your **real completions**: when you finish work,
  where you work, and which days you ship. The windows it measures are the same
  ones scheduling places work into, so the observation and the placement stay in
  step.

**Plan is parked.** The calendar view was collapsed from the navigation — the
focus is on getting work done rather than arranging a day — but the decision is
reversible: [`src/views/PlanView.tsx`](./src/views/PlanView.tsx) and its
stylesheet are still in the tree, unrouted. Scheduling itself is untouched:
tasks keep `scheduledAt` / `deadline`, **repeats** (daily / weekdays / weekly /
monthly) still expand, prioritisation still runs the full **Eisenhower** matrix
— **important** work (deep, long-term) outranks merely-**urgent** work — and the
energy **peak is still a defended lane** reserved for important/deep tasks.
**"Plan my week with AI"** schedules a whole backlog with an on-device LLM (see
[On-device AI scheduling](#on-device-ai-scheduling)).

**The task editor** (+ New, or click any item on Work) edits
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
│   ├── ai/                     # local LLM (WebGPU in-browser, Ollama via the server) + scheduler
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
│   │   └── web/                # go:embed of the built SPA (make web-embed / Dockerfile)
│   └── scripts/                # app-role.sql (non-superuser RLS role)
├── Dockerfile, docker-compose.yml  # private deployment: app + Postgres
├── scripts/                    # backup.sh / restore.sh (pg_dump / pg_restore + smoke test)
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
so you can click straight through; wire up SMTP (below) to receive it by email
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

### Email delivery (SMTP, or Resend)

Magic-link email goes out over **plain SMTP** to a server you control — your own
Postfix/Mailcow box or any relay — so nothing about your users leaves your
infrastructure. Set `SMTP_HOST` + `MAIL_FROM` and delivery goes live; leave both
unset and links are logged/returned for dev.

```bash
cp .env.example .env
# edit .env →
#   SMTP_HOST=mail.example.com  SMTP_USER=cadence  SMTP_PASS=…
#   MAIL_FROM="Cadence <cadence@example.com>"
./dev.sh    # reads .env automatically
```

Port `587` uses STARTTLS (the default); `465` is implicit TLS. With STARTTLS on,
a server that doesn't offer it is an error, never a plaintext downgrade — set
`SMTP_STARTTLS=false` only for a trusted relay on localhost.

[Resend](https://resend.com) remains available as an optional hosted provider:
set `RESEND_API_KEY` (and `MAIL_FROM` on a verified domain) with no `SMTP_HOST`.

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

## Private deployment

Cadence is built to run **entirely on your own host**: one container serves the
API and the web app from the same origin, Postgres sits beside it, and nothing
phones home. The [`Dockerfile`](./Dockerfile) builds the SPA, embeds it in a
static Go binary (`server/internal/web`) and ships it on a distroless, non-root
image with a self-contained health check.

```bash
cp .env.example .env
# edit .env →
#   CADENCE_DB_PASSWORD=<long random>        required — compose refuses to start without it
#   CADENCE_WEB_URL=https://cadence.internal  CADENCE_WEB_ORIGINS=cadence.internal
#   CADENCE_COOKIE_SECURE=true                 when a TLS proxy fronts :8088
#   SMTP_HOST=… MAIL_FROM=…                    magic links by email (or leave unset — see below)
#   CADENCE_ADMIN_EMAILS=you@example.com       the first account, no CLI needed
docker compose up -d --build       # db (healthy) → app on http://<host>:8088
```

**First sign-in.** With the Postgres backend the server defaults to
`CADENCE_SIGNUP=invite`: only addresses that already have an account may
request a magic link. Unknown addresses get the same `200` and simply never
receive a link, so the public endpoint confirms nothing. Bootstrap yourself
either with `CADENCE_ADMIN_EMAILS` (always allowed) or by pre-creating the
account:

```bash
make invite EMAIL=you@example.com            # → docker compose exec app /cadence-server invite …
# or, running the binary directly:
CADENCE_BACKEND=postgres DATABASE_URL=… cadence-server invite you@example.com
```

`CADENCE_SIGNUP=domains:example.com,other.org` additionally admits anyone at
those domains; `open` restores the anyone-may-join behaviour (the memory backend's
default). With no SMTP configured the link is only written to the server log —
`docker compose logs app` — and, only while `CADENCE_DEV_AUTH=true`, returned in
the API response. `CADENCE_DEV_AUTH` defaults to **off** on Postgres or whenever
a live mailer is set, and the server logs a `WARN` at boot whenever it is on.

Put a TLS-terminating reverse proxy (Caddy, nginx, Traefik) in front of `:8088`
for a private hostname, set `CADENCE_COOKIE_SECURE=true` so the session cookie
is never sent in clear, and list that hostname in `CADENCE_WEB_ORIGINS`. The
app is same-origin with the API, so the allowlist never needs `*`. Every
response carries a strict `Content-Security-Policy` (`default-src 'self'`,
`frame-ancestors 'none'`, `'wasm-unsafe-eval'` only because WebLLM compiles
kernels to WebAssembly), `X-Content-Type-Options: nosniff`, `Referrer-Policy`
and `Permissions-Policy`.

### Backup & restore

```bash
make backup                                   # scripts/backup.sh → ./backups/cadence-<UTC stamp>.dump
make restore DUMP=backups/cadence-….dump      # scripts/restore.sh → fresh DB cadence_restore_<stamp> + smoke query
```

`backup.sh` runs `pg_dump -Fc` inside the compose `db` container (no client
tools needed on the host) and keeps the newest `CADENCE_BACKUP_KEEP` dumps
(default 14) — schedule it from cron and copy `./backups` off the box. A dump
is every tenant's data with RLS out of the picture, so `./backups` is created
`0700` and each dump `0600`; the script never prints a connection password.
`restore.sh` loads a dump into a **new** database and prints the tenant, task and
migration counts it finds, so a backup is proven restorable without touching
the live data; drop the scratch DB afterwards. For real recovery,
`docker compose stop app && CADENCE_RESTORE_REPLACE=1 scripts/restore.sh <dump>`
recreates the live `cadence` database from it. Rehearse this once before you
need it:

```bash
make backup
make restore DUMP="$(ls -t backups/*.dump | head -1)"
# [restore] restore OK → 'cadence_restore_…': 1 tenant(s), 42 task(s), 10 migration(s) recorded
docker compose exec db dropdb -U cadence cadence_restore_…
```

### What leaves the box

| | default | to make it zero |
|---|---|---|
| Fonts | **none** — Newsreader + Hanken Grotesk are self-hosted in the bundle | — |
| Magic-link email | **none** unless `SMTP_HOST` (your server) or `RESEND_API_KEY` (hosted) is set | use SMTP to a relay you run |
| Model prompts | **none** — WebGPU inference is in the tab; Ollama is reached only via your server | — |
| Model weights | WebLLM's upstream CDN on first use (`huggingface.co`, `raw.githubusercontent.com`) — the only hosts the CSP admits, and only while no local weights exist | set `CADENCE_MODEL_DIR` (see [On-device AI scheduling](#on-device-ai-scheduling)); the CSP then drops those hosts |
| Telemetry / analytics / update checks | **none** | — |

**Air-gapped install** needs, on the build side, the Docker base images and
`npm ci` / `go mod download` caches (or build the image elsewhere and
`docker save`/`load` it); at run time, an SMTP relay inside the network (or no
mail — links are in the log), an Ollama on the LAN if you want the larger
models, and the WebLLM artifacts under `CADENCE_MODEL_DIR` for in-browser
models. Nothing else is fetched.

## Configuration

| env | default | meaning |
|---|---|---|
| `CADENCE_ADDR` | `:8088` | listen address |
| `CADENCE_BACKEND` | `memory` | `memory` or `postgres` |
| `DATABASE_URL` | — | required for `postgres` |
| `CADENCE_AUTO_MIGRATE` | `true` | apply migrations on boot |
| `CADENCE_OUTBOX_RETENTION_DAYS` | `7` | prune outbox events older than this (postgres; hourly) |
| `CADENCE_WEB_URL` | `http://localhost:4173` | where the SPA lives (used in magic links) |
| `CADENCE_WEB_ORIGINS` | `localhost:*,127.0.0.1:*` | CORS + WebSocket origin allowlist |
| `SMTP_HOST` | — | SMTP server; set ⇒ magic links are emailed via SMTP (takes precedence over Resend) |
| `SMTP_PORT` | `587` | `587` = STARTTLS, `465` = implicit TLS |
| `SMTP_USER` / `SMTP_PASS` | — | SMTP credentials (PLAIN, LOGIN or CRAM-MD5; unset ⇒ no AUTH) |
| `SMTP_STARTTLS` | `true` | require STARTTLS on non-465 ports; `false` only for a trusted local relay |
| `RESEND_API_KEY` | — | optional [Resend](https://resend.com) key, used only when `SMTP_HOST` is unset |
| `MAIL_FROM` | — | sender address, e.g. `Cadence <cadence@example.com>`; **required** with SMTP or Resend |
| `CADENCE_DEV_AUTH` | `true` on memory with no mailer, else `false` | return/log the magic link when no live mailer is configured; a `WARN` is logged at boot while on |
| `CADENCE_SIGNUP` | `invite` on postgres, `open` on memory | `open` \| `invite` (existing accounts + admins only) \| `domains:<csv>` (those domains too) |
| `CADENCE_ADMIN_EMAILS` | — | comma-separated addresses that may always sign in (bootstraps the first account) |
| `CADENCE_COOKIE_SECURE` | `false` | set `true` behind HTTPS |
| `CADENCE_DB_PASSWORD` | — (**required** by compose; `cadence` for `./dev.sh`/`make`) | password of the Compose Postgres |
| `CADENCE_DB_PORT` | `55432` | host port the Compose Postgres binds |
| `CADENCE_PORT` | `8088` | host port the Compose `app` service binds |
| `OLLAMA_URL` | — | Ollama base URL (e.g. `http://localhost:11434`); unset ⇒ Ollama disabled. The browser never calls it directly |
| `CADENCE_MODEL_DIR` | — | directory of WebLLM artifacts served read-only at `/models/`; unset ⇒ weights come from WebLLM's upstream CDN |
| `CADENCE_AI_POLICY` | `local` | `local` or `local+cloud` (validated + reported; no cloud provider is wired yet) |
| `VITE_API_URL` (web) | `http://localhost:8088` | API base URL |

## Auth (magic link)

Cadence has native, passwordless auth — **we store only your email**:

1. Enter your email → `POST /auth/request` issues a one-time link.
2. The link (`/auth?token=…`) is emailed over **SMTP** when `SMTP_HOST` is set
   (or via Resend when only `RESEND_API_KEY` is); with no provider (dev) it's
   logged and returned so you can click it immediately. The endpoint is
   rate-limited per IP and per email (magic-link email is a bombing /
   cost-amplification target). Server logs only ever carry a redacted address
   (`m…@example.com`), never the full email or — outside dev mode — the link.
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
| `GET/POST` | `/api/v1/requirements` | list (`?projectId=`) / create — what a project must deliver |
| `PATCH/DELETE` | `/api/v1/requirements/{id}` | update / delete |
| `GET` | `/api/v1/audit` | the tenant's append-only audit log, keyset-paged (`?limit=&cursor=`) — read-only |
| `GET` | `/api/v1/realtime` | WebSocket event stream |
| `GET` | `/api/v1/ai/status` | AI providers this server offers (Ollama reachability + models, hosted WebLLM models, policy) |
| `POST` | `/api/v1/ai/chat` | one non-streaming chat turn `{model, messages, format?}` forwarded to Ollama; audited |
| `GET` | `/models/…` | read-only WebLLM artifacts when `CADENCE_MODEL_DIR` is set |

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

**"Plan my week with AI"** hands your whole backlog to a small
LLM that places each task around your energy — deep/important work in your
**learned peak**, light/admin in the post-lunch dip, **important before merely
urgent**, deadlines respected, personal tasks on the weekend, spread out and
collision-free. The peak/dip windows are **learned from your own completion
history** (falling back to a sensible default until there's enough signal), and
a deterministic reconcile step **guarantees the peak stays reserved for deep /
important work**. **Nothing leaves your infrastructure** — the browser never
talks to a model provider directly; every model call either stays in the tab or
goes through your own Cadence server. Two interchangeable backends (pattern
borrowed from `~/loam`):

- **WebGPU (primary)** — a model runs fully in-browser via
  [`@mlc-ai/web-llm`](https://github.com/mlc-ai/web-llm) (Gemma 2 2B / Gemma 3
  1B). Zero install; lazy-loaded so the app bundle stays small.
- **Ollama (fallback)** — an [Ollama](https://ollama.com) server for bigger /
  newer models (Gemma 3 4B, Qwen 2.5 7B), reached **only through the Cadence
  server's AI gateway**. Cadence reports what's installed; pick a model from the
  picker under the button.

Pick the backend/model from the **picker beside the button**; the choice is
saved locally. The model's output is reconciled into a guaranteed-valid,
in-range, collision-free schedule, so even a 1–2B model can't produce a broken
week. A **"quick arrange"** heuristic (no model) is always available as an
instant fallback.

### What leaves the device, per mode

| mode | prompts (your tasks) go to | model weights come from |
|---|---|---|
| WebGPU, default | nowhere — inference is in the tab | WebLLM's upstream CDN (HuggingFace weights, GitHub wasm) **on first use**, then the browser cache |
| WebGPU + `CADENCE_MODEL_DIR` | nowhere | **your Cadence server** (`GET /models/…`) — no third-party egress at all |
| Ollama (`OLLAMA_URL`) | **your Cadence server**, which forwards to Ollama | Ollama's own store (`ollama pull`) |

Ollama is opt-in on the server (`OLLAMA_URL`, e.g. `http://localhost:11434`);
without it the picker shows **"Ollama · via server · not configured"**. The
gateway (`GET /api/v1/ai/status`, `POST /api/v1/ai/chat`) validates each request,
forwards a single non-streaming turn with a 10-minute deadline and a response
cap, and writes **one `ai.chat` row per call** to the tenant's append-only
`audit_log` — provider, model, prompt size, ok/err, duration — never the
prompt itself. Sign-ins and personal-token changes land in the same log
(`GET /api/v1/audit`); see `docs/SCHEMA.md` for the no-PII rule.
`CADENCE_AI_POLICY` is `local` by default; `local+cloud` is accepted and reported
in the status payload, but no cloud provider is wired, so nothing leaves either
way today.

To host the WebLLM artifacts yourself, point `CADENCE_MODEL_DIR` at a directory
laid out like a HuggingFace clone (one folder per model id, wasm under `lib/`):

```
$CADENCE_MODEL_DIR/
├── gemma3-1b-it-q4f16_1-MLC/         # git clone https://huggingface.co/mlc-ai/gemma3-1b-it-q4f16_1-MLC
│   ├── mlc-chat-config.json, ndarray-cache.json, params_shard_*.bin, …
└── lib/
    └── gemma3-1b-it-q4f16_1_cs1k-webgpu.wasm   # from mlc-ai/binary-mlc-llm-libs (see prebuiltAppConfig)
```

The picker tags hosted models **"hosted here"** and the loader rewrites WebLLM's
`model` / `model_lib` URLs to `/models/…`. `/models/` is session-protected and
read-only (no listings). WebLLM's own loader fetches without credentials, so
server-hosted weights are picked up when the app and API share an origin (a
reverse proxy in front of both); with the split-origin dev setup the loader
falls back to the upstream CDN and the picker says so.

> The AI code lives in [`src/ai/`](./src/ai) (`config.ts`, `llm.ts`,
> `llm.worker.ts`, `scheduler.ts`) and
> [`server/internal/httpapi/ai.go`](./server/internal/httpapi/ai.go). WebGPU
> needs a Chromium-based browser or Safari Technology Preview; otherwise the
> picker defaults to Ollama.

## Build & check

```bash
npm run build                 # type-check + bundle the web app (repo root)
cd server && go build ./... && go vet ./...   # build + vet the backend
gofmt -l server                # should print nothing
make web-embed                 # bundle the web app INTO server/bin/cadence-server (one binary serves both)
docker compose build app       # the same, as the distroless image
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
