package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/cadence/server/internal/domain"
)

// NormalizeAuditEntry fills in and validates an entry before either adapter
// stores it, so the two agree on exactly what an appended row looks like:
//
//   - ID and At are assigned when zero (At is µs-truncated so the memory
//     adapter returns what Postgres would);
//   - TenantID is forced to the caller's tenant — an entry can never be filed
//     under someone else's log, whatever the caller put in the struct;
//   - Kind is required; empty pointer-fields are normalised to nil;
//   - Detail defaults to {} and must be valid JSON of at most
//     domain.MaxAuditDetailBytes.
//
// Validation failures are ValidationErrors so a misuse by a producer surfaces
// as a 400 in a test rather than a 500 in production.
func NormalizeAuditEntry(tenantID string, e *domain.AuditEntry, now time.Time) error {
	e.TenantID = tenantID
	if e.ID == "" {
		e.ID = domain.NewID()
	}
	if e.At.IsZero() {
		e.At = now
	}
	e.At = e.At.UTC().Truncate(time.Microsecond)
	e.Kind = strings.TrimSpace(e.Kind)
	if e.Kind == "" {
		return domain.Invalid("kind", "is required")
	}
	e.ActorID = nilIfEmpty(e.ActorID)
	e.EntityType = nilIfEmpty(e.EntityType)
	e.EntityID = nilIfEmpty(e.EntityID)
	e.IPHash = nilIfEmpty(e.IPHash)
	if len(e.Detail) == 0 {
		e.Detail = json.RawMessage(`{}`)
	}
	if len(e.Detail) > domain.MaxAuditDetailBytes {
		return domain.Invalid("detail", "is too large")
	}
	if !json.Valid(e.Detail) {
		return domain.Invalid("detail", "must be valid JSON")
	}
	return nil
}

// AuditKey is the paging key for the audit log: at, id.
func AuditKey(e domain.AuditEntry) (time.Time, string) { return e.At, e.ID }

func nilIfEmpty(s *string) *string {
	if s != nil && *s == "" {
		return nil
	}
	return s
}
