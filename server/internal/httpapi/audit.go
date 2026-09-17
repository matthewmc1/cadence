package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// The audit log's HTTP surface is read-only and session-scoped: a user sees
// their own tenant's history, nothing else, and nobody writes to it over
// HTTP — rows are produced server-side by the auth handlers (auth.go), the
// AI gateway (StoreAudit below) and, later, MCP/export/connector paths.

// GET /audit?limit=&cursor= — one page of the tenant's log, newest first.
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.store.ListAudit(ctx, TenantID(ctx), pageFromQuery(r))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// pageFromQuery reads the paging contract off the query string. A bad limit
// is treated as unset (the store applies default/cap); a bad cursor is left
// to store.DecodeCursor, which answers 400.
func pageFromQuery(r *http.Request) store.Page {
	q := r.URL.Query()
	p := store.Page{Cursor: q.Get("cursor")}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		p.Limit = n
	}
	return p
}

// auditLog appends an audit row for the tenant, best-effort: a failure is
// logged and never fails the request that produced it (a login must not
// bounce because the log hiccuped). IPHash is filled from the request when
// the entry has none.
func (s *Server) auditLog(r *http.Request, tenantID string, e domain.AuditEntry) {
	if e.IPHash == nil {
		if h := s.hashIP(clientIP(r)); h != "" {
			e.IPHash = &h
		}
	}
	// Detached from the request's cancellation (a client that disconnects
	// after the action it is being logged for must not erase the row), with
	// its own short deadline so a stuck store cannot pin the handler.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditWriteTimeout)
	defer cancel()
	if _, err := s.store.AppendAudit(ctx, tenantID, e); err != nil {
		s.log.Warn("audit append failed", "kind", e.Kind, "err", err)
	}
}

// auditWriteTimeout bounds a detached audit append.
const auditWriteTimeout = 5 * time.Second

// auditDetail marshals a small detail map for AuditEntry.Detail. The
// producers above only ever pass string/number/bool values, so a marshal
// failure is a programming error; it degrades to {} rather than dropping
// the row.
func auditDetail(m map[string]any) json.RawMessage {
	raw, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func ptr(s string) *string { return &s }

// hashIP is the only trace of a client address that reaches the audit log:
// the first 16 bytes (hex) of HMAC-SHA256 over the address, keyed with the
// deployment's audit salt (CADENCE_AUDIT_SALT). It lets an operator ask "did
// these logins come from the same place" without the address being stored.
// The key is what makes that true: an unkeyed digest of the 2^32 IPv4 space
// is a lookup table, not a hash, and the value is returned to every member
// of the tenant by GET /audit and copied into every backup. Without the key
// nobody — not a tenant member, not a backup holder — can turn the value
// back into an address.
func (s *Server) hashIP(ip string) string {
	if ip == "" {
		return ""
	}
	mac := hmac.New(sha256.New, s.auditSalt)
	mac.Write([]byte(ip))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// auditSaltOrRandom returns the configured salt or, when none is set, a
// random per-process key: hashes then still cannot be reversed, they just
// stop correlating across restarts. Boot logs a warning in that case (New).
func auditSaltOrRandom(configured string) ([]byte, bool) {
	if configured != "" {
		return []byte(configured), true
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("audit salt: crypto/rand failed: " + err.Error())
	}
	return key, false
}

// StoreAudit is the store-backed AuditSink: one ai.chat row per gateway call
// under the caller's tenant. It carries what SlogAudit logs — provider,
// model, prompt size, ok/err, duration — and, like it, never the prompt.
type StoreAudit struct {
	Store store.Store
	Log   *slog.Logger
}

func (a StoreAudit) RecordAI(ctx context.Context, rec AIAuditRecord) {
	detail := map[string]any{
		"provider": rec.Provider, "model": rec.Model,
		"promptChars": rec.PromptChars, "ok": rec.OK,
		"durationMs": rec.Duration.Milliseconds(),
	}
	if !rec.OK {
		detail["err"] = rec.Err
	}
	e := domain.AuditEntry{Kind: domain.AuditAIChat, Detail: auditDetail(detail)}
	if rec.UserID != "" {
		actor := rec.UserID
		e.ActorID = &actor
	}
	if _, err := a.Store.AppendAudit(ctx, rec.TenantID, e); err != nil {
		log := a.Log
		if log == nil {
			log = slog.Default()
		}
		log.Warn("audit append failed", "kind", domain.AuditAIChat, "err", err)
	}
}
