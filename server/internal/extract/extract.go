// Package extract pulls facts out of a captured signal's text: dates,
// deadlines, people, links and a suggested title (R4.2). The output is small,
// structured and content-free — it rides on the signal row (Signal.Extracted)
// that every inbox list returns, so it must never carry the body.
//
// Two extractors sit behind one interface. Heuristic is deterministic regex
// work and is always available; LLM asks the deployment's Ollama for the same
// shape with a strict JSON prompt and merges its answer over the heuristic
// one, falling back to it on any error. Confidence is per fact so a client
// can show "probably Thursday" differently from "12 Sep 2026 14:00".
package extract

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Confidence bounds. The heuristic never claims more than HeuristicMax; the
// LLM never more than LLMMax (it is still a guess, however well phrased).
const (
	HeuristicMax = 0.6
	LLMMax       = 0.95
)

// Caps keep Facts inside domain.MaxSignalExtractedLen (16 KB) with room for
// whatever else a client seeded into `extracted`. Trim enforces them.
const (
	MaxDates     = 30
	MaxDeadlines = 30
	MaxPeople    = 30
	MaxLinks     = 50
	MaxTextLen   = 120 // runes, for any `text`/`label`/`name` field
	MaxTitleLen  = 80  // runes, suggestedTitle
)

// Date is a moment the text mentions ("Thu 12 Sep", "next Friday", "by EOD").
// Text is the phrase as written; At is what it resolves to. Corrected marks a
// fact a human rewrote (the client's chip editor sets it): no extractor ever
// sets it, and Merge never overwrites one — see the Corrected note on Merge.
type Date struct {
	Text       string    `json:"text"`
	At         time.Time `json:"at"`
	Confidence float64   `json:"confidence"`
	Corrected  bool      `json:"corrected,omitempty"`
}

// Person is someone the text names. Email is set when it appeared alongside.
// Corrected means the same as on Date.
type Person struct {
	Name       string  `json:"name"`
	Email      string  `json:"email,omitempty"`
	Confidence float64 `json:"confidence"`
	Corrected  bool    `json:"corrected,omitempty"`
}

// Link mirrors domain.Link (label,url) so the manual capture's seeded links
// merge in without translation.
type Link struct {
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

// Facts is the stored shape of Signal.Extracted after extraction. Model and
// PromptVersion say how it was produced ("heuristic"/"heuristic-v1" or an
// Ollama model name/"facts-v1") so a client can offer "re-extract with the
// model" when only the heuristic ran.
type Facts struct {
	Dates          []Date    `json:"dates"`
	People         []Person  `json:"people"`
	Links          []Link    `json:"links"`
	Deadlines      []Date    `json:"deadlines"`
	SuggestedTitle string    `json:"suggestedTitle"`
	Model          string    `json:"model"`
	PromptVersion  string    `json:"promptVersion"`
	ExtractedAt    time.Time `json:"extractedAt"`
}

// Input is what an extractor sees: the signal's title and full text, plus
// the clock and zone relative dates resolve against ("tomorrow" needs both).
type Input struct {
	Title string
	Text  string
	Now   time.Time      // zero ⇒ time.Now()
	Loc   *time.Location // nil ⇒ Now's location
}

func (in Input) now() time.Time {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	if in.Loc != nil {
		now = now.In(in.Loc)
	}
	return now
}

// Extractor turns an Input into Facts. Implementations must be safe for
// concurrent use and must never return the body in any field.
type Extractor interface {
	Extract(ctx context.Context, in Input) (Facts, error)
}

// Normalize keeps the JSON contract stable ([] rather than null) and the
// per-field bounds honest.
func (f *Facts) Normalize() {
	if f.Dates == nil {
		f.Dates = []Date{}
	}
	if f.People == nil {
		f.People = []Person{}
	}
	if f.Links == nil {
		f.Links = []Link{}
	}
	if f.Deadlines == nil {
		f.Deadlines = []Date{}
	}
	for i := range f.Dates {
		f.Dates[i].Text = truncateRunes(strings.TrimSpace(f.Dates[i].Text), MaxTextLen)
		f.Dates[i].Confidence = clamp(f.Dates[i].Confidence)
	}
	for i := range f.Deadlines {
		f.Deadlines[i].Text = truncateRunes(strings.TrimSpace(f.Deadlines[i].Text), MaxTextLen)
		f.Deadlines[i].Confidence = clamp(f.Deadlines[i].Confidence)
	}
	for i := range f.People {
		f.People[i].Name = truncateRunes(strings.TrimSpace(f.People[i].Name), MaxTextLen)
		f.People[i].Email = truncateRunes(strings.ToLower(strings.TrimSpace(f.People[i].Email)), 254)
		f.People[i].Confidence = clamp(f.People[i].Confidence)
	}
	for i := range f.Links {
		f.Links[i].URL = strings.TrimSpace(f.Links[i].URL)
		f.Links[i].Label = truncateRunes(strings.TrimSpace(f.Links[i].Label), MaxTextLen)
	}
	f.SuggestedTitle = truncateRunes(strings.TrimSpace(f.SuggestedTitle), MaxTitleLen)
}

// Trim enforces the list caps and then, if the encoded form is still over
// maxBytes, halves the lists until it fits — a pasted 200 KB transcript can
// legitimately mention hundreds of dates, and the row has a 16 KB budget.
func (f *Facts) Trim(maxBytes int) {
	f.Normalize()
	capTo := func(n int) {
		f.Dates = capDates(f.Dates, n)
		f.Deadlines = capDates(f.Deadlines, n)
		f.People = capPeople(f.People, n)
		if len(f.Links) > n {
			f.Links = f.Links[:n]
		}
	}
	f.Dates = capDates(f.Dates, MaxDates)
	f.Deadlines = capDates(f.Deadlines, MaxDeadlines)
	f.People = capPeople(f.People, MaxPeople)
	if len(f.Links) > MaxLinks {
		f.Links = f.Links[:MaxLinks]
	}
	if maxBytes <= 0 {
		return
	}
	for n := MaxLinks; n > 0; n /= 2 {
		b, err := json.Marshal(f)
		if err == nil && len(b) <= maxBytes {
			return
		}
		capTo(n / 2)
	}
}

// capDates / capPeople keep the first n entries — but a corrected fact is
// never the one that falls off the end: when the budget bites, the human's
// edits come first and the model's readings fill what is left.
func capDates(ds []Date, n int) []Date {
	if len(ds) <= n {
		return ds
	}
	out := make([]Date, 0, n)
	for _, d := range ds {
		if d.Corrected {
			out = append(out, d)
		}
	}
	for _, d := range ds {
		if !d.Corrected && len(out) < n {
			out = append(out, d)
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func capPeople(ps []Person, n int) []Person {
	if len(ps) <= n {
		return ps
	}
	out := make([]Person, 0, n)
	for _, p := range ps {
		if p.Corrected {
			out = append(out, p)
		}
	}
	for _, p := range ps {
		if !p.Corrected && len(out) < n {
			out = append(out, p)
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Merge lays `over` on top of `base`: facts that name the same thing keep
// the higher confidence (and fill in whatever the other lacked — an email,
// a label); everything else is appended in base-then-over order. The title,
// model and prompt version come from `over` when it has them.
//
// CORRECTED FACTS ARE NEVER OVERWRITTEN. A fact a human rewrote (Corrected)
// keeps its text/name and its flag whatever `over` says about the same thing,
// so a background re-extraction can never quietly take a correction back. A
// person is "the same thing" by email when there is one, else by name — so a
// corrected name without an email reads as a new person and the model's
// original comes back alongside it, which is the honest outcome: nothing tells
// the server the two are one.
func Merge(base, over Facts) Facts {
	out := Facts{
		Dates:          mergeDates(base.Dates, over.Dates),
		Deadlines:      mergeDates(base.Deadlines, over.Deadlines),
		People:         mergePeople(base.People, over.People),
		Links:          mergeLinks(base.Links, over.Links),
		SuggestedTitle: base.SuggestedTitle,
		Model:          base.Model,
		PromptVersion:  base.PromptVersion,
		ExtractedAt:    base.ExtractedAt,
	}
	if strings.TrimSpace(over.SuggestedTitle) != "" {
		out.SuggestedTitle = over.SuggestedTitle
	}
	if over.Model != "" {
		out.Model, out.PromptVersion = over.Model, over.PromptVersion
	}
	if !over.ExtractedAt.IsZero() {
		out.ExtractedAt = over.ExtractedAt
	}
	out.Normalize()
	return out
}

// dateKey identifies "the same date": same minute, whatever the phrasing.
func dateKey(d Date) string { return d.At.UTC().Truncate(time.Minute).Format(time.RFC3339) }

func mergeDates(base, over []Date) []Date {
	out := []Date{}
	idx := map[string]int{}
	add := func(d Date) {
		k := dateKey(d)
		if i, ok := idx[k]; ok {
			switch {
			case out[i].Corrected: // a human's reading outranks every later one
			case d.Corrected:
				out[i] = d
			case d.Confidence > out[i].Confidence:
				out[i].Confidence = d.Confidence
				if strings.TrimSpace(d.Text) != "" {
					out[i].Text = d.Text
				}
			}
			return
		}
		idx[k] = len(out)
		out = append(out, d)
	}
	for _, d := range base {
		add(d)
	}
	for _, d := range over {
		add(d)
	}
	return out
}

func personKey(p Person) string {
	if e := strings.ToLower(strings.TrimSpace(p.Email)); e != "" {
		return "e:" + e
	}
	return "n:" + strings.ToLower(strings.Join(strings.Fields(p.Name), " "))
}

func mergePeople(base, over []Person) []Person {
	out := []Person{}
	byKey := map[string]int{}
	byName := map[string]int{} // lets a bare name find its emailed twin
	add := func(p Person) {
		name := strings.ToLower(strings.Join(strings.Fields(p.Name), " "))
		i, ok := byKey[personKey(p)]
		if !ok && name != "" {
			i, ok = byName[name]
		}
		if ok {
			if p.Confidence > out[i].Confidence && !out[i].Corrected {
				out[i].Confidence = p.Confidence
			}
			if p.Corrected && !out[i].Corrected { // the correction arrived in `over`
				out[i].Name, out[i].Corrected = p.Name, true
			}
			if out[i].Email == "" && p.Email != "" {
				out[i].Email = p.Email
				byKey[personKey(p)] = i
			}
			if out[i].Name == "" && p.Name != "" {
				out[i].Name = p.Name
			}
			return
		}
		byKey[personKey(p)] = len(out)
		if name != "" {
			byName[name] = len(out)
		}
		out = append(out, p)
	}
	for _, p := range base {
		add(p)
	}
	for _, p := range over {
		add(p)
	}
	return out
}

func mergeLinks(base, over []Link) []Link {
	out := []Link{}
	idx := map[string]int{}
	add := func(l Link) {
		k := strings.TrimRight(strings.TrimSpace(l.URL), "/")
		if k == "" {
			return
		}
		if i, ok := idx[k]; ok {
			if out[i].Label == "" && l.Label != "" {
				out[i].Label = l.Label
			}
			return
		}
		idx[k] = len(out)
		out = append(out, l)
	}
	for _, l := range base {
		add(l)
	}
	for _, l := range over {
		add(l)
	}
	return out
}

// SortByTime orders dates/deadlines soonest first; extractors return them in
// order of appearance, which is the better default for "what did it say",
// so this is opt-in for callers that want a timeline.
func SortByTime(ds []Date) {
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].At.Before(ds[j].At) })
}

func clamp(c float64) float64 {
	switch {
	case c < 0:
		return 0
	case c > 1:
		return 1
	}
	return c
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
