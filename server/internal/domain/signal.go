package domain

import (
	"encoding/json"
	"time"
)

// Signals (migration 0014) are captured things — meeting notes, a pasted
// email, a link, a chat message, a document. A signal is NEVER a task: work
// items derive from signals (Origin, below) the way they derive from
// requirements, and the signal stays behind as the immutable record of what
// arrived. Signals are unbounded, so they are paged and never in Bootstrap.
// Outputs (migration 0015) are the other end: what a work item produced.

// ---- sources -----------------------------------------------------------------

// Source kinds. 'manual' is the paste box — every tenant has exactly one,
// created lazily by the store (GetOrCreateManualSource); the rest are
// connectors, added by their own slices.
const (
	SourceManual   = "manual"
	SourceCalendar = "calendar"
	SourceEmail    = "email"
	SourceNotes    = "notes"
	SourceDocs     = "docs"
	SourceChat     = "chat"
)

func ValidSourceKind(k string) bool {
	switch k {
	case SourceManual, SourceCalendar, SourceEmail, SourceNotes, SourceDocs, SourceChat:
		return true
	}
	return false
}

// ManualSourceName is the display name of the implicit manual source.
const ManualSourceName = "Manual capture"

// MaxSourceConfigBytes bounds Source.Config — settings, not a document.
const MaxSourceConfigBytes = 16 << 10

// Source is where signals come from. Config is NON-SECRET connector settings
// (a calendar id, a folder name) and is safe to list; credentials live in a
// separate table added by a later slice, never here.
type Source struct {
	ID       string          `json:"id"`
	TenantID string          `json:"tenantId"`
	Kind     string          `json:"kind"` // manual|calendar|email|notes|docs|chat
	Name     string          `json:"name"`
	OwnerID  *string         `json:"ownerId"` // tenant-local users ref (SET NULL on delete)
	Config   json.RawMessage `json:"config"`  // always a JSON object; {} when empty
	// ConsentAt is when the owner consented to this source being read; nil =
	// not yet. RetentionDays expires this source's signals that many days
	// after they occurred (copied onto Signal.RetentionUntil at capture);
	// nil = keep.
	ConsentAt     *time.Time `json:"consentAt"`
	RetentionDays *int       `json:"retentionDays"`
	DisabledAt    *time.Time `json:"disabledAt"`
	Version       int        `json:"version"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// Normalize keeps the JSON contract stable: Config is always an object.
func (s *Source) Normalize() {
	if len(s.Config) == 0 {
		s.Config = json.RawMessage(`{}`)
	}
}

// CreateSourceInput is the payload for POST /sources. Kind and name are
// required; 'manual' cannot be created by hand (the store owns the one
// manual source).
type CreateSourceInput struct {
	Kind          string          `json:"kind"`
	Name          string          `json:"name"`
	OwnerID       *string         `json:"ownerId"`
	Config        json.RawMessage `json:"config"`
	ConsentAt     *time.Time      `json:"consentAt"`
	RetentionDays *int            `json:"retentionDays"`
}

// ---- signals -----------------------------------------------------------------

// Signal kinds: what shape of thing was captured.
const (
	SignalMeeting = "meeting"
	SignalEmail   = "email"
	SignalNote    = "note"
	SignalDoc     = "doc"
	SignalChat    = "chat"
	SignalLink    = "link"
	SignalText    = "text"
)

func ValidSignalKind(k string) bool {
	switch k {
	case SignalMeeting, SignalEmail, SignalNote, SignalDoc, SignalChat, SignalLink, SignalText:
		return true
	}
	return false
}

// SignalStage is the signal's disposition — the ONLY thing about a signal
// that changes after capture. inbox → snoozed (comes back at snoozedUntil) |
// promoted (a work item was made from it) | attached (linked to an existing
// work item) | dismissed.
type SignalStage string

const (
	SignalInbox     SignalStage = "inbox"
	SignalSnoozed   SignalStage = "snoozed"
	SignalPromoted  SignalStage = "promoted"
	SignalAttached  SignalStage = "attached"
	SignalDismissed SignalStage = "dismissed"
)

func (s SignalStage) Valid() bool {
	switch s {
	case SignalInbox, SignalSnoozed, SignalPromoted, SignalAttached, SignalDismissed:
		return true
	}
	return false
}

// Capture bounds. A manual capture is at most MaxSignalTextBytes of text; the
// first MaxSignalExcerptLen characters become the excerpt on the signal row
// and the whole thing becomes the body. Extracted is bounded because it rides
// on the row that every list returns.
const (
	MaxSignalTextBytes    = 200 << 10
	MaxSignalExcerptLen   = 2000
	MaxSignalExtractedLen = 16 << 10
	// MaxSignalParticipants bounds one capture's participant list, and
	// MaxParticipantEmailLen an address on it (RFC 5321's 254): participants
	// ride on every list row, so an unbounded list would make a single
	// capture inflate every page that includes it.
	MaxSignalParticipants  = 200
	MaxParticipantEmailLen = 254
	// MaxSignalRefLen bounds the source-native identifiers (externalId,
	// bodyRef): a message id or a URL, and — for externalId — short enough for
	// the (tenant, source, external_id) unique index, which caps a btree
	// entry at ~2.7 KB.
	MaxSignalRefLen = 1024
)

// Signal is the captured record. Everything above the disposition block is
// immutable after insert (a BEFORE UPDATE trigger enforces it in Postgres;
// store.ApplySignalPatch refuses it on both adapters). The body is NOT here —
// it lives in SignalBody and is fetched on its own so a list never carries a
// hundred transcripts.
type Signal struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	SourceID   string    `json:"sourceId"`
	ExternalID string    `json:"externalId"` // the source's own id; the signal id for manual captures
	Kind       string    `json:"kind"`       // meeting|email|note|doc|chat|link|text
	Title      string    `json:"title"`
	Excerpt    string    `json:"excerpt"` // ≤ MaxSignalExcerptLen characters
	BodyRef    string    `json:"bodyRef"` // source-native locator for the full thing; may be ""
	OccurredAt time.Time `json:"occurredAt"`
	CapturedBy *string   `json:"capturedBy"` // the actor at capture; attribution only, no FK

	// Disposition — the mutable part.
	Stage          SignalStage     `json:"stage"`
	SnoozedUntil   *time.Time      `json:"snoozedUntil"`
	DispositionAt  *time.Time      `json:"dispositionAt"` // when it left inbox; nil while in inbox
	Extracted      json.RawMessage `json:"extracted"`     // facts pulled from the body; always an object
	ProjectHint    *string         `json:"projectHint"`   // tenant-local projects ref (SET NULL on delete)
	RetentionUntil *time.Time      `json:"retentionUntil"`

	// Participants are returned with the signal (they are small and needed to
	// triage) but NEVER in an event.
	Participants []Participant `json:"participants"`

	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Normalize keeps the JSON contract stable ({} and [] rather than null).
func (s *Signal) Normalize() {
	if len(s.Extracted) == 0 {
		s.Extracted = json.RawMessage(`{}`)
	}
	if s.Participants == nil {
		s.Participants = []Participant{}
	}
	if s.Stage == "" {
		s.Stage = SignalInbox
	}
}

// SignalMeta is the event payload for signal.* events: id, version, kind,
// title, occurredAt, stage — and nothing else. Excerpt, body and participants
// never leave the tenant-fenced API (see NewEntityEvent).
type SignalMeta struct {
	ID         string      `json:"id"`
	Version    int         `json:"version"`
	Kind       string      `json:"kind"`
	Title      string      `json:"title"`
	OccurredAt time.Time   `json:"occurredAt"`
	Stage      SignalStage `json:"stage"`
}

func (s Signal) Meta() SignalMeta {
	return SignalMeta{ID: s.ID, Version: s.Version, Kind: s.Kind, Title: s.Title, OccurredAt: s.OccurredAt, Stage: s.Stage}
}

// SignalBody is the full captured text, one per signal, read through
// GET /signals/{id}/body only.
type SignalBody struct {
	TenantID  string    `json:"tenantId"`
	SignalID  string    `json:"signalId"`
	Body      string    `json:"body"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// Participant roles.
const (
	RoleFrom     = "from"
	RoleTo       = "to"
	RoleCC       = "cc"
	RoleAttendee = "attendee"
	RoleSpeaker  = "speaker"
)

func ValidParticipantRole(r string) bool {
	switch r {
	case RoleFrom, RoleTo, RoleCC, RoleAttendee, RoleSpeaker:
		return true
	}
	return false
}

// Participant is someone on a signal. This is PII: it is stored with the
// signal, indexed by email for data-subject requests, and never put in an
// event or an audit row. PersonID is reserved for the people table (later).
type Participant struct {
	Idx      int     `json:"idx"`
	Name     string  `json:"name"`
	Email    *string `json:"email"`
	Role     string  `json:"role"` // from|to|cc|attendee|speaker
	PersonID *string `json:"personId"`
}

// CreateSignalInput is the payload for POST /signals — the manual capture
// path. Only kind and one of title/text are required. Text (≤ 200 KB) becomes
// the body in full and its first 2000 characters the excerpt; Links are
// seeded into Extracted as {"links": […]}. SourceID/ExternalID/BodyRef are
// for connectors; a manual capture leaves them unset (manual source, own id).
type CreateSignalInput struct {
	SourceID       *string         `json:"sourceId"`
	ExternalID     *string         `json:"externalId"`
	Kind           string          `json:"kind"`
	Title          string          `json:"title"`
	Text           string          `json:"text"`
	BodyRef        *string         `json:"bodyRef"`
	OccurredAt     *time.Time      `json:"occurredAt"`
	Participants   []Participant   `json:"participants"`
	Links          []Link          `json:"links"`
	Extracted      json.RawMessage `json:"extracted"`
	ProjectHint    *string         `json:"projectHint"`
	RetentionUntil *time.Time      `json:"retentionUntil"`
}

// ---- origins -----------------------------------------------------------------

// OriginSnapshot is what a work item keeps of the signal it came from, copied
// at attach time so a purged signal still leaves the provenance behind (see
// the 0014 migration header for the trade-off).
type OriginSnapshot struct {
	Title      string    `json:"title"`
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurredAt"`
	BodyRef    string    `json:"bodyRef"`
}

// Origin is one work_item_signals row: this work item derives from this
// signal. The signal may no longer exist; Snapshot always does.
type Origin struct {
	TenantID  string         `json:"tenantId"`
	TaskID    string         `json:"taskId"`
	SignalID  string         `json:"signalId"`
	Snapshot  OriginSnapshot `json:"snapshot"`
	CreatedAt time.Time      `json:"createdAt"`
}

// ---- outputs -----------------------------------------------------------------

// Output kinds: what shape of thing a work item produced.
const (
	OutputLink     = "link"
	OutputDoc      = "doc"
	OutputPR       = "pr"
	OutputEmail    = "email"
	OutputDecision = "decision"
	OutputFile     = "file"
	OutputNote     = "note"
)

func ValidOutputKind(k string) bool {
	switch k {
	case OutputLink, OutputDoc, OutputPR, OutputEmail, OutputDecision, OutputFile, OutputNote:
		return true
	}
	return false
}

// MaxOutputDetailLen bounds Output.Detail: a note about the artefact, not the
// artefact. MaxOutputURLLen bounds the locator (a URL, not a document).
const (
	MaxOutputDetailLen = 4000
	MaxOutputURLLen    = 2048
)

// Output is an artefact a work item produced. Created and deleted, never
// edited, and it goes with its work item.
type Output struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	TaskID    string    `json:"taskId"`
	Kind      string    `json:"kind"` // link|doc|pr|email|decision|file|note
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Detail    string    `json:"detail"`
	CreatedBy *string   `json:"createdBy"` // attribution only, no FK
	CreatedAt time.Time `json:"createdAt"`
}

// CreateOutputInput is the payload for POST /tasks/{id}/outputs.
type CreateOutputInput struct {
	Kind   string  `json:"kind"`
	Title  string  `json:"title"`
	URL    *string `json:"url"`
	Detail *string `json:"detail"`
}

// ---- events ------------------------------------------------------------------

// Entity type labels for the generic event envelope.
const (
	EntitySource = "source"
	EntitySignal = "signal"
	EntityOrigin = "origin" // EntityID is the task; the body names the signal
	EntityOutput = "output"
)

// Event types. signal.* bodies are SignalMeta — METADATA ONLY, never the
// excerpt, body or participants. origin.* bodies are the Origin row (its
// snapshot is title/kind/occurredAt/bodyRef, which is metadata too).
const (
	EventSourceCreated  EventType = "source.created"
	EventSourceUpdated  EventType = "source.updated"
	EventSourceDeleted  EventType = "source.deleted"
	EventSignalCreated  EventType = "signal.created"
	EventSignalUpdated  EventType = "signal.updated"
	EventSignalDeleted  EventType = "signal.deleted"
	EventOriginAttached EventType = "origin.attached"
	EventOriginDetached EventType = "origin.detached"
	EventOutputCreated  EventType = "output.created"
	EventOutputDeleted  EventType = "output.deleted"
)

// Audit kinds for the signal surface. A capture and a delete are recorded
// because they change what the workspace holds; a body read is recorded
// because the body is the sensitive part. None of them carry the content.
//
// The three bulk paths are recorded too, and for the same reason: deleting a
// source cascades every signal filed under it, changing a signal's
// retentionUntil arms (or disarms) that deletion, and the hourly sweep is what
// finally carries it out. A delete nobody can see afterwards is the one kind
// of delete this log exists to prevent, so each carries its count.
const (
	AuditSignalCapture   = "signal.capture"
	AuditSignalDelete    = "signal.delete"
	AuditSignalBodyRead  = "signal.body.read"
	AuditSignalRetention = "signal.retention" // retentionUntil was set or cleared on a signal
	AuditSignalPrune     = "signal.prune"     // the hourly retention sweep deleted {signals: n}
	AuditSourceDelete    = "source.delete"    // a source went, taking {signals: n} with it
)
