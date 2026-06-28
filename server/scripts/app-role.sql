-- Cadence — production application role.
--
-- RLS only isolates tenants if the app connects as a NON-superuser, NOBYPASSRLS
-- role. Superusers (and BYPASSRLS roles) silently bypass every policy. Run this
-- once as a superuser, then point DATABASE_URL at cadence_app.
--
--   psql "$ADMIN_URL" -f server/scripts/app-role.sql
--   DATABASE_URL=postgres://cadence_app:...@host/cadence  CADENCE_BACKEND=postgres ...
--
-- Migrations still run as the owner/superuser (DDL); cadence_app only gets DML.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'cadence_app') THEN
    CREATE ROLE cadence_app LOGIN PASSWORD 'change-me' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
  END IF;
END $$;

GRANT USAGE ON SCHEMA public TO cadence_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO cadence_app;

-- so future migrations' tables are reachable without re-granting
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO cadence_app;

-- cadence_app must be able to set the per-transaction tenant GUC (it can: custom
-- GUCs under an extension-prefixed name are user-settable by default) and LISTEN
-- on the realtime channel — both are available to ordinary roles.
