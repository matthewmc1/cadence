package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// The CRUD parity suite is the "same contract, either backend" guarantee made
// executable. For every entity it walks create → get → list → paged list →
// update (with and without a version token) → invalid patches → delete →
// not-found, and after every write it demands exactly one realtime event with
// the right type and entityId. Both adapters run it, so any drift between
// them — a stale updated_at, a raw constraint error where the other returns a
// 400, a null where the other returns [] — fails here rather than in a client.
//
// It is table-driven: Round 3/5 add an entity by appending an entityCase to
// parityCases (and, if it is paged, a paged func) — nothing else changes.

// parityRecord is the adapter-neutral view of one returned entity.
type parityRecord struct {
	ID        string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	JSON      []byte // the entity marshalled, for the normalized-JSON assertions
}

// invalidPatch is a patch the store must reject with a ValidationError on
// Field, leaving the row (and its version) untouched and emitting no event.
type invalidPatch struct {
	name  string
	patch map[string]any
	field string
}

// entityCase adapts one entity's Store methods to the generic walk.
type entityCase struct {
	name       string
	entityType string
	created    domain.EventType
	updated    domain.EventType
	deleted    domain.EventType

	// setup is optional: it runs BEFORE the event tap subscribes, so fixtures
	// an entity needs (a requirement's project) don't show up as stray events.
	setup  func(ctx context.Context, st store.Store, acct domain.Account) error
	create func(ctx context.Context, st store.Store, acct domain.Account, i int) (parityRecord, error)
	// get is optional: entities without a Get method are looked up via list.
	get    func(ctx context.Context, st store.Store, tenantID, id string) (parityRecord, error)
	list   func(ctx context.Context, st store.Store, tenantID string) ([]parityRecord, error)
	update func(ctx context.Context, st store.Store, acct domain.Account, id string, patch map[string]any, expected *int) (parityRecord, error)
	delete func(ctx context.Context, st store.Store, acct domain.Account, id string) error
	// paged is optional: the entity's keyset-paged list, returning ids in page
	// order plus the next cursor.
	paged func(ctx context.Context, st store.Store, tenantID string, p store.Page) (ids []string, next string, err error)
	// afterUpdate is an optional entity-specific hook run once the valid patch
	// has landed on the first record (e.g. to check a filtered paged list).
	afterUpdate func(t *testing.T, ctx context.Context, st store.Store, acct domain.Account, id string)

	patch   map[string]any // a valid update
	invalid []invalidPatch
	arrays  []string // top-level JSON keys that must be [] and never null
	// eventVersion reads the entity version off an event body (the typed
	// pointer for the legacy trio, entity JSON for later types); ok=false if
	// the body is missing.
	eventVersion func(ev domain.Event) (int, bool)
}

// parityN is how many rows each case creates: five walks three pages at
// limit 2 (2 + 2 + 1) and leaves enough to prove list/delete bookkeeping.
const parityN = 5

// parityCases is the hand-maintained entity table. Append here.
var parityCases = []entityCase{
	{
		name: "task", entityType: domain.EntityTask,
		created: domain.EventTaskCreated, updated: domain.EventTaskUpdated, deleted: domain.EventTaskDeleted,
		create: func(ctx context.Context, st store.Store, acct domain.Account, i int) (parityRecord, error) {
			t, err := st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: fmt.Sprintf("task %d", i)})
			if err != nil {
				return parityRecord{}, err
			}
			return taskRecord(*t), nil
		},
		get: func(ctx context.Context, st store.Store, tenantID, id string) (parityRecord, error) {
			t, err := st.GetTask(ctx, tenantID, id)
			if err != nil {
				return parityRecord{}, err
			}
			return taskRecord(*t), nil
		},
		list: func(ctx context.Context, st store.Store, tenantID string) ([]parityRecord, error) {
			ts, err := st.ListTasks(ctx, tenantID, store.TaskFilter{})
			if err != nil {
				return nil, err
			}
			out := make([]parityRecord, len(ts))
			for i, t := range ts {
				out[i] = taskRecord(t)
			}
			return out, nil
		},
		paged: func(ctx context.Context, st store.Store, tenantID string, p store.Page) ([]string, string, error) {
			res, err := st.ListTasksPaged(ctx, tenantID, store.TaskFilter{}, p)
			if err != nil {
				return nil, "", err
			}
			if res.Items == nil {
				return nil, "", errors.New("PageResult.Items is nil (must be [] not null)")
			}
			ids := make([]string, len(res.Items))
			for i, t := range res.Items {
				ids[i] = t.ID
			}
			return ids, res.NextCursor, nil
		},
		update: func(ctx context.Context, st store.Store, acct domain.Account, id string, patch map[string]any, expected *int) (parityRecord, error) {
			t, err := st.UpdateTask(ctx, acct.TenantID, acct.UserID, id, patch, expected)
			if err != nil {
				return parityRecord{}, err
			}
			return taskRecord(*t), nil
		},
		delete: func(ctx context.Context, st store.Store, acct domain.Account, id string) error {
			return st.DeleteTask(ctx, acct.TenantID, acct.UserID, id)
		},
		afterUpdate: func(t *testing.T, ctx context.Context, st store.Store, acct domain.Account, id string) {
			t.Helper()
			// The valid patch marks the row done; the paged list must honour the
			// same filter as ListTasks and see exactly that row.
			done := domain.StatusDone
			res, err := st.ListTasksPaged(ctx, acct.TenantID, store.TaskFilter{Status: &done}, store.Page{Limit: 2})
			if err != nil {
				t.Fatalf("ListTasksPaged(status=done): %v", err)
			}
			if len(res.Items) != 1 || res.Items[0].ID != id || res.NextCursor != "" {
				t.Errorf("ListTasksPaged(status=done): want exactly [%s] and no cursor, got %d item(s) next=%q", id, len(res.Items), res.NextCursor)
			}
			if res.Items[0].DoneAt == nil {
				t.Error("task marked done has no doneAt")
			}
			// status=done must have derived stage=done, and the stage filter
			// must agree with the status filter on the same row.
			if res.Items[0].Stage != domain.StageDone {
				t.Errorf("status=done: want stage=done, got %q", res.Items[0].Stage)
			}
			doneStage := domain.StageDone
			byStage, err := st.ListTasks(ctx, acct.TenantID, store.TaskFilter{Stage: &doneStage})
			if err != nil {
				t.Fatalf("ListTasks(stage=done): %v", err)
			}
			if len(byStage) != 1 || byStage[0].ID != id {
				t.Errorf("ListTasks(stage=done): want exactly [%s], got %d item(s)", id, len(byStage))
			}
		},
		patch: map[string]any{"title": "renamed", "status": "done", "links": []any{map[string]any{"label": "spec", "url": "https://example.test/spec"}}},
		invalid: []invalidPatch{
			{"empty title", map[string]any{"title": "   "}, "title"},
			{"bad kind type", map[string]any{"kind": 42.0}, "kind"},
			{"bad status", map[string]any{"status": "nope"}, "status"},
			{"negative effort", map[string]any{"effortMinutes": -5.0}, "effortMinutes"},
			{"bad scheduledAt", map[string]any{"scheduledAt": "yesterday"}, "scheduledAt"},
			{"bad recurrence", map[string]any{"recurrence": "fortnightly"}, "recurrence"},
			{"bad links", map[string]any{"links": "not-a-list"}, "links"},
			{"dangling projectId", map[string]any{"projectId": domain.NewID()}, "projectId"},
			{"bad stage", map[string]any{"stage": "blocked"}, "stage"},
			{"waiting with nothing to wait on", map[string]any{"stage": "waiting"}, "waitingOnReason"},
			{"dangling ownerId", map[string]any{"ownerId": domain.NewID()}, "ownerId"},
			{"dangling requirementId", map[string]any{"requirementId": domain.NewID()}, "requirementId"},
			{"dangling waitingOnPersonId", map[string]any{"stage": "waiting", "waitingOnPersonId": domain.NewID()}, "waitingOnPersonId"},
			{"oversize ask", map[string]any{"ask": map[string]any{"what": strings.Repeat("x", domain.MaxAskFieldLen+1)}}, "ask"},
			{"ask not an object", map[string]any{"ask": "please"}, "ask"},
			{"bad askBy", map[string]any{"askBy": "next week"}, "askBy"},
		},
		arrays: []string{"links", "subtasks", "assignees"},
		eventVersion: func(ev domain.Event) (int, bool) {
			return versionOf(ev.Task != nil, func() int { return ev.Task.Version })
		},
	},
	{
		name: "project", entityType: domain.EntityProject,
		created: domain.EventProjectCreated, updated: domain.EventProjectUpdated, deleted: domain.EventProjectDeleted,
		create: func(ctx context.Context, st store.Store, acct domain.Account, i int) (parityRecord, error) {
			p, err := st.CreateProject(ctx, acct.TenantID, acct.UserID, domain.CreateProjectInput{Name: fmt.Sprintf("project %d", i)})
			if err != nil {
				return parityRecord{}, err
			}
			return projectRecord(*p), nil
		},
		list: func(ctx context.Context, st store.Store, tenantID string) ([]parityRecord, error) {
			ps, err := st.ListProjects(ctx, tenantID)
			if err != nil {
				return nil, err
			}
			out := make([]parityRecord, len(ps))
			for i, p := range ps {
				out[i] = projectRecord(p)
			}
			return out, nil
		},
		update: func(ctx context.Context, st store.Store, acct domain.Account, id string, patch map[string]any, expected *int) (parityRecord, error) {
			p, err := st.UpdateProject(ctx, acct.TenantID, acct.UserID, id, patch, expected)
			if err != nil {
				return parityRecord{}, err
			}
			return projectRecord(*p), nil
		},
		delete: func(ctx context.Context, st store.Store, acct domain.Account, id string) error {
			return st.DeleteProject(ctx, acct.TenantID, acct.UserID, id)
		},
		patch: map[string]any{"name": "renamed", "subtitle": "with a subtitle", "due": "2026-12-01", "outcome": "shipped and signed off", "archived": true},
		invalid: []invalidPatch{
			{"dangling clientId", map[string]any{"clientId": domain.NewID()}, "clientId"},
		},
		arrays: []string{"members"},
		eventVersion: func(ev domain.Event) (int, bool) {
			return versionOf(ev.Project != nil, func() int { return ev.Project.Version })
		},
	},
	{
		name: "client", entityType: domain.EntityClient,
		created: domain.EventClientCreated, updated: domain.EventClientUpdated, deleted: domain.EventClientDeleted,
		create: func(ctx context.Context, st store.Store, acct domain.Account, i int) (parityRecord, error) {
			c, err := st.CreateClient(ctx, acct.TenantID, acct.UserID, domain.CreateClientInput{Name: fmt.Sprintf("client %d", i)})
			if err != nil {
				return parityRecord{}, err
			}
			return clientRecord(*c), nil
		},
		list: func(ctx context.Context, st store.Store, tenantID string) ([]parityRecord, error) {
			cs, err := st.ListClients(ctx, tenantID)
			if err != nil {
				return nil, err
			}
			out := make([]parityRecord, len(cs))
			for i, c := range cs {
				out[i] = clientRecord(c)
			}
			return out, nil
		},
		update: func(ctx context.Context, st store.Store, acct domain.Account, id string, patch map[string]any, expected *int) (parityRecord, error) {
			c, err := st.UpdateClient(ctx, acct.TenantID, acct.UserID, id, patch, expected)
			if err != nil {
				return parityRecord{}, err
			}
			return clientRecord(*c), nil
		},
		delete: func(ctx context.Context, st store.Store, acct domain.Account, id string) error {
			return st.DeleteClient(ctx, acct.TenantID, acct.UserID, id)
		},
		patch: map[string]any{"name": "renamed", "tier": domain.TierA, "kind": domain.ClientArea, "standard": "books closed by the 5th", "expectedTouchDays": 14.0, "archived": true},
		invalid: []invalidPatch{
			{"bad tier", map[string]any{"tier": "z"}, "tier"},
			{"bad kind", map[string]any{"kind": "alien"}, "kind"},
			{"zero touch days", map[string]any{"expectedTouchDays": 0.0}, "expectedTouchDays"},
			{"non-numeric touch days", map[string]any{"expectedTouchDays": "ten"}, "expectedTouchDays"},
		},
		eventVersion: func(ev domain.Event) (int, bool) {
			return versionOf(ev.Client != nil, func() int { return ev.Client.Version })
		},
	},
	{
		name: "requirement", entityType: domain.EntityRequirement,
		created: domain.EventRequirementCreated, updated: domain.EventRequirementUpdated, deleted: domain.EventRequirementDeleted,
		setup: func(ctx context.Context, st store.Store, acct domain.Account) error {
			_, err := st.CreateProject(ctx, acct.TenantID, acct.UserID, domain.CreateProjectInput{Name: parityProjectName})
			return err
		},
		create: func(ctx context.Context, st store.Store, acct domain.Account, i int) (parityRecord, error) {
			pid, err := parityProjectID(ctx, st, acct.TenantID)
			if err != nil {
				return parityRecord{}, err
			}
			r, err := st.CreateRequirement(ctx, acct.TenantID, acct.UserID, domain.CreateRequirementInput{ProjectID: pid, Title: fmt.Sprintf("requirement %d", i)})
			if err != nil {
				return parityRecord{}, err
			}
			return requirementRecord(*r), nil
		},
		list: func(ctx context.Context, st store.Store, tenantID string) ([]parityRecord, error) {
			rs, err := st.ListRequirements(ctx, tenantID, store.RequirementFilter{})
			if err != nil {
				return nil, err
			}
			out := make([]parityRecord, len(rs))
			for i, r := range rs {
				out[i] = requirementRecord(r)
			}
			return out, nil
		},
		update: func(ctx context.Context, st store.Store, acct domain.Account, id string, patch map[string]any, expected *int) (parityRecord, error) {
			r, err := st.UpdateRequirement(ctx, acct.TenantID, acct.UserID, id, patch, expected)
			if err != nil {
				return parityRecord{}, err
			}
			return requirementRecord(*r), nil
		},
		delete: func(ctx context.Context, st store.Store, acct domain.Account, id string) error {
			return st.DeleteRequirement(ctx, acct.TenantID, acct.UserID, id)
		},
		afterUpdate: func(t *testing.T, ctx context.Context, st store.Store, acct domain.Account, id string) {
			t.Helper()
			// The project filter must see every requirement (they all share the
			// fixture project) and an unknown project must see none.
			pid, err := parityProjectID(ctx, st, acct.TenantID)
			if err != nil {
				t.Fatalf("parityProjectID: %v", err)
			}
			rs, err := st.ListRequirements(ctx, acct.TenantID, store.RequirementFilter{ProjectID: &pid})
			if err != nil {
				t.Fatalf("ListRequirements(projectId): %v", err)
			}
			if len(rs) != parityN {
				t.Errorf("ListRequirements(projectId): want %d, got %d", parityN, len(rs))
			}
			for _, r := range rs {
				if r.ID == id && (r.Status != domain.RequirementMet || r.Weight != 5 || r.ArchivedAt == nil) {
					t.Errorf("ListRequirements(projectId): patch not reflected: status=%s weight=%d archivedAt=%v", r.Status, r.Weight, r.ArchivedAt)
				}
			}
			other := domain.NewID()
			rs, err = st.ListRequirements(ctx, acct.TenantID, store.RequirementFilter{ProjectID: &other})
			if err != nil {
				t.Fatalf("ListRequirements(unknown projectId): %v", err)
			}
			if len(rs) != 0 {
				t.Errorf("ListRequirements(unknown projectId): want 0, got %d", len(rs))
			}
		},
		patch: map[string]any{"title": "renamed", "status": domain.RequirementMet, "weight": 5.0, "acceptance": "signed off", "archived": true},
		invalid: []invalidPatch{
			{"empty title", map[string]any{"title": "   "}, "title"},
			{"weight too low", map[string]any{"weight": 0.0}, "weight"},
			{"weight too high", map[string]any{"weight": 6.0}, "weight"},
			{"fractional weight", map[string]any{"weight": 2.5}, "weight"},
			{"bad status", map[string]any{"status": "maybe"}, "status"},
			{"null projectId", map[string]any{"projectId": nil}, "projectId"},
			{"dangling projectId", map[string]any{"projectId": domain.NewID()}, "projectId"},
			{"bad description", map[string]any{"description": 7.0}, "description"},
		},
		eventVersion: func(ev domain.Event) (int, bool) {
			var r domain.Requirement
			if len(ev.Entity) == 0 || json.Unmarshal(ev.Entity, &r) != nil {
				return 0, false
			}
			return r.Version, true
		},
	},
}

// parityProjectName is the fixture project every parity requirement hangs off.
const parityProjectName = "requirements fixture"

// parityProjectID finds the fixture project created by the requirement case's
// setup. Looking it up (rather than caching the id) keeps the case stateless.
func parityProjectID(ctx context.Context, st store.Store, tenantID string) (string, error) {
	ps, err := st.ListProjects(ctx, tenantID)
	if err != nil {
		return "", err
	}
	for _, p := range ps {
		if p.Name == parityProjectName {
			return p.ID, nil
		}
	}
	return "", errors.New("parity: fixture project not found (setup did not run?)")
}

// AssertCRUDParity runs every parityCase against st inside a fresh tenant for
// email. Each case subscribes to the tenant first, so event accounting is
// exact: one event per write, in write order, and none for a rejected write.
func AssertCRUDParity(t *testing.T, st store.Store, email string) {
	t.Helper()
	ctx := context.Background()
	acct, err := st.FindOrCreateAccount(ctx, email)
	if err != nil {
		t.Fatalf("FindOrCreateAccount(%s): %v", email, err)
	}
	for _, c := range parityCases {
		t.Run(c.name, func(t *testing.T) { runParityCase(t, ctx, st, acct, c) })
	}
	t.Run("task lifecycle", func(t *testing.T) { runTaskLifecycleParity(t, ctx, st, acct) })
	t.Run("audit", func(t *testing.T) { runAuditParity(t, ctx, st, acct) })
	t.Run("signals", func(t *testing.T) { runSignalParity(t, ctx, st, acct) })
	t.Run("signal retention", func(t *testing.T) { runSignalRetentionParity(t, ctx, st, acct) })
}

// runTaskLifecycleParity is the work-item walk (0013): status and stage are
// one lifecycle in two columns, waiting needs a reason or a person, a
// deleted requirement lets go of its tasks, and the ask round-trips. It is
// separate from the generic walk because it needs several rows and its own
// sequence of patches; it does not do event accounting (the generic walk
// already proves one event per write).
func runTaskLifecycleParity(t *testing.T, ctx context.Context, st store.Store, acct domain.Account) {
	t.Helper()
	create := func(in domain.CreateTaskInput) *domain.Task {
		t.Helper()
		task, err := st.CreateTask(ctx, acct.TenantID, acct.UserID, in)
		if err != nil {
			t.Fatalf("CreateTask(%+v): %v", in, err)
		}
		return task
	}
	patch := func(id string, p map[string]any) *domain.Task {
		t.Helper()
		task, err := st.UpdateTask(ctx, acct.TenantID, acct.UserID, id, p, nil)
		if err != nil {
			t.Fatalf("UpdateTask(%s, %v): %v", id, p, err)
		}
		return task
	}
	wantLifecycle := func(step string, task *domain.Task, status domain.Status, stage domain.Stage) {
		t.Helper()
		if task.Status != status || task.Stage != stage {
			t.Errorf("%s: want status=%s stage=%s, got status=%s stage=%s", step, status, stage, task.Status, task.Stage)
		}
	}
	// Tidy so the rest of the suite starts from an empty tenant, then drain:
	// on Postgres the tidy's own events arrive asynchronously and would
	// otherwise land in the next sub-test's tap. (unsub is deferred first so
	// it runs after the drain.)
	sub, unsub := st.Subscribe(acct.TenantID)
	defer unsub()
	var ids []string
	var projectID string
	defer func() {
		for _, id := range ids {
			_ = st.DeleteTask(ctx, acct.TenantID, acct.UserID, id)
		}
		if projectID != "" {
			_ = st.DeleteProject(ctx, acct.TenantID, acct.UserID, projectID)
		}
		(&eventTap{t: t, sub: sub}).drain()
	}()

	// --- create: whichever of status/stage is given derives the other ---
	when := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	focus, sched, done := domain.StatusFocus, domain.StatusScheduled, domain.StatusDone
	doing, waiting, todo := domain.StageDoing, domain.StageWaiting, domain.StageTodo
	for _, c := range []struct {
		name   string
		in     domain.CreateTaskInput
		status domain.Status
		stage  domain.Stage
	}{
		{"default", domain.CreateTaskInput{Title: "default"}, domain.StatusBacklog, todo},
		{"status=focus", domain.CreateTaskInput{Title: "focus", Status: &focus}, focus, doing},
		{"status=scheduled", domain.CreateTaskInput{Title: "scheduled", Status: &sched, ScheduledAt: &when}, sched, todo},
		{"stage=doing", domain.CreateTaskInput{Title: "doing", Stage: &doing}, focus, doing},
		{"stage=todo+scheduledAt", domain.CreateTaskInput{Title: "todo scheduled", Stage: &todo, ScheduledAt: &when}, sched, todo},
		{"stage wins over status", domain.CreateTaskInput{Title: "both", Status: &done, Stage: &doing}, focus, doing},
		{"stage=waiting", domain.CreateTaskInput{Title: "waiting", Stage: &waiting, WaitingOnReason: ptrOf("legal")}, domain.StatusBacklog, waiting},
	} {
		task := create(c.in)
		ids = append(ids, task.ID)
		wantLifecycle("create "+c.name, task, c.status, c.stage)
		if task.CreatedBy == nil || *task.CreatedBy != acct.UserID {
			t.Errorf("create %s: want createdBy=%s, got %v", c.name, acct.UserID, task.CreatedBy)
		}
		if got, err := st.GetTask(ctx, acct.TenantID, task.ID); err != nil {
			t.Fatalf("GetTask: %v", err)
		} else {
			wantLifecycle("get "+c.name, got, c.status, c.stage)
		}
		if c.stage == waiting && task.WaitingOnSince == nil {
			t.Errorf("create %s: waiting must stamp waitingOnSince", c.name)
		}
	}
	_, err := st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "x", Stage: &waiting})
	assertInvalid(t, "CreateTask(stage=waiting, nothing to wait on)", err, "waitingOnReason")
	bad := domain.Stage("blocked")
	_, err = st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "x", Stage: &bad})
	assertInvalid(t, "CreateTask(bad stage)", err, "stage")
	_, err = st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "x", RequirementID: ptrOf(domain.NewID())})
	assertInvalid(t, "CreateTask(dangling requirementId)", err, "requirementId")
	_, err = st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "x", OwnerID: ptrOf(domain.NewID())})
	assertInvalid(t, "CreateTask(dangling ownerId)", err, "ownerId")
	_, err = st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "x", Ask: &domain.Ask{Why: strings.Repeat("y", domain.MaxAskFieldLen+1)}})
	assertInvalid(t, "CreateTask(oversize ask)", err, "ask")

	// --- patch: the same derivation, in both directions ---
	task := create(domain.CreateTaskInput{Title: "lifecycle"})
	ids = append(ids, task.ID)
	wantLifecycle("patch stage=doing", patch(task.ID, map[string]any{"stage": "doing"}), focus, doing)
	wantLifecycle("patch status=scheduled", patch(task.ID, map[string]any{"status": "scheduled", "scheduledAt": when.Format(time.RFC3339)}), sched, todo)
	wantLifecycle("patch stage=todo (scheduledAt set)", patch(task.ID, map[string]any{"stage": "todo"}), sched, todo)
	wantLifecycle("patch stage=todo (scheduledAt cleared)", patch(task.ID, map[string]any{"stage": "todo", "scheduledAt": nil}), domain.StatusBacklog, todo)
	got := patch(task.ID, map[string]any{"status": "done"})
	wantLifecycle("patch status=done", got, done, domain.StageDone)
	if got.DoneAt == nil {
		t.Error("patch status=done: no doneAt")
	}
	got = patch(task.ID, map[string]any{"stage": "todo"})
	wantLifecycle("patch stage=todo from done", got, domain.StatusBacklog, todo)
	if got.DoneAt != nil {
		t.Error("leaving done must clear doneAt")
	}
	// a patch touching neither leaves the pair alone
	wantLifecycle("patch title only", patch(task.ID, map[string]any{"title": "still todo"}), domain.StatusBacklog, todo)

	// --- waiting: needs a reason or a person; since is stamped and cleared ---
	got = patch(task.ID, map[string]any{"stage": "waiting", "waitingOnReason": "  waiting on legal  "})
	wantLifecycle("patch stage=waiting", got, domain.StatusBacklog, waiting)
	if got.WaitingOnReason != "waiting on legal" || got.WaitingOnSince == nil {
		t.Errorf("waiting: want trimmed reason and a since, got reason=%q since=%v", got.WaitingOnReason, got.WaitingOnSince)
	}
	since := got.WaitingOnSince
	got = patch(task.ID, map[string]any{"note": "still waiting"})
	if got.WaitingOnSince == nil || !got.WaitingOnSince.Equal(*since) {
		t.Errorf("staying in waiting must keep since: before=%v after=%v", since, got.WaitingOnSince)
	}
	_, err = st.UpdateTask(ctx, acct.TenantID, acct.UserID, task.ID, map[string]any{"waitingOnReason": ""}, nil)
	assertInvalid(t, "patch(clear the only reason while waiting)", err, "waitingOnReason")
	got = patch(task.ID, map[string]any{"waitingOnReason": "", "waitingOnPersonId": acct.UserID})
	if got.WaitingOnPersonID == nil || *got.WaitingOnPersonID != acct.UserID || got.Stage != waiting {
		t.Errorf("waiting on a person: want personId=%s stage=waiting, got %v/%s", acct.UserID, got.WaitingOnPersonID, got.Stage)
	}
	got = patch(task.ID, map[string]any{"stage": "doing"})
	wantLifecycle("leave waiting", got, focus, doing)
	if got.WaitingOnSince != nil {
		t.Error("leaving waiting must clear since")
	}
	// status alone can pull a row out of waiting (a status-only client)
	_ = patch(task.ID, map[string]any{"stage": "waiting", "waitingOnReason": "again"})
	got = patch(task.ID, map[string]any{"status": "focus"})
	wantLifecycle("status=focus from waiting", got, focus, doing)
	if got.WaitingOnSince != nil {
		t.Error("status-driven exit from waiting must clear since")
	}

	// --- owner / requirement refs, and the SET NULL when a requirement goes ---
	got = patch(task.ID, map[string]any{"ownerId": acct.UserID, "definitionOfDone": "SOW countersigned"})
	if got.OwnerID == nil || *got.OwnerID != acct.UserID || got.DefinitionOfDone != "SOW countersigned" {
		t.Errorf("owner/definitionOfDone did not land: %v %q", got.OwnerID, got.DefinitionOfDone)
	}
	proj, err := st.CreateProject(ctx, acct.TenantID, acct.UserID, domain.CreateProjectInput{Name: "lifecycle fixture"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	projectID = proj.ID
	req, err := st.CreateRequirement(ctx, acct.TenantID, acct.UserID, domain.CreateRequirementInput{ProjectID: proj.ID, Title: "signed SOW"})
	if err != nil {
		t.Fatalf("CreateRequirement: %v", err)
	}
	got = patch(task.ID, map[string]any{"requirementId": req.ID})
	if got.RequirementID == nil || *got.RequirementID != req.ID {
		t.Errorf("requirementId did not land: %v", got.RequirementID)
	}
	linked := create(domain.CreateTaskInput{Title: "derived from requirement", RequirementID: &req.ID, ProjectID: &proj.ID})
	ids = append(ids, linked.ID)
	if err := st.DeleteRequirement(ctx, acct.TenantID, acct.UserID, req.ID); err != nil {
		t.Fatalf("DeleteRequirement: %v", err)
	}
	for _, id := range []string{task.ID, linked.ID} {
		after, err := st.GetTask(ctx, acct.TenantID, id)
		if err != nil {
			t.Fatalf("GetTask after requirement delete: %v", err)
		}
		if after.RequirementID != nil {
			t.Errorf("deleting a requirement must SET NULL its tasks' requirementId, task %s still has %s", id, *after.RequirementID)
		}
		if after.Version != got.Version && id == task.ID {
			t.Errorf("SET NULL must not bump version: want %d, got %d", got.Version, after.Version)
		}
	}

	// --- ask: bounded object, round-trips, null clears; askBy is hoisted ---
	askBy := when.Add(48 * time.Hour)
	got = patch(task.ID, map[string]any{
		"ask":   map[string]any{"what": "countersign", "forWhom": "legal", "why": "kickoff Monday"},
		"askBy": askBy.Format(time.RFC3339),
	})
	want := domain.Ask{What: "countersign", ForWhom: "legal", Why: "kickoff Monday"}
	if got.Ask != want || got.AskBy == nil || !got.AskBy.Equal(askBy) {
		t.Errorf("ask did not round-trip: ask=%+v askBy=%v", got.Ask, got.AskBy)
	}
	if again, err := st.GetTask(ctx, acct.TenantID, task.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	} else if again.Ask != want {
		t.Errorf("ask did not survive a read: %+v", again.Ask)
	}
	got = patch(task.ID, map[string]any{"ask": nil, "askBy": nil})
	if !got.Ask.IsZero() || got.AskBy != nil {
		t.Errorf("ask null must clear: ask=%+v askBy=%v", got.Ask, got.AskBy)
	}
	var obj map[string]json.RawMessage
	if b, _ := json.Marshal(got); json.Unmarshal(b, &obj) != nil || string(obj["ask"]) != `{"what":"","forWhom":"","why":""}` {
		t.Errorf("ask must marshal as an object, got %s", obj["ask"])
	}
}

func runParityCase(t *testing.T, ctx context.Context, st store.Store, acct domain.Account, c entityCase) {
	t.Helper()
	sub, unsub := st.Subscribe(acct.TenantID)
	defer unsub()
	events := eventTap{t: t, sub: sub, entityType: c.entityType, versionOf: c.eventVersion}
	if c.setup != nil {
		// Subscribe first, then drain: on Postgres the fixture's own events
		// arrive asynchronously (LISTEN/NOTIFY), so subscribing after setup
		// would still catch them a few ms later.
		if err := c.setup(ctx, st, acct); err != nil {
			t.Fatalf("setup: %v", err)
		}
		events.drain()
	}

	get := c.get
	if get == nil {
		get = func(ctx context.Context, st store.Store, tenantID, id string) (parityRecord, error) {
			recs, err := c.list(ctx, st, tenantID)
			if err != nil {
				return parityRecord{}, err
			}
			for _, r := range recs {
				if r.ID == id {
					return r, nil
				}
			}
			return parityRecord{}, domain.ErrNotFound
		}
	}

	// --- empty tenant: an empty first page is [] with no cursor ---
	if c.paged != nil {
		ids, next, err := c.paged(ctx, st, acct.TenantID, store.Page{})
		if err != nil {
			t.Fatalf("paged(empty): %v", err)
		}
		if len(ids) != 0 || next != "" {
			t.Errorf("paged(empty): want no items and no cursor, got %d item(s) next=%q", len(ids), next)
		}
	}

	// --- create ---
	created := make([]parityRecord, parityN)
	for i := range created {
		rec, err := c.create(ctx, st, acct, i)
		if err != nil {
			t.Fatalf("create #%d: %v", i, err)
		}
		if rec.ID == "" {
			t.Fatalf("create #%d: empty id", i)
		}
		if rec.Version != 1 {
			t.Errorf("create #%d: want version 1, got %d", i, rec.Version)
		}
		if rec.CreatedAt.IsZero() || !rec.UpdatedAt.Equal(rec.CreatedAt) {
			t.Errorf("create #%d: want updatedAt == createdAt (non-zero), got created=%v updated=%v", i, rec.CreatedAt, rec.UpdatedAt)
		}
		assertArrays(t, "create", rec, c.arrays)
		events.expect(c.created, rec.ID, 1)
		created[i] = rec
	}
	first := created[0]

	// --- get ---
	got, err := get(ctx, st, acct.TenantID, first.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Version != 1 || !got.CreatedAt.Equal(first.CreatedAt) || !got.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("get: want version 1 and the timestamps create returned, got version=%d created=%v (create said %v) updated=%v (create said %v)",
			got.Version, got.CreatedAt, first.CreatedAt, got.UpdatedAt, first.UpdatedAt)
	}
	assertArrays(t, "get", got, c.arrays)
	if _, err := get(ctx, st, acct.TenantID, domain.NewID()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get(unknown id): want ErrNotFound, got %v", err)
	}

	// --- list ---
	listed, err := c.list(ctx, st, acct.TenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	assertIDs(t, "list", idsOf(listed), idsOf(created), false)
	for _, r := range listed {
		assertArrays(t, "list", r, c.arrays)
	}

	// --- paged list: walk 3 pages at limit 2, newest first, no gaps/dupes ---
	if c.paged != nil {
		var walked []string
		cursor := ""
		for page := 1; ; page++ {
			ids, next, err := c.paged(ctx, st, acct.TenantID, store.Page{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatalf("paged page %d: %v", page, err)
			}
			walked = append(walked, ids...)
			if page < 3 && (len(ids) != 2 || next == "") {
				t.Fatalf("paged page %d: want 2 items and a cursor, got %d item(s) next=%q", page, len(ids), next)
			}
			if page == 3 {
				if len(ids) != 1 || next != "" {
					t.Fatalf("paged page 3: want 1 item and no cursor, got %d item(s) next=%q", len(ids), next)
				}
				break
			}
			cursor = next
		}
		want := make([]string, 0, parityN)
		for i := parityN - 1; i >= 0; i-- { // newest first == reverse creation order
			want = append(want, created[i].ID)
		}
		assertIDs(t, "paged walk", walked, want, true)

		ids, next, err := c.paged(ctx, st, acct.TenantID, store.Page{}) // default limit covers everything
		if err != nil {
			t.Fatalf("paged(default limit): %v", err)
		}
		if next != "" {
			t.Errorf("paged(default limit): unexpected cursor %q", next)
		}
		assertIDs(t, "paged(default limit)", ids, want, true)

		_, _, err = c.paged(ctx, st, acct.TenantID, store.Page{Cursor: "not a cursor"})
		assertInvalid(t, "paged(bad cursor)", err, "cursor")
	}

	// --- update, no version token ---
	updated, err := c.update(ctx, st, acct, first.ID, c.patch, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ID != first.ID || updated.Version != 2 {
		t.Errorf("update: want same id and version 2, got id=%s version=%d", updated.ID, updated.Version)
	}
	if !updated.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("update: updatedAt did not advance: before=%v after=%v", first.UpdatedAt, updated.UpdatedAt)
	}
	if !updated.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("update: createdAt changed: before=%v after=%v", first.CreatedAt, updated.CreatedAt)
	}
	assertArrays(t, "update", updated, c.arrays)
	events.expect(c.updated, first.ID, 2)
	if got, err := get(ctx, st, acct.TenantID, first.ID); err != nil {
		t.Fatalf("get after update: %v", err)
	} else if got.Version != 2 || !got.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Errorf("get after update: want version 2 / updatedAt %v (what update returned), got version=%d updatedAt=%v", updated.UpdatedAt, got.Version, got.UpdatedAt)
	}
	if c.afterUpdate != nil {
		c.afterUpdate(t, ctx, st, acct, first.ID)
	}

	// --- update with a stale token: conflict, nothing changes, no event ---
	stale := 1
	if _, err := c.update(ctx, st, acct, first.ID, c.patch, &stale); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("update(expectedVersion=1 on version 2): want ErrConflict, got %v", err)
	}
	// --- update with the right token ---
	current := 2
	updated2, err := c.update(ctx, st, acct, first.ID, c.patch, &current)
	if err != nil {
		t.Fatalf("update(expectedVersion=2): %v", err)
	}
	if updated2.Version != 3 || !updated2.UpdatedAt.After(updated.UpdatedAt) {
		t.Errorf("update(expectedVersion=2): want version 3 and a later updatedAt, got version=%d updatedAt=%v (prev %v)", updated2.Version, updated2.UpdatedAt, updated.UpdatedAt)
	}
	events.expect(c.updated, first.ID, 3)

	// --- invalid patches: same rejection on every backend, row untouched ---
	for _, inv := range c.invalid {
		_, err := c.update(ctx, st, acct, first.ID, inv.patch, nil)
		assertInvalid(t, "update("+inv.name+")", err, inv.field)
		if got, err := get(ctx, st, acct.TenantID, first.ID); err != nil {
			t.Fatalf("get after invalid patch %q: %v", inv.name, err)
		} else if got.Version != 3 {
			t.Errorf("invalid patch %q mutated the row: version %d", inv.name, got.Version)
		}
	}

	// --- delete → not found everywhere ---
	if err := c.delete(ctx, st, acct, first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	events.expect(c.deleted, first.ID, 0)
	if _, err := get(ctx, st, acct.TenantID, first.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get after delete: want ErrNotFound, got %v", err)
	}
	if _, err := c.update(ctx, st, acct, first.ID, c.patch, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("update after delete: want ErrNotFound, got %v", err)
	}
	if err := c.delete(ctx, st, acct, first.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second delete: want ErrNotFound, got %v", err)
	}
	listed, err = c.list(ctx, st, acct.TenantID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	assertIDs(t, "list after delete", idsOf(listed), idsOf(created[1:]), false)

	// --- tidy the rest, one event each, then prove nothing else was emitted ---
	for _, rec := range created[1:] {
		if err := c.delete(ctx, st, acct, rec.ID); err != nil {
			t.Fatalf("delete %s: %v", rec.ID, err)
		}
		events.expect(c.deleted, rec.ID, 0)
	}
	events.expectNone()
}

// runAuditParity is the append-only entity's walk: append → paged list. It
// checks what the store fills in (id, at, {} detail), what it rejects (empty
// kind, oversize detail, bad cursor), newest-first paging with no gaps or
// dupes, that every field round-trips, and that appending emits no event.
func runAuditParity(t *testing.T, ctx context.Context, st store.Store, acct domain.Account) {
	t.Helper()
	sub, unsub := st.Subscribe(acct.TenantID)
	defer unsub()
	events := eventTap{t: t, sub: sub}

	ids, next, err := pagedAudit(ctx, st, acct.TenantID, store.Page{})
	if err != nil {
		t.Fatalf("ListAudit(empty): %v", err)
	}
	if len(ids) != 0 || next != "" {
		t.Errorf("ListAudit(empty): want no items and no cursor, got %d item(s) next=%q", len(ids), next)
	}

	// --- rejected appends: nothing stored, no event ---
	if _, err := st.AppendAudit(ctx, acct.TenantID, domain.AuditEntry{Kind: "  "}); err == nil {
		t.Error("AppendAudit(empty kind): want ValidationError, got nil")
	} else {
		assertInvalid(t, "AppendAudit(empty kind)", err, "kind")
	}
	big := json.RawMessage(`"` + strings.Repeat("x", domain.MaxAuditDetailBytes) + `"`)
	_, err = st.AppendAudit(ctx, acct.TenantID, domain.AuditEntry{Kind: domain.AuditAIChat, Detail: big})
	assertInvalid(t, "AppendAudit(oversize detail)", err, "detail")
	_, err = st.AppendAudit(ctx, acct.TenantID, domain.AuditEntry{Kind: domain.AuditAIChat, Detail: json.RawMessage(`{not json`)})
	assertInvalid(t, "AppendAudit(malformed detail)", err, "detail")

	// --- append: the store fills in id/at/detail and returns what it stored ---
	entityID := domain.NewID()
	inputs := []domain.AuditEntry{
		{Kind: domain.AuditLoginVerified, ActorID: &acct.UserID},
		{Kind: domain.AuditTokenCreate, ActorID: &acct.UserID, EntityType: ptrOf(domain.EntityAPIToken), EntityID: &entityID,
			Detail: json.RawMessage(`{"name":"mcp"}`), IPHash: ptrOf("abc123")},
		{Kind: domain.AuditAIChat, ActorID: &acct.UserID, Detail: json.RawMessage(`{"model":"gemma3:4b","promptChars":120,"ok":true}`)},
		{Kind: "connector.calendar.sync"}, // system-initiated: no actor, open-ended kind
		{Kind: domain.AuditTokenRevoke, ActorID: &acct.UserID, EntityType: ptrOf(""), EntityID: &entityID}, // "" pointer → nil
	}
	appended := make([]domain.AuditEntry, len(inputs))
	for i, in := range inputs {
		got, err := st.AppendAudit(ctx, acct.TenantID, in)
		if err != nil {
			t.Fatalf("AppendAudit #%d: %v", i, err)
		}
		if got.ID == "" || got.At.IsZero() || got.TenantID != acct.TenantID {
			t.Errorf("AppendAudit #%d: store must fill id/at/tenant, got id=%q at=%v tenant=%q", i, got.ID, got.At, got.TenantID)
		}
		if len(in.Detail) == 0 && string(got.Detail) != "{}" {
			t.Errorf("AppendAudit #%d: nil detail must become {}, got %s", i, got.Detail)
		}
		if in.EntityType != nil && *in.EntityType == "" && got.EntityType != nil {
			t.Errorf("AppendAudit #%d: empty entityType must be normalised to nil", i)
		}
		appended[i] = *got
	}
	events.expectNone() // appending is silent

	// --- paged list: newest first, 2+2+1, every field round-trips ---
	var walked []string
	cursor := ""
	for page := 1; ; page++ {
		res, err := st.ListAudit(ctx, acct.TenantID, store.Page{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListAudit page %d: %v", page, err)
		}
		if res.Items == nil {
			t.Fatal("ListAudit: Items is nil (must be [] not null)")
		}
		for _, e := range res.Items {
			walked = append(walked, e.ID)
			assertAuditRoundTrip(t, e, appended)
		}
		if page < 3 && (len(res.Items) != 2 || res.NextCursor == "") {
			t.Fatalf("ListAudit page %d: want 2 items and a cursor, got %d item(s) next=%q", page, len(res.Items), res.NextCursor)
		}
		if page == 3 {
			if len(res.Items) != 1 || res.NextCursor != "" {
				t.Fatalf("ListAudit page 3: want 1 item and no cursor, got %d item(s) next=%q", len(res.Items), res.NextCursor)
			}
			break
		}
		cursor = res.NextCursor
	}
	want := make([]string, 0, len(appended))
	for i := len(appended) - 1; i >= 0; i-- {
		want = append(want, appended[i].ID)
	}
	assertIDs(t, "ListAudit walk", walked, want, true)

	ids, next, err = pagedAudit(ctx, st, acct.TenantID, store.Page{})
	if err != nil {
		t.Fatalf("ListAudit(default limit): %v", err)
	}
	if next != "" {
		t.Errorf("ListAudit(default limit): unexpected cursor %q", next)
	}
	assertIDs(t, "ListAudit(default limit)", ids, want, true)

	_, _, err = pagedAudit(ctx, st, acct.TenantID, store.Page{Cursor: "not a cursor"})
	assertInvalid(t, "ListAudit(bad cursor)", err, "cursor")
}

// assertAuditRoundTrip checks a listed row equals the row append returned.
func assertAuditRoundTrip(t *testing.T, got domain.AuditEntry, appended []domain.AuditEntry) {
	t.Helper()
	for _, a := range appended {
		if a.ID != got.ID {
			continue
		}
		if !a.At.Equal(got.At) || a.Kind != got.Kind || !ptrEq(a.ActorID, got.ActorID) ||
			!ptrEq(a.EntityType, got.EntityType) || !ptrEq(a.EntityID, got.EntityID) || !ptrEq(a.IPHash, got.IPHash) {
			t.Errorf("ListAudit: row %s differs from what append returned:\n append: %+v\n   list: %+v", got.ID, a, got)
		}
		var wantD, gotD any
		if json.Unmarshal(a.Detail, &wantD) != nil || json.Unmarshal(got.Detail, &gotD) != nil || fmt.Sprint(wantD) != fmt.Sprint(gotD) {
			t.Errorf("ListAudit: row %s detail differs: append=%s list=%s", got.ID, a.Detail, got.Detail)
		}
		return
	}
	t.Errorf("ListAudit: returned row %s that was never appended", got.ID)
}

func pagedAudit(ctx context.Context, st store.Store, tenantID string, p store.Page) ([]string, string, error) {
	res, err := st.ListAudit(ctx, tenantID, p)
	if err != nil {
		return nil, "", err
	}
	if res.Items == nil {
		return nil, "", errors.New("PageResult.Items is nil (must be [] not null)")
	}
	ids := make([]string, len(res.Items))
	for i, e := range res.Items {
		ids[i] = e.ID
	}
	return ids, res.NextCursor, nil
}

func ptrOf(s string) *string { return &s }

func ptrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// eventTap consumes a tenant subscription one event at a time so the suite
// can insist on exactly one event per write, in write order.
type eventTap struct {
	t          *testing.T
	sub        <-chan domain.Event
	entityType string
	versionOf  func(domain.Event) (int, bool)
}

// expect waits for the next event and checks it is exactly (typ, entityID),
// carrying entityType and — for creates/updates (version > 0) — a body with
// that version. Any other event arriving first is a parity failure: either a
// write emitted twice, or a rejected write emitted at all.
func (e *eventTap) expect(typ domain.EventType, entityID string, version int) {
	e.t.Helper()
	select {
	case ev, ok := <-e.sub:
		if !ok {
			e.t.Fatalf("event %s for %s: subscription closed", typ, entityID)
		}
		if ev.Type != typ || ev.EntityID != entityID {
			e.t.Fatalf("event: want %s for %s, got %s for %s", typ, entityID, ev.Type, ev.EntityID)
		}
		if ev.EntityType != e.entityType {
			e.t.Errorf("event %s: want entityType %q, got %q", typ, e.entityType, ev.EntityType)
		}
		if version > 0 {
			if v, ok := e.versionOf(ev); !ok {
				e.t.Errorf("event %s for %s: no entity body", typ, entityID)
			} else if v != version {
				e.t.Errorf("event %s for %s: want body version %d, got %d", typ, entityID, version, v)
			}
		} else if _, ok := e.versionOf(ev); ok {
			e.t.Errorf("event %s for %s: delete must not carry a body", typ, entityID)
		}
	case <-time.After(3 * time.Second):
		e.t.Fatalf("event %s for %s: not received", typ, entityID)
	}
}

// drain discards events until the channel has been quiet for a while — used
// once after fixtures are created so their events don't count against the
// entity under test.
func (e *eventTap) drain() {
	for {
		select {
		case <-e.sub:
		case <-time.After(500 * time.Millisecond):
			return
		}
	}
}

// expectNone asserts no further event is in flight — every write above was
// matched one-to-one, so anything here is an extra emission.
func (e *eventTap) expectNone() {
	e.t.Helper()
	select {
	case ev := <-e.sub:
		e.t.Errorf("unexpected extra event %s for %s", ev.Type, ev.EntityID)
	case <-time.After(300 * time.Millisecond):
	}
}

func versionOf(present bool, v func() int) (int, bool) {
	if !present {
		return 0, false
	}
	return v(), true
}

// assertArrays checks the entity marshals each key as a JSON array — the
// "[] not null" contract Normalize guarantees — regardless of adapter.
func assertArrays(t *testing.T, step string, rec parityRecord, keys []string) {
	t.Helper()
	if len(keys) == 0 {
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rec.JSON, &obj); err != nil {
		t.Fatalf("%s: entity JSON: %v", step, err)
	}
	for _, k := range keys {
		raw, ok := obj[k]
		if !ok || len(raw) == 0 || raw[0] != '[' {
			t.Errorf("%s: %s must be a JSON array, got %s", step, k, string(raw))
		}
	}
}

func assertInvalid(t *testing.T, step string, err error, field string) {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("%s: want ValidationError on %q, got %v", step, field, err)
		return
	}
	if ve.Field != field {
		t.Errorf("%s: want ValidationError on %q, got field %q (%s)", step, field, ve.Field, ve.Message)
	}
}

// assertIDs compares id lists as sets, or in order when ordered is set.
func assertIDs(t *testing.T, step string, got, want []string, ordered bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: want %d id(s), got %d", step, len(want), len(got))
		return
	}
	if ordered {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: position %d: want %s, got %s", step, i, want[i], got[i])
			}
		}
		return
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("%s: duplicate id %s", step, id)
		}
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Errorf("%s: missing id %s", step, id)
		}
	}
}

func idsOf(recs []parityRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func taskRecord(t domain.Task) parityRecord {
	b, _ := json.Marshal(t)
	return parityRecord{ID: t.ID, Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, JSON: b}
}

func projectRecord(p domain.Project) parityRecord {
	b, _ := json.Marshal(p)
	return parityRecord{ID: p.ID, Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, JSON: b}
}

func requirementRecord(r domain.Requirement) parityRecord {
	b, _ := json.Marshal(r)
	return parityRecord{ID: r.ID, Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, JSON: b}
}

func clientRecord(c domain.Client) parityRecord {
	b, _ := json.Marshal(c)
	return parityRecord{ID: c.ID, Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, JSON: b}
}
