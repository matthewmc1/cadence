package httpapi

import (
	"bytes"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strings"
	"time"
)

/* ------------------------------ embedded SPA ------------------------------ */

// spa serves the web app that server/internal/web embeds, so a private
// deployment is one binary on one origin: the API under /api/, the app
// everywhere else. Files that exist are served as-is (hashed /assets/ with
// an immutable cache lifetime); anything else falls back to index.html so
// client-side routes (/plan, /auth?token=…) deep-link correctly.
type spa struct {
	fsys     fs.FS
	files    http.Handler
	index    []byte
	indexMod time.Time
}

func newSPA(fsys fs.FS) (*spa, error) {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, err
	}
	return &spa{
		fsys:     fsys,
		files:    http.FileServerFS(noListFS{fsys}),
		index:    index,
		indexMod: time.Now(), // embedded files carry no mtime; the binary's start is a fair proxy
	}, nil
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, errBody{errPayload{Message: "method not allowed", Code: "method_not_allowed"}})
		return
	}
	p := path.Clean("/" + r.URL.Path)
	// Unregistered API / model paths are API 404s, never the app shell — a
	// misspelt endpoint should not come back as 200 text/html.
	if p == "/api" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/models/") {
		writeJSON(w, http.StatusNotFound, errBody{errPayload{Message: "not found", Code: "not_found"}})
		return
	}
	if rel := strings.TrimPrefix(p, "/"); rel != "" {
		if st, err := fs.Stat(s.spa.fsys, rel); err == nil && !st.IsDir() {
			if strings.HasPrefix(rel, "assets/") {
				// Vite content-hashes everything under assets/, so a URL never
				// changes meaning: cache forever.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			s.spa.files.ServeHTTP(w, r)
			return
		}
	}
	// SPA fallback. index.html references the hashed assets, so it must be
	// revalidated on every load or a deploy would strand clients on old chunks.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", s.spa.indexMod, bytes.NewReader(s.spa.index))
}

/* ---------------------------- security headers ---------------------------- */

// webllmUpstream are the hosts WebLLM's loader fetches weights + wasm from
// when the server does not host them (CADENCE_MODEL_DIR unset). HuggingFace
// redirects LFS downloads to its CDN hosts, which the browser also checks
// against connect-src, hence the wildcards. With CADENCE_MODEL_DIR set they
// are omitted and connect-src is same-origin only.
const webllmUpstream = " https://huggingface.co https://*.huggingface.co https://*.hf.co https://raw.githubusercontent.com"

// secureHeaders applies the same hardening headers to every response, API and
// app alike (the worker script under /assets/ inherits the CSP of its own
// response, so assets need it as much as index.html does).
func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp(r))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY") // frame-ancestors for browsers that predate CSP2
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// csp builds the Content-Security-Policy for one response.
//
//   - script-src keeps 'wasm-unsafe-eval': WebLLM compiles model kernels to
//     WebAssembly at runtime. No 'unsafe-eval', no 'unsafe-inline' — Vite's
//     production index.html carries no inline script.
//   - style-src allows inline *attributes* only (React sets style={{…}});
//     style-src-elem stays 'self' so an injected <style> element is refused.
//   - connect-src is same-origin (+ the explicit ws/wss form of this host for
//     browsers that don't fold WebSocket into 'self'), plus WebLLM's upstream
//     hosts only while the server isn't hosting the artifacts itself.
//   - worker-src 'self' blob: — the WebLLM worker is a same-origin chunk;
//     blob: is kept for the module-worker shim some browsers still need.
func (s *Server) csp(r *http.Request) string {
	var b strings.Builder
	b.WriteString("default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; ")
	b.WriteString("script-src 'self' 'wasm-unsafe-eval'; worker-src 'self' blob:; ")
	b.WriteString("style-src 'self' 'unsafe-inline'; style-src-elem 'self'; style-src-attr 'unsafe-inline'; ")
	// data: on img/font: the favicon is a data: SVG and Vite inlines assets
	// under 4 KB (one small font subset) as data: URIs. Neither can exfiltrate.
	b.WriteString("img-src 'self' data:; font-src 'self' data:; ")
	b.WriteString("connect-src 'self'")
	if host := cspHost(r.Host); host != "" {
		b.WriteString(" ws://" + host + " wss://" + host)
	}
	if s.modelDir == "" {
		b.WriteString(webllmUpstream)
	}
	return b.String()
}

// cspHost returns r.Host if it is a plain host[:port] safe to splice into a
// header value; "" otherwise (the 'self' source still covers most browsers).
func cspHost(host string) string {
	if host == "" {
		return ""
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		if net.ParseIP(strings.Trim(h, "[]")) == nil && !validDNSName(h) {
			return ""
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return ""
			}
		}
		return host
	}
	if net.ParseIP(host) != nil || validDNSName(host) {
		return host
	}
	return ""
}

func validDNSName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, c := range h {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}
