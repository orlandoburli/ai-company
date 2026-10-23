package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ReceiptState is the persisted outcome of one edge from an upstream execution.
type ReceiptState string

const (
	ReceiptDelivered ReceiptState = "delivered"
	ReceiptSkipped   ReceiptState = "skipped"
	ReceiptDead      ReceiptState = "dead"
)

// Receipt records an upstream execution's outcome on one graph edge.
//
// The JSON tags are the wire shape: the receipt map is persisted in
// step_runs.input_edges_json and returned by the API unchanged.
type Receipt struct {
	EdgeID   string          `json:"edge_id"`
	FromNode string          `json:"from_node"`
	Port     string          `json:"port"`
	State    ReceiptState    `json:"state"`
	Value    json.RawMessage `json:"value,omitempty"`
	Summary  string          `json:"summary,omitempty"`
}

// StepState is the lifecycle state of a node execution.
type StepState string

const (
	StepPending    StepState = "pending"
	StepReady      StepState = "ready"
	StepRunning    StepState = "running"
	StepWaiting    StepState = "waiting"
	StepSucceeded  StepState = "succeeded"
	StepFailed     StepState = "failed"
	StepSkipped    StepState = "skipped"
	StepBlocked    StepState = "blocked"
	StepCancelled  StepState = "cancelled"
	StepSuperseded StepState = "superseded"
)

// FailureKind is an engine- or adapter-supplied classification; the planner does
// not infer it from an exit reason.
type FailureKind string

const (
	FailureDependency FailureKind = "dependency"
	FailureNeedsInput FailureKind = "needs_input"
	FailureCapability FailureKind = "capability"
	FailureTransient  FailureKind = "transient"
)

// PersistedStep is the planner's view of one persisted node execution.
type PersistedStep struct {
	NodeID        string
	IterationPath string
	RunIndex      int
	State         StepState
	Kind          NodeKind
	OnError       OnError
	FailureKind   FailureKind
	ExitReason    string
	OutputPorts   map[string]json.RawMessage
	Incoming      map[string]Receipt // by edge id
	Lap           int
	Question      string
	Iteration     int // loop iteration represented by this execution; zero means unspecified
	Attempt       int // transient retry attempt; first attempt is 1
}

// Plan is the pure planning result for one scheduler tick.
type Plan struct {
	Ready            []ReadyNode
	Waiting          []WaitingCheckpoint
	Blocked          []string
	RunState         string
	RunFailureKind   FailureKind
	RunError         string
	Terminal         bool
	Receipts         []Receipt
	GeneratedFailed  []PersistedStep
	GeneratedSkipped []PersistedStep
}

// ReadyNode describes one execution which may be dispatched concurrently.
type ReadyNode struct {
	NodeID        string
	IterationPath string
	RunIndex      int
	Kind          NodeKind
	Inputs        map[string]Receipt
	Iteration     int
	Attempt       int
}

// WaitingCheckpoint is a request to park a branch for human input.
type WaitingCheckpoint struct {
	NodeID   string
	StepID   string
	Lap      int
	Question string
}

// Decide computes the ready set, checkpoint requests, run state, and implied
// edge receipts from a template and persisted execution snapshot.
func Decide(t *Template, steps []PersistedStep) (Plan, error) {
	if t == nil {
		return Plan{}, fmt.Errorf("workflow template is nil")
	}
	if err := t.Validate(); err != nil {
		return Plan{}, err
	}

	// Keep the latest non-superseded execution for each node/path. Execution
	// history remains available to the store; only its current projection matters.
	latest := make(map[string]PersistedStep)
	for _, step := range steps {
		if step.State == StepSuperseded {
			continue
		}
		key := stepKey(step.NodeID, step.IterationPath)
		if old, ok := latest[key]; !ok || step.RunIndex >= old.RunIndex {
			latest[key] = step
		}
	}

	inputs := make(map[string]map[string]Receipt)
	plan := Plan{RunState: "running"}
	for _, step := range latest {
		if inputs[step.NodeID] == nil {
			inputs[step.NodeID] = make(map[string]Receipt)
		}
		for edgeID, receipt := range step.Incoming {
			inputs[step.NodeID][edgeID] = receipt
		}
	}
	for _, step := range latest {
		if !isTerminalStep(step.State) || (step.State == StepFailed && step.FailureKind == FailureNeedsInput) {
			continue
		}
		if step.State == StepFailed && step.FailureKind == FailureTransient {
			if node, ok := t.Node(step.NodeID); ok && step.Attempt < maxAttempts(node) {
				continue
			}
		}
		emitted := EmitReceipts(t, step)
		plan.Receipts = append(plan.Receipts, emitted...)
		for _, receipt := range emitted {
			if inputs[edgeTarget(t, receipt.EdgeID)] == nil {
				inputs[edgeTarget(t, receipt.EdgeID)] = make(map[string]Receipt)
			}
			// An explicitly persisted receipt wins over a recomputed one.
			to := edgeTarget(t, receipt.EdgeID)
			if _, exists := inputs[to][receipt.EdgeID]; !exists {
				inputs[to][receipt.EdgeID] = receipt
			}
		}
	}

	stepFor := make(map[string]PersistedStep)
	for _, step := range latest {
		if old, ok := stepFor[step.NodeID]; !ok || step.RunIndex >= old.RunIndex {
			stepFor[step.NodeID] = step
		}
	}
	rootFailure := FailureKind("")
	rootError := ""
	stopFailure := false
	anyReadyOrRunning := false
	remaining := 0
	waitingCount := 0
	activeCount := 0

	for _, node := range t.Nodes {
		step, hasStep := stepFor[node.ID]
		if hasStep && step.State == StepCancelled {
			continue
		}
		loopReentry := hasStep && node.Kind == NodeLoop && step.State == StepSucceeded && hasCycleReceipt(t, node.ID, inputs[node.ID])
		retryTransient := hasStep && step.State == StepFailed && step.FailureKind == FailureTransient && step.Attempt < maxAttempts(node)
		if hasStep && isTerminalStep(step.State) && !loopReentry && !retryTransient {
			if step.State == StepFailed {
				if step.FailureKind == FailureNeedsInput {
					plan.Waiting = append(plan.Waiting, checkpointRequest(step))
					waitingCount++
					remaining++
					activeCount++
					continue
				}
				if step.FailureKind == FailureDependency {
					plan.Blocked = append(plan.Blocked, node.ID)
				}
				if rootFailure == "" {
					rootFailure = step.FailureKind
					rootError = step.ExitReason
				}
				if step.OnError == OnErrorStop {
					stopFailure = true
				}
			}
			if step.State == StepBlocked {
				plan.Blocked = append(plan.Blocked, node.ID)
				if rootFailure == "" {
					rootFailure = FailureDependency
					rootError = step.ExitReason
				}
			}
			continue
		}
		if hasStep && step.State == StepWaiting {
			plan.Waiting = append(plan.Waiting, checkpointRequest(step))
			waitingCount++
			remaining++
			activeCount++
			continue
		}
		if hasStep && step.State == StepRunning {
			anyReadyOrRunning = true
			remaining++
			activeCount++
			continue
		}

		receipts := inputs[node.ID]
		if node.Kind != NodeMerge && allIncomingSkipped(t, node.ID, receipts) {
			skipped := PersistedStep{NodeID: node.ID, State: StepSkipped, Kind: node.Kind, OnError: node.OnError}
			plan.GeneratedSkipped = append(plan.GeneratedSkipped, skipped)
			plan.Receipts = append(plan.Receipts, EmitReceipts(t, skipped)...)
			continue
		}
		ready, isBlocked := nodeReadiness(t, node, receipts)
		if isBlocked {
			plan.Blocked = append(plan.Blocked, node.ID)
			blockedStep := PersistedStep{NodeID: node.ID, State: StepBlocked, Kind: node.Kind, OnError: node.OnError, FailureKind: FailureDependency, ExitReason: "dependency blocked"}
			plan.Receipts = append(plan.Receipts, EmitReceipts(t, blockedStep)...)
			remaining++
			activeCount++
			if rootFailure == "" {
				rootFailure = FailureDependency
				rootError = "dependency blocked"
			}
			continue
		}
		if !ready {
			// The node is not ready yet: it waits for its upstream. That is
			// not activity — a node that waits for a checkpoint is waiting on
			// the human, and counting it as active would hide that.
			remaining++
			continue
		}
		attempt := 0
		runIndex := nextRunIndex(step, hasStep)
		if retryTransient {
			attempt = step.Attempt + 1
			runIndex = step.RunIndex
		}
		iteration := 0
		if node.Kind == NodeLoop {
			iteration = 1
			if hasStep && step.Iteration > 0 {
				iteration = step.Iteration
			}
			if hasStep && step.State == StepSucceeded && hasCycleReceipt(t, node.ID, receipts) {
				iteration++
			}
			limit := loopLimit(node)
			if limit > 0 && iteration > limit {
				failed := PersistedStep{NodeID: node.ID, IterationPath: step.IterationPath, RunIndex: step.RunIndex + 1, State: StepFailed, Kind: node.Kind, OnError: node.OnError, ExitReason: "cycle_budget_exhausted", Iteration: iteration}
				plan.GeneratedFailed = append(plan.GeneratedFailed, failed)
				plan.Receipts = append(plan.Receipts, EmitReceipts(t, failed)...)
				if node.OnError == OnErrorStop {
					stopFailure = true
					if rootFailure == "" {
						rootFailure, rootError = FailureDependency, failed.ExitReason
					}
				}
				remaining++
				activeCount++
				continue
			}
		}
		plan.Ready = append(plan.Ready, ReadyNode{NodeID: node.ID, IterationPath: step.IterationPath, RunIndex: runIndex, Kind: node.Kind, Inputs: cloneReceipts(receipts), Iteration: iteration, Attempt: attempt})
		anyReadyOrRunning = true
		remaining++
		activeCount++
	}

	// Stable output is useful to a scheduler, but ordering has no dependency
	// meaning: every member of Ready is independently dispatchable.
	sort.Slice(plan.Ready, func(i, j int) bool { return plan.Ready[i].NodeID < plan.Ready[j].NodeID })
	sort.Strings(plan.Blocked)
	sort.Slice(plan.Waiting, func(i, j int) bool { return plan.Waiting[i].NodeID < plan.Waiting[j].NodeID })
	sort.Slice(plan.Receipts, func(i, j int) bool { return plan.Receipts[i].EdgeID < plan.Receipts[j].EdgeID })

	switch {
	case anyReadyOrRunning:
		plan.RunState = "running"
	case stopFailure:
		// A node failed with stop_workflow: the run fails with that node's
		// failure kind and its reason ("cycle_budget_exhausted" for a loop
		// that ran out of iterations, for example).
		plan.RunState = "failed"
		if rootFailure == "" {
			rootFailure = FailureDependency
		}
		plan.RunFailureKind = rootFailure
		plan.RunError = rootError
		plan.Terminal = true
	case len(plan.Blocked) > 0:
		plan.RunState = "failed"
		if rootFailure == "" {
			rootFailure = FailureDependency
		}
		plan.RunFailureKind = rootFailure
		plan.RunError = rootError
		plan.Terminal = true
	case remaining > 0 && waitingCount > 0:
		// Everything that is left is waiting on a human: a checkpoint is
		// parked, and the nodes behind it wait for its decision.
		plan.RunState = "waiting_human"
	case remaining > 0:
		plan.RunState = "failed"
		plan.RunFailureKind = FailureDependency
		plan.RunError = "no node can progress"
		plan.Terminal = true
	case stopFailure:
		plan.RunState = "failed"
		plan.RunFailureKind = rootFailure
		plan.RunError = rootError
		plan.Terminal = true
	default:
		plan.RunState = "completed"
		plan.Terminal = true
	}
	if plan.RunState == "failed" && plan.RunFailureKind == "" {
		plan.RunFailureKind = rootFailure
	}
	return plan, nil
}

// EmitReceipts computes the receipt for every outgoing edge of a terminal step.
// Missing output ports are skipped; failure policies override that default.
func EmitReceipts(t *Template, step PersistedStep) []Receipt {
	if t == nil || !isTerminalStep(step.State) {
		return nil
	}
	var result []Receipt
	for _, edge := range t.OutgoingEdges(step.NodeID) {
		receipt := Receipt{EdgeID: edge.ID, FromNode: step.NodeID, Port: edge.From.Port}
		switch step.State {
		case StepBlocked:
			receipt.State = ReceiptDead
		case StepSkipped:
			receipt.State = ReceiptSkipped
		case StepFailed:
			if step.FailureKind == FailureDependency {
				receipt.State = ReceiptDead
				break
			}
			switch step.OnError {
			case OnErrorContinueRegular:
				if edge.From.Port == "main" {
					receipt.State, receipt.Value = ReceiptDelivered, json.RawMessage("null")
				} else {
					receipt.State = ReceiptSkipped
				}
			case OnErrorContinueError:
				if edge.From.Port == "error" {
					receipt.State, receipt.Value = ReceiptDelivered, json.RawMessage("null")
				} else if edge.From.Port == "main" {
					receipt.State = ReceiptDead
				} else {
					receipt.State = ReceiptSkipped
				}
			default:
				receipt.State = ReceiptDead
			}
		default:
			if value, ok := step.OutputPorts[edge.From.Port]; ok {
				receipt.State, receipt.Value = ReceiptDelivered, append(json.RawMessage(nil), value...)
			} else {
				receipt.State = ReceiptSkipped
			}
		}
		result = append(result, receipt)
	}
	return result
}

// RedoPlan describes the graph-reachability reset produced by redoing a node.
type RedoPlan struct {
	Superseded []PersistedStep
	Reset      []PersistedStep
	Receipts   []Receipt
}

// Redo supersedes the selected node and all graph descendants, recreates those
// executions as pending, and re-emits receipts from untouched predecessors.
func Redo(t *Template, nodeID string, steps []PersistedStep) (RedoPlan, error) {
	if t == nil {
		return RedoPlan{}, fmt.Errorf("workflow template is nil")
	}
	if _, ok := t.Node(nodeID); !ok {
		return RedoPlan{}, fmt.Errorf("unknown node %q", nodeID)
	}
	resetSet := map[string]bool{nodeID: true}
	for _, id := range t.Descendants(nodeID) {
		resetSet[id] = true
	}
	var result RedoPlan
	latest := make(map[string]PersistedStep)
	for _, step := range steps {
		if resetSet[step.NodeID] {
			old := step
			old.State = StepSuperseded
			result.Superseded = append(result.Superseded, old)
			if current, ok := latest[step.NodeID]; !ok || step.RunIndex > current.RunIndex {
				latest[step.NodeID] = step
			}
		}
	}
	for _, id := range sortedKeys(resetSet) {
		node, _ := t.Node(id)
		fresh, exists := latest[id]
		if !exists {
			fresh = PersistedStep{NodeID: id, Kind: node.Kind, OnError: node.OnError}
		} else {
			fresh.RunIndex++
			if node.Kind == NodeCheckpoint {
				fresh.Lap++
			}
		}
		fresh.State = StepPending
		fresh.FailureKind = ""
		fresh.ExitReason = ""
		fresh.OutputPorts = nil
		fresh.Incoming = make(map[string]Receipt)
		result.Reset = append(result.Reset, fresh)
	}
	for _, edge := range t.Edges {
		if !resetSet[edge.To.Node] || resetSet[edge.From.Node] {
			continue
		}
		for _, step := range steps {
			if step.NodeID != edge.From.Node || !isTerminalStep(step.State) {
				continue
			}
			for _, receipt := range EmitReceipts(t, step) {
				if receipt.EdgeID == edge.ID {
					result.Receipts = append(result.Receipts, receipt)
					for i := range result.Reset {
						if result.Reset[i].NodeID == edge.To.Node {
							if result.Reset[i].Incoming == nil {
								result.Reset[i].Incoming = make(map[string]Receipt)
							}
							result.Reset[i].Incoming[edge.ID] = receipt
						}
					}
				}
			}
		}
	}
	sort.Slice(result.Reset, func(i, j int) bool { return result.Reset[i].NodeID < result.Reset[j].NodeID })
	sort.Slice(result.Superseded, func(i, j int) bool { return result.Superseded[i].NodeID < result.Superseded[j].NodeID })
	sort.Slice(result.Receipts, func(i, j int) bool { return result.Receipts[i].EdgeID < result.Receipts[j].EdgeID })
	return result, nil
}

func nodeReadiness(t *Template, node Node, receipts map[string]Receipt) (ready, blocked bool) {
	edges := t.IncomingEdges(node.ID)
	if node.Kind == NodeLoop {
		filtered := make([]Edge, 0, len(edges))
		for _, edge := range edges {
			if !t.IsCycleReturnEdge(edge) || receipts[edge.ID].State != "" {
				filtered = append(filtered, edge)
			}
		}
		edges = filtered
	}
	if len(edges) == 0 {
		return true, false
	}
	if node.Kind == NodeMerge {
		mode := configString(node, "mode")
		delivered, resolved := 0, 0
		dead := false
		for _, edge := range edges {
			r, ok := receipts[edge.ID]
			if !ok {
				continue
			}
			switch r.State {
			case ReceiptDelivered:
				delivered++
				resolved++
			case ReceiptSkipped, ReceiptDead:
				resolved++
				if r.State == ReceiptDead && mode == "all" {
					dead = true
				}
			}
		}
		if dead {
			return false, true
		}
		switch mode {
		case "all":
			return resolved == len(edges), false
		case "any":
			if delivered > 0 {
				return true, false
			}
			return resolved == len(edges), resolved == len(edges)
		case "n_of_m":
			n := configInt(node, "n")
			if delivered >= n {
				return true, false
			}
			if delivered+len(edges)-resolved < n {
				return false, true
			}
		}
		return false, false
	}
	allResolved, hasDead := true, false
	for _, edge := range edges {
		r, ok := receipts[edge.ID]
		if !ok || (r.State != ReceiptDelivered && r.State != ReceiptSkipped && r.State != ReceiptDead) {
			allResolved = false
		}
		if ok && r.State == ReceiptDead {
			hasDead = true
		}
	}
	if hasDead {
		return false, true
	}
	return allResolved, false
}

func configString(node Node, key string) string {
	var config map[string]json.RawMessage
	_ = json.Unmarshal(node.Config, &config)
	var value string
	_ = json.Unmarshal(config[key], &value)
	return value
}

func configInt(node Node, key string) int {
	var config map[string]json.RawMessage
	_ = json.Unmarshal(node.Config, &config)
	var value int
	_ = json.Unmarshal(config[key], &value)
	return value
}

func loopLimit(node Node) int { return configInt(node, "max_iterations") }

func maxAttempts(node Node) int {
	if attempts := configInt(node, "max_attempts"); attempts > 0 {
		return attempts
	}
	return 3
}

func hasCycleReceipt(t *Template, nodeID string, receipts map[string]Receipt) bool {
	for _, edge := range t.CycleReturnEdges(nodeID) {
		if r, ok := receipts[edge.ID]; ok && r.State == ReceiptDelivered {
			return true
		}
	}
	return false
}

func allIncomingSkipped(t *Template, nodeID string, receipts map[string]Receipt) bool {
	edges := t.IncomingEdges(nodeID)
	if len(edges) == 0 {
		return false
	}
	for _, edge := range edges {
		if r, ok := receipts[edge.ID]; !ok || r.State != ReceiptSkipped {
			return false
		}
	}
	return true
}

func isTerminalStep(state StepState) bool {
	switch state {
	case StepSucceeded, StepFailed, StepSkipped, StepBlocked:
		return true
	default:
		return false
	}
}

func checkpointRequest(step PersistedStep) WaitingCheckpoint {
	lap := step.Lap
	if lap < 1 {
		lap = 1
	}
	return WaitingCheckpoint{NodeID: step.NodeID, StepID: step.NodeID + ":" + step.IterationPath + ":" + fmt.Sprint(step.RunIndex), Lap: lap, Question: step.Question}
}

// nextRunIndex returns the execution index a node should run under.
//
// A step that is still pending is the SAME execution, not a new one: the row
// was created when the node became ready and is claimed when it runs. Only a
// finished execution advances the counter — a redo, a re-entry, a loop
// iteration.
func nextRunIndex(step PersistedStep, exists bool) int {
	if !exists {
		return 0
	}
	switch step.State {
	case StepPending, StepReady:
		return step.RunIndex
	}
	return step.RunIndex + 1
}

func cloneReceipts(receipts map[string]Receipt) map[string]Receipt {
	result := make(map[string]Receipt, len(receipts))
	for key, receipt := range receipts {
		result[key] = receipt
	}
	return result
}

func edgeTarget(t *Template, edgeID string) string {
	for _, edge := range t.Edges {
		if edge.ID == edgeID {
			return edge.To.Node
		}
	}
	return ""
}

func stepKey(nodeID, iterationPath string) string { return nodeID + "\x00" + iterationPath }
