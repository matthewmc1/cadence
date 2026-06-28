package httpapi

import (
	"net/http"

	"github.com/cadence/server/internal/domain"
)

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projects, err := s.store.ListProjects(ctx, TenantID(ctx))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	if projects == nil {
		projects = []domain.Project{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in domain.CreateProjectInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	p, err := s.store.CreateProject(ctx, TenantID(ctx), ActorID(ctx), in)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	patch, err := decodePatch(r)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	p, err := s.store.UpdateProject(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id"), patch, ifMatchVersion(r))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteProject(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id")); err != nil {
		writeError(w, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
