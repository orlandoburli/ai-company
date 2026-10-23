package workflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

const exampleTemplate = `{
  "schema_version": 1,
  "nodes": [
    {"id": "n_pm", "kind": "agent", "name": "spec", "type": "agent.pm",
     "type_version": 2, "config": {"agent_id": "a_pm", "outputs": {"spec_md": "string"}},
     "position": {"x": 0, "y": 0}, "on_error": "stop_workflow",
     "always_output_data": false, "workspace": "scratch"},
    {"id": "n_gate", "kind": "checkpoint", "name": "approve spec",
     "type": "checkpoint.approval", "type_version": 1,
     "config": {"policy": "retry_step", "max_cycles": 2},
     "position": {"x": 240, "y": 0}, "on_error": "stop_workflow",
     "always_output_data": false, "workspace": "scratch"},
    {"id": "n_code", "kind": "agent", "name": "implement", "type": "agent.coder",
     "type_version": 3, "config": {"agent_id": "a_dev", "outputs": {"pr_url": "string"}},
     "position": {"x": 480, "y": 0}, "on_error": "continue_error_output",
     "always_output_data": false, "workspace": "worktree"},
    {"id": "n_review", "kind": "agent", "name": "review", "type": "agent.reviewer",
     "type_version": 1, "config": {"agent_id": "a_rev"},
     "position": {"x": 720, "y": -80}, "on_error": "stop_workflow",
     "always_output_data": false, "workspace": "scratch"},
    {"id": "n_ci", "kind": "integration", "name": "wait ci", "type": "github.wait_ci",
     "type_version": 1, "config": {"timeout_s": 1800},
     "position": {"x": 720, "y": 80}, "on_error": "stop_workflow",
     "always_output_data": false, "workspace": "scratch"},
    {"id": "n_join", "kind": "merge", "name": "reconcile", "type": "merge.all",
     "type_version": 1, "config": {"mode": "all"},
     "position": {"x": 960, "y": 0}, "on_error": "stop_workflow",
     "always_output_data": true, "workspace": "worktree"},
    {"id": "n_pr", "kind": "integration", "name": "open pr", "type": "github.open_pr",
     "type_version": 1, "config": {}, "position": {"x": 1200, "y": 0},
     "on_error": "stop_workflow", "always_output_data": false, "workspace": "worktree"}
  ],
  "edges": [
    {"id": "e1", "from": {"node": "n_pm", "port": "main"},
     "to": {"node": "n_gate", "port": "in"}},
    {"id": "e2", "from": {"node": "n_gate", "port": "approved"},
     "to": {"node": "n_code", "port": "in"}},
    {"id": "e3", "from": {"node": "n_code", "port": "main"},
     "to": {"node": "n_review", "port": "in"}},
    {"id": "e4", "from": {"node": "n_code", "port": "main"},
     "to": {"node": "n_ci", "port": "in"}},
    {"id": "e5", "from": {"node": "n_review", "port": "main"},
     "to": {"node": "n_join", "port": "in"}},
    {"id": "e6", "from": {"node": "n_ci", "port": "main"},
     "to": {"node": "n_join", "port": "in"}},
    {"id": "e7", "from": {"node": "n_join", "port": "main"},
     "to": {"node": "n_pr", "port": "in"}}
  ]
}`

func TestParseExampleTemplate(t *testing.T) {
	graph, err := ParseTemplate([]byte(exampleTemplate))
	if err != nil {
		t.Fatal(err)
	}
	if got := graph.EntryNodes(); !reflect.DeepEqual(got, []string{"n_pm"}) {
		t.Fatalf("EntryNodes() = %v, want [n_pm]", got)
	}
	if got := len(graph.IncomingEdges("n_join")); got != 2 {
		t.Fatalf("n_join incoming edge count = %d, want 2", got)
	}
}

func TestValidationErrors(t *testing.T) {
	valid := mustTemplate(t, exampleTemplate)
	cycleWithoutBudget := graphWithNodes(
		node("root", NodeAgent, `{"agent_id":"a"}`),
		node("a", NodeAgent, `{"agent_id":"a"}`),
		node("b", NodeAgent, `{"agent_id":"b"}`),
	)
	cycleWithoutBudget.Edges = []Edge{
		edge("r-a", "root", "main", "a", "in"), edge("a-b", "a", "main", "b", "in"), edge("b-a", "b", "main", "a", "in"),
	}
	cycleWithBudget := graphWithNodes(
		node("root", NodeAgent, `{"agent_id":"a"}`),
		node("loop", NodeLoop, `{"max_iterations":3,"until":true}`),
		node("body", NodeAgent, `{"agent_id":"b"}`),
	)
	cycleWithBudget.Edges = []Edge{
		edge("start", "root", "main", "loop", "in"), edge("body", "loop", "body", "body", "in"), edge("back", "body", "main", "loop", "in"),
	}
	badSchema := *valid
	badSchema.SchemaVersion = 2
	duplicateNode := *valid
	duplicateNode.Nodes = append(append([]Node(nil), valid.Nodes...), valid.Nodes[0])
	emptyNodeID := graphWithNodes(node("", NodeAgent, `{"agent_id":"a"}`))
	unknownEndpoint := *valid
	unknownEndpoint.Edges = append(append([]Edge(nil), valid.Edges...), edge("missing", "n_pm", "main", "ghost", "in"))
	duplicateEdgeID := *valid
	duplicateEdgeID.Edges = append(append([]Edge(nil), valid.Edges...), valid.Edges[0])
	emptyEdgeID := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`))
	emptyEdgeID.Edges = []Edge{edge("", "a", "main", "a", "in")}
	duplicatePair := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`), node("b", NodeAgent, `{"agent_id":"b"}`))
	duplicatePair.Edges = []Edge{edge("one", "a", "main", "b", "in"), edge("two", "a", "main", "b", "in")}
	toOutput := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`), node("b", NodeAgent, `{"agent_id":"b"}`))
	toOutput.Edges = []Edge{edge("bad", "a", "main", "b", "main")}
	fromInput := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`), node("b", NodeAgent, `{"agent_id":"b"}`))
	fromInput.Edges = []Edge{edge("bad", "a", "in", "b", "in")}
	noEntry := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`), node("b", NodeAgent, `{"agent_id":"b"}`))
	noEntry.Edges = []Edge{edge("ab", "a", "main", "b", "in"), edge("ba", "b", "main", "a", "in")}
	unreachable := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`), node("b", NodeAgent, `{"agent_id":"b"}`), node("c", NodeAgent, `{"agent_id":"c"}`))
	unreachable.Edges = []Edge{edge("bc", "b", "main", "c", "in"), edge("cb", "c", "main", "b", "in")}
	badMerge := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`), node("m", NodeMerge, `{"mode":"n_of_m","n":2}`))
	badMerge.Edges = []Edge{edge("am", "a", "main", "m", "in")}
	badOutput := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a","outputs":{"x":"date"}}`))
	badOnError := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`))
	badOnError.Nodes[0].OnError = "continue"
	emptyOnError := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`))
	emptyOnError.Nodes[0].OnError = ""
	badWorkspace := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`))
	badWorkspace.Nodes[0].Workspace = "unknown"
	badKind := graphWithNodes(node("a", NodeKind("other"), `{}`))
	badType := graphWithNodes(node("a", NodeAgent, `{"agent_id":"a"}`))
	badType.Nodes[0].TypeVersion = 0
	missingAgent := graphWithNodes(node("a", NodeAgent, `{}`))
	badGoto := graphWithNodes(node("a", NodeCheckpoint, `{"policy":"goto_step","target":"ghost"}`))
	badGoto.Nodes = append(badGoto.Nodes, node("b", NodeAgent, `{"agent_id":"b"}`))
	badGoto.Edges = []Edge{edge("ab", "a", "approved", "b", "in")}
	badCheckpointPolicy := graphWithNodes(node("a", NodeCheckpoint, `{"policy":"skip"}`))
	badLoop := graphWithNodes(node("a", NodeLoop, `{"max_iterations":0,"until":true}`))

	tests := []struct {
		name  string
		graph *Template
		code  string
	}{
		{"schema version", &badSchema, CodeSchemaVersion},
		{"empty graph", &Template{SchemaVersion: 1}, CodeEmptyGraph},
		{"duplicate node id", &duplicateNode, CodeDuplicateNodeID},
		{"empty node id", emptyNodeID, CodeInvalidNodeID},
		{"unknown endpoint", &unknownEndpoint, CodeUnknownNode},
		{"duplicate edge id", &duplicateEdgeID, CodeDuplicateEdgeID},
		{"empty edge id", emptyEdgeID, CodeInvalidEdgeID},
		{"duplicate edge pair", duplicatePair, CodeDuplicateEdgePair},
		{"edge to output port", toOutput, CodeInvalidPort},
		{"edge from input port", fromInput, CodeInvalidPort},
		{"no entry node", noEntry, CodeNoEntryNode},
		{"unreachable node", unreachable, CodeUnreachableNode},
		{"cycle without loop budget", cycleWithoutBudget, CodeCycleWithoutBudget},
		{"cycle with loop budget", cycleWithBudget, ""},
		{"merge n_of_m over budget", badMerge, CodeMergeBudget},
		{"bad output type", badOutput, CodeInvalidOutputType},
		{"bad on_error", badOnError, CodeInvalidOnError},
		{"empty on_error is not defaulted", emptyOnError, CodeInvalidOnError},
		{"bad workspace", badWorkspace, CodeInvalidWorkspace},
		{"bad kind", badKind, CodeInvalidKind},
		{"missing node type version", badType, CodeInvalidNodeType},
		{"missing agent id", missingAgent, CodeInvalidConfig},
		{"goto_step unknown target", badGoto, CodeInvalidConfig},
		{"bad checkpoint policy", badCheckpointPolicy, CodeInvalidConfig},
		{"bad loop config", badLoop, CodeInvalidConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.graph.Validate()
			if tt.code == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("Validate() error = %v, want *ValidationError", err)
			}
			if validation.Code != tt.code {
				t.Fatalf("error code = %q, want %q (error: %v)", validation.Code, tt.code, err)
			}
		})
	}
}

func TestParseTemplateIsStrictObjectJSON(t *testing.T) {
	for _, input := range []string{`[]`, `null`, `{"schema_version":1,"nodes":[],"edges":[],"extra":true}`} {
		if _, err := ParseTemplate([]byte(input)); err == nil {
			t.Errorf("ParseTemplate(%s) unexpectedly succeeded", input)
		}
	}
}

func TestJSONRoundTrip(t *testing.T) {
	original := mustTemplate(t, exampleTemplate)
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseTemplate(data)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(original)
	gotJSON, _ := json.Marshal(got)
	if !reflect.DeepEqual(wantJSON, gotJSON) {
		t.Fatalf("round-trip JSON differs\nwant: %s\n got: %s", wantJSON, gotJSON)
	}
}

func TestMemoryTierJSONShape(t *testing.T) {
	var selector MemorySelector
	if err := json.Unmarshal([]byte(`{"run":"full","agent":"auto","workspace":["repo","conventions"]}`), &selector); err != nil {
		t.Fatal(err)
	}
	if selector.Run.Mode != MemoryFull || selector.Agent.Mode != MemoryAuto || !reflect.DeepEqual(selector.Workspace.Keys, []string{"repo", "conventions"}) {
		t.Fatalf("unexpected memory selector: %#v", selector)
	}
}

func mustTemplate(t *testing.T, input string) *Template {
	t.Helper()
	graph, err := ParseTemplate([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func node(id string, kind NodeKind, config string) Node {
	return Node{ID: id, Kind: kind, Name: id, Type: string(kind) + ".test", TypeVersion: 1, Config: json.RawMessage(config), OnError: OnErrorStop}
}

func edge(id, from, fromPort, to, toPort string) Edge {
	return Edge{ID: id, From: PortRef{Node: from, Port: fromPort}, To: PortRef{Node: to, Port: toPort}}
}

func graphWithNodes(nodes ...Node) *Template { return &Template{SchemaVersion: 1, Nodes: nodes} }
