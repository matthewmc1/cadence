package httpapi

import (
	"net/http"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// Signals (0014) over HTTP. Everything here is session-protected and
// tenant-fenced by the store. The list is paged (signals are unbounded and
// never in Bootstrap); the body is its own endpoint so a list never carries
// it; PATCH is disposition-only (signals are immutable records). A capture,
// a delete and a body read each leave an audit row — without the content.

// GET /signals?stage=&sourceId=&projectHint=&occurredAfter=&limit=&cursor=
func (s *Server) handleListSignals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := signalFilterFromQuery(r)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	page, err := s.store.ListSignals(ctx, TenantID(ctx), f, pageFromQuery(r))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// GET /signals/count?stage=inbox — the header badge.
func (s *Server) handleCountSignals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := signalFilterFromQuery(r)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	n, err := s.store.CountSignals(ctx, TenantID(ctx), f)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// signalFilterFromQuery validates the filter here (see handleListTasks):
// an unknown stage is a 400, not a silently empty page.
func signalFilterFromQuery(r *http.Request) (store.SignalFilter, error) {
	q := r.URL.Query()
	f := store.SignalFilter{}
	if v := q.Get("stage"); v != "" {
		st := domain.SignalStage(v)
		if !st.Valid() {
			return f, domain.Invalid("stage", "must be one of inbox, snoozed, promoted, attached, dismissed")
		}
		f.Stage = &st
	}
	if v := q.Get("sourceId"); v != "" {
		f.SourceID = &v
	}
	if v := q.Get("projectHint"); v != "" {
		f.ProjectHint = &v
	}
	if v := q.Get("occurredAfter"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, domain.Invalid("occurredAfter", "must be an RFC3339 timestamp")
		}
		f.OccurredAfter = &t
	}
	return f, nil
}

// POST /signals — manual capture. The 1 MiB request cap (decodeJSON) is the
// transport bound; the store bounds text at 200 KB.
func (s *Server) handleCreateSignal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in domain.CreateSignalInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	sig, err := s.store.CreateSignal(ctx, TenantID(ctx), ActorID(ctx), in)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	s.auditLog(r, TenantID(ctx), domain.AuditEntry{
		ActorID: ptr(ActorID(ctx)), Kind: domain.AuditSignalCapture, EntityType: ptr(domain.EntitySignal), EntityID: &sig.ID,
		Detail: auditDetail(map[string]any{"kind": sig.Kind, "textChars": len(in.Text), "participants": len(sig.Participants)}),
	})
	// Facts are pulled out in the background (extract.go) so the paste box
	// answers immediately; the row's `extracted` fills in via signal.updated.
	s.enqueueExtract(extractJob{tenantID: TenantID(ctx), actorID: ActorID(ctx), signalID: sig.ID})
	writeJSON(w, http.StatusCreated, sig)
}

func (s *Server) handleGetSignal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sig, err := s.store.GetSignal(ctx, TenantID(ctx), r.PathValue("id"))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, sig)
}

// GET /signals/{id}/body — the only path to the full text; audited.
func (s *Server) handleGetSignalBody(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	body, err := s.store.GetSignalBody(ctx, TenantID(ctx), id)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	s.auditLog(r, TenantID(ctx), domain.AuditEntry{
		ActorID: ptr(ActorID(ctx)), Kind: domain.AuditSignalBodyRead, EntityType: ptr(domain.EntitySignal), EntityID: &id,
		Detail: auditDetail(map[string]any{"bodyChars": len(body.Body)}),
	})
	writeJSON(w, http.StatusOK, body)
}

// PATCH /signals/{id} — disposition only (stage, snoozedUntil, projectHint,
// extracted, retentionUntil); anything else is a 400.
func (s *Server) handleUpdateSignal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	patch, err := decodePatch(r)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	sig, err := s.store.UpdateSignalDisposition(ctx, TenantID(ctx), ActorID(ctx), id, patch, ifMatchVersion(r))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	// Setting retentionUntil arms (or disarms) the hourly sweep that deletes
	// this signal, body and all — a deletion scheduled through a PATCH is
	// still a deletion, so it leaves the same kind of trace one does.
	if _, ok := patch["retentionUntil"]; ok {
		detail := map[string]any{"retentionUntil": nil}
		if sig.RetentionUntil != nil {
			detail["retentionUntil"] = sig.RetentionUntil.UTC().Format(time.RFC3339)
		}
		s.auditLog(r, TenantID(ctx), domain.AuditEntry{
			ActorID: ptr(ActorID(ctx)), Kind: domain.AuditSignalRetention, EntityType: ptr(domain.EntitySignal), EntityID: &id,
			Detail: auditDetail(detail),
		})
	}
	writeJSON(w, http.StatusOK, sig)
}

func (s *Server) handleDeleteSignal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if err := s.store.DeleteSignal(ctx, TenantID(ctx), ActorID(ctx), id); err != nil {
		writeError(w, s.log, err)
		return
	}
	s.auditLog(r, TenantID(ctx), domain.AuditEntry{
		ActorID: ptr(ActorID(ctx)), Kind: domain.AuditSignalDelete, EntityType: ptr(domain.EntitySignal), EntityID: &id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---- sources ------------------------------------------------------------------

// GET /sources — the tenant's sources; the manual one is created on first
// use so the paste box always has somewhere to file.
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := s.store.GetOrCreateManualSource(ctx, TenantID(ctx)); err != nil {
		writeError(w, s.log, err)
		return
	}
	srcs, err := s.store.ListSources(ctx, TenantID(ctx))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	if srcs == nil {
		srcs = []domain.Source{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": srcs})
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in domain.CreateSourceInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	src, err := s.store.CreateSource(ctx, TenantID(ctx), ActorID(ctx), in)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, src)
}

func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	patch, err := decodePatch(r)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	src, err := s.store.UpdateSource(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id"), patch, ifMatchVersion(r))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, src)
}

// DELETE /sources/{id} — the source and, by cascade, every signal filed under
// it (with its body and participants). That is the largest deletion this API
// offers, so it is audited with the count the store actually removed; the
// manual source is refused by the store.
func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	n, err := s.store.DeleteSource(ctx, TenantID(ctx), ActorID(ctx), id)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	s.auditLog(r, TenantID(ctx), domain.AuditEntry{
		ActorID: ptr(ActorID(ctx)), Kind: domain.AuditSourceDelete, EntityType: ptr(domain.EntitySource), EntityID: &id,
		Detail: auditDetail(map[string]any{"signals": n}),
	})
	w.WriteHeader(http.StatusNoContent)
}
