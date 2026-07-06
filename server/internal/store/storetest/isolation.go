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

	// --- B's list/bootstrap must not leak A's rows ---
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
	if n := len(bBoot.Tasks) + len(bBoot.Projects) + len(bBoot.Clients); n != 0 {
		t.Errorf("B bootstrap leaked data: tasks=%d projects=%d clients=%d", len(bBoot.Tasks), len(bBoot.Projects), len(bBoot.Clients))
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
	if len(aBoot.Tasks) != 1 || len(aBoot.Projects) != 1 || len(aBoot.Clients) != 1 {
		t.Fatalf("A bootstrap want 1/1/1, got tasks=%d projects=%d clients=%d", len(aBoot.Tasks), len(aBoot.Projects), len(aBoot.Clients))
	}
	if aBoot.Projects[0].Name != "A's project" {
		t.Errorf("A's project was renamed across tenants: %q", aBoot.Projects[0].Name)
	}
	if aBoot.Clients[0].Name != "A's client" {
		t.Errorf("A's client was renamed across tenants: %q", aBoot.Clients[0].Name)
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
