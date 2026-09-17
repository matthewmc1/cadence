// Package store defines the persistence and realtime contract Cadence depends
// on. Two adapters implement it: an in-memory one (default, runs anywhere) and
// a Postgres one (production, RLS + transactional outbox + LISTEN/NOTIFY).
package store

import (
	"context"
	"time"

	"github.com/cadence/server/internal/domain"
)

// TaskFilter narrows ListTasks. Nil fields are ignored.
type TaskFilter struct {
	Status    *domain.Status
	Stage     *domain.Stage // the successor to Status; both work while status lasts
	ProjectID *string
}

// RequirementFilter narrows ListRequirements. Nil fields are ignored.
type RequirementFilter struct {
	ProjectID *string
}

// Store is the full contract: CRUD over the tenant's data, plus a realtime
// subscription. Every method is tenant-scoped — adapters must guarantee a
// caller can never read or write another tenant's rows.
type Store interface {
	// Bootstrap returns everything the web app needs to hydrate, from the point
	// of view of userID within tenantID.
	Bootstrap(ctx context.Context, tenantID, userID string) (*domain.Bootstrap, error)

	ListTasks(ctx context.Context, tenantID string, f TaskFilter) ([]domain.Task, error)
	// ListTasksPaged is the keyset-paged read (see paging.go): newest first by
	// (created_at, id), same filter as ListTasks. ListTasks stays unpaged for
	// the bounded working set; unbounded entities only get the paged form.
	ListTasksPaged(ctx context.Context, tenantID string, f TaskFilter, p Page) (PageResult[domain.Task], error)
	GetTask(ctx context.Context, tenantID, id string) (*domain.Task, error)
	CreateTask(ctx context.Context, tenantID, actorID string, in domain.CreateTaskInput) (*domain.Task, error)
	UpdateTask(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Task, error)
	DeleteTask(ctx context.Context, tenantID, actorID, id string) error

	ListProjects(ctx context.Context, tenantID string) ([]domain.Project, error)
	CreateProject(ctx context.Context, tenantID, actorID string, in domain.CreateProjectInput) (*domain.Project, error)
	UpdateProject(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Project, error)
	DeleteProject(ctx context.Context, tenantID, actorID, id string) error

	ListClients(ctx context.Context, tenantID string) ([]domain.Client, error)
	CreateClient(ctx context.Context, tenantID, actorID string, in domain.CreateClientInput) (*domain.Client, error)
	UpdateClient(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Client, error)
	DeleteClient(ctx context.Context, tenantID, actorID, id string) error

	// Requirements are bounded (a handful per project) and hydrate in
	// Bootstrap, so the list is unpaged like tasks/projects/clients.
	ListRequirements(ctx context.Context, tenantID string, f RequirementFilter) ([]domain.Requirement, error)
	CreateRequirement(ctx context.Context, tenantID, actorID string, in domain.CreateRequirementInput) (*domain.Requirement, error)
	UpdateRequirement(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Requirement, error)
	DeleteRequirement(ctx context.Context, tenantID, actorID, id string) error

	// AppendAudit writes one immutable audit row (see domain.AuditEntry for the
	// no-PII rule). The store assigns ID and At when zero, defaults Detail to
	// {} and bounds it (store.NormalizeAuditEntry), and returns the row as
	// stored. Appending emits NO realtime event: the log is its own record,
	// and fanning "someone signed in" to every open tab is noise.
	AppendAudit(ctx context.Context, tenantID string, e domain.AuditEntry) (*domain.AuditEntry, error)
	// ListAudit is the keyset-paged read of a tenant's log, newest first by
	// (at, id) — the same contract as ListTasksPaged. There is no unpaged
	// form: the log grows without bound.
	ListAudit(ctx context.Context, tenantID string, p Page) (PageResult[domain.AuditEntry], error)

	// ---- signals (0014): captured things, never tasks, never in Bootstrap ----

	// GetOrCreateManualSource returns the tenant's one implicit 'manual'
	// source (the paste box), creating it on first use. Sources are the
	// small, bounded list of where signals come from.
	GetOrCreateManualSource(ctx context.Context, tenantID string) (*domain.Source, error)
	ListSources(ctx context.Context, tenantID string) ([]domain.Source, error)
	CreateSource(ctx context.Context, tenantID, actorID string, in domain.CreateSourceInput) (*domain.Source, error)
	UpdateSource(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Source, error)
	// DeleteSource takes the source's signals (and their bodies and
	// participants) with it; origin rows keep their snapshot. It returns how
	// many signals went, so the caller can audit the size of the deletion.
	// The implicit 'manual' source is refused (ErrManualSourceDelete).
	DeleteSource(ctx context.Context, tenantID, actorID, id string) (int, error)

	// CreateSignal writes the signal, its body (when there is text) and its
	// participants in one transaction. in.SourceID nil = the manual source.
	// A duplicate (source, externalId) is a ValidationError on externalId.
	CreateSignal(ctx context.Context, tenantID, actorID string, in domain.CreateSignalInput) (*domain.Signal, error)
	// GetSignal returns the row with its participants — never the body.
	GetSignal(ctx context.Context, tenantID, id string) (*domain.Signal, error)
	// ListSignals is keyset-paged newest first by (occurred_at, id); there is
	// no unpaged form. CountSignals is the same filter, counted (the inbox
	// badge).
	ListSignals(ctx context.Context, tenantID string, f SignalFilter, p Page) (PageResult[domain.Signal], error)
	CountSignals(ctx context.Context, tenantID string, f SignalFilter) (int, error)
	// UpdateSignalDisposition is the ONLY write to a signal after capture:
	// stage / snoozedUntil / projectHint / extracted / retentionUntil (see
	// ApplySignalPatch). Any other key is a ValidationError — signals are
	// immutable records.
	UpdateSignalDisposition(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Signal, error)
	// DeleteSignal purges the row, its body and its participants. Origin rows
	// survive with their snapshot.
	DeleteSignal(ctx context.Context, tenantID, actorID, id string) error
	// GetSignalBody is the only path to the full text. ErrNotFound if the
	// signal does not exist; an empty Body if it has none (a bare link).
	GetSignalBody(ctx context.Context, tenantID, id string) (*domain.SignalBody, error)

	// AttachSignalToTask records that the work item derives from the signal
	// (snapshotting its provenance) and moves an inbox/snoozed signal to
	// 'attached'. Idempotent: attaching twice returns the existing origin.
	AttachSignalToTask(ctx context.Context, tenantID, actorID, taskID, signalID string) (*domain.Origin, error)
	DetachSignalFromTask(ctx context.Context, tenantID, actorID, taskID, signalID string) error
	// ListTaskOrigins is oldest-first (attach order); ErrNotFound if the task
	// is not in the tenant.
	ListTaskOrigins(ctx context.Context, tenantID, taskID string) ([]domain.Origin, error)

	// ---- outputs (0015): what a work item produced ----

	CreateOutput(ctx context.Context, tenantID, actorID, taskID string, in domain.CreateOutputInput) (*domain.Output, error)
	// ListOutputs is oldest-first; ErrNotFound if the task is not in the tenant.
	ListOutputs(ctx context.Context, tenantID, taskID string) ([]domain.Output, error)
	DeleteOutput(ctx context.Context, tenantID, actorID, taskID, id string) error

	// Subscribe returns a channel of events for one tenant and an unsubscribe
	// func. The channel is closed when unsubscribe is called.
	Subscribe(tenantID string) (<-chan domain.Event, func())

	// PurgeTenant hard-deletes EVERYTHING belonging to a tenant across every
	// table — its rows, its users' auth material (sessions, API tokens, login
	// tokens, accounts), its outbox events, and finally the tenant row itself.
	// Irreversible; ErrNotFound if the tenant does not exist. No event is
	// emitted (the tenant's outbox is gone).
	PurgeTenant(ctx context.Context, tenantID string) error

	// Ping checks liveness of the backing store.
	Ping(ctx context.Context) error
	Close() error

	// ---- auth (operates outside tenant RLS; identity is established here) ----

	// CreateLoginToken stores a one-time magic-link token (by hash).
	CreateLoginToken(ctx context.Context, tokenHash, email string, expiresAt time.Time) error
	// ConsumeLoginToken validates + single-uses a token, returning its email.
	// Returns ErrNotFound if missing, expired, or already used.
	ConsumeLoginToken(ctx context.Context, tokenHash string) (string, error)
	// FindOrCreateAccount returns the account for email, creating a fresh
	// personal tenant + user on first sign-in.
	FindOrCreateAccount(ctx context.Context, email string) (domain.Account, error)
	// FindAccount returns the account for email without creating one;
	// ErrNotFound for an unknown email. Lets a pre-session path (a magic link
	// request) attribute an audit row to the tenant it belongs to.
	FindAccount(ctx context.Context, email string) (domain.Account, error)
	// GetUser returns a tenant's user (deriving display fields from email).
	GetUser(ctx context.Context, tenantID, userID string) (domain.User, error)

	CreateSession(ctx context.Context, tokenHash string, sess domain.Session) error
	GetSession(ctx context.Context, tokenHash string) (domain.Session, error) // ErrNotFound if missing/expired
	DeleteSession(ctx context.Context, tokenHash string) error

	// Personal access tokens (Bearer auth for the MCP server / API clients).
	CreateAPIToken(ctx context.Context, tokenHash string, tok domain.APIToken) error
	ResolveAPIToken(ctx context.Context, tokenHash string) (domain.APIToken, error) // ErrNotFound; touches last_used_at
	ListAPITokens(ctx context.Context, tenantID, userID string) ([]domain.APIToken, error)
	RevokeAPIToken(ctx context.Context, tenantID, userID, id string) error
}
