package domain

import (
	"encoding/json"
	"time"
)

// Tenant is the unit of isolation. Every other row carries a tenant_id and is
// fenced off by Postgres Row-Level Security.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// User is a member of a tenant (used for assignees and avatars).
type User struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Initial  string `json:"initial"`
	Color    string `json:"color"`
}

// ProjectMember is the avatar stack shown on a project.
type ProjectMember struct {
	UserID  string `json:"userId"`
	Initial string `json:"initial"`
	Color   string `json:"color"`
}

// Link is an attached reference on a task.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Subtask is a lightweight checklist item under a task.
type Subtask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

// Assignee is a person a task is shared with ("With").
type Assignee struct {
	UserID  string `json:"userId"`
	Initial string `json:"initial"`
	Color   string `json:"color"`
}

// Recurrence values a task may repeat on.
const (
	RecurNone     = "none"
	RecurDaily    = "daily"
	RecurWeekdays = "weekdays"
	RecurWeekly   = "weekly"
	RecurMonthly  = "monthly"
)

func ValidRecurrence(r string) bool {
	switch r {
	case RecurNone, RecurDaily, RecurWeekdays, RecurWeekly, RecurMonthly:
		return true
	}
	return false
}

// Client is the strategic spine: who a body of work is ultimately for. Projects
// belong to a client; tasks inherit their client through their project.
type Client struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
	Tier     string `json:"tier"` // a|b|c — how much attention it warrants
	Kind     string `json:"kind"` // client|internal
	Color    string `json:"color"`
	// Standard is what the area is held to ("books closed by the 5th"); the
	// PARA counterpart of a project's Outcome. Empty for most plain clients.
	Standard string `json:"standard"`
	// ExpectedTouchDays is the cadence target: flag the client "underserved"
	// when it goes untouched longer than this. nil = no expectation set.
	ExpectedTouchDays *int       `json:"expectedTouchDays"`
	ArchivedAt        *time.Time `json:"archivedAt"`
	Version           int        `json:"version"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// Project groups tasks toward an outcome with a due date (PARA: no outcome or
// no date and it is really an area). Archived projects stay readable — the
// archive is the record of what got finished — but leave every active surface.
type Project struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenantId"`
	ClientID   *string         `json:"clientId"` // the client/area this project serves (nil = unassigned)
	Name       string          `json:"name"`
	Subtitle   string          `json:"subtitle"`
	Outcome    string          `json:"outcome"` // why it exists / what finishing looks like; its work items' "why"
	Due        *string         `json:"due"`
	Color      string          `json:"color"`
	ArchivedAt *time.Time      `json:"archivedAt"`
	Members    []ProjectMember `json:"members"`
	Version    int             `json:"version"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

// Task is the heart of Cadence. One normalized record drives Today, Plan and
// Board; the views are projections of these fields.
type Task struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenantId"`
	ProjectID     *string    `json:"projectId"`
	Title         string     `json:"title"`
	Kind          Kind       `json:"kind"`
	Status        Status     `json:"status"`
	EffortMinutes int        `json:"effortMinutes"`
	Urgent        bool       `json:"urgent"`
	Important     bool       `json:"important"` // the "important" axis (Eisenhower): protects deep long-term work
	Note          string     `json:"note"`
	Reflection    string     `json:"reflection"` // "what did this advance?" — captured at completion (done-and-why)
	Place         *string    `json:"place"`
	ScheduledAt   *time.Time `json:"scheduledAt"` // absolute datetime the task is planned for
	Position      float64    `json:"position"`    // ordering within a column/list
	DoneAt        *time.Time `json:"doneAt"`
	Deadline      *time.Time `json:"deadline"`
	Recurrence    string     `json:"recurrence"` // none|daily|weekdays|weekly|monthly
	Links         []Link     `json:"links"`
	Subtasks      []Subtask  `json:"subtasks"`
	Assignees     []Assignee `json:"assignees"`

	// Work-item fields (migration 0013; see workitem.go). Stage is the
	// lifecycle that replaces Status — the two are kept coherent for one
	// release by DeriveLifecycle, then Status goes.
	Stage             Stage      `json:"stage"`             // todo|doing|waiting|done
	OwnerID           *string    `json:"ownerId"`           // tenant-local users ref
	CreatedBy         *string    `json:"createdBy"`         // the actor at create; attribution only, no FK
	RequirementID     *string    `json:"requirementId"`     // the requirement this item serves (SET NULL on delete)
	DefinitionOfDone  string     `json:"definitionOfDone"`  // what "done" means, in the owner's words
	WaitingOnPersonID *string    `json:"waitingOnPersonId"` // stage=waiting: on whom (users for now; people later)
	WaitingOnReason   string     `json:"waitingOnReason"`   // stage=waiting: why
	WaitingOnSince    *time.Time `json:"waitingOnSince"`    // stamped entering waiting, cleared leaving it
	Ask               Ask        `json:"ask"`               // bounded {what, forWhom, why}
	AskBy             *time.Time `json:"askBy"`             // when the ask is needed by (hoisted, indexed)

	// Provenance counts (0014/0015), DERIVED and read-only: how many signals
	// this item came from and how many artefacts it produced. They ride on
	// every task read so a list of work items can show "where did this come
	// from / what came of it" without a request per row; a patch never sets
	// them (ApplyTaskPatch ignores the keys) and the lists themselves stay at
	// /tasks/{id}/origins and /tasks/{id}/outputs.
	OriginCount int `json:"originCount"`
	OutputCount int `json:"outputCount"`

	Version   int       `json:"version"` // optimistic-concurrency token
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Normalize ensures slice fields are non-nil and recurrence has a value, so the
// JSON contract is stable ([] not null) regardless of source.
func (t *Task) Normalize() {
	if t.Recurrence == "" {
		t.Recurrence = RecurNone
	}
	if t.Links == nil {
		t.Links = []Link{}
	}
	if t.Subtasks == nil {
		t.Subtasks = []Subtask{}
	}
	if t.Assignees == nil {
		t.Assignees = []Assignee{}
	}
	// A row from before 0013 (or a fixture that only set Status) gets its
	// stage derived, so status/stage are coherent on every read.
	if t.Stage == "" {
		t.Status, t.Stage = DeriveLifecycle(t.Status, "", false, t.ScheduledAt != nil)
	}
}

// Entity type labels carried in Event.EntityType. Task/project/client are the
// legacy trio that also populate the typed pointers below; new entity types
// use EntityType + Entity only.
const (
	EntityTask    = "task"
	EntityProject = "project"
	EntityClient  = "client"
	// EntityRequirement rides the generic envelope: Event.Entity carries the
	// requirement body (a small user-authored record, not a captured signal).
	EntityRequirement = "requirement"
)

// Event is a realtime change notification fanned out to a tenant's clients.
//
// The envelope is generic: EntityType names what changed and Entity carries
// its JSON body (metadata only — see NewEntityEvent). The typed Task/Project/
// Client pointers are kept populated for those three types so existing web
// clients keep working; entity types added later use EntityType+Entity alone.
type Event struct {
	ID       string    `json:"id"`
	Type     EventType `json:"type"`
	TenantID string    `json:"tenantId"`
	ActorID  string    `json:"actorId"` // who made the change — lets a client skip its own echo
	Task     *Task     `json:"task,omitempty"`
	Project  *Project  `json:"project,omitempty"`
	Client   *Client   `json:"client,omitempty"`
	// EntityType is always set: task|project|client|… (see Entity* consts).
	EntityType string `json:"entityType"`
	// Entity is the generic body for entity types without a typed pointer.
	// nil for task/project/client (their pointer above carries the body) and
	// for deletes.
	Entity json.RawMessage `json:"entity,omitempty"`
	// EntityID is always set (covers deletes, where Task/Project/Client is nil).
	EntityID string    `json:"entityId"`
	At       time.Time `json:"at"`
}

// NewEntityEvent builds a generic-envelope event, marshalling payload into
// Entity (nil payload = no body, e.g. a delete). Both store adapters route
// new entity types through this so an event is one line at the call site.
//
// RULE — event payloads must never carry signal bodies or participant PII.
// Events are fanned out to every subscriber in the tenant and persisted in
// the outbox for replay, so payload is METADATA ONLY: id, version, kind,
// occurredAt, title and similar. A subscriber that needs the body fetches it
// through the tenant-fenced API.
func NewEntityEvent(typ EventType, tenantID, actorID, entityType, entityID string, payload any) (Event, error) {
	ev := Event{
		ID: NewID(), Type: typ, TenantID: tenantID, ActorID: actorID,
		EntityType: entityType, EntityID: entityID, At: time.Now().UTC(),
	}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Event{}, err
		}
		ev.Entity = b
	}
	return ev, nil
}

// Bootstrap is the single hydration payload the web app loads on start.
type Bootstrap struct {
	Tenant   Tenant    `json:"tenant"`
	User     User      `json:"user"`
	Clients  []Client  `json:"clients"`
	Projects []Project `json:"projects"`
	// Requirements are small and always needed alongside their projects, so
	// they hydrate here. (Signals never do — they are unbounded and paged.)
	Requirements []Requirement `json:"requirements"`
	Tasks        []Task        `json:"tasks"`
	ServerAt     time.Time     `json:"serverAt"`
}
