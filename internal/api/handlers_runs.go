package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

// registerWorkflowRunRoutes installs the tenant-scoped M3 run API. The server
// router should call this alongside registerWorkflowTemplateRoutes.
func (s *Server) registerWorkflowRunRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/items/{id}/runs", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleStartRun)))
	mux.Handle("GET /api/runs", s.authenticated(s.handleListRuns))
	mux.Handle("GET /api/runs/{id}", s.authenticated(s.handleGetRun))
	mux.Handle("POST /api/runs/{id}/cancel", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleCancelRun)))
	mux.Handle("GET /api/runs/{id}/logs", s.authenticated(s.handleRunLogs))
	mux.Handle("GET /api/runs/{id}/events", s.authenticated(s.handleRunEvents))
	mux.Handle("GET /api/runs/{id}/interventions", s.authenticated(s.handleListInterventions))
	mux.Handle("POST /api/runs/{id}/intervene", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleIntervene)))
}

type startRunRequest struct {
	TemplateID      string `json:"template_id"`
	TemplateVersion *int   `json:"template_version"`
	// Variables are the run's own inputs: the values the run's templates are
	// filled with, and the values a node's `config.runner` may resolve
	// ($NAME). They win over the tenant's defaults.
	Variables map[string]string `json:"variables"`
	// Language is the language this run works in. It is optional: without it
	// the run inherits the company's language. An unknown language is refused
	// rather than ignored.
	Language string `json:"language"`
}

type runWire struct {
	ID                     string            `json:"id"`
	ItemID                 string            `json:"item_id"`
	TemplateID             string            `json:"template_id"`
	TemplateVersion        int               `json:"template_version"`
	Variables              map[string]string `json:"variables"`
	Language               tenancy.Language  `json:"language"`
	State                  workflow.RunState `json:"state"`
	MaxConcurrency         int               `json:"max_concurrency"`
	StartedBy              string            `json:"started_by"`
	StartedAt              *time.Time        `json:"started_at"`
	FinishedAt             *time.Time        `json:"finished_at"`
	Error                  *string           `json:"error"`
	FailureKind            *string           `json:"failure_kind"`
	Branch                 string            `json:"branch"`
	WorktreePath           string            `json:"worktree_path"`
	DefinitionSnapshotJSON json.RawMessage   `json:"definition_snapshot_json"`
	CreatedAt              time.Time         `json:"created_at"`
}

func runResponse(run workflow.Run) runWire {
	var errText, failure *string
	if run.ErrorText != "" {
		errText = &run.ErrorText
	}
	if run.FailureKind != "" {
		failure = &run.FailureKind
	}
	variables := run.Variables
	if variables == nil {
		variables = map[string]string{}
	}
	language := run.Language
	if !language.Valid() {
		language = tenancy.DefaultLanguage
	}
	return runWire{ID: run.ID, ItemID: run.ItemID, TemplateID: run.TemplateID,
		TemplateVersion: run.TemplateVersion, Variables: variables, Language: language, State: run.State, MaxConcurrency: run.MaxConcurrency,
		StartedBy: run.StartedBy, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Error: errText, FailureKind: failure, Branch: run.Branch, WorktreePath: run.WorktreePath,
		DefinitionSnapshotJSON: run.DefinitionSnapshot, CreatedAt: run.CreatedAt}
}

type stepWire struct {
	ID              string                      `json:"id"`
	RunID           string                      `json:"run_id"`
	NodeID          string                      `json:"node_id"`
	IterationPath   string                      `json:"iteration_path"`
	RunIndex        int                         `json:"run_index"`
	Kind            string                      `json:"kind"`
	Name            string                      `json:"name"`
	State           workflow.StepState          `json:"state"`
	InputEdges      map[string]workflow.Receipt `json:"input_edges"`
	OutputPorts     map[string]any              `json:"output_ports"`
	Result          any                         `json:"result"`
	FailureKind     *string                     `json:"failure_kind"`
	WorkspaceKind   string                      `json:"workspace_kind"`
	Workdir         *string                     `json:"workdir"`
	AgentJobID      *string                     `json:"agent_job_id"`
	MemoryManifest  json.RawMessage             `json:"memory_manifest"`
	ContextManifest json.RawMessage             `json:"context_manifest"`
	ClaimExpiresAt  *time.Time                  `json:"claim_expires_at"`
	StartedAt       *time.Time                  `json:"started_at"`
	FinishedAt      *time.Time                  `json:"finished_at"`
}

// visibleSteps drops the join-accumulator rows of an earlier engine version
// (run index -1). The engine now creates the execution row itself and claims
// it when the node runs, so no new row carries a negative index; the filter
// keeps the monitor honest for runs that were started before that change.
func visibleSteps(steps []workflow.StepRun) []workflow.StepRun {
	out := make([]workflow.StepRun, 0, len(steps))
	for _, step := range steps {
		if step.RunIndex < 0 {
			continue
		}
		out = append(out, step)
	}
	return out
}

func stepResponse(step workflow.StepRun) stepWire {
	var failure, workdir, jobID *string
	if step.FailureKind != "" {
		failure = &step.FailureKind
	}
	if step.Workdir != "" {
		workdir = &step.Workdir
	}
	if step.AgentJobID != "" {
		jobID = &step.AgentJobID
	}
	ports := make(map[string]any, len(step.OutputPorts))
	for name, raw := range step.OutputPorts {
		ports[name] = map[string]any{"value": json.RawMessage(raw)}
	}
	var result any
	if len(step.Result) > 0 {
		var value map[string]any
		if json.Unmarshal(step.Result, &value) == nil {
			if in, ok := value["tokens_in"]; ok {
				out := value["tokens_out"]
				value["tokens"] = map[string]any{"in": in, "out": out}
				delete(value, "tokens_in")
				delete(value, "tokens_out")
			}
			if value["artifacts"] == nil {
				value["artifacts"] = []any{}
			}
			result = value
		} else {
			result = json.RawMessage(step.Result)
		}
	}
	return stepWire{ID: step.ID, RunID: step.RunID, NodeID: step.NodeID,
		IterationPath: step.IterationPath, RunIndex: step.RunIndex, Kind: step.Kind, Name: step.Name,
		State: step.State, InputEdges: step.InputEdges, OutputPorts: ports, Result: result,
		FailureKind: failure, WorkspaceKind: step.WorkspaceKind, Workdir: workdir, AgentJobID: jobID,
		MemoryManifest: nonNilJSON(step.MemoryManifest, "[]"), ContextManifest: nonNilJSON(step.ContextManifest, "[]"),
		ClaimExpiresAt: step.ClaimExpiresAt, StartedAt: step.StartedAt, FinishedAt: step.FinishedAt}
}

func nonNilJSON(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(fallback)
	}
	return raw
}

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.engine == nil || s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	var req startRunRequest
	if !decodeM3JSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.TemplateID) == "" || (req.TemplateVersion != nil && *req.TemplateVersion <= 0) {
		writeError(w, http.StatusBadRequest, "invalid_request", "A template_id and a positive template_version are required.")
		return
	}
	// The language of the run: the one the request names, the company's own
	// when the request names none. An unknown language is refused here, so a
	// run is never queued in a language no agent would be told.
	language, err := s.resolveRunLanguage(r, req.Language)
	if err != nil {
		if errors.Is(err, workflow.ErrUnknownLanguage) {
			writeError(w, http.StatusBadRequest, "invalid_request", "Language must be one of en, pt-BR.")
			return
		}
		s.internal(w, "resolve run language", err)
		return
	}
	// Validate the item separately so a missing item is not misreported as a
	// missing workflow template by StartRun.
	if _, err := s.work.GetItem(r.Context(), sc, r.PathValue("id")); err != nil {
		if errors.Is(err, work.ErrNotFound) {
			writeError(w, http.StatusNotFound, "item_not_found", "That item does not exist in this tenant.")
		} else {
			s.writeRunError(w, err, "run")
		}
		return
	}
	requested, err := s.workflows.GetTemplate(r.Context(), sc, req.TemplateID)
	if err != nil {
		s.writeRunError(w, err, "template")
		return
	}
	if requested.DisabledAt != nil {
		writeError(w, http.StatusConflict, "template_disabled", "That workflow template is disabled and cannot start a run.")
		return
	}
	published, err := s.workflows.ListTemplates(r.Context(), sc, true)
	if err != nil {
		s.writeRunError(w, err, "template")
		return
	}
	var chosen *workflow.TemplateRow
	for i := range published {
		candidate := &published[i]
		if candidate.Name != requested.Name {
			continue
		}
		if candidate.DisabledAt != nil {
			continue
		}
		if req.TemplateVersion != nil && candidate.Version != *req.TemplateVersion {
			continue
		}
		if chosen == nil || candidate.Version > chosen.Version {
			chosen = candidate
		}
	}
	if chosen == nil {
		writeError(w, http.StatusUnprocessableEntity, "template_not_published", "No matching published template version exists.")
		return
	}
	run, err := s.engine.StartRun(r.Context(), sc, chosen.ID, r.PathValue("id"), sc.UserID,
		workflow.WithRunVariables(req.Variables), workflow.WithRunLanguage(language))
	if err != nil {
		if errors.Is(err, workflow.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "template_not_published", "The selected template is no longer published.")
		} else if errors.Is(err, workflow.ErrTemplateDisabled) {
			writeError(w, http.StatusConflict, "template_disabled", "That workflow template is disabled and cannot start a run.")
		} else if errors.Is(err, workflow.ErrUnknownLanguage) {
			writeError(w, http.StatusBadRequest, "invalid_request", "Language must be one of en, pt-BR.")
		} else if isWorkflowValidationError(err) {
			writeTemplateInvalid(w, err)
		} else {
			s.writeRunError(w, err, "run")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": runResponse(run)})
}

// resolveRunLanguage resolves the language a run works in.
//
// The request may name one; otherwise the company's own language is the run's.
// The run remembers the answer, so a company that changes its language later
// does not change the language of a run already under way.
func (s *Server) resolveRunLanguage(r *http.Request, requested string) (tenancy.Language, error) {
	if strings.TrimSpace(requested) != "" {
		// The request names one, so the company's setting is not consulted.
		return workflow.ResolveLanguage(requested, "")
	}
	tenantLanguage := ""
	if digest, ok := tenancy.SessionHashFrom(r.Context()); ok {
		language, err := s.tenancy.TenantLanguage(r.Context(), digest)
		if err != nil {
			return "", err
		}
		tenantLanguage = string(language)
	}
	return workflow.ResolveLanguage("", tenantLanguage)
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.engine == nil || s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	state := r.URL.Query().Get("state")
	if state != "" && !validRunState(state) {
		writeError(w, http.StatusBadRequest, "invalid_request", "Unknown run state.")
		return
	}
	runs, err := s.workflows.ListRuns(r.Context(), sc, workflow.RunFilter{ItemID: r.URL.Query().Get("item_id"), State: state, Limit: queryInt(r, "limit", 50)})
	if err != nil {
		s.writeRunError(w, err, "run")
		return
	}
	out := make([]runWire, 0, len(runs))
	for _, run := range runs {
		out = append(out, runResponse(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func validRunState(state string) bool {
	switch workflow.RunState(state) {
	case workflow.RunQueued, workflow.RunRunning, workflow.RunWaitingHuman, workflow.RunCompleted, workflow.RunFailed, workflow.RunCancelled:
		return true
	}
	return false
}

func (s *Server) getRunAndSteps(r *http.Request, sc tenancy.Scope, id string) (workflow.Run, []workflow.StepRun, error) {
	run, err := s.workflows.GetRun(r.Context(), sc, id)
	if err != nil {
		return workflow.Run{}, nil, err
	}
	steps, err := s.workflows.ListStepRuns(r.Context(), sc, id)
	return run, steps, err
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.engine == nil || s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	run, steps, err := s.getRunAndSteps(r, sc, r.PathValue("id"))
	if err != nil {
		s.writeRunError(w, err, "run")
		return
	}
	out := make([]stepWire, 0, len(steps))
	for _, step := range visibleSteps(steps) {
		out = append(out, stepResponse(step))
	}
	// The checkpoints travel with the run: a checkpoint is its own row with
	// its own id, and the monitor needs to know which one belongs to which
	// step (the decision endpoint takes the checkpoint id, not the step id).
	checkpoints := []checkpointWire{}
	if cps, err := s.workflows.ListRunCheckpoints(r.Context(), sc, run.ID); err == nil {
		runEvidence := s.checkpointRunEvidenceOfRun(r.Context(), sc, run)
		for _, cp := range cps {
			checkpoints = append(checkpoints, checkpointResponse(cp, s.checkpointEvidenceFor(r.Context(), sc, cp, runEvidence)))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": runResponse(run), "steps": out, "checkpoints": checkpoints})
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.engine == nil || s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	run, err := s.engine.CancelRun(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeRunError(w, err, "run")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": runResponse(run)})
}

type runLogWire struct {
	Seq       int64           `json:"seq"`
	JobID     string          `json:"job_id"`
	RunID     string          `json:"run_id"`
	StepRunID string          `json:"step_run_id"`
	Stream    string          `json:"stream"`
	Level     string          `json:"level"`
	Kind      string          `json:"kind"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data,omitempty"`
	TS        time.Time       `json:"ts"`
}

type runLogAtStep struct {
	entry  agent.LogEntry
	stepID string
	runID  string
}

func (s *Server) runLogs(r *http.Request, sc tenancy.Scope, runID string, after int64, limit int) ([]runLogWire, error) {
	if s.agents == nil {
		return []runLogWire{}, nil
	}
	steps, err := s.workflows.ListStepRuns(r.Context(), sc, runID)
	if err != nil {
		return nil, err
	}
	merged := make([]runLogAtStep, 0)
	for _, step := range steps {
		if step.AgentJobID == "" {
			continue
		}
		job, err := s.agents.GetJob(r.Context(), sc, step.AgentJobID)
		if err != nil {
			if errors.Is(err, agent.ErrNotFound) {
				continue
			}
			return nil, err
		}
		logs, err := s.agents.ListLogs(r.Context(), sc, job.ID, after, limit)
		if err != nil {
			return nil, err
		}
		for _, entry := range logs {
			merged = append(merged, runLogAtStep{entry: entry, stepID: step.ID, runID: runID})
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].entry.Seq != merged[j].entry.Seq {
			return merged[i].entry.Seq < merged[j].entry.Seq
		}
		return merged[i].entry.CreatedAt.Before(merged[j].entry.CreatedAt)
	})
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	out := make([]runLogWire, 0, len(merged))
	for _, row := range merged {
		e := row.entry
		out = append(out, runLogWire{Seq: e.Seq, JobID: e.JobID, RunID: row.runID, StepRunID: row.stepID, Stream: e.Stream, Level: e.Level, Kind: e.Kind, Message: e.Message, Data: e.Data, TS: e.CreatedAt})
	}
	return out, nil
}

func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.engine == nil || s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	if _, err := s.workflows.GetRun(r.Context(), sc, r.PathValue("id")); err != nil {
		s.writeRunError(w, err, "run")
		return
	}
	logs, err := s.runLogs(r, sc, r.PathValue("id"), queryInt64(r, "after_seq", 0), queryInt(r, "limit", 500))
	if err != nil {
		s.writeRunError(w, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func (s *Server) handleListInterventions(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.engine == nil || s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	if _, err := s.workflows.GetRun(r.Context(), sc, r.PathValue("id")); err != nil {
		s.writeRunError(w, err, "run")
		return
	}
	rows, err := s.workflows.ListInterventions(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeRunError(w, err, "intervention")
		return
	}
	out := make([]interventionWire, 0, len(rows))
	for _, row := range rows {
		out = append(out, interventionResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"interventions": out})
}

type interveneRequest struct {
	Kind      string `json:"kind"`
	NodeID    string `json:"node_id"`
	StepRunID string `json:"step_run_id"`
	BodyMD    string `json:"body_md"`
}

func (s *Server) handleIntervene(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.engine == nil || s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	var req interveneRequest
	if !decodeM3JSON(w, r, &req) {
		return
	}
	runID := r.PathValue("id")
	if _, err := s.workflows.GetRun(r.Context(), sc, runID); err != nil {
		s.writeRunError(w, err, "run")
		return
	}
	kind := workflow.InterventionKind(req.Kind)
	if kind != workflow.InterventionInject && kind != workflow.InterventionRedo && kind != workflow.InterventionStop {
		writeError(w, http.StatusBadRequest, "invalid_request", "kind must be inject, redo, or stop.")
		return
	}
	if kind == workflow.InterventionInject && req.StepRunID == "" || kind == workflow.InterventionRedo && req.NodeID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "inject requires step_run_id and redo requires node_id.")
		return
	}
	if req.StepRunID != "" {
		step, err := s.workflows.GetStepRun(r.Context(), sc, req.StepRunID)
		if err != nil || step.RunID != runID {
			s.writeRunError(w, workflow.ErrNotFound, "run")
			return
		}
		if req.NodeID != "" && step.NodeID != req.NodeID && kind != workflow.InterventionRedo {
			writeError(w, http.StatusBadRequest, "invalid_request", "node_id does not match step_run_id.")
			return
		}
	}
	in := workflow.NewIntervention{RunID: runID, NodeID: req.NodeID, StepRunID: req.StepRunID, Kind: kind, BodyMD: req.BodyMD, ActorKind: "human", ActorID: sc.UserID}
	var intervention workflow.Intervention
	var err error
	if kind == workflow.InterventionRedo {
		intervention, err = s.workflows.CreateIntervention(r.Context(), sc, in)
		if err == nil {
			err = s.engine.RedoNode(r.Context(), sc, runID, req.NodeID, req.BodyMD, sc.UserID)
		}
	} else {
		intervention, err = s.engine.Intervene(r.Context(), sc, in)
	}
	if err != nil {
		s.writeRunError(w, err, "intervention")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"intervention": interventionResponse(intervention)})
}

type interventionWire struct {
	ID          string                    `json:"id"`
	RunID       string                    `json:"run_id"`
	NodeID      *string                   `json:"node_id"`
	StepRunID   *string                   `json:"step_run_id"`
	AgentJobID  *string                   `json:"agent_job_id"`
	Kind        workflow.InterventionKind `json:"kind"`
	BodyMD      *string                   `json:"body_md"`
	ActorKind   string                    `json:"actor_kind"`
	ActorID     *string                   `json:"actor_id"`
	Seq         int                       `json:"seq"`
	DeliveredAt *time.Time                `json:"delivered_at"`
	CreatedAt   time.Time                 `json:"created_at"`
}

func interventionResponse(in workflow.Intervention) interventionWire {
	var node, step, job, body, actor *string
	if in.NodeID != "" {
		node = &in.NodeID
	}
	if in.StepRunID != "" {
		step = &in.StepRunID
	}
	if in.AgentJobID != "" {
		job = &in.AgentJobID
	}
	if in.BodyMD != "" {
		body = &in.BodyMD
	}
	if in.ActorID != "" {
		actor = &in.ActorID
	}
	return interventionWire{ID: in.ID, RunID: in.RunID, NodeID: node, StepRunID: step, AgentJobID: job, Kind: in.Kind, BodyMD: body, ActorKind: in.ActorKind, ActorID: actor, Seq: in.Seq, DeliveredAt: in.DeliveredAt, CreatedAt: in.CreatedAt}
}

func (s *Server) writeRunError(w http.ResponseWriter, err error, resource string) {
	switch {
	case errors.Is(err, workflow.ErrNotFound):
		code, message := "run_not_found", "That workflow run does not exist in this tenant."
		if resource == "checkpoint" {
			code, message = "checkpoint_not_found", "That checkpoint does not exist in this tenant."
		}
		if resource == "intervention" {
			code, message = "intervention_not_found", "That intervention does not exist in this tenant."
		}
		writeError(w, http.StatusNotFound, code, message)
	case errors.Is(err, workflow.ErrAlreadyDecided):
		writeError(w, http.StatusConflict, "already_decided", "That checkpoint was already decided; the winning decision is preserved.")
	case errors.Is(err, workflow.ErrTerminal):
		writeError(w, http.StatusConflict, "run_terminal", "That workflow run is already terminal.")
	case errors.Is(err, workflow.ErrInvalidTransition):
		code := "decision_invalid"
		if resource == "intervention" {
			code = "intervention_invalid"
		}
		writeError(w, http.StatusUnprocessableEntity, code, "The requested workflow transition is not valid.")
	case errors.Is(err, work.ErrNotFound):
		writeError(w, http.StatusNotFound, "item_not_found", "That item does not exist in this tenant.")
	default:
		s.log.Error("workflow run handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

// parseAfter is deliberately tolerant like the M2 log endpoint; malformed
// cursors fall back to the beginning rather than silently dropping rows.
func parseAfter(r *http.Request) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.engine == nil || s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	runID := r.PathValue("id")
	if _, err := s.workflows.GetRun(r.Context(), sc, runID); err != nil {
		s.writeRunError(w, err, "run")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	lastSeq := parseAfter(r)
	seenSteps, seenCPs := map[string]string{}, map[string]string{}
	lastRun := ""
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if r.Context().Err() != nil {
			return
		}
		run, steps, err := s.getRunAndSteps(r, sc, runID)
		if err != nil {
			s.writeSSE(w, "error", map[string]string{"message": "Could not load workflow run."})
			flush()
			return
		}
		for _, step := range visibleSteps(steps) {
			b, _ := json.Marshal(stepResponse(step))
			key := string(b)
			if seenSteps[step.ID] != key {
				s.writeSSE(w, "step", map[string]any{"step": stepResponse(step)})
				seenSteps[step.ID] = key
			}
		}
		checkpoints, err := s.workflows.ListCheckpoints(r.Context(), sc, "", 500)
		if err != nil {
			s.writeSSE(w, "error", map[string]string{"message": "Could not load checkpoints."})
			flush()
			return
		}
		runs := map[string]checkpointRunEvidence{}
		for _, cp := range checkpoints {
			if cp.RunID != runID {
				continue
			}
			evidence := s.checkpointEvidenceOf(r.Context(), sc, cp, runs)
			b, _ := json.Marshal(checkpointResponse(cp, evidence))
			key := string(b)
			if seenCPs[cp.ID] != key {
				s.writeSSE(w, "checkpoint", map[string]any{"checkpoint": checkpointResponse(cp, evidence)})
				seenCPs[cp.ID] = key
			}
		}
		logs, err := s.runLogs(r, sc, runID, lastSeq, 500)
		if err != nil {
			s.writeSSE(w, "error", map[string]string{"message": "Could not load run logs."})
			flush()
			return
		}
		for _, entry := range logs {
			s.writeSSE(w, "log", entry)
			if entry.Seq > lastSeq {
				lastSeq = entry.Seq
			}
		}
		b, _ := json.Marshal(runResponse(run))
		if string(b) != lastRun {
			s.writeSSE(w, "run", map[string]any{"run": runResponse(run)})
			lastRun = string(b)
			flush()
		}
		fmt.Fprint(w, ": ping\n\n")
		flush()
		if run.State == workflow.RunCompleted || run.State == workflow.RunFailed || run.State == workflow.RunCancelled {
			s.writeSSE(w, "done", struct{}{})
			flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
