package workflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/command"
	"github.com/orlandoburli/ai-company/internal/ctxbundle"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/notification"
	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/skill"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workspace"
)

func projectSteps(t *Template, rows []StepRun) []PersistedStep {
	nodes := make(map[string]Node, len(t.Nodes))
	for _, n := range t.Nodes {
		nodes[n.ID] = n
	}
	out := make([]PersistedStep, 0, len(rows))
	for _, row := range rows {
		node := nodes[row.NodeID]
		p := PersistedStep{NodeID: row.NodeID, IterationPath: row.IterationPath, RunIndex: row.RunIndex,
			State: row.State, Kind: NodeKind(row.Kind), OnError: node.OnError, FailureKind: FailureKind(row.FailureKind),
			OutputPorts: row.OutputPorts, Incoming: row.InputEdges}
		var result struct {
			ExitReason string `json:"exit_reason"`
			Question   string `json:"question"`
			Lap        int    `json:"lap"`
			Iteration  int    `json:"iteration"`
			Attempt    int    `json:"attempt"`
		}
		_ = json.Unmarshal(row.Result, &result)
		p.ExitReason, p.Question, p.Lap, p.Iteration, p.Attempt = result.ExitReason, result.Question, result.Lap, result.Iteration, result.Attempt
		if p.Lap == 0 && node.Kind == NodeCheckpoint {
			p.Lap = 1
		}
		out = append(out, p)
	}
	return out
}

func (e *Engine) finalizeJobs(ctx context.Context, sc tenancy.Scope, run Run, t *Template, rows []StepRun) error {
	for _, step := range rows {
		if step.State != StepRunning || step.AgentJobID == "" || e.agents == nil {
			continue
		}
		job, err := e.agents.GetJob(ctx, sc, step.AgentJobID)
		if err != nil {
			return err
		}
		if !job.State.Terminal() {
			continue
		}
		if job.State == agent.JobCancelled {
			// The job was stopped on purpose — a steered execution that is
			// about to be resumed, or a cancelled run. The step is left alone:
			// the intervention resets it, the run cancel marks it cancelled.
			continue
		}
		node, _ := t.Node(step.NodeID)
		status := StepSucceeded
		failure := ""
		ports := map[string]json.RawMessage{}
		result := map[string]any{"status": "succeeded", "exit_reason": "ok"}
		var parsedMarkers agentMarkerResult
		var output string
		if job.State == agent.JobFailed {
			status = StepFailed
			failure = "capability"
			if job.FailureKind != nil && FailureKind(*job.FailureKind) == FailureTransient {
				failure = string(FailureTransient)
			}
			if job.FailureKind != nil && FailureKind(*job.FailureKind) == FailureDependency {
				failure = string(FailureDependency)
			}
			message := "agent job failed"
			if job.ErrorText != nil {
				message = *job.ErrorText
			}
			// The design's own rule: a capability failure tries the agent's
			// fallback chain — which is the runner chain now — and only then
			// fails the node.
			if failure == string(FailureCapability) {
				next, ok, fallbackErr := e.fallbackRunner(ctx, sc, node, step)
				if fallbackErr != nil {
					return fallbackErr
				}
				if ok {
					if fallbackErr = e.redispatchWithRunner(ctx, sc, run, t, node, step, next); fallbackErr != nil {
						return fallbackErr
					}
					e.logger.Info("workflow runner fallback",
						"run_id", run.ID, "node_id", node.ID, "step_id", step.ID, "runner", next.Name)
					continue
				}
			}
			result = map[string]any{"status": "failed", "exit_reason": message, "failure_kind": failure, "summary": message}
		} else {
			var decoded map[string]json.RawMessage
			if len(job.Result) > 0 {
				_ = json.Unmarshal(job.Result, &decoded)
			}
			if len(decoded) > 0 {
				_ = json.Unmarshal(decoded["output"], &output)
			}
			if len(decoded) == 0 {
				_ = json.Unmarshal(job.Result, &output)
			}
			if len(decoded) > 0 && len(decoded["data"]) > 0 {
				var candidate map[string]json.RawMessage
				if json.Unmarshal(decoded["data"], &candidate) == nil {
					if _, ok := candidate["main"]; ok {
						ports = candidate
					} else if len(candidate) > 0 {
						ports["main"] = json.RawMessage(decoded["data"])
					}
				}
			}
			if _, ok := ports["main"]; !ok {
				ports["main"] = rawJSON(map[string]string{"text": output})
			}
			parsedMarkers = e.inspectAgentMarkers(ctx, sc, run, node, output)
			if parsedMarkers.Failure != "" {
				status, failure = StepFailed, parsedMarkers.Failure
				result = map[string]any{"status": "failed", "exit_reason": parsedMarkers.FailureText, "failure_kind": failure, "summary": output, "output": output}
			} else {
				if parsedMarkers.HasOutput {
					ports["main"] = rawJSON(parsedMarkers.Output)
				}
				// The downstream integration needs the server-created branch and workdir.
				ports["main"] = withWorkLocation(ports["main"], step.ContextManifest)
				result["summary"] = output
				result["output"] = output
				if parsedMarkers.HasOutput {
					result["outputs"] = parsedMarkers.Output
				}
			}
			if parsedMarkers.Publish != "" {
				result["publish_proposal"] = parsedMarkers.Publish
			}
		}
		result["tokens_in"], result["tokens_out"], result["cost_usd"] = job.TokensIn, job.TokensOut, job.CostUSD
		// The step says which runner ran it: the transport is part of the
		// result, not only of the job.
		trace := readRunnerTrace(step)
		if trace.Runner != "" {
			result["runner"] = trace.Runner
			result["runner_attempt"] = trace.Attempt
			result["runners_tried"] = trace.Tried
		}
		// The step says WHICH credential the runner ran under: the reference
		// is a name, so it is safe to keep next to the result. It is what
		// makes the run's GitHub work run under the agent's identity.
		if trace.Credential != "" {
			result["runner_credential"] = trace.Credential
		}
		var contextInfo struct {
			WorkspaceNote string   `json:"workspace_note"`
			Dropped       []string `json:"dropped_markers"`
		}
		_ = json.Unmarshal(step.ContextManifest, &contextInfo)
		parsedMarkers.Dropped = append(parsedMarkers.Dropped, contextInfo.Dropped...)
		if len(parsedMarkers.Dropped) > 0 {
			result["dropped_markers"] = parsedMarkers.Dropped
		}
		if contextInfo.WorkspaceNote != "" {
			result["workspace_note"] = contextInfo.WorkspaceNote
		}
		terminalErr := e.terminalWithReceipts(ctx, sc, run, t, step, status, rawJSON(result), ports, failure)
		if terminalErr != nil && !errors.Is(terminalErr, ErrClaimLost) {
			return terminalErr
		}
		if terminalErr == nil && status == StepSucceeded && job.State == agent.JobSucceeded {
			e.writeAgentMarkers(ctx, sc, run, step, node, parsedMarkers)
			if node.Workspace == WorkspaceWorktree {
				e.recordAgentFileArtifacts(ctx, sc, run, step, node, parsedMarkers.Output)
			}
		}
	}
	return nil
}

// recordAgentFileArtifacts records the files a successful worktree agent
// execution produced. Validated changed_files output is preferred when
// declared; otherwise the worktree's scrubbed git status/diff is authoritative.
func (e *Engine) recordAgentFileArtifacts(ctx context.Context, sc tenancy.Scope, run Run, step StepRun, node Node, outputs map[string]any) {
	files := changedFilesForAgent(ctx, step.Workdir, node, outputs)
	const maxFiles = 50
	shown := files
	if len(shown) > maxFiles {
		shown = shown[:maxFiles]
	}
	for _, file := range shown {
		meta := map[string]any{"path": file.Path, "status": file.Status}
		if file.HasLines {
			meta["lines"] = file.Lines
		}
		if _, err := e.store.CreateArtifact(ctx, sc, NewArtifact{
			RunID: run.ID, StepRunID: step.ID, ItemID: run.ItemID,
			Kind: "file", URI: file.Path, Meta: rawJSON(meta),
		}); err != nil {
			e.logger.Warn("persist agent file artifact", "run", run.ID, "step", step.ID, "path", file.Path, "error", err)
		}
	}
	if len(files) > maxFiles {
		message := fmt.Sprintf("%d files changed; showing the first %d", len(files), maxFiles)
		if _, err := e.store.CreateArtifact(ctx, sc, NewArtifact{
			RunID: run.ID, StepRunID: step.ID, ItemID: run.ItemID,
			Kind: "report", URI: "changed files", Meta: rawJSON(map[string]any{"message": message, "total": len(files), "shown": maxFiles}),
		}); err != nil {
			e.logger.Warn("persist agent file summary", "run", run.ID, "step", step.ID, "error", err)
		}
	}
}

func (e *Engine) terminalWithReceipts(ctx context.Context, sc tenancy.Scope, run Run, t *Template, step StepRun, state StepState, result json.RawMessage, ports map[string]json.RawMessage, failure string) error {
	projected := PersistedStep{NodeID: step.NodeID, State: state, Kind: NodeKind(step.Kind), OnError: nodeOnError(t, step.NodeID), FailureKind: FailureKind(failure), OutputPorts: ports}
	receipts := EmitReceipts(t, projected)
	downstream := make(map[string]map[string]Receipt)
	for _, receipt := range receipts {
		for _, edge := range t.Edges {
			if edge.ID != receipt.EdgeID {
				continue
			}
			target, ok := latestStepForRun(ctx, e.store, sc, run.ID, edge.To.Node)
			if !ok {
				continue
			}
			if downstream[target.ID] == nil {
				downstream[target.ID] = map[string]Receipt{}
			}
			downstream[target.ID][receipt.EdgeID] = receipt
		}
	}
	updates := make([]DownstreamReceipt, 0, len(downstream))
	for id, rs := range downstream {
		updates = append(updates, DownstreamReceipt{StepRunID: id, Receipts: rs})
	}
	_, err := e.store.SetStepTerminalWithReceipts(ctx, sc, step.ID, step.ClaimLock, state, result, ports, failure, updates)
	if err != nil {
		return err
	}
	return nil
}

func latestStepForRun(ctx context.Context, st *Store, sc tenancy.Scope, runID, nodeID string) (StepRun, bool) {
	rows, err := st.ListStepRuns(ctx, sc, runID)
	if err != nil {
		return StepRun{}, false
	}
	return latestStep(rows, nodeID, "")
}

func nodeOnError(t *Template, id string) OnError { n, _ := t.Node(id); return n.OnError }

func (e *Engine) applyGenerated(ctx context.Context, sc tenancy.Scope, run Run, t *Template, rows []StepRun, plan Plan) error {
	generated := append([]PersistedStep{}, plan.GeneratedSkipped...)
	generated = append(generated, plan.GeneratedFailed...)
	// Dependency-blocked nodes are materialized as failed terminal executions.
	for _, id := range plan.Blocked {
		if old, ok := latestStep(rows, id, ""); ok && (old.State == StepBlocked || old.State == StepFailed || old.State == StepSucceeded || old.State == StepSkipped || old.State == StepCancelled || old.State == StepSuperseded) {
			continue
		}
		n, _ := t.Node(id)
		blocked := PersistedStep{NodeID: id, State: StepBlocked, Kind: n.Kind, OnError: n.OnError, FailureKind: FailureDependency, ExitReason: "dependency blocked"}
		if old, ok := latestStep(rows, id, ""); ok {
			blocked.RunIndex = old.RunIndex
			blocked.IterationPath = old.IterationPath
		}
		generated = append(generated, blocked)
	}
	for _, p := range generated {
		idx := p.RunIndex
		if p.State == StepSkipped || p.State == StepBlocked {
			if old, ok := latestStep(rows, p.NodeID, p.IterationPath); ok {
				idx = old.RunIndex
			}
		}
		step, ok := findStep(rows, p.NodeID, p.IterationPath, idx)
		if !ok {
			n, _ := t.Node(p.NodeID)
			created, err := e.store.CreateStepRun(ctx, sc, NewStepRun{RunID: run.ID, NodeID: p.NodeID, IterationPath: p.IterationPath, RunIndex: idx, Kind: string(n.Kind), Name: n.Name, WorkspaceKind: string(n.Workspace)})
			if err != nil {
				if errors.Is(err, ErrDuplicateStep) {
					continue
				}
				return err
			}
			step = created
		}
		if step.State == StepSucceeded || step.State == StepFailed || step.State == StepSkipped || step.State == StepBlocked {
			continue
		}
		claimed, err := e.claim(ctx, sc, step)
		if err != nil {
			if errors.Is(err, ErrClaimLost) {
				continue
			}
			return err
		}
		result := rawJSON(map[string]any{"status": p.State, "exit_reason": p.ExitReason, "failure_kind": p.FailureKind, "iteration": p.Iteration, "summary": p.ExitReason})
		failure := string(p.FailureKind)
		if err := e.terminalWithReceipts(ctx, sc, run, t, claimed, p.State, result, nil, failure); err != nil && !errors.Is(err, ErrClaimLost) {
			return err
		}
	}
	return nil
}

func findStep(rows []StepRun, node, path string, index int) (StepRun, bool) {
	for _, s := range rows {
		if s.NodeID == node && s.IterationPath == path && s.RunIndex == index {
			return s, true
		}
	}
	return StepRun{}, false
}

func (e *Engine) dispatchStep(ctx context.Context, sc tenancy.Scope, run Run, t *Template, rows []StepRun, ready ReadyNode) error {
	node, ok := t.Node(ready.NodeID)
	if !ok {
		return fmt.Errorf("unknown planned node %q", ready.NodeID)
	}
	step, exists := findStep(rows, ready.NodeID, ready.IterationPath, ready.RunIndex)
	if !exists {
		var err error
		step, err = e.store.CreateStepRun(ctx, sc, NewStepRun{RunID: run.ID, NodeID: node.ID, IterationPath: ready.IterationPath, RunIndex: ready.RunIndex, Kind: string(node.Kind), Name: node.Name, WorkspaceKind: string(node.Workspace)})
		if err != nil {
			if errors.Is(err, ErrDuplicateStep) {
				return nil
			}
			return err
		}
	}
	if len(ready.Inputs) > 0 {
		if _, err := e.store.UpdateStepRun(ctx, sc, step.ID, StepRunPatch{InputEdges: &ready.Inputs}); err != nil {
			return err
		}
	}
	claimed, err := e.claim(ctx, sc, step)
	if err != nil {
		if errors.Is(err, ErrClaimLost) {
			return nil
		}
		return err
	}
	switch node.Kind {
	case NodeAgent:
		return e.dispatchAgent(ctx, sc, run, t, node, claimed, ready)
	case NodeCheckpoint:
		return e.dispatchCheckpoint(ctx, sc, run, t, node, claimed, ready)
	case NodeMerge:
		out := map[string]json.RawMessage{"main": rawJSON(ready.Inputs)}
		res := rawJSON(map[string]any{"status": "succeeded", "exit_reason": "ok", "summary": "Inputs reconciled"})
		return e.terminalWithReceipts(ctx, sc, run, t, claimed, StepSucceeded, res, out, "")
	case NodeLoop:
		cfg := decodeConfig(node)
		// The until condition decides whether this iteration leaves the loop
		// ("done") or enters its body again ("body"). A condition that cannot
		// be evaluated keeps the loop iterating: the iteration budget, not a
		// timeout, is the backstop.
		port := "body"
		if EvaluateUntil(cfg["until"], LoopCycleValue(ready.Inputs)) {
			port = "done"
		}
		out := map[string]json.RawMessage{port: rawJSON(map[string]int{"iteration": ready.Iteration})}
		res := rawJSON(map[string]any{"status": "succeeded", "exit_reason": "ok", "iteration": ready.Iteration, "summary": "loop " + port})
		return e.terminalWithReceipts(ctx, sc, run, t, claimed, StepSucceeded, res, out, "")
	case NodeIntegration:
		return e.runIntegration(ctx, sc, run, t, claimed, ready, node)
	default:
		return e.failClaim(ctx, sc, run, t, claimed, "capability", "unsupported workflow node")
	}
}

func (e *Engine) claim(ctx context.Context, sc tenancy.Scope, step StepRun) (StepRun, error) {
	lock := uuid.NewString()
	return e.store.ClaimStepRun(ctx, sc, step.ID, lock, e.lease)
}

func (e *Engine) failClaim(ctx context.Context, sc tenancy.Scope, run Run, t *Template, step StepRun, kind, message string) error {
	res := rawJSON(map[string]any{"status": "failed", "exit_reason": message, "failure_kind": kind, "summary": message})
	return e.terminalWithReceipts(ctx, sc, run, t, step, StepFailed, res, nil, kind)
}

func (e *Engine) dispatchAgent(ctx context.Context, sc tenancy.Scope, run Run, t *Template, node Node, step StepRun, ready ReadyNode) error {
	return e.dispatchAgentOn(ctx, sc, run, t, node, step, ready, nil, 1)
}

// dispatchAgentOn queues the node's job.
//
// The transport is nil for an ordinary dispatch: the engine then resolves one
// from the node's config, the agent's chain and the runner's budget. It is
// set for a fallback, where the chain has already chosen the runner the step
// is re-dispatched on.
//
// The attempt counts the runner attempts of this step: the first dispatch is
// attempt 1, and every fallback advances it. The attempt is part of the
// job's idempotency key, so a fallback queues a NEW job instead of
// re-attaching to the failed one.
func (e *Engine) dispatchAgentOn(ctx context.Context, sc tenancy.Scope, run Run, t *Template, node Node, step StepRun, ready ReadyNode, transport *runner.Runner, attempt int) error {
	if e.agents == nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", "agent store is not configured")
	}
	cfg := decodeConfig(node)
	agentID := stringConfig(cfg, "agent_id")
	a, err := e.agents.GetAgent(ctx, sc, agentID)
	if err != nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", err.Error())
	}
	if transport == nil {
		resolved, resolveErr := e.ResolveRunner(ctx, sc, run, node, a.ID)
		if resolveErr != nil {
			return e.failClaim(ctx, sc, run, t, step, "capability", resolveErr.Error())
		}
		transport = &resolved
	}
	var item work.Item
	if run.ItemID != "" && e.work != nil {
		item, err = e.work.GetItem(ctx, sc, run.ItemID)
		if err != nil {
			return e.failClaim(ctx, sc, run, t, step, "dependency", err.Error())
		}
	}
	workdir := ""
	branch := ""
	scratchReason := ""
	if node.Workspace == WorkspaceWorktree {
		if step.Workdir != "" {
			// The step already has a workspace from an earlier attempt: the
			// fallback continues in it rather than preparing a second one.
			workdir = step.Workdir
			branch = stepBranch(step)
		} else {
			productID, repoURL := e.productForItem(ctx, sc, item)
			if e.workspaces == nil || productID == "" || repoURL == "" {
				scratchReason = "No repository mirror is configured; this execution uses a scratch workspace."
			} else {
				ws, prepErr := e.workspaces.Prepare(ctx, workspace.Request{ProductID: productID, SourceURL: repoURL, RunID: run.ID, StepRunID: step.ID, ItemID: item.ID, Attempt: maxInt(1, ready.Attempt)})
				if prepErr != nil {
					// A repository that cannot be mirrored (missing, unreachable,
					// private) must not fail the run: the execution falls back to a
					// scratch directory and says so in its result. Failing the node
					// would turn an environment problem into a broken workflow.
					scratchReason = "No repository mirror could be prepared (" + prepErr.Error() + "); this execution uses a scratch workspace."
				} else {
					workdir = ws.Dir
					branch = ws.Branch
				}
			}
		}
	}
	thread, err := e.agents.EnsureRunThread(ctx, sc, run.ID, "Workflow run", run.StartedBy)
	if err != nil {
		return err
	}
	memEntries, err := e.readNodeMemory(ctx, sc, run, item, a, node)
	if err != nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", err.Error())
	}
	promptTmpl := stringConfig(cfg, "prompt_tmpl")
	if promptTmpl == "" {
		promptTmpl = node.Name
	}
	promptTmpl = strings.ReplaceAll(promptTmpl, "{{item.title}}", item.Title)
	promptTmpl = strings.ReplaceAll(promptTmpl, "{{item.description}}", item.DescriptionMD)
	var task strings.Builder
	task.WriteString(promptTmpl)
	// The persona is the persona: the task says WHAT to do, never WHICH
	// transport carries it out. The runner is the platform's business, not
	// the agent's.
	if item.ID != "" {
		fmt.Fprintf(&task, "\n\nWork item: %s\nStatus: %s\n\n%s", item.Title, item.Status, item.DescriptionMD)
	}
	var sections []ctxbundle.Section
	// External issue text is context only: it is never a skill selector and is
	// never consulted for memory keys, scope references, or capabilities.
	issueSection, issueMarkers := e.issueBodySection(ctx, sc, run, item)
	if issueSection != nil {
		sections = append(sections, *issueSection)
	}
	if len(ready.Inputs) > 0 {
		keys := make([]string, 0, len(ready.Inputs))
		for edge := range ready.Inputs {
			keys = append(keys, edge)
		}
		sort.Strings(keys)
		for _, edge := range keys {
			receipt := ready.Inputs[edge]
			value := string(receipt.Value)
			if value == "" {
				value = "(empty)"
			}
			sections = append(sections, ctxbundle.Section{Name: "incoming-" + edge, Trust: ctxbundle.TrustUntrusted, Ref: edge, Content: fmt.Sprintf("Edge %s from %s port %s (%s):\n%s", edge, receipt.FromNode, receipt.Port, receipt.State, value), Provenance: ctxbundle.Provenance{System: "workflow receipt", Ref: edge}})
		}
	}
	if scratchReason != "" {
		fmt.Fprintf(&task, "\n\nWorkspace note: %s", scratchReason)
	}
	interventions, err := e.store.PendingInterventions(ctx, sc, step.ID)
	if err != nil {
		return err
	}
	for _, in := range interventions {
		if in.Kind == InterventionInject && in.BodyMD != "" {
			fmt.Fprintf(&task, "\n\nAdditional operator instruction (seq %d):\n%s", in.Seq, in.BodyMD)
		}
	}
	skills, err := e.selectSkills(ctx, sc, item, a, node, task.String())
	if err != nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", err.Error())
	}
	// The soul is a column, not a versioned row, so its fingerprint is its
	// content hash: the manifest then says exactly which persona was used.
	bundle, err := ctxbundle.Assemble(ctxbundle.AssembleInput{Soul: a.SoulMD, SoulFingerprint: fingerprint(a.SoulMD), Memory: memEntries, Task: task.String(), Sections: sections, Skills: skills})
	if err != nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", err.Error())
	}
	bundle.DroppedMarkers = append(bundle.DroppedMarkers, issueMarkers...)
	contextJSON := rawJSON(map[string]any{"manifest": bundle.Manifest, "memory_manifest": bundle.MemoryManifest, "trimmed": bundle.Trimmed, "task_file_sha": bundle.TaskFileSHA, "dropped_markers": bundle.DroppedMarkers, "inputs": ready.Inputs})
	key := jobKey(run.ID, node.ID, ready.IterationPath, ready.RunIndex, attempt)
	// The job carries a SNAPSHOT of the runner: its id, its kind and its
	// config. A queued job never changes when somebody edits the runner, and
	// the worker never has to read the runner to know how to run.
	job, err := e.agents.CreateJob(ctx, sc, agent.NewJob{AgentID: a.ID, ThreadID: thread.ID, ItemID: item.ID, StepRunID: step.ID, IdempotencyKey: key, Prompt: bundle.Prompt, Context: contextJSON, Workdir: workdir, CreatedBy: run.StartedBy,
		RunnerID: transport.ID, RunnerKind: string(transport.Kind), RunnerConfig: transport.Config})
	if err != nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", err.Error())
	}
	state := StepRunning
	jobID := job.ID
	wk := string(node.Workspace)
	wd := workdir
	if branch == "" {
		// No worktree: the execution still gets a branch name, so a downstream
		// integration has something stable to talk about.
		branch = fmt.Sprintf("aic/%s-%s-%d", item.ID, step.ID, maxInt(1, ready.Attempt))
	}
	contextManifest := rawJSON(map[string]any{"manifest": bundle.Manifest, "trimmed": bundle.Trimmed, "workspace_note": scratchReason, "dropped_markers": bundle.DroppedMarkers, "branch": branch, "workdir": workdir})
	// The step remembers which runner it is on: the result says what ran, and
	// the tried list is what keeps a fallback from retrying the same runner.
	// The credential reference travels with it: it is the identity the run's
	// work runs under.
	trace := readRunnerTrace(step).with(transport.Name, attempt, transport.CredentialRef)
	stepResultFields := map[string]any{"status": "running", "runner": trace.Runner, "runner_attempt": trace.Attempt, "runners_tried": trace.Tried}
	if trace.Credential != "" {
		stepResultFields["runner_credential"] = trace.Credential
	}
	stepResult := rawJSON(stepResultFields)
	_, err = e.store.UpdateStepRun(ctx, sc, step.ID, StepRunPatch{State: &state, AgentJobID: &jobID, MemoryManifest: ptrRaw(rawJSON(bundle.MemoryManifest)), ContextManifest: ptrRaw(contextManifest), TaskFileSHA: &bundle.TaskFileSHA, Workdir: &wd, WorkspaceKind: &wk, Result: ptrRaw(stepResult)})
	if err != nil {
		return err
	}
	for _, in := range interventions {
		if in.Kind == InterventionInject {
			_, _ = e.store.MarkInterventionDelivered(ctx, sc, in.ID)
		}
	}
	// An idempotency replay may attach to an already terminal job; finalize it now.
	if job.State.Terminal() {
		fresh, _ := e.store.GetStepRun(ctx, sc, step.ID)
		return e.finalizeJobs(ctx, sc, run, t, []StepRun{fresh})
	}
	return nil
}

// issueBodySection returns the most recently fetched issue_body artifact for
// this run and item. The body remains untrusted through ctxbundle; marker names
// are neutralized as text and recorded as dropped, so external text cannot
// cause skill loads, memory writes, marker actions, or scope expansion.
func (e *Engine) issueBodySection(ctx context.Context, sc tenancy.Scope, run Run, item work.Item) (*ctxbundle.Section, []string) {
	if e.store == nil || run.ID == "" || item.ID == "" {
		return nil, nil
	}
	artifacts, err := e.store.ListArtifacts(ctx, sc, run.ID)
	if err != nil {
		e.logger.Warn("read issue body artifact", "run", run.ID, "error", err)
		return nil, nil
	}
	for i := len(artifacts) - 1; i >= 0; i-- {
		artifact := artifacts[i]
		if artifact.Kind != "issue_body" || artifact.ItemID != item.ID {
			continue
		}
		var meta struct {
			Repository  string `json:"repository"`
			IssueNumber int    `json:"issue_number"`
			Author      string `json:"author"`
			Timestamp   string `json:"timestamp"`
			Title       string `json:"title"`
			Body        string `json:"body"`
		}
		if err := json.Unmarshal(artifact.Meta, &meta); err != nil {
			e.logger.Warn("decode issue body artifact", "artifact", artifact.ID, "error", err)
			return nil, nil
		}
		content := "Title: " + meta.Title + "\n\n" + meta.Body
		content, dropped := neutralizeExternalMarkers(content, "issue-body")
		section := &ctxbundle.Section{
			Name: "issue-body", Trust: ctxbundle.TrustUntrusted, Ref: artifact.URI, Content: content,
			Provenance: ctxbundle.Provenance{System: "GitHub issue", Ref: fmt.Sprintf("%s#%d", meta.Repository, meta.IssueNumber), Author: meta.Author, At: meta.Timestamp},
		}
		return section, dropped
	}
	return nil, nil
}

func neutralizeExternalMarkers(text, section string) (string, []string) {
	var dropped []string
	for _, marker := range []string{"AIC_PUBLISH", "AIC_MEMORIZE", "AIC_OUTPUT", "AIC_SPAWN"} {
		if strings.Contains(text, marker) {
			dropped = append(dropped, section+":"+marker)
			text = strings.ReplaceAll(text, marker, "[ignored untrusted marker]")
		}
	}
	return text, dropped
}

func ptrRaw(v json.RawMessage) *json.RawMessage { return &v }
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// jobKey is the idempotency key of a node's job.
//
// The runner attempt is part of the key: a fallback queues a NEW job instead
// of re-attaching to the job that failed.
func jobKey(runID, nodeID, path string, runIndex, attempt int) string {
	key := fmt.Sprintf("run:%s:%s:%s:%d", runID, nodeID, path, runIndex)
	if attempt > 1 {
		key = fmt.Sprintf("%s:runner:%d", key, attempt)
	}
	return key
}

// runnerTrace is what a step remembers about the runners it has tried.
type runnerTrace struct {
	// Attempt counts the runner attempts of this step; the first one is 1.
	Attempt int `json:"runner_attempt"`
	// Runner is the name of the runner the step is on.
	Runner string `json:"runner"`
	// Tried lists the runners the step has tried, in order.
	Tried []string `json:"runners_tried"`
	// Credential is the credential REFERENCE of the runner the step is on:
	// a name, never a secret. It says which identity the run's work runs
	// under.
	Credential string `json:"runner_credential,omitempty"`
}

// readRunnerTrace reads the runner trace out of a step's result.
func readRunnerTrace(step StepRun) runnerTrace {
	var trace runnerTrace
	if len(step.Result) > 0 {
		_ = json.Unmarshal(step.Result, &trace)
	}
	if trace.Tried == nil {
		trace.Tried = []string{}
	}
	return trace
}

// with records a runner attempt: the runner the step is on, the list of
// runners the step has tried and the credential reference the runner runs
// under.
func (t runnerTrace) with(name string, attempt int, credential string) runnerTrace {
	t.Runner = name
	t.Credential = credential
	if attempt > t.Attempt {
		t.Attempt = attempt
	}
	for _, tried := range t.Tried {
		if strings.EqualFold(tried, name) {
			return t
		}
	}
	t.Tried = append(t.Tried, name)
	return t
}

// stepBranch reads the branch an execution reported on its context manifest.
func stepBranch(step StepRun) string {
	var info struct {
		Branch string `json:"branch"`
	}
	_ = json.Unmarshal(step.ContextManifest, &info)
	return info.Branch
}

// fallbackRunner returns the next runner of the agent's chain for a step
// whose job failed with a capability failure.
//
// The chain is walked in priority order and a runner is tried at most once per
// step execution: the runners the step already tried are skipped, and so are
// the disabled ones and the ones whose budget is spent — a fallback onto a
// runner that cannot run would fail the node anyway.
//
// The second result is false when the chain is exhausted: the node then fails
// as it does today.
func (e *Engine) fallbackRunner(ctx context.Context, sc tenancy.Scope, node Node, step StepRun) (runner.Runner, bool, error) {
	if e.runners == nil {
		return runner.Runner{}, false, nil
	}
	cfg := decodeConfig(node)
	agentID := stringConfig(cfg, "agent_id")
	if agentID == "" {
		return runner.Runner{}, false, nil
	}
	chain, err := e.runners.Chain(ctx, sc, agentID)
	if err != nil {
		return runner.Runner{}, false, err
	}
	if len(chain) == 0 {
		return runner.Runner{}, false, nil
	}
	trace := readRunnerTrace(step)
	tried := map[string]bool{}
	for _, r := range chain {
		for _, name := range trace.Tried {
			if strings.EqualFold(name, r.Name) {
				tried[r.ID] = true
			}
		}
	}
	next, ok, err := WalkChain(chain, nil, e.spendFor(ctx, sc), tried)
	if err != nil {
		return runner.Runner{}, false, err
	}
	return next, ok, nil
}

// redispatchWithRunner queues the step again on another runner of the chain.
//
// The old job stays for the audit trail — the step moves to the new job and
// the failed one is still there to be read. The step's attempt advances, so
// the fallback cannot loop, and the step keeps its workspace: the second
// attempt continues where the first one stopped.
func (e *Engine) redispatchWithRunner(ctx context.Context, sc tenancy.Scope, run Run, t *Template, node Node, step StepRun, transport runner.Runner) error {
	attempt := readRunnerTrace(step).Attempt + 1
	ready := ReadyNode{NodeID: node.ID, IterationPath: step.IterationPath, RunIndex: step.RunIndex,
		Kind: node.Kind, Inputs: step.InputEdges, Attempt: attempt}
	return e.dispatchAgentOn(ctx, sc, run, t, node, step, ready, &transport, attempt)
}

func (e *Engine) productForItem(ctx context.Context, sc tenancy.Scope, item work.Item) (string, string) {
	if item.ID == "" || e.work == nil {
		return "", ""
	}
	projects, err := e.work.ListProjects(ctx, sc, "")
	if err != nil {
		return "", ""
	}
	productID := ""
	for _, p := range projects {
		if p.ID == item.ProjectID {
			productID = p.ProductID
			break
		}
	}
	if productID == "" {
		return "", ""
	}
	products, err := e.work.ListProducts(ctx, sc)
	if err != nil {
		return "", ""
	}
	for _, p := range products {
		if p.ID == productID {
			return p.ID, p.RepoURL
		}
	}
	return "", ""
}

// runIntegration executes an integration node: a deterministic, idempotent
// action of a platform (github.open_pr, github.wait_ci, github.comment).
//
// The node's config carries what the template knows; the execution supplies
// what only the run knows — the worktree, the branch, the repository. The
// idempotency key is the design's `run:node:path:index`, so a replay attaches
// to the same action instead of opening a second pull request.
func (e *Engine) runIntegration(ctx context.Context, sc tenancy.Scope, run Run, t *Template, claimed StepRun, ready ReadyNode, node Node) error {
	if e.integrations == nil {
		return e.failClaim(ctx, sc, run, t, claimed, "capability", "integrations are not configured")
	}
	var item work.Item
	if run.ItemID != "" && e.work != nil {
		if loaded, err := e.work.GetItem(ctx, sc, run.ItemID); err == nil {
			item = loaded
		}
	}
	if node.Type == "item.mark_done" {
		return e.markItemDone(ctx, sc, run, t, claimed, item)
	}
	productID, repoURL := e.productForItem(ctx, sc, item)
	mirrorPath := ""
	if productID != "" && e.workspaces != nil {
		mirrorPath = e.workspaces.MirrorPath(productID)
	}

	// The template may use placeholders for what only the item knows.
	config := node.Config
	if len(config) > 0 {
		replaced := strings.ReplaceAll(string(config), "{{item.title}}", item.Title)
		replaced = strings.ReplaceAll(replaced, "{{item.id}}", item.ID)
		replaced = strings.ReplaceAll(replaced, "{{item.description}}", item.DescriptionMD)
		config = json.RawMessage(replaced)
	}
	// The identity of the run's GitHub work is the credential of the runner
	// the run's agent used: the agent's own reference first, the tenant's
	// registered reference second. The node's own reference wins over both
	// — the template knows the identity it wants.
	if configSecretRef(config) == "" {
		if ref := e.runIdentitySecretRef(ctx, sc, run); ref != "" {
			config = withSecretRef(config, ref)
		}
	}

	branch := fmt.Sprintf("aic/%s-%s-%d", item.ID, claimed.ID, maxInt(1, ready.Attempt))
	workdir := claimed.Workdir
	if upstreamBranch, upstreamWorkdir := workLocationFromInputs(ready.Inputs); upstreamBranch != "" {
		// The work happened upstream: the integration pushes that branch, from
		// that worktree.
		branch = upstreamBranch
		if upstreamWorkdir != "" {
			workdir = upstreamWorkdir
		}
	}
	req := IntegrationRequest{
		Type:           node.Type,
		RunID:          run.ID,
		StepRunID:      claimed.ID,
		NodeID:         node.ID,
		IterationPath:  ready.IterationPath,
		RunIndex:       ready.RunIndex,
		Config:         config,
		Workdir:        workdir,
		MirrorPath:     mirrorPath,
		Branch:         branch,
		RepoURL:        repoURL,
		ProductID:      productID,
		ItemID:         item.ID,
		IdempotencyKey: fmt.Sprintf("run:%s:%s:%s:%d", run.ID, node.ID, ready.IterationPath, ready.RunIndex),
		Inputs:         ready.Inputs,
	}
	result, err := RunIntegration(ctx, *e.integrations, req)
	if err != nil {
		kind := FailureCapability
		var typed *IntegrationError
		if errors.As(err, &typed) {
			kind = typed.Kind
		}
		return e.failClaim(ctx, sc, run, t, claimed, string(kind), err.Error())
	}
	for _, artifact := range result.Artifacts {
		if _, err := e.store.CreateArtifact(ctx, sc, NewArtifact{
			RunID: run.ID, StepRunID: claimed.ID, ItemID: item.ID,
			Kind: artifact.Kind, URI: artifact.URI, Meta: artifact.Meta,
		}); err != nil {
			e.logger.Warn("persist integration artifact", "run", run.ID, "step", claimed.ID, "kind", artifact.Kind, "error", err)
			if artifact.Kind == "issue_body" {
				return e.failClaim(ctx, sc, run, t, claimed, string(FailureTransient), "could not persist fetched issue artifact: "+err.Error())
			}
		}
	}
	return e.terminalWithReceipts(ctx, sc, run, t, claimed, StepSucceeded, result.Result, result.OutputPorts, "")
}

// markItemDone closes the item the run worked on.
//
// It is the last step of the designed Bugfix flow: the work is done, the
// checks passed, a human approved the result — so the item goes to "done".
// The change is attributed to the run's starter, and the previous status is
// kept in the activity log like every other item change.
func (e *Engine) markItemDone(ctx context.Context, sc tenancy.Scope, run Run, t *Template, claimed StepRun, item work.Item) error {
	if item.ID == "" {
		return e.failClaim(ctx, sc, run, t, claimed, "dependency", "the run has no item to close")
	}
	if e.work == nil {
		return e.failClaim(ctx, sc, run, t, claimed, "capability", "the work store is not configured")
	}
	if item.Status == "done" {
		// Already closed: the step is idempotent, not a failure.
		return e.terminalWithReceipts(ctx, sc, run, t, claimed, StepSucceeded,
			rawJSON(map[string]any{"status": "succeeded", "exit_reason": "ok", "summary": "the item was already done"}),
			map[string]json.RawMessage{"main": rawJSON(map[string]any{"item_id": item.ID, "status": "done"})}, "")
	}
	status := "done"
	updated, err := e.work.UpdateItem(ctx, sc, item.ID, work.ItemPatch{Status: &status}, run.StartedBy)
	if err != nil {
		return e.failClaim(ctx, sc, run, t, claimed, "dependency", err.Error())
	}
	if _, err := e.store.CreateArtifact(ctx, sc, NewArtifact{
		RunID: run.ID, StepRunID: claimed.ID, ItemID: item.ID,
		Kind: "item", URI: item.ID, Meta: rawJSON(map[string]any{"status": updated.Status}),
	}); err != nil {
		e.logger.Warn("record item artifact", "run", run.ID, "item", item.ID, "error", err)
	}
	return e.terminalWithReceipts(ctx, sc, run, t, claimed, StepSucceeded,
		rawJSON(map[string]any{"status": "succeeded", "exit_reason": "ok", "summary": "the item is done"}),
		map[string]json.RawMessage{"main": rawJSON(map[string]any{"item_id": item.ID, "status": updated.Status})}, "")
}

// tenantSecretRef returns the secret reference the tenant registered for a
// platform, if any. The reference names a secret held elsewhere (the OS
// keychain); the secret itself never reaches the database.
func (e *Engine) tenantSecretRef(ctx context.Context, sc tenancy.Scope, kind string) string {
	if e.store == nil {
		return ""
	}
	var ref string
	// The read is tenant-scoped like every other query: the row is behind the
	// same policy as the rest of the tenant's data.
	err := e.store.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT secret_ref FROM integration_credentials WHERE kind=$1`, kind).Scan(&ref)
	})
	if err != nil {
		return ""
	}
	return ref
}

// runIdentitySecretRef returns the secret reference the run's platform work
// authenticates with.
//
// The identity of a run's GitHub work is the credential of the runner the
// run's agent used: the agent's own reference first, the tenant's registered
// reference second. A reference that does not resolve is not an identity —
// the next one is tried, and a run whose references all fail leaves the
// integration without a credential (the GitHub service is then not
// configured).
//
// The reference is a NAME. The secret behind it is fetched at the moment the
// integration needs it and never reaches the database.
func (e *Engine) runIdentitySecretRef(ctx context.Context, sc tenancy.Scope, run Run) string {
	if ref := e.agentCredentialRef(ctx, sc, run); ref != "" && e.secretResolves(ctx, ref) {
		return ref
	}
	if ref := e.tenantSecretRef(ctx, sc, "github"); ref != "" && e.secretResolves(ctx, ref) {
		return ref
	}
	return ""
}

// agentCredentialRef returns the credential reference of the runner the run's
// agent used: the most recent succeeded agent step of the run that carries
// one.
//
// The reference is a name, not a secret: it is read out of the step's result,
// which is safe to store.
func (e *Engine) agentCredentialRef(ctx context.Context, sc tenancy.Scope, run Run) string {
	if e.store == nil || run.ID == "" {
		return ""
	}
	steps, err := e.store.ListStepRuns(ctx, sc, run.ID)
	if err != nil {
		e.logger.Warn("read the run's steps for the agent's credential", "run", run.ID, "error", err)
		return ""
	}
	ref := ""
	// The steps come in creation order, so the last one that carries a
	// reference is the most recent one.
	for _, step := range steps {
		if step.Kind != string(NodeAgent) || step.State != StepSucceeded {
			continue
		}
		var result struct {
			Credential string `json:"runner_credential"`
		}
		if json.Unmarshal(step.Result, &result) != nil || result.Credential == "" {
			continue
		}
		ref = result.Credential
	}
	return ref
}

// secretResolves reports whether the reference resolves to a secret. The
// secret itself is not kept: only the fact that it exists matters here.
func (e *Engine) secretResolves(ctx context.Context, ref string) bool {
	if e.integrations == nil || e.integrations.Tokens == nil {
		return false
	}
	secret, err := e.integrations.Tokens.Resolve(ctx, ref)
	return err == nil && strings.TrimSpace(secret) != ""
}

// withRemote fills the push remote into an integration config when the
// template did not name one.
func withRemote(config json.RawMessage, remote string) json.RawMessage {
	if remote == "" || len(config) == 0 {
		return config
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil {
		return config
	}
	if existing, ok := fields["remote"]; ok {
		var current string
		if json.Unmarshal(existing, &current) == nil && current != "" {
			return config
		}
	}
	encoded, err := json.Marshal(remote)
	if err != nil {
		return config
	}
	fields["remote"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return config
	}
	return out
}

// withSecretRef fills the secret reference into an integration config when the
// template did not name one.
//
// A config that names its own reference keeps it: the template's choice wins
// over the run's identity.
func withSecretRef(config json.RawMessage, ref string) json.RawMessage {
	if ref == "" {
		return config
	}
	fields := map[string]json.RawMessage{}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &fields); err != nil {
			return config
		}
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
	}
	if existing, ok := fields["secret_ref"]; ok {
		var current string
		if json.Unmarshal(existing, &current) == nil && current != "" {
			return config
		}
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		return config
	}
	fields["secret_ref"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return config
	}
	return out
}

// configSecretRef returns the secret reference an integration config names
// itself, if any.
func configSecretRef(config json.RawMessage) string {
	if len(config) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil {
		return ""
	}
	var ref string
	if err := json.Unmarshal(fields["secret_ref"], &ref); err != nil {
		return ""
	}
	return strings.TrimSpace(ref)
}

// withWorkLocation adds the branch and the workdir of a finished execution to
// its output value, so the node downstream (an integration that opens a pull
// request) knows what to push and from where.
func withWorkLocation(value json.RawMessage, contextManifest json.RawMessage) json.RawMessage {
	var info struct {
		Branch  string `json:"branch"`
		Workdir string `json:"workdir"`
	}
	if len(contextManifest) == 0 || json.Unmarshal(contextManifest, &info) != nil {
		return value
	}
	if info.Branch == "" && info.Workdir == "" {
		return value
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(value, &fields); err != nil || fields == nil {
		fields = map[string]json.RawMessage{"text": value}
	}
	if info.Branch != "" {
		fields["branch"] = rawJSON(info.Branch)
	}
	if info.Workdir != "" {
		fields["workdir"] = rawJSON(info.Workdir)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return value
	}
	return out
}

// workLocationFromInputs finds the branch and the workdir an upstream
// execution reported on its output port.
func workLocationFromInputs(inputs map[string]Receipt) (branch, workdir string) {
	for _, receipt := range inputs {
		if receipt.State != ReceiptDelivered || len(receipt.Value) == 0 {
			continue
		}
		var fields struct {
			Branch  string `json:"branch"`
			Workdir string `json:"workdir"`
		}
		if json.Unmarshal(receipt.Value, &fields) != nil {
			continue
		}
		if fields.Branch != "" {
			branch = fields.Branch
		}
		if fields.Workdir != "" {
			workdir = fields.Workdir
		}
	}
	return branch, workdir
}

// fingerprint is a short, stable identifier of a piece of text.
func fingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:12]
}

// selectSkills picks the skills that belong in the node's context bundle.
//
// The selection is made from trusted fields only — the skill's own description
// and slug, never the task text — and an agent's skills policy decides whether
// the selection is automatic or explicit. A skill that no human approved is
// never attached.
func (e *Engine) selectSkills(ctx context.Context, sc tenancy.Scope, item work.Item, a agent.Agent, node Node, task string) ([]ctxbundle.Skill, error) {
	if e.skills == nil {
		return nil, nil
	}
	policy := struct {
		Auto      bool     `json:"auto"`
		MaxAttach int      `json:"max_attach"`
		Keys      []string `json:"keys"`
	}{Auto: true, MaxAttach: ctxbundle.DefaultMaxSkills}
	if len(a.SkillsPolicy) > 0 {
		_ = json.Unmarshal(a.SkillsPolicy, &policy)
	}
	keys := policy.Keys
	if len(keys) == 0 {
		// A node may name the skills it wants explicitly.
		var cfg map[string]json.RawMessage
		if len(node.Config) > 0 {
			_ = json.Unmarshal(node.Config, &cfg)
		}
		if raw, ok := cfg["skills"]; ok {
			_ = json.Unmarshal(raw, &keys)
		}
	}
	if !policy.Auto && len(keys) == 0 {
		return nil, nil
	}
	opts := skill.SelectOptions{Task: task, MaxAttach: policy.MaxAttach, Keys: keys}
	if productID, _ := e.productForItem(ctx, sc, item); productID != "" {
		opts.ProductID = &productID
	}
	selected, err := skill.Select(ctx, sc, e.skills, opts)
	if err != nil {
		return nil, err
	}
	out := make([]ctxbundle.Skill, 0, len(selected))
	for _, pick := range selected {
		out = append(out, ctxbundle.Skill{Slug: pick.Skill.Slug, Version: pick.Version.Version, Content: pick.Version.ContentMD, Source: pick.Ref})
	}
	return out, nil
}

func (e *Engine) readNodeMemory(ctx context.Context, sc tenancy.Scope, run Run, item work.Item, a agent.Agent, node Node) ([]ctxbundle.MemoryEntry, error) {
	if e.memory == nil {
		return nil, nil
	}
	selector := MemorySelector{Run: MemoryTier{Mode: MemoryAuto}, Agent: MemoryTier{Mode: MemoryAuto}, Workspace: MemoryTier{Mode: MemoryAuto}}
	if node.Memory != nil {
		selector = *node.Memory
	}
	tiers := []struct {
		scope memory.Scope
		tier  MemoryTier
		name  string
	}{{memory.Scope{Kind: memory.ScopeRun, Ref: run.ID}, selector.Run, "run"}, {memory.Scope{Kind: memory.ScopeItem, Ref: item.ID}, MemoryTier{Mode: MemoryAuto}, "item"}, {memory.Scope{Kind: memory.ScopeAgent, Ref: a.ID}, selector.Agent, "agent"}}
	productID, _ := e.productForItem(ctx, sc, item)
	if productID != "" {
		tiers = append(tiers, struct {
			scope memory.Scope
			tier  MemoryTier
			name  string
		}{memory.Scope{Kind: memory.ScopeWorkspace, Ref: productID}, selector.Workspace, "workspace"})
	}
	var result []ctxbundle.MemoryEntry
	for _, tier := range tiers {
		if tier.scope.Ref == "" || tier.tier.Mode == MemoryNone {
			continue
		}
		entries, _, err := e.memory.Read(ctx, sc, tier.scope)
		if err != nil {
			return nil, err
		}
		keys := map[string]bool{}
		for _, key := range tier.tier.Keys {
			keys[key] = true
		}
		for _, entry := range entries {
			if len(keys) > 0 && !keys[entry.Key] {
				continue
			}
			if tier.tier.Mode == MemorySummary && entry.ContentMD != "" { /* keep complete entry; summarization is not in the M2 store */
			}
			result = append(result, ctxbundle.MemoryEntry{Scope: tier.name, Ref: tier.scope.Ref, Key: entry.Key, Revision: entry.Revision, Content: entry.ContentMD, Tokens: entry.TokenEstimate, Source: string(entry.Source)})
		}
	}
	return result, nil
}

func (e *Engine) dispatchCheckpoint(ctx context.Context, sc tenancy.Scope, run Run, t *Template, node Node, step StepRun, ready ReadyNode) error {
	cfg := decodeConfig(node)
	lap, err := nextCheckpointLap(ctx, e.store, sc, run.ID, node.ID)
	if err != nil {
		return err
	}
	var fields json.RawMessage = cfg["fields"]
	question := stringConfig(cfg, "question")
	if question == "" {
		question = DefaultCheckpointQuestion(node.Name)
	}
	// The card a human decides on carries the work so far, not the node's
	// name: the brief says what was done, what the agent says and what
	// changed.
	brief, _, err := e.checkpointBrief(ctx, sc, run, t, node, question)
	if err != nil {
		e.logger.Warn("build checkpoint brief", "run", run.ID, "node", node.ID, "error", err)
		brief = question
	}
	cp, err := e.store.CreateCheckpoint(ctx, sc, NewCheckpoint{RunID: run.ID, StepRunID: step.ID, NodeID: node.ID, Lap: lap, SummaryMD: brief, FieldsSpec: fields})
	if err != nil {
		return err
	}
	waiting := StepWaiting
	result := rawJSON(map[string]any{"status": "waiting", "question": question, "lap": lap, "checkpoint_id": cp.ID})
	if _, err = e.store.UpdateStepRun(ctx, sc, step.ID, StepRunPatch{State: &waiting, Result: &result}); err != nil {
		return err
	}
	e.notifyCheckpoint(ctx, sc, run, cp, question)
	return nil
}

// checkpointBrief assembles the brief a human decides on: the question, what
// happened so far, the agent's own words and the change.
//
// The artifacts the brief collected are facts about the run's worktree, so
// they are recorded once per run: a later checkpoint updates the row instead
// of adding a second one.
func (e *Engine) checkpointBrief(ctx context.Context, sc tenancy.Scope, run Run, t *Template, node Node, question string) (string, []ArtifactSummary, error) {
	var item work.Item
	if run.ItemID != "" && e.work != nil {
		loaded, err := e.work.GetItem(ctx, sc, run.ItemID)
		if err != nil {
			// A deleted item must not stop a checkpoint from being decided.
			e.logger.Warn("read item for checkpoint brief", "run", run.ID, "item", run.ItemID, "error", err)
		} else {
			item = loaded
		}
	}
	rows, err := e.store.ListStepRuns(ctx, sc, run.ID)
	if err != nil {
		return "", nil, err
	}
	artifacts, err := e.store.ListArtifacts(ctx, sc, run.ID)
	if err != nil {
		e.logger.Warn("read artifacts for checkpoint brief", "run", run.ID, "error", err)
	}
	workdir, branch := briefWorktree(rows)
	brief, evidence, err := BuildCheckpointBrief(BriefInput{
		ItemNumber: item.Number,
		ItemTitle:  item.Title,
		Question:   question,
		Steps:      briefSteps(t, rows, node.ID),
		Artifacts:  ArtifactSummaries(artifacts, rows),
		Workdir:    workdir,
		Branch:     branch,
		BaseBranch: defaultBaseBranch,
	})
	if err != nil {
		return "", nil, err
	}
	e.recordCheckpointArtifacts(ctx, sc, run, item, evidence)
	return brief, evidence, nil
}

// recordCheckpointArtifacts records the worktree facts of a run's artifacts.
//
// The branch, the commit and the diff stat describe the worktree, not one
// step: they are written once per run and updated afterwards.
func (e *Engine) recordCheckpointArtifacts(ctx context.Context, sc tenancy.Scope, run Run, item work.Item, artifacts []ArtifactSummary) {
	for _, artifact := range artifacts {
		switch artifact.Kind {
		case "branch", "commit", "diff_summary":
		default:
			continue
		}
		if _, err := e.store.UpsertRunArtifact(ctx, sc, NewArtifact{RunID: run.ID, ItemID: item.ID, Kind: artifact.Kind, URI: artifact.URI, Meta: artifact.Meta}); err != nil {
			e.logger.Warn("record checkpoint artifact", "run", run.ID, "kind", artifact.Kind, "error", err)
		}
	}
}

// briefSteps lists the run's steps that already produced a result, in the
// order of the graph.
//
// The node the checkpoint asks about is left out: the card asks the question,
// it does not report the question as work.
func briefSteps(t *Template, rows []StepRun, nodeID string) []BriefStep {
	out := []BriefStep{}
	seen := map[string]bool{}
	for _, node := range t.Nodes {
		seen[node.ID] = true
		if node.ID == nodeID {
			continue
		}
		step, ok := latestStepAny(rows, node.ID)
		if !ok || len(step.Result) == 0 {
			continue
		}
		out = append(out, briefStep(step))
	}
	// A step whose node is not in the graph — the template changed under a
	// running execution — keeps its creation order.
	for _, row := range rows {
		if seen[row.NodeID] || row.NodeID == nodeID || len(row.Result) == 0 {
			continue
		}
		out = append(out, briefStep(row))
	}
	return out
}

// briefStep turns a step run into the line the brief shows.
//
// Whether the line is work or a question is decided by the brief itself.
func briefStep(step StepRun) BriefStep {
	return BriefStep{Name: step.Name, Kind: step.Kind, State: string(step.State), Summary: stepSummary(step), Output: stepOutput(step)}
}

// latestStepAny finds the latest execution of a node, whatever its iteration
// path is.
func latestStepAny(rows []StepRun, nodeID string) (StepRun, bool) {
	var found StepRun
	for _, step := range rows {
		if step.NodeID != nodeID || step.State == StepSuperseded {
			continue
		}
		if found.ID == "" || step.RunIndex > found.RunIndex || (step.RunIndex == found.RunIndex && step.CreatedAt.After(found.CreatedAt)) {
			found = step
		}
	}
	return found, found.ID != ""
}

// briefWorktree finds the worktree the run works in: the last step that has
// one. The branch comes from the step's context manifest.
func briefWorktree(rows []StepRun) (workdir, branch string) {
	for _, step := range rows {
		if step.Workdir == "" {
			continue
		}
		workdir = step.Workdir
		if name := stepBranch(step); name != "" {
			branch = name
		}
	}
	return workdir, branch
}

// stepOutput reads the agent's output out of a step's result.
func stepOutput(step StepRun) string {
	var result struct {
		Output string `json:"output"`
	}
	_ = json.Unmarshal(step.Result, &result)
	return strings.TrimSpace(result.Output)
}

func nextCheckpointLap(ctx context.Context, st *Store, sc tenancy.Scope, runID, nodeID string) (int, error) {
	lap := 1
	err := st.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COALESCE(max(lap),0)+1 FROM checkpoints WHERE run_id=$1 AND node_id=$2`, runID, nodeID).Scan(&lap)
	})
	return lap, err
}

// notifyCheckpoint tells the human that a decision is waiting.
//
// The body is the question, not the brief: a notification is one line, and the
// brief is what the card shows when the human opens it.
func (e *Engine) notifyCheckpoint(ctx context.Context, sc tenancy.Scope, run Run, cp Checkpoint, question string) {
	if e.notifications == nil || run.StartedBy == "" {
		return
	}
	body := question
	if body == "" {
		body = cp.SummaryMD
	}
	payload := rawJSON(map[string]string{"run_id": run.ID, "checkpoint_id": cp.ID})
	title := "Workflow checkpoint needs review"
	link := "/checkpoints/" + cp.ID
	_, err := e.notifications.CreateMany(ctx, sc, notification.New{Kind: notification.KindCheckpointPending, TitleMD: title, BodyMD: body, Link: link, Payload: payload}, []string{run.StartedBy})
	if err != nil {
		e.logger.Warn("notify pending workflow checkpoint", "checkpoint_id", cp.ID, "error", err)
	}
	e.notifyChannels(ctx, sc, "checkpoint_pending", run, title, body, link)
}

// notifyChannels delivers the event to the out-of-band channels: the browser
// push and the user's own commands. A channel that fails never fails the run.
func (e *Engine) notifyChannels(ctx context.Context, sc tenancy.Scope, kind string, run Run, title, body, link string) {
	if e.push != nil {
		if err := e.push.Notify(ctx, sc, title, body, link); err != nil {
			e.logger.Warn("push notification", "kind", kind, "error", err)
		}
	}
	if e.commands != nil {
		for _, result := range e.commands.Run(ctx, sc, command.Event{
			Kind:   kind,
			RunID:  run.ID,
			ItemID: run.ItemID,
			Title:  title,
			Body:   body,
			Link:   link,
		}) {
			if result.Err != "" {
				e.logger.Warn("notification command failed", "channel", result.ChannelID, "error", result.Err)
			}
		}
	}
}

var _ = memory.ScopeRun
