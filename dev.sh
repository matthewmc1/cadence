#!/usr/bin/env bash
#
# Cadence local dev — one command brings up a PERSISTENT Postgres, the API, and
# the web app. Data survives restarts because it lives in a Docker volume, not
# the in-memory store.
#
#   ./dev.sh                      # persistent Postgres backend (default)
#   CADENCE_BACKEND=memory ./dev.sh   # in-memory (NOT persisted — data lost on restart)
#
# Env is read from ./.env if present (e.g. RESEND_API_KEY for real magic-link email).
set -euo pipefail
cd "$(dirname "$0")"

c_cyan=$'\033[1;36m'; c_red=$'\033[1;31m'; c_yellow=$'\033[1;33m'; c_reset=$'\033[0m'
log()  { printf '%s[cadence]%s %s\n' "$c_cyan" "$c_reset" "$*"; }
warn() { printf '%s[cadence]%s %s\n' "$c_yellow" "$c_reset" "$*"; }
die()  { printf '%s[cadence]%s %s\n' "$c_red" "$c_reset" "$*" >&2; exit 1; }

# Load .env (export every var) so secrets/config are picked up.
if [ -f .env ]; then set -a; . ./.env; set +a; fi

BACKEND="${CADENCE_BACKEND:-postgres}"
DB_PORT="${CADENCE_DB_PORT:-55432}"

pids=()
cleanup() {
  printf '\n'
  log "stopping API + web (Postgres keeps running so your data persists — 'make db-down' to stop it)"
  for pid in "${pids[@]:-}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

command -v go   >/dev/null || die "Go is required (https://go.dev/dl/)."
command -v node >/dev/null || die "Node is required (https://nodejs.org)."

if [ "$BACKEND" = "postgres" ]; then
  docker compose version >/dev/null 2>&1 || die \
"Docker Compose is required for the persistent Postgres backend.
       Install Docker Desktop, or run non-persistently:  CADENCE_BACKEND=memory ./dev.sh"

  log "starting persistent Postgres (docker compose up -d db)…"
  docker compose up -d db >/dev/null
  log "waiting for Postgres…"
  for _ in $(seq 1 30); do
    docker compose exec -T db pg_isready -U cadence -d cadence >/dev/null 2>&1 && break
    sleep 1
  done
  docker compose exec -T db pg_isready -U cadence -d cadence >/dev/null 2>&1 \
    || die "Postgres did not become ready. Check 'docker compose logs db'."

  export CADENCE_BACKEND=postgres
  export DATABASE_URL="${DATABASE_URL:-postgres://cadence:cadence@localhost:${DB_PORT}/cadence?sslmode=disable}"
  log "backend: Postgres — data persists in the 'cadence_pgdata' volume ✓"
else
  export CADENCE_BACKEND=memory
  warn "backend: in-memory — DATA IS NOT PERSISTED and is lost when the API stops."
fi

# --- API server (auto-migrates on boot) ---
log "building API server…"
mkdir -p server/bin
( cd server && go build -o ./bin/cadence-server ./cmd/cadence-server )
log "starting API on http://localhost${CADENCE_ADDR:-:8088}"
( cd server && exec ./bin/cadence-server ) &
pids+=($!)

# --- web app ---
[ -d node_modules ] || { log "installing web deps (npm install)…"; npm install; }
log "starting web on http://localhost:5173"
npm run dev &
pids+=($!)

log "Cadence is up → open http://localhost:5173  (Ctrl-C to stop)"
wait
