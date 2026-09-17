package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/cadence/server/internal/domain"
)

// MaxTitleLen bounds a task title so event payloads stay modest.
const MaxTitleLen = 500

// MaxTextLen bounds the free-text work-item and requirement fields
// (definitionOfDone, waitingOnReason, description, acceptance): a note, not
// a document — they ride in Bootstrap and in every task event.
const MaxTextLen = 16 << 10

// CheckTextLen is the shared MaxTextLen guard for those fields.
func CheckTextLen(field, s string) error {
	if len(s) > MaxTextLen {
		return domain.Invalid(field, "is too long")
	}
	return nil
}

// remarshal converts a JSON-decoded value (map/slice of any) into a typed dest
// by round-tripping through JSON. Used for nested patch fields.
func remarshal(v any, dest any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dest)
}

// NewTask validates a create input into a row — defaults, title inference,
// the status/stage derivation and the waiting/ask rules — so both adapters
// build exactly the same record and only differ in how they store it. The
// caller still checks the references (project, owner, requirement, waiting-on
// person) exist in the tenant. actorID becomes CreatedBy ("" → nil).
func NewTask(tenantID, actorID string, in domain.CreateTaskInput, now time.Time) (domain.Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.Task{}, domain.Invalid("title", "is required")
	}
	if len(title) > MaxTitleLen {
		return domain.Task{}, domain.Invalid("title", "is too long")
	}
	kind, effort := domain.Infer(title)
	if in.Kind != nil {
		if !in.Kind.Valid() {
			return domain.Task{}, domain.Invalid("kind", "is invalid")
		}
		kind = *in.Kind
	}
	if in.EffortMinutes != nil {
		if *in.EffortMinutes < 0 {
			return domain.Task{}, domain.Invalid("effortMinutes", "must be a non-negative number")
		}
		effort = *in.EffortMinutes
	}
	status := domain.StatusBacklog
	if in.Status != nil {
		if !in.Status.Valid() {
			return domain.Task{}, domain.Invalid("status", "is invalid")
		}
		status = *in.Status
	}
	var stage domain.Stage
	if in.Stage != nil {
		if !in.Stage.Valid() {
			return domain.Task{}, domain.Invalid("stage", "must be one of todo, doing, waiting, done")
		}
		stage = *in.Stage
	}
	status, stage = domain.DeriveLifecycle(status, stage, in.Stage != nil, in.ScheduledAt != nil)
	if err := CheckTextLen("definitionOfDone", derefString(in.DefinitionOfDone)); err != nil {
		return domain.Task{}, err
	}
	if err := CheckTextLen("waitingOnReason", derefString(in.WaitingOnReason)); err != nil {
		return domain.Task{}, err
	}

	t := domain.Task{
		ID: domain.NewID(), TenantID: tenantID, ProjectID: in.ProjectID, Title: title,
		Kind: kind, Status: status, Stage: stage, EffortMinutes: effort,
		Note: derefString(in.Note), Reflection: derefString(in.Reflection), Place: in.Place,
		ScheduledAt: in.ScheduledAt, Deadline: in.Deadline,
		Links: in.Links, Subtasks: in.Subtasks, Assignees: in.Assignees,
		OwnerID: in.OwnerID, RequirementID: in.RequirementID, DefinitionOfDone: derefString(in.DefinitionOfDone),
		WaitingOnPersonID: in.WaitingOnPersonID, WaitingOnReason: strings.TrimSpace(derefString(in.WaitingOnReason)),
		AskBy:   in.AskBy,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if actorID != "" {
		t.CreatedBy = &actorID
	}
	if in.Urgent != nil {
		t.Urgent = *in.Urgent
	}
	if in.Important != nil {
		t.Important = *in.Important
	}
	if in.Position != nil {
		t.Position = *in.Position
	}
	if in.Recurrence != nil {
		if !domain.ValidRecurrence(*in.Recurrence) {
			return domain.Task{}, domain.Invalid("recurrence", "must be one of none, daily, weekdays, weekly, monthly")
		}
		t.Recurrence = *in.Recurrence
	}
	if in.Ask != nil {
		if err := in.Ask.Validate(); err != nil {
			return domain.Task{}, err
		}
		t.Ask = *in.Ask
	}
	if err := domain.ValidateWaiting(&t); err != nil {
		return domain.Task{}, err
	}
	domain.SyncWaitingSince(&t, "", now)
	if status == domain.StatusDone {
		t.DoneAt = &now
	}
	t.Normalize()
	return t, nil
}

// ApplyTaskPatch mutates t in place from a JSON-decoded patch map, validating
// each field. A key present with a JSON null clears nullable fields. Both the
// in-memory and Postgres adapters route through this so patch semantics stay
// identical regardless of backend.
//
// status and stage are one lifecycle in two columns: setting either derives
// the other (domain.DeriveLifecycle; stage wins if a patch carries both).
// createdBy and waitingOnSince are never patchable — the store owns them.
func ApplyTaskPatch(t *domain.Task, patch map[string]any, now time.Time) error {
	t.Normalize() // a pre-0013 row gets its stage before we compare against it
	was := t.Stage
	statusSet, stageSet := false, false
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
			statusSet = true
		case "stage":
			s, ok := v.(string)
			if !ok || !domain.Stage(s).Valid() {
				return domain.Invalid("stage", "must be one of todo, doing, waiting, done")
			}
			t.Stage = domain.Stage(s)
			stageSet = true
		case "ownerId":
			p, err := nullableString(v, "ownerId")
			if err != nil {
				return err
			}
			t.OwnerID = p
		case "requirementId":
			p, err := nullableString(v, "requirementId")
			if err != nil {
				return err
			}
			t.RequirementID = p
		case "definitionOfDone":
			if v == nil {
				t.DefinitionOfDone = ""
			} else if s, ok := v.(string); ok {
				if err := CheckTextLen("definitionOfDone", s); err != nil {
					return err
				}
				t.DefinitionOfDone = s
			} else {
				return domain.Invalid("definitionOfDone", "must be a string")
			}
		case "waitingOnPersonId":
			p, err := nullableString(v, "waitingOnPersonId")
			if err != nil {
				return err
			}
			t.WaitingOnPersonID = p
		case "waitingOnReason":
			if v == nil {
				t.WaitingOnReason = ""
			} else if s, ok := v.(string); ok {
				t.WaitingOnReason = strings.TrimSpace(s)
			} else {
				return domain.Invalid("waitingOnReason", "must be a string")
			}
		case "ask":
			if v == nil {
				t.Ask = domain.Ask{}
				break
			}
			if _, ok := v.(map[string]any); !ok {
				return domain.Invalid("ask", "must be an object {what, forWhom, why} or null")
			}
			var a domain.Ask
			if err := remarshal(v, &a); err != nil {
				return domain.Invalid("ask", "must be an object {what, forWhom, why} or null")
			}
			if err := a.Validate(); err != nil {
				return err
			}
			t.Ask = a
		case "askBy":
			if v == nil {
				t.AskBy = nil
			} else if s, ok := v.(string); ok {
				tm, err := time.Parse(time.RFC3339, s)
				if err != nil {
					return domain.Invalid("askBy", "must be an RFC3339 timestamp or null")
				}
				t.AskBy = &tm
			} else {
				return domain.Invalid("askBy", "must be a string or null")
			}
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
		case "important":
			b, ok := v.(bool)
			if !ok {
				return domain.Invalid("important", "must be a boolean")
			}
			t.Important = b
		case "note":
			if v == nil {
				t.Note = ""
			} else if s, ok := v.(string); ok {
				t.Note = s
			} else {
				return domain.Invalid("note", "must be a string")
			}
		case "reflection":
			if v == nil {
				t.Reflection = ""
			} else if s, ok := v.(string); ok {
				t.Reflection = s
			} else {
				return domain.Invalid("reflection", "must be a string")
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

	// Lifecycle coherence: whichever of status/stage the patch set drives the
	// other (a patch with neither leaves both alone).
	if statusSet || stageSet {
		t.Status, t.Stage = domain.DeriveLifecycle(t.Status, t.Stage, stageSet, t.ScheduledAt != nil)
	}
	if err := domain.ValidateWaiting(t); err != nil {
		return err
	}
	domain.SyncWaitingSince(t, was, now)

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

// ApplyRequirementPatch mutates r in place from a JSON-decoded patch map,
// validating each field. Both adapters route through it (like
// ApplyTaskPatch) so patch semantics never drift between backends. projectId
// may move the requirement to another project but never be cleared — a
// requirement without a project is meaningless; the caller still has to
// check the target project exists in the tenant.
func ApplyRequirementPatch(r *domain.Requirement, patch map[string]any, now time.Time) error {
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
			r.Title = strings.TrimSpace(s)
		case "projectId":
			s, ok := v.(string)
			if !ok || s == "" {
				return domain.Invalid("projectId", "must be a non-empty string")
			}
			r.ProjectID = s
		case "description":
			if v == nil {
				r.Description = ""
			} else if s, ok := v.(string); ok {
				r.Description = s
			} else {
				return domain.Invalid("description", "must be a string")
			}
		case "acceptance":
			if v == nil {
				r.Acceptance = ""
			} else if s, ok := v.(string); ok {
				r.Acceptance = s
			} else {
				return domain.Invalid("acceptance", "must be a string")
			}
		case "weight":
			f, ok := v.(float64)
			if !ok || f != float64(int(f)) || int(f) < domain.RequirementWeightMin || int(f) > domain.RequirementWeightMax {
				return domain.Invalid("weight", "must be a whole number from 1 to 5")
			}
			r.Weight = int(f)
		case "status":
			s, ok := v.(string)
			if !ok || !domain.ValidRequirementStatus(s) {
				return domain.Invalid("status", "must be one of open, met, dropped")
			}
			r.Status = s
		case "position":
			f, ok := v.(float64)
			if !ok {
				return domain.Invalid("position", "must be a number")
			}
			r.Position = f
		case "archived":
			b, ok := v.(bool)
			if !ok {
				return domain.Invalid("archived", "must be a boolean")
			}
			if b {
				if r.ArchivedAt == nil {
					at := now
					r.ArchivedAt = &at
				}
			} else {
				r.ArchivedAt = nil
			}
		default:
			// Unknown keys are ignored so older/newer clients interoperate.
		}
	}
	return nil
}

// ApplySourcePatch mutates s in place from a JSON-decoded patch map. kind is
// fixed at create (a calendar source does not become an email source); the
// caller checks ownerId exists in the tenant.
func ApplySourcePatch(s *domain.Source, patch map[string]any, now time.Time) error {
	for k, v := range patch {
		switch k {
		case "name":
			str, ok := v.(string)
			if !ok || strings.TrimSpace(str) == "" {
				return domain.Invalid("name", "must be a non-empty string")
			}
			if len(str) > MaxTitleLen {
				return domain.Invalid("name", "is too long")
			}
			s.Name = strings.TrimSpace(str)
		case "kind":
			return domain.Invalid("kind", "is immutable")
		case "ownerId":
			p, err := nullableString(v, "ownerId")
			if err != nil {
				return err
			}
			s.OwnerID = p
		case "config":
			if v == nil {
				s.Config = json.RawMessage(`{}`)
				break
			}
			if _, ok := v.(map[string]any); !ok {
				return domain.Invalid("config", "must be a JSON object")
			}
			raw, err := json.Marshal(v)
			if err != nil || len(raw) > domain.MaxSourceConfigBytes {
				return domain.Invalid("config", "is too large")
			}
			s.Config = raw
		case "consentAt":
			t, err := nullableTime(v, "consentAt")
			if err != nil {
				return err
			}
			s.ConsentAt = t
		case "retentionDays":
			n, err := nullableInt(v, "retentionDays")
			if err != nil {
				return err
			}
			if n != nil && *n <= 0 {
				return domain.Invalid("retentionDays", "must be positive")
			}
			s.RetentionDays = n
		case "disabled":
			b, ok := v.(bool)
			if !ok {
				return domain.Invalid("disabled", "must be a boolean")
			}
			if b {
				if s.DisabledAt == nil {
					at := now
					s.DisabledAt = &at
				}
			} else {
				s.DisabledAt = nil
			}
		default:
			// Unknown keys are ignored so older/newer clients interoperate.
		}
	}
	s.Normalize()
	return nil
}

// signalImmutable is every Signal field a patch may never touch. It mirrors
// the Postgres signals_immutable trigger (0014) so the memory adapter refuses
// exactly what Postgres would, and the caller gets a 400 with the field name
// rather than a 500 from the trigger.
var signalImmutable = map[string]bool{
	"id": true, "tenantId": true, "sourceId": true, "externalId": true, "kind": true,
	"title": true, "excerpt": true, "bodyRef": true, "occurredAt": true, "capturedBy": true,
	"participants": true, "dispositionAt": true, "version": true, "createdAt": true, "updatedAt": true,
}

// ApplySignalPatch is the disposition patch: stage, snoozedUntil, projectHint,
// extracted, retentionUntil — nothing else (signals are immutable records).
// Rules: snoozed needs a snoozedUntil (a snoozedUntil alone implies snoozed);
// leaving snoozed clears it; leaving inbox stamps dispositionAt, returning
// to inbox clears it. The caller checks projectHint exists in the tenant.
func ApplySignalPatch(s *domain.Signal, patch map[string]any, now time.Time) error {
	s.Normalize()
	was := s.Stage
	stageSet, snoozeSet := false, false
	for k, v := range patch {
		if signalImmutable[k] {
			return domain.Invalid(k, "is immutable")
		}
		switch k {
		case "stage":
			str, ok := v.(string)
			if !ok || !domain.SignalStage(str).Valid() {
				return domain.Invalid("stage", "must be one of inbox, snoozed, promoted, attached, dismissed")
			}
			s.Stage = domain.SignalStage(str)
			stageSet = true
		case "snoozedUntil":
			t, err := nullableTime(v, "snoozedUntil")
			if err != nil {
				return err
			}
			s.SnoozedUntil = t
			snoozeSet = true
		case "projectHint":
			p, err := nullableString(v, "projectHint")
			if err != nil {
				return err
			}
			s.ProjectHint = p
		case "extracted":
			if v == nil {
				s.Extracted = json.RawMessage(`{}`)
				break
			}
			if _, ok := v.(map[string]any); !ok {
				return domain.Invalid("extracted", "must be a JSON object")
			}
			raw, err := json.Marshal(v)
			if err != nil || len(raw) > domain.MaxSignalExtractedLen {
				return domain.Invalid("extracted", "is too large")
			}
			s.Extracted = raw
		case "retentionUntil":
			t, err := nullableTime(v, "retentionUntil")
			if err != nil {
				return err
			}
			s.RetentionUntil = t
		default:
			// Unknown keys are ignored so older/newer clients interoperate.
		}
	}
	if snoozeSet && !stageSet && s.SnoozedUntil != nil {
		s.Stage = domain.SignalSnoozed
	}
	if s.Stage == domain.SignalSnoozed && s.SnoozedUntil == nil {
		return domain.Invalid("snoozedUntil", "snoozing needs a time to come back")
	}
	if s.Stage != domain.SignalSnoozed {
		s.SnoozedUntil = nil
	}
	switch {
	case s.Stage == domain.SignalInbox:
		s.DispositionAt = nil
	case s.Stage != was || s.DispositionAt == nil:
		at := now
		s.DispositionAt = &at
	}
	return nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullableTime(v any, field string) (*time.Time, error) {
	if v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, domain.Invalid(field, "must be an RFC3339 timestamp or null")
	}
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, domain.Invalid(field, "must be an RFC3339 timestamp or null")
	}
	tm = tm.UTC().Truncate(time.Microsecond)
	return &tm, nil
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
