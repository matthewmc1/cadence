package domain

import "time"

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
	// ExpectedTouchDays is the cadence target: flag the client "underserved"
	// when it goes untouched longer than this. nil = no expectation set.
	ExpectedTouchDays *int       `json:"expectedTouchDays"`
	ArchivedAt        *time.Time `json:"archivedAt"`
	Version           int        `json:"version"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// Project groups tasks toward an outcome with a due date.
type Project struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenantId"`
	ClientID  *string         `json:"clientId"` // the client this project serves (nil = unassigned)
	Name      string          `json:"name"`
	Subtitle  string          `json:"subtitle"`
	Due       *string         `json:"due"`
	Color     string          `json:"color"`
	Members   []ProjectMember `json:"members"`
	Version   int             `json:"version"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
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
	Version       int        `json:"version"` // optimistic-concurrency token
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
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
}

// Event is a realtime change notification fanned out to a tenant's clients.
type Event struct {
	ID       string    `json:"id"`
	Type     EventType `json:"type"`
	TenantID string    `json:"tenantId"`
	ActorID  string    `json:"actorId"` // who made the change — lets a client skip its own echo
	Task     *Task     `json:"task,omitempty"`
	Project  *Project  `json:"project,omitempty"`
	Client   *Client   `json:"client,omitempty"`
	// EntityID is always set (covers deletes, where Task/Project/Client is nil).
	EntityID string    `json:"entityId"`
	At       time.Time `json:"at"`
}

// Bootstrap is the single hydration payload the web app loads on start.
type Bootstrap struct {
	Tenant   Tenant    `json:"tenant"`
	User     User      `json:"user"`
	Clients  []Client  `json:"clients"`
	Projects []Project `json:"projects"`
	Tasks    []Task    `json:"tasks"`
	ServerAt time.Time `json:"serverAt"`
}
