package domain

import (
	"strings"
	"testing"
	"time"
)

func TestDeriveLifecycleFromStatus(t *testing.T) {
	cases := []struct {
		status Status
		want   Stage
	}{
		{StatusBacklog, StageTodo},
		{StatusScheduled, StageTodo},
		{StatusFocus, StageDoing},
		{StatusDone, StageDone},
		{Status(""), StageTodo}, // unknown → the backlog/todo pair
	}
	for _, c := range cases {
		gotStatus, gotStage := DeriveLifecycle(c.status, "", false, true)
		if gotStage != c.want {
			t.Errorf("DeriveLifecycle(status=%q): want stage %q, got %q", c.status, c.want, gotStage)
		}
		wantStatus := c.status
		if !c.status.Valid() {
			wantStatus = StatusBacklog
		}
		if gotStatus != wantStatus {
			t.Errorf("DeriveLifecycle(status=%q): status must be kept (or defaulted), got %q", c.status, gotStatus)
		}
	}
}

func TestDeriveLifecycleFromStage(t *testing.T) {
	cases := []struct {
		stage     Stage
		scheduled bool
		want      Status
	}{
		{StageTodo, false, StatusBacklog},
		{StageTodo, true, StatusScheduled},
		{StageDoing, false, StatusFocus},
		{StageDoing, true, StatusFocus},
		{StageWaiting, true, StatusBacklog},
		{StageDone, true, StatusDone},
		{Stage(""), false, StatusBacklog},
	}
	for _, c := range cases {
		gotStatus, gotStage := DeriveLifecycle(StatusFocus, c.stage, true, c.scheduled)
		if gotStatus != c.want {
			t.Errorf("DeriveLifecycle(stage=%q scheduled=%v): want status %q, got %q", c.stage, c.scheduled, c.want, gotStatus)
		}
		wantStage := c.stage
		if !c.stage.Valid() {
			wantStage = StageTodo
		}
		if gotStage != wantStage {
			t.Errorf("DeriveLifecycle(stage=%q): stage must be kept (or defaulted), got %q", c.stage, gotStage)
		}
	}
	// stage wins when both are given: it is the field that survives
	if s, st := DeriveLifecycle(StatusDone, StageDoing, true, false); s != StatusFocus || st != StageDoing {
		t.Errorf("stage must be authoritative when set: got %q/%q", s, st)
	}
}

func TestDeriveLifecycleRoundTrips(t *testing.T) {
	// Every stage survives stage → status → stage, so a client that only
	// speaks status can never knock a row out of its stage.
	for _, st := range []Stage{StageTodo, StageDoing, StageDone} {
		status, _ := DeriveLifecycle("", st, true, false)
		if _, back := DeriveLifecycle(status, "", false, false); back != st {
			t.Errorf("stage %q → status %q → stage %q", st, status, back)
		}
	}
	// waiting is the one lossy case (status has no waiting), by design
	if _, back := DeriveLifecycle(StatusBacklog, "", false, false); back != StageTodo {
		t.Errorf("backlog → %q, want todo", back)
	}
}

func TestAskValidate(t *testing.T) {
	ok := Ask{What: "sign the SOW", ForWhom: "legal", Why: "kickoff is Monday"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid ask rejected: %v", err)
	}
	if !(Ask{}).IsZero() || ok.IsZero() {
		t.Error("IsZero is wrong")
	}
	big := strings.Repeat("x", MaxAskFieldLen+1)
	for _, a := range []Ask{{What: big}, {ForWhom: big}, {Why: big}} {
		err := a.Validate()
		ve, isVE := err.(*ValidationError)
		if !isVE || ve.Field != "ask" {
			t.Errorf("oversize ask %+v: want ValidationError on ask, got %v", a, err)
		}
	}
	if err := (Ask{What: strings.Repeat("x", MaxAskFieldLen)}).Validate(); err != nil {
		t.Errorf("ask at exactly the bound must pass: %v", err)
	}
}

func TestValidateWaiting(t *testing.T) {
	if err := ValidateWaiting(&Task{Stage: StageWaiting}); err == nil {
		t.Error("waiting with no reason and no person must be rejected")
	}
	if err := ValidateWaiting(&Task{Stage: StageWaiting, WaitingOnReason: "legal"}); err != nil {
		t.Errorf("waiting with a reason: %v", err)
	}
	who := NewID()
	if err := ValidateWaiting(&Task{Stage: StageWaiting, WaitingOnPersonID: &who}); err != nil {
		t.Errorf("waiting with a person: %v", err)
	}
	if err := ValidateWaiting(&Task{Stage: StageDoing}); err != nil {
		t.Errorf("not waiting never needs a reason: %v", err)
	}
}

func TestSyncWaitingSince(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)

	task := &Task{Stage: StageWaiting}
	SyncWaitingSince(task, StageTodo, now)
	if task.WaitingOnSince == nil || !task.WaitingOnSince.Equal(now) {
		t.Fatalf("entering waiting must stamp since=now, got %v", task.WaitingOnSince)
	}

	task.WaitingOnSince = &earlier
	SyncWaitingSince(task, StageWaiting, now)
	if !task.WaitingOnSince.Equal(earlier) {
		t.Error("staying in waiting must keep the original since")
	}

	task.Stage = StageDoing
	SyncWaitingSince(task, StageWaiting, now)
	if task.WaitingOnSince != nil {
		t.Error("leaving waiting must clear since")
	}
}
