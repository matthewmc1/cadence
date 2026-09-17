package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/extract"
	"github.com/cadence/server/internal/store"
	"github.com/cadence/server/internal/store/memory"
)

const captureText = `Subject: Q3 numbers

Hi Jane Doe — can you send the Q3 numbers by Friday? Deck: https://docs.example.com/q3
Thanks, Priya (priya@example.com)`

// capture posts a manual capture as tenant t1 and returns the created row.
func capture(t *testing.T, s *Server, text string, links []domain.Link) domain.Signal {
	t.Helper()
	in := map[string]any{"kind": "email", "text": text}
	if links != nil {
		in["links"] = links
	}
	body, _ := json.Marshal(in)
	req := withIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/signals", strings.NewReader(string(body))))
	rr := httptest.NewRecorder()
	s.handleCreateSignal(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("capture: %d %s", rr.Code, rr.Body.String())
	}
	var sig domain.Signal
	if err := json.Unmarshal(rr.Body.Bytes(), &sig); err != nil {
		t.Fatal(err)
	}
	return sig
}

// waitExtracted polls until the row's extracted carries a model (the
// background worker ran) or the deadline passes.
func waitExtracted(t *testing.T, st store.Store, id string) extract.Facts {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sig, err := st.GetSignal(context.Background(), "t1", id)
		if err != nil {
			t.Fatal(err)
		}
		var f extract.Facts
		if err := json.Unmarshal(sig.Extracted, &f); err == nil && f.Model != "" {
			return f
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background extraction never landed")
	return extract.Facts{}
}

func auditKinds(t *testing.T, st store.Store) []string {
	t.Helper()
	page, err := st.ListAudit(context.Background(), "t1", store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range page.Items {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func TestExtractRouteHeuristicWhenNoOllama(t *testing.T) {
	st := memory.New()
	s := New(st, Options{}) // no OLLAMA_URL ⇒ heuristic
	sig := capture(t, s, captureText, []domain.Link{{Label: "seed", URL: "https://seed.example/x"}})
	waitExtracted(t, st, sig.ID) // capture-time extraction

	req := withIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/signals/"+sig.ID+"/extract", nil))
	req.SetPathValue("id", sig.ID)
	rr := httptest.NewRecorder()
	s.handleExtractSignal(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Signal    domain.Signal `json:"signal"`
		Extracted extract.Facts `json:"extracted"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	f := out.Extracted
	if f.Model != extract.HeuristicModel || f.PromptVersion != extract.HeuristicPromptVersion {
		t.Errorf("model = %q %q", f.Model, f.PromptVersion)
	}
	if f.SuggestedTitle != "Q3 numbers" {
		t.Errorf("title = %q", f.SuggestedTitle)
	}
	var names, urls []string
	for _, p := range f.People {
		names = append(names, p.Name+"|"+p.Email)
	}
	for _, l := range f.Links {
		urls = append(urls, l.URL)
	}
	if !strings.Contains(strings.Join(names, ","), "Jane Doe|") || !strings.Contains(strings.Join(names, ","), "priya@example.com") {
		t.Errorf("people = %v", names)
	}
	// the manual capture's seeded link survives alongside the found one
	if !strings.Contains(strings.Join(urls, ","), "https://seed.example/x") || !strings.Contains(strings.Join(urls, ","), "https://docs.example.com/q3") {
		t.Errorf("links = %v", urls)
	}
	if len(f.Deadlines) == 0 || !strings.EqualFold(f.Deadlines[0].Text, "by Friday") {
		t.Errorf("deadlines = %+v", f.Deadlines)
	}

	// stored on the row, never with the body; the signal's version moved
	if out.Signal.Version < 2 {
		t.Errorf("signal version = %d", out.Signal.Version)
	}
	if strings.Contains(string(out.Signal.Extracted), "can you send") {
		t.Errorf("extracted carries the body: %s", out.Signal.Extracted)
	}
	stored, _ := st.GetSignal(context.Background(), "t1", sig.ID)
	if !strings.Contains(string(stored.Extracted), `"model":"heuristic"`) {
		t.Errorf("stored extracted = %s", stored.Extracted)
	}

	// one ai.extract row per run (capture-time + explicit), with no content
	n := 0
	page, _ := st.ListAudit(context.Background(), "t1", store.Page{})
	for _, e := range page.Items {
		if e.Kind == domain.AuditAIExtract {
			n++
			if !strings.Contains(string(e.Detail), `"signalId":"`+sig.ID+`"`) || !strings.Contains(string(e.Detail), `"promptVersion":"heuristic-v1"`) {
				t.Errorf("audit detail = %s", e.Detail)
			}
			if strings.Contains(string(e.Detail), "Jane") || strings.Contains(string(e.Detail), "example.com") {
				t.Errorf("audit detail carries content: %s", e.Detail)
			}
			if e.ActorID == nil || *e.ActorID != "u1" {
				t.Errorf("audit actor = %v", e.ActorID)
			}
		}
	}
	if n != 2 {
		t.Errorf("ai.extract audit rows = %d (%v)", n, auditKinds(t, st))
	}

	// unknown / other-tenant signal is a 404
	req = withIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/signals/nope/extract", nil))
	req.SetPathValue("id", "nope")
	rr = httptest.NewRecorder()
	s.handleExtractSignal(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown signal: status = %d", rr.Code)
	}
}

func TestExtractRouteUsesOllamaAndFallsBack(t *testing.T) {
	calls := 0
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"gemma3:4b"}]}`))
		case "/api/chat":
			calls++
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["format"] != "json" || in["stream"] != false {
				t.Errorf("chat body = %v", in)
			}
			if calls == 1 {
				_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{\"suggestedTitle\":\"Send Q3 numbers\",\"dates\":[],\"deadlines\":[{\"text\":\"by Friday\",\"at\":\"2026-09-11T17:00:00Z\",\"confidence\":0.9}],\"people\":[{\"name\":\"Jane Doe\",\"confidence\":0.9}],\"links\":[]}"},"done":true}`))
				return
			}
			http.Error(w, `{"error":"model is busy"}`, http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ollama.Close()

	st := memory.New()
	s := New(st, Options{OllamaURL: ollama.URL})
	sig := capture(t, s, captureText, nil)
	f := waitExtracted(t, st, sig.ID)
	if f.Model != "gemma3:4b" || f.PromptVersion != extract.PromptVersion || f.SuggestedTitle != "Send Q3 numbers" {
		t.Errorf("llm facts = %q %q %q", f.Model, f.PromptVersion, f.SuggestedTitle)
	}
	found := false
	for _, p := range f.People {
		if p.Name == "Jane Doe" && p.Confidence == 0.9 {
			found = true
		}
	}
	if !found {
		t.Errorf("model confidence not kept: %+v", f.People)
	}

	// second run: Ollama errors ⇒ heuristic result, still 200, still stored
	req := withIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/signals/"+sig.ID+"/extract", nil))
	req.SetPathValue("id", sig.ID)
	rr := httptest.NewRecorder()
	s.handleExtractSignal(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"model":"heuristic"`) {
		t.Errorf("fallback not used: %s", rr.Body.String())
	}
	if calls != 2 {
		t.Errorf("ollama chat calls = %d", calls)
	}
}

func TestExtractQueueNeverBlocksAndWorkersSurvive(t *testing.T) {
	st := memory.New()
	s := New(st, Options{})
	// More jobs than the queue holds, all for a signal that does not exist.
	// Two things must hold: enqueue returns at once every time (a paste storm
	// never stalls the paste box), and the workers survive every one of those
	// failures — a worker that died on the first ErrNotFound would leave the
	// queue undrained and the next real capture unextracted.
	done := make(chan struct{})
	go func() {
		for i := 0; i < extractQueueCap*3; i++ {
			s.enqueueExtract(extractJob{tenantID: "t1", actorID: "u1", signalID: "missing"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueueExtract blocked")
	}

	// drain: the workers chew through the queue rather than dying on it
	deadline := time.Now().Add(5 * time.Second)
	for len(s.extractQueue) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(s.extractQueue); n != 0 {
		t.Fatalf("queue still holds %d job(s) — the workers stopped draining it", n)
	}
	// nothing was audited for a signal that never existed
	for _, k := range auditKinds(t, st) {
		if k == domain.AuditAIExtract {
			t.Errorf("ai.extract row for a missing signal: %v", auditKinds(t, st))
			break
		}
	}
	// and a real capture still gets extracted by those same workers
	sig := capture(t, s, captureText, nil)
	if f := waitExtracted(t, st, sig.ID); f.Model != extract.HeuristicModel {
		t.Errorf("after the storm, model = %q", f.Model)
	}
}

// TestExtractKeepsCorrectedFacts is the promise behind the tick on a chip:
// once a human has rewritten a fact, no later extraction may take it back.
func TestExtractKeepsCorrectedFacts(t *testing.T) {
	st := memory.New()
	s := New(st, Options{})
	sig := capture(t, s, captureText, nil)
	facts := waitExtracted(t, st, sig.ID)
	if len(facts.Deadlines) == 0 {
		t.Fatalf("nothing to correct: %+v", facts)
	}

	// the client's chip editor: same entry, new text, corrected: true
	corrected := facts
	corrected.Deadlines[0].Text = "by Friday the 19th"
	corrected.Deadlines[0].Corrected = true
	blob, _ := json.Marshal(corrected)
	var patch map[string]any
	if err := json.Unmarshal(blob, &patch); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"extracted": patch})
	req := withIdentity(httptest.NewRequest(http.MethodPatch, "/api/v1/signals/"+sig.ID, strings.NewReader(string(body))))
	req.SetPathValue("id", sig.ID)
	rr := httptest.NewRecorder()
	s.handleUpdateSignal(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH extracted: %d %s", rr.Code, rr.Body.String())
	}

	// re-extract: the model reads "by Friday" again, and must not win
	req = withIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/signals/"+sig.ID+"/extract", nil))
	req.SetPathValue("id", sig.ID)
	rr = httptest.NewRecorder()
	s.handleExtractSignal(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-extract: %d %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Extracted extract.Facts `json:"extracted"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, d := range out.Extracted.Deadlines {
		if d.Text == "by Friday the 19th" && d.Corrected {
			kept = true
		}
		if strings.EqualFold(d.Text, "by Friday") {
			t.Errorf("the model's original reading came back alongside the correction: %+v", out.Extracted.Deadlines)
		}
	}
	if !kept {
		t.Fatalf("correction lost: %+v", out.Extracted.Deadlines)
	}
	// …and it is what the row holds, not just what the response said
	stored, _ := st.GetSignal(context.Background(), "t1", sig.ID)
	if !strings.Contains(string(stored.Extracted), `"by Friday the 19th"`) || !strings.Contains(string(stored.Extracted), `"corrected":true`) {
		t.Errorf("stored extracted = %s", stored.Extracted)
	}
}
