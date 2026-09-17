package memory_test

import (
	"testing"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store/memory"
	"github.com/cadence/server/internal/store/storetest"
)

// Tenant purge for the in-memory adapter: every map is walked raw (CountRows)
// so the assertion does not trust the Store API it is checking.
func TestMemoryTenantPurge(t *testing.T) {
	st := memory.New()
	count := func(t *testing.T, table string, acct domain.Account) int {
		t.Helper()
		n, err := st.CountRows(table, acct.TenantID, acct.Email)
		if err != nil {
			t.Fatalf("CountRows(%s): %v", table, err)
		}
		return n
	}
	storetest.AssertTenantPurge(t, st, "alice@example.com", "bob@example.com", count, false)
}
