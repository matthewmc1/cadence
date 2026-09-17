package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// LLM asks a chat model for the same Facts shape with a strict JSON prompt
// and lays the answer over the heuristic result (Merge keeps the higher
// confidence per fact). Any failure — no model, timeout, bad JSON, a model
// that invents an email — degrades to the heuristic Facts, never to an
// error: extraction is a convenience on the inbox path, not a gate.
type LLM struct {
	// Client is the chat transport (Ollama in production, a fake in tests).
	Client Chatter
	// Model is the Ollama model name; "" picks the first installed model.
	Model string
	// Timeout bounds one extraction end to end (default DefaultLLMTimeout).
	Timeout time.Duration
	// Fallback runs first and is what the model's answer merges over
	// (default Heuristic{}).
	Fallback Extractor
	Log      *slog.Logger
}

// Chatter is the slice of a chat provider extraction needs.
type Chatter interface {
	// Chat sends one non-streaming turn and returns the assistant's content.
	// jsonFormat asks the provider to constrain the reply to JSON.
	Chat(ctx context.Context, model string, messages []Message, jsonFormat bool) (string, error)
	// Models lists the installed model names, sorted.
	Models(ctx context.Context) ([]string, error)
}

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

const (
	// PromptVersion tags Facts produced by this prompt so a later prompt can
	// tell which rows to refresh.
	PromptVersion     = "facts-v1"
	DefaultLLMTimeout = 60 * time.Second

	// llmMaxTextRunes bounds what the model sees: a 4k-context model reads
	// the first few thousand characters and the rest is wasted tokens.
	llmMaxTextRunes = 12_000
	// llmDefaultConfidence is used when the model omits a confidence — it
	// found the fact, which is worth more than the heuristic's ceiling.
	llmDefaultConfidence = 0.7
)

// ErrNoModel is returned (and logged) when Ollama has nothing installed.
var ErrNoModel = errors.New("extract: no model installed")

const systemPrompt = `You extract facts from a captured note, email or meeting transcript for a task manager.
Reply with ONLY a JSON object — no prose, no code fences — with exactly these keys:
{
  "suggestedTitle": "a short, specific title for this text, at most 80 characters",
  "dates": [{"text": "the phrase as written", "at": "RFC3339 timestamp with offset", "confidence": 0.0-1.0}],
  "deadlines": [{"text": "the phrase as written, e.g. 'by Friday'", "at": "RFC3339 timestamp with offset", "confidence": 0.0-1.0}],
  "people": [{"name": "full name as written", "email": "only if it appears in the text, else omit", "confidence": 0.0-1.0}],
  "links": [{"url": "only URLs that appear verbatim in the text", "label": "optional"}]
}
Rules:
- NOW is %s (%s). Resolve relative dates ("tomorrow", "next Friday", "EOD") against NOW; a date without a time is 00:00; "end of day" is 17:00.
- A deadline is a date something is due by; list it under both "deadlines" and "dates".
- Never invent people, emails, URLs or dates that are not in the text. Use empty arrays when there is nothing.
- confidence is your certainty the fact is real and correctly resolved.`

// Extract runs the fallback, then the model, and merges. The returned error
// is always nil; the fallback's Facts carry Model "heuristic" when the model
// did not contribute.
func (l *LLM) Extract(ctx context.Context, in Input) (Facts, error) {
	fallback := l.Fallback
	if fallback == nil {
		fallback = Heuristic{}
	}
	base, err := fallback.Extract(ctx, in)
	if err != nil {
		return base, err
	}
	timeout := l.Timeout
	if timeout <= 0 {
		timeout = DefaultLLMTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	over, model, err := l.ask(ctx, in)
	if err != nil {
		l.log().Info("extract: model unavailable, heuristic only", "model", model, "err", err.Error())
		return base, nil
	}
	over.Model, over.PromptVersion = model, PromptVersion
	over.ExtractedAt = in.now().UTC().Truncate(time.Microsecond)
	return Merge(base, over), nil
}

func (l *LLM) log() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.Default()
}

// ask picks the model, sends the prompt and parses the reply. The model
// name is returned even on error so the caller can log it.
func (l *LLM) ask(ctx context.Context, in Input) (Facts, string, error) {
	if l.Client == nil {
		return Facts{}, "", errors.New("extract: no chat client")
	}
	model := strings.TrimSpace(l.Model)
	if model == "" {
		names, err := l.Client.Models(ctx)
		if err != nil {
			return Facts{}, "", err
		}
		if len(names) == 0 {
			return Facts{}, "", ErrNoModel
		}
		model = names[0]
	}
	now := in.now()
	zone, _ := now.Zone()
	text := in.Text
	if r := []rune(text); len(r) > llmMaxTextRunes {
		text = string(r[:llmMaxTextRunes])
	}
	user := "TITLE: " + strings.TrimSpace(in.Title) + "\n\nTEXT:\n" + text
	msgs := []Message{
		{Role: "system", Content: fmt.Sprintf(systemPrompt, now.Format(time.RFC3339), zone)},
		{Role: "user", Content: user},
	}
	content, err := l.Client.Chat(ctx, model, msgs, true)
	if err != nil {
		return Facts{}, model, err
	}
	facts, err := parseReply(content, in, now)
	return facts, model, err
}

// llmReply is the loosely-typed shape the model answers with: timestamps as
// strings (validated below) and every field optional.
type llmReply struct {
	SuggestedTitle string `json:"suggestedTitle"`
	Dates          []struct {
		Text       string   `json:"text"`
		At         string   `json:"at"`
		Confidence *float64 `json:"confidence"`
	} `json:"dates"`
	Deadlines []struct {
		Text       string   `json:"text"`
		At         string   `json:"at"`
		Confidence *float64 `json:"confidence"`
	} `json:"deadlines"`
	People []struct {
		Name       string   `json:"name"`
		Email      string   `json:"email"`
		Confidence *float64 `json:"confidence"`
	} `json:"people"`
	Links []struct {
		URL   string `json:"url"`
		Label string `json:"label"`
	} `json:"links"`
}

// parseReply validates the model's JSON against the text it was given:
// every email and URL must appear verbatim, every name at least loosely,
// every timestamp must parse. What fails is dropped, not the whole reply.
func parseReply(content string, in Input, now time.Time) (Facts, error) {
	content = stripFences(content)
	var r llmReply
	if err := json.Unmarshal([]byte(content), &r); err != nil {
		return Facts{}, fmt.Errorf("extract: model reply is not the expected JSON: %w", err)
	}
	hay := strings.ToLower(in.Title + "\n" + in.Text)
	f := Facts{SuggestedTitle: r.SuggestedTitle}
	conf := func(p *float64) float64 {
		if p == nil {
			return llmDefaultConfidence
		}
		return min(clamp(*p), LLMMax)
	}
	for _, d := range r.Dates {
		if at, ok := parseAt(d.At, now); ok {
			f.Dates = append(f.Dates, Date{Text: d.Text, At: at, Confidence: conf(d.Confidence)})
		}
	}
	for _, d := range r.Deadlines {
		if at, ok := parseAt(d.At, now); ok {
			f.Deadlines = append(f.Deadlines, Date{Text: d.Text, At: at, Confidence: conf(d.Confidence)})
		}
	}
	for _, p := range r.People {
		email := strings.ToLower(strings.TrimSpace(p.Email))
		if email != "" && !strings.Contains(hay, email) {
			email = "" // invented; keep the name if that at least is real
		}
		name := strings.TrimSpace(p.Name)
		if name != "" && !strings.Contains(hay, strings.ToLower(name)) {
			// a model that "helpfully" completes "Jane" to "Jane Smith" —
			// accept it only if every word of the name is in the text
			for _, w := range strings.Fields(strings.ToLower(name)) {
				if !strings.Contains(hay, w) {
					name = ""
					break
				}
			}
		}
		if name == "" && email == "" {
			continue
		}
		f.People = append(f.People, Person{Name: name, Email: email, Confidence: conf(p.Confidence)})
	}
	for _, l := range r.Links {
		u := strings.TrimSpace(l.URL)
		if u == "" || !strings.Contains(hay, strings.ToLower(u)) {
			continue
		}
		f.Links = append(f.Links, Link{URL: u, Label: l.Label})
	}
	f.Normalize()
	return f, nil
}

// parseAt accepts RFC3339, a date-time without zone (read in now's zone) or
// a bare date.
func parseAt(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// stripFences removes a ```json … ``` wrapper some models add despite the
// format hint, and any prose before the first brace.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if i := strings.LastIndex(s, "}"); i >= 0 && i < len(s)-1 {
		s = s[:i+1]
	}
	return s
}
