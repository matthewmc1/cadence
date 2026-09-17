-- 0010_outbox_hygiene — keep the outbox bounded and purgeable.
--
-- The outbox is append-only and, until now, nothing ever removed rows from
-- it, so it grew without bound. Two background operations now touch it:
--
--   * RETENTION: the server prunes rows older than CADENCE_OUTBOX_RETENTION_DAYS
--     (default 7) on an hourly ticker. Pruning is by created_at, not
--     published_at — the only consumer today is the LISTEN/NOTIFY listener,
--     which fetches a row by id the instant it is committed and never marks it
--     published (see postgres/outbox.go for why it must not).
--   * PURGE: PurgeTenant deletes every outbox row carrying the tenant's id.
--
-- Both need an index the original schema lacks: outbox_unpublished_idx is
-- partial (published_at IS NULL) so a plain range scan over created_at still
-- works for it, but a tenant-keyed delete would be a full scan.
--
-- entity_type mirrors the new generic event envelope (domain.Event.EntityType)
-- so a future relay can filter by entity without unpacking payload. Additive,
-- constant default: a clean zero-downtime EXPAND step.

ALTER TABLE outbox ADD COLUMN entity_type text NOT NULL DEFAULT '';

CREATE INDEX outbox_created_idx ON outbox (created_at);
CREATE INDEX outbox_tenant_idx  ON outbox (tenant_id);
