package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
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
	now := time.Now()

	// This endpoint is public and sends real email, so it is an email-bombing /
	// cost-amplification target. Cap requests per source IP first, before any work.
	if !s.authRate.allowIP(clientIP(r), now) {
		writeJSON(w, http.StatusTooManyRequests, errBody{errPayload{Message: "too many sign-in requests — wait a minute and try again", Code: "rate_limited"}})
		return
	}

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

	okResp := map[string]any{"ok": true, "email": email}

	// Per-email cooldown: don't flood one address with links. Report the same
	// success (no enumeration signal); a real user rarely re-requests within a
	// minute, and can retry after the cooldown.
	if s.authRate.emailInCooldown(email, now) {
		writeJSON(w, http.StatusOK, okResp)
		return
	}

	tok := newToken()
	if err := s.store.CreateLoginToken(r.Context(), hashToken(tok), email, now.Add(s.loginTokenTTL)); err != nil {
		writeError(w, s.log, err)
		return
	}
	link := s.linkBase(r) + "/auth?token=" + tok

	// Deliver the link by email. A live-provider failure is surfaced to the user
	// so they aren't left waiting for a mail that will never arrive (there is no
	// account-enumeration concern: an account is created on verify, not request,
	// so every well-formed email is treated identically).
	if err := s.mailer.SendMagicLink(r.Context(), email, link); err != nil {
		s.log.Error("magic link send failed", "email", email, "err", err)
		writeJSON(w, http.StatusBadGateway, errBody{errPayload{Message: "couldn't send your sign-in email — please try again", Code: "mail_failed"}})
		return
	}
	s.authRate.recordEmailSent(email, now) // start the cooldown only on a real send
	s.log.Info("magic link issued", "email", email, "delivered", s.mailer.Live())

	// In dev we also hand back the link for convenience. Never do this once a
	// real mail provider is delivering it — the link is a bearer credential.
	if s.devAuth && !s.mailer.Live() {
		okResp["devLink"] = link
	}
	writeJSON(w, http.StatusOK, okResp)
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

// POST /auth/tokens {name} — mint a personal access token (shown once).
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "MCP token"
	}
	ctx := r.Context()
	raw := "cdnc_" + newToken()
	tok := domain.APIToken{
		ID: domain.NewID(), TenantID: TenantID(ctx), UserID: ActorID(ctx),
		Name: name, CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateAPIToken(ctx, hashToken(raw), tok); err != nil {
		writeError(w, s.log, err)
		return
	}
	// `token` is returned only here, once.
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": raw, "id": tok.ID, "name": tok.Name, "createdAt": tok.CreatedAt,
	})
}

// GET /auth/tokens — list this user's tokens (no secrets).
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	toks, err := s.store.ListAPITokens(ctx, TenantID(ctx), ActorID(ctx))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": toks})
}

// DELETE /auth/tokens/{id} — revoke a token.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.RevokeAPIToken(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id")); err != nil {
		writeError(w, s.log, err)
		return
	}
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
