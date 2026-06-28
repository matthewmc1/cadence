package httpapi

import (
	"net/http"

	"github.com/cadence/server/internal/domain"
)

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	boot, err := s.store.Bootstrap(ctx, TenantID(ctx), ActorID(ctx))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	// serialize empty collections as [] rather than null
	if boot.Projects == nil {
		boot.Projects = []domain.Project{}
	}
	if boot.Tasks == nil {
		boot.Tasks = []domain.Task{}
	}
	writeJSON(w, http.StatusOK, boot)
}
