package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// AddRequirement is the raw fixture loader (emits no event).
func (s *Store) AddRequirement(r domain.Requirement) {
	s.mu.Lock()
	cp := r
	s.requirements[r.ID] = &cp
	s.mu.Unlock()
}

func (s *Store) ListRequirements(_ context.Context, tenantID string, f store.RequirementFilter) ([]domain.Requirement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selectRequirements(tenantID, f), nil
}

// selectRequirements applies the tenant fence and filter. Caller holds s.mu.
func (s *Store) selectRequirements(tenantID string, f store.RequirementFilter) []domain.Requirement {
	var out []domain.Requirement
	for _, r := range s.requirements {
		if r.TenantID != tenantID {
			continue
		}
		if f.ProjectID != nil && r.ProjectID != *f.ProjectID {
			continue
		}
		out = append(out, *r)
	}
	sortRequirements(out)
	return out
}

func (s *Store) CreateRequirement(_ context.Context, tenantID, actorID string, in domain.CreateRequirementInput) (*domain.Requirement, error) {
	r, err := newRequirement(tenantID, in, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if !s.hasProject(tenantID, r.ProjectID) {
		s.mu.Unlock()
		return nil, domain.Invalid("projectId", "not found in this workspace")
	}
	cp := r
	s.requirements[r.ID] = &cp
	s.mu.Unlock()

	if err := s.emitEntity(tenantID, actorID, domain.EventRequirementCreated, domain.EntityRequirement, r.ID, r); err != nil {
		return nil, err
	}
	return &r, nil
}

// newRequirement validates a create input into a row. Shared verbatim with
// the postgres adapter's intent (same defaults, same rejections) so parity
// holds; it lives here because memory is the reference implementation.
func newRequirement(tenantID string, in domain.CreateRequirementInput, now time.Time) (domain.Requirement, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.Requirement{}, domain.Invalid("title", "is required")
	}
	if len(title) > store.MaxTitleLen {
		return domain.Requirement{}, domain.Invalid("title", "is too long")
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		return domain.Requirement{}, domain.Invalid("projectId", "is required")
	}
	weight := domain.RequirementWeightDefault
	if in.Weight != nil {
		if *in.Weight < domain.RequirementWeightMin || *in.Weight > domain.RequirementWeightMax {
			return domain.Requirement{}, domain.Invalid("weight", "must be a whole number from 1 to 5")
		}
		weight = *in.Weight
	}
	status := orDefault(in.Status, domain.RequirementOpen)
	if !domain.ValidRequirementStatus(status) {
		return domain.Requirement{}, domain.Invalid("status", "must be one of open, met, dropped")
	}
	r := domain.Requirement{
		ID: domain.NewID(), TenantID: tenantID, ProjectID: in.ProjectID, Title: title,
		Description: deref(in.Description), Weight: weight, Acceptance: deref(in.Acceptance),
		Status: status, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if in.Position != nil {
		r.Position = *in.Position
	}
	return r, nil
}

func (s *Store) UpdateRequirement(_ context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Requirement, error) {
	now := time.Now().UTC()

	s.mu.Lock()
	existing, ok := s.requirements[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return nil, domain.ErrNotFound
	}
	if expectedVersion != nil && *expectedVersion != existing.Version {
		s.mu.Unlock()
		return nil, domain.ErrConflict
	}
	updated := *existing
	if err := store.ApplyRequirementPatch(&updated, patch, now); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !s.hasProject(tenantID, updated.ProjectID) {
		s.mu.Unlock()
		return nil, domain.Invalid("projectId", "not found in this workspace")
	}
	updated.Version = existing.Version + 1
	updated.UpdatedAt = now
	cp := updated
	s.requirements[id] = &cp
	s.mu.Unlock()

	if err := s.emitEntity(tenantID, actorID, domain.EventRequirementUpdated, domain.EntityRequirement, id, updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *Store) DeleteRequirement(_ context.Context, tenantID, actorID, id string) error {
	s.mu.Lock()
	existing, ok := s.requirements[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return domain.ErrNotFound
	}
	delete(s.requirements, id)
	s.detachTasksFromRequirement(tenantID, id, time.Now().UTC())
	s.mu.Unlock()

	return s.emitEntity(tenantID, actorID, domain.EventRequirementDeleted, domain.EntityRequirement, id, nil)
}

// cascadeRequirements mirrors the postgres ON DELETE CASCADE from projects:
// a project's requirements go with it. Caller holds s.mu. No events fire for
// the cascaded rows (parity with postgres, where the FK does the delete).
func (s *Store) cascadeRequirements(tenantID, projectID string) {
	now := time.Now().UTC()
	for id, r := range s.requirements {
		if r.TenantID == tenantID && r.ProjectID == projectID {
			delete(s.requirements, id)
			s.detachTasksFromRequirement(tenantID, id, now) // and their tasks let go (SET NULL)
		}
	}
}

func sortRequirements(rs []domain.Requirement) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Position != rs[j].Position {
			return rs[i].Position < rs[j].Position
		}
		return rs[i].CreatedAt.Before(rs[j].CreatedAt)
	})
}
