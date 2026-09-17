package extract

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Heuristic is the always-available extractor: deterministic regexes over
// the text, no model, no network. It is deliberately conservative — every
// fact it emits is at most HeuristicMax confidence, and a client should show
// it as a suggestion, never a certainty. It also runs first under the LLM
// extractor, so its output is the floor the model improves on.
//
// What it finds:
//   - dates: ISO (2026-09-12, 2026-09-12T14:00), day-month ("Thu 12 Sep",
//     "12th September 2026"), month-day ("Sep 12", "September 12th, 2026"),
//     numeric day/month ("12/09", "12/09/26"), relative ("today",
//     "tomorrow", "next Friday", "Monday", "next week", "EOD", "EOW"), each
//     with an optional trailing time ("at 3pm", "14:00");
//   - deadlines: a date preceded by a cue ("by", "due", "deadline",
//     "before", "no later than", "until");
//   - people: emails (with a "Name <email>" / "Name (email)" name when
//     present, else one derived from the local part) and capitalised name
//     pairs not on the stoplist;
//   - links: http(s) URLs, with a Markdown label when written that way;
//   - suggested title: the first line, or its first sentence, at most 80
//     characters, with mail/markdown prefixes stripped.
type Heuristic struct{}

const (
	HeuristicModel         = "heuristic"
	HeuristicPromptVersion = "heuristic-v1"

	// maxScanBytes bounds how much text the regexes walk. Go's regexp is
	// linear, but a 200 KB paste is still 200 KB × a dozen passes; the
	// first 64 KB is where the facts of a note or a mail live.
	maxScanBytes = 64 << 10
)

var (
	reISO       = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::\d{2})?(?:Z|[+-]\d{2}:?\d{2})?)?\b`)
	reNumeric   = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})(?:/(\d{4}|\d{2}))?\b`)
	reDayMonth  = regexp.MustCompile(`(?i)\b(?:(mon|tue|wed|thu|fri|sat|sun)[a-z]*,?\s+)?(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?(?:,?\s+(\d{4}))?\b`)
	reMonthDay  = regexp.MustCompile(`(?i)\b(?:(mon|tue|wed|thu|fri|sat|sun)[a-z]*,?\s+)?(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:,?\s+(\d{4}))?\b`)
	reRelative  = regexp.MustCompile(`(?i)\b(today|tomorrow|tonight|(?:next|this)\s+(?:week|month|monday|tuesday|wednesday|thursday|friday|saturday|sunday|mon|tue|tues|wed|thu|thur|thurs|fri|sat|sun)\b|(?:on\s+)?(?:monday|tuesday|wednesday|thursday|friday|saturday|sunday)|eod|end of (?:the )?day|cob|eow|end of (?:the )?week)\b`)
	reTime      = regexp.MustCompile(`(?i)^\s*,?\s*(?:at\s+|@\s*)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\b`)
	reDeadline  = regexp.MustCompile(`(?i)\b(by|due(?:\s+on|\s+by)?|deadline(?:\s+is)?:?|before|no later than|until|latest)\s*:?\s*$`)
	reEmail     = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reNameEmail = regexp.MustCompile(`([A-Z][A-Za-z'\-]+(?:\s+[A-Z][A-Za-z'\-]+){0,2})\s*[<(]\s*$`)
	reCapWord   = regexp.MustCompile(`\b(?:[A-Z][a-z]+(?:[-'][A-Z][a-z]+)?|[A-Z]'[A-Z][a-z]+)\b`)
	reNameCue   = regexp.MustCompile(`(?i)\b(with|from|cc|to|attendees?|owner|assigned to|ask|ping|thanks)\s*:?\s*$`)
	reURL       = regexp.MustCompile(`\bhttps?://[^\s<>"'\)\]]+`)
	reMDLabel   = regexp.MustCompile(`\[([^\]]{1,120})\]\($`)
	reSentence  = regexp.MustCompile(`^(.*?[.!?])(?:\s|$)`)
	reTitleLead = regexp.MustCompile(`(?i)^(?:subject\s*:\s*|(?:re|fwd?|fw)\s*:\s*|#+\s*|[-*•]\s+|\d+[.)]\s+)+`)
)

// nameStop is every capitalised word that is not a person, in the kinds of
// text that reach the inbox: months, weekdays, mail and note furniture,
// meeting nouns, product names. Both words of a pair must clear it.
var nameStop = wordSet(`
january february march april may june july august september october november december
jan feb mar apr jun jul aug sep sept oct nov dec
monday tuesday wednesday thursday friday saturday sunday mon tue wed thu fri sat sun
the this that these those next last meeting meetings notes note action actions items item
project hi hello hey dear thanks thank best regards kind cheers subject from to cc re fwd
please team client clients sprint agenda summary update updates follow up review status weekly
monthly daily new old all our your my his her their its and or but for with you we they he
she it in on at of by is are was were be will can should would could ok okay yes no eod
eow cob asap tbd tbc invoice proposal contract budget launch release deadline due google
microsoft slack zoom github jira figma notion docs sheet sheets drive api sdk ui ux pr mvp
design product marketing sales engineering finance legal ops board planning roadmap kickoff
standup retro demo workshop call sync catch quick onboarding offboarding phase version draft
final plan week month year day morning afternoon evening north south east west street road
avenue city university ltd inc llc co company group limited plc q1 q2 q3 q4 email mail
attached attachment link links doc document decision decisions minutes recap todo done
waiting blocked open closed monday's also just when after before then now today tomorrow
tonight`)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

var monthIndex = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

var weekdayIndex = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

// Extract never fails: an empty input yields empty Facts.
func (Heuristic) Extract(_ context.Context, in Input) (Facts, error) {
	now := in.now()
	text := in.Text
	if len(text) > maxScanBytes {
		text = text[:maxScanBytes]
	}
	f := Facts{
		Model:         HeuristicModel,
		PromptVersion: HeuristicPromptVersion,
		ExtractedAt:   now.UTC().Truncate(time.Microsecond),
	}
	f.Dates, f.Deadlines = findDates(text, now)
	f.People = findPeople(text)
	f.Links = findLinks(text)
	f.SuggestedTitle = SuggestTitle(in.Title, in.Text)
	f.Normalize()
	return f, nil
}

// ---- dates ------------------------------------------------------------------

// span is one date candidate with its byte range in the text, so overlapping
// matches ("Thu 12 Sep" vs "12 Sep") resolve to the longest.
type span struct {
	start, end int
	at         time.Time
	conf       float64
	hasTime    bool
}

func findDates(text string, now time.Time) (dates, deadlines []Date) {
	loc := now.Location()
	var spans []span
	add := func(start, end int, at time.Time, conf float64) {
		if at.IsZero() {
			return
		}
		sp := span{start: start, end: end, at: at, conf: conf}
		// an adjacent "at 3pm" / "14:00" fixes the hour
		if m := reTime.FindStringSubmatch(tail(text, end, 16)); m != nil && (m[2] != "" || m[3] != "") {
			h, _ := strconv.Atoi(m[1])
			min := 0
			if m[2] != "" {
				min, _ = strconv.Atoi(m[2])
			}
			switch strings.ToLower(m[3]) {
			case "pm":
				if h < 12 {
					h += 12
				}
			case "am":
				if h == 12 {
					h = 0
				}
			}
			if h < 24 && min < 60 {
				sp.at = time.Date(at.Year(), at.Month(), at.Day(), h, min, 0, 0, loc)
				sp.end += len(m[0])
				sp.hasTime = true
			}
		}
		spans = append(spans, sp)
	}

	for _, m := range reISO.FindAllStringSubmatchIndex(text, -1) {
		y, _ := strconv.Atoi(text[m[2]:m[3]])
		mo, _ := strconv.Atoi(text[m[4]:m[5]])
		d, _ := strconv.Atoi(text[m[6]:m[7]])
		if !validDate(y, mo, d) {
			continue
		}
		h, min := 0, 0
		conf := 0.6
		if m[8] >= 0 {
			h, _ = strconv.Atoi(text[m[8]:m[9]])
			min, _ = strconv.Atoi(text[m[10]:m[11]])
			if h > 23 || min > 59 {
				continue
			}
		}
		spans = append(spans, span{start: m[0], end: m[1], at: time.Date(y, time.Month(mo), d, h, min, 0, 0, loc), conf: conf, hasTime: m[8] >= 0})
	}
	for _, m := range reDayMonth.FindAllStringSubmatchIndex(text, -1) {
		d, _ := strconv.Atoi(text[m[4]:m[5]])
		mo := monthIndex[strings.ToLower(text[m[6]:m[7]])[:3]]
		y, conf := inferYear(text, m[8], m[9], now, mo, d)
		if !validDate(y, int(mo), d) {
			continue
		}
		add(m[0], m[1], time.Date(y, mo, d, 0, 0, 0, 0, loc), conf)
	}
	for _, m := range reMonthDay.FindAllStringSubmatchIndex(text, -1) {
		mo := monthIndex[strings.ToLower(text[m[4]:m[5]])[:3]]
		d, _ := strconv.Atoi(text[m[6]:m[7]])
		y, conf := inferYear(text, m[8], m[9], now, mo, d)
		if !validDate(y, int(mo), d) {
			continue
		}
		add(m[0], m[1], time.Date(y, mo, d, 0, 0, 0, 0, loc), conf)
	}
	for _, m := range reNumeric.FindAllStringSubmatchIndex(text, -1) {
		// day/month (12/09 = 12 September), the convention the product ships
		// with; a month-first reading is the LLM's to make.
		d, _ := strconv.Atoi(text[m[2]:m[3]])
		mo, _ := strconv.Atoi(text[m[4]:m[5]])
		if mo > 12 && d <= 12 {
			d, mo = mo, d
		}
		y, conf := inferYear(text, m[6], m[7], now, time.Month(mo), d)
		conf = min(conf, 0.35)
		if !validDate(y, mo, d) {
			continue
		}
		add(m[0], m[1], time.Date(y, time.Month(mo), d, 0, 0, 0, 0, loc), conf)
	}
	for _, m := range reRelative.FindAllStringSubmatchIndex(text, -1) {
		at, conf := resolveRelative(strings.ToLower(text[m[2]:m[3]]), now)
		if at.IsZero() {
			continue
		}
		add(m[0], m[1], at, conf)
	}

	spans = dropOverlaps(spans)
	seen := map[string]bool{}
	for _, sp := range spans {
		d := Date{Text: text[sp.start:sp.end], At: sp.at, Confidence: min(sp.conf, HeuristicMax)}
		// "by <date>", "due <date>" … is a deadline too
		if cue := reDeadline.FindStringIndex(head(text, sp.start, 24)); cue != nil {
			lead := head(text, sp.start, 24)
			dl := d
			dl.Text = strings.TrimSpace(lead[cue[0]:] + text[sp.start:sp.end])
			if k := dateKey(dl); !seen["dl:"+k] {
				seen["dl:"+k] = true
				deadlines = append(deadlines, dl)
			}
		}
		if k := dateKey(d); !seen[k] {
			seen[k] = true
			dates = append(dates, d)
		}
	}
	return dates, deadlines
}

// inferYear reads an explicit year group or picks one: this year, unless
// that lands more than 90 days in the past — notes look forward — in which
// case next year. An explicit year earns a little confidence.
func inferYear(text string, ys, ye int, now time.Time, mo time.Month, d int) (int, float64) {
	if ys >= 0 {
		y, _ := strconv.Atoi(text[ys:ye])
		if y < 100 {
			y += 2000
		}
		return y, 0.55
	}
	y := now.Year()
	if validDate(y, int(mo), d) {
		if cand := time.Date(y, mo, d, 0, 0, 0, 0, now.Location()); cand.Before(now.AddDate(0, 0, -90)) {
			y++
		}
	}
	return y, 0.5
}

func resolveRelative(phrase string, now time.Time) (time.Time, float64) {
	loc := now.Location()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	eod := func(t time.Time) time.Time { return t.Add(17 * time.Hour) }
	nextWeekday := func(from time.Time, wd time.Weekday, strict bool) time.Time {
		delta := (int(wd) - int(from.Weekday()) + 7) % 7
		if delta == 0 && strict {
			delta = 7
		}
		return from.AddDate(0, 0, delta)
	}
	phrase = strings.TrimPrefix(phrase, "on ")
	switch phrase {
	case "today":
		return day, 0.5
	case "tonight":
		return day.Add(19 * time.Hour), 0.45
	case "tomorrow":
		return day.AddDate(0, 0, 1), 0.5
	case "eod", "end of day", "end of the day", "cob":
		return eod(day), 0.45
	case "eow", "end of week", "end of the week":
		return eod(nextWeekday(day, time.Friday, false)), 0.4
	case "next week":
		return nextWeekday(day, time.Monday, true), 0.4
	case "this week":
		return eod(nextWeekday(day, time.Friday, false)), 0.3
	case "next month":
		return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, loc), 0.35
	case "this month":
		return time.Time{}, 0
	}
	strict := false
	rest := phrase
	if strings.HasPrefix(phrase, "next ") {
		strict, rest = true, strings.TrimPrefix(phrase, "next ")
	} else if strings.HasPrefix(phrase, "this ") {
		rest = strings.TrimPrefix(phrase, "this ")
	}
	if len(rest) >= 3 {
		if wd, ok := weekdayIndex[rest[:3]]; ok {
			conf := 0.4
			if strict {
				conf = 0.45
			}
			return nextWeekday(day, wd, strict), conf
		}
	}
	return time.Time{}, 0
}

func validDate(y, m, d int) bool {
	if y < 1970 || y > 2200 || m < 1 || m > 12 || d < 1 || d > 31 {
		return false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t.Day() == d
}

// dropOverlaps keeps the longest span where candidates overlap, in text order.
func dropOverlaps(spans []span) []span {
	if len(spans) < 2 {
		return spans
	}
	// longest first so the survivors are decided before their neighbours
	order := make([]span, len(spans))
	copy(order, spans)
	sortSpans(order)
	var kept []span
	for _, sp := range order {
		clash := false
		for _, k := range kept {
			if sp.start < k.end && k.start < sp.end {
				clash = true
				break
			}
		}
		if !clash {
			kept = append(kept, sp)
		}
	}
	// back to text order
	for i := 1; i < len(kept); i++ {
		for j := i; j > 0 && kept[j].start < kept[j-1].start; j-- {
			kept[j], kept[j-1] = kept[j-1], kept[j]
		}
	}
	return kept
}

func sortSpans(spans []span) {
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0; j-- {
			a, b := spans[j-1], spans[j]
			if (a.end-a.start) < (b.end-b.start) || ((a.end-a.start) == (b.end-b.start) && a.start > b.start) {
				spans[j], spans[j-1] = spans[j-1], spans[j]
				continue
			}
			break
		}
	}
}

// head returns up to n bytes before offset; tail up to n bytes after.
func head(text string, offset, n int) string {
	if offset > len(text) {
		offset = len(text)
	}
	start := offset - n
	if start < 0 {
		start = 0
	}
	return text[start:offset]
}

func tail(text string, offset, n int) string {
	if offset > len(text) {
		return ""
	}
	end := offset + n
	if end > len(text) {
		end = len(text)
	}
	return text[offset:end]
}

// ---- people -----------------------------------------------------------------

func findPeople(text string) []Person {
	var out []Person
	byEmail := map[string]int{}
	byName := map[string]int{}

	for _, m := range reEmail.FindAllStringIndex(text, -1) {
		email := strings.ToLower(text[m[0]:m[1]])
		if _, ok := byEmail[email]; ok {
			continue
		}
		p := Person{Email: email, Confidence: 0.6}
		if nm := reNameEmail.FindStringSubmatch(head(text, m[0], 60)); nm != nil && !stopped(nm[1]) {
			p.Name = nm[1]
		} else if nm := nameFromLocalPart(email); nm != "" {
			p.Name = nm
			p.Confidence = 0.5
		}
		byEmail[email] = len(out)
		if p.Name != "" {
			byName[strings.ToLower(p.Name)] = len(out)
		}
		out = append(out, p)
	}

	// Capitalised words, paired with their immediate neighbour: "Hi Jane Doe"
	// yields "Jane Doe" once "Hi" is stopped, which a pair regex (which would
	// consume "Hi Jane" and never see "Jane Doe") cannot do.
	words := reCapWord.FindAllStringIndex(text, -1)
	for i := 0; i+1 < len(words); i++ {
		a, b := words[i], words[i+1]
		if b[0] != a[1]+1 || text[a[1]] != ' ' {
			continue
		}
		first, last := text[a[0]:a[1]], text[b[0]:b[1]]
		if nameStop[strings.ToLower(first)] || nameStop[strings.ToLower(last)] {
			continue
		}
		// inside an email address or a URL: not a name
		if b[1] < len(text) && (text[b[1]] == '@' || text[b[1]] == '.' && b[1]+1 < len(text) && text[b[1]+1] != ' ') {
			continue
		}
		i++ // the pair is spent; "Jane Doe Smith" does not also yield "Doe Smith"
		name := first + " " + last
		key := strings.ToLower(name)
		conf := 0.4
		if reNameCue.MatchString(head(text, a[0], 16)) {
			conf = 0.5
		}
		if i, ok := byName[key]; ok {
			if conf > out[i].Confidence {
				out[i].Confidence = conf
			}
			continue
		}
		byName[key] = len(out)
		out = append(out, Person{Name: name, Confidence: conf})
	}
	return out
}

func stopped(name string) bool {
	for _, w := range strings.Fields(name) {
		if nameStop[strings.ToLower(w)] {
			return true
		}
	}
	return false
}

// nameFromLocalPart turns jane.doe / jane_doe / jane-doe into "Jane Doe";
// anything with digits or a single token is left alone (no name is better
// than "Info" or "Jd42").
func nameFromLocalPart(email string) string {
	local, _, _ := strings.Cut(email, "@")
	parts := strings.FieldsFunc(local, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == '+' })
	if len(parts) < 2 {
		return ""
	}
	var words []string
	for _, p := range parts {
		if p == "" {
			continue
		}
		for _, r := range p {
			if r < 'a' || r > 'z' {
				return ""
			}
		}
		words = append(words, strings.ToUpper(p[:1])+p[1:])
	}
	if len(words) < 2 {
		return ""
	}
	return strings.Join(words, " ")
}

// ---- links ------------------------------------------------------------------

func findLinks(text string) []Link {
	var out []Link
	seen := map[string]bool{}
	for _, m := range reURL.FindAllStringIndex(text, -1) {
		u := strings.TrimRight(text[m[0]:m[1]], ".,;:!?")
		// a closing paren that has no opening one inside the URL belongs to the prose
		for strings.HasSuffix(u, ")") && strings.Count(u, "(") < strings.Count(u, ")") {
			u = strings.TrimSuffix(u, ")")
		}
		if seen[u] {
			continue
		}
		seen[u] = true
		l := Link{URL: u}
		if lm := reMDLabel.FindStringSubmatch(head(text, m[0], 130)); lm != nil {
			l.Label = lm[1]
		}
		out = append(out, l)
	}
	return out
}

// ---- title ------------------------------------------------------------------

// SuggestTitle is the first non-empty line of the text (or the given title
// when there is no text), stripped of "Subject:", "Re:", markdown heading and
// bullet prefixes, reduced to its first sentence when the line runs long,
// and never more than MaxTitleLen characters.
func SuggestTitle(title, text string) string {
	line := ""
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	if line == "" {
		line = strings.TrimSpace(title)
	}
	line = reTitleLead.ReplaceAllString(line, "")
	line = strings.TrimSpace(strings.Join(strings.Fields(line), " "))
	if runeLen(line) > MaxTitleLen {
		if m := reSentence.FindStringSubmatch(line); m != nil && runeLen(m[1]) <= MaxTitleLen {
			line = m[1]
		}
	}
	if runeLen(line) > MaxTitleLen {
		cut := truncateRunes(line, MaxTitleLen-1)
		if i := strings.LastIndex(cut, " "); i > MaxTitleLen/2 {
			cut = cut[:i]
		}
		line = strings.TrimRight(cut, " ,;:-") + "…"
	}
	return line
}

func runeLen(s string) int { return len([]rune(s)) }
