package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cadence/server/internal/store/postgres"
	"github.com/cadence/server/internal/store/storetest"
)

// Tenant isolation for the Postgres adapter — this is the real Row-Level
// Security proof: tenant B, running under app.tenant_id = B, cannot reach
// tenant A's rows even by their exact id. Gated on TEST_DATABASE_URL so the
// default `go test` (no DB) stays hermetic.
//
//	TEST_DATABASE_URL=postgres://user:pass@host:port/db?sslmode=disable go test ./internal/store/postgres/ -run RLS
func TestPostgresTenantIsolationRLS(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run the Postgres RLS isolation test")
	}
	ctx := context.Background()
	st, err := postgres.Open(ctx, postgres.Config{URL: url, AutoMigrate: true})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer st.Close()

	// Unique emails per run — accounts.email is unique and rows persist.
	nonce := time.Now().UnixNano()
	a := fmt.Sprintf("alice+%d@example.com", nonce)
	b := fmt.Sprintf("bob+%d@example.com", nonce)
	storetest.AssertTenantIsolation(t, st, a, b)
}
