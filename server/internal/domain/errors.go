package domain

import (
	"errors"
	"time"
)

// Sentinel errors the HTTP layer maps to status codes.
var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("version conflict")
	ErrForbidden = errors.New("forbidden")
)

// ValidationError is a 400 with a human-readable message and an optional field.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return e.Field + ": " + e.Message
	}
	return e.Message
}

func Invalid(field, msg string) error { return &ValidationError{Field: field, Message: msg} }

// CreateTaskInput is the payload for POST /tasks. Unset optional fields are
// filled with sensible defaults / inference by the service.
type CreateTaskInput struct {
	Title         string     `json:"title"`
	Kind          *Kind      `json:"kind"`
	ProjectID     *string    `json:"projectId"`
	Status        *Status    `json:"status"`
	EffortMinutes *int       `json:"effortMinutes"`
	Urgent        *bool      `json:"urgent"`
	Note          *string    `json:"note"`
	Place         *string    `json:"place"`
	ScheduledAt   *time.Time `json:"scheduledAt"`
	Position      *float64   `json:"position"`
	Deadline      *time.Time `json:"deadline"`
	Recurrence    *string    `json:"recurrence"`
	Links         []Link     `json:"links"`
	Subtasks      []Subtask  `json:"subtasks"`
	Assignees     []Assignee `json:"assignees"`
}

// CreateProjectInput is the payload for POST /projects.
type CreateProjectInput struct {
	Name     string  `json:"name"`
	Subtitle *string `json:"subtitle"`
	Due      *string `json:"due"`
	Color    *string `json:"color"`
}
