package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/tenancy"
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
	ID            string                   `json:"id"`
	RunID         string                   `json:"run_id"`
	StepRunID     string                   `json:"step_run_id"`
	NodeID        string                   `json:"node_id"`
	Lap           int                      `json:"lap"`
	State         workflow.CheckpointState `json:"state"`
	SummaryMD     string                   `json:"summary_md"`
	DecisionMD    *string                  `json:"decision_md"`
	Fields        json.RawMessage          `json:"fields"`
	FieldsSpec    json.RawMessage          `json:"fields_spec"`
	DecidedByKind *string                  `json:"decided_by_kind"`
	DecidedBy     *string                  `json:"decided_by"`
	DecidedAt     *time.Time               `json:"decided_at"`
	RemindAfter   *time.Time               `json:"remind_after"`
	CreatedAt     time.Time                `json:"created_at"`
}

func checkpointResponse(cp workflow.Checkpoint) checkpointWire {
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
	return checkpointWire{ID: cp.ID, RunID: cp.RunID, StepRunID: cp.StepRunID, NodeID: cp.NodeID,
		Lap: cp.Lap, State: cp.State, SummaryMD: cp.SummaryMD, DecisionMD: decisionMD,
		Fields: nonNilJSON(cp.Fields, "null"), FieldsSpec: nonNilJSON(cp.FieldsSpec, "[]"),
		DecidedByKind: byKind, DecidedBy: by, DecidedAt: cp.DecidedAt, RemindAfter: cp.RemindAfter,
		CreatedAt: cp.CreatedAt}
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
	for _, cp := range filtered {
		out = append(out, checkpointResponse(cp))
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
	writeJSON(w, http.StatusOK, map[string]any{"checkpoint": checkpointResponse(cp)})
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
	writeJSON(w, http.StatusOK, map[string]any{"checkpoint": checkpointResponse(decided)})
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
	writeJSON(w, http.StatusOK, map[string]any{"checkpoint": checkpointResponse(cp)})
}
