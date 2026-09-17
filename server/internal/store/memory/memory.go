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
	clients  map[string]*domain.Client
	projects map[string]*domain.Project
	tasks    map[string]*domain.Task
	// requirements by id (requirements.go); audit rows by tenant, append-only
	// (audit.go) — only PurgeTenant ever removes from it.
	requirements map[string]*domain.Requirement
	audit        map[string][]domain.AuditEntry
	// lastAuditAt is the most recent `at` handed to an audit row. Appends can
	// land in the same microsecond in-process (a Postgres round-trip never
	// does), which would leave the (at, id) paging key to the random bits of
	// two v7 ids; nudging a tie forward keeps list order = append order.
	lastAuditAt time.Time
	// signal surface (signals.go): sources and signals by id, bodies by
	// signal id (kept apart so a list never copies them), origins by task id
	// then signal id, outputs by id.
	sources map[string]*domain.Source
	signals map[string]*domain.Signal
	bodies  map[string]*domain.SignalBody
	origins map[string]map[string]*domain.Origin
	outputs map[string]*domain.Output
	broker  *broker
	// retention sweep (signals.go pruneLoop): closed once by Close
	stopPrune chan struct{}
	stopOnce  sync.Once

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
	s := &Store{
		tenants:      make(map[string]domain.Tenant),
		users:        make(map[string]domain.User),
		clients:      make(map[string]*domain.Client),
		projects:     make(map[string]*domain.Project),
		tasks:        make(map[string]*domain.Task),
		requirements: make(map[string]*domain.Requirement),
		audit:        make(map[string][]domain.AuditEntry),
		sources:      make(map[string]*domain.Source),
		signals:      make(map[string]*domain.Signal),
		bodies:       make(map[string]*domain.SignalBody),
		origins:      make(map[string]map[string]*domain.Origin),
		outputs:      make(map[string]*domain.Output),
		broker:       newBroker(),
		accounts:     make(map[string]domain.Account),
		loginTokens:  make(map[string]loginToken),
		sessions:     make(map[string]domain.Session),
		apiTokens:    make(map[string]domain.APIToken),
		stopPrune:    make(chan struct{}),
	}
	go s.pruneLoop()
	return s
}

// ---- raw fixture loaders (used by the seed package; emit no events) ----

func (s *Store) AddTenant(t domain.Tenant) { s.mu.Lock(); s.tenants[t.ID] = t; s.mu.Unlock() }
func (s *Store) AddUser(u domain.User)     { s.mu.Lock(); s.users[u.ID] = u; s.mu.Unlock() }
func (s *Store) AddClient(c domain.Client) {
	s.mu.Lock()
	cp := c
	s.clients[c.ID] = &cp
	s.mu.Unlock()
}
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

	var clients []domain.Client
	for _, c := range s.clients {
		if c.TenantID == tenantID {
			clients = append(clients, *c)
		}
	}
	sortClients(clients)

	var projects []domain.Project
	for _, p := range s.projects {
		if p.TenantID == tenantID {
			projects = append(projects, cloneProject(p))
		}
	}
	sortProjects(projects)

	requirements := s.selectRequirements(tenantID, store.RequirementFilter{})

	var tasks []domain.Task
	for _, t := range s.tasks {
		if t.TenantID == tenantID {
			tasks = append(tasks, s.withCounts(*t))
		}
	}
	sortTasks(tasks)

	return &domain.Bootstrap{
		Tenant:       tenant,
		User:         user,
		Clients:      clients,
		Projects:     projects,
		Requirements: requirements,
		Tasks:        tasks,
		ServerAt:     time.Now().UTC(),
	}, nil
}

func (s *Store) ListTasks(_ context.Context, tenantID string, f store.TaskFilter) ([]domain.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Task
	for _, t := range s.tasks {
		if matchTask(t, tenantID, f) {
			out = append(out, s.withCounts(*t))
		}
	}
	sortTasks(out)
	return out, nil
}

// ListTasksPaged is the keyset-paged read: filter, keep rows older than the
// cursor, sort newest-first, then let store.Paginate trim to the page. Any
// entity paged later (signals) follows exactly this shape with its own key.
func (s *Store) ListTasksPaged(_ context.Context, tenantID string, f store.TaskFilter, p store.Page) (store.PageResult[domain.Task], error) {
	cur, err := store.DecodeCursor(p.Cursor)
	if err != nil {
		return store.PageResult[domain.Task]{}, err
	}
	limit := p.EffectiveLimit()

	s.mu.RLock()
	defer s.mu.RUnlock()
	var rows []domain.Task
	for _, t := range s.tasks {
		if matchTask(t, tenantID, f) && cur.Admits(t.CreatedAt, t.ID) {
			rows = append(rows, s.withCounts(*t))
		}
	}
	store.SortNewestFirst(rows, store.TaskKey)
	if len(rows) > limit+1 {
		rows = rows[:limit+1] // limit+1: Paginate uses the spare row as "has more"
	}
	return store.Paginate(rows, limit, store.TaskKey), nil
}

// matchTask applies the tenant fence and the ListTasks filter to one row.
func matchTask(t *domain.Task, tenantID string, f store.TaskFilter) bool {
	if t.TenantID != tenantID {
		return false
	}
	if f.Status != nil && t.Status != *f.Status {
		return false
	}
	if f.Stage != nil && t.Stage != *f.Stage {
		return false
	}
	if f.ProjectID != nil && (t.ProjectID == nil || *t.ProjectID != *f.ProjectID) {
		return false
	}
	return true
}

func (s *Store) GetTask(_ context.Context, tenantID, id string) (*domain.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok || t.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	cp := s.withCounts(*t)
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

func (s *Store) ListClients(_ context.Context, tenantID string) ([]domain.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Client
	for _, c := range s.clients {
		if c.TenantID == tenantID {
			out = append(out, *c)
		}
	}
	sortClients(out)
	return out, nil
}

// ---- task writes ----

func (s *Store) CreateTask(_ context.Context, tenantID, actorID string, in domain.CreateTaskInput) (*domain.Task, error) {
	t, err := store.NewTask(tenantID, actorID, in, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if err := s.checkTaskRefs(tenantID, &t); err != nil {
		s.mu.Unlock()
		return nil, err
	}
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
	if err := s.checkTaskRefs(tenantID, &updated); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	updated.Version = existing.Version + 1
	updated.UpdatedAt = now
	s.tasks[id] = &updated
	// the stored row never carries the derived counts; what leaves does
	out := s.withCounts(updated)
	s.mu.Unlock()

	s.broker.publish(taskEvent(domain.EventTaskUpdated, actorID, &out))
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
	s.cascadeTaskChildren(id) // origins and outputs go with the work item
	s.mu.Unlock()

	s.broker.publish(domain.Event{
		ID: domain.NewID(), Type: domain.EventTaskDeleted, TenantID: tenantID,
		ActorID: actorID, EntityType: domain.EntityTask, EntityID: id, At: time.Now().UTC(),
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
		ID: domain.NewID(), TenantID: tenantID, ClientID: in.ClientID, Name: name,
		Subtitle: deref(in.Subtitle), Outcome: strings.TrimSpace(deref(in.Outcome)), Due: in.Due,
		Color:   orDefault(in.Color, "#C2743D"),
		Members: []domain.ProjectMember{}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CheckTextLen("outcome", p.Outcome); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if p.ClientID != nil && !s.hasClient(tenantID, *p.ClientID) {
		s.mu.Unlock()
		return nil, domain.Invalid("clientId", "not found in this workspace")
	}
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
	if v, ok := patch["clientId"]; ok {
		if v == nil {
			updated.ClientID = nil
		} else if s2, ok := v.(string); ok {
			updated.ClientID = &s2
		}
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
	if o, ok := patch["outcome"].(string); ok {
		if err := store.CheckTextLen("outcome", o); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		updated.Outcome = strings.TrimSpace(o)
	}
	if v, ok := patch["archived"]; ok {
		if b, _ := v.(bool); b {
			if updated.ArchivedAt == nil {
				at := now
				updated.ArchivedAt = &at
			}
		} else {
			updated.ArchivedAt = nil
		}
	}
	if updated.ClientID != nil && !s.hasClient(tenantID, *updated.ClientID) {
		s.mu.Unlock()
		return nil, domain.Invalid("clientId", "not found in this workspace")
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
	s.cascadeRequirements(tenantID, id) // requirements go with their project
	s.detachSignalsFromProject(tenantID, id, time.Now().UTC())
	// detach tasks from the deleted project
	for _, t := range s.tasks {
		if t.ProjectID != nil && *t.ProjectID == id {
			t.ProjectID = nil
		}
	}
	s.mu.Unlock()

	s.broker.publish(domain.Event{
		ID: domain.NewID(), Type: domain.EventProjectDeleted, TenantID: tenantID,
		ActorID: actorID, EntityType: domain.EntityProject, EntityID: id, At: time.Now().UTC(),
	})
	return nil
}

// ---- client writes ----

func (s *Store) CreateClient(_ context.Context, tenantID, actorID string, in domain.CreateClientInput) (*domain.Client, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, domain.Invalid("name", "is required")
	}
	tier := orDefault(in.Tier, domain.TierB)
	if !domain.ValidTier(tier) {
		return nil, domain.Invalid("tier", "is invalid")
	}
	kind := orDefault(in.Kind, domain.ClientExternal)
	if !domain.ValidClientKind(kind) {
		return nil, domain.Invalid("kind", "is invalid")
	}
	if in.ExpectedTouchDays != nil && *in.ExpectedTouchDays <= 0 {
		return nil, domain.Invalid("expectedTouchDays", "must be positive")
	}
	now := time.Now().UTC()
	c := domain.Client{
		ID: domain.NewID(), TenantID: tenantID, Name: name, Tier: tier, Kind: kind,
		Color: orDefault(in.Color, "#6E7E91"), Standard: strings.TrimSpace(deref(in.Standard)),
		ExpectedTouchDays: in.ExpectedTouchDays,
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CheckTextLen("standard", c.Standard); err != nil {
		return nil, err
	}

	s.mu.Lock()
	cp := c
	s.clients[c.ID] = &cp
	s.mu.Unlock()

	s.broker.publish(clientEvent(domain.EventClientCreated, actorID, &c))
	return &c, nil
}

func (s *Store) UpdateClient(_ context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Client, error) {
	now := time.Now().UTC()

	s.mu.Lock()
	existing, ok := s.clients[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return nil, domain.ErrNotFound
	}
	if expectedVersion != nil && *expectedVersion != existing.Version {
		s.mu.Unlock()
		return nil, domain.ErrConflict
	}
	updated := *existing
	if name, ok := patch["name"].(string); ok && strings.TrimSpace(name) != "" {
		updated.Name = strings.TrimSpace(name)
	}
	if t, ok := patch["tier"].(string); ok {
		if !domain.ValidTier(t) {
			s.mu.Unlock()
			return nil, domain.Invalid("tier", "is invalid")
		}
		updated.Tier = t
	}
	if k, ok := patch["kind"].(string); ok {
		if !domain.ValidClientKind(k) {
			s.mu.Unlock()
			return nil, domain.Invalid("kind", "is invalid")
		}
		updated.Kind = k
	}
	if c, ok := patch["color"].(string); ok {
		updated.Color = c
	}
	if st, ok := patch["standard"].(string); ok {
		if err := store.CheckTextLen("standard", st); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		updated.Standard = strings.TrimSpace(st)
	}
	if v, ok := patch["expectedTouchDays"]; ok {
		n, err := touchDays(v)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		updated.ExpectedTouchDays = n
	}
	if v, ok := patch["archived"]; ok {
		if b, _ := v.(bool); b {
			updated.ArchivedAt = &now
		} else {
			updated.ArchivedAt = nil
		}
	}
	updated.Version = existing.Version + 1
	updated.UpdatedAt = now
	cp := updated
	s.clients[id] = &cp
	s.mu.Unlock()

	s.broker.publish(clientEvent(domain.EventClientUpdated, actorID, &updated))
	return &updated, nil
}

func (s *Store) DeleteClient(_ context.Context, tenantID, actorID, id string) error {
	s.mu.Lock()
	existing, ok := s.clients[id]
	if !ok || existing.TenantID != tenantID {
		s.mu.Unlock()
		return domain.ErrNotFound
	}
	delete(s.clients, id)
	// detach projects from the deleted client (tasks are unaffected). Bump
	// updated_at to mirror the postgres ON DELETE SET NULL trigger; version is
	// left as-is and no project.updated event fires (parity with postgres).
	now := time.Now().UTC()
	for _, p := range s.projects {
		if p.ClientID != nil && *p.ClientID == id {
			p.ClientID = nil
			p.UpdatedAt = now
		}
	}
	s.mu.Unlock()

	s.broker.publish(domain.Event{
		ID: domain.NewID(), Type: domain.EventClientDeleted, TenantID: tenantID,
		ActorID: actorID, EntityType: domain.EntityClient, EntityID: id, At: time.Now().UTC(),
	})
	return nil
}

// ---- realtime / lifecycle ----

func (s *Store) Subscribe(tenantID string) (<-chan domain.Event, func()) {
	return s.broker.subscribe(tenantID)
}

func (s *Store) Ping(context.Context) error { return nil }

func (s *Store) Close() error {
	s.stopOnce.Do(func() { close(s.stopPrune) })
	s.broker.close()
	return nil
}

// ---- helpers ----

// taskEvent / projectEvent / clientEvent build the legacy typed-pointer events.
// New entity types should use s.emitEntity (events.go) instead.
func taskEvent(t domain.EventType, actorID string, task *domain.Task) domain.Event {
	cp := *task
	return domain.Event{
		ID: domain.NewID(), Type: t, TenantID: task.TenantID, ActorID: actorID,
		Task: &cp, EntityType: domain.EntityTask, EntityID: task.ID, At: time.Now().UTC(),
	}
}

func projectEvent(t domain.EventType, actorID string, p *domain.Project) domain.Event {
	cp := cloneProject(p)
	return domain.Event{
		ID: domain.NewID(), Type: t, TenantID: p.TenantID, ActorID: actorID,
		Project: &cp, EntityType: domain.EntityProject, EntityID: p.ID, At: time.Now().UTC(),
	}
}

func clientEvent(t domain.EventType, actorID string, c *domain.Client) domain.Event {
	cp := *c
	return domain.Event{
		ID: domain.NewID(), Type: t, TenantID: c.TenantID, ActorID: actorID,
		Client: &cp, EntityType: domain.EntityClient, EntityID: c.ID, At: time.Now().UTC(),
	}
}

func cloneProject(p *domain.Project) domain.Project {
	cp := *p
	// start from a non-nil slice so an empty members list stays [] (not null) in JSON
	cp.Members = append([]domain.ProjectMember{}, p.Members...)
	return cp
}

// hasProject / hasClient report whether a referenced row exists in the tenant.
// They read the maps without locking — the caller must already hold s.mu. Used
// to reject cross-tenant references (a task pointing at another tenant's project,
// or a project at another tenant's client), matching the Postgres composite FKs.
func (s *Store) hasProject(tenantID, id string) bool {
	p, ok := s.projects[id]
	return ok && p.TenantID == tenantID
}
func (s *Store) hasClient(tenantID, id string) bool {
	c, ok := s.clients[id]
	return ok && c.TenantID == tenantID
}
func (s *Store) hasUser(tenantID, id string) bool {
	u, ok := s.users[id]
	return ok && u.TenantID == tenantID
}
func (s *Store) hasRequirement(tenantID, id string) bool {
	r, ok := s.requirements[id]
	return ok && r.TenantID == tenantID
}

// checkTaskRefs mirrors the tenant-local FKs on tasks (0001 project, 0013
// owner / requirement / waiting-on person): every reference must exist in
// the tenant. Caller holds s.mu. createdBy is attribution with no FK, so it
// is not checked.
func (s *Store) checkTaskRefs(tenantID string, t *domain.Task) error {
	if t.ProjectID != nil && !s.hasProject(tenantID, *t.ProjectID) {
		return domain.Invalid("projectId", "not found in this workspace")
	}
	if t.OwnerID != nil && !s.hasUser(tenantID, *t.OwnerID) {
		return domain.Invalid("ownerId", "not found in this workspace")
	}
	if t.RequirementID != nil && !s.hasRequirement(tenantID, *t.RequirementID) {
		return domain.Invalid("requirementId", "not found in this workspace")
	}
	if t.WaitingOnPersonID != nil && !s.hasUser(tenantID, *t.WaitingOnPersonID) {
		return domain.Invalid("waitingOnPersonId", "not found in this workspace")
	}
	return nil
}

// detachTasksFromRequirement mirrors the postgres ON DELETE SET NULL
// (requirement_id) from requirements: the tasks stay, pointing at nothing.
// updated_at is bumped like the trigger would; version is left as-is and no
// task.updated event fires (parity with postgres, where the FK does it).
// Caller holds s.mu.
func (s *Store) detachTasksFromRequirement(tenantID, requirementID string, now time.Time) {
	for _, t := range s.tasks {
		if t.TenantID == tenantID && t.RequirementID != nil && *t.RequirementID == requirementID {
			t.RequirementID = nil
			t.UpdatedAt = now
		}
	}
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

func sortClients(cs []domain.Client) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].CreatedAt.Before(cs[j].CreatedAt) })
}

// touchDays coerces a JSON patch value into a validated *int for
// expected_touch_days: nil clears it, a positive number sets it.
func touchDays(v any) (*int, error) {
	if v == nil {
		return nil, nil
	}
	f, ok := v.(float64) // JSON numbers decode to float64
	if !ok {
		return nil, domain.Invalid("expectedTouchDays", "must be a number")
	}
	n := int(f)
	if n <= 0 {
		return nil, domain.Invalid("expectedTouchDays", "must be positive")
	}
	return &n, nil
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
