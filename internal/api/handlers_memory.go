package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

// registerMemoryRoutes installs the memory read/write surface.
func (s *Server) registerMemoryRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/memory", s.authenticated(s.handleListMemory))
	mux.Handle("GET /api/memory/manifest", s.authenticated(s.handleMemoryManifest))
	mux.Handle("POST /api/memory", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleWriteMemory)))
}

type memoryWriteRequest struct {
	ScopeKind string   `json:"scope_kind"`
	ScopeRef  string   `json:"scope_ref"`
	Key       string   `json:"key"`
	ContentMD string   `json:"content_md"`
	Tags      []string `json:"tags"`
}

type memoryEntryResponse struct {
	ID              string           `json:"id"`
	TenantID        string           `json:"tenant_id"`
	ScopeKind       memory.ScopeKind `json:"scope_kind"`
	ScopeRef        string           `json:"scope_ref"`
	Key             string           `json:"key"`
	Revision        int              `json:"revision"`
	ContentMD       string           `json:"content_md"`
	ContentJSON     json.RawMessage  `json:"content_json,omitempty"`
	Source          memory.Source    `json:"source"`
	SourceRunID     string           `json:"source_run_id,omitempty"`
	SourceStepRunID string           `json:"source_step_run_id,omitempty"`
	SourceUserID    string           `json:"source_user_id,omitempty"`
	Tags            []string         `json:"tags"`
	TokenEstimate   int              `json:"token_estimate"`
	Seq             int64            `json:"seq"`
	SupersededBy    *string          `json:"superseded_by"`
	CreatedAt       any              `json:"created_at"`
}

func memoryEntryDTO(entry memory.Entry) memoryEntryResponse {
	var supersededBy *string
	if entry.SupersededBy != "" {
		supersededBy = &entry.SupersededBy
	}
	return memoryEntryResponse{
		ID: entry.ID, TenantID: entry.TenantID, ScopeKind: entry.Scope.Kind, ScopeRef: entry.Scope.Ref,
		Key: entry.Key, Revision: entry.Revision, ContentMD: entry.ContentMD, ContentJSON: entry.ContentJSON,
		Source: entry.Source, SourceRunID: entry.SourceRunID, SourceStepRunID: entry.SourceStepRunID,
		SourceUserID: entry.SourceUserID, Tags: entry.Tags, TokenEstimate: entry.TokenEstimate,
		Seq: entry.Seq, SupersededBy: supersededBy, CreatedAt: entry.CreatedAt,
	}
}

func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.memory == nil {
		writeWorkflowUnavailable(w)
		return
	}
	query := r.URL.Query()
	kindValue, ref := query.Get("scope_kind"), query.Get("scope_ref")
	var entries []memory.Entry
	var err error
	if kindValue != "" || ref != "" {
		kind := memory.ScopeKind(kindValue)
		if !validMemoryScopeKind(kind) || strings.TrimSpace(ref) == "" {
			writeError(w, http.StatusBadRequest, "memory_scope_invalid", "A valid scope_kind and scope_ref are required for a scoped read.")
			return
		}
		scope := memory.Scope{Kind: kind, Ref: ref}
		if key := query.Get("key"); key != "" {
			entries, err = s.memory.History(r.Context(), sc, scope, key)
		} else {
			entries, _, err = s.memory.Read(r.Context(), sc, scope)
		}
	} else {
		if query.Get("key") != "" {
			writeError(w, http.StatusBadRequest, "memory_scope_invalid", "A key filter requires scope_kind and scope_ref.")
			return
		}
		entries, err = s.readAllTenantMemory(r.Context(), sc)
	}
	if err != nil {
		s.writeMemoryError(w, err)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Seq == entries[j].Seq && entries[i].Scope.Kind == entries[j].Scope.Kind && entries[i].Scope.Ref == entries[j].Scope.Ref {
			return entries[i].CreatedAt.After(entries[j].CreatedAt)
		}
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].Seq > entries[j].Seq
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	limit := queryInt(r, "limit", 100)
	if limit > 500 {
		limit = 500
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	response := make([]memoryEntryResponse, 0, len(entries))
	for _, entry := range entries {
		response = append(response, memoryEntryDTO(entry))
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": response})
}

// readAllTenantMemory discovers scopes under tenant RLS, then uses the memory
// store's current-entry reader for each scope. The API intentionally does not
// expose a separate unscoped SQL read path for memory rows.
func (s *Server) readAllTenantMemory(ctx context.Context, sc tenancy.Scope) ([]memory.Entry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT scope_kind, scope_ref FROM memory_entries ORDER BY scope_kind, scope_ref`)
	if err != nil {
		return nil, err
	}
	scopes := make([]memory.Scope, 0)
	for rows.Next() {
		var scope memory.Scope
		if err := rows.Scan(&scope.Kind, &scope.Ref); err != nil {
			rows.Close()
			return nil, err
		}
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	byKind, err := s.memory.ReadAll(ctx, sc, scopes)
	if err != nil {
		return nil, err
	}
	entries := make([]memory.Entry, 0)
	for _, list := range byKind {
		entries = append(entries, list...)
	}
	return entries, nil
}

func (s *Server) handleWriteMemory(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.memory == nil {
		writeWorkflowUnavailable(w)
		return
	}
	var req memoryWriteRequest
	if !decodeM3JSON(w, r, &req) {
		return
	}
	scope := memory.Scope{Kind: memory.ScopeKind(req.ScopeKind), Ref: req.ScopeRef}
	if !validMemoryScopeKind(scope.Kind) || strings.TrimSpace(scope.Ref) == "" {
		writeError(w, http.StatusBadRequest, "memory_scope_invalid", "scope_kind must be run, item, agent, or workspace and scope_ref is required.")
		return
	}
	if scope.Kind == memory.ScopeRun {
		writeError(w, http.StatusForbidden, "run_memory_readonly", "Run memory can only be written by the workflow engine.")
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "A memory key is required.")
		return
	}
	entry, err := s.memory.WriteHuman(r.Context(), sc, memory.Write{
		Scope: scope, Key: req.Key, ContentMD: req.ContentMD, Tags: req.Tags,
	})
	if err != nil {
		s.writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"entry": memoryEntryDTO(entry)})
}

func (s *Server) handleMemoryManifest(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	stepID := strings.TrimSpace(r.URL.Query().Get("step_run_id"))
	if stepID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "step_run_id is required.")
		return
	}
	step, err := s.workflows.GetStepRun(r.Context(), sc, stepID)
	if errors.Is(err, workflow.ErrNotFound) {
		writeError(w, http.StatusNotFound, "step_not_found", "That step run does not exist in this tenant.")
		return
	}
	if err != nil {
		s.log.Error("read workflow step manifest", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	// ListStepRuns confirms that the step remains a member of the tenant-scoped
	// run before returning its manifests (GetStepRun itself is tenant-scoped too).
	steps, err := s.workflows.ListStepRuns(r.Context(), sc, step.RunID)
	if err != nil {
		s.log.Error("list workflow step manifests", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	found := false
	for _, candidate := range steps {
		if candidate.ID == step.ID {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "step_not_found", "That step run does not exist in this tenant.")
		return
	}
	manifest := step.MemoryManifest
	if len(manifest) == 0 || string(manifest) == "null" {
		manifest = step.ContextManifest
	}
	if len(manifest) == 0 || string(manifest) == "null" {
		manifest = json.RawMessage(`[]`)
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifest": manifest})
}

func validMemoryScopeKind(kind memory.ScopeKind) bool {
	switch kind {
	case memory.ScopeRun, memory.ScopeItem, memory.ScopeAgent, memory.ScopeWorkspace:
		return true
	default:
		return false
	}
}

func (s *Server) writeMemoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, memory.ErrRunScopeReadonly):
		writeError(w, http.StatusForbidden, "run_memory_readonly", "Run memory can only be written by the workflow engine.")
	case errors.Is(err, memory.ErrInvalidScope):
		writeError(w, http.StatusBadRequest, "memory_scope_invalid", "The memory scope is invalid.")
	case errors.Is(err, memory.ErrInvalidKey):
		writeError(w, http.StatusBadRequest, "invalid_request", "A memory key is required.")
	default:
		s.log.Error("memory handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}
