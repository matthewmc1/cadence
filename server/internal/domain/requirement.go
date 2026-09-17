package domain

import "time"

// Requirement statuses: open until it is either met or consciously dropped.
const (
	RequirementOpen    = "open"
	RequirementMet     = "met"
	RequirementDropped = "dropped"
)

func ValidRequirementStatus(s string) bool {
	switch s {
	case RequirementOpen, RequirementMet, RequirementDropped:
		return true
	}
	return false
}

// Requirement weight bounds: 1 = nice to have … 5 = the project fails without it.
const (
	RequirementWeightMin     = 1
	RequirementWeightMax     = 5
	RequirementWeightDefault = 3
)

// Requirement is one thing a project must deliver. It is never a task — work
// items derive from requirements the way they derive from signals — and it
// belongs to exactly one project, going with it when the project is deleted.
type Requirement struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenantId"`
	ProjectID   string     `json:"projectId"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Weight      int        `json:"weight"`     // 1..5
	Acceptance  string     `json:"acceptance"` // how we will know it is met
	Status      string     `json:"status"`     // open|met|dropped
	Position    float64    `json:"position"`   // ordering within the project
	ArchivedAt  *time.Time `json:"archivedAt"`
	Version     int        `json:"version"` // optimistic-concurrency token
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// CreateRequirementInput is the payload for POST /requirements. Only
// projectId and title are required.
type CreateRequirementInput struct {
	ProjectID   string   `json:"projectId"`
	Title       string   `json:"title"`
	Description *string  `json:"description"`
	Weight      *int     `json:"weight"`
	Acceptance  *string  `json:"acceptance"`
	Status      *string  `json:"status"`
	Position    *float64 `json:"position"`
}
