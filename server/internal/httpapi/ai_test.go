package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cadence/server/internal/store/memory"
)

type recordingAudit struct{ recs []AIAuditRecord }

func (r *recordingAudit) RecordAI(_ context.Context, rec AIAuditRecord) { r.recs = append(r.recs, rec) }

// withIdentity stamps a fake session onto the request the way resolveIdentity would.
func withIdentity(r *http.Request) *http.Request {
	ctx := context.WithValue(r.Context(), ctxTenant, "t1")
	ctx = context.WithValue(ctx, ctxActor, "u1")
	return r.WithContext(ctx)
}

func TestAIChatForwardsAndAudits(t *testing.T) {
	var got map[string]any
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gemma3:1b","message":{"role":"assistant","content":"{\"ok\":true}"},"done":true}`))
	}))
	defer ollama.Close()

	audit := &recordingAudit{}
	s := New(memory.New(), Options{OllamaURL: ollama.URL, Audit: audit})

	body := `{"model":"gemma3:1b","messages":[{"role":"system","content":"be terse"},{"role":"user","content":"hi"}],"format":"json"}`
	req := withIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/ai/chat", strings.NewReader(body)))
	rr := httptest.NewRecorder()
	s.handleAIChat(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"content":"{\"ok\":true}"`) {
		t.Fatalf("response not passed through verbatim: %s", rr.Body.String())
	}
	// forwarded shape matches what the browser used to send directly
	if got["stream"] != false || got["format"] != "json" || got["model"] != "gemma3:1b" {
		t.Fatalf("forwarded body = %v", got)
	}
	if opts, _ := got["options"].(map[string]any); opts["temperature"] != 0.2 {
		t.Fatalf("temperature = %v", got["options"])
	}
	if len(audit.recs) != 1 {
		t.Fatalf("audit records = %d", len(audit.recs))
	}
	rec := audit.recs[0]
	if rec.TenantID != "t1" || rec.UserID != "u1" || rec.Provider != "ollama" || rec.Model != "gemma3:1b" || !rec.OK || rec.PromptChars != len("be terse")+len("hi") {
		t.Fatalf("audit = %+v", rec)
	}
}

func TestAIChatValidationAndNotConfigured(t *testing.T) {
	audit := &recordingAudit{}
	s := New(memory.New(), Options{Audit: audit}) // no OLLAMA_URL

	cases := map[string]int{
		`{"model":"","messages":[{"role":"user","content":"x"}]}`:                 http.StatusBadRequest,
		`{"model":"m","messages":[]}`:                                             http.StatusBadRequest,
		`{"model":"m","messages":[{"role":"tool","content":"x"}]}`:                http.StatusBadRequest,
		`{"model":"m","messages":[{"role":"user","content":"x"}],"format":"xml"}`: http.StatusBadRequest,
		`{"model":"m","messages":[{"role":"user","content":"x"}],"stream":true}`:  http.StatusBadRequest, // unknown field
		`{"model":"m","messages":[{"role":"user","content":"x"}]}`:                http.StatusNotImplemented,
	}
	for body, want := range cases {
		req := withIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/ai/chat", strings.NewReader(body)))
		rr := httptest.NewRecorder()
		s.handleAIChat(rr, req)
		if rr.Code != want {
			t.Errorf("%s: status = %d, want %d (%s)", body, rr.Code, want, rr.Body.String())
		}
	}
	if len(audit.recs) != 0 {
		t.Fatalf("nothing reached a provider, but audit records = %d", len(audit.recs))
	}
}

func TestAIChatUpstreamErrorIsAudited(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model 'nope' not found"}`, http.StatusNotFound)
	}))
	defer ollama.Close()
	audit := &recordingAudit{}
	s := New(memory.New(), Options{OllamaURL: ollama.URL, Audit: audit})

	req := withIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/ai/chat",
		strings.NewReader(`{"model":"nope","messages":[{"role":"user","content":"x"}]}`)))
	rr := httptest.NewRecorder()
	s.handleAIChat(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(audit.recs) != 1 || audit.recs[0].OK || !strings.Contains(audit.recs[0].Err, "HTTP 404") {
		t.Fatalf("audit = %+v", audit.recs)
	}
}

func TestAIStatus(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5:7b"},{"name":"gemma3:4b"}]}`))
	}))
	defer ollama.Close()

	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "gemma3-1b-it-q4f16_1-MLC"))
	mustWrite(t, filepath.Join(dir, "gemma3-1b-it-q4f16_1-MLC", "mlc-chat-config.json"), "{}")
	mustMkdir(t, filepath.Join(dir, "incomplete")) // no config ⇒ not advertised
	mustMkdir(t, filepath.Join(dir, "lib"))

	s := New(memory.New(), Options{OllamaURL: ollama.URL, ModelDir: dir, AIPolicy: AIPolicyLocalCloud})
	rr := httptest.NewRecorder()
	s.handleAIStatus(rr, withIdentity(httptest.NewRequest(http.MethodGet, "/api/v1/ai/status", nil)))

	var st aiStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Ollama.Configured || !st.Ollama.Reachable || strings.Join(st.Ollama.Models, ",") != "gemma3:4b,qwen2.5:7b" {
		t.Fatalf("ollama = %+v", st.Ollama)
	}
	if !st.WebLLM.LocalModels || strings.Join(st.WebLLM.Models, ",") != "gemma3-1b-it-q4f16_1-MLC" {
		t.Fatalf("webllm = %+v", st.WebLLM)
	}
	if st.Policy != AIPolicyLocalCloud {
		t.Fatalf("policy = %q", st.Policy)
	}

	// Unconfigured deployment: nothing reachable, nothing hosted, default policy.
	s = New(memory.New(), Options{})
	rr = httptest.NewRecorder()
	s.handleAIStatus(rr, withIdentity(httptest.NewRequest(http.MethodGet, "/api/v1/ai/status", nil)))
	if !strings.Contains(rr.Body.String(), `"configured":false`) || !strings.Contains(rr.Body.String(), `"localModels":false`) || !strings.Contains(rr.Body.String(), `"policy":"local"`) {
		t.Fatalf("status = %s", rr.Body.String())
	}
}

func TestModelsServing(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "m1"))
	mustWrite(t, filepath.Join(dir, "m1", "mlc-chat-config.json"), `{"a":1}`)
	mustMkdir(t, filepath.Join(dir, "lib"))
	mustWrite(t, filepath.Join(dir, "lib", "m1.wasm"), "wasm")
	mustWrite(t, filepath.Join(filepath.Dir(dir), "outside.txt"), "secret")

	s := New(memory.New(), Options{ModelDir: dir})
	get := func(p string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.handleModels(rr, withIdentity(httptest.NewRequest(http.MethodGet, p, nil)))
		return rr
	}
	if rr := get("/models/m1/mlc-chat-config.json"); rr.Code != 200 || rr.Body.String() != `{"a":1}` {
		t.Fatalf("plain: %d %s", rr.Code, rr.Body.String())
	}
	// WebLLM appends resolve/main/ to the model URL; the segment is dropped.
	if rr := get("/models/m1/resolve/main/mlc-chat-config.json"); rr.Code != 200 {
		t.Fatalf("resolve/main: %d", rr.Code)
	}
	if rr := get("/models/lib/m1.wasm"); rr.Code != 200 || rr.Body.String() != "wasm" {
		t.Fatalf("lib: %d", rr.Code)
	}
	for _, p := range []string{"/models/", "/models/m1", "/models/m1/", "/models/../outside.txt", "/models/m1/../../outside.txt", "/models/nope.txt"} {
		if rr := get(p); rr.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, rr.Code)
		}
	}

	// Not configured ⇒ 404 with a hint, never a directory of the cwd.
	s = New(memory.New(), Options{})
	if rr := get("/models/m1/mlc-chat-config.json"); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "CADENCE_MODEL_DIR") {
		t.Fatalf("unset: %d %s", rr.Code, rr.Body.String())
	}
}

func TestParseAIConfig(t *testing.T) {
	for in, want := range map[string]string{"": "local", "local": "local", "LOCAL+CLOUD": "local+cloud"} {
		if got, err := ParseAIPolicy(in); err != nil || got != want {
			t.Errorf("ParseAIPolicy(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseAIPolicy("cloud"); err == nil {
		t.Error("ParseAIPolicy(cloud) should fail")
	}
	if got, err := ParseOllamaURL("http://localhost:11434/"); err != nil || got != "http://localhost:11434" {
		t.Errorf("ParseOllamaURL = %q, %v", got, err)
	}
	for _, bad := range []string{"localhost:11434", "ftp://x", "http://"} {
		if _, err := ParseOllamaURL(bad); err == nil {
			t.Errorf("ParseOllamaURL(%q) should fail", bad)
		}
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
