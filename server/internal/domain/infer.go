package domain

import "strings"

// Infer reads a task's shape from its title — the server-side mirror of the
// web app's "Cadence sees" step, so tasks created through the API (or a future
// integration) still land with a sensible kind and effort.
func Infer(title string) (Kind, int) {
	t := strings.ToLower(title)
	has := func(hints ...string) bool {
		for _, h := range hints {
			if strings.Contains(t, h) {
				return true
			}
		}
		return false
	}

	switch {
	case has("1:1", "meet", "call", "sync", "standup", "interview", "review with", "demo", "catch up"):
		return KindMeet, 30
	case has("draft", "write", "design", "build", "refactor", "spec", "architect", "strategy", "deck", "outline", "prototype", "analy", "research", "model"):
		return KindDeep, 120
	case has("email", "invoice", "expense", "schedule", "book", "submit", "file", "contract", "legal", "metrics", "triage", "inbox"):
		return KindAdmin, 30
	case has("run", "gym", "lunch", "walk", "doctor", "family", "birthday", "groceries"):
		return KindPersonal, 60
	default:
		return KindLight, 40
	}
}
