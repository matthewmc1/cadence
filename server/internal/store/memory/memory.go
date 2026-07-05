// Package memory is the default Store adapter: a goroutine-safe in-memory
// implementation with an in-process event broker. It lets Cadence run with
// zero external services while implementing the exact same contract as the
// Postgres adapter.
package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

type Store struct {
	mu       sync.RWMutex
	tenants  map[string]domain.Tenant
	users    map[string]domain.User
	projects map[string]*domain.Project
	tasks    map[string]*domain.Task
	broker   *broker

	// auth
	accounts    map[string]domain.Account  // by email
	loginTokens map[string]loginToken      // by token hash
	sessions    map[string]domain.Session  // by token hash
	apiTokens   map[string]domain.APIToken // by token hash
}

type loginToken struct {
	email     string
	expiresAt time.Time
	consumed  bool
}

func New() *Store {
	return &Store{
		tenants:     make(map[string]domain.Tenant),
		users:       make(map[string]domain.User),
		projects:    make(map[string]*domain.Project),
		tasks:       make(map[string]*domain.Task),
		broker:      newBroker(),
		accounts:    make(map[string]domain.Account),
		loginTokens: make(map[string]loginToken),
		sessions:    make(map[string]domain.Session),
		apiTokens:   make(map[string]domain.APIToken),
	}
}

// ---- raw fixture loaders (used by the seed package; emit no events) ----

func (s *Store) AddTenant(t domain.Tenant) { s.mu.Lock(); s.tenants[t.ID] = t; s.mu.Unlock() }
func (s *Store) AddUser(u domain.User)     { s.mu.Lock(); s.users[u.ID] = u; s.mu.Unlock() }
func (s *Store) AddProject(p domain.Project) {
	s.mu.Lock()
	cp := p
	s.projects[p.ID] = &cp
	s.mu.Unlock()
}
func (s *Store) AddTask(t domain.Task) {
	t.Normalize()
	s.mu.Lock()
	cp := t
	s.tasks[t.ID] = &cp
	s.mu.Unlock()
}

// ---- reads ----

func (s *Store) Bootstrap(_ context.Context, tenantID, userID string) (*domain.Bootstrap, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tenant, ok := s.tenants[tenantID]
	if !ok {
		return nil, domain.ErrNotFound
	}

	user, ok := s.users[userID]
	if !ok || user.TenantID != tenantID {
		// fall back to any member of the tenant
		for _, u := range s.users {
			if u.TenantID == tenantID {
				user = u
				ok = true
				break
			}
		}
		if !ok {
			return nil, domain.ErrNotFound
		}
	}

	var projects []domain.Project
	for _, p := range s.projects {
		if p.TenantID == tenantID {
			projects = append(projects, cloneProject(p))
		}
	}
	sortProjects(projects)

	var tasks []domain.Task
	for _, t := range s.tasks {
		if t.TenantID == tenantID {
			tasks = append(tasks, *t)
		}
	}
	sortTasks(tasks)

	return &domain.Bootstrap{
		Tenant:   tenant,
		User:     user,
		Projects: projects,
		Tasks:    tasks,
		ServerAt: time.Now().UTC(),
	}, nil
}

func (s *Store) ListTasks(_ context.Context, tenantID string, f store.TaskFilter) ([]domain.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Task
	for _, t := range s.tasks {
		if t.TenantID != tenantID {
			continue
		}
		if f.Status != nil && t.Status != *f.Status {
			continue
		}
		if f.ProjectID != nil && (t.ProjectID == nil || *t.ProjectID != *f.ProjectID) {
			continue
		}
		out = append(out, *t)
	}
	sortTasks(out)
	return out, nil
}

func (s *Store) GetTask(_ context.Context, tenantID, id string) (*domain.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok || t.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *Store) ListProjects(_ context.Context, tenantID string) ([]domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Project
	for _, p := range s.projects {
		if p.TenantID == tenantID {
			out = append(out, cloneProject(p))
		}
	}
	sortProjects(out)
	return out, nil
}

// ---- task writes ----

func (s *Store) CreateTask(_ context.Context, tenantID, actorID string, in domain.CreateTaskInput) (*domain.Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, domain.Invalid("title", "is required")
	}

	kind, effort := domain.Infer(title)
	if in.Kind != nil {
		if !in.Kind.Valid() {
			return nil, domain.Invalid("kind", "is invalid")
		}
		kind = *in.Kind
	}
	if in.EffortMinutes != nil {
		effort = *in.EffortMinutes
	}
	status := domain.StatusBacklog
	if in.Status != nil {
		if !in.Status.Valid() {
			return nil, domain.Invalid("status", "is invalid")
		}
		status = *in.Status
	}

	now := time.Now().UTC()
	t := domain.Task{
		ID:            domain.NewID(),
		TenantID:      tenantID,
		ProjectID:     in.ProjectID,
		Title:         title,
		Kind:          kind,
		Status:        status,
		EffortMinutes: effort,
		Note:          deref(in.Note),
		Reflection:    deref(in.Reflection),
		Place:         in.Place,
		ScheduledAt:   in.ScheduledAt,
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if in.Urgent != nil {
		t.Urgent = *in.Urgent
	}
	if in.Important != nil {
		t.Important = *in.Important
	}
	if in.Position != nil {
		t.Position = *in.Position
	}
	if in.Deadline != nil {
		t.Deadline = in.Deadline
	}
	if in.Recurrence != nil {
		t.Recurrence = *in.Recurrence
	}
	t.Links = in.Links
	t.Subtasks = in.Subtasks
	t.Assignees = in.Assignees
	if status == domain.StatusDone {
		t.DoneAt = &now
	}
	t.Normalize()

	s.mu.Lock()
	cp := t
	s.tasks[t.ID] = &cp
	s.mu.Unlock()

	s.broker.publish(taskEvent(domain.EventTaskCreated, actorID, &t))
	return &t, nil
}

func (s *Store) UpdateTask(_ context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Task, error) {
	now := time.Now().UTC()

	s.mu.Lock()
	existing, ok := s.tasks[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return nil, domain.ErrNotFound
	}
	if expectedVersion != nil && *expectedVersion != existing.Version {
		s.mu.Unlock()
		return nil, domain.ErrConflict
	}
	updated := *existing
	if err := store.ApplyTaskPatch(&updated, patch, now); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	updated.Version = existing.Version + 1
	updated.UpdatedAt = now
	s.tasks[id] = &updated
	s.mu.Unlock()

	s.broker.publish(taskEvent(domain.EventTaskUpdated, actorID, &updated))
	out := updated
	return &out, nil
}

func (s *Store) DeleteTask(_ context.Context, tenantID, actorID, id string) error {
	s.mu.Lock()
	existing, ok := s.tasks[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return domain.ErrNotFound
	}
	delete(s.tasks, id)
	s.mu.Unlock()

	s.broker.publish(domain.Event{
		ID: domain.NewID(), Type: domain.EventTaskDeleted, TenantID: tenantID,
		ActorID: actorID, EntityID: id, At: time.Now().UTC(),
	})
	return nil
}

// ---- project writes ----

func (s *Store) CreateProject(_ context.Context, tenantID, actorID string, in domain.CreateProjectInput) (*domain.Project, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, domain.Invalid("name", "is required")
	}
	now := time.Now().UTC()
	p := domain.Project{
		ID: domain.NewID(), TenantID: tenantID, Name: name,
		Subtitle: deref(in.Subtitle), Due: in.Due, Color: orDefault(in.Color, "#C2743D"),
		Members: []domain.ProjectMember{}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}

	s.mu.Lock()
	cp := p
	s.projects[p.ID] = &cp
	s.mu.Unlock()

	s.broker.publish(projectEvent(domain.EventProjectCreated, actorID, &p))
	return &p, nil
}

func (s *Store) UpdateProject(_ context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Project, error) {
	now := time.Now().UTC()

	s.mu.Lock()
	existing, ok := s.projects[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return nil, domain.ErrNotFound
	}
	if expectedVersion != nil && *expectedVersion != existing.Version {
		s.mu.Unlock()
		return nil, domain.ErrConflict
	}
	updated := cloneProject(existing)
	if name, ok := patch["name"].(string); ok && strings.TrimSpace(name) != "" {
		updated.Name = strings.TrimSpace(name)
	}
	if v, ok := patch["subtitle"]; ok {
		updated.Subtitle, _ = v.(string)
	}
	if v, ok := patch["due"]; ok {
		if v == nil {
			updated.Due = nil
		} else if s2, ok := v.(string); ok {
			updated.Due = &s2
		}
	}
	if c, ok := patch["color"].(string); ok {
		updated.Color = c
	}
	updated.Version = existing.Version + 1
	updated.UpdatedAt = now
	cp := updated
	s.projects[id] = &cp
	s.mu.Unlock()

	s.broker.publish(projectEvent(domain.EventProjectUpdated, actorID, &updated))
	return &updated, nil
}

func (s *Store) DeleteProject(_ context.Context, tenantID, actorID, id string) error {
	s.mu.Lock()
	existing, ok := s.projects[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return domain.ErrNotFound
	}
	delete(s.projects, id)
	// detach tasks from the deleted project
	for _, t := range s.tasks {
		if t.ProjectID != nil && *t.ProjectID == id {
			t.ProjectID = nil
		}
	}
	s.mu.Unlock()

	s.broker.publish(domain.Event{
		ID: domain.NewID(), Type: domain.EventProjectDeleted, TenantID: tenantID,
		ActorID: actorID, EntityID: id, At: time.Now().UTC(),
	})
	return nil
}

// ---- realtime / lifecycle ----

func (s *Store) Subscribe(tenantID string) (<-chan domain.Event, func()) {
	return s.broker.subscribe(tenantID)
}

func (s *Store) Ping(context.Context) error { return nil }

func (s *Store) Close() error {
	s.broker.close()
	return nil
}

// ---- helpers ----

func taskEvent(t domain.EventType, actorID string, task *domain.Task) domain.Event {
	cp := *task
	return domain.Event{
		ID: domain.NewID(), Type: t, TenantID: task.TenantID, ActorID: actorID,
		Task: &cp, EntityID: task.ID, At: time.Now().UTC(),
	}
}

func projectEvent(t domain.EventType, actorID string, p *domain.Project) domain.Event {
	cp := cloneProject(p)
	return domain.Event{
		ID: domain.NewID(), Type: t, TenantID: p.TenantID, ActorID: actorID,
		Project: &cp, EntityID: p.ID, At: time.Now().UTC(),
	}
}

func cloneProject(p *domain.Project) domain.Project {
	cp := *p
	// start from a non-nil slice so an empty members list stays [] (not null) in JSON
	cp.Members = append([]domain.ProjectMember{}, p.Members...)
	return cp
}

func sortTasks(ts []domain.Task) {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].Position != ts[j].Position {
			return ts[i].Position < ts[j].Position
		}
		return ts[i].CreatedAt.Before(ts[j].CreatedAt)
	})
}

func sortProjects(ps []domain.Project) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].CreatedAt.Before(ps[j].CreatedAt) })
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orDefault(s *string, def string) string {
	if s == nil || *s == "" {
		return def
	}
	return *s
}
