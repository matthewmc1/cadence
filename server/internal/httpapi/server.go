// Package httpapi is Cadence's HTTP + WebSocket surface: magic-link auth,
// session-scoped identity, and a tenant-fenced API over store.Store.
package httpapi

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cadence/server/internal/store"
)

type Server struct {
	store store.Store
	log   *slog.Logger

	webURL        string
	cookieName    string
	cookieSecure  bool
	sessionTTL    time.Duration
	loginTokenTTL time.Duration
	devAuth       bool
	wsOrigins     []string
}

type Options struct {
	WebOrigins    []string
	WebURL        string
	CookieName    string
	CookieSecure  bool
	SessionTTL    time.Duration
	LoginTokenTTL time.Duration
	DevAuth       bool
	Log           *slog.Logger
}

func New(st store.Store, opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	origins := opts.WebOrigins
	if len(origins) == 0 {
		origins = []string{"localhost:*", "127.0.0.1:*"}
	}
	return &Server{
		store:         st,
		log:           log,
		webURL:        opts.WebURL,
		cookieName:    orStr(opts.CookieName, "cadence_session"),
		cookieSecure:  opts.CookieSecure,
		sessionTTL:    orDur(opts.SessionTTL, 30*24*time.Hour),
		loginTokenTTL: orDur(opts.LoginTokenTTL, 15*time.Minute),
		devAuth:       opts.DevAuth,
		wsOrigins:     origins,
	}
}

// Handler builds the routed, middleware-wrapped http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---- public ----
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/auth/request", s.handleAuthRequest)
	mux.HandleFunc("POST /api/v1/auth/verify", s.handleAuthVerify)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleAuthMe)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleAuthLogout)

	// ---- session-protected ----
	mux.Handle("POST /api/v1/auth/tokens", s.auth(s.handleCreateToken))
	mux.Handle("GET /api/v1/auth/tokens", s.auth(s.handleListTokens))
	mux.Handle("DELETE /api/v1/auth/tokens/{id}", s.auth(s.handleRevokeToken))
	mux.Handle("GET /api/v1/bootstrap", s.auth(s.handleBootstrap))
	mux.Handle("GET /api/v1/tasks", s.auth(s.handleListTasks))
	mux.Handle("POST /api/v1/tasks", s.auth(s.handleCreateTask))
	mux.Handle("GET /api/v1/tasks/{id}", s.auth(s.handleGetTask))
	mux.Handle("PATCH /api/v1/tasks/{id}", s.auth(s.handleUpdateTask))
	mux.Handle("DELETE /api/v1/tasks/{id}", s.auth(s.handleDeleteTask))
	mux.Handle("GET /api/v1/projects", s.auth(s.handleListProjects))
	mux.Handle("POST /api/v1/projects", s.auth(s.handleCreateProject))
	mux.Handle("PATCH /api/v1/projects/{id}", s.auth(s.handleUpdateProject))
	mux.Handle("DELETE /api/v1/projects/{id}", s.auth(s.handleDeleteProject))
	mux.Handle("GET /api/v1/clients", s.auth(s.handleListClients))
	mux.Handle("POST /api/v1/clients", s.auth(s.handleCreateClient))
	mux.Handle("PATCH /api/v1/clients/{id}", s.auth(s.handleUpdateClient))
	mux.Handle("DELETE /api/v1/clients/{id}", s.auth(s.handleDeleteClient))
	mux.Handle("GET /api/v1/realtime", s.auth(s.handleRealtime))

	// middleware chain (outermost first)
	var h http.Handler = mux
	h = s.resolveIdentity(h) // sets identity from the session cookie if present
	h = requestID(h)
	h = s.cors(h)
	h = s.logging(h)
	h = s.recoverer(h)
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	code := http.StatusOK
	if err := s.store.Ping(r.Context()); err != nil {
		status, code = "degraded", http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]string{"status": status, "service": "cadence"})
}

// linkBase is where magic links point back to. We use the request's Origin
// when it's an allowlisted web origin (so the link works whether the app is on
// the dev or preview port), falling back to the configured WebURL. Only
// allowlisted origins are trusted — never an arbitrary attacker-supplied one.
func (s *Server) linkBase(r *http.Request) string {
	if o := r.Header.Get("Origin"); o != "" && s.originAllowed(o) {
		return strings.TrimRight(o, "/")
	}
	return s.webURL
}

func (s *Server) originAllowed(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Host
	bare := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		bare = h
	}
	for _, p := range s.wsOrigins {
		switch {
		case p == "*":
			return true
		case strings.HasSuffix(p, ":*"):
			if bare == strings.TrimSuffix(p, ":*") {
				return true
			}
		case p == host:
			return true
		}
	}
	return false
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
func orDur(d, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d
}
