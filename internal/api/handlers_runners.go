package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The runner registry is the HOW half of the agent registry: a CLI binary,
// with the transport config the adapter needs. Reads are open to
// every member; writing the registry is a settings surface, so it is
// owner/admin only — the same gate the agent registry uses.

type runnerCreateRequest struct {
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Config        json.RawMessage `json:"config"`
	CredentialRef string          `json:"credential_ref"`
	BudgetTokens  int64           `json:"budget_tokens"`
	BudgetCostUSD float64         `json:"budget_cost_usd"`
	Enabled       *bool           `json:"enabled"`
}

type runnerPatchRequest struct {
	Name          *string         `json:"name"`
	Kind          *string         `json:"kind"`
	Config        json.RawMessage `json:"config"`
	CredentialRef *string         `json:"credential_ref"`
	BudgetTokens  *int64          `json:"budget_tokens"`
	BudgetCostUSD *float64        `json:"budget_cost_usd"`
	Enabled       *bool           `json:"enabled"`
}

// registerRunnerRoutes mounts the runner registry.
func (s *Server) registerRunnerRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/runners", s.authenticated(s.handleListRunners))
	mux.Handle("POST /api/runners",
		s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleCreateRunner)))
	mux.Handle("GET /api/runners/{id}", s.authenticated(s.handleGetRunner))
	mux.Handle("PATCH /api/runners/{id}",
		s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handlePatchRunner)))
	mux.Handle("DELETE /api/runners/{id}",
		s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleDeleteRunner)))
}

func (s *Server) handleListRunners(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	list, err := s.runners.List(r.Context(), sc)
	if err != nil {
		s.writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runners": list})
}

func (s *Server) handleGetRunner(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	out, err := s.runners.Get(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runner": out})
}

func (s *Server) handleCreateRunner(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req runnerCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := runner.ValidateName(name); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	kind := runner.Kind(strings.TrimSpace(req.Kind))
	if !kind.Valid() {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request",
			"kind must be cli.")
		return
	}
	cfg := normalizeJSONObject(req.Config)
	if err := runner.ValidateConfig(kind, cfg); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_config", err.Error())
		return
	}

	in := runner.NewRunner{
		Name:          name,
		Kind:          kind,
		Config:        cfg,
		CredentialRef: strings.TrimSpace(req.CredentialRef),
		BudgetTokens:  req.BudgetTokens,
		BudgetCostUSD: req.BudgetCostUSD,
		Enabled:       req.Enabled,
	}
	out, err := s.runners.Create(r.Context(), sc, in)
	if err != nil {
		if errors.Is(err, runner.ErrConflict) {
			writeError(w, http.StatusConflict, "runner_name_taken",
				"A runner with that name already exists in this tenant.")
			return
		}
		s.writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"runner": out})
}

func (s *Server) handlePatchRunner(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req runnerPatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cur, err := s.runners.Get(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeRunnerError(w, err)
		return
	}

	patch := runner.Patch{
		Name:          req.Name,
		CredentialRef: req.CredentialRef,
		BudgetTokens:  req.BudgetTokens,
		BudgetCostUSD: req.BudgetCostUSD,
		Enabled:       req.Enabled,
	}
	if req.Kind != nil {
		kind := runner.Kind(strings.TrimSpace(*req.Kind))
		patch.Kind = &kind
	}
	if req.Config != nil {
		cfg := normalizeJSONObject(req.Config)
		patch.Config = &cfg
	}

	// Validate the effective row, not just the patch: changing the kind
	// without a config that matches it would leave a runner the adapter could
	// never run.
	kind := cur.Kind
	if patch.Kind != nil {
		kind = *patch.Kind
		if !kind.Valid() {
			writeError(w, http.StatusUnprocessableEntity, "invalid_request",
				"kind must be cli.")
			return
		}
	}
	cfg := cur.Config
	if patch.Config != nil {
		cfg = *patch.Config
	}
	if err := runner.ValidateConfig(kind, cfg); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_config", err.Error())
		return
	}

	out, err := s.runners.Update(r.Context(), sc, cur.ID, patch)
	if err != nil {
		if errors.Is(err, runner.ErrConflict) {
			writeError(w, http.StatusConflict, "runner_name_taken",
				"A runner with that name already exists in this tenant.")
			return
		}
		s.writeRunnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runner": out})
}

func (s *Server) handleDeleteRunner(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := s.runners.Delete(r.Context(), sc, r.PathValue("id")); err != nil {
		s.writeRunnerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeRunnerError maps a runner store error onto the house error codes.
func (s *Server) writeRunnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runner.ErrNotFound):
		writeError(w, http.StatusNotFound, "runner_not_found",
			"That runner does not exist in this tenant.")
	case errors.Is(err, runner.ErrConflict):
		writeError(w, http.StatusConflict, "runner_name_taken",
			"A runner with that name already exists in this tenant.")
	case errors.Is(err, runner.ErrRunnerInUse):
		writeError(w, http.StatusConflict, "runner_in_use",
			"That runner is bound to an agent. Remove it from the agent's chain first.")
	case errors.Is(err, runner.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
	case errors.Is(err, tenancy.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden",
			"Your role does not allow this action.")
	default:
		s.log.Error("runner handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}
