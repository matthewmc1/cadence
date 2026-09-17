// Package httpapi is Cadence's HTTP + WebSocket surface: magic-link auth,
// session-scoped identity, and a tenant-fenced API over store.Store.
package httpapi

import (
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cadence/server/internal/mailer"
	"github.com/cadence/server/internal/store"
)

type Server struct {
	store    store.Store
	log      *slog.Logger
	mailer   mailer.Mailer
	authRate *authLimiter

	webURL        string
	cookieName    string
	cookieSecure  bool
	sessionTTL    time.Duration
	loginTokenTTL time.Duration
	devAuth       bool
	wsOrigins     []string

	// Signup policy (signup.go): who may request a magic link.
	signup      SignupPolicy
	adminEmails map[string]bool

	// auditSalt keys the audit log's ip_hash (audit.go); never empty.
	auditSalt []byte

	// Embedded web app (web.go); nil ⇒ API only.
	spa *spa

	// AI gateway (ai.go)
	ollamaURL  string       // "" ⇒ Ollama disabled
	modelDir   string       // "" ⇒ WebLLM artifacts come from the upstream CDN
	aiPolicy   string       // AIPolicyLocal | AIPolicyLocalCloud
	audit      AuditSink    // one record per provider call
	aiClient   *http.Client // outbound to Ollama; per-call deadlines via context
	modelFiles http.Handler // read-only file server over modelDir

	// Fact extraction (extract.go): the model to ask ("" ⇒ first installed)
	// and the bounded capture-time worker queue, started on first use.
	extractModel string
	extractQueue chan extractJob
	extractOnce  sync.Once
}

type Options struct {
	WebOrigins    []string
	WebURL        string
	CookieName    string
	CookieSecure  bool
	SessionTTL    time.Duration
	LoginTokenTTL time.Duration
	DevAuth       bool
	Mailer        mailer.Mailer
	Log           *slog.Logger

	// Signup is the parsed CADENCE_SIGNUP policy (zero value ⇒ invite);
	// AdminEmails may always sign in regardless of it.
	Signup      SignupPolicy
	AdminEmails []string

	// AuditSalt keys the HMAC behind audit_log.ip_hash (CADENCE_AUDIT_SALT).
	// Empty ⇒ a random per-process key, with a boot warning: hashes stay
	// irreversible but only correlate within one server run.
	AuditSalt string

	// Web is the built SPA to serve from this origin (server/internal/web);
	// nil serves the API only.
	Web fs.FS

	// AI gateway — see ai.go. OllamaURL/AIPolicy are expected pre-validated
	// (ParseOllamaURL / ParseAIPolicy); Audit defaults to SlogAudit.
	OllamaURL string
	ModelDir  string
	AIPolicy  string
	Audit     AuditSink
	// ExtractModel is the Ollama model fact extraction asks (extract.go);
	// "" ⇒ the first installed model. Ignored when OllamaURL is unset.
	ExtractModel string
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
	ml := opts.Mailer
	if ml == nil {
		ml = mailer.Dev(log, opts.DevAuth) // dev mailer: log links only
	}
	audit := opts.Audit
	if audit == nil {
		audit = SlogAudit{Log: log}
	}
	auditSalt, configured := auditSaltOrRandom(opts.AuditSalt)
	if !configured {
		log.Warn("CADENCE_AUDIT_SALT is unset: audit ip hashes use a random per-boot key and will not correlate across restarts — set it on any persistent deployment")
	}
	var app *spa
	if opts.Web != nil {
		var err error
		if app, err = newSPA(opts.Web); err != nil {
			// A dist without index.html is a broken build, not a mode: say so
			// rather than quietly serving API-only.
			log.Error("embedded web app unusable; serving API only", "err", err)
		}
	}
	return &Server{
		store:         st,
		log:           log,
		mailer:        ml,
		authRate:      newAuthLimiter(),
		webURL:        opts.WebURL,
		cookieName:    orStr(opts.CookieName, "cadence_session"),
		cookieSecure:  opts.CookieSecure,
		sessionTTL:    orDur(opts.SessionTTL, 30*24*time.Hour),
		loginTokenTTL: orDur(opts.LoginTokenTTL, 15*time.Minute),
		devAuth:       opts.DevAuth,
		wsOrigins:     origins,
		signup:        SignupPolicy{Mode: orStr(opts.Signup.Mode, SignupInvite), Domains: opts.Signup.Domains},
		adminEmails:   adminSet(opts.AdminEmails),
		auditSalt:     auditSalt,
		spa:           app,
		ollamaURL:     strings.TrimRight(opts.OllamaURL, "/"),
		modelDir:      opts.ModelDir,
		aiPolicy:      orStr(opts.AIPolicy, AIPolicyLocal),
		audit:         audit,
		aiClient:      &http.Client{},
		modelFiles:    newModelFileServer(opts.ModelDir),
		extractModel:  strings.TrimSpace(opts.ExtractModel),
		extractQueue:  make(chan extractJob, extractQueueCap),
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
	mux.Handle("GET /api/v1/requirements", s.auth(s.handleListRequirements))
	mux.Handle("POST /api/v1/requirements", s.auth(s.handleCreateRequirement))
	mux.Handle("PATCH /api/v1/requirements/{id}", s.auth(s.handleUpdateRequirement))
	mux.Handle("DELETE /api/v1/requirements/{id}", s.auth(s.handleDeleteRequirement))
	mux.Handle("GET /api/v1/audit", s.auth(s.handleListAudit)) // read-only, paged; rows are produced server-side
	// signals (signals.go): paged list + count badge, manual capture, the
	// body on its own, disposition-only PATCH; "count" is a literal so it
	// wins over {id}
	mux.Handle("GET /api/v1/signals", s.auth(s.handleListSignals))
	mux.Handle("GET /api/v1/signals/count", s.auth(s.handleCountSignals))
	mux.Handle("POST /api/v1/signals", s.auth(s.handleCreateSignal))
	mux.Handle("GET /api/v1/signals/{id}", s.auth(s.handleGetSignal))
	mux.Handle("GET /api/v1/signals/{id}/body", s.auth(s.handleGetSignalBody))
	mux.Handle("PATCH /api/v1/signals/{id}", s.auth(s.handleUpdateSignal))
	mux.Handle("DELETE /api/v1/signals/{id}", s.auth(s.handleDeleteSignal))
	mux.Handle("POST /api/v1/signals/{id}/extract", s.auth(s.handleExtractSignal)) // facts from the body (extract.go)
	mux.Handle("GET /api/v1/sources", s.auth(s.handleListSources))
	mux.Handle("POST /api/v1/sources", s.auth(s.handleCreateSource))
	mux.Handle("PATCH /api/v1/sources/{id}", s.auth(s.handleUpdateSource))
	mux.Handle("DELETE /api/v1/sources/{id}", s.auth(s.handleDeleteSource))
	// a work item's provenance (outputs.go): origins and outputs
	mux.Handle("GET /api/v1/tasks/{id}/origins", s.auth(s.handleListOrigins))
	mux.Handle("POST /api/v1/tasks/{id}/origins", s.auth(s.handleAttachOrigin))
	mux.Handle("DELETE /api/v1/tasks/{id}/origins/{signalId}", s.auth(s.handleDetachOrigin))
	mux.Handle("GET /api/v1/tasks/{id}/outputs", s.auth(s.handleListOutputs))
	mux.Handle("POST /api/v1/tasks/{id}/outputs", s.auth(s.handleCreateOutput))
	mux.Handle("DELETE /api/v1/tasks/{id}/outputs/{outputId}", s.auth(s.handleDeleteOutput))
	mux.Handle("GET /api/v1/realtime", s.auth(s.handleRealtime))
	mux.Handle("GET /api/v1/ai/status", s.auth(s.handleAIStatus))
	mux.Handle("POST /api/v1/ai/chat", s.auth(s.handleAIChat))
	mux.Handle("GET /models/", s.auth(s.handleModels)) // WebLLM artifacts, when CADENCE_MODEL_DIR is set

	// ---- embedded web app (web.go) ----
	// "/" is the catch-all; handleSPA turns unregistered /api paths back into
	// API 404s so the app shell never masquerades as an endpoint.
	if s.spa != nil {
		mux.HandleFunc("/", s.handleSPA)
	}

	// middleware chain (outermost first)
	var h http.Handler = mux
	h = s.secureHeaders(h)   // CSP + nosniff etc. on every response, app and API
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
