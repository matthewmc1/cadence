package extract

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// now is a fixed Monday so relative dates in the table are deterministic:
// Mon 7 Sep 2026 10:00 in London (BST, +01:00).
var (
	london = mustLoc("Europe/London")
	now    = time.Date(2026, 9, 7, 10, 0, 0, 0, london)
)

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func day(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, london)
}

func run(t *testing.T, text string) Facts {
	t.Helper()
	f, err := Heuristic{}.Extract(context.Background(), Input{Text: text, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func hasDate(ds []Date, at time.Time) bool {
	for _, d := range ds {
		if d.At.Equal(at) {
			return true
		}
	}
	return false
}

func hasPerson(ps []Person, name, email string) bool {
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) && (email == "" || p.Email == email) {
			return true
		}
	}
	return false
}

func hasLink(ls []Link, url string) bool {
	for _, l := range ls {
		if l.URL == url {
			return true
		}
	}
	return false
}

func TestHeuristicDates(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantDates []time.Time
		wantDL    []time.Time
	}{
		{
			name:      "iso date and datetime",
			text:      "Kickoff moved to 2026-09-12. Slides due 2026-09-11T14:30.",
			wantDates: []time.Time{day(2026, 9, 12, 0, 0), day(2026, 9, 11, 14, 30)},
			wantDL:    []time.Time{day(2026, 9, 11, 14, 30)},
		},
		{
			name:      "weekday day month with time",
			text:      "Let's meet Thu 12 Sep at 3pm to review the draft.",
			wantDates: []time.Time{day(2026, 9, 12, 15, 0)},
		},
		{
			name:      "ordinal day month year",
			text:      "The contract renews on 1st October 2026.",
			wantDates: []time.Time{day(2026, 10, 1, 0, 0)},
		},
		{
			name:      "month day comma year",
			text:      "Board meeting: September 12th, 2026 — agenda attached.",
			wantDates: []time.Time{day(2026, 9, 12, 0, 0)},
		},
		{
			name:      "next friday and by eod",
			text:      "Need the invoice by EOD and the deck next Friday.",
			wantDates: []time.Time{day(2026, 9, 7, 17, 0), day(2026, 9, 11, 0, 0)},
			wantDL:    []time.Time{day(2026, 9, 7, 17, 0)},
		},
		{
			name:      "tomorrow with time",
			text:      "Call with the client tomorrow 10:30.",
			wantDates: []time.Time{day(2026, 9, 8, 10, 30)},
		},
		{
			name:      "numeric day month is a deadline",
			text:      "Deadline: 12/09 for the proposal.",
			wantDates: []time.Time{day(2026, 9, 12, 0, 0)},
			wantDL:    []time.Time{day(2026, 9, 12, 0, 0)},
		},
		{
			name:      "bare weekday resolves forward",
			text:      "Jane will send the numbers on Wednesday.",
			wantDates: []time.Time{day(2026, 9, 9, 0, 0)},
		},
		{
			name:      "next week is monday",
			text:      "Let's pick this up next week.",
			wantDates: []time.Time{day(2026, 9, 14, 0, 0)},
		},
		{
			name:      "due before",
			text:      "Legal needs the redlines before 20 Sep; no later than Friday for the summary.",
			wantDates: []time.Time{day(2026, 9, 20, 0, 0), day(2026, 9, 11, 0, 0)},
			wantDL:    []time.Time{day(2026, 9, 20, 0, 0), day(2026, 9, 11, 0, 0)},
		},
		{
			name:      "past month without year rolls to next year",
			text:      "Renewal lands 3 Feb.",
			wantDates: []time.Time{day(2027, 2, 3, 0, 0)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := run(t, tc.text)
			for _, want := range tc.wantDates {
				if !hasDate(f.Dates, want) {
					t.Errorf("dates missing %s; got %s", want.Format(time.RFC3339), fmtDates(f.Dates))
				}
			}
			for _, want := range tc.wantDL {
				if !hasDate(f.Deadlines, want) {
					t.Errorf("deadlines missing %s; got %s", want.Format(time.RFC3339), fmtDates(f.Deadlines))
				}
			}
			if len(tc.wantDL) == 0 && len(f.Deadlines) != 0 {
				t.Errorf("unexpected deadlines %s", fmtDates(f.Deadlines))
			}
			for _, d := range append(f.Dates, f.Deadlines...) {
				if d.Confidence <= 0 || d.Confidence > HeuristicMax {
					t.Errorf("confidence %v out of heuristic range for %q", d.Confidence, d.Text)
				}
				if d.Text == "" {
					t.Errorf("date %s has no text", d.At)
				}
			}
		})
	}
}

func fmtDates(ds []Date) string {
	var parts []string
	for _, d := range ds {
		parts = append(parts, d.Text+"="+d.At.Format(time.RFC3339))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func TestHeuristicPeople(t *testing.T) {
	text := `From: Jane Doe <jane.doe@example.com>
To: ops@example.com
Subject: Q3 planning

Hi team — Meeting Notes from Monday. Action Items:
- Priya Natarajan to send the vendor list (cc Tom O'Brien)
- ping Marcus Lee about the Figma file
Thanks,
Jane`
	f := run(t, text)
	if !hasPerson(f.People, "Jane Doe", "jane.doe@example.com") {
		t.Errorf("Jane Doe with email missing: %+v", f.People)
	}
	if !hasPerson(f.People, "Priya Natarajan", "") || !hasPerson(f.People, "Tom O'Brien", "") || !hasPerson(f.People, "Marcus Lee", "") {
		t.Errorf("name pairs missing: %+v", f.People)
	}
	for _, bad := range []string{"Meeting Notes", "Action Items", "Hi Team", "Q3 Planning"} {
		if hasPerson(f.People, bad, "") {
			t.Errorf("%q should be stopped: %+v", bad, f.People)
		}
	}
	// ops@ has no human name to derive — email only, never "Ops"
	for _, p := range f.People {
		if p.Email == "ops@example.com" && p.Name != "" {
			t.Errorf("derived a name from a role mailbox: %+v", p)
		}
		if p.Confidence > HeuristicMax {
			t.Errorf("confidence %v over cap: %+v", p.Confidence, p)
		}
	}
	// the emailed Jane and the signature Jane collapse into one entry
	n := 0
	for _, p := range f.People {
		if strings.HasPrefix(strings.ToLower(p.Name), "jane") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Jane appears %d times: %+v", n, f.People)
	}
}

func TestHeuristicLinksAndTitle(t *testing.T) {
	text := `Subject: Re: Design review follow-ups

See the [brief](https://docs.example.com/brief) and https://github.com/acme/site/pull/42). Also http://example.com/a?b=1&c=2.`
	f := run(t, text)
	if !hasLink(f.Links, "https://docs.example.com/brief") || !hasLink(f.Links, "https://github.com/acme/site/pull/42") || !hasLink(f.Links, "http://example.com/a?b=1&c=2") {
		t.Errorf("links = %+v", f.Links)
	}
	for _, l := range f.Links {
		if l.URL == "https://docs.example.com/brief" && l.Label != "brief" {
			t.Errorf("markdown label lost: %+v", l)
		}
	}
	if f.SuggestedTitle != "Design review follow-ups" {
		t.Errorf("title = %q", f.SuggestedTitle)
	}

	long := "This is the first sentence of a rather long opening line that goes on. And then a second one that should not be part of the title."
	if got := SuggestTitle("", long); got != "This is the first sentence of a rather long opening line that goes on." {
		t.Errorf("first sentence title = %q", got)
	}
	noStop := strings.Repeat("word ", 40)
	if got := SuggestTitle("", noStop); len([]rune(got)) > MaxTitleLen || !strings.HasSuffix(got, "…") {
		t.Errorf("truncated title = %q (%d)", got, len([]rune(got)))
	}
	if got := SuggestTitle("Fallback title", ""); got != "Fallback title" {
		t.Errorf("empty text should fall back to the title: %q", got)
	}
}

func TestHeuristicEmptyAndBounds(t *testing.T) {
	f := run(t, "")
	b, _ := json.Marshal(f)
	for _, key := range []string{`"dates":[]`, `"people":[]`, `"links":[]`, `"deadlines":[]`, `"model":"heuristic"`, `"promptVersion":"heuristic-v1"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("empty facts missing %s: %s", key, b)
		}
	}

	// a paste that mentions hundreds of dates still fits the row budget
	var sb strings.Builder
	for i := 0; i < 400; i++ {
		sb.WriteString("Review on 2026-")
		sb.WriteString([]string{"01", "02", "03", "04", "05", "06", "07", "08", "09", "10", "11", "12"}[i%12])
		sb.WriteString("-")
		sb.WriteString([]string{"01", "05", "09", "13", "17", "21", "25"}[i%7])
		sb.WriteString(" with Person")
		sb.WriteString(string(rune('A' + i%26)))
		sb.WriteString(" Surname and https://example.com/")
		sb.WriteString(strings.Repeat("x", i%50))
		sb.WriteString("\n")
	}
	f = run(t, sb.String())
	f.Trim(16 << 10)
	b, _ = json.Marshal(f)
	if len(b) > 16<<10 {
		t.Errorf("trimmed facts are %d bytes", len(b))
	}
	if len(f.Dates) > MaxDates || len(f.Links) > MaxLinks || len(f.People) > MaxPeople {
		t.Errorf("caps not applied: %d dates %d links %d people", len(f.Dates), len(f.Links), len(f.People))
	}
}

func TestMergeKeepsHigherConfidence(t *testing.T) {
	at := day(2026, 9, 12, 0, 0)
	base := Facts{
		Dates:          []Date{{Text: "12 Sep", At: at, Confidence: 0.5}},
		People:         []Person{{Name: "Jane Doe", Confidence: 0.4}},
		Links:          []Link{{URL: "https://a.example"}},
		SuggestedTitle: "first line",
		Model:          HeuristicModel, PromptVersion: HeuristicPromptVersion,
	}
	over := Facts{
		Dates:          []Date{{Text: "Saturday 12th", At: at.Add(30 * time.Second), Confidence: 0.8}, {Text: "tomorrow", At: day(2026, 9, 8, 0, 0), Confidence: 0.7}},
		People:         []Person{{Name: "Jane Doe", Email: "jane@example.com", Confidence: 0.3}},
		Links:          []Link{{URL: "https://a.example", Label: "A"}},
		SuggestedTitle: "Better title",
		Model:          "gemma3:4b", PromptVersion: PromptVersion,
	}
	got := Merge(base, over)
	if len(got.Dates) != 2 || got.Dates[0].Confidence != 0.8 || got.Dates[0].Text != "Saturday 12th" {
		t.Errorf("dates = %+v", got.Dates)
	}
	if len(got.People) != 1 || got.People[0].Confidence != 0.4 || got.People[0].Email != "jane@example.com" {
		t.Errorf("people = %+v", got.People)
	}
	if len(got.Links) != 1 || got.Links[0].Label != "A" {
		t.Errorf("links = %+v", got.Links)
	}
	if got.SuggestedTitle != "Better title" || got.Model != "gemma3:4b" || got.PromptVersion != PromptVersion {
		t.Errorf("title/model = %q %q %q", got.SuggestedTitle, got.Model, got.PromptVersion)
	}
}
