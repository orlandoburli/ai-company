package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

// registerCheckpointRoutes installs the checkpoint inbox and decision surface.
// The server router should call this alongside registerWorkflowRunRoutes.
func (s *Server) registerCheckpointRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/checkpoints", s.authenticated(s.handleListCheckpoints))
	mux.Handle("GET /api/checkpoints/{id}", s.authenticated(s.handleGetCheckpoint))
	mux.Handle("POST /api/checkpoints/{id}/decision", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleDecideCheckpoint)))
	mux.Handle("POST /api/checkpoints/{id}/remind", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleRemindCheckpoint)))
}

type checkpointWire struct {
	ID            string                     `json:"id"`
	RunID         string                     `json:"run_id"`
	StepRunID     string                     `json:"step_run_id"`
	NodeID        string                     `json:"node_id"`
	Lap           int                        `json:"lap"`
	State         workflow.CheckpointState   `json:"state"`
	SummaryMD     string                     `json:"summary_md"`
	Question      *string                    `json:"question"`
	Artifacts     []workflow.ArtifactSummary `json:"artifacts"`
	Item          *checkpointItemWire        `json:"item"`
	DecisionMD    *string                    `json:"decision_md"`
	Fields        json.RawMessage            `json:"fields"`
	FieldsSpec    json.RawMessage            `json:"fields_spec"`
	DecidedByKind *string                    `json:"decided_by_kind"`
	DecidedBy     *string                    `json:"decided_by"`
	DecidedAt     *time.Time                 `json:"decided_at"`
	RemindAfter   *time.Time                 `json:"remind_after"`
	CreatedAt     time.Time                  `json:"created_at"`
}

// checkpointItemWire names the work a checkpoint is about, so the card can
// link it.
type checkpointItemWire struct {
	ID     string `json:"id"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
}

// checkpointEvidence is what a card shows next to the brief: the question it
// asks, the artifacts of the run and the item the run works on.
type checkpointEvidence struct {
	Question  string
	Artifacts []workflow.ArtifactSummary
	Item      *checkpointItemWire
}

// checkpointRunEvidence is the part of the evidence that belongs to the run,
// so a list of checkpoints reads it once per run.
type checkpointRunEvidence struct {
	Run       workflow.Run
	Artifacts []workflow.ArtifactSummary
	Item      *checkpointItemWire
}

func checkpointResponse(cp workflow.Checkpoint, evidence checkpointEvidence) checkpointWire {
	var decisionMD, byKind, by *string
	if cp.DecisionMD != "" {
		decisionMD = &cp.DecisionMD
	}
	if cp.DecidedByKind != "" {
		byKind = &cp.DecidedByKind
	}
	if cp.DecidedBy != "" {
		by = &cp.DecidedBy
	}
	var question *string
	if evidence.Question != "" {
		q := evidence.Question
		question = &q
	}
	artifacts := evidence.Artifacts
	if artifacts == nil {
		artifacts = []workflow.ArtifactSummary{}
	}
	return checkpointWire{ID: cp.ID, RunID: cp.RunID, StepRunID: cp.StepRunID, NodeID: cp.NodeID,
		Lap: cp.Lap, State: cp.State, SummaryMD: cp.SummaryMD, Question: question,
		Artifacts: artifacts, Item: evidence.Item, DecisionMD: decisionMD,
		Fields: nonNilJSON(cp.Fields, "null"), FieldsSpec: nonNilJSON(cp.FieldsSpec, "[]"),
		DecidedByKind: byKind, DecidedBy: by, DecidedAt: cp.DecidedAt, RemindAfter: cp.RemindAfter,
		CreatedAt: cp.CreatedAt}
}

// checkpointRunEvidenceOf reads the evidence that belongs to a run: its
// artifacts and the item it works on.
//
// A missing item — the work was deleted — is not an error: the card then
// shows no item. A missing run is not an error either.
func (s *Server) checkpointRunEvidenceOf(ctx context.Context, sc tenancy.Scope, runID string) checkpointRunEvidence {
	if s.workflows == nil {
		return checkpointRunEvidence{Artifacts: []workflow.ArtifactSummary{}}
	}
	run, err := s.workflows.GetRun(ctx, sc, runID)
	if err != nil {
		s.log.Warn("read checkpoint run", "run", runID, "error", err)
		return checkpointRunEvidence{Artifacts: []workflow.ArtifactSummary{}}
	}
	return s.checkpointRunEvidenceOfRun(ctx, sc, run)
}

// checkpointRunEvidenceOfRun reads the evidence of a run that is loaded.
func (s *Server) checkpointRunEvidenceOfRun(ctx context.Context, sc tenancy.Scope, run workflow.Run) checkpointRunEvidence {
	evidence := checkpointRunEvidence{Run: run, Artifacts: []workflow.ArtifactSummary{}}
	if s.workflows == nil {
		return evidence
	}
	artifacts, err := s.workflows.ListArtifacts(ctx, sc, run.ID)
	if err != nil {
		s.log.Warn("read checkpoint artifacts", "run", run.ID, "error", err)
	} else {
		steps, stepErr := s.workflows.ListStepRuns(ctx, sc, run.ID)
		if stepErr != nil {
			s.log.Warn("read checkpoint artifact steps", "run", run.ID, "error", stepErr)
		}
		evidence.Artifacts = workflow.ArtifactSummaries(artifacts, steps)
	}
	if run.ItemID != "" && s.work != nil {
		item, err := s.work.GetItem(ctx, sc, run.ItemID)
		switch {
		case err == nil:
			evidence.Item = &checkpointItemWire{ID: item.ID, Number: item.Number, Title: item.Title}
		case !errors.Is(err, work.ErrNotFound):
			s.log.Warn("read checkpoint item", "run", run.ID, "item", run.ItemID, "error", err)
		}
	}
	return evidence
}

// checkpointEvidenceOf assembles the evidence of one checkpoint, reading the
// run's evidence once for a list of checkpoints.
func (s *Server) checkpointEvidenceOf(ctx context.Context, sc tenancy.Scope, cp workflow.Checkpoint, runs map[string]checkpointRunEvidence) checkpointEvidence {
	run, ok := runs[cp.RunID]
	if !ok {
		run = s.checkpointRunEvidenceOf(ctx, sc, cp.RunID)
		runs[cp.RunID] = run
	}
	return s.checkpointEvidenceFor(ctx, sc, cp, run)
}

// checkpointEvidenceFor assembles the evidence of one checkpoint from a run
// that is already loaded.
func (s *Server) checkpointEvidenceFor(ctx context.Context, sc tenancy.Scope, cp workflow.Checkpoint, run checkpointRunEvidence) checkpointEvidence {
	evidence := checkpointEvidence{Artifacts: run.Artifacts, Item: run.Item}
	evidence.Question = s.checkpointQuestion(ctx, sc, cp, run.Run)
	return evidence
}

// checkpointQuestion reads the question a checkpoint asks.
//
// The node's config names it; a node without one asks the default question
// built from the node's name. The run's graph snapshot is the source: it is
// the graph the run actually executes.
func (s *Server) checkpointQuestion(ctx context.Context, sc tenancy.Scope, cp workflow.Checkpoint, run workflow.Run) string {
	if question, ok := workflow.NodeQuestion(run.DefinitionSnapshot, cp.NodeID); ok {
		return question
	}
	// The snapshot does not carry the node: the brief knows the question, and
	// the step's name is the last resort.
	stepName := ""
	if step, err := s.workflows.GetStepRun(ctx, sc, cp.StepRunID); err == nil {
		stepName = step.Name
	}
	return workflow.CheckpointQuestion(run.DefinitionSnapshot, cp.NodeID, stepName, cp.SummaryMD)
}

func (s *Server) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	state := r.URL.Query().Get("state")
	if state != "" && state != "pending" && state != "decided" {
		writeError(w, http.StatusBadRequest, "invalid_request", "state must be pending or decided.")
		return
	}
	limit := queryInt(r, "limit", 50)
	storeState := ""
	storeLimit := limit
	if state == "pending" {
		storeState = "pending"
	} else if state == "decided" {
		// The store can filter one exact state; gather its bounded newest window
		// and select all non-pending laps before applying the API result limit.
		storeLimit = 500
	}
	rows, err := s.workflows.ListCheckpoints(r.Context(), sc, storeState, storeLimit)
	if err != nil {
		s.writeRunError(w, err, "checkpoint")
		return
	}
	filtered := make([]workflow.Checkpoint, 0, len(rows))
	for _, cp := range rows {
		if state == "pending" && cp.State != workflow.CheckpointPending {
			continue
		}
		if state == "decided" && cp.State == workflow.CheckpointPending {
			continue
		}
		filtered = append(filtered, cp)
	}
	if state == "pending" {
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	out := make([]checkpointWire, 0, len(filtered))
	runs := map[string]checkpointRunEvidence{}
	for _, cp := range filtered {
		out = append(out, checkpointResponse(cp, s.checkpointEvidenceOf(r.Context(), sc, cp, runs)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkpoints": out})
}

func (s *Server) handleGetCheckpoint(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	cp, err := s.workflows.GetCheckpoint(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeRunError(w, err, "checkpoint")
		return
	}
	evidence := s.checkpointEvidenceOf(r.Context(), sc, cp, map[string]checkpointRunEvidence{})
	writeJSON(w, http.StatusOK, map[string]any{"checkpoint": checkpointResponse(cp, evidence)})
}

type checkpointDecisionRequest struct {
	Decision   string         `json:"decision"`
	DecisionMD string         `json:"decision_md"`
	Fields     map[string]any `json:"fields"`
}

func (s *Server) handleDecideCheckpoint(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.engine == nil || s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	var req checkpointDecisionRequest
	if !decodeM3JSON(w, r, &req) {
		return
	}
	cpID := r.PathValue("id")
	kinds := map[string]workflow.CheckpointState{
		"approve": workflow.CheckpointApproved, "approve_with_edits": workflow.CheckpointApprovedWithEdit,
		"reject": workflow.CheckpointRejected, "abort": workflow.CheckpointAborted,
	}
	kind, ok := kinds[req.Decision]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "decision must be approve, approve_with_edits, reject, or abort.")
		return
	}
	if (kind == workflow.CheckpointApprovedWithEdit || kind == workflow.CheckpointRejected) && strings.TrimSpace(req.DecisionMD) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "decision_md is required for this decision.")
		return
	}
	cp, err := s.workflows.GetCheckpoint(r.Context(), sc, cpID)
	if err != nil {
		s.writeRunError(w, err, "checkpoint")
		return
	}
	if err := validateCheckpointFields(cp.FieldsSpec, req.Fields); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	fields, err := json.Marshal(req.Fields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "fields must be a JSON object.")
		return
	}
	decided, err := s.engine.DecideCheckpoint(r.Context(), sc, cpID, workflow.Decision{Kind: kind, DecisionMD: req.DecisionMD, Fields: fields}, sc.UserID)
	if err != nil {
		if errors.Is(err, workflow.ErrAlreadyDecided) {
			message := "That checkpoint was already decided."
			if decided.ID != "" {
				message += " Winning decision: " + string(decided.State)
				if decided.DecisionMD != "" {
					message += " — " + decided.DecisionMD
				}
			}
			writeError(w, http.StatusConflict, "already_decided", message)
			return
		}
		s.writeRunError(w, err, "checkpoint")
		return
	}
	evidence := s.checkpointEvidenceOf(r.Context(), sc, decided, map[string]checkpointRunEvidence{})
	writeJSON(w, http.StatusOK, map[string]any{"checkpoint": checkpointResponse(decided, evidence)})
}

type checkpointFieldSpec struct {
	Key      string   `json:"key"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options"`
}

func validateCheckpointFields(raw json.RawMessage, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	var specs []checkpointFieldSpec
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &specs); err != nil {
			return errors.New("checkpoint fields_spec is malformed.")
		}
	}
	known := make(map[string]checkpointFieldSpec, len(specs))
	for _, spec := range specs {
		known[spec.Key] = spec
	}
	for key, value := range fields {
		spec, ok := known[key]
		if !ok {
			return errors.New("fields contains an unknown key: " + key + ".")
		}
		valid := false
		switch spec.Type {
		case "string", "text":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "number":
			_, valid = value.(float64)
		case "enum":
			var text string
			text, valid = value.(string)
			if valid {
				valid = false
				for _, option := range spec.Options {
					if text == option {
						valid = true
						break
					}
				}
			}
		default:
			valid = true // unknown declared types do not make existing laps unreadable
		}
		if !valid {
			return errors.New("field " + key + " has the wrong type or value.")
		}
	}
	for _, spec := range specs {
		if spec.Required {
			if value, ok := fields[spec.Key]; !ok || value == nil || value == "" {
				return errors.New("field " + spec.Key + " is required.")
			}
		}
	}
	return nil
}

func (s *Server) handleRemindCheckpoint(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.workflows == nil {
		writeWorkflowUnavailable(w)
		return
	}
	cp, err := s.workflows.SetCheckpointRemindAfter(r.Context(), sc, r.PathValue("id"), time.Now().Add(24*time.Hour))
	if err != nil {
		s.writeRunError(w, err, "checkpoint")
		return
	}
	evidence := s.checkpointEvidenceOf(r.Context(), sc, cp, map[string]checkpointRunEvidence{})
	writeJSON(w, http.StatusOK, map[string]any{"checkpoint": checkpointResponse(cp, evidence)})
}
