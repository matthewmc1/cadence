package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The signals_immutable trigger (0014) is the database's own guarantee, below
// the app's ApplySignalPatch: a raw UPDATE to a content column is refused,
// while the disposition columns stay writable. Gated on TEST_DATABASE_URL.
//
//	TEST_DATABASE_URL=postgres://… go test ./internal/store/postgres/ -run Immutable
func TestPostgresSignalsImmutableTrigger(t *testing.T) {
	st, url := openTestStore(t)
	ctx := context.Background()
	raw, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("raw pool: %v", err)
	}
	defer raw.Close()

	acct, err := st.FindOrCreateAccount(ctx, fmt.Sprintf("immutable+%d@example.com", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	defer st.PurgeTenant(ctx, acct.TenantID)
	sig, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, domain.CreateSignalInput{Kind: domain.SignalNote, Title: "frozen", Text: "as captured"})
	if err != nil {
		t.Fatalf("CreateSignal: %v", err)
	}

	exec := func(q string, args ...any) error {
		tx, err := raw.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, acct.TenantID); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	for _, c := range []struct{ name, q string }{
		{"title", `UPDATE signals SET title = 'rewritten' WHERE id = $1 AND tenant_id = $2`},
		{"excerpt", `UPDATE signals SET excerpt = 'rewritten' WHERE id = $1 AND tenant_id = $2`},
		{"occurred_at", `UPDATE signals SET occurred_at = now() WHERE id = $1 AND tenant_id = $2`},
		{"kind", `UPDATE signals SET kind = 'email' WHERE id = $1 AND tenant_id = $2`},
		{"external_id", `UPDATE signals SET external_id = 'x' WHERE id = $1 AND tenant_id = $2`},
	} {
		err := exec(c.q, sig.ID, acct.TenantID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" { // insufficient_privilege, as the trigger raises
			t.Errorf("raw UPDATE of %s: want the immutability trigger to refuse (42501), got %v", c.name, err)
		}
	}
	if err := exec(`UPDATE signals SET stage = 'dismissed', disposition_at = now(), version = version + 1 WHERE id = $1 AND tenant_id = $2`, sig.ID, acct.TenantID); err != nil {
		t.Errorf("raw UPDATE of the disposition columns must be allowed, got %v", err)
	}
	if got, err := st.GetSignal(ctx, acct.TenantID, sig.ID); err != nil || got.Title != "frozen" || got.Stage != domain.SignalDismissed {
		t.Errorf("after the raw updates: want title frozen / stage dismissed, got %+v (%v)", got, err)
	}
}
