package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cadence/server/internal/store/memory"
)

func testSPA(t *testing.T, modelDir string) http.Handler {
	t.Helper()
	dist := fstest.MapFS{
		"index.html":                  {Data: []byte("<!doctype html><title>Cadence</title><div id=root></div>")},
		"assets/index-abc123.js":      {Data: []byte("console.log('hi')")},
		"assets/font-abc123.woff2":    {Data: []byte("wOF2")},
		"assets/llm.worker-abc123.js": {Data: []byte("self.onmessage=()=>{}")},
	}
	return New(memory.New(), Options{Web: dist, ModelDir: modelDir}).Handler()
}

func TestSPAFallbackAndAssets(t *testing.T) {
	h := testSPA(t, "")

	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr
	}

	// "/" and any client route serve index.html, never cached.
	for _, p := range []string{"/", "/plan", "/auth?token=abc", "/deep/link"} {
		rr := get(p)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "id=root") {
			t.Fatalf("%s: status %d body %q", p, rr.Code, rr.Body.String())
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s: content-type %q", p, ct)
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s: cache-control %q", p, cc)
		}
	}

	// Hashed assets: real content types, immutable caching.
	rr := get("/assets/index-abc123.js")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("js asset: %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("js asset cache-control %q", cc)
	}
	rr = get("/assets/font-abc123.woff2")
	if ct := rr.Header().Get("Content-Type"); ct != "font/woff2" {
		t.Fatalf("woff2 content-type %q (nosniff would refuse it)", ct)
	}

	// Unregistered API / model paths are API 404s, not the app shell.
	for _, p := range []string{"/api/v1/nope", "/api", "/models/x/y"} {
		rr := get(p)
		if rr.Code == http.StatusOK && strings.Contains(rr.Body.String(), "id=root") {
			t.Fatalf("%s served index.html", p)
		}
	}
	// Registered API routes still win over the catch-all.
	if rr := get("/api/v1/health"); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"status"`) {
		t.Fatalf("health via SPA handler: %d %s", rr.Code, rr.Body.String())
	}
	// Directory paths are not listed.
	if rr := get("/assets/"); rr.Code == http.StatusOK && strings.Contains(rr.Body.String(), "index-abc123") {
		t.Fatalf("directory listing leaked: %s", rr.Body.String())
	}
}

func TestSecurityHeadersAndCSP(t *testing.T) {
	// Without a model dir the CSP admits WebLLM's upstream hosts; with one it is
	// same-origin only.
	for _, tc := range []struct {
		modelDir string
		upstream bool
	}{{"", true}, {t.TempDir(), false}} {
		h := testSPA(t, tc.modelDir)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "cadence.internal:8088"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		csp := rr.Header().Get("Content-Security-Policy")
		for _, want := range []string{
			"default-src 'self'",
			"script-src 'self' 'wasm-unsafe-eval'",
			"worker-src 'self' blob:",
			"frame-ancestors 'none'",
			"style-src-elem 'self'",
			"connect-src 'self' ws://cadence.internal:8088 wss://cadence.internal:8088",
		} {
			if !strings.Contains(csp, want) {
				t.Fatalf("modelDir=%q: CSP missing %q: %s", tc.modelDir, want, csp)
			}
		}
		if strings.Contains(csp, "unsafe-eval'") && !strings.Contains(csp, "'wasm-unsafe-eval'") {
			t.Fatalf("CSP allows unsafe-eval: %s", csp)
		}
		if got := strings.Contains(csp, "huggingface.co"); got != tc.upstream {
			t.Fatalf("modelDir=%q: upstream hosts in CSP = %v: %s", tc.modelDir, got, csp)
		}
		for k, v := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "same-origin",
		} {
			if rr.Header().Get(k) != v {
				t.Fatalf("%s = %q, want %q", k, rr.Header().Get(k), v)
			}
		}
		if rr.Header().Get("Permissions-Policy") == "" {
			t.Fatal("no Permissions-Policy")
		}
	}

	// A Host that isn't a clean host[:port] never reaches the header.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil; script-src *"
	rr := httptest.NewRecorder()
	testSPA(t, "").ServeHTTP(rr, req)
	if csp := rr.Header().Get("Content-Security-Policy"); strings.Contains(csp, "evil") {
		t.Fatalf("host spliced into CSP: %s", csp)
	}

	// API-only servers get the same headers.
	rr = httptest.NewRecorder()
	New(memory.New(), Options{}).Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if rr.Header().Get("Content-Security-Policy") == "" || rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("API response lacks security headers: %v", rr.Header())
	}
}
