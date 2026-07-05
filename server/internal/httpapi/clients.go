package httpapi

import (
	"net/http"

	"github.com/cadence/server/internal/domain"
)

func (s *Server) handleListClients(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clients, err := s.store.ListClients(ctx, TenantID(ctx))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	if clients == nil {
		clients = []domain.Client{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": clients})
}

func (s *Server) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in domain.CreateClientInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	c, err := s.store.CreateClient(ctx, TenantID(ctx), ActorID(ctx), in)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	patch, err := decodePatch(r)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	c, err := s.store.UpdateClient(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id"), patch, ifMatchVersion(r))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteClient(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id")); err != nil {
		writeError(w, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
