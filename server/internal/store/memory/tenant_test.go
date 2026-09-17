package memory_test

import (
	"testing"

	"github.com/cadence/server/internal/store/memory"
	"github.com/cadence/server/internal/store/storetest"
)

// Tenant isolation for the in-memory adapter (per-record tenant filtering).
func TestMemoryTenantIsolation(t *testing.T) {
	storetest.AssertTenantIsolation(t, memory.New(), "alice@example.com", "bob@example.com")
}

// CRUD parity for the in-memory adapter: the same walk the Postgres adapter
// runs, so the two cannot drift.
func TestMemoryCRUDParity(t *testing.T) {
	storetest.AssertCRUDParity(t, memory.New(), "parity@example.com")
}
