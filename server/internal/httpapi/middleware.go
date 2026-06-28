package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/cadence/server/internal/domain"
)

type ctxKey int

const (
	ctxTenant ctxKey = iota
	ctxActor
	ctxEmail
	ctxReqID
)

func TenantID(ctx context.Context) string { v, _ := ctx.Value(ctxTenant).(string); return v }
func ActorID(ctx context.Context) string  { v, _ := ctx.Value(ctxActor).(string); return v }
func Email(ctx context.Context) string    { v, _ := ctx.Value(ctxEmail).(string); return v }

// resolveIdentity reads the session cookie and, if it maps to a live session,
// puts the tenant + user + email in the request context. No cookie / invalid
// session → no identity (handlers behind auth() then reject). Replaces the
// previous header-trust scheme entirely.
func (s *Server) resolveIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		// 1) Bearer token (programmatic clients, e.g. the MCP server)
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
			if tok != "" {
				if t, err := s.store.ResolveAPIToken(ctx, hashToken(tok)); err == nil {
					ctx = context.WithValue(ctx, ctxTenant, t.TenantID)
					ctx = context.WithValue(ctx, ctxActor, t.UserID)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
		}
		// 2) session cookie (browser)
		if c, err := r.Cookie(s.cookieName); err == nil && c.Value != "" {
			if sess, err := s.store.GetSession(ctx, hashToken(c.Value)); err == nil {
				ctx = context.WithValue(ctx, ctxTenant, sess.TenantID)
				ctx = context.WithValue(ctx, ctxActor, sess.UserID)
				ctx = context.WithValue(ctx, ctxEmail, sess.Email)
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// auth gates a handler on a valid session.
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TenantID(r.Context()) == "" {
			writeJSON(w, http.StatusUnauthorized, errBody{errPayload{Message: "sign in to continue", Code: "unauthorized"}})
			return
		}
		next(w, r)
	})
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = domain.NewID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxReqID, id)))
	})
}

// cors reflects the request Origin and allows credentials (cookies). A specific
// origin is echoed (never `*`) because credentialed requests forbid wildcard.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if origin := r.Header.Get("Origin"); origin != "" {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
		}
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID, If-Match, Authorization")
		h.Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic recovered", "err", rec, "path", r.URL.Path)
				writeJSON(w, http.StatusInternalServerError, errBody{errPayload{Message: "something went wrong", Code: "internal"}})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/api/v1/realtime" {
			s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"dur", time.Since(start).Round(time.Millisecond).String())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer (needed so
// the websocket handler can Hijack through this wrapper).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
