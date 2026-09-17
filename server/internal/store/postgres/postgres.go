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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	URL         string
	AutoMigrate bool
	Log         *slog.Logger
	// OutboxRetention is how long outbox rows are kept before the hourly
	// pruner deletes them (see outbox.go). Zero = DefaultOutboxRetention.
	OutboxRetention time.Duration
}

type Store struct {
	pool      *pgxpool.Pool
	log       *slog.Logger
	fan       *fanout
	cancel    context.CancelFunc
	retention time.Duration
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

	retention := cfg.OutboxRetention
	if retention <= 0 {
		retention = DefaultOutboxRetention
	}
	lctx, cancel := context.WithCancel(context.Background())
	s := &Store{pool: pool, log: log, fan: newFanout(), cancel: cancel, retention: retention}
	go listen(lctx, pool, s.fan, log)
	go s.pruneLoop(lctx) // stops with cancel() in Close
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
	effort_minutes, urgent, important, note, reflection, place, scheduled_at,
	position, done_at, deadline, recurrence, links, subtasks, assignees,
	stage, owner_id::text, created_by::text, requirement_id::text, definition_of_done,
	waiting_on_person_id::text, waiting_on_reason, waiting_on_since, ask, ask_by,
	version, created_at, updated_at,
	(SELECT count(*) FROM work_item_signals wis WHERE wis.tenant_id = tasks.tenant_id AND wis.task_id = tasks.id) AS origin_count,
	(SELECT count(*) FROM outputs o WHERE o.tenant_id = tasks.tenant_id AND o.task_id = tasks.id) AS output_count`

type taskRow struct {
	TenantID      string     `db:"tenant_id"`
	ID            string     `db:"id"`
	ProjectID     *string    `db:"project_id"`
	Title         string     `db:"title"`
	Kind          string     `db:"kind"`
	Status        string     `db:"status"`
	EffortMinutes int        `db:"effort_minutes"`
	Urgent        bool       `db:"urgent"`
	Important     bool       `db:"important"`
	Note          string     `db:"note"`
	Reflection    string     `db:"reflection"`
	Place         *string    `db:"place"`
	ScheduledAt   *time.Time `db:"scheduled_at"`
	Position      float64    `db:"position"`
	DoneAt        *time.Time `db:"done_at"`
	Deadline      *time.Time `db:"deadline"`
	Recurrence    string     `db:"recurrence"`
	Links         []byte     `db:"links"`
	Subtasks      []byte     `db:"subtasks"`
	Assignees     []byte     `db:"assignees"`
	// work-item fields (0013)
	Stage             string     `db:"stage"`
	OwnerID           *string    `db:"owner_id"`
	CreatedBy         *string    `db:"created_by"`
	RequirementID     *string    `db:"requirement_id"`
	DefinitionOfDone  string     `db:"definition_of_done"`
	WaitingOnPersonID *string    `db:"waiting_on_person_id"`
	WaitingOnReason   string     `db:"waiting_on_reason"`
	WaitingOnSince    *time.Time `db:"waiting_on_since"`
	Ask               []byte     `db:"ask"`
	AskBy             *time.Time `db:"ask_by"`
	Version           int        `db:"version"`
	CreatedAt         time.Time  `db:"created_at"`
	UpdatedAt         time.Time  `db:"updated_at"`
	// derived counts (0014/0015), computed by the two sub-selects in taskCols
	OriginCount int `db:"origin_count"`
	OutputCount int `db:"output_count"`
}

func (r taskRow) toTask() domain.Task {
	t := domain.Task{
		ID: r.ID, TenantID: r.TenantID, ProjectID: r.ProjectID, Title: r.Title,
		Kind: domain.Kind(r.Kind), Status: domain.Status(r.Status), EffortMinutes: r.EffortMinutes,
		Urgent: r.Urgent, Important: r.Important, Note: r.Note, Reflection: r.Reflection, Place: r.Place, ScheduledAt: r.ScheduledAt,
		Position: r.Position, DoneAt: r.DoneAt, Deadline: r.Deadline, Recurrence: r.Recurrence,
		Stage: domain.Stage(r.Stage), OwnerID: r.OwnerID, CreatedBy: r.CreatedBy, RequirementID: r.RequirementID,
		DefinitionOfDone: r.DefinitionOfDone, WaitingOnPersonID: r.WaitingOnPersonID, WaitingOnReason: r.WaitingOnReason,
		WaitingOnSince: r.WaitingOnSince, AskBy: r.AskBy,
		OriginCount: r.OriginCount, OutputCount: r.OutputCount,
		Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	_ = json.Unmarshal(r.Links, &t.Links)
	_ = json.Unmarshal(r.Subtasks, &t.Subtasks)
	_ = json.Unmarshal(r.Assignees, &t.Assignees)
	_ = json.Unmarshal(r.Ask, &t.Ask)
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
		if e := tx.QueryRow(ctx, `SELECT email FROM users WHERE id = $1 AND tenant_id = $2`, userID, tenantID).Scan(&email); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return e
		}
		boot.User = domain.DeriveUser(userID, tenantID, email)

		clients, err := selectClients(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		boot.Clients = clients

		projects, err := selectProjects(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		boot.Projects = projects

		requirements, err := selectRequirements(ctx, tx, tenantID, store.RequirementFilter{})
		if err != nil {
			return err
		}
		boot.Requirements = requirements

		tasks, err := selectTasks(ctx, tx, tenantID, "")
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
	if boot.Clients == nil {
		boot.Clients = []domain.Client{}
	}
	if boot.Projects == nil {
		boot.Projects = []domain.Project{}
	}
	if boot.Requirements == nil {
		boot.Requirements = []domain.Requirement{}
	}
	if boot.Tasks == nil {
		boot.Tasks = []domain.Task{}
	}
	return &boot, nil
}

func (s *Store) ListTasks(ctx context.Context, tenantID string, f store.TaskFilter) ([]domain.Task, error) {
	var out []domain.Task
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		extra, args := taskFilterWhere(f)
		tasks, err := selectTasks(ctx, tx, tenantID, extra, args...)
		if err != nil {
			return emptyOnBadUUID(err) // a non-uuid projectId matches nothing
		}
		out = tasks
		return nil
	})
	return out, err
}

// ListTasksPaged is the keyset-paged read: same tenant fence and filter as
// ListTasks, then the cursor predicate, newest-first order and a limit+1 fetch
// that store.Paginate trims into the page (see store/paging.go).
func (s *Store) ListTasksPaged(ctx context.Context, tenantID string, f store.TaskFilter, p store.Page) (store.PageResult[domain.Task], error) {
	cur, err := store.DecodeCursor(p.Cursor)
	if err != nil {
		return store.PageResult[domain.Task]{}, err
	}
	limit := p.EffectiveLimit()

	var out store.PageResult[domain.Task]
	err = s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		extra, args := taskFilterWhere(f)
		after, cursorArgs := keysetWhere(cur, "created_at", len(args)+2) // +2: $1 is tenant_id
		args = append(args, cursorArgs...)
		args = append(args, limit+1)
		q := `SELECT ` + taskCols + ` FROM tasks WHERE tenant_id = $1` + extra + after +
			fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)+1)
		rows, err := queryTasks(ctx, tx, q, append([]any{tenantID}, args...)...)
		if err != nil {
			return emptyOnBadUUID(err)
		}
		out = store.Paginate(rows, limit, store.TaskKey)
		return nil
	})
	if out.Items == nil {
		out.Items = []domain.Task{}
	}
	return out, err
}

// taskFilterWhere renders a TaskFilter as ` AND …` clauses with placeholders
// numbered from $2 ($1 is always tenant_id), plus their args.
func taskFilterWhere(f store.TaskFilter) (string, []any) {
	extra := ""
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		extra += " AND " + fmt.Sprintf(cond, len(args)+1)
	}
	if f.Status != nil {
		add("status = $%d::task_status", string(*f.Status))
	}
	if f.Stage != nil {
		add("stage = $%d", string(*f.Stage))
	}
	if f.ProjectID != nil {
		add("project_id = $%d", *f.ProjectID)
	}
	return extra, args
}

func (s *Store) GetTask(ctx context.Context, tenantID, id string) (*domain.Task, error) {
	var t *domain.Task
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		got, err := getTaskTx(ctx, tx, tenantID, id, false)
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
		projects, err := selectProjects(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		out = projects
		return nil
	})
	return out, err
}

// selectTasks always fences by tenant_id ($1) as its own predicate — isolation
// must not depend solely on RLS, which superuser / BYPASSRLS roles skip. Any
// caller filters go in extraWhere with placeholders starting at $2.
func selectTasks(ctx context.Context, tx pgx.Tx, tenantID, extraWhere string, args ...any) ([]domain.Task, error) {
	all := append([]any{tenantID}, args...)
	return queryTasks(ctx, tx, `SELECT `+taskCols+` FROM tasks WHERE tenant_id = $1`+extraWhere+` ORDER BY position, created_at`, all...)
}

// queryTasks runs a full-column tasks query and maps the rows. The caller
// owns the WHERE/ORDER/LIMIT — and the tenant predicate that must be in it.
func queryTasks(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]domain.Task, error) {
	rows, err := tx.Query(ctx, q, args...)
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

func getTaskTx(ctx context.Context, tx pgx.Tx, tenantID, id string, forUpdate bool) (*domain.Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE id = $1 AND tenant_id = $2`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	rows, err := tx.Query(ctx, q, id, tenantID)
	if err != nil {
		return nil, notFoundOnBadUUID(err)
	}
	r, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[taskRow])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, notFoundOnBadUUID(err)
	}
	t := r.toTask()
	return &t, nil
}

func selectProjects(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.Project, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, tenant_id::text, client_id::text, name, subtitle, outcome, due, color, archived_at, version, created_at, updated_at
		FROM projects WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []domain.Project
	index := map[string]int{}
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.TenantID, &p.ClientID, &p.Name, &p.Subtitle, &p.Outcome, &p.Due, &p.Color, &p.ArchivedAt, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Members = []domain.ProjectMember{}
		index[p.ID] = len(projects)
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	mrows, err := tx.Query(ctx, `SELECT project_id::text, user_id::text, initial, color FROM project_members WHERE tenant_id = $1`, tenantID)
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
	t, err := store.NewTask(tenantID, actorID, in, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	var created *domain.Task
	err = s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireTaskRefs(ctx, tx, tenantID, &t); err != nil {
			return err
		}
		if err := insertTask(ctx, tx, t); err != nil {
			return err
		}
		got, err := getTaskTx(ctx, tx, tenantID, t.ID, false)
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
		existing, err := getTaskTx(ctx, tx, tenantID, id, true)
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
		if err := requireTaskRefs(ctx, tx, tenantID, &next); err != nil {
			return err
		}
		next.Version = existing.Version + 1
		// created_by is never rewritten: it is attribution stamped at insert.
		if _, err := tx.Exec(ctx, `
			UPDATE tasks SET
				project_id = $2, title = $3, kind = $4::task_kind, status = $5::task_status,
				effort_minutes = $6, urgent = $7, note = $8, place = $9,
				scheduled_at = $10, position = $11, done_at = $12, deadline = $13, recurrence = $14,
				links = $15::jsonb, subtasks = $16::jsonb, assignees = $17::jsonb, version = $18, important = $19, reflection = $20,
				stage = $22, owner_id = $23, requirement_id = $24, definition_of_done = $25,
				waiting_on_person_id = $26, waiting_on_reason = $27, waiting_on_since = $28, ask = $29::jsonb, ask_by = $30
			WHERE id = $1 AND tenant_id = $21`,
			id, next.ProjectID, next.Title, string(next.Kind), string(next.Status), next.EffortMinutes,
			next.Urgent, next.Note, next.Place, next.ScheduledAt,
			next.Position, next.DoneAt, next.Deadline, next.Recurrence,
			jsonArr(next.Links), jsonArr(next.Subtasks), jsonArr(next.Assignees), next.Version, next.Important, next.Reflection, tenantID,
			string(next.Stage), next.OwnerID, next.RequirementID, next.DefinitionOfDone,
			next.WaitingOnPersonID, next.WaitingOnReason, next.WaitingOnSince, jsonObj(next.Ask), next.AskBy); err != nil {
			return err
		}
		got, err := getTaskTx(ctx, tx, tenantID, id, false)
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
		ct, err := tx.Exec(ctx, `DELETE FROM tasks WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emit(ctx, tx, domain.Event{
			ID: domain.NewID(), Type: domain.EventTaskDeleted, TenantID: tenantID,
			ActorID: actorID, EntityType: domain.EntityTask, EntityID: id, At: time.Now().UTC(),
		})
	})
}

func insertTask(ctx context.Context, tx pgx.Tx, t domain.Task) error {
	t.Normalize()
	_, err := tx.Exec(ctx, `
		INSERT INTO tasks (
			tenant_id, id, project_id, title, kind, status, effort_minutes, urgent, note, place,
			scheduled_at, position, done_at,
			deadline, recurrence, links, subtasks, assignees, version, created_at, updated_at, important, reflection,
			stage, owner_id, created_by, requirement_id, definition_of_done,
			waiting_on_person_id, waiting_on_reason, waiting_on_since, ask, ask_by
		) VALUES (
			$1, $2, $3, $4, $5::task_kind, $6::task_status, $7, $8, $9, $10,
			$11, $12, $13,
			$14, $15, $16::jsonb, $17::jsonb, $18::jsonb, $19, $20, $21, $22, $23,
			$24, $25, $26, $27, $28,
			$29, $30, $31, $32::jsonb, $33
		)`,
		t.TenantID, t.ID, t.ProjectID, t.Title, string(t.Kind), string(t.Status), t.EffortMinutes, t.Urgent, t.Note, t.Place,
		t.ScheduledAt, t.Position, t.DoneAt,
		t.Deadline, t.Recurrence, jsonArr(t.Links), jsonArr(t.Subtasks), jsonArr(t.Assignees), t.Version, t.CreatedAt, t.UpdatedAt, t.Important, t.Reflection,
		string(t.Stage), t.OwnerID, t.CreatedBy, t.RequirementID, t.DefinitionOfDone,
		t.WaitingOnPersonID, t.WaitingOnReason, t.WaitingOnSince, jsonObj(t.Ask), t.AskBy)
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

// jsonObj marshals a struct/map to a JSON object string ("{}" on nil/failure)
// for jsonb insert — the ask column is CHECKed to be an object.
func jsonObj(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "{}"
	}
	return string(b)
}

// ---- project writes --------------------------------------------------------

func (s *Store) CreateProject(ctx context.Context, tenantID, actorID string, in domain.CreateProjectInput) (*domain.Project, error) {
	name := trim(in.Name)
	if name == "" {
		return nil, domain.Invalid("name", "is required")
	}
	// Microsecond precision: timestamptz stores µs, and the struct we return is
	// not re-read from the row, so truncate to keep create == later reads.
	now := time.Now().UTC().Truncate(time.Microsecond)
	p := domain.Project{
		ID: domain.NewID(), TenantID: tenantID, ClientID: in.ClientID, Name: name, Subtitle: deref(in.Subtitle),
		Outcome: trim(deref(in.Outcome)), Due: in.Due, Color: orDefault(in.Color, "#C2743D"), Members: []domain.ProjectMember{},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CheckTextLen("outcome", p.Outcome); err != nil {
		return nil, err
	}
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireClient(ctx, tx, tenantID, p.ClientID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO projects (tenant_id, id, client_id, name, subtitle, outcome, due, color, version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			p.TenantID, p.ID, p.ClientID, p.Name, p.Subtitle, p.Outcome, p.Due, p.Color, p.Version, p.CreatedAt, p.UpdatedAt); err != nil {
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
			SELECT id::text, tenant_id::text, client_id::text, name, subtitle, outcome, due, color, archived_at, version, created_at, updated_at
			FROM projects WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID).
			Scan(&p.ID, &p.TenantID, &p.ClientID, &p.Name, &p.Subtitle, &p.Outcome, &p.Due, &p.Color, &p.ArchivedAt, &p.Version, &p.CreatedAt, &p.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if expectedVersion != nil && *expectedVersion != p.Version {
			return domain.ErrConflict
		}
		if name, ok := patch["name"].(string); ok && trim(name) != "" {
			p.Name = trim(name)
		}
		if v, ok := patch["clientId"]; ok {
			if v == nil {
				p.ClientID = nil
			} else if s2, ok := v.(string); ok {
				p.ClientID = &s2
			}
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
		if o, ok := patch["outcome"].(string); ok {
			if err := store.CheckTextLen("outcome", o); err != nil {
				return err
			}
			p.Outcome = trim(o)
		}
		if v, ok := patch["archived"]; ok {
			if b, _ := v.(bool); b {
				if p.ArchivedAt == nil {
					at := time.Now().UTC().Truncate(time.Microsecond)
					p.ArchivedAt = &at
				}
			} else {
				p.ArchivedAt = nil
			}
		}
		if err := requireClient(ctx, tx, tenantID, p.ClientID); err != nil {
			return err
		}
		p.Version++
		// RETURNING updated_at so the response/event carry the trigger-bumped
		// timestamp rather than the pre-update value we SELECTed above.
		if err := tx.QueryRow(ctx,
			`UPDATE projects SET name=$2, subtitle=$3, due=$4, color=$5, version=$6, client_id=$7, outcome=$9, archived_at=$10 WHERE id=$1 AND tenant_id=$8 RETURNING updated_at`,
			id, p.Name, p.Subtitle, p.Due, p.Color, p.Version, p.ClientID, tenantID, p.Outcome, p.ArchivedAt).Scan(&p.UpdatedAt); err != nil {
			return err
		}
		members, err := projectMembers(ctx, tx, tenantID, id)
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
		ct, err := tx.Exec(ctx, `DELETE FROM projects WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emit(ctx, tx, domain.Event{
			ID: domain.NewID(), Type: domain.EventProjectDeleted, TenantID: tenantID,
			ActorID: actorID, EntityType: domain.EntityProject, EntityID: id, At: time.Now().UTC(),
		})
	})
}

func projectMembers(ctx context.Context, tx pgx.Tx, tenantID, projectID string) ([]domain.ProjectMember, error) {
	rows, err := tx.Query(ctx, `SELECT user_id::text, initial, color FROM project_members WHERE project_id = $1 AND tenant_id = $2`, projectID, tenantID)
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

// ---- client reads / writes -------------------------------------------------

func selectClients(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.Client, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, tenant_id::text, name, tier, kind, color, standard, expected_touch_days, archived_at, version, created_at, updated_at
		FROM clients WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Client
	for rows.Next() {
		var c domain.Client
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.Tier, &c.Kind, &c.Color, &c.Standard,
			&c.ExpectedTouchDays, &c.ArchivedAt, &c.Version, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ListClients(ctx context.Context, tenantID string) ([]domain.Client, error) {
	var out []domain.Client
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		clients, err := selectClients(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		out = clients
		return nil
	})
	return out, err
}

func (s *Store) CreateClient(ctx context.Context, tenantID, actorID string, in domain.CreateClientInput) (*domain.Client, error) {
	name := trim(in.Name)
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
	now := time.Now().UTC().Truncate(time.Microsecond) // see CreateProject
	c := domain.Client{
		ID: domain.NewID(), TenantID: tenantID, Name: name, Tier: tier, Kind: kind,
		Color: orDefault(in.Color, "#6E7E91"), Standard: trim(deref(in.Standard)), ExpectedTouchDays: in.ExpectedTouchDays,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CheckTextLen("standard", c.Standard); err != nil {
		return nil, err
	}
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO clients (tenant_id, id, name, tier, kind, color, standard, expected_touch_days, version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			c.TenantID, c.ID, c.Name, c.Tier, c.Kind, c.Color, c.Standard, c.ExpectedTouchDays, c.Version, c.CreatedAt, c.UpdatedAt); err != nil {
			return err
		}
		return emit(ctx, tx, clientEvent(domain.EventClientCreated, actorID, &c))
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) UpdateClient(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Client, error) {
	var updated *domain.Client
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var c domain.Client
		err := tx.QueryRow(ctx, `
			SELECT id::text, tenant_id::text, name, tier, kind, color, standard, expected_touch_days, archived_at, version, created_at, updated_at
			FROM clients WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID).
			Scan(&c.ID, &c.TenantID, &c.Name, &c.Tier, &c.Kind, &c.Color, &c.Standard,
				&c.ExpectedTouchDays, &c.ArchivedAt, &c.Version, &c.CreatedAt, &c.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if expectedVersion != nil && *expectedVersion != c.Version {
			return domain.ErrConflict
		}
		if name, ok := patch["name"].(string); ok && trim(name) != "" {
			c.Name = trim(name)
		}
		if t, ok := patch["tier"].(string); ok {
			if !domain.ValidTier(t) {
				return domain.Invalid("tier", "is invalid")
			}
			c.Tier = t
		}
		if k, ok := patch["kind"].(string); ok {
			if !domain.ValidClientKind(k) {
				return domain.Invalid("kind", "is invalid")
			}
			c.Kind = k
		}
		if col, ok := patch["color"].(string); ok {
			c.Color = col
		}
		if st, ok := patch["standard"].(string); ok {
			if err := store.CheckTextLen("standard", st); err != nil {
				return err
			}
			c.Standard = trim(st)
		}
		if v, ok := patch["expectedTouchDays"]; ok {
			n, err := touchDaysPG(v)
			if err != nil {
				return err
			}
			c.ExpectedTouchDays = n
		}
		if v, ok := patch["archived"]; ok {
			if b, _ := v.(bool); b {
				at := time.Now().UTC()
				c.ArchivedAt = &at
			} else {
				c.ArchivedAt = nil
			}
		}
		c.Version++
		// RETURNING updated_at so the response/event carry the trigger-bumped
		// timestamp (matches the memory adapter, which sets UpdatedAt to now).
		if err := tx.QueryRow(ctx,
			`UPDATE clients SET name=$2, tier=$3, kind=$4, color=$5, expected_touch_days=$6, archived_at=$7, version=$8, standard=$10 WHERE id=$1 AND tenant_id=$9 RETURNING updated_at`,
			id, c.Name, c.Tier, c.Kind, c.Color, c.ExpectedTouchDays, c.ArchivedAt, c.Version, tenantID, c.Standard).Scan(&c.UpdatedAt); err != nil {
			return err
		}
		updated = &c
		return emit(ctx, tx, clientEvent(domain.EventClientUpdated, actorID, &c))
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) DeleteClient(ctx context.Context, tenantID, actorID, id string) error {
	return s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// projects.client_id is ON DELETE SET NULL, so projects are detached automatically.
		ct, err := tx.Exec(ctx, `DELETE FROM clients WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emit(ctx, tx, domain.Event{
			ID: domain.NewID(), Type: domain.EventClientDeleted, TenantID: tenantID,
			ActorID: actorID, EntityType: domain.EntityClient, EntityID: id, At: time.Now().UTC(),
		})
	})
}

// touchDaysPG coerces a JSON patch value into a validated *int for
// expected_touch_days: nil clears it, a positive number sets it.
func touchDaysPG(v any) (*int, error) {
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

// ---- outbox / events -------------------------------------------------------

// emit writes the event to the outbox in the current transaction. The AFTER
// INSERT trigger pg_notify()s on commit, so delivery is atomic with the change.
// New entity types should go through emitEntity (outbox.go) rather than
// building an Event by hand — and never put bodies or PII in the payload.
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
		INSERT INTO outbox (id, tenant_id, type, entity_type, entity_id, actor_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		domain.NewID(), ev.TenantID, string(ev.Type), ev.EntityType, ev.EntityID, actor, payload)
	return err
}

func taskEvent(t domain.EventType, actorID string, task *domain.Task) domain.Event {
	cp := *task
	return domain.Event{ID: domain.NewID(), Type: t, TenantID: task.TenantID, ActorID: actorID, Task: &cp, EntityType: domain.EntityTask, EntityID: task.ID, At: time.Now().UTC()}
}

func clientEvent(t domain.EventType, actorID string, c *domain.Client) domain.Event {
	cp := *c
	return domain.Event{ID: domain.NewID(), Type: t, TenantID: c.TenantID, ActorID: actorID, Client: &cp, EntityType: domain.EntityClient, EntityID: c.ID, At: time.Now().UTC()}
}

func projectEvent(t domain.EventType, actorID string, p *domain.Project) domain.Event {
	cp := *p
	return domain.Event{ID: domain.NewID(), Type: t, TenantID: p.TenantID, ActorID: actorID, Project: &cp, EntityType: domain.EntityProject, EntityID: p.ID, At: time.Now().UTC()}
}

// ---- small helpers ---------------------------------------------------------

// isInvalidUUID reports whether err is Postgres refusing to read a value as a
// uuid (SQLSTATE 22P02, invalid_text_representation). A caller-supplied id
// or filter that is not a uuid can match nothing, so every statement that
// binds one maps this to the memory adapter's answer — ErrNotFound for an
// addressed row (notFoundOnBadUUID), no rows for a filter (emptyOnBadUUID) —
// and never to a 500. Cursors are checked before they reach SQL
// (store.DecodeCursor).
func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

func notFoundOnBadUUID(err error) error {
	if isInvalidUUID(err) {
		return domain.ErrNotFound
	}
	return err
}

func emptyOnBadUUID(err error) error {
	if isInvalidUUID(err) {
		return nil
	}
	return err
}

// requireProject / requireClient reject a dangling or cross-tenant reference
// as a ValidationError (400) before the INSERT/UPDATE, matching the memory
// adapter's hasProject/hasClient. The tenant-local composite FKs still hold
// as the backstop; without this check they surface as a raw constraint error
// (500) instead. nil id = no reference, nothing to check.
func requireProject(ctx context.Context, tx pgx.Tx, tenantID string, id *string) error {
	return requireRef(ctx, tx, "projects", "projectId", tenantID, id)
}

func requireClient(ctx context.Context, tx pgx.Tx, tenantID string, id *string) error {
	return requireRef(ctx, tx, "clients", "clientId", tenantID, id)
}

// requireTaskRefs checks every reference a task row carries (project, owner,
// requirement, waiting-on person) the way requireProject does, matching the
// memory adapter's checkTaskRefs. created_by has no FK and is not checked.
func requireTaskRefs(ctx context.Context, tx pgx.Tx, tenantID string, t *domain.Task) error {
	if err := requireProject(ctx, tx, tenantID, t.ProjectID); err != nil {
		return err
	}
	if err := requireRef(ctx, tx, "users", "ownerId", tenantID, t.OwnerID); err != nil {
		return err
	}
	if err := requireRef(ctx, tx, "requirements", "requirementId", tenantID, t.RequirementID); err != nil {
		return err
	}
	return requireRef(ctx, tx, "users", "waitingOnPersonId", tenantID, t.WaitingOnPersonID)
}

func requireRef(ctx context.Context, tx pgx.Tx, table, field, tenantID string, id *string) error {
	if id == nil {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1 AND tenant_id = $2)`, *id, tenantID).Scan(&ok); err != nil {
		// A non-uuid id fails the cast; that is the same "not found" to the caller.
		return domain.Invalid(field, "not found in this workspace")
	}
	if !ok {
		return domain.Invalid(field, "not found in this workspace")
	}
	return nil
}

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
