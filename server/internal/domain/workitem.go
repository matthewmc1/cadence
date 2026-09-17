package domain

import "time"

// WorkItem is what a Task becomes in the context-first model: a task that
// carries who owns it, what requirement it serves, what "done" means, who it
// is waiting on and the ask it makes. The physical table stays `tasks` and
// the type stays Task for now; the alias lets new code say what it means.
type WorkItem = Task

// Stage is where a work item sits: todo|doing|waiting|done. It replaces
// Status, which is kept coherent alongside it for one release (see
// DeriveLifecycle) and then goes.
type Stage string

const (
	StageTodo    Stage = "todo"
	StageDoing   Stage = "doing"
	StageWaiting Stage = "waiting"
	StageDone    Stage = "done"
)

func (s Stage) Valid() bool {
	switch s {
	case StageTodo, StageDoing, StageWaiting, StageDone:
		return true
	}
	return false
}

// DeriveLifecycle is the ONE status<->stage mapping. Callers pass whichever
// of the two they set; the other is derived so the pair is always coherent.
// When stageSet is true the stage is authoritative (it is the newer field,
// and the one that survives), otherwise status is.
//
//	status    → stage        stage   → status
//	backlog   → todo         todo    → scheduled if scheduledAt set, else backlog
//	scheduled → todo         doing   → focus
//	focus     → doing        waiting → backlog
//	done      → done         done    → done
//
// Migration 0013 applies the status→stage half once to existing rows; both
// create paths and ApplyTaskPatch apply it live. Unknown values map to the
// backlog/todo pair so a stale row can never end up incoherent.
func DeriveLifecycle(status Status, stage Stage, stageSet bool, scheduled bool) (Status, Stage) {
	if stageSet {
		switch stage {
		case StageDoing:
			return StatusFocus, StageDoing
		case StageWaiting:
			return StatusBacklog, StageWaiting
		case StageDone:
			return StatusDone, StageDone
		default: // todo
			if scheduled {
				return StatusScheduled, StageTodo
			}
			return StatusBacklog, StageTodo
		}
	}
	switch status {
	case StatusScheduled:
		return StatusScheduled, StageTodo
	case StatusFocus:
		return StatusFocus, StageDoing
	case StatusDone:
		return StatusDone, StageDone
	default: // backlog
		return StatusBacklog, StageTodo
	}
}

// MaxAskFieldLen bounds each Ask string so the jsonb column (and the event
// payload it rides in) stays modest.
const MaxAskFieldLen = 2000

// Ask is what a work item asks of someone else: a bounded {what, forWhom,
// why}. It is stored as jsonb on the row ({} when empty) and AskBy — when
// the ask is needed by — is hoisted to its own column so it indexes like the
// deadline it effectively is.
type Ask struct {
	What    string `json:"what"`
	ForWhom string `json:"forWhom"`
	Why     string `json:"why"`
}

func (a Ask) IsZero() bool { return a == Ask{} }

// Validate enforces the per-field bound; the shape itself is fixed by the type.
func (a Ask) Validate() error {
	for _, f := range []struct{ name, v string }{{"what", a.What}, {"forWhom", a.ForWhom}, {"why", a.Why}} {
		if len(f.v) > MaxAskFieldLen {
			return Invalid("ask", f.name+" is too long")
		}
	}
	return nil
}

// ValidateWaiting is the waiting rule: a work item parked on someone needs
// to say who or why, or the stage means nothing. Both create paths and the
// patch route through it.
func ValidateWaiting(t *Task) error {
	if t.Stage == StageWaiting && t.WaitingOnReason == "" && t.WaitingOnPersonID == nil {
		return Invalid("waitingOnReason", "waiting needs a reason or a person to wait on")
	}
	return nil
}

// SyncWaitingSince keeps waiting_on_since honest around a stage change:
// entering waiting stamps it (once), leaving clears it.
func SyncWaitingSince(t *Task, was Stage, now time.Time) {
	switch {
	case t.Stage == StageWaiting && (was != StageWaiting || t.WaitingOnSince == nil):
		at := now
		t.WaitingOnSince = &at
	case t.Stage != StageWaiting:
		t.WaitingOnSince = nil
	}
}
