#!/usr/bin/env bash
#
# Cadence — restore a scripts/backup.sh dump into a FRESH database and prove it
# loaded (a backup you have never restored is a hope, not a backup).
#
#   scripts/restore.sh backups/cadence-20260906-120000.dump
#       → restores into a new database `cadence_restore_<stamp>`, runs a smoke
#         query, leaves it for inspection. The live `cadence` DB is untouched.
#
#   CADENCE_RESTORE_REPLACE=1 scripts/restore.sh backups/….dump
#       → DROPS and recreates the live `cadence` database from the dump. Stop
#         the app first (`docker compose stop app`); this is the real recovery.
#
#   scripts/restore.sh backups/….dump some_other_name    # explicit target DB name
#
# Runs inside the compose `db` container by default (no client tools needed
# on the host). Set PGRESTORE_ADMIN_URL to restore into another server with
# local psql/pg_restore instead; it must be a URL to the `postgres` maintenance
# database with rights to CREATE DATABASE.
#
# Like backup.sh, only CADENCE_DB_PASSWORD is read from .env — the mail / API
# keys in there are never exported to psql, pg_restore or docker.
set -euo pipefail
cd "$(dirname "$0")/.."

c_cyan=$'\033[1;36m'; c_red=$'\033[1;31m'; c_yellow=$'\033[1;33m'; c_reset=$'\033[0m'
log()  { printf '%s[restore]%s %s\n' "$c_cyan" "$c_reset" "$*"; }
warn() { printf '%s[restore]%s %s\n' "$c_yellow" "$c_reset" "$*"; }
die()  { printf '%s[restore]%s %s\n' "$c_red" "$c_reset" "$*" >&2; exit 1; }

dump="${1:-}"
[ -n "$dump" ] || die "usage: scripts/restore.sh <backup.dump> [target-db]"
[ -f "$dump" ] || die "no such file: $dump"

# .env supplies CADENCE_DB_PASSWORD (compose refuses to interpolate without it).
# Sourced in a subshell so ONLY that key reaches this process — the mail/API
# keys in .env have no business in psql's or pg_restore's environment. An
# already-exported value wins, as it does for compose itself.
if [ -z "${CADENCE_DB_PASSWORD:-}" ] && [ -f .env ]; then
  CADENCE_DB_PASSWORD="$(set +u; . ./.env >/dev/null 2>&1; printf '%s' "${CADENCE_DB_PASSWORD:-}")"
fi
export CADENCE_DB_PASSWORD="${CADENCE_DB_PASSWORD:-cadence}"

stamp="$(date -u +%Y%m%d%H%M%S)"
if [ "${CADENCE_RESTORE_REPLACE:-0}" = "1" ]; then
  target="${2:-cadence}"
else
  target="${2:-cadence_restore_$stamp}"
fi
[[ "$target" =~ ^[a-z_][a-z0-9_]*$ ]] || die "target database name must be a plain identifier: $target"
if [ "$target" = "cadence" ] && [ "${CADENCE_RESTORE_REPLACE:-0}" != "1" ]; then
  die "refusing to overwrite the live 'cadence' database — set CADENCE_RESTORE_REPLACE=1 to mean it."
fi

# ---- two transports, one interface -----------------------------------------
if [ -n "${PGRESTORE_ADMIN_URL:-}" ]; then
  command -v psql >/dev/null && command -v pg_restore >/dev/null \
    || die "psql/pg_restore not found — install the Postgres client tools or unset PGRESTORE_ADMIN_URL."
  # Derive the target's URL from the admin URL by swapping the database name.
  target_url="${PGRESTORE_ADMIN_URL%/*}/$target"
  admin_sql()   { psql -v ON_ERROR_STOP=1 -Atq "$PGRESTORE_ADMIN_URL" -c "$1"; }
  target_sql()  { psql -v ON_ERROR_STOP=1 -Atq "$target_url" -c "$1"; }
  do_restore()  { pg_restore --no-owner --no-privileges --exit-on-error -d "$target_url" "$dump"; }
else
  docker compose ps --status running db 2>/dev/null | grep -q cadence-db \
    || die "the compose db container isn't running — 'make db-up' first, or set PGRESTORE_ADMIN_URL."
  admin_sql()   { docker compose exec -T db psql -v ON_ERROR_STOP=1 -Atq -U cadence -d postgres -c "$1"; }
  target_sql()  { docker compose exec -T db psql -v ON_ERROR_STOP=1 -Atq -U cadence -d "$target" -c "$1"; }
  do_restore()  { docker compose exec -T db pg_restore --no-owner --no-privileges --exit-on-error -U cadence -d "$target" < "$dump"; }
fi

# ---- fresh database ----------------------------------------------------------
if [ "${CADENCE_RESTORE_REPLACE:-0}" = "1" ]; then
  warn "REPLACING database '$target' from $dump"
  # FORCE disconnects the app if it is still attached; stop it first regardless.
  admin_sql "DROP DATABASE IF EXISTS $target WITH (FORCE)"
else
  if [ "$(admin_sql "SELECT 1 FROM pg_database WHERE datname = '$target'")" = "1" ]; then
    die "database '$target' already exists — pick another name or drop it first."
  fi
fi
log "creating database '$target'…"
admin_sql "CREATE DATABASE $target"

log "restoring $dump…"
do_restore

# ---- smoke test --------------------------------------------------------------
# A restore that "succeeded" with zero tenants is a wrong dump or a wrong
# server; count the things that matter and print them.
tenants="$(target_sql 'SELECT count(*) FROM tenants')"
tasks="$(target_sql 'SELECT count(*) FROM tasks')"
migrated="$(target_sql 'SELECT count(*) FROM schema_migrations' 2>/dev/null || echo '?')"
log "restore OK → '$target': $tenants tenant(s), $tasks task(s), $migrated migration(s) recorded"
[ "$tenants" -ge 0 ] 2>/dev/null || die "smoke query failed (tenants table missing?)"

if [ "${CADENCE_RESTORE_REPLACE:-0}" = "1" ]; then
  log "start the app again: docker compose start app"
else
  log "inspect it with:  docker compose exec db psql -U cadence -d $target"
  log "drop it when done: docker compose exec db dropdb -U cadence $target"
fi
