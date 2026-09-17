package httpapi

// Server-side AI gateway.
//
// The browser used to talk to Ollama directly at localhost:11434, which made
// per-tenant policy and egress logging impossible to enforce. Every model call
// now goes through the Cadence server:
//
//   - GET  /api/v1/ai/status — what providers this deployment offers.
//   - POST /api/v1/ai/chat   — a single non-streaming chat turn, forwarded to
//     OLLAMA_URL/api/chat. One structured audit line per call.
//   - GET  /models/          — read-only static hosting for WebLLM artifacts
//     (weights + wasm) when CADENCE_MODEL_DIR is set, so in-browser inference
//     never has to reach HuggingFace / GitHub.
//
// Policy: `local` (default) allows only self-hosted providers. `local+cloud` is
// accepted and reported so clients can plan for it, but no cloud provider is
// wired yet — nothing leaves the deployment under either policy today.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cadence/server/internal/domain"
)

const (
	AIPolicyLocal      = "local"
	AIPolicyLocalCloud = "local+cloud"

	aiChatTimeout   = 10 * time.Minute // a 7B model on CPU can take a while
	aiStatusTimeout = 3 * time.Second
	aiResponseCap   = 4 << 20 // bytes we will read back from the provider
	aiMaxMessages   = 64
	aiMaxPromptRune = 200_000
)

// ParseAIPolicy validates CADENCE_AI_POLICY. Empty means the default (local).
func ParseAIPolicy(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", AIPolicyLocal:
		return AIPolicyLocal, nil
	case AIPolicyLocalCloud:
		return AIPolicyLocalCloud, nil
	}
	return "", fmt.Errorf("CADENCE_AI_POLICY %q: want local or local+cloud", s)
}

// ParseOllamaURL validates OLLAMA_URL. Empty means Ollama is disabled.
func ParseOllamaURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("OLLAMA_URL %q: want http(s)://host[:port]", s)
	}
	return strings.TrimRight(s, "/"), nil
}

// AIAuditRecord is one provider call, as seen by the gateway. It carries no
// prompt content — only enough to answer "who sent how much to which model".
type AIAuditRecord struct {
	TenantID    string
	UserID      string
	Provider    string // "ollama" for now
	Model       string
	PromptChars int
	OK          bool
	Err         string // empty when OK
	Duration    time.Duration
}

// AuditSink receives one record per AI call. The default writes a structured
// slog line; swap in a store-backed sink once the audit_log table lands.
type AuditSink interface {
	RecordAI(ctx context.Context, rec AIAuditRecord)
}

// SlogAudit is the default AuditSink: one "ai call" line per request.
type SlogAudit struct{ Log *slog.Logger }

func (a SlogAudit) RecordAI(_ context.Context, rec AIAuditRecord) {
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	attrs := []any{
		"tenantId", rec.TenantID, "userId", rec.UserID,
		"provider", rec.Provider, "model", rec.Model,
		"promptChars", rec.PromptChars, "ok", rec.OK,
		"durationMs", rec.Duration.Milliseconds(),
	}
	if !rec.OK {
		attrs = append(attrs, "err", rec.Err)
	}
	log.Info("ai call", attrs...)
}

/* -------------------------------- status -------------------------------- */

type aiStatus struct {
	Ollama struct {
		Configured bool     `json:"configured"`
		Reachable  bool     `json:"reachable"`
		Models     []string `json:"models"`
	} `json:"ollama"`
	WebLLM struct {
		LocalModels bool     `json:"localModels"`
		Models      []string `json:"models"`
	} `json:"webllm"`
	Policy string `json:"policy"`
}

// GET /ai/status — providers available to this deployment. Reachability is
// probed live (short timeout) so the picker can say "not running" honestly.
func (s *Server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	var st aiStatus
	st.Policy = s.aiPolicy
	st.Ollama.Models = []string{}
	st.WebLLM.Models = []string{}

	if s.ollamaURL != "" {
		st.Ollama.Configured = true
		if names, err := s.ollamaTags(r.Context()); err == nil {
			st.Ollama.Reachable = true
			st.Ollama.Models = names
		}
	}
	if s.modelDir != "" {
		st.WebLLM.LocalModels = true
		st.WebLLM.Models = s.localWebLLMModels()
	}
	writeJSON(w, http.StatusOK, st)
}

// ollamaTags lists the models installed on the Ollama server.
func (s *Server) ollamaTags(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, aiStatusTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.ollamaURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	res, err := s.aiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama tags: HTTP %d", res.StatusCode)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, aiResponseCap)).Decode(&body); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	return names, nil
}

// localWebLLMModels lists the model ids hosted under modelDir: one directory
// per model id, laid out like a HuggingFace clone (mlc-chat-config.json at its
// root). `lib/` holds the wasm model libraries and is not a model.
func (s *Server) localWebLLMModels() []string {
	entries, err := os.ReadDir(s.modelDir)
	if err != nil {
		return []string{}
	}
	ids := []string{}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "lib" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.modelDir, e.Name(), "mlc-chat-config.json")); err == nil {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

/* --------------------------------- chat --------------------------------- */

type aiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiChatInput struct {
	Model    string          `json:"model"`
	Messages []aiChatMessage `json:"messages"`
	Format   string          `json:"format,omitempty"` // "" | "json"
	// Temperature is optional; the scheduler wants low variance (0.2).
	Temperature *float64 `json:"temperature,omitempty"`
}

func (in *aiChatInput) validate() (promptChars int, err error) {
	in.Model = strings.TrimSpace(in.Model)
	// Ollama names look like `gemma3:4b` or `hf.co/user/model:tag` — no whitespace.
	if in.Model == "" || len(in.Model) > 128 || strings.ContainsAny(in.Model, " \t\r\n") {
		return 0, domain.Invalid("model", "model must be an Ollama model name, e.g. gemma3:4b")
	}
	if len(in.Messages) == 0 {
		return 0, domain.Invalid("messages", "at least one message is required")
	}
	if len(in.Messages) > aiMaxMessages {
		return 0, domain.Invalid("messages", fmt.Sprintf("at most %d messages", aiMaxMessages))
	}
	for i, m := range in.Messages {
		switch m.Role {
		case "system", "user", "assistant":
		default:
			return 0, domain.Invalid(fmt.Sprintf("messages[%d].role", i), "role must be system, user or assistant")
		}
		if strings.TrimSpace(m.Content) == "" {
			return 0, domain.Invalid(fmt.Sprintf("messages[%d].content", i), "content is required")
		}
		promptChars += len([]rune(m.Content))
	}
	if promptChars > aiMaxPromptRune {
		return 0, domain.Invalid("messages", "prompt is too long")
	}
	switch in.Format {
	case "", "json":
	default:
		return 0, domain.Invalid("format", `format must be "json" when set`)
	}
	if in.Temperature != nil && (*in.Temperature < 0 || *in.Temperature > 2) {
		return 0, domain.Invalid("temperature", "temperature must be between 0 and 2")
	}
	return promptChars, nil
}

// POST /ai/chat — one non-streaming turn against Ollama. The forwarded body
// matches what the browser used to send directly (stream:false, temperature
// 0.2, optional format:"json"); the response is Ollama's JSON, verbatim.
func (s *Server) handleAIChat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in aiChatInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	promptChars, err := in.validate()
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	if s.ollamaURL == "" {
		writeJSON(w, http.StatusNotImplemented, errBody{errPayload{Message: "Ollama isn't configured on this server (set OLLAMA_URL)", Code: "ai_not_configured"}})
		return
	}

	rec := AIAuditRecord{
		TenantID: TenantID(ctx), UserID: ActorID(ctx),
		Provider: "ollama", Model: in.Model, PromptChars: promptChars,
	}
	start := time.Now()
	body, status, err := s.ollamaChat(ctx, in)
	rec.Duration = time.Since(start)
	rec.OK = err == nil
	if err != nil {
		rec.Err = err.Error()
	}
	// The audit row must land even when the request context is already gone —
	// a browser that navigated away mid-generation is exactly the call whose
	// prompt left the server and must be on record — so it is written under a
	// context detached from the request's cancellation.
	s.audit.RecordAI(context.WithoutCancel(ctx), rec)

	if err != nil {
		msg := "the model server didn't answer"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "the model took too long to answer"
		} else if status != 0 {
			msg = fmt.Sprintf("Ollama returned HTTP %d", status)
		}
		writeJSON(w, http.StatusBadGateway, errBody{errPayload{Message: msg, Code: "ai_upstream"}})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// ollamaChat forwards one turn and returns the raw response body. A non-2xx
// upstream status is returned as an error alongside the status code.
func (s *Server) ollamaChat(ctx context.Context, in aiChatInput) (body []byte, status int, err error) {
	temp := 0.2
	if in.Temperature != nil {
		temp = *in.Temperature
	}
	payload := map[string]any{
		"model":    in.Model,
		"messages": in.Messages,
		"stream":   false,
		"options":  map[string]any{"temperature": temp},
	}
	if in.Format != "" {
		payload["format"] = in.Format
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, aiChatTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ollamaURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.aiClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()

	body, err = io.ReadAll(io.LimitReader(res.Body, aiResponseCap+1))
	if err != nil {
		return nil, res.StatusCode, err
	}
	if len(body) > aiResponseCap {
		return nil, res.StatusCode, errors.New("ollama response exceeds size cap")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Ollama's error body is {"error": "..."}; keep it short in the audit line.
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return nil, res.StatusCode, fmt.Errorf("ollama HTTP %d: %s", res.StatusCode, msg)
	}
	if !json.Valid(body) {
		return nil, res.StatusCode, errors.New("ollama returned non-JSON")
	}
	return body, res.StatusCode, nil
}

/* -------------------------------- models -------------------------------- */

// GET /models/{model_id}/... — read-only hosting for WebLLM artifacts.
//
// Layout under CADENCE_MODEL_DIR mirrors a HuggingFace clone so `git clone
// https://huggingface.co/mlc-ai/<id>` drops straight in:
//
//	<dir>/<model_id>/mlc-chat-config.json, ndarray-cache.json, params_shard_*.bin, …
//	<dir>/lib/<model_lib>.wasm
//
// WebLLM appends `resolve/<branch>/` to every model URL (a HuggingFace-ism);
// that segment is stripped here so the on-disk layout stays flat.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if s.modelDir == "" {
		writeJSON(w, http.StatusNotFound, errBody{errPayload{Message: "the server doesn't host model files (set CADENCE_MODEL_DIR)", Code: "not_found"}})
		return
	}
	rel := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/models")), "/")
	if parts := strings.Split(rel, "/"); len(parts) >= 3 && parts[1] == "resolve" {
		rel = parts[0] + "/" + strings.Join(parts[3:], "/")
	}
	if rel == "" || !fs.ValidPath(rel) {
		http.NotFound(w, r)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + rel
	s.modelFiles.ServeHTTP(w, r2)
}

// noListFS serves files only: directories 404 instead of listing, and there
// is no index.html fallback. Everything under it is read-only by construction.
type noListFS struct{ fs.FS }

func (n noListFS) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || st.IsDir() {
		_ = f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

func newModelFileServer(dir string) http.Handler {
	if dir == "" {
		return http.NotFoundHandler()
	}
	return http.FileServerFS(noListFS{os.DirFS(dir)})
}
