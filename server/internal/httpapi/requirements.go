package httpapi

import (
	"net/http"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

// GET /requirements?projectId= — a tenant's requirements, optionally one project's.
func (s *Server) handleListRequirements(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := store.RequirementFilter{}
	if v := r.URL.Query().Get("projectId"); v != "" {
		f.ProjectID = &v
	}
	rs, err := s.store.ListRequirements(ctx, TenantID(ctx), f)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	if rs == nil {
		rs = []domain.Requirement{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requirements": rs})
}

func (s *Server) handleCreateRequirement(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in domain.CreateRequirementInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	req, err := s.store.CreateRequirement(ctx, TenantID(ctx), ActorID(ctx), in)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

func (s *Server) handleUpdateRequirement(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	patch, err := decodePatch(r)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	req, err := s.store.UpdateRequirement(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id"), patch, ifMatchVersion(r))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) handleDeleteRequirement(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteRequirement(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id")); err != nil {
		writeError(w, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
