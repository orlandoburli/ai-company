package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// DecideCheckpoint atomically records a decision and applies its graph effect.
func (e *Engine) DecideCheckpoint(ctx context.Context, sc tenancy.Scope, checkpointID string, decision Decision, actorUserID string) (Checkpoint, error) {
	cp, err := e.store.GetCheckpoint(ctx, sc, checkpointID)
	if err != nil {
		return Checkpoint{}, err
	}
	decision.DecidedBy = actorUserID
	decided, err := e.store.DecideCheckpoint(ctx, sc, checkpointID, cp.Lap, decision)
	if err != nil {
		return decided, err
	}
	run, err := e.store.GetRun(ctx, sc, cp.RunID)
	if err != nil {
		return decided, err
	}
	t, err := ParseTemplate(run.DefinitionSnapshot)
	if err != nil {
		return decided, err
	}
	node, _ := t.Node(cp.NodeID)
	step, err := e.store.GetStepRun(ctx, sc, cp.StepRunID)
	if err != nil {
		return decided, err
	}
	if e.agents != nil {
		thread, thErr := e.agents.EnsureRunThread(ctx, sc, run.ID, "Workflow run", run.StartedBy)
		if thErr != nil {
			return decided, thErr
		}
		body := fmt.Sprintf("Checkpoint **%s** decided: %s", node.Name, decided.State)
		if decided.DecisionMD != "" {
			body += "\n\n" + decided.DecisionMD
		}
		if _, thErr = e.agents.AddMessage(ctx, sc, agent.NewMessage{ThreadID: thread.ID, AuthorKind: agent.AuthorSystem, BodyMD: body}); thErr != nil {
			return decided, thErr
		}
	}
	if e.memory != nil {
		payload := map[string]any{"lap": decided.Lap, "state": decided.State, "decision": decided.DecisionMD, "fields": json.RawMessage(decided.Fields), "actor_user_id": actorUserID}
		if _, err = e.memory.Write(ctx, sc, memory.Write{Scope: memory.Scope{Kind: memory.ScopeRun, Ref: run.ID}, Key: fmt.Sprintf("checkpoint.%s.lap.%d", node.ID, decided.Lap), ContentMD: decided.DecisionMD, ContentJSON: rawJSON(payload), Source: memory.SourceCheckpoint, SourceRunID: run.ID, SourceStepRunID: step.ID, SourceUserID: actorUserID}); err != nil {
			return decided, err
		}
	}
	switch decided.State {
	case CheckpointApproved, CheckpointApprovedWithEdit:
		if err = e.finishWaitingStep(ctx, sc, run, t, step, StepSucceeded, "", map[string]json.RawMessage{"approved": rawJSON(map[string]any{"decision": decided.DecisionMD, "fields": json.RawMessage(decided.Fields)})}, map[string]any{"status": "succeeded", "exit_reason": "ok", "summary": decided.DecisionMD, "fields": json.RawMessage(decided.Fields)}); err != nil {
			return decided, err
		}
	case CheckpointRejected:
		cfg := decodeConfig(node)
		policy := stringConfig(cfg, "policy")
		switch policy {
		case "retry_step", "":
			maxCycles := 3
			var max int
			if json.Unmarshal(cfg["max_cycles"], &max) == nil && max > 0 {
				maxCycles = max
			}
			if decided.Lap >= maxCycles {
				if err = e.finishWaitingStep(ctx, sc, run, t, step, StepFailed, string(FailureNeedsInput), nil, map[string]any{"status": "failed", "exit_reason": "checkpoint retry cycle budget exhausted", "failure_kind": FailureNeedsInput, "summary": decided.DecisionMD}); err != nil {
					return decided, err
				}
				break
			}
			oldState := StepSuperseded
			if _, err = e.store.UpdateStepRun(ctx, sc, step.ID, StepRunPatch{State: &oldState, FinishedAt: timePtrNow()}); err != nil {
				return decided, err
			}
			fresh, err := e.store.CreateStepRun(ctx, sc, NewStepRun{RunID: run.ID, NodeID: node.ID, IterationPath: step.IterationPath, RunIndex: step.RunIndex + 1, Kind: string(node.Kind), Name: node.Name, WorkspaceKind: step.WorkspaceKind})
			if err != nil {
				return decided, err
			}
			if len(step.InputEdges) > 0 {
				if _, err = e.store.UpdateStepRun(ctx, sc, fresh.ID, StepRunPatch{InputEdges: &step.InputEdges}); err != nil {
					return decided, err
				}
			}
		case "goto_step":
			ports := map[string]json.RawMessage{"rejected": rawJSON(map[string]string{"feedback": decided.DecisionMD})}
			if err = e.finishWaitingStep(ctx, sc, run, t, step, StepSucceeded, "", ports, map[string]any{"status": "succeeded", "exit_reason": "rejected", "summary": decided.DecisionMD}); err != nil {
				return decided, err
			}
		default:
			return decided, ErrInvalidTransition
		}
	case CheckpointAborted:
		if err = e.finishWaitingStep(ctx, sc, run, t, step, StepFailed, string(FailureDependency), nil, map[string]any{"status": "failed", "exit_reason": "checkpoint aborted", "failure_kind": FailureDependency}); err != nil {
			return decided, err
		}
		failedRun, setErr := e.store.SetRunState(ctx, sc, run.ID, RunFailed, string(FailureDependency), "checkpoint aborted")
		if setErr == nil {
			e.notifyRunFailure(ctx, sc, failedRun)
		}
		_ = e.cancelRemaining(ctx, sc, run.ID, step.ID)
	}
	return decided, nil
}

func timePtrNow() *time.Time { now := time.Now(); return &now }

func (e *Engine) finishWaitingStep(ctx context.Context, sc tenancy.Scope, run Run, t *Template, step StepRun, state StepState, failure string, ports map[string]json.RawMessage, result any) error {
	// The public store intentionally exposes CAS claiming for ready work. A human
	// decision is the sole waiting->running transition, guarded by its checkpoint
	// CAS above; retain a lease token and then use the same atomic terminal+receipt
	// transaction as an agent completion.
	lock := step.ClaimLock
	if lock == "" {
		lock = newID()
	}
	err := e.store.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE step_runs SET state='running',claim_lock=$2,claim_expires_at=now()+interval '2 minutes' WHERE id=$1 AND state='waiting'`, step.ID, lock)
		return err
	})
	if err != nil {
		return err
	}
	current, err := e.store.GetStepRun(ctx, sc, step.ID)
	if err != nil {
		return err
	}
	if current.ClaimLock == "" {
		current.ClaimLock = lock
	}
	return e.terminalWithReceipts(ctx, sc, run, t, current, state, rawJSON(result), ports, failure)
}

// Intervene records an ordered steering event and applies stop/redo actions.
func (e *Engine) Intervene(ctx context.Context, sc tenancy.Scope, in NewIntervention) (Intervention, error) {
	if in.ActorKind == "" {
		in.ActorKind = "human"
	}
	if in.ActorID == "" {
		in.ActorID = sc.UserID
	}
	created, err := e.store.CreateIntervention(ctx, sc, in)
	if err != nil {
		return Intervention{}, err
	}
	run, err := e.store.GetRun(ctx, sc, in.RunID)
	if err != nil {
		return created, err
	}
	if e.agents != nil {
		thread, thErr := e.agents.EnsureRunThread(ctx, sc, run.ID, "Workflow run", run.StartedBy)
		if thErr == nil {
			_, thErr = e.agents.AddMessage(ctx, sc, agent.NewMessage{ThreadID: thread.ID, AuthorKind: agent.AuthorSystem, BodyMD: fmt.Sprintf("Intervention (%s, seq %d): %s", in.Kind, created.Seq, in.BodyMD)})
		}
		if thErr != nil {
			return created, thErr
		}
	}
	if e.memory != nil && in.BodyMD != "" {
		_, err = e.memory.Write(ctx, sc, memory.Write{Scope: memory.Scope{Kind: memory.ScopeRun, Ref: run.ID}, Key: fmt.Sprintf("intervention.%s.%d", in.NodeID, created.Seq), ContentMD: in.BodyMD, Source: memory.SourceIntervention, SourceRunID: run.ID, SourceStepRunID: in.StepRunID, SourceUserID: in.ActorID})
		if err != nil {
			return created, err
		}
	}
	switch in.Kind {
	case InterventionRedo:
		if in.NodeID == "" {
			return created, ErrInvalidTransition
		}
		err = e.RedoNode(ctx, sc, run.ID, in.NodeID, in.BodyMD, in.ActorID)
	case InterventionStop:
		if in.StepRunID == "" {
			_, err = e.CancelRun(ctx, sc, run.ID)
		} else {
			err = e.stopStep(ctx, sc, run.ID, in.StepRunID)
		}
	case InterventionInject:
		if in.StepRunID == "" {
			return created, ErrInvalidTransition
		}
		step, stepErr := e.store.GetStepRun(ctx, sc, in.StepRunID)
		if stepErr != nil {
			return created, stepErr
		}
		if step.State == StepRunning && step.AgentJobID != "" && e.agents != nil {
			// Stop-and-resume: the CLI adapter has no live input, so the run is
			// stopped, the job is put back in the queue and the step is reset.
			// The next dispatch appends the instruction to the context and
			// marks the intervention delivered.
			_, cancelErr := e.agents.CancelJob(ctx, sc, step.AgentJobID)
			if cancelErr != nil && !errors.Is(cancelErr, agent.ErrNotRunning) {
				return created, cancelErr
			}
			// The job is re-queued by the next dispatch (the store restarts a
			// cancelled job under the same idempotency key), so the instruction
			// is in the prompt before the adapter starts.
			if _, resetErr := e.store.ResetStep(ctx, sc, step.ID); resetErr != nil && !errors.Is(resetErr, ErrNotFound) {
				return created, resetErr
			}
		}
	default:
		return created, ErrInvalidTransition
	}
	if err != nil {
		return created, err
	}
	return created, nil
}

func (e *Engine) stopStep(ctx context.Context, sc tenancy.Scope, runID, stepID string) error {
	step, err := e.store.GetStepRun(ctx, sc, stepID)
	if err != nil {
		return err
	}
	if step.RunID != runID {
		return ErrNotFound
	}
	if step.State == StepRunning && step.AgentJobID != "" && e.agents != nil {
		_, err = e.agents.CancelJob(ctx, sc, step.AgentJobID)
		if err != nil && !errors.Is(err, agent.ErrNotRunning) {
			return err
		}
	}
	if step.State == StepPending || step.State == StepReady || step.State == StepRunning || step.State == StepWaiting {
		state := StepCancelled
		_, err = e.store.UpdateStepRun(ctx, sc, step.ID, StepRunPatch{State: &state})
	}
	return err
}

func (e *Engine) cancelRemaining(ctx context.Context, sc tenancy.Scope, runID, except string) error {
	rows, err := e.store.ListStepRuns(ctx, sc, runID)
	if err != nil {
		return err
	}
	for _, s := range rows {
		if s.ID == except || s.State == StepSucceeded || s.State == StepFailed || s.State == StepSkipped || s.State == StepBlocked || s.State == StepSuperseded || s.State == StepCancelled {
			continue
		}
		if s.AgentJobID != "" && e.agents != nil {
			_, _ = e.agents.CancelJob(ctx, sc, s.AgentJobID)
		}
		state := StepCancelled
		if _, err = e.store.UpdateStepRun(ctx, sc, s.ID, StepRunPatch{State: &state}); err != nil {
			return err
		}
	}
	return nil
}

// RedoNode resets a node and graph descendants, leaving unaffected siblings alone.
func (e *Engine) RedoNode(ctx context.Context, sc tenancy.Scope, runID, nodeID, instructions, actorUserID string) error {
	run, err := e.store.GetRun(ctx, sc, runID)
	if err != nil {
		return err
	}
	if terminalRun(run.State) {
		return ErrTerminal
	}
	t, err := ParseTemplate(run.DefinitionSnapshot)
	if err != nil {
		return err
	}
	rows, err := e.store.ListStepRuns(ctx, sc, runID)
	if err != nil {
		return err
	}
	p, err := Redo(t, nodeID, projectSteps(t, rows))
	if err != nil {
		return err
	}
	resetIDs := map[string]bool{nodeID: true}
	for _, id := range t.Descendants(nodeID) {
		resetIDs[id] = true
	}
	for _, s := range rows {
		if resetIDs[s.NodeID] && s.AgentJobID != "" && s.State == StepRunning && e.agents != nil {
			_, _ = e.agents.CancelJob(ctx, sc, s.AgentJobID)
		}
	}
	if err = e.store.SupersedeSteps(ctx, sc, runID, []string{nodeID}); err != nil {
		return err
	}
	for _, fresh := range p.Reset {
		n, _ := t.Node(fresh.NodeID)
		created, createErr := e.store.CreateStepRun(ctx, sc, NewStepRun{RunID: runID, NodeID: fresh.NodeID, IterationPath: fresh.IterationPath, RunIndex: fresh.RunIndex, Kind: string(n.Kind), Name: n.Name, WorkspaceKind: string(n.Workspace)})
		if createErr != nil {
			if errors.Is(createErr, ErrDuplicateStep) {
				continue
			}
			return createErr
		}
		if len(fresh.Incoming) > 0 {
			if _, err = e.store.UpdateStepRun(ctx, sc, created.ID, StepRunPatch{InputEdges: &fresh.Incoming}); err != nil {
				return err
			}
		}
	}
	for _, r := range p.Receipts {
		for _, edge := range t.Edges {
			if edge.ID != r.EdgeID {
				continue
			}
			if target, ok := latestStepForRun(ctx, e.store, sc, runID, edge.To.Node); ok {
				_, _ = e.store.MergeStepReceipts(ctx, sc, target.ID, map[string]Receipt{r.EdgeID: r})
			}
		}
	}
	if instructions != "" && e.memory != nil {
		_, err = e.memory.Write(ctx, sc, memory.Write{Scope: memory.Scope{Kind: memory.ScopeRun, Ref: runID}, Key: fmt.Sprintf("redo.%s.%d", nodeID, len(rows)), ContentMD: instructions, Source: memory.SourceIntervention, SourceRunID: runID, SourceUserID: actorUserID})
		if err != nil {
			return err
		}
	}
	return nil
}
