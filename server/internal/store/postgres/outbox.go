package postgres

import (
	"context"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/jackc/pgx/v5"
)

// DefaultOutboxRetention is how long outbox rows are kept when Config leaves
// OutboxRetention unset.
const DefaultOutboxRetention = 7 * 24 * time.Hour

// outboxPruneEvery is the pruner's ticker interval.
const outboxPruneEvery = time.Hour

// emitEntity appends a generic-envelope event to the outbox inside tx. It is
// the one-liner later rounds use for new entity types:
//
//	emitEntity(ctx, tx, tenantID, actorID, domain.EventSignalCreated, "signal", sig.ID, sig.Meta())
//
// RULE — payload is METADATA ONLY (id, version, kind, occurredAt, title…).
// Never pass a signal body or participant PII: the event is fanned out to
// every subscriber in the tenant and persisted in the outbox for replay.
// Pass nil for deletes.
func emitEntity(ctx context.Context, tx pgx.Tx, tenantID, actorID string, typ domain.EventType, entityType, entityID string, payload any) error {
	ev, err := domain.NewEntityEvent(typ, tenantID, actorID, entityType, entityID, payload)
	if err != nil {
		return err
	}
	return emit(ctx, tx, ev)
}

// PruneOutbox deletes outbox rows created before cutoff and reports how many.
//
// Why created_at and not published_at: the outbox's only consumer is the
// LISTEN/NOTIFY listener (broker.go), which reads a row by id the instant it
// commits and deliberately does NOT mark it published — with N instances all
// listening, "published" would mean "seen by whichever instance got there
// first", which is meaningless, and a future external relay polling
// `published_at IS NULL` would then never see the row. So published_at stays
// reserved for that relay, and retention is simply time-based on created_at:
// the row's realtime job is done at commit, and its replay job expires with
// the retention window (a client offline longer than that re-bootstraps).
func (s *Store) PruneOutbox(ctx context.Context, cutoff time.Time) (int64, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM outbox WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// pruneLoop runs the housekeeping sweeps — PruneOutbox and PruneSignals
// (signals.go) — once at start and then hourly until ctx ends. Errors are
// logged, never fatal: a transient DB hiccup must not take the server down
// over housekeeping.
func (s *Store) pruneLoop(ctx context.Context) {
	tick := time.NewTicker(outboxPruneEvery)
	defer tick.Stop()
	for {
		n, err := s.PruneOutbox(ctx, time.Now().Add(-s.retention))
		switch {
		case err != nil && ctx.Err() == nil:
			s.log.Warn("outbox prune failed", "err", err)
		case n > 0:
			s.log.Info("outbox pruned", "rows", n, "retention", s.retention.String())
		}
		n, err = s.PruneSignals(ctx, time.Now())
		switch {
		case err != nil && ctx.Err() == nil:
			s.log.Warn("signal retention sweep failed", "err", err)
		case n > 0:
			s.log.Info("expired signals pruned", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
