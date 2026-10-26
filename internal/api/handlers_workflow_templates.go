package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

// registerWorkflowTemplateRoutes installs the tenant-scoped M3 template API.
func (s *Server) registerWorkflowTemplateRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/workflow-templates", s.authenticated(s.handleListWorkflowTemplates))
	mux.Handle("POST /api/workflow-templates", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleCreateWorkflowTemplate)))
	mux.Handle("GET /api/workflow-templates/{id}", s.authenticated(s.handleGetWorkflowTemplate))
	mux.Handle("PATCH /api/workflow-templates/{id}", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handlePatchWorkflowTemplate)))
	mux.Handle("DELETE /api/workflow-templates/{id}", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleDeleteWorkflowTemplate)))
	mux.Handle("POST /api/workflow-templates/{id}/publish", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handlePublishWorkflowTemplate)))
	mux.Handle("POST /api/workflow-templates/{id}/versions", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleNewWorkflowTemplateVersion)))
	mux.Handle("POST /api/workflow-templates/{id}/disable", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleDisableWorkflowTemplate)))
	mux.Handle("POST /api/workflow-templates/{id}/enable", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleEnableWorkflowTemplate)))
}

// registerWorkflowValidateRoute installs the read-only graph validation API.
func (s *Server) registerWorkflowValidateRoute(mux *http.ServeMux) {
	mux.Handle("POST /api/workflow-templates/validate", s.authenticated(s.handleValidateWorkflowTemplate))
}

type workflowTemplateRequest struct {
	Name           *string         `json:"name"`
	AppliesTo      *string         `json:"applies_to"`
	MaxConcurrency *int            `json:"max_concurrency"`
	Graph          json.RawMessage `json:"graph"`
}

type workflowTemplateResponse struct {
	ID             string                    `json:"id"`
	TenantID       string                    `json:"tenant_id"`
	Name           string                    `json:"name"`
	Version        int                       `json:"version"`
	AppliesTo      string                    `json:"applies_to"`
	Graph          workflow.Template         `json:"graph"`
	MaxConcurrency int                       `json:"max_concurrency"`
	CreatedBy      string                    `json:"created_by"`
	PublishedAt    any                       `json:"published_at"`
	DisabledAt     any                       `json:"disabled_at"`
	CreatedAt      any                       `json:"created_at"`
	Versions       []workflowTemplateVersion `json:"versions,omitempty"`
}

type workflowTemplateVersion struct {
	Version        int    `json:"version"`
	PublishedAt    any    `json:"published_at"`
	CreatedAt      any    `json:"created_at"`
	Name           string `json:"name"`
	AppliesTo      string `json:"applies_to"`
	MaxConcurrency int    `json:"max_concurrency"`
}

type workflowTemplateSummary struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id"`
	Name           string `json:"name"`
	Version        int    `json:"version"`
	AppliesTo      string `json:"applies_to"`
	MaxConcurrency int    `json:"max_concurrency"`
	PublishedAt    any    `json:"published_at"`
	DisabledAt     any    `json:"disabled_at"`
	CreatedAt      any    `json:"created_at"`
}

func templateResponse(row workflow.TemplateRow) (workflowTemplateResponse, error) {
	var graph workflow.Template
	if err := json.Unmarshal(row.GraphJSON, &graph); err != nil {
		return workflowTemplateResponse{}, err
	}
	return workflowTemplateResponse{
		ID: row.ID, TenantID: row.TenantID, Name: row.Name, Version: row.Version,
		AppliesTo: row.AppliesTo, Graph: graph, MaxConcurrency: row.MaxConcurrency,
		CreatedBy: row.CreatedBy, PublishedAt: row.PublishedAt, DisabledAt: row.DisabledAt, CreatedAt: row.CreatedAt,
	}, nil
}

func (s *Server) handleListWorkflowTemplates(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	rows, err := s.workflows.ListTemplates(r.Context(), sc, false)
	if err != nil {
		s.writeWorkflowTemplateError(w, err)
		return
	}
	items := make([]workflowTemplateSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, workflowTemplateSummary{
			ID: row.ID, TenantID: row.TenantID, Name: row.Name, Version: row.Version,
			AppliesTo: row.AppliesTo, MaxConcurrency: row.MaxConcurrency,
			PublishedAt: row.PublishedAt, DisabledAt: row.DisabledAt, CreatedAt: row.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": items})
}

func (s *Server) handleCreateWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	var req workflowTemplateRequest
	if !decodeM3JSON(w, r, &req) {
		return
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		writeError(w, http.StatusUnprocessableEntity, "name_required", "A template name is required.")
		return
	}
	graph, err := parseWorkflowGraph(req.Graph)
	if err != nil {
		writeTemplateInvalid(w, err)
		return
	}
	maxConcurrency := 0
	if req.MaxConcurrency != nil {
		maxConcurrency = *req.MaxConcurrency
	}
	appliesTo := ""
	if req.AppliesTo != nil {
		appliesTo = *req.AppliesTo
	}
	row, err := s.workflows.CreateTemplate(r.Context(), sc, workflow.NewTemplate{
		Name: *req.Name, AppliesTo: appliesTo, Graph: *graph,
		MaxConcurrency: maxConcurrency, CreatedBy: sc.UserID,
	})
	if err != nil {
		if isWorkflowValidationError(err) {
			writeTemplateInvalid(w, err)
			return
		}
		s.writeWorkflowTemplateError(w, err)
		return
	}
	result, err := templateResponse(row)
	if err != nil {
		s.log.Error("decode stored workflow template", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"template": result})
}

func (s *Server) handleGetWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	row, err := s.workflows.GetTemplate(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeWorkflowTemplateError(w, err)
		return
	}
	result, err := templateResponse(row)
	if err != nil {
		s.log.Error("decode stored workflow template", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	versions, err := s.workflows.ListTemplates(r.Context(), sc, false)
	if err != nil {
		s.writeWorkflowTemplateError(w, err)
		return
	}
	for _, version := range versions {
		if version.Name == row.Name {
			result.Versions = append(result.Versions, workflowTemplateVersion{
				Version: version.Version, PublishedAt: version.PublishedAt, CreatedAt: version.CreatedAt,
				Name: version.Name, AppliesTo: version.AppliesTo, MaxConcurrency: version.MaxConcurrency,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": result})
}

type workflowValidateRequest struct {
	Graph json.RawMessage `json:"graph"`
}

func (s *Server) handleValidateWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	var req workflowValidateRequest
	if !decodeM3JSON(w, r, &req) {
		return
	}
	var shape map[string]json.RawMessage
	if len(req.Graph) == 0 || json.Unmarshal(req.Graph, &shape) != nil || shape == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The graph must be a JSON object.")
		return
	}
	var graph workflow.Template
	dec := json.NewDecoder(bytes.NewReader(req.Graph))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&graph); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The graph is malformed or contains unknown fields.")
		return
	}
	problems := workflow.ValidateAll(&graph)
	if problems == nil {
		problems = []workflow.Problem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"problems": problems})
}

func (s *Server) handlePatchWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	var req workflowTemplateRequest
	if !decodeM3JSON(w, r, &req) {
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		writeError(w, http.StatusUnprocessableEntity, "name_required", "A template name is required.")
		return
	}
	patch := workflow.TemplatePatch{Name: req.Name, AppliesTo: req.AppliesTo, MaxConcurrency: req.MaxConcurrency}
	if req.Graph != nil {
		graph, err := parseWorkflowGraph(req.Graph)
		if err != nil {
			writeTemplateInvalid(w, err)
			return
		}
		patch.Graph = graph
	}
	row, err := s.workflows.UpdateTemplate(r.Context(), sc, r.PathValue("id"), patch)
	if err != nil {
		if isWorkflowValidationError(err) {
			writeTemplateInvalid(w, err)
			return
		}
		s.writeWorkflowTemplateError(w, err)
		return
	}
	result, err := templateResponse(row)
	if err != nil {
		s.log.Error("decode stored workflow template", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": result})
}

// decodeM3JSON keeps the common strict body limits while using the contract's
// invalid_request code for malformed endpoint payloads.
func decodeM3JSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is malformed or too large.")
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body must be a JSON object.")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is malformed or contains unknown fields.")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body must contain one JSON object.")
		return false
	}
	return true
}

func (s *Server) handleDeleteWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	if err := s.workflows.DeleteTemplate(r.Context(), sc, r.PathValue("id")); err != nil {
		s.writeWorkflowTemplateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePublishWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	row, err := s.workflows.PublishTemplate(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeWorkflowTemplateError(w, err)
		return
	}
	result, err := templateResponse(row)
	if err != nil {
		s.log.Error("decode stored workflow template", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": result})
}

func (s *Server) handleNewWorkflowTemplateVersion(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	row, err := s.workflows.NewTemplateVersion(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeWorkflowTemplateError(w, err)
		return
	}
	result, err := templateResponse(row)
	if err != nil {
		s.log.Error("decode stored workflow template", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"template": result})
}

func (s *Server) handleDisableWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	s.setWorkflowTemplateDisabled(w, r, true)
}

func (s *Server) handleEnableWorkflowTemplate(w http.ResponseWriter, r *http.Request) {
	s.setWorkflowTemplateDisabled(w, r, false)
}

func (s *Server) setWorkflowTemplateDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	var row workflow.TemplateRow
	var err error
	if disabled {
		row, err = s.workflows.DisableTemplate(r.Context(), sc, r.PathValue("id"))
	} else {
		row, err = s.workflows.EnableTemplate(r.Context(), sc, r.PathValue("id"))
	}
	if err != nil {
		s.writeWorkflowTemplateError(w, err)
		return
	}
	result, err := templateResponse(row)
	if err != nil {
		s.log.Error("decode stored workflow template", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": result})
}

func parseWorkflowGraph(raw json.RawMessage) (*workflow.Template, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("invalid_graph: graph is required")
	}
	return workflow.ParseTemplate(raw)
}

func isWorkflowValidationError(err error) bool {
	var validation *workflow.ValidationError
	return errors.As(err, &validation)
}

func writeTemplateInvalid(w http.ResponseWriter, err error) {
	var validation *workflow.ValidationError
	if errors.As(err, &validation) {
		writeError(w, http.StatusUnprocessableEntity, "template_invalid", validation.Error())
		return
	}
	writeError(w, http.StatusUnprocessableEntity, "template_invalid", "invalid_graph: "+err.Error())
}

func (s *Server) writeWorkflowTemplateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workflow.ErrNotFound):
		writeError(w, http.StatusNotFound, "template_not_found", "That workflow template does not exist in this tenant.")
	case errors.Is(err, workflow.ErrTemplatePublished):
		writeError(w, http.StatusConflict, "template_published", "Published templates are immutable; create a new version to change this template.")
	case errors.Is(err, workflow.ErrTemplateDisabled):
		writeError(w, http.StatusConflict, "template_disabled", "Disabled templates are retired; enable the template or create a new version.")
	case errors.Is(err, workflow.ErrTemplateInUse):
		writeError(w, http.StatusConflict, "template_in_use", "That template is used by a workflow run and cannot be deleted.")
	case errors.Is(err, workflow.ErrTemplateNameTaken):
		writeError(w, http.StatusConflict, "template_name_taken", "This tenant already has a template with that name and version.")
	default:
		s.log.Error("workflow template handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}
