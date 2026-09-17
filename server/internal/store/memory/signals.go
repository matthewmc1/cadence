package memory

import (
	"context"
	"sort"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// The signal surface (0014/0015): sources, signals + bodies + participants,
// origins and outputs. Bodies live in their own map so a list never copies
// them; participants ride on the signal row (they are returned with it).
// Every event goes through emitEntity with sig.Meta() — never the row.

// ---- sources ------------------------------------------------------------------

func (s *Store) GetOrCreateManualSource(_ context.Context, tenantID string) (*domain.Source, error) {
	s.mu.Lock()
	if src := s.manualSource(tenantID); src != nil {
		cp := *src
		s.mu.Unlock()
		return &cp, nil
	}
	src := store.NewManualSource(tenantID, time.Now().UTC())
	cp := src
	s.sources[src.ID] = &cp
	s.mu.Unlock()

	if err := s.emitEntity(tenantID, "", domain.EventSourceCreated, domain.EntitySource, src.ID, src); err != nil {
		return nil, err
	}
	return &src, nil
}

// manualSource finds the tenant's manual source. Caller holds s.mu.
func (s *Store) manualSource(tenantID string) *domain.Source {
	for _, src := range s.sources {
		if src.TenantID == tenantID && src.Kind == domain.SourceManual {
			return src
		}
	}
	return nil
}

func (s *Store) ListSources(_ context.Context, tenantID string) ([]domain.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Source
	for _, src := range s.sources {
		if src.TenantID == tenantID {
			cp := *src
			cp.Normalize()
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) CreateSource(_ context.Context, tenantID, actorID string, in domain.CreateSourceInput) (*domain.Source, error) {
	src, err := store.NewSource(tenantID, in, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if src.OwnerID != nil && !s.hasUser(tenantID, *src.OwnerID) {
		s.mu.Unlock()
		return nil, domain.Invalid("ownerId", "not found in this workspace")
	}
	cp := src
	s.sources[src.ID] = &cp
	s.mu.Unlock()

	if err := s.emitEntity(tenantID, actorID, domain.EventSourceCreated, domain.EntitySource, src.ID, src); err != nil {
		return nil, err
	}
	return &src, nil
}

func (s *Store) UpdateSource(_ context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Source, error) {
	now := time.Now().UTC()

	s.mu.Lock()
	existing, ok := s.sources[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return nil, domain.ErrNotFound
	}
	if expectedVersion != nil && *expectedVersion != existing.Version {
		s.mu.Unlock()
		return nil, domain.ErrConflict
	}
	updated := *existing
	if err := store.ApplySourcePatch(&updated, patch, now); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if updated.OwnerID != nil && !s.hasUser(tenantID, *updated.OwnerID) {
		s.mu.Unlock()
		return nil, domain.Invalid("ownerId", "not found in this workspace")
	}
	updated.Version = existing.Version + 1
	updated.UpdatedAt = now
	cp := updated
	s.sources[id] = &cp
	s.mu.Unlock()

	if err := s.emitEntity(tenantID, actorID, domain.EventSourceUpdated, domain.EntitySource, id, updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// DeleteSource returns how many signals the cascade took with the source (the
// caller audits that count); the manual source is refused.
func (s *Store) DeleteSource(_ context.Context, tenantID, actorID, id string) (int, error) {
	s.mu.Lock()
	existing, ok := s.sources[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return 0, domain.ErrNotFound
	}
	if existing.Kind == domain.SourceManual {
		s.mu.Unlock()
		return 0, store.ErrManualSourceDelete()
	}
	delete(s.sources, id)
	// mirrors the ON DELETE CASCADE from sources: the signals go, silently
	n := 0
	for sid, sig := range s.signals {
		if sig.TenantID == tenantID && sig.SourceID == id {
			s.dropSignal(sid)
			n++
		}
	}
	s.mu.Unlock()

	return n, s.emitEntity(tenantID, actorID, domain.EventSourceDeleted, domain.EntitySource, id, nil)
}

// hasSource / hasSignal report whether a referenced row exists in the tenant.
// Caller holds s.mu.
func (s *Store) hasSource(tenantID, id string) bool {
	src, ok := s.sources[id]
	return ok && src.TenantID == tenantID
}
func (s *Store) hasSignal(tenantID, id string) bool {
	sig, ok := s.signals[id]
	return ok && sig.TenantID == tenantID
}
func (s *Store) hasTask(tenantID, id string) bool {
	t, ok := s.tasks[id]
	return ok && t.TenantID == tenantID
}

// dropSignal removes a signal, its body and (through the row) its
// participants. Origins are NOT touched: they keep their snapshot (0014).
// Caller holds s.mu.
func (s *Store) dropSignal(id string) {
	delete(s.signals, id)
	delete(s.bodies, id)
}

// ---- signals ------------------------------------------------------------------

func (s *Store) CreateSignal(ctx context.Context, tenantID, actorID string, in domain.CreateSignalInput) (*domain.Signal, error) {
	var src domain.Source
	if in.SourceID == nil {
		got, err := s.GetOrCreateManualSource(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		src = *got
	}

	s.mu.Lock()
	if in.SourceID != nil {
		found, ok := s.sources[*in.SourceID]
		if !ok || found.TenantID != tenantID {
			s.mu.Unlock()
			return nil, domain.Invalid("sourceId", "not found in this workspace")
		}
		src = *found
	}
	sig, body, err := store.NewSignal(tenantID, actorID, src, in, time.Now().UTC())
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if sig.ProjectHint != nil && !s.hasProject(tenantID, *sig.ProjectHint) {
		s.mu.Unlock()
		return nil, domain.Invalid("projectHint", "not found in this workspace")
	}
	for _, other := range s.signals {
		if other.TenantID == tenantID && other.SourceID == sig.SourceID && other.ExternalID == sig.ExternalID {
			s.mu.Unlock()
			return nil, domain.Invalid("externalId", "already captured from this source")
		}
	}
	cp := cloneSignal(&sig)
	s.signals[sig.ID] = &cp
	if body != nil {
		b := *body
		s.bodies[sig.ID] = &b
	}
	s.mu.Unlock()

	if err := s.emitEntity(tenantID, actorID, domain.EventSignalCreated, domain.EntitySignal, sig.ID, sig.Meta()); err != nil {
		return nil, err
	}
	return &sig, nil
}

func (s *Store) GetSignal(_ context.Context, tenantID, id string) (*domain.Signal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sig, ok := s.signals[id]
	if !ok || sig.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	cp := cloneSignal(sig)
	return &cp, nil
}

// ListSignals is the keyset-paged read (see ListTasksPaged): filter, keep
// rows older than the cursor, sort newest-first by occurredAt, trim.
func (s *Store) ListSignals(_ context.Context, tenantID string, f store.SignalFilter, p store.Page) (store.PageResult[domain.Signal], error) {
	cur, err := store.DecodeCursor(p.Cursor)
	if err != nil {
		return store.PageResult[domain.Signal]{}, err
	}
	limit := p.EffectiveLimit()

	s.mu.RLock()
	defer s.mu.RUnlock()
	var rows []domain.Signal
	for _, sig := range s.signals {
		if matchSignal(sig, tenantID, f) && cur.Admits(sig.OccurredAt, sig.ID) {
			rows = append(rows, cloneSignal(sig))
		}
	}
	store.SortNewestFirst(rows, store.SignalKey)
	if len(rows) > limit+1 {
		rows = rows[:limit+1]
	}
	return store.Paginate(rows, limit, store.SignalKey), nil
}

func (s *Store) CountSignals(_ context.Context, tenantID string, f store.SignalFilter) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, sig := range s.signals {
		if matchSignal(sig, tenantID, f) {
			n++
		}
	}
	return n, nil
}

// matchSignal applies the tenant fence and the SignalFilter to one row.
func matchSignal(sig *domain.Signal, tenantID string, f store.SignalFilter) bool {
	if sig.TenantID != tenantID {
		return false
	}
	if f.Stage != nil && sig.Stage != *f.Stage {
		return false
	}
	if f.SourceID != nil && sig.SourceID != *f.SourceID {
		return false
	}
	if f.ProjectHint != nil && (sig.ProjectHint == nil || *sig.ProjectHint != *f.ProjectHint) {
		return false
	}
	if f.OccurredAfter != nil && !sig.OccurredAt.After(*f.OccurredAfter) {
		return false
	}
	return true
}

func (s *Store) UpdateSignalDisposition(_ context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Signal, error) {
	now := time.Now().UTC()

	s.mu.Lock()
	existing, ok := s.signals[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return nil, domain.ErrNotFound
	}
	if expectedVersion != nil && *expectedVersion != existing.Version {
		s.mu.Unlock()
		return nil, domain.ErrConflict
	}
	updated := cloneSignal(existing)
	if err := store.ApplySignalPatch(&updated, patch, now); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if updated.ProjectHint != nil && !s.hasProject(tenantID, *updated.ProjectHint) {
		s.mu.Unlock()
		return nil, domain.Invalid("projectHint", "not found in this workspace")
	}
	updated.Version = existing.Version + 1
	updated.UpdatedAt = now
	cp := cloneSignal(&updated)
	s.signals[id] = &cp
	s.mu.Unlock()

	if err := s.emitEntity(tenantID, actorID, domain.EventSignalUpdated, domain.EntitySignal, id, updated.Meta()); err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *Store) DeleteSignal(_ context.Context, tenantID, actorID, id string) error {
	s.mu.Lock()
	existing, ok := s.signals[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return domain.ErrNotFound
	}
	s.dropSignal(id)
	s.mu.Unlock()

	return s.emitEntity(tenantID, actorID, domain.EventSignalDeleted, domain.EntitySignal, id, nil)
}

// PruneSignals is the retention sweep — the memory twin of
// postgres.PruneSignals: every signal whose retention_until has passed goes,
// with its body and participants; origins keep their snapshot. No event is
// emitted (housekeeping, not a user action) but each tenant that lost rows
// gets one audit row saying how many — a sweep nobody can see afterwards is
// exactly the deletion the log exists for. It runs from the ticker started in
// New and can be called directly (the parity suite does).
func (s *Store) PruneSignals(ctx context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	pruned := map[string]int{}
	for id, sig := range s.signals {
		if sig.RetentionUntil != nil && sig.RetentionUntil.Before(now) {
			s.dropSignal(id)
			pruned[sig.TenantID]++
		}
	}
	s.mu.Unlock() // AppendAudit takes the same lock

	var total int64
	for tenantID, n := range pruned {
		total += int64(n)
		if _, err := s.AppendAudit(ctx, tenantID, store.PruneAuditEntry(n)); err != nil {
			return total, err
		}
	}
	return total, nil
}

// signalPruneEvery matches the Postgres adapter's housekeeping cadence.
const signalPruneEvery = time.Hour

// pruneLoop sweeps expired signals hourly until Close.
func (s *Store) pruneLoop() {
	tick := time.NewTicker(signalPruneEvery)
	defer tick.Stop()
	for {
		select {
		case <-s.stopPrune:
			return
		case now := <-tick.C:
			_, _ = s.PruneSignals(context.Background(), now)
		}
	}
}

func (s *Store) GetSignalBody(_ context.Context, tenantID, id string) (*domain.SignalBody, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sig, ok := s.signals[id]
	if !ok || sig.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	if b, ok := s.bodies[id]; ok {
		cp := *b
		return &cp, nil
	}
	return &domain.SignalBody{TenantID: tenantID, SignalID: id, FetchedAt: sig.CreatedAt}, nil
}

// cloneSignal copies a row including its participants slice, so a returned
// signal can never alias the stored one.
func cloneSignal(sig *domain.Signal) domain.Signal {
	cp := *sig
	cp.Participants = append([]domain.Participant{}, sig.Participants...)
	cp.Extracted = append([]byte(nil), sig.Extracted...)
	cp.Normalize()
	return cp
}

// detachSignalsFromProject mirrors ON DELETE SET NULL (project_hint) from
// projects. updated_at is bumped like the trigger would; version is left
// as-is and no event fires (parity with postgres). Caller holds s.mu.
func (s *Store) detachSignalsFromProject(tenantID, projectID string, now time.Time) {
	for _, sig := range s.signals {
		if sig.TenantID == tenantID && sig.ProjectHint != nil && *sig.ProjectHint == projectID {
			sig.ProjectHint = nil
			sig.UpdatedAt = now
		}
	}
}

// ---- origins ------------------------------------------------------------------

func (s *Store) AttachSignalToTask(_ context.Context, tenantID, actorID, taskID, signalID string) (*domain.Origin, error) {
	now := time.Now().UTC()

	s.mu.Lock()
	if !s.hasTask(tenantID, taskID) {
		s.mu.Unlock()
		return nil, domain.ErrNotFound
	}
	sig, ok := s.signals[signalID]
	if !ok || sig.TenantID != tenantID {
		s.mu.Unlock()
		return nil, domain.Invalid("signalId", "not found in this workspace")
	}
	if existing, ok := s.origins[taskID][signalID]; ok {
		cp := *existing
		s.mu.Unlock()
		return &cp, nil // idempotent: already attached, nothing to emit
	}
	origin := store.NewOrigin(taskID, *sig, now)
	if s.origins[taskID] == nil {
		s.origins[taskID] = map[string]*domain.Origin{}
	}
	cp := origin
	s.origins[taskID][signalID] = &cp
	// an inbox/snoozed signal is now disposed: attached
	var disposed *domain.Signal
	if sig.Stage == domain.SignalInbox || sig.Stage == domain.SignalSnoozed {
		updated := cloneSignal(sig)
		updated.Stage, updated.SnoozedUntil = domain.SignalAttached, nil
		at := now
		updated.DispositionAt = &at
		updated.Version++
		updated.UpdatedAt = now
		u := cloneSignal(&updated)
		s.signals[signalID] = &u
		disposed = &updated
	}
	s.mu.Unlock()

	if disposed != nil {
		if err := s.emitEntity(tenantID, actorID, domain.EventSignalUpdated, domain.EntitySignal, signalID, disposed.Meta()); err != nil {
			return nil, err
		}
	}
	if err := s.emitEntity(tenantID, actorID, domain.EventOriginAttached, domain.EntityOrigin, taskID, origin); err != nil {
		return nil, err
	}
	return &origin, nil
}

func (s *Store) DetachSignalFromTask(_ context.Context, tenantID, actorID, taskID, signalID string) error {
	s.mu.Lock()
	if !s.hasTask(tenantID, taskID) {
		s.mu.Unlock()
		return domain.ErrNotFound
	}
	if _, ok := s.origins[taskID][signalID]; !ok {
		s.mu.Unlock()
		return domain.ErrNotFound
	}
	delete(s.origins[taskID], signalID)
	s.mu.Unlock()

	return s.emitEntity(tenantID, actorID, domain.EventOriginDetached, domain.EntityOrigin, taskID,
		map[string]string{"taskId": taskID, "signalId": signalID})
}

func (s *Store) ListTaskOrigins(_ context.Context, tenantID, taskID string) ([]domain.Origin, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hasTask(tenantID, taskID) {
		return nil, domain.ErrNotFound
	}
	out := []domain.Origin{}
	for _, o := range s.origins[taskID] {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].SignalID < out[j].SignalID
	})
	return out, nil
}

// withCounts fills a task's derived provenance counts (domain.Task's
// OriginCount/OutputCount) — the Postgres adapter gets them from the two
// sub-selects in taskCols, this one counts the maps. Caller holds s.mu.
func (s *Store) withCounts(t domain.Task) domain.Task {
	t.OriginCount = len(s.origins[t.ID])
	t.OutputCount = 0
	for _, o := range s.outputs {
		if o.TenantID == t.TenantID && o.TaskID == t.ID {
			t.OutputCount++
		}
	}
	return t
}

// cascadeTaskChildren mirrors the ON DELETE CASCADE from tasks: origins and
// outputs go with the work item, silently. Caller holds s.mu.
func (s *Store) cascadeTaskChildren(taskID string) {
	delete(s.origins, taskID)
	for id, o := range s.outputs {
		if o.TaskID == taskID {
			delete(s.outputs, id)
		}
	}
}

// ---- outputs ------------------------------------------------------------------

func (s *Store) CreateOutput(_ context.Context, tenantID, actorID, taskID string, in domain.CreateOutputInput) (*domain.Output, error) {
	o, err := store.NewOutput(tenantID, actorID, taskID, in, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if !s.hasTask(tenantID, taskID) {
		s.mu.Unlock()
		return nil, domain.ErrNotFound
	}
	cp := o
	s.outputs[o.ID] = &cp
	s.mu.Unlock()

	if err := s.emitEntity(tenantID, actorID, domain.EventOutputCreated, domain.EntityOutput, o.ID, o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) ListOutputs(_ context.Context, tenantID, taskID string) ([]domain.Output, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hasTask(tenantID, taskID) {
		return nil, domain.ErrNotFound
	}
	out := []domain.Output{}
	for _, o := range s.outputs {
		if o.TenantID == tenantID && o.TaskID == taskID {
			out = append(out, *o)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Store) DeleteOutput(_ context.Context, tenantID, actorID, taskID, id string) error {
	s.mu.Lock()
	o, ok := s.outputs[id]
	if !ok || o.TenantID != tenantID || o.TaskID != taskID {
		s.mu.Unlock()
		return domain.ErrNotFound
	}
	delete(s.outputs, id)
	s.mu.Unlock()

	return s.emitEntity(tenantID, actorID, domain.EventOutputDeleted, domain.EntityOutput, id, nil)
}
