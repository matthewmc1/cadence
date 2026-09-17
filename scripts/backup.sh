#!/usr/bin/env bash
#
# Cadence — logical backup of the Postgres database (pg_dump custom format),
# keeping the newest N dumps.
#
#   scripts/backup.sh                       # → ./backups/cadence-<UTC stamp>.dump, keeps 14
#   CADENCE_BACKUP_KEEP=30 scripts/backup.sh
#   CADENCE_BACKUP_DIR=/mnt/backups scripts/backup.sh
#
# By default pg_dump runs INSIDE the compose `db` container, so no Postgres
# client tools are needed on the host. To dump a database that isn't the
# compose one, set PGDUMP_URL (uses a local pg_dump):
#
#   PGDUMP_URL='postgres://cadence:…@db.internal:5432/cadence' scripts/backup.sh
#
# Custom format (-Fc) is compressed and restorable table-by-table; restore
# with scripts/restore.sh. Put the backups directory on separate storage.
#
# A dump holds every tenant's data (signal bodies, participants' emails, the
# audit log, credential hashes) with RLS and the tenant fence out of the
# picture, so the directory is created 0700 and each dump is written 0600.
# Nothing secret is logged: connection URLs are printed with the userinfo
# stripped, and only CADENCE_DB_PASSWORD is read from .env (the mail / API
# keys in there are not exported to pg_dump or docker).
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."

c_cyan=$'\033[1;36m'; c_red=$'\033[1;31m'; c_reset=$'\033[0m'
log() { printf '%s[backup]%s %s\n' "$c_cyan" "$c_reset" "$*"; }
die() { printf '%s[backup]%s %s\n' "$c_red" "$c_reset" "$*" >&2; exit 1; }
# redact_url drops `user:pass@` from a connection URL for logging.
redact_url() {
  case "$1" in
    *://*@*) printf '%s://…@%s' "${1%%://*}" "${1##*@}" ;;
    *)       printf '%s' "$1" ;;
  esac
}

# .env supplies CADENCE_DB_PASSWORD (compose refuses to interpolate without it).
# It is sourced in a subshell so only that one key reaches this process; an
# already-exported value wins, as it does for compose itself.
if [ -z "${CADENCE_DB_PASSWORD:-}" ] && [ -f .env ]; then
  CADENCE_DB_PASSWORD="$(set +u; . ./.env >/dev/null 2>&1; printf '%s' "${CADENCE_DB_PASSWORD:-}")"
fi
export CADENCE_DB_PASSWORD="${CADENCE_DB_PASSWORD:-cadence}"

DIR="${CADENCE_BACKUP_DIR:-./backups}"
KEEP="${CADENCE_BACKUP_KEEP:-14}"
[[ "$KEEP" =~ ^[0-9]+$ ]] && [ "$KEEP" -ge 1 ] || die "CADENCE_BACKUP_KEEP must be a positive integer"

install -d -m 700 "$DIR"
stamp="$(date -u +%Y%m%d-%H%M%S)"
out="$DIR/cadence-$stamp.dump"
tmp="$out.part"
trap 'rm -f "$tmp"' EXIT

# --no-owner/--no-privileges: the dump restores cleanly under whichever role
# runs the restore. RLS policies are table objects and are always included.
if [ -n "${PGDUMP_URL:-}" ]; then
  command -v pg_dump >/dev/null || die "pg_dump not found — install the Postgres client tools or unset PGDUMP_URL to dump via Docker."
  log "dumping $(redact_url "$PGDUMP_URL") (local pg_dump)…"
  pg_dump -Fc --no-owner --no-privileges "$PGDUMP_URL" > "$tmp"
else
  docker compose ps --status running db 2>/dev/null | grep -q cadence-db \
    || die "the compose db container isn't running — 'make db-up' first, or set PGDUMP_URL."
  log "dumping via docker compose exec db pg_dump…"
  docker compose exec -T db pg_dump -Fc --no-owner --no-privileges -U cadence -d cadence > "$tmp"
fi
[ -s "$tmp" ] || die "pg_dump produced an empty file"
mv "$tmp" "$out"
trap - EXIT
log "wrote $out ($(du -h "$out" | cut -f1))"

# Retention: keep the newest $KEEP dumps (lexical order == chronological, the
# stamp is UTC and zero-padded).
n=0
for f in $(ls -1 "$DIR"/cadence-*.dump 2>/dev/null | sort -r); do
  n=$((n + 1))
  if [ "$n" -gt "$KEEP" ]; then
    rm -f "$f"
    log "pruned $f"
  fi
done
log "keeping the newest $KEEP dump(s) in $DIR"
