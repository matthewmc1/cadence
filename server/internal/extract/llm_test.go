package extract

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeChat answers with a canned reply (or error) and records the prompt.
type fakeChat struct {
	reply  string
	err    error
	models []string
	gotMsg []Message
	model  string
}

func (f *fakeChat) Chat(_ context.Context, model string, msgs []Message, _ bool) (string, error) {
	f.model, f.gotMsg = model, msgs
	return f.reply, f.err
}
func (f *fakeChat) Models(context.Context) ([]string, error) { return f.models, nil }

const sample = `Hi Jane Doe — can you send the Q3 numbers by Friday? Deck is at https://docs.example.com/q3. Thanks, Priya (priya@example.com)`

func TestLLMMergesOverHeuristic(t *testing.T) {
	fc := &fakeChat{models: []string{"gemma3:4b", "qwen2.5:7b"}, reply: "```json\n" + `{
	  "suggestedTitle": "Send Q3 numbers to Priya",
	  "dates": [{"text": "by Friday", "at": "2026-09-11T17:00:00+01:00", "confidence": 0.9}],
	  "deadlines": [{"text": "by Friday", "at": "2026-09-11T17:00:00+01:00", "confidence": 0.9}],
	  "people": [
	    {"name": "Jane Doe", "confidence": 0.85},
	    {"name": "Priya", "email": "priya@example.com", "confidence": 0.8},
	    {"name": "Invented Person", "email": "nobody@example.com", "confidence": 0.9}
	  ],
	  "links": [{"url": "https://docs.example.com/q3", "label": "Q3 deck"}, {"url": "https://evil.example/phish"}]
	}` + "\n```"}
	l := &LLM{Client: fc}
	f, err := l.Extract(context.Background(), Input{Text: sample, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if fc.model != "gemma3:4b" {
		t.Errorf("picked model %q, want the first installed", fc.model)
	}
	if len(fc.gotMsg) != 2 || !strings.Contains(fc.gotMsg[0].Content, now.Format(time.RFC3339)) || !strings.Contains(fc.gotMsg[1].Content, sample) {
		t.Errorf("prompt = %+v", fc.gotMsg)
	}
	if f.Model != "gemma3:4b" || f.PromptVersion != PromptVersion || f.SuggestedTitle != "Send Q3 numbers to Priya" {
		t.Errorf("model/title = %q %q %q", f.Model, f.PromptVersion, f.SuggestedTitle)
	}
	// the heuristic found "Friday" at 00:00 with ≤0.6; the model's 17:00 is a
	// different minute, so both survive — the model's with its own confidence
	if !hasDate(f.Deadlines, day(2026, 9, 11, 17, 0)) {
		t.Errorf("deadlines = %+v", f.Deadlines)
	}
	if !hasPerson(f.People, "Jane Doe", "") || !hasPerson(f.People, "Priya", "priya@example.com") {
		t.Errorf("people = %+v", f.People)
	}
	for _, p := range f.People {
		if p.Email == "nobody@example.com" || p.Name == "Invented Person" {
			t.Errorf("invented person survived: %+v", p)
		}
		if p.Name == "Jane Doe" && p.Confidence != 0.85 {
			t.Errorf("Jane should carry the model's confidence: %+v", p)
		}
	}
	if !hasLink(f.Links, "https://docs.example.com/q3") || hasLink(f.Links, "https://evil.example/phish") {
		t.Errorf("links = %+v", f.Links)
	}
	for _, l := range f.Links {
		if l.URL == "https://docs.example.com/q3" && l.Label != "Q3 deck" {
			t.Errorf("label not merged: %+v", l)
		}
	}
}

func TestLLMFallsBackToHeuristic(t *testing.T) {
	for name, fc := range map[string]*fakeChat{
		"transport error": {models: []string{"m"}, err: errors.New("connection refused")},
		"bad json":        {models: []string{"m"}, reply: "Sure! Here are the facts: none."},
		"no models":       {},
	} {
		t.Run(name, func(t *testing.T) {
			l := &LLM{Client: fc, Timeout: time.Second}
			f, err := l.Extract(context.Background(), Input{Text: sample, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			if f.Model != HeuristicModel || f.PromptVersion != HeuristicPromptVersion {
				t.Errorf("model = %q %q, want heuristic", f.Model, f.PromptVersion)
			}
			if !hasPerson(f.People, "Jane Doe", "") || !hasLink(f.Links, "https://docs.example.com/q3") || !hasDate(f.Deadlines, day(2026, 9, 11, 0, 0)) {
				t.Errorf("heuristic facts missing: %+v", f)
			}
		})
	}
}

func TestParseAtAndFences(t *testing.T) {
	if at, ok := parseAt("2026-09-11", now); !ok || !at.Equal(day(2026, 9, 11, 0, 0)) {
		t.Errorf("bare date = %v %v", at, ok)
	}
	if at, ok := parseAt("2026-09-11T09:15", now); !ok || !at.Equal(day(2026, 9, 11, 9, 15)) {
		t.Errorf("naive datetime = %v %v", at, ok)
	}
	if _, ok := parseAt("Friday", now); ok {
		t.Error("prose should not parse")
	}
	if got := stripFences("Here you go:\n```json\n{\"a\":1}\n```\nHope that helps"); got != `{"a":1}` {
		t.Errorf("stripFences = %q", got)
	}
}
