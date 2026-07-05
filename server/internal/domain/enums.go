package domain

// Kind is the energy-shape of a task — what kind of attention it needs.
type Kind string

const (
	KindDeep     Kind = "deep"
	KindLight    Kind = "light"
	KindAdmin    Kind = "admin"
	KindMeet     Kind = "meet"
	KindPersonal Kind = "personal"
)

func (k Kind) Valid() bool {
	switch k {
	case KindDeep, KindLight, KindAdmin, KindMeet, KindPersonal:
		return true
	}
	return false
}

// Status is where a task sits in its lifecycle. It maps directly to the Board
// columns and is what Plan/Today filter on.
type Status string

const (
	StatusBacklog   Status = "backlog"
	StatusScheduled Status = "scheduled"
	StatusFocus     Status = "focus"
	StatusDone      Status = "done"
)

func (s Status) Valid() bool {
	switch s {
	case StatusBacklog, StatusScheduled, StatusFocus, StatusDone:
		return true
	}
	return false
}

// ClientTier ranks how much of your attention a client warrants (A > B > C).
const (
	TierA = "a"
	TierB = "b"
	TierC = "c"
)

func ValidTier(t string) bool {
	switch t {
	case TierA, TierB, TierC:
		return true
	}
	return false
}

// ClientKind separates external clients from internal initiatives.
const (
	ClientExternal = "client"
	ClientInternal = "internal"
)

func ValidClientKind(k string) bool {
	switch k {
	case ClientExternal, ClientInternal:
		return true
	}
	return false
}

// EventType labels a realtime change broadcast to a tenant's subscribers.
type EventType string

const (
	EventTaskCreated    EventType = "task.created"
	EventTaskUpdated    EventType = "task.updated"
	EventTaskDeleted    EventType = "task.deleted"
	EventProjectCreated EventType = "project.created"
	EventProjectUpdated EventType = "project.updated"
	EventProjectDeleted EventType = "project.deleted"
	EventClientCreated  EventType = "client.created"
	EventClientUpdated  EventType = "client.updated"
	EventClientDeleted  EventType = "client.deleted"
)
