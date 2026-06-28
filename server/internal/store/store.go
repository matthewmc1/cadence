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
	GetTask(ctx context.Context, tenantID, id string) (*domain.Task, error)
	CreateTask(ctx context.Context, tenantID, actorID string, in domain.CreateTaskInput) (*domain.Task, error)
	UpdateTask(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Task, error)
	DeleteTask(ctx context.Context, tenantID, actorID, id string) error

	ListProjects(ctx context.Context, tenantID string) ([]domain.Project, error)
	CreateProject(ctx context.Context, tenantID, actorID string, in domain.CreateProjectInput) (*domain.Project, error)
	UpdateProject(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Project, error)
	DeleteProject(ctx context.Context, tenantID, actorID, id string) error

	// Subscribe returns a channel of events for one tenant and an unsubscribe
	// func. The channel is closed when unsubscribe is called.
	Subscribe(tenantID string) (<-chan domain.Event, func())

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
	// GetUser returns a tenant's user (deriving display fields from email).
	GetUser(ctx context.Context, tenantID, userID string) (domain.User, error)

	CreateSession(ctx context.Context, tokenHash string, sess domain.Session) error
	GetSession(ctx context.Context, tokenHash string) (domain.Session, error) // ErrNotFound if missing/expired
	DeleteSession(ctx context.Context, tokenHash string) error
}
