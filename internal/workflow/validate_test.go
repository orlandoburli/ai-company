package workflow

import "testing"

func TestValidateAllCollectsIndependentProblems(t *testing.T) {
	graph := graphWithNodes(
		node("entry", NodeAgent, `{"agent_id":"a"}`),
		node("duplicate", NodeAgent, `{"agent_id":"b"}`),
		node("duplicate", NodeAgent, `{"agent_id":"c"}`),
		node("orphan", NodeAgent, `{"agent_id":"d"}`),
		node("orphan-peer", NodeAgent, `{"agent_id":"e"}`),
	)
	graph.Edges = []Edge{
		edge("unknown-edge", "entry", "main", "missing", "in"),
		edge("wrong-port-edge", "entry", "main", "duplicate", "not-a-port"),
		edge("orphan-forward", "orphan", "main", "orphan-peer", "in"),
		edge("orphan-return", "orphan-peer", "main", "orphan", "in"),
	}

	problems := ValidateAll(graph)
	want := map[string]string{
		CodeDuplicateNodeID: "duplicate",
		CodeUnknownNode:     "unknown-edge",
		CodeInvalidPort:     "wrong-port-edge",
		CodeUnreachableNode: "orphan",
	}
	got := make(map[string]string, len(problems))
	for _, problem := range problems {
		id := problem.NodeID
		if problem.EdgeID != "" {
			id = problem.EdgeID
		}
		got[problem.Code] = id
	}
	for code, id := range want {
		if got[code] != id {
			t.Errorf("problem %q id = %q, want %q (problems: %#v)", code, got[code], id, problems)
		}
	}
	if len(problems) < len(want) {
		t.Fatalf("got %d problems, want at least %d: %#v", len(problems), len(want), problems)
	}

	firstErr := graph.Validate()
	first, ok := firstErr.(*ValidationError)
	if !ok {
		t.Fatalf("Validate() error = %v, want *ValidationError", firstErr)
	}
	if len(problems) == 0 || problems[0].Code != first.Code || problems[0].Message != first.Message {
		t.Fatalf("first ValidateAll problem = %#v, Validate() = %#v", problems[0], first)
	}
}

func TestValidateAllValidGraph(t *testing.T) {
	graph := graphWithNodes(node("entry", NodeAgent, `{"agent_id":"a"}`))
	if problems := ValidateAll(graph); len(problems) != 0 {
		t.Fatalf("ValidateAll(valid) = %#v, want no problems", problems)
	}
}
