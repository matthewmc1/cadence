package memory

import (
	"context"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// AppendAudit files one immutable row under the tenant. Nothing in this
// adapter ever mutates or removes an entry except PurgeTenant, matching the
// postgres append-only trigger. No event is emitted (see store.Store).
func (s *Store) AppendAudit(_ context.Context, tenantID string, e domain.AuditEntry) (*domain.AuditEntry, error) {
	assigned := e.At.IsZero() // the store, not the caller, is picking `at`
	if err := store.NormalizeAuditEntry(tenantID, &e, time.Now().UTC()); err != nil {
		return nil, err
	}
	// Detail is a []byte the caller may keep a handle on; copy so the stored
	// row is immutable in practice, not just by convention.
	e.Detail = append([]byte(nil), e.Detail...)

	s.mu.Lock()
	// Strictly monotonic `at` (see Store.lastAuditAt): only a store-assigned
	// timestamp is nudged — a caller-supplied At is kept as given.
	if assigned && !e.At.After(s.lastAuditAt) {
		e.At = s.lastAuditAt.Add(time.Microsecond)
	}
	if e.At.After(s.lastAuditAt) {
		s.lastAuditAt = e.At
	}
	s.audit[tenantID] = append(s.audit[tenantID], e)
	s.mu.Unlock()
	out := e
	return &out, nil
}

// ListAudit is the keyset-paged read: rows older than the cursor, newest
// first, limit+1 fetched so store.Paginate can set the next cursor.
func (s *Store) ListAudit(_ context.Context, tenantID string, p store.Page) (store.PageResult[domain.AuditEntry], error) {
	cur, err := store.DecodeCursor(p.Cursor)
	if err != nil {
		return store.PageResult[domain.AuditEntry]{}, err
	}
	limit := p.EffectiveLimit()

	s.mu.RLock()
	defer s.mu.RUnlock()
	var rows []domain.AuditEntry
	for _, e := range s.audit[tenantID] {
		if cur.Admits(e.At, e.ID) {
			rows = append(rows, e)
		}
	}
	store.SortNewestFirst(rows, store.AuditKey)
	if len(rows) > limit+1 {
		rows = rows[:limit+1]
	}
	return store.Paginate(rows, limit, store.AuditKey), nil
}
