package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/mailer"
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

	// Signup policy (signup.go). An address that may not sign in gets exactly
	// the response an allowed one gets, and no email: a closed door that never
	// confirms whether an account exists behind it.
	allowed, err := s.signupAllowed(r.Context(), email)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	if !allowed {
		s.log.Info("magic link withheld", "email", mailer.Redact(email), "signup", s.signup.String())
		writeJSON(w, http.StatusOK, okResp)
		return
	}

	tok := newToken()
	req := magicLinkRequest{
		email:     email,
		link:      s.linkBase(r) + "/auth?token=" + tok,
		tokenHash: hashToken(tok),
		expiresAt: now.Add(s.loginTokenTTL),
		ipHash:    s.hashIP(clientIP(r)),
	}

	// Issue and deliver the link OFF the request. This endpoint is public, and
	// under an invite/domains policy the only difference between an admitted
	// address and a withheld one is whether an account exists — so the
	// response's status AND its latency must be the same on both paths. Every
	// step that only an admitted address reaches (the token insert, the
	// account lookup, the audit append, the SMTP round-trip) therefore happens
	// after the response is decided, detached from the request with its own
	// deadline; a failure is logged, never surfaced. Both branches answer
	// after exactly one signupAllowed lookup.
	s.authRate.recordEmailSent(email, now)
	if s.devAuth && !s.mailer.Live() {
		// Dev convenience: the link comes back in the response, so the token
		// has to exist before we answer. There is no enumeration to protect
		// against here — no provider is delivering anything.
		s.issueMagicLink(req)
		okResp["devLink"] = req.link
	} else {
		go s.issueMagicLink(req)
	}
	s.log.Info("magic link issued", "email", mailer.Redact(email), "delivered", s.mailer.Live())
	writeJSON(w, http.StatusOK, okResp)
}

// magicLinkRequest is one issued link, carried off the request path: the
// address it is for, the link itself, the token hash to store, its expiry, and
// the hash of the client address for the audit row (computed while the request
// is still alive — nothing here holds on to *http.Request).
type magicLinkRequest struct {
	email, link, tokenHash, ipHash string
	expiresAt                      time.Time
}

// magicLinkSendTimeout bounds one detached delivery attempt; magicLinkStoreTimeout
// bounds the two store writes around it.
const (
	magicLinkSendTimeout  = 30 * time.Second
	magicLinkStoreTimeout = 5 * time.Second
)

// issueMagicLink stores the one-time token, audits the issue under the
// account's tenant and sends the mail — the whole admitted-address path, off
// the request (see handleAuthRequest). Nothing it does can be surfaced to the
// caller, so every failure is logged and the address never is.
func (s *Server) issueMagicLink(req magicLinkRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), magicLinkStoreTimeout)
	if err := s.store.CreateLoginToken(ctx, req.tokenHash, req.email, req.expiresAt); err != nil {
		s.log.Error("magic link token not stored", "email", mailer.Redact(req.email), "err", err)
		cancel()
		return
	}
	// Audit under the account's tenant when there is one. A first-time
	// sign-up has no tenant until verify creates it, so its first row is the
	// auth.login.verified one; the email itself never reaches the log.
	if acc, err := s.store.FindAccount(ctx, req.email); err == nil {
		e := domain.AuditEntry{ActorID: &acc.UserID, Kind: domain.AuditLoginIssued}
		if req.ipHash != "" {
			e.IPHash = &req.ipHash
		}
		if _, err := s.store.AppendAudit(ctx, acc.TenantID, e); err != nil {
			s.log.Warn("audit append failed", "kind", e.Kind, "err", err)
		}
	}
	cancel()
	s.deliverMagicLink(req.email, req.link)
}

// deliverMagicLink sends the link with its own deadline. The address is never
// logged, and neither is a provider's error verbatim: SMTP rejections and
// Resend's sandbox errors quote the recipient back at us, so the error text
// goes through the same redaction the address does.
func (s *Server) deliverMagicLink(email, link string) {
	ctx, cancel := context.WithTimeout(context.Background(), magicLinkSendTimeout)
	defer cancel()
	if err := s.mailer.SendMagicLink(ctx, email, link); err != nil {
		s.log.Error("magic link send failed", "email", mailer.Redact(email), "err", mailer.RedactErr(err))
		return
	}
	if s.mailer.Live() {
		s.log.Info("magic link delivered", "email", mailer.Redact(email))
	}
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

	// Re-check the policy here too: a link issued before CADENCE_SIGNUP was
	// tightened must not be the one thing that still creates a workspace.
	if allowed, err := s.signupAllowed(ctx, email); err != nil {
		writeError(w, s.log, err)
		return
	} else if !allowed {
		writeJSON(w, http.StatusForbidden, errBody{errPayload{Message: "sign-in for this address needs an invitation", Code: "not_invited"}})
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
	s.auditLog(r, acc.TenantID, domain.AuditEntry{ActorID: &acc.UserID, Kind: domain.AuditLoginVerified})

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
	// The token's display name is the only detail; its secret never leaves
	// this response.
	s.auditLog(r, tok.TenantID, domain.AuditEntry{
		ActorID: &tok.UserID, Kind: domain.AuditTokenCreate,
		EntityType: ptr(domain.EntityAPIToken), EntityID: &tok.ID,
		Detail: auditDetail(map[string]any{"name": tok.Name}),
	})
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
	s.auditLog(r, TenantID(ctx), domain.AuditEntry{
		ActorID: ptr(ActorID(ctx)), Kind: domain.AuditTokenRevoke,
		EntityType: ptr(domain.EntityAPIToken), EntityID: ptr(r.PathValue("id")),
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
