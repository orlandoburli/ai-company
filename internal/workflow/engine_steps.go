package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/ctxbundle"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/notification"
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
		node, _ := t.Node(step.NodeID)
		status := StepSucceeded
		failure := ""
		ports := map[string]json.RawMessage{}
		result := map[string]any{"status": "succeeded", "exit_reason": "ok"}
		if job.State == agent.JobFailed || job.State == agent.JobCancelled {
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
			result = map[string]any{"status": "failed", "exit_reason": message, "failure_kind": failure, "summary": message}
		} else {
			var decoded map[string]json.RawMessage
			if len(job.Result) > 0 {
				_ = json.Unmarshal(job.Result, &decoded)
			}
			var output string
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
			result["summary"] = output
			result["output"] = output
		}
		result["tokens_in"], result["tokens_out"], result["cost_usd"] = job.TokensIn, job.TokensOut, job.CostUSD
		var contextInfo struct {
			WorkspaceNote string `json:"workspace_note"`
		}
		_ = json.Unmarshal(step.ContextManifest, &contextInfo)
		if contextInfo.WorkspaceNote != "" {
			result["workspace_note"] = contextInfo.WorkspaceNote
		}
		_ = node
		if err := e.terminalWithReceipts(ctx, sc, run, t, step, status, rawJSON(result), ports, failure); err != nil && !errors.Is(err, ErrClaimLost) {
			return err
		}
	}
	return nil
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
	// The run tier is the run's memory document: what a node produced and
	// what a human decided. It is written after the step is terminal, so a
	// later node that reads the run scope sees it. A memory failure never
	// fails the step — the result is already recorded.
	e.writeRunMemory(ctx, sc, run, step, state, ports, result)
	return nil
}

// writeRunMemory records what a terminal node produced in the run's memory
// scope. The key is the node id, so a redo of the same node writes the next
// revision of the same key and the history stays auditable.
func (e *Engine) writeRunMemory(ctx context.Context, sc tenancy.Scope, run Run, step StepRun, state StepState, ports map[string]json.RawMessage, result json.RawMessage) {
	if e.memory == nil || run.ID == "" {
		return
	}
	key := "node:" + step.NodeID
	content := ""
	if len(ports) > 0 {
		if raw, ok := ports["main"]; ok {
			content = string(raw)
		}
	}
	if content == "" {
		content = string(result)
	}
	if content == "" {
		return
	}
	source := memory.SourceStepOutput
	if state != StepSucceeded {
		source = memory.SourceIntegration
	}
	if _, err := e.memory.Write(ctx, sc, memory.Write{
		Scope:           memory.Scope{Kind: memory.ScopeRun, Ref: run.ID},
		Key:             key,
		ContentMD:       content,
		Source:          source,
		SourceRunID:     run.ID,
		SourceStepRunID: step.ID,
	}); err != nil {
		e.logger.Warn("write run memory", "run", run.ID, "node", step.NodeID, "error", err)
	}
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
		return e.failClaim(ctx, sc, run, t, claimed, "capability", "integration nodes are not available until M4")
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
	if e.agents == nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", "agent store is not configured")
	}
	cfg := decodeConfig(node)
	agentID := stringConfig(cfg, "agent_id")
	a, err := e.agents.GetAgent(ctx, sc, agentID)
	if err != nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", err.Error())
	}
	var item work.Item
	if run.ItemID != "" && e.work != nil {
		item, err = e.work.GetItem(ctx, sc, run.ItemID)
		if err != nil {
			return e.failClaim(ctx, sc, run, t, step, "dependency", err.Error())
		}
	}
	workdir := ""
	scratchReason := ""
	if node.Workspace == WorkspaceWorktree {
		productID, repoURL := e.productForItem(ctx, sc, item)
		if e.workspaces == nil || productID == "" || repoURL == "" {
			scratchReason = "No repository mirror is configured; this execution uses a scratch workspace."
		} else {
			ws, prepErr := e.workspaces.Prepare(ctx, workspace.Request{ProductID: productID, SourceURL: repoURL, RunID: run.ID, StepRunID: step.ID, ItemID: item.ID, Attempt: maxInt(1, ready.Attempt)})
			if prepErr != nil {
				if errors.Is(prepErr, workspace.ErrNoMirror) {
					scratchReason = "No repository mirror is available; this execution uses a scratch workspace."
				} else {
					return e.failClaim(ctx, sc, run, t, step, "capability", prepErr.Error())
				}
			} else {
				workdir = ws.Dir
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
	if item.ID != "" {
		fmt.Fprintf(&task, "\n\nWork item: %s\nStatus: %s\n\n%s", item.Title, item.Status, item.DescriptionMD)
	}
	var sections []ctxbundle.Section
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
	bundle, err := ctxbundle.Assemble(ctxbundle.AssembleInput{Soul: a.SoulMD, Memory: memEntries, Task: task.String(), Sections: sections})
	if err != nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", err.Error())
	}
	contextJSON := rawJSON(map[string]any{"manifest": bundle.Manifest, "memory_manifest": bundle.MemoryManifest, "task_file_sha": bundle.TaskFileSHA, "inputs": ready.Inputs})
	key := fmt.Sprintf("run:%s:%s:%s:%d", run.ID, node.ID, ready.IterationPath, ready.RunIndex)
	job, err := e.agents.CreateJob(ctx, sc, agent.NewJob{AgentID: a.ID, ThreadID: thread.ID, ItemID: item.ID, StepRunID: step.ID, IdempotencyKey: key, Prompt: bundle.Prompt, Context: contextJSON, CreatedBy: run.StartedBy})
	if err != nil {
		return e.failClaim(ctx, sc, run, t, step, "capability", err.Error())
	}
	state := StepRunning
	jobID := job.ID
	wk := string(node.Workspace)
	wd := workdir
	contextManifest := rawJSON(map[string]any{"manifest": bundle.Manifest, "workspace_note": scratchReason})
	_, err = e.store.UpdateStepRun(ctx, sc, step.ID, StepRunPatch{State: &state, AgentJobID: &jobID, MemoryManifest: ptrRaw(rawJSON(bundle.MemoryManifest)), ContextManifest: ptrRaw(contextManifest), TaskFileSHA: &bundle.TaskFileSHA, Workdir: &wd, WorkspaceKind: &wk})
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

func ptrRaw(v json.RawMessage) *json.RawMessage { return &v }
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
		question = node.Name
	}
	cp, err := e.store.CreateCheckpoint(ctx, sc, NewCheckpoint{RunID: run.ID, StepRunID: step.ID, NodeID: node.ID, Lap: lap, SummaryMD: question, FieldsSpec: fields})
	if err != nil {
		return err
	}
	waiting := StepWaiting
	result := rawJSON(map[string]any{"status": "waiting", "question": question, "lap": lap, "checkpoint_id": cp.ID})
	if _, err = e.store.UpdateStepRun(ctx, sc, step.ID, StepRunPatch{State: &waiting, Result: &result}); err != nil {
		return err
	}
	e.notifyCheckpoint(ctx, sc, run, cp)
	return nil
}

func nextCheckpointLap(ctx context.Context, st *Store, sc tenancy.Scope, runID, nodeID string) (int, error) {
	lap := 1
	err := st.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COALESCE(max(lap),0)+1 FROM checkpoints WHERE run_id=$1 AND node_id=$2`, runID, nodeID).Scan(&lap)
	})
	return lap, err
}

func (e *Engine) notifyCheckpoint(ctx context.Context, sc tenancy.Scope, run Run, cp Checkpoint) {
	if e.notifications == nil || run.StartedBy == "" {
		return
	}
	payload := rawJSON(map[string]string{"run_id": run.ID, "checkpoint_id": cp.ID})
	_, err := e.notifications.CreateMany(ctx, sc, notification.New{Kind: notification.KindCheckpointPending, TitleMD: "Workflow checkpoint needs review", BodyMD: cp.SummaryMD, Link: "/checkpoints/" + cp.ID, Payload: payload}, []string{run.StartedBy})
	if err != nil {
		e.logger.Warn("notify pending workflow checkpoint", "checkpoint_id", cp.ID, "error", err)
	}
}

var _ = memory.ScopeRun
