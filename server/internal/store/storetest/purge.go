package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// PurgeTables enumerates, by hand, every table that can hold a tenant's rows.
// PurgeTenant must leave all of them empty for the purged tenant. When a new
// tenant-scoped table is added, add it here — the suite then fails until both
// adapters' PurgeTenant (and the memory adapter's CountRows) learn about it.
var PurgeTables = []string{
	"tenants", "users", "clients", "projects", "project_members", "tasks",
	"requirements", "audit_log",
	"sources", "signals", "signal_bodies", "signal_participants", "work_item_signals", "outputs",
	"outbox", "accounts", "login_tokens", "sessions", "api_tokens",
}

// RowCounter returns the number of rows in table that belong to acct's tenant
// (login_tokens are keyed by acct.Email, which has no tenant column). It is
// the adapter's raw, bypass-everything view — for Postgres a direct query,
// for memory a walk of the maps — so the assertion does not trust the Store
// API it is checking.
type RowCounter func(t *testing.T, table string, acct domain.Account) int

// AssertTenantPurge seeds two tenants with a row in every table it can reach,
// purges A, and proves (1) A has zero rows in EVERY table, including the
// outbox and auth tables, (2) B's data and credentials are untouched, and (3)
// A's email can sign up again as a fresh tenant. durableOutbox says whether
// the adapter persists events (Postgres) — if so, A must have outbox rows
// before the purge, proving the outbox delete is exercised and not vacuous.
func AssertTenantPurge(t *testing.T, st store.Store, emailA, emailB string, count RowCounter, durableOutbox bool) {
	t.Helper()
	ctx := context.Background()

	a := seedTenant(t, st, emailA)
	b := seedTenant(t, st, emailB)
	if a.acct.TenantID == b.acct.TenantID {
		t.Fatalf("expected distinct tenants, both got %s", a.acct.TenantID)
	}

	// Positive control: the counter sees A's rows before the purge, so a zero
	// afterwards means "deleted", not "counter looked in the wrong place".
	// project_members has no write path through the Store, and the memory
	// adapter keeps no outbox, so those two are only asserted empty below.
	for _, table := range PurgeTables {
		if table == "project_members" || (table == "outbox" && !durableOutbox) {
			continue
		}
		if n := count(t, table, a.acct); n == 0 {
			t.Errorf("seed: expected rows in %s for tenant A before purge, got 0", table)
		}
	}

	if err := st.PurgeTenant(ctx, a.acct.TenantID); err != nil {
		t.Fatalf("PurgeTenant(A): %v", err)
	}

	// --- A is gone from every table ---
	for _, table := range PurgeTables {
		if n := count(t, table, a.acct); n != 0 {
			t.Errorf("after purge: %d row(s) for tenant A remain in %s", n, table)
		}
	}
	if _, err := st.Bootstrap(ctx, a.acct.TenantID, a.acct.UserID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("A Bootstrap after purge: want ErrNotFound, got %v", err)
	}
	if _, err := st.GetSession(ctx, a.sessionHash); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("A session survived purge: %v", err)
	}
	if _, err := st.ResolveAPIToken(ctx, a.apiHash); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("A API token survived purge: %v", err)
	}
	if _, err := st.ConsumeLoginToken(ctx, a.loginHash); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("A login token survived purge: %v", err)
	}
	if err := st.PurgeTenant(ctx, a.acct.TenantID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second PurgeTenant(A): want ErrNotFound, got %v", err)
	}

	// --- B is intact: data, session, API token, login token — and, where the
	// outbox is durable, B's replay window too (an unscoped outbox delete
	// would leave A at zero and B's data rows untouched, so only this catches it)
	for _, table := range PurgeTables {
		if table == "project_members" || (table == "outbox" && !durableOutbox) {
			continue
		}
		if n := count(t, table, b.acct); n == 0 {
			t.Errorf("collateral damage: tenant B lost its rows in %s", table)
		}
	}
	boot, err := st.Bootstrap(ctx, b.acct.TenantID, b.acct.UserID)
	if err != nil {
		t.Fatalf("B Bootstrap after purge(A): %v", err)
	}
	if len(boot.Tasks) != 1 || len(boot.Projects) != 1 || len(boot.Clients) != 1 || len(boot.Requirements) != 1 {
		t.Errorf("B bootstrap want 1/1/1/1, got tasks=%d projects=%d clients=%d requirements=%d", len(boot.Tasks), len(boot.Projects), len(boot.Clients), len(boot.Requirements))
	}
	if _, err := st.GetSession(ctx, b.sessionHash); err != nil {
		t.Errorf("B session lost: %v", err)
	}
	if _, err := st.ResolveAPIToken(ctx, b.apiHash); err != nil {
		t.Errorf("B API token lost: %v", err)
	}
	if got, err := st.ConsumeLoginToken(ctx, b.loginHash); err != nil || got != emailB {
		t.Errorf("B login token lost: email=%q err=%v", got, err)
	}

	// --- A's email can start over as a brand-new tenant ---
	fresh, err := st.FindOrCreateAccount(ctx, emailA)
	if err != nil {
		t.Fatalf("re-sign-up A: %v", err)
	}
	if fresh.TenantID == a.acct.TenantID {
		t.Errorf("re-sign-up reused purged tenant id %s", fresh.TenantID)
	}
}

// seeded is one tenant with a row in every Store-reachable table.
type seeded struct {
	acct                            domain.Account
	sessionHash, apiHash, loginHash string
}

func seedTenant(t *testing.T, st store.Store, email string) seeded {
	t.Helper()
	ctx := context.Background()
	acct, err := st.FindOrCreateAccount(ctx, email)
	if err != nil {
		t.Fatalf("FindOrCreateAccount(%s): %v", email, err)
	}
	cli, err := st.CreateClient(ctx, acct.TenantID, acct.UserID, domain.CreateClientInput{Name: "client"})
	if err != nil {
		t.Fatalf("CreateClient: %v", err)
	}
	proj, err := st.CreateProject(ctx, acct.TenantID, acct.UserID, domain.CreateProjectInput{Name: "project", ClientID: &cli.ID})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, err := st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "task", ProjectID: &proj.ID})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := st.CreateRequirement(ctx, acct.TenantID, acct.UserID, domain.CreateRequirementInput{ProjectID: proj.ID, Title: "requirement"}); err != nil {
		t.Fatalf("CreateRequirement: %v", err)
	}
	// The signal surface: a capture with text and a participant reaches
	// sources (the lazily created manual one), signals, signal_bodies and
	// signal_participants; attaching it reaches work_item_signals; an output
	// reaches outputs.
	sig, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, domain.CreateSignalInput{
		Kind: domain.SignalNote, Title: "signal", Text: "captured text",
		Participants: []domain.Participant{{Name: "Someone", Role: domain.RoleSpeaker}},
	})
	if err != nil {
		t.Fatalf("CreateSignal: %v", err)
	}
	if _, err := st.AttachSignalToTask(ctx, acct.TenantID, acct.UserID, task.ID, sig.ID); err != nil {
		t.Fatalf("AttachSignalToTask: %v", err)
	}
	if _, err := st.CreateOutput(ctx, acct.TenantID, acct.UserID, task.ID, domain.CreateOutputInput{Kind: domain.OutputNote, Title: "output"}); err != nil {
		t.Fatalf("CreateOutput: %v", err)
	}
	// An audit row is the one thing a purge must be able to delete that
	// nothing else can (append-only trigger) — seed one so the purge suite
	// proves the escape hatch works, not just that the table was empty.
	if _, err := st.AppendAudit(ctx, acct.TenantID, domain.AuditEntry{ActorID: &acct.UserID, Kind: domain.AuditLoginVerified}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	exp := time.Now().Add(time.Hour)
	s := seeded{
		acct:        acct,
		sessionHash: "sess-" + acct.UserID,
		apiHash:     "api-" + acct.UserID,
		loginHash:   "login-" + acct.UserID,
	}
	if err := st.CreateSession(ctx, s.sessionHash, domain.Session{UserID: acct.UserID, TenantID: acct.TenantID, Email: email, ExpiresAt: exp}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := st.CreateAPIToken(ctx, s.apiHash, domain.APIToken{ID: domain.NewID(), TenantID: acct.TenantID, UserID: acct.UserID, Name: "mcp", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := st.CreateLoginToken(ctx, s.loginHash, email, exp); err != nil {
		t.Fatalf("CreateLoginToken: %v", err)
	}
	return s
}
