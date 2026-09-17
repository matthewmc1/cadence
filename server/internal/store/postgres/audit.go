package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
	"github.com/jackc/pgx/v5"
)

const auditCols = `id::text, tenant_id::text, at, actor_id::text, kind, entity_type, entity_id::text, detail, ip_hash`

// AppendAudit inserts one row. The table's BEFORE UPDATE/DELETE trigger makes
// it immutable from here on; only PurgeTenant (which sets app.purging) can
// remove it. No outbox event: the log is its own record (see store.Store).
func (s *Store) AppendAudit(ctx context.Context, tenantID string, e domain.AuditEntry) (*domain.AuditEntry, error) {
	if err := store.NormalizeAuditEntry(tenantID, &e, time.Now().UTC()); err != nil {
		return nil, err
	}
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return insertAudit(ctx, tx, e)
	})
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// appendAuditTx is AppendAudit inside a caller's transaction — how a store-side
// producer (the retention sweep) records what it did in the very transaction
// that did it, so the row and the deletion commit together or not at all.
func appendAuditTx(ctx context.Context, tx pgx.Tx, tenantID string, e domain.AuditEntry) error {
	if err := store.NormalizeAuditEntry(tenantID, &e, time.Now().UTC()); err != nil {
		return err
	}
	return insertAudit(ctx, tx, e)
}

func insertAudit(ctx context.Context, tx pgx.Tx, e domain.AuditEntry) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, id, at, actor_id, kind, entity_type, entity_id, detail, ip_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)`,
		e.TenantID, e.ID, e.At, e.ActorID, e.Kind, e.EntityType, e.EntityID, string(e.Detail), e.IPHash)
	return err
}

// ListAudit is the keyset-paged read: tenant fence, cursor predicate on
// (at, id), newest first, limit+1 (see ListTasksPaged).
func (s *Store) ListAudit(ctx context.Context, tenantID string, p store.Page) (store.PageResult[domain.AuditEntry], error) {
	cur, err := store.DecodeCursor(p.Cursor)
	if err != nil {
		return store.PageResult[domain.AuditEntry]{}, err
	}
	limit := p.EffectiveLimit()

	var out store.PageResult[domain.AuditEntry]
	err = s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		after, args := keysetWhere(cur, "at", 2) // $1 is tenant_id
		args = append([]any{tenantID}, args...)
		args = append(args, limit+1)
		q := `SELECT ` + auditCols + ` FROM audit_log WHERE tenant_id = $1` + after +
			fmt.Sprintf(` ORDER BY at DESC, id DESC LIMIT $%d`, len(args))
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var entries []domain.AuditEntry
		for rows.Next() {
			var e domain.AuditEntry
			if err := rows.Scan(&e.ID, &e.TenantID, &e.At, &e.ActorID, &e.Kind, &e.EntityType, &e.EntityID, &e.Detail, &e.IPHash); err != nil {
				return err
			}
			entries = append(entries, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = store.Paginate(entries, limit, store.AuditKey)
		return nil
	})
	return out, err
}
