package store

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cadence/server/internal/domain"
)

// Shared create builders for the signal surface (sources, signals, outputs).
// Like NewTask, they validate an input into the exact row both adapters
// store, so the two never disagree on defaults or rejections; the adapter
// then only checks references and persists. Timestamps are µs-truncated so
// what create returns equals what Postgres reads back.

// SignalFilter narrows ListSignals / CountSignals. Nil fields are ignored.
type SignalFilter struct {
	Stage         *domain.SignalStage
	SourceID      *string
	ProjectHint   *string
	OccurredAfter *time.Time // strictly after
}

// SignalKey is the paging key for signals: occurred_at, id.
func SignalKey(s domain.Signal) (time.Time, string) { return s.OccurredAt, s.ID }

// NewSource validates a CreateSourceInput into a row. 'manual' is refused —
// the store owns the one manual source per tenant (NewManualSource).
func NewSource(tenantID string, in domain.CreateSourceInput, now time.Time) (domain.Source, error) {
	kind := strings.TrimSpace(in.Kind)
	if !domain.ValidSourceKind(kind) {
		return domain.Source{}, domain.Invalid("kind", "must be one of calendar, email, notes, docs, chat")
	}
	if kind == domain.SourceManual {
		return domain.Source{}, domain.Invalid("kind", "the manual source is created for you")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return domain.Source{}, domain.Invalid("name", "is required")
	}
	if len(name) > MaxTitleLen {
		return domain.Source{}, domain.Invalid("name", "is too long")
	}
	cfg, err := jsonObject(in.Config, "config", domain.MaxSourceConfigBytes)
	if err != nil {
		return domain.Source{}, err
	}
	if in.RetentionDays != nil && *in.RetentionDays <= 0 {
		return domain.Source{}, domain.Invalid("retentionDays", "must be positive")
	}
	now = now.UTC().Truncate(time.Microsecond)
	s := domain.Source{
		ID: domain.NewID(), TenantID: tenantID, Kind: kind, Name: name, OwnerID: in.OwnerID,
		Config: cfg, ConsentAt: in.ConsentAt, RetentionDays: in.RetentionDays,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	return s, nil
}

// ErrManualSourceDelete is what both adapters answer when DeleteSource names
// the implicit manual source. It cascades to every pasted capture in the
// tenant — bodies and participants included — and the paste box would have no
// home afterwards, so the one source a user never created is the one they
// cannot delete. (A signal at a time still goes through DELETE /signals/{id}.)
func ErrManualSourceDelete() error {
	return domain.Invalid("id", "the manual source is where every pasted capture is filed — it cannot be deleted")
}

// PruneAuditEntry is the row one tenant's retention sweep leaves behind: how
// many signals it deleted, and nothing whatever about them. It is a system row
// (no actor) written by the adapter itself, in the same transaction as the
// delete where the adapter has one.
func PruneAuditEntry(n int) domain.AuditEntry {
	return domain.AuditEntry{
		Kind:   domain.AuditSignalPrune,
		Detail: json.RawMessage(`{"signals":` + strconv.Itoa(n) + `}`),
	}
}

// NewManualSource is the implicit per-tenant paste-box source.
func NewManualSource(tenantID string, now time.Time) domain.Source {
	now = now.UTC().Truncate(time.Microsecond)
	return domain.Source{
		ID: domain.NewID(), TenantID: tenantID, Kind: domain.SourceManual, Name: domain.ManualSourceName,
		Config: json.RawMessage(`{}`), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

// NewSignal validates a capture into the signal row, its body (nil when
// there is no text) and its participants. src is the resolved source (the
// caller has already mapped in.SourceID — or its absence — to a row in the
// tenant); its RetentionDays fills RetentionUntil when the input leaves it
// unset. actorID becomes CapturedBy ("" → nil). The caller still checks
// ProjectHint exists in the tenant.
func NewSignal(tenantID, actorID string, src domain.Source, in domain.CreateSignalInput, now time.Time) (domain.Signal, *domain.SignalBody, error) {
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = domain.SignalText
	}
	if !domain.ValidSignalKind(kind) {
		return domain.Signal{}, nil, domain.Invalid("kind", "must be one of meeting, email, note, doc, chat, link, text")
	}
	if len(in.Text) > domain.MaxSignalTextBytes {
		return domain.Signal{}, nil, domain.Invalid("text", "is too large (200 KB max)")
	}
	text := strings.TrimSpace(in.Text)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = firstLine(text, 120)
	}
	if title == "" {
		return domain.Signal{}, nil, domain.Invalid("title", "is required when there is no text")
	}
	if len(title) > MaxTitleLen {
		return domain.Signal{}, nil, domain.Invalid("title", "is too long")
	}
	now = now.UTC().Truncate(time.Microsecond)
	id := domain.NewID()
	occurred := now
	if in.OccurredAt != nil {
		occurred = in.OccurredAt.UTC().Truncate(time.Microsecond)
	}
	externalID := id
	if in.ExternalID != nil && strings.TrimSpace(*in.ExternalID) != "" {
		externalID = strings.TrimSpace(*in.ExternalID)
	}
	if len(externalID) > domain.MaxSignalRefLen {
		return domain.Signal{}, nil, domain.Invalid("externalId", "is too long")
	}
	bodyRef := strings.TrimSpace(derefString(in.BodyRef))
	if len(bodyRef) > domain.MaxSignalRefLen {
		return domain.Signal{}, nil, domain.Invalid("bodyRef", "is too long")
	}

	extracted, err := jsonObject(in.Extracted, "extracted", domain.MaxSignalExtractedLen)
	if err != nil {
		return domain.Signal{}, nil, err
	}
	if len(in.Links) > 0 {
		var m map[string]any
		_ = json.Unmarshal(extracted, &m) // validated above
		m["links"] = in.Links
		if extracted, err = json.Marshal(m); err != nil {
			return domain.Signal{}, nil, err
		}
		if len(extracted) > domain.MaxSignalExtractedLen {
			return domain.Signal{}, nil, domain.Invalid("links", "too many links")
		}
	}

	if len(in.Participants) > domain.MaxSignalParticipants {
		return domain.Signal{}, nil, domain.Invalid("participants", "too many participants")
	}
	participants := make([]domain.Participant, 0, len(in.Participants))
	for i, p := range in.Participants {
		p.Idx = i
		p.Name = strings.TrimSpace(p.Name)
		if len(p.Name) > MaxTitleLen {
			return domain.Signal{}, nil, domain.Invalid("participants", "a participant name is too long")
		}
		if p.Email != nil {
			e := strings.TrimSpace(strings.ToLower(*p.Email))
			if len(e) > domain.MaxParticipantEmailLen {
				return domain.Signal{}, nil, domain.Invalid("participants", "a participant email is too long")
			}
			if e == "" {
				p.Email = nil
			} else {
				p.Email = &e
			}
		}
		if p.Name == "" && p.Email == nil {
			return domain.Signal{}, nil, domain.Invalid("participants", "each participant needs a name or an email")
		}
		if p.Role == "" {
			p.Role = domain.RoleAttendee
		}
		if !domain.ValidParticipantRole(p.Role) {
			return domain.Signal{}, nil, domain.Invalid("participants", "role must be one of from, to, cc, attendee, speaker")
		}
		participants = append(participants, p)
	}

	s := domain.Signal{
		ID: id, TenantID: tenantID, SourceID: src.ID, ExternalID: externalID, Kind: kind, Title: title,
		Excerpt: truncateRunes(text, domain.MaxSignalExcerptLen), BodyRef: bodyRef,
		OccurredAt: occurred, Stage: domain.SignalInbox, Extracted: extracted, ProjectHint: in.ProjectHint,
		RetentionUntil: in.RetentionUntil, Participants: participants,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if actorID != "" {
		s.CapturedBy = &actorID
	}
	if s.RetentionUntil == nil && src.RetentionDays != nil {
		until := occurred.Add(time.Duration(*src.RetentionDays) * 24 * time.Hour)
		s.RetentionUntil = &until
	} else if s.RetentionUntil != nil {
		until := s.RetentionUntil.UTC().Truncate(time.Microsecond)
		s.RetentionUntil = &until
	}
	s.Normalize()

	var body *domain.SignalBody
	if text != "" {
		body = &domain.SignalBody{TenantID: tenantID, SignalID: id, Body: text, FetchedAt: now}
	}
	return s, body, nil
}

// NewOrigin builds the join row for AttachSignalToTask, snapshotting the
// signal's provenance fields.
func NewOrigin(taskID string, sig domain.Signal, now time.Time) domain.Origin {
	return domain.Origin{
		TenantID: sig.TenantID, TaskID: taskID, SignalID: sig.ID,
		Snapshot:  domain.OriginSnapshot{Title: sig.Title, Kind: sig.Kind, OccurredAt: sig.OccurredAt, BodyRef: sig.BodyRef},
		CreatedAt: now.UTC().Truncate(time.Microsecond),
	}
}

// NewOutput validates a CreateOutputInput into a row. The caller checks the
// task exists in the tenant. actorID becomes CreatedBy ("" → nil).
func NewOutput(tenantID, actorID, taskID string, in domain.CreateOutputInput, now time.Time) (domain.Output, error) {
	kind := strings.TrimSpace(in.Kind)
	if !domain.ValidOutputKind(kind) {
		return domain.Output{}, domain.Invalid("kind", "must be one of link, doc, pr, email, decision, file, note")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.Output{}, domain.Invalid("title", "is required")
	}
	if len(title) > MaxTitleLen {
		return domain.Output{}, domain.Invalid("title", "is too long")
	}
	detail := derefString(in.Detail)
	if len(detail) > domain.MaxOutputDetailLen {
		return domain.Output{}, domain.Invalid("detail", "is too long")
	}
	url := strings.TrimSpace(derefString(in.URL))
	if len(url) > domain.MaxOutputURLLen {
		return domain.Output{}, domain.Invalid("url", "is too long")
	}
	o := domain.Output{
		ID: domain.NewID(), TenantID: tenantID, TaskID: taskID, Kind: kind, Title: title,
		URL: url, Detail: detail,
		CreatedAt: now.UTC().Truncate(time.Microsecond),
	}
	if actorID != "" {
		o.CreatedBy = &actorID
	}
	return o, nil
}

// jsonObject validates an optional raw JSON value as a bounded object,
// defaulting to {} when absent.
func jsonObject(raw json.RawMessage, field string, max int) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	if len(raw) > max {
		return nil, domain.Invalid(field, "is too large")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, domain.Invalid(field, "must be a JSON object")
	}
	return raw, nil
}

// truncateRunes keeps the first n characters (not bytes) of s.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// firstLine is the title fallback: the first non-empty line, at most n
// characters.
func firstLine(text string, n int) string {
	for _, line := range strings.Split(text, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return truncateRunes(l, n)
		}
	}
	return ""
}
