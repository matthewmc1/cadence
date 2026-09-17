package httpapi

import (
	"net/http"

	"github.com/cadence/server/internal/domain"
)

// A work item's provenance in both directions: origins (the signals it
// derives from, 0014) and outputs (what it produced, 0015). Both hang off
// /tasks/{id}; the store answers 404 when the task is not in the tenant.

// GET /tasks/{id}/origins
func (s *Server) handleListOrigins(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	origins, err := s.store.ListTaskOrigins(ctx, TenantID(ctx), r.PathValue("id"))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	if origins == nil {
		origins = []domain.Origin{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"origins": origins})
}

// POST /tasks/{id}/origins {signalId}
func (s *Server) handleAttachOrigin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		SignalID string `json:"signalId"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	if in.SignalID == "" {
		writeError(w, s.log, domain.Invalid("signalId", "is required"))
		return
	}
	origin, err := s.store.AttachSignalToTask(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id"), in.SignalID)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, origin)
}

// DELETE /tasks/{id}/origins/{signalId}
func (s *Server) handleDetachOrigin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DetachSignalFromTask(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id"), r.PathValue("signalId")); err != nil {
		writeError(w, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /tasks/{id}/outputs
func (s *Server) handleListOutputs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	outs, err := s.store.ListOutputs(ctx, TenantID(ctx), r.PathValue("id"))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	if outs == nil {
		outs = []domain.Output{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"outputs": outs})
}

// POST /tasks/{id}/outputs
func (s *Server) handleCreateOutput(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in domain.CreateOutputInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	out, err := s.store.CreateOutput(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id"), in)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// DELETE /tasks/{id}/outputs/{outputId}
func (s *Server) handleDeleteOutput(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteOutput(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id"), r.PathValue("outputId")); err != nil {
		writeError(w, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
