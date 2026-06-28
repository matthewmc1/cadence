package httpapi

import (
	"net/http"
	"strconv"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
)

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := store.TaskFilter{}
	q := r.URL.Query()
	if v := q.Get("status"); v != "" {
		st := domain.Status(v)
		f.Status = &st
	}
	if v := q.Get("projectId"); v != "" {
		f.ProjectID = &v
	}
	tasks, err := s.store.ListTasks(ctx, TenantID(ctx), f)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	if tasks == nil {
		tasks = []domain.Task{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := s.store.GetTask(ctx, TenantID(ctx), r.PathValue("id"))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in domain.CreateTaskInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, s.log, err)
		return
	}
	t, err := s.store.CreateTask(ctx, TenantID(ctx), ActorID(ctx), in)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	patch, err := decodePatch(r)
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	t, err := s.store.UpdateTask(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id"), patch, ifMatchVersion(r))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteTask(ctx, TenantID(ctx), ActorID(ctx), r.PathValue("id")); err != nil {
		writeError(w, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ifMatchVersion reads an optimistic-concurrency token from the If-Match header
// (e.g. If-Match: 3). Absent → nil (last-write-wins).
func ifMatchVersion(r *http.Request) *int {
	v := r.Header.Get("If-Match")
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}
