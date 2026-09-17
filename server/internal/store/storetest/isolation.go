// Package storetest holds cross-adapter conformance suites. The tenant-isolation
// suite is the load-bearing multi-tenancy guarantee: whatever backend is used,
// one tenant must never be able to read or mutate another tenant's data — even
// when it knows the exact row id — and must never receive another tenant's
// realtime events. Both the memory and Postgres (RLS) adapters run this suite.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// AssertTenantIsolation drives a Store through an adversarial two-tenant
// scenario: tenant B, holding the exact ids of tenant A's rows, must be fenced
// off from every one of them, while A still sees its own data.
func AssertTenantIsolation(t *testing.T, st store.Store, emailA, emailB string) {
	t.Helper()
	ctx := context.Background()

	a, err := st.FindOrCreateAccount(ctx, emailA)
	if err != nil {
		t.Fatalf("create account A: %v", err)
	}
	b, err := st.FindOrCreateAccount(ctx, emailB)
	if err != nil {
		t.Fatalf("create account B: %v", err)
	}
	if a.TenantID == b.TenantID {
		t.Fatalf("expected distinct tenants, both got %s", a.TenantID)
	}

	// Tenant A creates one of each entity.
	task, err := st.CreateTask(ctx, a.TenantID, a.UserID, domain.CreateTaskInput{Title: "A's private task"})
	if err != nil {
		t.Fatalf("A CreateTask: %v", err)
	}
	proj, err := st.CreateProject(ctx, a.TenantID, a.UserID, domain.CreateProjectInput{Name: "A's project"})
	if err != nil {
		t.Fatalf("A CreateProject: %v", err)
	}
	cli, err := st.CreateClient(ctx, a.TenantID, a.UserID, domain.CreateClientInput{Name: "A's client"})
	if err != nil {
		t.Fatalf("A CreateClient: %v", err)
	}
	req, err := st.CreateRequirement(ctx, a.TenantID, a.UserID, domain.CreateRequirementInput{ProjectID: proj.ID, Title: "A's requirement"})
	if err != nil {
		t.Fatalf("A CreateRequirement: %v", err)
	}
	if _, err := st.AppendAudit(ctx, a.TenantID, domain.AuditEntry{ActorID: &a.UserID, Kind: domain.AuditLoginVerified}); err != nil {
		t.Fatalf("A AppendAudit: %v", err)
	}
	// …and the signal surface (0014/0015): a capture with a body and a
	// participant (in A's lazily created manual source), attached to A's task,
	// plus an output on that task.
	sig, err := st.CreateSignal(ctx, a.TenantID, a.UserID, domain.CreateSignalInput{
		Kind: domain.SignalEmail, Title: "A's private signal", Text: "A's private body",
		Participants: []domain.Participant{{Name: "A's contact", Email: ptrOf("contact@a.example.com"), Role: domain.RoleFrom}},
	})
	if err != nil {
		t.Fatalf("A CreateSignal: %v", err)
	}
	if _, err := st.AttachSignalToTask(ctx, a.TenantID, a.UserID, task.ID, sig.ID); err != nil {
		t.Fatalf("A AttachSignalToTask: %v", err)
	}
	out, err := st.CreateOutput(ctx, a.TenantID, a.UserID, task.ID, domain.CreateOutputInput{Kind: domain.OutputNote, Title: "A's output"})
	if err != nil {
		t.Fatalf("A CreateOutput: %v", err)
	}
	manual, err := st.GetOrCreateManualSource(ctx, a.TenantID)
	if err != nil {
		t.Fatalf("A GetOrCreateManualSource: %v", err)
	}

	notFound := func(label string, err error) {
		t.Helper()
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: want ErrNotFound (isolation), got %v", label, err)
		}
	}

	// --- B knows A's ids but must be denied every read/mutate path ---
	_, err = st.GetTask(ctx, b.TenantID, task.ID)
	notFound("B GetTask(A.task)", err)
	_, err = st.UpdateTask(ctx, b.TenantID, b.UserID, task.ID, map[string]any{"title": "hijacked"}, nil)
	notFound("B UpdateTask(A.task)", err)
	notFound("B DeleteTask(A.task)", st.DeleteTask(ctx, b.TenantID, b.UserID, task.ID))

	_, err = st.UpdateProject(ctx, b.TenantID, b.UserID, proj.ID, map[string]any{"name": "hijacked"}, nil)
	notFound("B UpdateProject(A.project)", err)
	notFound("B DeleteProject(A.project)", st.DeleteProject(ctx, b.TenantID, b.UserID, proj.ID))

	_, err = st.UpdateClient(ctx, b.TenantID, b.UserID, cli.ID, map[string]any{"name": "hijacked"}, nil)
	notFound("B UpdateClient(A.client)", err)
	notFound("B DeleteClient(A.client)", st.DeleteClient(ctx, b.TenantID, b.UserID, cli.ID))

	_, err = st.UpdateRequirement(ctx, b.TenantID, b.UserID, req.ID, map[string]any{"title": "hijacked"}, nil)
	notFound("B UpdateRequirement(A.requirement)", err)
	notFound("B DeleteRequirement(A.requirement)", st.DeleteRequirement(ctx, b.TenantID, b.UserID, req.ID))

	// --- B's list/bootstrap must not leak A's rows ---
	reqs, err := st.ListRequirements(ctx, b.TenantID, store.RequirementFilter{ProjectID: &proj.ID})
	if err != nil {
		t.Fatalf("B ListRequirements(filter A.project): %v", err)
	}
	if len(reqs) != 0 {
		t.Errorf("B ListRequirements filtered by A's project leaked %d requirement(s)", len(reqs))
	}
	// The audit log has no id-addressed read or any write besides append, so
	// "denied" for B means its paged list is empty while A's is not.
	bAudit, err := st.ListAudit(ctx, b.TenantID, store.Page{})
	if err != nil {
		t.Fatalf("B ListAudit: %v", err)
	}
	if len(bAudit.Items) != 0 {
		t.Errorf("B ListAudit leaked %d audit row(s)", len(bAudit.Items))
	}
	// Signals: every id-addressed path is denied, and — the sensitive one —
	// B cannot read A's body. The paged list and the count see nothing, even
	// filtered by A's manual source or A's project.
	_, err = st.GetSignal(ctx, b.TenantID, sig.ID)
	notFound("B GetSignal(A.signal)", err)
	_, err = st.GetSignalBody(ctx, b.TenantID, sig.ID)
	notFound("B GetSignalBody(A.signal)", err)
	_, err = st.UpdateSignalDisposition(ctx, b.TenantID, b.UserID, sig.ID, map[string]any{"stage": "dismissed"}, nil)
	notFound("B UpdateSignalDisposition(A.signal)", err)
	notFound("B DeleteSignal(A.signal)", st.DeleteSignal(ctx, b.TenantID, b.UserID, sig.ID))
	if res, err := st.ListSignals(ctx, b.TenantID, store.SignalFilter{SourceID: &manual.ID}, store.Page{}); err != nil {
		t.Fatalf("B ListSignals(filter A.source): %v", err)
	} else if len(res.Items) != 0 {
		t.Errorf("B ListSignals filtered by A's source leaked %d signal(s)", len(res.Items))
	}
	if res, err := st.ListSignals(ctx, b.TenantID, store.SignalFilter{ProjectHint: &proj.ID}, store.Page{}); err != nil {
		t.Fatalf("B ListSignals(filter A.project): %v", err)
	} else if len(res.Items) != 0 {
		t.Errorf("B ListSignals filtered by A's project leaked %d signal(s)", len(res.Items))
	}
	if n, err := st.CountSignals(ctx, b.TenantID, store.SignalFilter{}); err != nil || n != 0 {
		t.Errorf("B CountSignals: want 0, got %d (%v)", n, err)
	}
	if srcs, err := st.ListSources(ctx, b.TenantID); err != nil {
		t.Fatalf("B ListSources: %v", err)
	} else if len(srcs) != 0 {
		t.Errorf("B ListSources leaked %d source(s)", len(srcs))
	}
	_, err = st.UpdateSource(ctx, b.TenantID, b.UserID, manual.ID, map[string]any{"name": "hijacked"}, nil)
	notFound("B UpdateSource(A.source)", err)
	// the tenant fence answers before the manual-source rule does: B must not
	// even learn that A's source exists
	_, err = st.DeleteSource(ctx, b.TenantID, b.UserID, manual.ID)
	notFound("B DeleteSource(A.source)", err)
	// Origins and outputs hang off A's task, which B cannot see.
	_, err = st.ListTaskOrigins(ctx, b.TenantID, task.ID)
	notFound("B ListTaskOrigins(A.task)", err)
	notFound("B DetachSignalFromTask(A.task, A.signal)", st.DetachSignalFromTask(ctx, b.TenantID, b.UserID, task.ID, sig.ID))
	_, err = st.AttachSignalToTask(ctx, b.TenantID, b.UserID, task.ID, domain.NewID())
	notFound("B AttachSignalToTask(A.task, …)", err)
	_, err = st.ListOutputs(ctx, b.TenantID, task.ID)
	notFound("B ListOutputs(A.task)", err)
	_, err = st.CreateOutput(ctx, b.TenantID, b.UserID, task.ID, domain.CreateOutputInput{Kind: domain.OutputNote, Title: "hijacked"})
	notFound("B CreateOutput(A.task)", err)
	notFound("B DeleteOutput(A.task, A.output)", st.DeleteOutput(ctx, b.TenantID, b.UserID, task.ID, out.ID))
	tasks, err := st.ListTasks(ctx, b.TenantID, store.TaskFilter{})
	if err != nil {
		t.Fatalf("B ListTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("B ListTasks leaked %d task(s)", len(tasks))
	}
	// ...even filtering by A's project id.
	tasks, err = st.ListTasks(ctx, b.TenantID, store.TaskFilter{ProjectID: &proj.ID})
	if err != nil {
		t.Fatalf("B ListTasks(filter A.project): %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("B ListTasks filtered by A's project leaked %d task(s)", len(tasks))
	}
	bBoot, err := st.Bootstrap(ctx, b.TenantID, b.UserID)
	if err != nil {
		t.Fatalf("B Bootstrap: %v", err)
	}
	if n := len(bBoot.Tasks) + len(bBoot.Projects) + len(bBoot.Clients) + len(bBoot.Requirements); n != 0 {
		t.Errorf("B bootstrap leaked data: tasks=%d projects=%d clients=%d requirements=%d", len(bBoot.Tasks), len(bBoot.Projects), len(bBoot.Clients), len(bBoot.Requirements))
	}

	// --- Positive controls: A's data is intact — none of B's hijack attempts
	//     landed, and isolation didn't just hide everything. ---
	gotTask, err := st.GetTask(ctx, a.TenantID, task.ID)
	if err != nil {
		t.Fatalf("A GetTask(own): %v", err)
	}
	if gotTask.Title != "A's private task" {
		t.Errorf("A's task was mutated across tenants: title=%q", gotTask.Title)
	}
	aBoot, err := st.Bootstrap(ctx, a.TenantID, a.UserID)
	if err != nil {
		t.Fatalf("A Bootstrap: %v", err)
	}
	if len(aBoot.Tasks) != 1 || len(aBoot.Projects) != 1 || len(aBoot.Clients) != 1 || len(aBoot.Requirements) != 1 {
		t.Fatalf("A bootstrap want 1/1/1/1, got tasks=%d projects=%d clients=%d requirements=%d", len(aBoot.Tasks), len(aBoot.Projects), len(aBoot.Clients), len(aBoot.Requirements))
	}
	if aBoot.Projects[0].Name != "A's project" {
		t.Errorf("A's project was renamed across tenants: %q", aBoot.Projects[0].Name)
	}
	if aBoot.Clients[0].Name != "A's client" {
		t.Errorf("A's client was renamed across tenants: %q", aBoot.Clients[0].Name)
	}
	if aBoot.Requirements[0].Title != "A's requirement" {
		t.Errorf("A's requirement was renamed across tenants: %q", aBoot.Requirements[0].Title)
	}
	aAudit, err := st.ListAudit(ctx, a.TenantID, store.Page{})
	if err != nil {
		t.Fatalf("A ListAudit: %v", err)
	}
	if len(aAudit.Items) != 1 || aAudit.Items[0].Kind != domain.AuditLoginVerified {
		t.Errorf("A ListAudit: want its one %s row, got %d row(s)", domain.AuditLoginVerified, len(aAudit.Items))
	}

	// --- B cannot forge cross-tenant references: pointing B's own project at
	//     A's client, or B's own task at A's project, must be rejected (memory
	//     checks ownership; Postgres enforces it with tenant-local FKs). ---
	pB, err := st.CreateProject(ctx, b.TenantID, b.UserID, domain.CreateProjectInput{Name: "B's project"})
	if err != nil {
		t.Fatalf("B CreateProject: %v", err)
	}
	tB, err := st.CreateTask(ctx, b.TenantID, b.UserID, domain.CreateTaskInput{Title: "B's task"})
	if err != nil {
		t.Fatalf("B CreateTask: %v", err)
	}
	if _, err := st.UpdateProject(ctx, b.TenantID, b.UserID, pB.ID, map[string]any{"clientId": cli.ID}, nil); err == nil {
		t.Error("B pointed its project at A's client — cross-tenant reference allowed")
	}
	if _, err := st.UpdateTask(ctx, b.TenantID, b.UserID, tB.ID, map[string]any{"projectId": proj.ID}, nil); err == nil {
		t.Error("B pointed its task at A's project — cross-tenant reference allowed")
	}
	if _, err := st.CreateProject(ctx, b.TenantID, b.UserID, domain.CreateProjectInput{Name: "x", ClientID: &cli.ID}); err == nil {
		t.Error("B created a project referencing A's client — cross-tenant reference allowed")
	}
	if _, err := st.CreateTask(ctx, b.TenantID, b.UserID, domain.CreateTaskInput{Title: "x", ProjectID: &proj.ID}); err == nil {
		t.Error("B created a task referencing A's project — cross-tenant reference allowed")
	}
	if _, err := st.CreateRequirement(ctx, b.TenantID, b.UserID, domain.CreateRequirementInput{ProjectID: proj.ID, Title: "x"}); err == nil {
		t.Error("B created a requirement under A's project — cross-tenant reference allowed")
	}
	rB, err := st.CreateRequirement(ctx, b.TenantID, b.UserID, domain.CreateRequirementInput{ProjectID: pB.ID, Title: "B's requirement"})
	if err != nil {
		t.Fatalf("B CreateRequirement: %v", err)
	}
	if _, err := st.UpdateRequirement(ctx, b.TenantID, b.UserID, rB.ID, map[string]any{"projectId": proj.ID}, nil); err == nil {
		t.Error("B moved its requirement under A's project — cross-tenant reference allowed")
	}
	// Work-item references (0013) are fenced the same way: B's task may not
	// serve A's requirement, be owned by A's user, or wait on A's user.
	if _, err := st.UpdateTask(ctx, b.TenantID, b.UserID, tB.ID, map[string]any{"requirementId": req.ID}, nil); err == nil {
		t.Error("B pointed its task at A's requirement — cross-tenant reference allowed")
	}
	if _, err := st.CreateTask(ctx, b.TenantID, b.UserID, domain.CreateTaskInput{Title: "x", RequirementID: &req.ID}); err == nil {
		t.Error("B created a task referencing A's requirement — cross-tenant reference allowed")
	}
	if _, err := st.UpdateTask(ctx, b.TenantID, b.UserID, tB.ID, map[string]any{"ownerId": a.UserID}, nil); err == nil {
		t.Error("B assigned its task to A's user — cross-tenant reference allowed")
	}
	if _, err := st.UpdateTask(ctx, b.TenantID, b.UserID, tB.ID, map[string]any{"stage": "waiting", "waitingOnPersonId": a.UserID}, nil); err == nil {
		t.Error("B parked its task on A's user — cross-tenant reference allowed")
	}
	// Signal references are fenced the same way: B's task may not derive from
	// A's signal, and B may not capture into A's source or hint at A's project.
	if _, err := st.AttachSignalToTask(ctx, b.TenantID, b.UserID, tB.ID, sig.ID); err == nil {
		t.Error("B attached A's signal to its task — cross-tenant reference allowed")
	}
	if _, err := st.CreateSignal(ctx, b.TenantID, b.UserID, domain.CreateSignalInput{Kind: domain.SignalText, Title: "x", SourceID: &manual.ID}); err == nil {
		t.Error("B captured into A's source — cross-tenant reference allowed")
	}
	if _, err := st.CreateSignal(ctx, b.TenantID, b.UserID, domain.CreateSignalInput{Kind: domain.SignalText, Title: "x", ProjectHint: &proj.ID}); err == nil {
		t.Error("B captured a signal hinting at A's project — cross-tenant reference allowed")
	}
	// A's body is intact and readable by A (the positive control for the
	// body fence above).
	if body, err := st.GetSignalBody(ctx, a.TenantID, sig.ID); err != nil || body.Body != "A's private body" {
		t.Errorf("A GetSignalBody(own): want its body, got %+v (%v)", body, err)
	}
	// (that B may reference its OWN requirement and user is proven by the
	// parity suite's lifecycle walk; a successful write here would race its
	// own async event into assertRealtimeIsolation below)

	// --- Realtime fan-out is tenant-scoped: A's events never reach B. ---
	assertRealtimeIsolation(t, st, a, b)
}

// assertRealtimeIsolation subscribes both tenants, has A create a task, and
// asserts A receives its own event while B receives nothing. The positive
// control (A sees its own) proves the pipeline is live, so B's empty channel is
// a real negative result and not a dead subscription.
func assertRealtimeIsolation(t *testing.T, st store.Store, a, b domain.Account) {
	t.Helper()
	ctx := context.Background()

	subA, unsubA := st.Subscribe(a.TenantID)
	defer unsubA()
	subB, unsubB := st.Subscribe(b.TenantID)
	defer unsubB()

	probe, err := st.CreateTask(ctx, a.TenantID, a.UserID, domain.CreateTaskInput{Title: "realtime probe"})
	if err != nil {
		t.Fatalf("A CreateTask(probe): %v", err)
	}

	// A receives its own event (pipeline is live).
	deadline := time.After(3 * time.Second)
	for done := false; !done; {
		select {
		case ev := <-subA:
			if ev.EntityID == probe.ID {
				done = true
			}
		case <-deadline:
			t.Error("tenant A did not receive its own realtime event")
			done = true
		}
	}

	// B must receive nothing — A's event is tenant A's and must never route to B.
	select {
	case ev := <-subB:
		t.Errorf("realtime leak: tenant B received event %q for entity %s", ev.Type, ev.EntityID)
	case <-time.After(600 * time.Millisecond):
		// good — no cross-tenant delivery
	}
}
