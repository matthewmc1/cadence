package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store/postgres"
	"github.com/cadence/server/internal/store/storetest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// purgePredicates is the raw per-table "rows belonging to this tenant"
// predicate. login_tokens has no tenant column and is keyed by email.
var purgePredicates = map[string]string{
	"tenants":             "id = $1",
	"users":               "tenant_id = $1",
	"clients":             "tenant_id = $1",
	"projects":            "tenant_id = $1",
	"project_members":     "tenant_id = $1",
	"tasks":               "tenant_id = $1",
	"requirements":        "tenant_id = $1",
	"audit_log":           "tenant_id = $1",
	"sources":             "tenant_id = $1",
	"signals":             "tenant_id = $1",
	"signal_bodies":       "tenant_id = $1",
	"signal_participants": "tenant_id = $1",
	"work_item_signals":   "tenant_id = $1",
	"outputs":             "tenant_id = $1",
	"outbox":              "tenant_id = $1",
	"accounts":            "tenant_id = $1",
	"login_tokens":        "email = $1", // $1 is the email for this table
	"sessions":            "tenant_id = $1",
	"api_tokens":          "tenant_id = $1",
}

// Tenant purge for the Postgres adapter. Row counts go through a separate raw
// pool, not the Store, so the assertion is independent of the code under test.
// Counts run with app.tenant_id set to the tenant so they are correct under the
// restricted NOBYPASSRLS role as well as the dev superuser.
//
//	TEST_DATABASE_URL=postgres://… go test ./internal/store/postgres/ -run Purge
func TestPostgresTenantPurge(t *testing.T) {
	st, url := openTestStore(t)
	ctx := context.Background()

	raw, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("raw pool: %v", err)
	}
	defer raw.Close()

	for _, table := range storetest.PurgeTables {
		if _, ok := purgePredicates[table]; !ok {
			t.Fatalf("purge_test: no raw predicate for table %q — add one", table)
		}
	}
	count := func(t *testing.T, table string, acct domain.Account) int {
		t.Helper()
		tx, err := raw.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, acct.TenantID); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		arg := acct.TenantID
		if table == "login_tokens" {
			arg = acct.Email
		}
		var n int
		q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s`, table, purgePredicates[table])
		if err := tx.QueryRow(ctx, q, arg).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}

	nonce := time.Now().UnixNano()
	a := fmt.Sprintf("purge-a+%d@example.com", nonce)
	b := fmt.Sprintf("purge-b+%d@example.com", nonce)
	storetest.AssertTenantPurge(t, st, a, b, count, true)

	// tidy: B is test data too
	acctB, err := st.FindOrCreateAccount(ctx, b)
	if err == nil {
		_ = st.PurgeTenant(ctx, acctB.TenantID)
	}
	// ...and A's re-sign-up tenant from the suite's last step
	if acctA, err := st.FindOrCreateAccount(ctx, a); err == nil {
		_ = st.PurgeTenant(ctx, acctA.TenantID)
	}
}

// Outbox retention: rows older than the cutoff are pruned, newer ones kept.
// The row is backdated via a raw UPDATE, since created_at is DEFAULT now().
func TestPostgresOutboxPrune(t *testing.T) {
	st, url := openTestStore(t)
	ctx := context.Background()
	raw, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("raw pool: %v", err)
	}
	defer raw.Close()

	acct, err := st.FindOrCreateAccount(ctx, fmt.Sprintf("prune+%d@example.com", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	defer st.PurgeTenant(ctx, acct.TenantID)

	old, err := st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "old"})
	if err != nil {
		t.Fatalf("create old: %v", err)
	}
	fresh, err := st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "fresh"})
	if err != nil {
		t.Fatalf("create fresh: %v", err)
	}
	if _, err := raw.Exec(ctx, `UPDATE outbox SET created_at = now() - interval '30 days' WHERE entity_id = $1`, old.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if _, err := st.PruneOutbox(ctx, time.Now().Add(-postgres.DefaultOutboxRetention)); err != nil {
		t.Fatalf("PruneOutbox: %v", err)
	}
	remaining := func(entityID string) int {
		var n int
		if err := raw.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE entity_id = $1`, entityID).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	if n := remaining(old.ID); n != 0 {
		t.Errorf("expired outbox row survived prune (%d left)", n)
	}
	if n := remaining(fresh.ID); n != 1 {
		t.Errorf("fresh outbox row was pruned (want 1, got %d)", n)
	}
}
