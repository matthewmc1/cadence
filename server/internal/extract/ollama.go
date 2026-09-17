package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Ollama is the Chatter over an Ollama server — the same /api/chat and
// /api/tags the HTTP gateway (httpapi/ai.go) forwards to, kept separate so
// the extractor has no dependency on the request layer. Deadlines are the
// caller's, via ctx.
type Ollama struct {
	BaseURL string       // e.g. http://localhost:11434 (no trailing slash)
	HTTP    *http.Client // nil ⇒ http.DefaultClient
}

// ollamaResponseCap bounds what is read back from the provider.
const ollamaResponseCap = 4 << 20

func (o Ollama) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return http.DefaultClient
}

func (o Ollama) Chat(ctx context.Context, model string, messages []Message, jsonFormat bool) (string, error) {
	payload := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   false,
		"options":  map[string]any{"temperature": 0},
	}
	if jsonFormat {
		payload["format"] = "json"
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := o.client().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, ollamaResponseCap+1))
	if err != nil {
		return "", err
	}
	if len(body) > ollamaResponseCap {
		return "", errors.New("ollama response exceeds size cap")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return "", fmt.Errorf("ollama HTTP %d: %s", res.StatusCode, msg)
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("ollama returned non-JSON: %w", err)
	}
	return out.Message.Content, nil
}

func (o Ollama) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(o.BaseURL, "/")+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	res, err := o.client().Do(req)
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
	if err := json.NewDecoder(io.LimitReader(res.Body, ollamaResponseCap)).Decode(&body); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	return names, nil
}
