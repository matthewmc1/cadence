package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cadence/server/internal/store/storetest"
)

// Tenant isolation for the Postgres adapter — this is the real Row-Level
// Security proof: tenant B, running under app.tenant_id = B, cannot reach
// tenant A's rows even by their exact id. The Store runs as the restricted
// NOBYPASSRLS role (openTestStore), so the policies are evaluated, not
// bypassed. Gated on TEST_DATABASE_URL so the default `go test` (no DB)
// stays hermetic.
//
//	TEST_DATABASE_URL=postgres://user:pass@host:port/db?sslmode=disable go test ./internal/store/postgres/ -run RLS
func TestPostgresTenantIsolationRLS(t *testing.T) {
	st, _ := openTestStore(t)

	// Unique emails per run — accounts.email is unique and rows persist.
	nonce := time.Now().UnixNano()
	a := fmt.Sprintf("alice+%d@example.com", nonce)
	b := fmt.Sprintf("bob+%d@example.com", nonce)
	storetest.AssertTenantIsolation(t, st, a, b)
}

// CRUD parity for the Postgres adapter — same suite as the memory adapter.
// Gated on TEST_DATABASE_URL like the RLS test; the tenant is purged after.
//
//	TEST_DATABASE_URL=postgres://… go test ./internal/store/postgres/ -run Parity
func TestPostgresCRUDParity(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()

	email := fmt.Sprintf("parity+%d@example.com", time.Now().UnixNano())
	storetest.AssertCRUDParity(t, st, email)

	// tidy: the suite's tenant is test data
	if acct, err := st.FindOrCreateAccount(ctx, email); err == nil {
		_ = st.PurgeTenant(ctx, acct.TenantID)
	}
}
