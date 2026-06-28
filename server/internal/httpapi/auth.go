package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/cadence/server/internal/domain"
)

func newToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// POST /auth/request {email} — issue a magic link.
func (s *Server) handleAuthRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	email := domain.NormalizeEmail(in.Email)
	if !domain.ValidEmail(email) {
		writeError(w, s.log, domain.Invalid("email", "enter a valid email address"))
		return
	}

	tok := newToken()
	if err := s.store.CreateLoginToken(r.Context(), hashToken(tok), email, time.Now().Add(s.loginTokenTTL)); err != nil {
		writeError(w, s.log, err)
		return
	}
	link := s.linkBase(r) + "/auth?token=" + tok

	// No email transport is wired; log the link. In dev we also return it so the
	// flow is usable. In production, deliver `link` by email and never return it.
	s.log.Info("magic link issued", "email", email, "link", link)
	resp := map[string]any{"ok": true, "email": email}
	if s.devAuth {
		resp["devLink"] = link
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /auth/verify {token} — consume the link, start a session, set the cookie.
func (s *Server) handleAuthVerify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	ctx := r.Context()

	email, err := s.store.ConsumeLoginToken(ctx, hashToken(in.Token))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errBody{errPayload{Message: "this link is invalid or has expired", Code: "bad_token"}})
		return
	}

	acc, err := s.store.FindOrCreateAccount(ctx, email)
	if err != nil {
		writeError(w, s.log, err)
		return
	}

	sessionTok := newToken()
	exp := time.Now().Add(s.sessionTTL)
	if err := s.store.CreateSession(ctx, hashToken(sessionTok), domain.Session{
		UserID: acc.UserID, TenantID: acc.TenantID, Email: email, ExpiresAt: exp,
	}); err != nil {
		writeError(w, s.log, err)
		return
	}
	s.setSessionCookie(w, sessionTok, exp)

	writeJSON(w, http.StatusOK, map[string]any{"user": domain.DeriveUser(acc.UserID, acc.TenantID, email)})
}

// GET /auth/me — the current session's user, or 401.
func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if TenantID(ctx) == "" {
		writeJSON(w, http.StatusUnauthorized, errBody{errPayload{Message: "not signed in", Code: "unauthorized"}})
		return
	}
	user, err := s.store.GetUser(ctx, TenantID(ctx), ActorID(ctx))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// POST /auth/logout — end the session and clear the cookie.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookieName); err == nil && c.Value != "" {
		_ = s.store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}
