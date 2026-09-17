package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// runSignalParity is the signal surface's walk (0014/0015): capture with a
// body and participants → get → body → paged list newest-first by occurredAt
// → count → disposition patch with a version conflict and the immutability
// rule → attach/detach with the surviving snapshot → outputs → cascades from
// task/project/source deletes. It is separate from the generic walk because
// signals have no free-form update (only the disposition patch) and several
// tables move together; it does its own event accounting, and demands that
// no event ever carries an excerpt, a body or a participant.
func runSignalParity(t *testing.T, ctx context.Context, st store.Store, acct domain.Account) {
	t.Helper()
	// Fixtures the walk needs (the manual source, a project, a task) are made
	// before the tap subscribes so their events are not counted.
	manual, err := st.GetOrCreateManualSource(ctx, acct.TenantID)
	if err != nil {
		t.Fatalf("GetOrCreateManualSource: %v", err)
	}
	if again, err := st.GetOrCreateManualSource(ctx, acct.TenantID); err != nil || again.ID != manual.ID {
		t.Fatalf("GetOrCreateManualSource must be idempotent: %v / %s vs %s", err, again.ID, manual.ID)
	}
	if manual.Kind != domain.SourceManual || string(manual.Config) != "{}" {
		t.Errorf("manual source: want kind=manual config={}, got kind=%s config=%s", manual.Kind, manual.Config)
	}
	proj, err := st.CreateProject(ctx, acct.TenantID, acct.UserID, domain.CreateProjectInput{Name: "signals fixture"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task, err := st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "derived work item"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	sub, unsub := st.Subscribe(acct.TenantID)
	defer unsub()
	signals := eventTap{t: t, sub: sub, entityType: domain.EntitySignal, versionOf: signalEventVersion}
	origins := eventTap{t: t, sub: sub, entityType: domain.EntityOrigin, versionOf: func(domain.Event) (int, bool) { return 0, false }}
	outputs := eventTap{t: t, sub: sub, entityType: domain.EntityOutput, versionOf: func(ev domain.Event) (int, bool) { return 1, len(ev.Entity) > 0 }}
	sources := eventTap{t: t, sub: sub, entityType: domain.EntitySource, versionOf: func(ev domain.Event) (int, bool) {
		var s domain.Source
		if len(ev.Entity) == 0 || json.Unmarshal(ev.Entity, &s) != nil {
			return 0, false
		}
		return s.Version, true
	}}
	signals.drain()
	defer func() {
		_ = st.DeleteTask(ctx, acct.TenantID, acct.UserID, task.ID)
		_ = st.DeleteProject(ctx, acct.TenantID, acct.UserID, proj.ID)
		signals.drain()
	}()

	// --- empty tenant: [] with no cursor, count 0 ---
	if res, err := st.ListSignals(ctx, acct.TenantID, store.SignalFilter{}, store.Page{}); err != nil {
		t.Fatalf("ListSignals(empty): %v", err)
	} else if res.Items == nil || len(res.Items) != 0 || res.NextCursor != "" {
		t.Errorf("ListSignals(empty): want [] and no cursor, got %v next=%q", res.Items, res.NextCursor)
	}

	// --- rejected captures: nothing stored, no event ---
	for _, c := range []struct {
		name  string
		in    domain.CreateSignalInput
		field string
	}{
		{"nothing to capture", domain.CreateSignalInput{Kind: domain.SignalText}, "title"},
		{"bad kind", domain.CreateSignalInput{Kind: "rumour", Title: "x"}, "kind"},
		{"oversize text", domain.CreateSignalInput{Kind: domain.SignalText, Text: strings.Repeat("x", domain.MaxSignalTextBytes+1)}, "text"},
		{"bad role", domain.CreateSignalInput{Kind: domain.SignalEmail, Title: "x", Participants: []domain.Participant{{Name: "a", Role: "bcc"}}}, "participants"},
		{"nameless participant", domain.CreateSignalInput{Kind: domain.SignalEmail, Title: "x", Participants: []domain.Participant{{Role: domain.RoleTo}}}, "participants"},
		{"dangling projectHint", domain.CreateSignalInput{Kind: domain.SignalText, Title: "x", ProjectHint: ptrOf(domain.NewID())}, "projectHint"},
		{"dangling sourceId", domain.CreateSignalInput{Kind: domain.SignalText, Title: "x", SourceID: ptrOf(domain.NewID())}, "sourceId"},
		{"extracted not an object", domain.CreateSignalInput{Kind: domain.SignalText, Title: "x", Extracted: json.RawMessage(`[1]`)}, "extracted"},
	} {
		_, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, c.in)
		assertInvalid(t, "CreateSignal("+c.name+")", err, c.field)
	}

	// --- capture: five signals, occurredAt ascending so newest-first is the
	//     reverse of creation order; the first carries a long body, participants
	//     and links ---
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	longText := "Kickoff notes\n" + strings.Repeat("The quick brown fox jumps over the lazy dog. ", 80) // > 2000 chars
	email := "ada@example.com"
	created := make([]domain.Signal, parityN)
	for i := range created {
		in := domain.CreateSignalInput{Kind: domain.SignalNote, Title: fmt.Sprintf("signal %d", i), OccurredAt: ptrTime(base.Add(time.Duration(i) * time.Hour))}
		if i == 0 {
			in = domain.CreateSignalInput{
				Kind: domain.SignalMeeting, Text: longText, OccurredAt: ptrTime(base),
				Participants: []domain.Participant{{Name: "Ada", Email: ptrOf(" Ada@Example.com "), Role: domain.RoleSpeaker}, {Name: "Bob"}},
				Links:        []domain.Link{{Label: "deck", URL: "https://example.test/deck"}},
				ProjectHint:  &proj.ID,
			}
		}
		sig, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, in)
		if err != nil {
			t.Fatalf("CreateSignal #%d: %v", i, err)
		}
		if sig.Version != 1 || sig.Stage != domain.SignalInbox || sig.DispositionAt != nil {
			t.Errorf("CreateSignal #%d: want version 1 / inbox / no dispositionAt, got %d / %s / %v", i, sig.Version, sig.Stage, sig.DispositionAt)
		}
		if sig.SourceID != manual.ID || sig.ExternalID != sig.ID {
			t.Errorf("CreateSignal #%d: manual capture must use the manual source and its own id as externalId (source=%s external=%s)", i, sig.SourceID, sig.ExternalID)
		}
		if sig.CapturedBy == nil || *sig.CapturedBy != acct.UserID {
			t.Errorf("CreateSignal #%d: want capturedBy=%s, got %v", i, acct.UserID, sig.CapturedBy)
		}
		if !sig.UpdatedAt.Equal(sig.CreatedAt) || sig.CreatedAt.IsZero() {
			t.Errorf("CreateSignal #%d: want updatedAt == createdAt, got %v / %v", i, sig.CreatedAt, sig.UpdatedAt)
		}
		signals.expect(domain.EventSignalCreated, sig.ID, 1)
		created[i] = *sig
	}
	first := created[0]
	if first.Title != "Kickoff notes" {
		t.Errorf("title must fall back to the first line of the text, got %q", first.Title)
	}
	if n := len([]rune(first.Excerpt)); n != domain.MaxSignalExcerptLen {
		t.Errorf("excerpt must be the first %d characters, got %d", domain.MaxSignalExcerptLen, n)
	}
	if len(first.Participants) != 2 || first.Participants[0].Idx != 0 || first.Participants[1].Idx != 1 ||
		first.Participants[0].Email == nil || *first.Participants[0].Email != email || first.Participants[1].Role != domain.RoleAttendee {
		t.Errorf("participants did not normalise (idx, lowercase trimmed email, default role): %+v", first.Participants)
	}
	var extracted map[string]json.RawMessage
	if json.Unmarshal(first.Extracted, &extracted) != nil || len(extracted["links"]) == 0 {
		t.Errorf("links must seed extracted.links, got %s", first.Extracted)
	}
	if first.ProjectHint == nil || *first.ProjectHint != proj.ID {
		t.Errorf("projectHint did not land: %v", first.ProjectHint)
	}
	if _, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, domain.CreateSignalInput{Kind: domain.SignalText, Title: "dupe", ExternalID: &first.ExternalID}); err == nil {
		t.Error("CreateSignal(duplicate externalId on the same source): want ValidationError, got nil")
	} else {
		assertInvalid(t, "CreateSignal(duplicate externalId)", err, "externalId")
	}

	// --- get: the row with participants, never the body; body on its own ---
	got, err := st.GetSignal(ctx, acct.TenantID, first.ID)
	if err != nil {
		t.Fatalf("GetSignal: %v", err)
	}
	if got.Version != 1 || !got.CreatedAt.Equal(first.CreatedAt) || !got.OccurredAt.Equal(first.OccurredAt) || got.Excerpt != first.Excerpt || len(got.Participants) != 2 {
		t.Errorf("GetSignal: differs from what create returned:\n create: %+v\n    get: %+v", first, *got)
	}
	assertArrays(t, "GetSignal", signalRecord(*got), []string{"participants"})
	if _, err := st.GetSignal(ctx, acct.TenantID, domain.NewID()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetSignal(unknown): want ErrNotFound, got %v", err)
	}
	body, err := st.GetSignalBody(ctx, acct.TenantID, first.ID)
	if err != nil {
		t.Fatalf("GetSignalBody: %v", err)
	}
	if body.Body != strings.TrimSpace(longText) || body.SignalID != first.ID {
		t.Errorf("GetSignalBody: want the full text back, got %d chars", len(body.Body))
	}
	if body, err := st.GetSignalBody(ctx, acct.TenantID, created[1].ID); err != nil || body.Body != "" {
		t.Errorf("GetSignalBody(no text): want empty body and no error, got %q / %v", body, err)
	}
	if _, err := st.GetSignalBody(ctx, acct.TenantID, domain.NewID()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetSignalBody(unknown): want ErrNotFound, got %v", err)
	}

	// --- paged list: 2+2+1 newest first by occurredAt; count agrees ---
	var walked []string
	cursor := ""
	for page := 1; ; page++ {
		res, err := st.ListSignals(ctx, acct.TenantID, store.SignalFilter{}, store.Page{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListSignals page %d: %v", page, err)
		}
		for _, s := range res.Items {
			walked = append(walked, s.ID)
			assertArrays(t, "ListSignals", signalRecord(s), []string{"participants"})
		}
		if page < 3 && (len(res.Items) != 2 || res.NextCursor == "") {
			t.Fatalf("ListSignals page %d: want 2 items and a cursor, got %d next=%q", page, len(res.Items), res.NextCursor)
		}
		if page == 3 {
			if len(res.Items) != 1 || res.NextCursor != "" {
				t.Fatalf("ListSignals page 3: want 1 item and no cursor, got %d next=%q", len(res.Items), res.NextCursor)
			}
			break
		}
		cursor = res.NextCursor
	}
	want := make([]string, 0, parityN)
	for i := parityN - 1; i >= 0; i-- {
		want = append(want, created[i].ID)
	}
	assertIDs(t, "ListSignals walk", walked, want, true)
	_, err = st.ListSignals(ctx, acct.TenantID, store.SignalFilter{}, store.Page{Cursor: "not a cursor"})
	assertInvalid(t, "ListSignals(bad cursor)", err, "cursor")
	inbox := domain.SignalInbox
	if n, err := st.CountSignals(ctx, acct.TenantID, store.SignalFilter{Stage: &inbox}); err != nil || n != parityN {
		t.Errorf("CountSignals(inbox): want %d, got %d (%v)", parityN, n, err)
	}
	after := base.Add(2*time.Hour + time.Minute)
	if res, err := st.ListSignals(ctx, acct.TenantID, store.SignalFilter{OccurredAfter: &after, ProjectHint: &proj.ID}, store.Page{}); err != nil || len(res.Items) != 0 {
		t.Errorf("ListSignals(occurredAfter+projectHint): want 0, got %d (%v)", len(res.Items), err)
	}
	if res, err := st.ListSignals(ctx, acct.TenantID, store.SignalFilter{ProjectHint: &proj.ID, SourceID: &manual.ID}, store.Page{}); err != nil || len(res.Items) != 1 || res.Items[0].ID != first.ID {
		t.Errorf("ListSignals(projectHint+sourceId): want exactly [%s], got %d item(s) (%v)", first.ID, len(res.Items), err)
	}

	// --- disposition: the only write; version tokens; immutability ---
	_, err = st.UpdateSignalDisposition(ctx, acct.TenantID, acct.UserID, first.ID, map[string]any{"stage": "snoozed"}, nil)
	assertInvalid(t, "patch(snoozed without a time)", err, "snoozedUntil")
	for _, inv := range []invalidPatch{
		{"bad stage", map[string]any{"stage": "archived"}, "stage"},
		{"title (immutable)", map[string]any{"title": "rewritten"}, "title"},
		{"excerpt (immutable)", map[string]any{"excerpt": "rewritten"}, "excerpt"},
		{"occurredAt (immutable)", map[string]any{"occurredAt": base.Format(time.RFC3339)}, "occurredAt"},
		{"participants (immutable)", map[string]any{"participants": []any{}}, "participants"},
		{"bad snoozedUntil", map[string]any{"snoozedUntil": "tomorrow"}, "snoozedUntil"},
		{"dangling projectHint", map[string]any{"projectHint": domain.NewID()}, "projectHint"},
		{"extracted not an object", map[string]any{"extracted": "facts"}, "extracted"},
	} {
		_, err := st.UpdateSignalDisposition(ctx, acct.TenantID, acct.UserID, first.ID, inv.patch, nil)
		assertInvalid(t, "patch("+inv.name+")", err, inv.field)
		if again, err := st.GetSignal(ctx, acct.TenantID, first.ID); err != nil || again.Version != 1 {
			t.Errorf("invalid patch %q mutated the row: version %d (%v)", inv.name, again.Version, err)
		}
	}
	wake := base.Add(72 * time.Hour)
	snoozed, err := st.UpdateSignalDisposition(ctx, acct.TenantID, acct.UserID, first.ID,
		map[string]any{"stage": "snoozed", "snoozedUntil": wake.Format(time.RFC3339), "extracted": map[string]any{"deadline": "2026-09-30"}}, nil)
	if err != nil {
		t.Fatalf("patch(snooze): %v", err)
	}
	if snoozed.Version != 2 || snoozed.Stage != domain.SignalSnoozed || snoozed.SnoozedUntil == nil || !snoozed.SnoozedUntil.Equal(wake) ||
		snoozed.DispositionAt == nil || !snoozed.UpdatedAt.After(first.UpdatedAt) || !strings.Contains(string(snoozed.Extracted), "deadline") {
		t.Errorf("patch(snooze): want v2 snoozed until %v with dispositionAt and extracted, got %+v", wake, *snoozed)
	}
	if snoozed.Title != first.Title || snoozed.Excerpt != first.Excerpt || len(snoozed.Participants) != 2 {
		t.Error("patch(snooze) must leave the captured content untouched")
	}
	signals.expect(domain.EventSignalUpdated, first.ID, 2)
	stale := 1
	if _, err := st.UpdateSignalDisposition(ctx, acct.TenantID, acct.UserID, first.ID, map[string]any{"stage": "inbox"}, &stale); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("patch(expectedVersion=1 on v2): want ErrConflict, got %v", err)
	}
	current := 2
	back, err := st.UpdateSignalDisposition(ctx, acct.TenantID, acct.UserID, first.ID, map[string]any{"stage": "inbox"}, &current)
	if err != nil {
		t.Fatalf("patch(back to inbox): %v", err)
	}
	if back.Version != 3 || back.Stage != domain.SignalInbox || back.SnoozedUntil != nil || back.DispositionAt != nil {
		t.Errorf("patch(back to inbox): want v3 inbox with snoozedUntil and dispositionAt cleared, got %+v", *back)
	}
	signals.expect(domain.EventSignalUpdated, first.ID, 3)
	if got, err := st.GetSignal(ctx, acct.TenantID, first.ID); err != nil || got.Version != 3 || !got.UpdatedAt.Equal(back.UpdatedAt) {
		t.Errorf("get after patch: want v3 / updatedAt %v, got %+v (%v)", back.UpdatedAt, got, err)
	}
	if _, err := st.UpdateSignalDisposition(ctx, acct.TenantID, acct.UserID, domain.NewID(), map[string]any{"stage": "dismissed"}, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("patch(unknown): want ErrNotFound, got %v", err)
	}

	// --- attach: origin row + snapshot, inbox → attached; idempotent ---
	if _, err := st.AttachSignalToTask(ctx, acct.TenantID, acct.UserID, task.ID, domain.NewID()); err == nil {
		t.Error("Attach(unknown signal): want ValidationError, got nil")
	} else {
		assertInvalid(t, "Attach(unknown signal)", err, "signalId")
	}
	if _, err := st.AttachSignalToTask(ctx, acct.TenantID, acct.UserID, domain.NewID(), first.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Attach(unknown task): want ErrNotFound, got %v", err)
	}
	origin, err := st.AttachSignalToTask(ctx, acct.TenantID, acct.UserID, task.ID, first.ID)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if origin.TaskID != task.ID || origin.SignalID != first.ID || origin.Snapshot.Title != first.Title || origin.Snapshot.Kind != first.Kind || !origin.Snapshot.OccurredAt.Equal(first.OccurredAt) {
		t.Errorf("Attach: snapshot must copy the signal's provenance, got %+v", *origin)
	}
	signals.expect(domain.EventSignalUpdated, first.ID, 4) // inbox → attached
	origins.expect(domain.EventOriginAttached, task.ID, 0)
	if got, err := st.GetSignal(ctx, acct.TenantID, first.ID); err != nil || got.Stage != domain.SignalAttached || got.Version != 4 || got.DispositionAt == nil {
		t.Errorf("Attach must dispose an inbox signal as attached (v4), got %+v (%v)", got, err)
	}
	if again, err := st.AttachSignalToTask(ctx, acct.TenantID, acct.UserID, task.ID, first.ID); err != nil || !again.CreatedAt.Equal(origin.CreatedAt) {
		t.Errorf("Attach twice: want the existing origin back, got %+v (%v)", again, err)
	}
	signals.expectNone() // idempotent attach emits nothing
	list, err := st.ListTaskOrigins(ctx, acct.TenantID, task.ID)
	if err != nil {
		t.Fatalf("ListTaskOrigins: %v", err)
	}
	if len(list) != 1 || list[0].SignalID != first.ID || list[0].Snapshot.Title != first.Title {
		t.Errorf("ListTaskOrigins: want [%s] with snapshot, got %+v", first.ID, list)
	}
	if _, err := st.ListTaskOrigins(ctx, acct.TenantID, domain.NewID()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ListTaskOrigins(unknown task): want ErrNotFound, got %v", err)
	}
	if err := st.DetachSignalFromTask(ctx, acct.TenantID, acct.UserID, task.ID, first.ID); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	origins.expect(domain.EventOriginDetached, task.ID, 0)
	if err := st.DetachSignalFromTask(ctx, acct.TenantID, acct.UserID, task.ID, first.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Detach twice: want ErrNotFound, got %v", err)
	}
	if list, err := st.ListTaskOrigins(ctx, acct.TenantID, task.ID); err != nil || len(list) != 0 {
		t.Errorf("ListTaskOrigins after detach: want [], got %+v (%v)", list, err)
	}
	// re-attach (already 'attached', so only the origin event), then delete
	// the signal: the origin survives with its snapshot
	if _, err := st.AttachSignalToTask(ctx, acct.TenantID, acct.UserID, task.ID, first.ID); err != nil {
		t.Fatalf("re-Attach: %v", err)
	}
	origins.expect(domain.EventOriginAttached, task.ID, 0)
	if err := st.DeleteSignal(ctx, acct.TenantID, acct.UserID, first.ID); err != nil {
		t.Fatalf("DeleteSignal: %v", err)
	}
	signals.expect(domain.EventSignalDeleted, first.ID, 0)
	if _, err := st.GetSignal(ctx, acct.TenantID, first.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetSignal after delete: want ErrNotFound, got %v", err)
	}
	if _, err := st.GetSignalBody(ctx, acct.TenantID, first.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetSignalBody after delete: want ErrNotFound (body goes with the signal), got %v", err)
	}
	if err := st.DeleteSignal(ctx, acct.TenantID, acct.UserID, first.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second DeleteSignal: want ErrNotFound, got %v", err)
	}
	if list, err := st.ListTaskOrigins(ctx, acct.TenantID, task.ID); err != nil || len(list) != 1 || list[0].Snapshot.Title != first.Title {
		t.Errorf("ListTaskOrigins after the signal is purged: the snapshot must survive, got %+v (%v)", list, err)
	}

	// --- outputs: create / list / delete, cascade with the task ---
	_, err = st.CreateOutput(ctx, acct.TenantID, acct.UserID, task.ID, domain.CreateOutputInput{Kind: "tweet", Title: "x"})
	assertInvalid(t, "CreateOutput(bad kind)", err, "kind")
	_, err = st.CreateOutput(ctx, acct.TenantID, acct.UserID, task.ID, domain.CreateOutputInput{Kind: domain.OutputLink, Title: " "})
	assertInvalid(t, "CreateOutput(empty title)", err, "title")
	if _, err := st.CreateOutput(ctx, acct.TenantID, acct.UserID, domain.NewID(), domain.CreateOutputInput{Kind: domain.OutputLink, Title: "x"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("CreateOutput(unknown task): want ErrNotFound, got %v", err)
	}
	var outs []domain.Output
	for i, in := range []domain.CreateOutputInput{
		{Kind: domain.OutputPR, Title: "PR #12", URL: ptrOf("https://example.test/pr/12")},
		{Kind: domain.OutputDecision, Title: "Go with option B", Detail: ptrOf("cheaper, ships sooner")},
	} {
		o, err := st.CreateOutput(ctx, acct.TenantID, acct.UserID, task.ID, in)
		if err != nil {
			t.Fatalf("CreateOutput #%d: %v", i, err)
		}
		if o.TaskID != task.ID || o.CreatedBy == nil || *o.CreatedBy != acct.UserID || o.CreatedAt.IsZero() {
			t.Errorf("CreateOutput #%d: want taskId/createdBy/createdAt, got %+v", i, *o)
		}
		outputs.expect(domain.EventOutputCreated, o.ID, 1)
		outs = append(outs, *o)
	}
	if list, err := st.ListOutputs(ctx, acct.TenantID, task.ID); err != nil || len(list) != 2 || list[0].ID != outs[0].ID || list[1].Detail != "cheaper, ships sooner" {
		t.Errorf("ListOutputs: want the two outputs oldest first, got %+v (%v)", list, err)
	}
	if _, err := st.ListOutputs(ctx, acct.TenantID, domain.NewID()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ListOutputs(unknown task): want ErrNotFound, got %v", err)
	}
	if err := st.DeleteOutput(ctx, acct.TenantID, acct.UserID, task.ID, outs[0].ID); err != nil {
		t.Fatalf("DeleteOutput: %v", err)
	}
	outputs.expect(domain.EventOutputDeleted, outs[0].ID, 0)
	if err := st.DeleteOutput(ctx, acct.TenantID, acct.UserID, task.ID, outs[0].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second DeleteOutput: want ErrNotFound, got %v", err)
	}
	if err := st.DeleteOutput(ctx, acct.TenantID, acct.UserID, domain.NewID(), outs[1].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("DeleteOutput(wrong task): want ErrNotFound, got %v", err)
	}
	// the work item goes: its remaining output and origin go with it, silently
	if err := st.DeleteTask(ctx, acct.TenantID, acct.UserID, task.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	signals.drain() // the task.deleted event
	if _, err := st.ListOutputs(ctx, acct.TenantID, task.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ListOutputs after task delete: want ErrNotFound, got %v", err)
	}

	// --- project delete: project_hint is SET NULL, version untouched, no event ---
	hinted, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, domain.CreateSignalInput{Kind: domain.SignalLink, Title: "hinted", ProjectHint: &proj.ID})
	if err != nil {
		t.Fatalf("CreateSignal(hinted): %v", err)
	}
	signals.expect(domain.EventSignalCreated, hinted.ID, 1)
	if err := st.DeleteProject(ctx, acct.TenantID, acct.UserID, proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	signals.drain() // project.deleted
	if got, err := st.GetSignal(ctx, acct.TenantID, hinted.ID); err != nil || got.ProjectHint != nil || got.Version != 1 {
		t.Errorf("deleting the hinted project must SET NULL projectHint without bumping version, got %+v (%v)", got, err)
	}

	// --- sources: a connector source, its retention flows onto captures, and
	//     deleting it takes its signals (silently) ---
	_, err = st.CreateSource(ctx, acct.TenantID, acct.UserID, domain.CreateSourceInput{Kind: domain.SourceManual, Name: "another paste box"})
	assertInvalid(t, "CreateSource(manual)", err, "kind")
	_, err = st.CreateSource(ctx, acct.TenantID, acct.UserID, domain.CreateSourceInput{Kind: "carrier pigeon", Name: "x"})
	assertInvalid(t, "CreateSource(bad kind)", err, "kind")
	_, err = st.CreateSource(ctx, acct.TenantID, acct.UserID, domain.CreateSourceInput{Kind: domain.SourceEmail, Name: "x", OwnerID: ptrOf(domain.NewID())})
	assertInvalid(t, "CreateSource(dangling ownerId)", err, "ownerId")
	days := 30
	src, err := st.CreateSource(ctx, acct.TenantID, acct.UserID, domain.CreateSourceInput{
		Kind: domain.SourceEmail, Name: "Work mail", OwnerID: &acct.UserID, RetentionDays: &days, Config: json.RawMessage(`{"folder":"INBOX"}`),
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if src.Version != 1 || src.RetentionDays == nil || *src.RetentionDays != 30 || string(src.Config) != `{"folder":"INBOX"}` {
		t.Errorf("CreateSource: %+v", *src)
	}
	sources.expect(domain.EventSourceCreated, src.ID, 1)
	if all, err := st.ListSources(ctx, acct.TenantID); err != nil || len(all) != 2 || all[0].ID != manual.ID || all[1].ID != src.ID {
		t.Errorf("ListSources: want [manual, %s], got %+v (%v)", src.ID, all, err)
	}
	_, err = st.UpdateSource(ctx, acct.TenantID, acct.UserID, src.ID, map[string]any{"kind": "chat"}, nil)
	assertInvalid(t, "UpdateSource(kind)", err, "kind")
	_, err = st.UpdateSource(ctx, acct.TenantID, acct.UserID, src.ID, map[string]any{"retentionDays": 0.0}, nil)
	assertInvalid(t, "UpdateSource(zero retention)", err, "retentionDays")
	upd, err := st.UpdateSource(ctx, acct.TenantID, acct.UserID, src.ID, map[string]any{"name": "Work mail (IMAP)", "disabled": true, "consentAt": base.Format(time.RFC3339)}, nil)
	if err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if upd.Version != 2 || upd.Name != "Work mail (IMAP)" || upd.DisabledAt == nil || upd.ConsentAt == nil || !upd.UpdatedAt.After(src.UpdatedAt) {
		t.Errorf("UpdateSource: %+v", *upd)
	}
	sources.expect(domain.EventSourceUpdated, src.ID, 2)
	if _, err := st.UpdateSource(ctx, acct.TenantID, acct.UserID, src.ID, map[string]any{"name": "x"}, &stale); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("UpdateSource(stale token): want ErrConflict, got %v", err)
	}
	mail, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, domain.CreateSignalInput{
		SourceID: &src.ID, ExternalID: ptrOf("<msg-1@example.com>"), Kind: domain.SignalEmail, Title: "Re: SOW", Text: "please sign", OccurredAt: ptrTime(base),
	})
	if err != nil {
		t.Fatalf("CreateSignal(from source): %v", err)
	}
	if mail.SourceID != src.ID || mail.ExternalID != "<msg-1@example.com>" || mail.RetentionUntil == nil || !mail.RetentionUntil.Equal(base.Add(30*24*time.Hour)) {
		t.Errorf("CreateSignal(from source): want the source's id/externalId and retentionUntil = occurredAt + 30d, got %+v", *mail)
	}
	signals.expect(domain.EventSignalCreated, mail.ID, 1)
	// the manual source is the one a user never made and cannot unmake
	if _, err := st.DeleteSource(ctx, acct.TenantID, acct.UserID, manual.ID); err == nil {
		t.Errorf("DeleteSource(manual): want a refusal, deleted the paste box's home instead")
	} else {
		assertInvalid(t, "DeleteSource(manual)", err, "id")
	}
	n, err := st.DeleteSource(ctx, acct.TenantID, acct.UserID, src.ID)
	if err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	if n != 1 {
		t.Errorf("DeleteSource: want the 1 cascaded signal counted, got %d", n)
	}
	sources.expect(domain.EventSourceDeleted, src.ID, 0)
	if _, err := st.GetSignal(ctx, acct.TenantID, mail.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleting a source must take its signals, got %v", err)
	}
	if _, err := st.DeleteSource(ctx, acct.TenantID, acct.UserID, src.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second DeleteSource: want ErrNotFound, got %v", err)
	}

	// --- tidy: one event each, then nothing else was emitted ---
	for _, sig := range append(created[1:], *hinted) {
		if err := st.DeleteSignal(ctx, acct.TenantID, acct.UserID, sig.ID); err != nil {
			t.Fatalf("DeleteSignal %s: %v", sig.ID, err)
		}
		signals.expect(domain.EventSignalDeleted, sig.ID, 0)
	}
	signals.expectNone()
}

// SignalPruner is the retention sweep every adapter must run: a source's
// retentionDays (copied onto Signal.RetentionUntil at capture) is a promise
// the server keeps by deleting expired signals — body and participants with
// them — on a ticker. Both adapters expose it so the suite can call it
// directly instead of waiting an hour.
type SignalPruner interface {
	PruneSignals(ctx context.Context, now time.Time) (int64, error)
}

// runSignalRetentionParity proves the sweep: an expired signal and its body
// vanish, an origin that pointed at it keeps its snapshot, and signals with
// no retention or a future one are untouched. It does no event accounting —
// a retention delete is housekeeping and emits nothing.
func runSignalRetentionParity(t *testing.T, ctx context.Context, st store.Store, acct domain.Account) {
	t.Helper()
	pruner, ok := st.(SignalPruner)
	if !ok {
		t.Fatalf("%T has no PruneSignals — a source's retentionDays would be a promise this adapter never keeps", st)
	}
	task, err := st.CreateTask(ctx, acct.TenantID, acct.UserID, domain.CreateTaskInput{Title: "derived from an expiring signal"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	defer st.DeleteTask(ctx, acct.TenantID, acct.UserID, task.ID)

	now := time.Now().UTC()
	expired, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, domain.CreateSignalInput{
		Kind: domain.SignalEmail, Title: "expired thread", Text: "the whole thread", RetentionUntil: ptrTime(now.Add(-time.Hour)),
		Participants: []domain.Participant{{Name: "Someone", Email: ptrOf("someone@example.com"), Role: domain.RoleFrom}},
	})
	if err != nil {
		t.Fatalf("CreateSignal(expired): %v", err)
	}
	kept, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, domain.CreateSignalInput{Kind: domain.SignalNote, Title: "kept forever", Text: "no retention"})
	if err != nil {
		t.Fatalf("CreateSignal(kept): %v", err)
	}
	defer st.DeleteSignal(ctx, acct.TenantID, acct.UserID, kept.ID)
	later, err := st.CreateSignal(ctx, acct.TenantID, acct.UserID, domain.CreateSignalInput{Kind: domain.SignalNote, Title: "not yet", RetentionUntil: ptrTime(now.Add(time.Hour))})
	if err != nil {
		t.Fatalf("CreateSignal(later): %v", err)
	}
	defer st.DeleteSignal(ctx, acct.TenantID, acct.UserID, later.ID)
	if _, err := st.AttachSignalToTask(ctx, acct.TenantID, acct.UserID, task.ID, expired.ID); err != nil {
		t.Fatalf("AttachSignalToTask: %v", err)
	}

	n, err := pruner.PruneSignals(ctx, now)
	if err != nil {
		t.Fatalf("PruneSignals: %v", err)
	}
	if n < 1 {
		t.Errorf("PruneSignals: want at least the expired signal counted, got %d", n)
	}
	if _, err := st.GetSignal(ctx, acct.TenantID, expired.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expired signal survived the sweep: %v", err)
	}
	if _, err := st.GetSignalBody(ctx, acct.TenantID, expired.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expired signal's body survived the sweep: %v", err)
	}
	if _, err := st.GetSignal(ctx, acct.TenantID, kept.ID); err != nil {
		t.Errorf("signal with no retention was swept: %v", err)
	}
	if _, err := st.GetSignal(ctx, acct.TenantID, later.ID); err != nil {
		t.Errorf("signal whose retention has not passed was swept: %v", err)
	}
	origins, err := st.ListTaskOrigins(ctx, acct.TenantID, task.ID)
	if err != nil {
		t.Fatalf("ListTaskOrigins: %v", err)
	}
	if len(origins) != 1 || origins[0].SignalID != expired.ID || origins[0].Snapshot.Title != "expired thread" {
		t.Errorf("origin must outlive its signal with its snapshot, got %+v", origins)
	}

	// …and the sweep says so: one row per tenant per sweep, carrying the count
	// and nothing about what it deleted.
	page, err := st.ListAudit(ctx, acct.TenantID, store.Page{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	swept := 0
	for _, e := range page.Items {
		if e.Kind != domain.AuditSignalPrune {
			continue
		}
		swept++
		if !strings.Contains(string(e.Detail), `"signals":`) || e.ActorID != nil {
			t.Errorf("signal.prune row = actor %v detail %s", e.ActorID, e.Detail)
		}
		if strings.Contains(string(e.Detail), "expired thread") || strings.Contains(string(e.Detail), expired.ID) {
			t.Errorf("signal.prune row names what it deleted: %s", e.Detail)
		}
	}
	if swept != 1 {
		t.Errorf("signal.prune audit rows = %d, want exactly one for the sweep", swept)
	}
}

// signalEventVersion reads SignalMeta off a signal.* event and, while it is
// there, enforces the metadata-only rule: no excerpt, body or participants.
func signalEventVersion(ev domain.Event) (int, bool) {
	if len(ev.Entity) == 0 {
		return 0, false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(ev.Entity, &raw) != nil {
		return 0, false
	}
	for _, k := range []string{"excerpt", "body", "participants", "extracted"} {
		if _, leaked := raw[k]; leaked {
			panic("signal event carries " + k + " — events are metadata only")
		}
	}
	var m domain.SignalMeta
	if json.Unmarshal(ev.Entity, &m) != nil {
		return 0, false
	}
	return m.Version, true
}

func signalRecord(s domain.Signal) parityRecord {
	b, _ := json.Marshal(s)
	return parityRecord{ID: s.ID, Version: s.Version, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, JSON: b}
}

func ptrTime(t time.Time) *time.Time { return &t }
