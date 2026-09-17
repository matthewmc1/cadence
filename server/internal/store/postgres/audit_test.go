package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The audit_log append-only trigger: a raw UPDATE or DELETE — even by the
// superuser the raw admin pool connects as — must be refused, and the same
// DELETE must go through once app.purging is set for the transaction (the
// escape hatch PurgeTenant uses). Gated on TEST_DATABASE_URL like the other
// suites.
func TestPostgresAuditAppendOnly(t *testing.T) {
	st, url := openTestStore(t)
	ctx := context.Background()
	raw, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("raw pool: %v", err)
	}
	defer raw.Close()

	acct, err := st.FindOrCreateAccount(ctx, fmt.Sprintf("audit+%d@example.com", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	defer st.PurgeTenant(ctx, acct.TenantID)

	row, err := st.AppendAudit(ctx, acct.TenantID, domain.AuditEntry{ActorID: &acct.UserID, Kind: domain.AuditLoginVerified})
	if err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}

	// Every raw statement runs with app.tenant_id set so RLS admits the row;
	// what we are testing is the trigger, not the policy.
	exec := func(purging bool, q string) error {
		tx, err := raw.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, acct.TenantID); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		if purging {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.purging', '1', true)`); err != nil {
				t.Fatalf("set_config(purging): %v", err)
			}
		}
		if _, err := tx.Exec(ctx, q, row.ID, acct.TenantID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	count := func() int {
		var n int
		if err := raw.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE id = $1 AND tenant_id = $2`, row.ID, acct.TenantID).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	if err := exec(false, `UPDATE audit_log SET kind = 'tampered' WHERE id = $1 AND tenant_id = $2`); err == nil {
		t.Error("UPDATE on audit_log succeeded — the log is not append-only")
	}
	if err := exec(false, `DELETE FROM audit_log WHERE id = $1 AND tenant_id = $2`); err == nil {
		t.Error("DELETE on audit_log succeeded without app.purging — the log is not append-only")
	}
	if n := count(); n != 1 {
		t.Fatalf("row should have survived the refused statements, count=%d", n)
	}
	if err := exec(true, `DELETE FROM audit_log WHERE id = $1 AND tenant_id = $2`); err != nil {
		t.Errorf("DELETE under app.purging refused: %v", err)
	}
	if n := count(); n != 0 {
		t.Errorf("row should be gone after the purging delete, count=%d", n)
	}
}
