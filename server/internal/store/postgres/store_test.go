package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/cadence/server/internal/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The Postgres suites must run the way production does, or they prove the
// wrong thing: migrations as the owner/superuser (DDL), the Store as a
// NOSUPERUSER NOBYPASSRLS role. A superuser — which is what the compose
// `cadence` role is — bypasses every policy, so under it the RLS tests only
// ever exercised the explicit tenant_id predicates and a broken CREATE POLICY
// would sail through. openTestStore therefore migrates over TEST_DATABASE_URL,
// creates (once) a restricted role with a per-process random password, grants
// it DML the way server/scripts/app-role.sql does, and opens the Store as
// that role — then checks pg_roles to be sure it is actually restricted.
//
// TEST_DATABASE_URL still names the admin connection, so `make test-db` is
// unchanged; raw row counts in the suites keep using it.

const testAppRole = "cadence_test_app"

var (
	testRoleOnce sync.Once
	testRolePass string
	testRoleErr  error
)

// openTestStore returns the Store opened as the restricted role, plus the
// admin URL for raw queries. It skips when TEST_DATABASE_URL is unset.
func openTestStore(t *testing.T) (*postgres.Store, string) {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the Postgres suites")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	defer admin.Close()
	if _, err := postgres.Migrate(ctx, admin); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	testRoleOnce.Do(func() {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			testRoleErr = err
			return
		}
		testRolePass = hex.EncodeToString(b[:]) // hex: safe to inline in the literal below
		for _, q := range []string{
			`DO $$ BEGIN
				IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '` + testAppRole + `') THEN
					CREATE ROLE ` + testAppRole + ` LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
				END IF;
			END $$`,
			`ALTER ROLE ` + testAppRole + ` LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '` + testRolePass + `'`,
			`GRANT USAGE ON SCHEMA public TO ` + testAppRole,
			`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ` + testAppRole,
		} {
			if _, err := admin.Exec(ctx, q); err != nil {
				testRoleErr = fmt.Errorf("%s: %w", q, err)
				return
			}
		}
	})
	if testRoleErr != nil {
		t.Fatalf("restricted test role: %v", testRoleErr)
	}
	var bypass bool
	if err := admin.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = $1`, testAppRole).Scan(&bypass); err != nil {
		t.Fatalf("pg_roles: %v", err)
	}
	if bypass {
		t.Fatalf("%s can bypass RLS — the suite would prove nothing about the policies", testAppRole)
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL: %v", err)
	}
	u.User = url.UserPassword(testAppRole, testRolePass)
	st, err := postgres.Open(ctx, postgres.Config{URL: u.String(), AutoMigrate: false})
	if err != nil {
		t.Fatalf("open postgres as %s: %v", testAppRole, err)
	}
	t.Cleanup(func() { st.Close() })
	return st, adminURL
}
