package postgres

import (
	"context"
	"errors"

	"github.com/cadence/server/internal/domain"
	"github.com/jackc/pgx/v5"
)

// PurgeTenant hard-deletes a tenant and everything it owns, in one
// transaction. It runs under withTenant so RLS admits the deletes, but every
// statement carries its own explicit tenant predicate — the same two-layer
// rule as every other query, so the purge is exactly as scoped under a
// superuser (which bypasses RLS) as under the restricted app role.
//
// Order: children before parents so nothing relies on ON DELETE CASCADE
// (which would also work, but explicit is auditable), then the non-RLS auth
// tables keyed by tenant_id or by the tenant's emails, then the outbox, and
// the tenant row last. Any new tenant-scoped table MUST be added here AND to
// storetest.PurgeTables, which is what proves this list is complete.
func (s *Store) PurgeTenant(ctx context.Context, tenantID string) error {
	return s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM tenants WHERE id = $1`, tenantID).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		// audit_log is append-only by trigger; the transaction-local
		// app.purging flag is the trigger's one escape hatch, and this is the
		// one place it is set. It also covers the ON DELETE CASCADE from the
		// tenants row below.
		if _, err := tx.Exec(ctx, `SELECT set_config('app.purging', '1', true)`); err != nil {
			return err
		}
		stmts := []string{
			`DELETE FROM audit_log       WHERE tenant_id = $1`,
			// the signal surface (0014/0015): leaves first
			`DELETE FROM outputs             WHERE tenant_id = $1`,
			`DELETE FROM work_item_signals   WHERE tenant_id = $1`,
			`DELETE FROM signal_participants WHERE tenant_id = $1`,
			`DELETE FROM signal_bodies       WHERE tenant_id = $1`,
			`DELETE FROM signals             WHERE tenant_id = $1`,
			`DELETE FROM sources             WHERE tenant_id = $1`,
			`DELETE FROM requirements    WHERE tenant_id = $1`,
			`DELETE FROM tasks           WHERE tenant_id = $1`,
			`DELETE FROM project_members WHERE tenant_id = $1`,
			`DELETE FROM projects        WHERE tenant_id = $1`,
			`DELETE FROM clients         WHERE tenant_id = $1`,
			`DELETE FROM users           WHERE tenant_id = $1`,
			// auth tables are not RLS-scoped; keyed by tenant_id, or by the
			// tenant's emails for login_tokens (which have no tenant column).
			`DELETE FROM api_tokens   WHERE tenant_id = $1`,
			`DELETE FROM sessions     WHERE tenant_id = $1`,
			`DELETE FROM login_tokens WHERE email IN (SELECT email FROM accounts WHERE tenant_id = $1)`,
			`DELETE FROM accounts     WHERE tenant_id = $1`,
			`DELETE FROM outbox       WHERE tenant_id = $1`,
			`DELETE FROM tenants      WHERE id = $1`,
		}
		for _, q := range stmts {
			if _, err := tx.Exec(ctx, q, tenantID); err != nil {
				return err
			}
		}
		return nil
	})
}
