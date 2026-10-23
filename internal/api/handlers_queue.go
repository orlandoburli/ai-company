package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/orlandoburli/ai-company/internal/failure"
	"github.com/orlandoburli/ai-company/internal/ranking"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

// registerQueueRoutes exposes the ranked queue and retry-state API. Ranking
// requests use CanWriteWork; model calls are not separately admin-gated yet.
func (s *Server) registerQueueRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/queue", s.authenticated(s.handleQueue))
	mux.Handle("POST /api/items/{id}/start-now", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleStartNow)))
	mux.Handle("GET /api/items/{id}/failure", s.authenticated(s.handleGetFailure))
	mux.Handle("POST /api/items/{id}/failure/reset", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleResetFailure)))
	mux.Handle("POST /api/rankings/run", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleRunRanking)))
	mux.Handle("GET /api/rankings", s.authenticated(s.handleListRankings))
	mux.Handle("GET /api/rankings/{id}", s.authenticated(s.handleGetRanking))
}

func (s *Server) handleStartNow(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	it, err := s.work.StartNow(r.Context(), sc, r.PathValue("id"), sc.UserID)
	switch {
	case errors.Is(err, work.ErrNotFound):
		writeError(w, http.StatusNotFound, "item_not_found", "That item does not exist in this tenant.")
	case errors.Is(err, work.ErrExhausted):
		writeError(w, http.StatusConflict, "item_exhausted", "An exhausted item must be reset by a human before dispatch.")
	case errors.Is(err, work.ErrNotDispatchable):
		writeError(w, http.StatusConflict, "not_dispatchable", "That item is not currently eligible for dispatch.")
	case err != nil:
		s.log.Error("start-now", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"item": it})
	}
}

func (s *Server) handleGetFailure(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	workflowID := strings.TrimSpace(r.URL.Query().Get("workflow_id"))
	if workflowID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "workflow_id is required.")
		return
	}
	st, found, err := failure.NewStore(s.db).State(r.Context(), sc, r.PathValue("id"), workflowID)
	if err != nil {
		s.writeQueueError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "failure_not_found", "No failure state exists for that item and workflow.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"failure": st})
}

type resetFailureRequest struct {
	WorkflowID string `json:"workflow_id"`
}

func (s *Server) handleResetFailure(w http.ResponseWriter, r *http.Request) {
	var req resetFailureRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	req.WorkflowID = strings.TrimSpace(req.WorkflowID)
	if req.WorkflowID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "workflow_id is required.")
		return
	}
	st, err := failure.NewStore(s.db).Reset(r.Context(), sc, r.PathValue("id"), req.WorkflowID, sc.UserID)
	if errors.Is(err, failure.ErrNotFound) {
		writeError(w, http.StatusNotFound, "failure_not_found", "No failure state exists for that item and workflow.")
		return
	}
	if err != nil {
		s.writeQueueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"failure": st})
}

func (s *Server) handleRunRanking(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	store := ranking.NewStore(s.db)
	run, err := ranking.NewRunner(store, nil, nil).Run(r.Context(), sc)
	if err != nil {
		s.writeQueueError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ranking": run})
}
func (s *Server) handleListRankings(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	runs, err := ranking.NewStore(s.db).List(r.Context(), sc, 50)
	if err != nil {
		s.writeQueueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rankings": runs})
}
func (s *Server) handleGetRanking(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	store := ranking.NewStore(s.db)
	run, found, err := store.Get(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeQueueError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "ranking_not_found", "That ranking does not exist in this tenant.")
		return
	}
	entries, err := store.Entries(r.Context(), sc, run.ID)
	if err != nil {
		s.writeQueueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ranking": run, "entries": entries})
}

func (s *Server) writeQueueError(w http.ResponseWriter, err error) {
	if errors.Is(err, tenancy.ErrForbidden) {
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
		return
	}
	s.log.Error("queue handler", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
}
