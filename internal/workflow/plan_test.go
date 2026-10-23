package workflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPlanExampleGraphReadinessAndReceipts(t *testing.T) {
	graph := mustTemplate(t, exampleTemplate)
	tests := []struct {
		name  string
		steps []PersistedStep
		ready []string
	}{
		{name: "empty state", ready: []string{"n_pm"}},
		{name: "pm succeeded", steps: []PersistedStep{success("n_pm", "main")}, ready: []string{"n_gate"}},
		{name: "checkpoint approved", steps: []PersistedStep{success("n_pm", "main"), success("n_gate", "approved")}, ready: []string{"n_code"}},
		{name: "code releases concurrent branches", steps: []PersistedStep{success("n_pm", "main"), success("n_gate", "approved"), success("n_code", "main")}, ready: []string{"n_ci", "n_review"}},
		{name: "one side of diamond", steps: []PersistedStep{success("n_pm", "main"), success("n_gate", "approved"), success("n_code", "main"), success("n_review", "main")}, ready: []string{"n_ci"}},
		{name: "both sides release merge", steps: []PersistedStep{success("n_pm", "main"), success("n_gate", "approved"), success("n_code", "main"), success("n_review", "main"), success("n_ci", "main")}, ready: []string{"n_join"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := Decide(graph, tt.steps)
			if err != nil {
				t.Fatal(err)
			}
			if got := readyIDs(plan); !reflect.DeepEqual(got, tt.ready) {
				t.Fatalf("ready = %v, want %v; plan=%+v", got, tt.ready, plan)
			}
		})
	}

	steps := []PersistedStep{success("n_pm", "main"), success("n_gate", "approved"), success("n_code", "main"), success("n_review", "main"), {NodeID: "n_ci", State: StepFailed, Kind: NodeIntegration, OnError: OnErrorStop, FailureKind: FailureCapability}}
	plan, err := Decide(graph, steps)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RunState != "failed" || !containsString(plan.Blocked, "n_join") {
		t.Fatalf("dead merge receipt plan = %+v, want failed run and blocked merge", plan)
	}
}

func TestPlanOnErrorReceiptPolicies(t *testing.T) {
	graph := linearFailureGraph()
	tests := []struct {
		policy        OnError
		wantReceipt   ReceiptState
		wantPort      string
		wantDescReady bool
		wantRun       string
	}{
		{OnErrorStop, ReceiptDead, "main", false, "failed"},
		{OnErrorContinueRegular, ReceiptDelivered, "main", true, "running"},
		{OnErrorContinueError, ReceiptDead, "main", false, "running"},
	}
	for _, tt := range tests {
		t.Run(string(tt.policy), func(t *testing.T) {
			graph.Nodes[0].OnError = tt.policy
			if tt.policy == OnErrorContinueError {
				graph.Edges = append(graph.Edges, edge("failure", "bad", "error", "error_sink", "in"))
				graph.Nodes = append(graph.Nodes, node("error_sink", NodeAgent, `{"agent_id":"err"}`))
			}
			failed := PersistedStep{NodeID: "bad", State: StepFailed, Kind: NodeAgent, OnError: tt.policy, FailureKind: FailureCapability}
			plan, err := Decide(graph, []PersistedStep{success("root", "main"), failed})
			if err != nil {
				t.Fatal(err)
			}
			var receipt *Receipt
			for i := range plan.Receipts {
				if plan.Receipts[i].EdgeID == "next" {
					receipt = &plan.Receipts[i]
				}
			}
			if receipt == nil || receipt.State != tt.wantReceipt || receipt.Port != tt.wantPort {
				t.Fatalf("main receipt = %+v, want %s on %s", receipt, tt.wantReceipt, tt.wantPort)
			}
			if plan.RunState != tt.wantRun {
				t.Fatalf("run state = %q, want %q (plan=%+v)", plan.RunState, tt.wantRun, plan)
			}
			if containsString(readyIDs(plan), "desc") != tt.wantDescReady {
				t.Fatalf("desc ready = %v, want %t", readyIDs(plan), tt.wantDescReady)
			}
			if tt.policy == OnErrorContinueRegular {
				if string(receipt.Value) != "null" {
					t.Fatalf("continue regular value = %s, want empty JSON null", receipt.Value)
				}
			}
			if tt.policy == OnErrorContinueError {
				if got := receiptByEdge(plan.Receipts, "failure"); got.State != ReceiptDelivered {
					t.Fatalf("error receipt = %+v, want delivered", got)
				}
			}
		})
	}
}

func TestPlanFailureKinds(t *testing.T) {
	graph := linearFailureGraph()
	graph.Nodes[1].OnError = OnErrorContinueRegular
	tests := []struct {
		name        string
		kind        FailureKind
		attempt     int
		wantReady   string
		wantAttempt int
		wantBlocked bool
	}{
		{"transient retries", FailureTransient, 1, "bad", 2, false},
		{"capability routes without retry", FailureCapability, 1, "desc", 0, false},
		{"dependency blocks", FailureDependency, 1, "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := Decide(graph, []PersistedStep{success("root", "main"), {NodeID: "bad", State: StepFailed, Kind: NodeAgent, OnError: OnErrorContinueRegular, FailureKind: tt.kind, Attempt: tt.attempt}})
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantReady != "" {
				if len(plan.Ready) != 1 || plan.Ready[0].NodeID != tt.wantReady || plan.Ready[0].Attempt != tt.wantAttempt {
					t.Fatalf("ready = %+v, want %s attempt %d", plan.Ready, tt.wantReady, tt.wantAttempt)
				}
			}
			if got := containsString(plan.Blocked, "bad"); got != tt.wantBlocked {
				t.Fatalf("bad blocked = %t, want %t", got, tt.wantBlocked)
			}
		})
	}
}

func TestPlanMergeModes(t *testing.T) {
	for _, tt := range []struct {
		mode        string
		n           int
		receipts    []ReceiptState
		wantReady   bool
		wantBlocked bool
	}{
		{"any", 0, []ReceiptState{ReceiptDelivered}, true, false},
		{"any", 0, []ReceiptState{ReceiptDead, ReceiptDead, ReceiptDead}, false, true},
		{"n_of_m", 2, []ReceiptState{ReceiptDelivered, ReceiptDead, ReceiptDelivered}, true, false},
		{"n_of_m", 2, []ReceiptState{ReceiptDelivered, ReceiptDead, ReceiptDead}, false, true},
		{"n_of_m", 2, []ReceiptState{ReceiptDelivered}, false, false},
	} {
		t.Run(tt.mode+string(rune('0'+len(tt.receipts))), func(t *testing.T) {
			graph := mergeGraph(tt.mode, tt.n, 3)
			incoming := make(map[string]Receipt)
			for i, state := range tt.receipts {
				id := "edge" + string(rune('a'+i))
				incoming[id] = Receipt{EdgeID: id, State: state}
			}
			plan, err := Decide(graph, []PersistedStep{{NodeID: "join", State: StepPending, Incoming: incoming}})
			if err != nil {
				t.Fatal(err)
			}
			if got := containsString(readyIDs(plan), "join"); got != tt.wantReady {
				t.Fatalf("join ready = %t, want %t; plan=%+v", got, tt.wantReady, plan)
			}
			if got := containsString(plan.Blocked, "join"); got != tt.wantBlocked {
				t.Fatalf("join blocked = %t, want %t; plan=%+v", got, tt.wantBlocked, plan)
			}
		})
	}
}

func TestPlanLoopBudgetAndDonePort(t *testing.T) {
	graph := loopGraph(2)
	first, err := Decide(graph, nil)
	if err != nil || len(first.Ready) != 1 || first.Ready[0].NodeID != "loop" || first.Ready[0].Iteration != 1 {
		t.Fatalf("initial loop plan = %+v, error %v", first, err)
	}
	body, err := Decide(graph, []PersistedStep{{NodeID: "loop", State: StepSucceeded, Kind: NodeLoop, Iteration: 1, OutputPorts: map[string]json.RawMessage{"body": json.RawMessage(`{}`)}}})
	if err != nil || !containsString(readyIDs(body), "body") {
		t.Fatalf("body plan = %+v, error %v", body, err)
	}
	cycleState := PersistedStep{NodeID: "body", State: StepSucceeded, Kind: NodeAgent, OutputPorts: map[string]json.RawMessage{"main": json.RawMessage(`{}`)}}
	next, err := Decide(graph, []PersistedStep{{NodeID: "loop", State: StepSucceeded, Kind: NodeLoop, Iteration: 1, OutputPorts: map[string]json.RawMessage{"body": json.RawMessage(`{}`)}}, cycleState})
	if err != nil || len(next.Ready) != 1 || next.Ready[0].NodeID != "loop" || next.Ready[0].Iteration != 2 {
		t.Fatalf("cycle re-entry plan = %+v, error %v", next, err)
	}
	exhausted, err := Decide(graph, []PersistedStep{{NodeID: "loop", State: StepSucceeded, Kind: NodeLoop, Iteration: 2, OutputPorts: map[string]json.RawMessage{"body": json.RawMessage(`{}`)}}, cycleState})
	if err != nil || len(exhausted.GeneratedFailed) != 1 || exhausted.GeneratedFailed[0].ExitReason != "cycle_budget_exhausted" {
		t.Fatalf("exhaustion = %+v, error %v", exhausted, err)
	}

	doneGraph := loopGraph(2)
	done, err := Decide(doneGraph, []PersistedStep{{NodeID: "loop", State: StepSucceeded, Kind: NodeLoop, Iteration: 1, OutputPorts: map[string]json.RawMessage{"done": json.RawMessage(`true`)}}})
	if err != nil || !containsString(readyIDs(done), "done") {
		t.Fatalf("done port plan = %+v, error %v", done, err)
	}
}

func TestPlanRunStatesAndNeedsInput(t *testing.T) {
	graph := mustTemplate(t, exampleTemplate)
	waitingAndRunning := []PersistedStep{
		{NodeID: "n_gate", State: StepWaiting, Kind: NodeCheckpoint, Lap: 2, Question: "approve?"},
		{NodeID: "n_ci", State: StepRunning, Kind: NodeIntegration},
	}
	plan, err := Decide(graph, waitingAndRunning)
	if err != nil || plan.RunState != "running" || len(plan.Waiting) != 1 {
		t.Fatalf("mixed wait/running plan = %+v, error %v", plan, err)
	}
	checkpointGraph := graphWithNodes(node("gate", NodeCheckpoint, `{"policy":"retry_step"}`))
	checkpointGraph.Nodes[0].OnError = OnErrorStop
	parked, err := Decide(checkpointGraph, []PersistedStep{{NodeID: "gate", State: StepWaiting, Kind: NodeCheckpoint, Lap: 1, Question: "credential?"}})
	if err != nil || parked.RunState != "waiting_human" {
		t.Fatalf("checkpoint-only plan = %+v, error %v", parked, err)
	}
	failedInput, err := Decide(checkpointGraph, []PersistedStep{{NodeID: "gate", State: StepFailed, Kind: NodeCheckpoint, FailureKind: FailureNeedsInput, Question: "credential?", Lap: 3}})
	if err != nil || failedInput.RunState != "waiting_human" || len(failedInput.Waiting) != 1 || failedInput.Waiting[0].Question != "credential?" || failedInput.Waiting[0].Lap != 3 {
		t.Fatalf("needs-input plan = %+v, error %v", failedInput, err)
	}
	completed, err := Decide(graph, allSucceeded(graph))
	if err != nil || completed.RunState != "completed" || !completed.Terminal {
		t.Fatalf("completed plan = %+v, error %v", completed, err)
	}
	blockedGraph := linearFailureGraph()
	blocked, err := Decide(blockedGraph, []PersistedStep{success("root", "main"), {NodeID: "bad", State: StepFailed, Kind: NodeAgent, OnError: OnErrorStop, FailureKind: FailureDependency}})
	if err != nil || blocked.RunState != "failed" {
		t.Fatalf("blocked plan = %+v, error %v", blocked, err)
	}
}

func TestRedoUsesDescendantsAndPreservesSibling(t *testing.T) {
	graph := mustTemplate(t, exampleTemplate)
	steps := []PersistedStep{
		success("n_pm", "main"), success("n_gate", "approved"), success("n_code", "main"),
		success("n_review", "main"), success("n_ci", "main"), success("n_join", "main"), success("n_pr", "main"),
	}
	redo, err := Redo(graph, "n_review", steps)
	if err != nil {
		t.Fatal(err)
	}
	resetIDs := make([]string, 0, len(redo.Reset))
	for _, step := range redo.Reset {
		resetIDs = append(resetIDs, step.NodeID)
		if step.State != StepPending {
			t.Fatalf("reset step %s state = %s, want pending", step.NodeID, step.State)
		}
	}
	if !reflect.DeepEqual(resetIDs, []string{"n_join", "n_pr", "n_review"}) {
		t.Fatalf("reset descendants = %v, want [n_join n_pr n_review]", resetIDs)
	}
	if !containsString(redo.SupersededIDs(), "n_review") || containsString(resetIDs, "n_ci") {
		t.Fatalf("redo superseded/reset sets = %+v", redo)
	}
	if got := receiptByEdge(redo.Receipts, "e6"); got.State != ReceiptDelivered {
		t.Fatalf("preserved sibling receipt e6 = %+v, want delivered", got)
	}
}

func success(nodeID, port string) PersistedStep {
	return PersistedStep{NodeID: nodeID, State: StepSucceeded, OutputPorts: map[string]json.RawMessage{port: json.RawMessage(`{}`)}}
}

func readyIDs(plan Plan) []string {
	ids := make([]string, 0, len(plan.Ready))
	for _, node := range plan.Ready {
		ids = append(ids, node.NodeID)
	}
	return ids
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func receiptByEdge(receipts []Receipt, id string) Receipt {
	for _, receipt := range receipts {
		if receipt.EdgeID == id {
			return receipt
		}
	}
	return Receipt{}
}

func linearFailureGraph() *Template {
	graph := graphWithNodes(node("root", NodeAgent, `{"agent_id":"root"}`), node("bad", NodeAgent, `{"agent_id":"bad"}`), node("desc", NodeAgent, `{"agent_id":"desc"}`))
	graph.Nodes[0].OnError = OnErrorStop
	graph.Nodes[1].OnError = OnErrorStop
	graph.Nodes[2].OnError = OnErrorStop
	graph.Edges = []Edge{edge("start", "root", "main", "bad", "in"), edge("next", "bad", "main", "desc", "in")}
	return graph
}

func mergeGraph(mode string, n, count int) *Template {
	nodes := []Node{node("join", NodeMerge, `{"mode":"`+mode+`","n":`+string(rune('0'+n))+`}`)}
	edges := make([]Edge, 0, count)
	for i := 0; i < count; i++ {
		id := "source" + string(rune('a'+i))
		nodes = append(nodes, node(id, NodeAgent, `{"agent_id":"`+id+`"}`))
		edges = append(edges, edge("edge"+string(rune('a'+i)), id, "main", "join", "in"))
	}
	graph := graphWithNodes(nodes...)
	graph.Edges = edges
	graph.Nodes[0].OnError = OnErrorStop
	return graph
}

func loopGraph(limit int) *Template {
	graph := graphWithNodes(node("loop", NodeLoop, `{"until":"done","max_iterations":`+string(rune('0'+limit))+`}`), node("body", NodeAgent, `{"agent_id":"body"}`), node("done", NodeAgent, `{"agent_id":"done"}`))
	for i := range graph.Nodes {
		graph.Nodes[i].OnError = OnErrorContinueRegular
	}
	graph.Edges = []Edge{edge("body-edge", "loop", "body", "body", "in"), edge("return", "body", "main", "loop", "in"), edge("done-edge", "loop", "done", "done", "in")}
	return graph
}

func allSucceeded(graph *Template) []PersistedStep {
	steps := make([]PersistedStep, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		ports := map[string]json.RawMessage{}
		for _, edge := range graph.OutgoingEdges(node.ID) {
			ports[edge.From.Port] = json.RawMessage(`{}`)
		}
		steps = append(steps, PersistedStep{NodeID: node.ID, State: StepSucceeded, Kind: node.Kind, OutputPorts: ports})
	}
	return steps
}

func (r RedoPlan) SupersededIDs() []string {
	ids := make([]string, 0, len(r.Superseded))
	for _, step := range r.Superseded {
		ids = append(ids, step.NodeID)
	}
	return ids
}
