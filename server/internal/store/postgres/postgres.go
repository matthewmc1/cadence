// Package postgres is the production Store adapter: pgx + Postgres with
// Row-Level Security for tenant isolation, a transactional outbox for durable
// realtime, and LISTEN/NOTIFY fan-out that works across many instances.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	URL         string
	AutoMigrate bool
	Log         *slog.Logger
}

type Store struct {
	pool   *pgxpool.Pool
	log    *slog.Logger
	fan    *fanout
	cancel context.CancelFunc
}

// Open connects the pool, optionally migrates, and starts the LISTEN loop.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.URL == "" {
		return nil, errors.New("postgres: DATABASE_URL is required")
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	pool, err := pgxpool.New(ctx, cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	if cfg.AutoMigrate {
		applied, err := Migrate(ctx, pool)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("postgres: migrate: %w", err)
		}
		if len(applied) > 0 {
			log.Info("applied migrations", "versions", applied)
		}
	}

	lctx, cancel := context.WithCancel(context.Background())
	s := &Store{pool: pool, log: log, fan: newFanout(), cancel: cancel}
	go listen(lctx, pool, s.fan, log)
	return s, nil
}

func (s *Store) Close() error {
	s.cancel()
	s.fan.close()
	s.pool.Close()
	return nil
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Subscribe(tenantID string) (<-chan domain.Event, func()) {
	return s.fan.subscribe(tenantID)
}

// withTenant runs fn inside a transaction with app.tenant_id set, so every
// statement is fenced by RLS to this tenant's rows.
func (s *Store) withTenant(ctx context.Context, tenantID string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const taskCols = `tenant_id::text, id::text, project_id::text, title, kind::text, status::text,
	effort_minutes, urgent, note, place, scheduled_at,
	position, done_at, deadline, recurrence, links, subtasks, assignees, version, created_at, updated_at`

type taskRow struct {
	TenantID      string     `db:"tenant_id"`
	ID            string     `db:"id"`
	ProjectID     *string    `db:"project_id"`
	Title         string     `db:"title"`
	Kind          string     `db:"kind"`
	Status        string     `db:"status"`
	EffortMinutes int        `db:"effort_minutes"`
	Urgent        bool       `db:"urgent"`
	Note          string     `db:"note"`
	Place         *string    `db:"place"`
	ScheduledAt   *time.Time `db:"scheduled_at"`
	Position      float64    `db:"position"`
	DoneAt        *time.Time `db:"done_at"`
	Deadline      *time.Time `db:"deadline"`
	Recurrence    string     `db:"recurrence"`
	Links         []byte     `db:"links"`
	Subtasks      []byte     `db:"subtasks"`
	Assignees     []byte     `db:"assignees"`
	Version       int        `db:"version"`
	CreatedAt     time.Time  `db:"created_at"`
	UpdatedAt     time.Time  `db:"updated_at"`
}

func (r taskRow) toTask() domain.Task {
	t := domain.Task{
		ID: r.ID, TenantID: r.TenantID, ProjectID: r.ProjectID, Title: r.Title,
		Kind: domain.Kind(r.Kind), Status: domain.Status(r.Status), EffortMinutes: r.EffortMinutes,
		Urgent: r.Urgent, Note: r.Note, Place: r.Place, ScheduledAt: r.ScheduledAt,
		Position: r.Position, DoneAt: r.DoneAt, Deadline: r.Deadline, Recurrence: r.Recurrence,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	_ = json.Unmarshal(r.Links, &t.Links)
	_ = json.Unmarshal(r.Subtasks, &t.Subtasks)
	_ = json.Unmarshal(r.Assignees, &t.Assignees)
	t.Normalize()
	return t
}

// ---- reads -----------------------------------------------------------------

func (s *Store) Bootstrap(ctx context.Context, tenantID, userID string) (*domain.Bootstrap, error) {
	var boot domain.Bootstrap
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text, name, created_at FROM tenants WHERE id = $1`, tenantID).
			Scan(&boot.Tenant.ID, &boot.Tenant.Name, &boot.Tenant.CreatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}

		var email string
		if e := tx.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return e
		}
		boot.User = domain.DeriveUser(userID, tenantID, email)

		projects, err := selectProjects(ctx, tx)
		if err != nil {
			return err
		}
		boot.Projects = projects

		tasks, err := selectTasks(ctx, tx, "")
		if err != nil {
			return err
		}
		boot.Tasks = tasks
		boot.ServerAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		return nil, err
	}
	if boot.Projects == nil {
		boot.Projects = []domain.Project{}
	}
	if boot.Tasks == nil {
		boot.Tasks = []domain.Task{}
	}
	return &boot, nil
}

func (s *Store) ListTasks(ctx context.Context, tenantID string, f store.TaskFilter) ([]domain.Task, error) {
	var out []domain.Task
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		where := ""
		var args []any
		add := func(cond string, v any) {
			args = append(args, v)
			if where == "" {
				where = " WHERE "
			} else {
				where += " AND "
			}
			where += fmt.Sprintf(cond, len(args))
		}
		if f.Status != nil {
			add("status = $%d::task_status", string(*f.Status))
		}
		if f.ProjectID != nil {
			add("project_id = $%d", *f.ProjectID)
		}
		tasks, err := selectTasks(ctx, tx, where, args...)
		if err != nil {
			return err
		}
		out = tasks
		return nil
	})
	return out, err
}

func (s *Store) GetTask(ctx context.Context, tenantID, id string) (*domain.Task, error) {
	var t *domain.Task
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		got, err := getTaskTx(ctx, tx, id, false)
		if err != nil {
			return err
		}
		t = got
		return nil
	})
	return t, err
}

func (s *Store) ListProjects(ctx context.Context, tenantID string) ([]domain.Project, error) {
	var out []domain.Project
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		projects, err := selectProjects(ctx, tx)
		if err != nil {
			return err
		}
		out = projects
		return nil
	})
	return out, err
}

func selectTasks(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]domain.Task, error) {
	rows, err := tx.Query(ctx, `SELECT `+taskCols+` FROM tasks`+where+` ORDER BY position, created_at`, args...)
	if err != nil {
		return nil, err
	}
	trows, err := pgx.CollectRows(rows, pgx.RowToStructByName[taskRow])
	if err != nil {
		return nil, err
	}
	out := make([]domain.Task, len(trows))
	for i, r := range trows {
		out[i] = r.toTask()
	}
	return out, nil
}

func getTaskTx(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (*domain.Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	rows, err := tx.Query(ctx, q, id)
	if err != nil {
		return nil, err
	}
	r, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[taskRow])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t := r.toTask()
	return &t, nil
}

func selectProjects(ctx context.Context, tx pgx.Tx) ([]domain.Project, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, tenant_id::text, name, subtitle, due, color, version, created_at, updated_at
		FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []domain.Project
	index := map[string]int{}
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.TenantID, &p.Name, &p.Subtitle, &p.Due, &p.Color, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Members = []domain.ProjectMember{}
		index[p.ID] = len(projects)
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	mrows, err := tx.Query(ctx, `SELECT project_id::text, user_id::text, initial, color FROM project_members`)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var pid string
		var m domain.ProjectMember
		if err := mrows.Scan(&pid, &m.UserID, &m.Initial, &m.Color); err != nil {
			return nil, err
		}
		if i, ok := index[pid]; ok {
			projects[i].Members = append(projects[i].Members, m)
		}
	}
	return projects, mrows.Err()
}

// ---- task writes -----------------------------------------------------------

func (s *Store) CreateTask(ctx context.Context, tenantID, actorID string, in domain.CreateTaskInput) (*domain.Task, error) {
	title := trim(in.Title)
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
		ID: domain.NewID(), TenantID: tenantID, ProjectID: in.ProjectID, Title: title,
		Kind: kind, Status: status, EffortMinutes: effort, Note: deref(in.Note), Place: in.Place,
		ScheduledAt: in.ScheduledAt,
		Version:     1, CreatedAt: now, UpdatedAt: now,
	}
	if in.Urgent != nil {
		t.Urgent = *in.Urgent
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

	var created *domain.Task
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := insertTask(ctx, tx, t); err != nil {
			return err
		}
		got, err := getTaskTx(ctx, tx, t.ID, false)
		if err != nil {
			return err
		}
		created = got
		return emit(ctx, tx, taskEvent(domain.EventTaskCreated, actorID, got))
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *Store) UpdateTask(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Task, error) {
	now := time.Now().UTC()
	var updated *domain.Task
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		existing, err := getTaskTx(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if expectedVersion != nil && *expectedVersion != existing.Version {
			return domain.ErrConflict
		}
		next := *existing
		if err := store.ApplyTaskPatch(&next, patch, now); err != nil {
			return err
		}
		next.Version = existing.Version + 1
		if _, err := tx.Exec(ctx, `
			UPDATE tasks SET
				project_id = $2, title = $3, kind = $4::task_kind, status = $5::task_status,
				effort_minutes = $6, urgent = $7, note = $8, place = $9,
				scheduled_at = $10, position = $11, done_at = $12, deadline = $13, recurrence = $14,
				links = $15::jsonb, subtasks = $16::jsonb, assignees = $17::jsonb, version = $18
			WHERE id = $1`,
			id, next.ProjectID, next.Title, string(next.Kind), string(next.Status), next.EffortMinutes,
			next.Urgent, next.Note, next.Place, next.ScheduledAt,
			next.Position, next.DoneAt, next.Deadline, next.Recurrence,
			jsonArr(next.Links), jsonArr(next.Subtasks), jsonArr(next.Assignees), next.Version); err != nil {
			return err
		}
		got, err := getTaskTx(ctx, tx, id, false)
		if err != nil {
			return err
		}
		updated = got
		return emit(ctx, tx, taskEvent(domain.EventTaskUpdated, actorID, got))
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) DeleteTask(ctx context.Context, tenantID, actorID, id string) error {
	return s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `DELETE FROM tasks WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emit(ctx, tx, domain.Event{
			ID: domain.NewID(), Type: domain.EventTaskDeleted, TenantID: tenantID,
			ActorID: actorID, EntityID: id, At: time.Now().UTC(),
		})
	})
}

func insertTask(ctx context.Context, tx pgx.Tx, t domain.Task) error {
	t.Normalize()
	_, err := tx.Exec(ctx, `
		INSERT INTO tasks (
			tenant_id, id, project_id, title, kind, status, effort_minutes, urgent, note, place,
			scheduled_at, position, done_at,
			deadline, recurrence, links, subtasks, assignees, version, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5::task_kind, $6::task_status, $7, $8, $9, $10,
			$11, $12, $13,
			$14, $15, $16::jsonb, $17::jsonb, $18::jsonb, $19, $20, $21
		)`,
		t.TenantID, t.ID, t.ProjectID, t.Title, string(t.Kind), string(t.Status), t.EffortMinutes, t.Urgent, t.Note, t.Place,
		t.ScheduledAt, t.Position, t.DoneAt,
		t.Deadline, t.Recurrence, jsonArr(t.Links), jsonArr(t.Subtasks), jsonArr(t.Assignees), t.Version, t.CreatedAt, t.UpdatedAt)
	return err
}

// jsonArr marshals a slice to a JSON array string ("[]" for nil) for jsonb insert.
func jsonArr(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

// ---- project writes --------------------------------------------------------

func (s *Store) CreateProject(ctx context.Context, tenantID, actorID string, in domain.CreateProjectInput) (*domain.Project, error) {
	name := trim(in.Name)
	if name == "" {
		return nil, domain.Invalid("name", "is required")
	}
	now := time.Now().UTC()
	p := domain.Project{
		ID: domain.NewID(), TenantID: tenantID, Name: name, Subtitle: deref(in.Subtitle),
		Due: in.Due, Color: orDefault(in.Color, "#C2743D"), Members: []domain.ProjectMember{},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO projects (tenant_id, id, name, subtitle, due, color, version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			p.TenantID, p.ID, p.Name, p.Subtitle, p.Due, p.Color, p.Version, p.CreatedAt, p.UpdatedAt); err != nil {
			return err
		}
		return emit(ctx, tx, projectEvent(domain.EventProjectCreated, actorID, &p))
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) UpdateProject(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Project, error) {
	var updated *domain.Project
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var p domain.Project
		err := tx.QueryRow(ctx, `
			SELECT id::text, tenant_id::text, name, subtitle, due, color, version, created_at, updated_at
			FROM projects WHERE id = $1 FOR UPDATE`, id).
			Scan(&p.ID, &p.TenantID, &p.Name, &p.Subtitle, &p.Due, &p.Color, &p.Version, &p.CreatedAt, &p.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if expectedVersion != nil && *expectedVersion != p.Version {
			return domain.ErrConflict
		}
		if name, ok := patch["name"].(string); ok && trim(name) != "" {
			p.Name = trim(name)
		}
		if v, ok := patch["subtitle"]; ok {
			p.Subtitle, _ = v.(string)
		}
		if v, ok := patch["due"]; ok {
			if v == nil {
				p.Due = nil
			} else if s2, ok := v.(string); ok {
				p.Due = &s2
			}
		}
		if c, ok := patch["color"].(string); ok {
			p.Color = c
		}
		p.Version++
		if _, err := tx.Exec(ctx,
			`UPDATE projects SET name=$2, subtitle=$3, due=$4, color=$5, version=$6 WHERE id=$1`,
			id, p.Name, p.Subtitle, p.Due, p.Color, p.Version); err != nil {
			return err
		}
		members, err := projectMembers(ctx, tx, id)
		if err != nil {
			return err
		}
		p.Members = members
		updated = &p
		return emit(ctx, tx, projectEvent(domain.EventProjectUpdated, actorID, &p))
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) DeleteProject(ctx context.Context, tenantID, actorID, id string) error {
	return s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emit(ctx, tx, domain.Event{
			ID: domain.NewID(), Type: domain.EventProjectDeleted, TenantID: tenantID,
			ActorID: actorID, EntityID: id, At: time.Now().UTC(),
		})
	})
}

func projectMembers(ctx context.Context, tx pgx.Tx, projectID string) ([]domain.ProjectMember, error) {
	rows, err := tx.Query(ctx, `SELECT user_id::text, initial, color FROM project_members WHERE project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ProjectMember{}
	for rows.Next() {
		var m domain.ProjectMember
		if err := rows.Scan(&m.UserID, &m.Initial, &m.Color); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- outbox / events -------------------------------------------------------

// emit writes the event to the outbox in the current transaction. The AFTER
// INSERT trigger pg_notify()s on commit, so delivery is atomic with the change.
func emit(ctx context.Context, tx pgx.Tx, ev domain.Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var actor any
	if ev.ActorID != "" {
		actor = ev.ActorID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (id, tenant_id, type, entity_id, actor_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		domain.NewID(), ev.TenantID, string(ev.Type), ev.EntityID, actor, payload)
	return err
}

func taskEvent(t domain.EventType, actorID string, task *domain.Task) domain.Event {
	cp := *task
	return domain.Event{ID: domain.NewID(), Type: t, TenantID: task.TenantID, ActorID: actorID, Task: &cp, EntityID: task.ID, At: time.Now().UTC()}
}

func projectEvent(t domain.EventType, actorID string, p *domain.Project) domain.Event {
	cp := *p
	return domain.Event{ID: domain.NewID(), Type: t, TenantID: p.TenantID, ActorID: actorID, Project: &cp, EntityID: p.ID, At: time.Now().UTC()}
}

// ---- small helpers ---------------------------------------------------------

func trim(s string) string {
	b, e := 0, len(s)
	for b < e && (s[b] == ' ' || s[b] == '\t' || s[b] == '\n' || s[b] == '\r') {
		b++
	}
	for e > b && (s[e-1] == ' ' || s[e-1] == '\t' || s[e-1] == '\n' || s[e-1] == '\r') {
		e--
	}
	return s[b:e]
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
