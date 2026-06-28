package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/cadence/server/internal/domain"
)

// MaxTitleLen bounds a task title so event payloads stay modest.
const MaxTitleLen = 500

// remarshal converts a JSON-decoded value (map/slice of any) into a typed dest
// by round-tripping through JSON. Used for nested patch fields.
func remarshal(v any, dest any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dest)
}

// ApplyTaskPatch mutates t in place from a JSON-decoded patch map, validating
// each field. A key present with a JSON null clears nullable fields. Both the
// in-memory and Postgres adapters route through this so patch semantics stay
// identical regardless of backend.
func ApplyTaskPatch(t *domain.Task, patch map[string]any, now time.Time) error {
	for k, v := range patch {
		switch k {
		case "title":
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return domain.Invalid("title", "must be a non-empty string")
			}
			if len(s) > MaxTitleLen {
				return domain.Invalid("title", "is too long")
			}
			t.Title = strings.TrimSpace(s)
		case "kind":
			s, ok := v.(string)
			if !ok || !domain.Kind(s).Valid() {
				return domain.Invalid("kind", "must be one of deep, light, admin, meet, personal")
			}
			t.Kind = domain.Kind(s)
		case "status":
			s, ok := v.(string)
			if !ok || !domain.Status(s).Valid() {
				return domain.Invalid("status", "must be one of backlog, scheduled, focus, done")
			}
			t.Status = domain.Status(s)
		case "effortMinutes":
			f, ok := v.(float64)
			if !ok || f < 0 {
				return domain.Invalid("effortMinutes", "must be a non-negative number")
			}
			t.EffortMinutes = int(f)
		case "urgent":
			b, ok := v.(bool)
			if !ok {
				return domain.Invalid("urgent", "must be a boolean")
			}
			t.Urgent = b
		case "note":
			if v == nil {
				t.Note = ""
			} else if s, ok := v.(string); ok {
				t.Note = s
			} else {
				return domain.Invalid("note", "must be a string")
			}
		case "projectId":
			p, err := nullableString(v, "projectId")
			if err != nil {
				return err
			}
			t.ProjectID = p
		case "place":
			p, err := nullableString(v, "place")
			if err != nil {
				return err
			}
			t.Place = p
		case "scheduledAt":
			if v == nil {
				t.ScheduledAt = nil
			} else if s, ok := v.(string); ok {
				tm, err := time.Parse(time.RFC3339, s)
				if err != nil {
					return domain.Invalid("scheduledAt", "must be an RFC3339 timestamp or null")
				}
				t.ScheduledAt = &tm
			} else {
				return domain.Invalid("scheduledAt", "must be a string or null")
			}
		case "position":
			f, ok := v.(float64)
			if !ok {
				return domain.Invalid("position", "must be a number")
			}
			t.Position = f
		case "recurrence":
			s, ok := v.(string)
			if !ok || !domain.ValidRecurrence(s) {
				return domain.Invalid("recurrence", "must be one of none, daily, weekdays, weekly, monthly")
			}
			t.Recurrence = s
		case "deadline":
			if v == nil {
				t.Deadline = nil
			} else if s, ok := v.(string); ok {
				tm, err := time.Parse(time.RFC3339, s)
				if err != nil {
					return domain.Invalid("deadline", "must be an RFC3339 timestamp or null")
				}
				t.Deadline = &tm
			} else {
				return domain.Invalid("deadline", "must be a string or null")
			}
		case "links":
			var links []domain.Link
			if err := remarshal(v, &links); err != nil {
				return domain.Invalid("links", "must be a list of {label,url}")
			}
			t.Links = links
		case "subtasks":
			var subs []domain.Subtask
			if err := remarshal(v, &subs); err != nil {
				return domain.Invalid("subtasks", "must be a list of {id,title,done}")
			}
			for i := range subs {
				if subs[i].ID == "" {
					subs[i].ID = domain.NewID()
				}
			}
			t.Subtasks = subs
		case "assignees":
			var as []domain.Assignee
			if err := remarshal(v, &as); err != nil {
				return domain.Invalid("assignees", "must be a list of {userId,initial,color}")
			}
			t.Assignees = as
		default:
			// Unknown keys are ignored so older/newer clients interoperate.
		}
	}

	// Completion bookkeeping: done implies a doneAt timestamp; leaving done clears it.
	if t.Status == domain.StatusDone && t.DoneAt == nil {
		t.DoneAt = &now
	}
	if t.Status != domain.StatusDone {
		t.DoneAt = nil
	}
	t.Normalize()
	return nil
}

func nullableString(v any, field string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, domain.Invalid(field, "must be a string or null")
	}
	return &s, nil
}

func nullableInt(v any, field string) (*int, error) {
	if v == nil {
		return nil, nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil, domain.Invalid(field, "must be a number or null")
	}
	i := int(f)
	return &i, nil
}

func nullableFloat(v any, field string) (*float64, error) {
	if v == nil {
		return nil, nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil, domain.Invalid(field, "must be a number or null")
	}
	return &f, nil
}
