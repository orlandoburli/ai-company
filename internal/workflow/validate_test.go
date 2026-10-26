package workflow

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

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
	// The validator walks the nodes in the graph's own order, so the first
	// unreachable node it names is always the same one.
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

// The start node takes no input and hands the flow to one successor; the end
// node takes the flow's last input and has no successor.
func TestPortsForFlowEndpoints(t *testing.T) {
	inputs, outputs := PortsFor(NodeStart)
	if len(inputs) != 0 {
		t.Fatalf("start inputs = %v, want none", inputs)
	}
	if !reflect.DeepEqual(outputs, []string{"main"}) {
		t.Fatalf("start outputs = %v, want [main]", outputs)
	}
	inputs, outputs = PortsFor(NodeEnd)
	if !reflect.DeepEqual(inputs, []string{"in"}) {
		t.Fatalf("end inputs = %v, want [in]", inputs)
	}
	if len(outputs) != 0 {
		t.Fatalf("end outputs = %v, want none", outputs)
	}
}

// endpointCase is one graph with one broken endpoint rule.
type endpointCase struct {
	name  string
	graph *Template
	code  string
	node  string
}

// endpointCases are the graphs the endpoint rules are about.
func endpointCases() []endpointCase {
	start := node("n_start", NodeStart, `{}`)
	secondStart := node("n_start2", NodeStart, `{}`)
	end := node("n_end", NodeEnd, `{}`)
	secondEnd := node("n_end2", NodeEnd, `{}`)
	work := node("n_work", NodeAgent, `{"agent_id":"a"}`)
	other := node("n_other", NodeAgent, `{"agent_id":"b"}`)

	twoStarts := graphWithNodes(start, secondStart, work)
	twoStarts.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_start2", "main", "n_work", "in"),
	}
	twoEnds := graphWithNodes(start, end, secondEnd, work)
	twoEnds.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_work", "main", "n_end", "in"),
		edge("e3", "n_work", "main", "n_end2", "in"),
	}
	startWithoutSuccessor := graphWithNodes(start, work)
	startWithTwoSuccessors := graphWithNodes(start, work, other)
	startWithTwoSuccessors.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_start", "main", "n_other", "in"),
	}
	startWithInput := graphWithNodes(start, work)
	startWithInput.Edges = []Edge{edge("e1", "n_work", "main", "n_start", "in")}
	endWithSuccessor := graphWithNodes(start, work, end)
	endWithSuccessor.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_work", "main", "n_end", "in"),
		edge("e3", "n_end", "main", "n_work", "in"),
	}
	endWithoutInput := graphWithNodes(start, work, end)
	endWithoutInput.Edges = []Edge{edge("e1", "n_start", "main", "n_work", "in")}
	// The start is not the only entry: n_other has no incoming edge, so the
	// flow would never reach it.
	secondEntry := graphWithNodes(start, work, other)
	secondEntry.Edges = []Edge{edge("e1", "n_start", "main", "n_work", "in")}
	// The end is not reachable: n_other loops into itself and feeds the end.
	unreachableEnd := graphWithNodes(start, work, other, end)
	unreachableEnd.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_other", "main", "n_other", "in"),
		edge("e3", "n_other", "main", "n_end", "in"),
	}
	// A graph without a start and without an end is still valid: the flow
	// begins and ends where it always did.
	implicit := graphWithNodes(work, other)
	implicit.Edges = []Edge{edge("e1", "n_work", "main", "n_other", "in")}
	// A graph that names both ends is valid when the rules hold.
	explicit := graphWithNodes(start, work, end)
	explicit.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_work", "main", "n_end", "in"),
	}

	return []endpointCase{
		{"two start nodes", twoStarts, CodeDuplicateStartNode, "n_start2"},
		{"two end nodes", twoEnds, CodeDuplicateEndNode, "n_end2"},
		{"start without a successor", startWithoutSuccessor, CodeStartNodeEdges, "n_start"},
		{"start with two successors", startWithTwoSuccessors, CodeStartNodeEdges, "n_start"},
		{"start with an incoming edge", startWithInput, CodeStartNodeEdges, "n_start"},
		{"end with a successor", endWithSuccessor, CodeEndNodeEdges, "n_end"},
		{"end without an incoming edge", endWithoutInput, CodeEndNodeEdges, "n_end"},
		{"start is not the only entry", secondEntry, CodeUnexpectedEntryNode, "n_other"},
		{"unreachable end", unreachableEnd, CodeUnreachableEndNode, "n_end"},
		{"no start and no end", implicit, "", ""},
		{"start and end", explicit, "", ""},
	}
}

// A graph names where its flow begins and where it ends. The rules below are
// the ones the editor's panel shows.
func TestValidateFlowEndpoints(t *testing.T) {
	for _, tt := range endpointCases() {
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
			if tt.node != "" && !strings.Contains(validation.Message, `"`+tt.node+`"`) {
				t.Fatalf("the message does not name %q: %v", tt.node, err)
			}
		})
	}
}

// The panel repairs a graph to find the next problem: the endpoint rules must
// not send it in circles.
func TestValidateAllRepairsTheEndpointProblems(t *testing.T) {
	for _, tt := range endpointCases() {
		if tt.code == "" {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			problems := ValidateAll(tt.graph)
			if len(problems) > 10 {
				t.Fatalf("ValidateAll found %d problems, want a handful: %#v", len(problems), problems)
			}
			found := false
			for _, problem := range problems {
				if problem.Code != tt.code {
					continue
				}
				found = true
				if problem.NodeID != tt.node {
					t.Fatalf("the %q problem names %q, want %q", tt.code, problem.NodeID, tt.node)
				}
			}
			if !found {
				t.Fatalf("ValidateAll did not report %q: %#v", tt.code, problems)
			}
		})
	}
}

// The panel names the node a problem is about: the second start, the second
// end, and the node the flow left behind.
func TestValidateAllNamesTheOffendingNode(t *testing.T) {
	graph := graphWithNodes(node("n_start", NodeStart, `{}`), node("n_start2", NodeStart, `{}`), node("n_work", NodeAgent, `{"agent_id":"a"}`))
	graph.Edges = []Edge{edge("e1", "n_start", "main", "n_work", "in")}

	problems := ValidateAll(graph)
	if len(problems) == 0 {
		t.Fatal("ValidateAll found no problems")
	}
	if problems[0].Code != CodeDuplicateStartNode {
		t.Fatalf("first problem = %#v, want %q", problems[0], CodeDuplicateStartNode)
	}
	if problems[0].NodeID != "n_start2" {
		t.Fatalf("the problem names %q, want n_start2", problems[0].NodeID)
	}
}

// A published workflow begins and ends. The completeness check is the gate
// between a draft and a publish, and every refusal names what is missing.
func TestValidateCompleteRefusesAnIncompleteGraph(t *testing.T) {
	start := node("n_start", NodeStart, `{}`)
	secondStart := node("n_start2", NodeStart, `{}`)
	end := node("n_end", NodeEnd, `{}`)
	secondEnd := node("n_end2", NodeEnd, `{}`)
	work := node("n_work", NodeAgent, `{"agent_id":"a"}`)

	noNodes := &Template{SchemaVersion: 1}
	noStart := graphWithNodes(work, end)
	noStart.Edges = []Edge{edge("e1", "n_work", "main", "n_end", "in")}
	noEnd := graphWithNodes(start, work)
	noEnd.Edges = []Edge{edge("e1", "n_start", "main", "n_work", "in")}
	twoStarts := graphWithNodes(start, secondStart, work)
	twoStarts.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_start2", "main", "n_work", "in"),
	}
	twoEnds := graphWithNodes(start, work, end, secondEnd)
	twoEnds.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_work", "main", "n_end", "in"),
		edge("e3", "n_work", "main", "n_end2", "in"),
	}
	complete := graphWithNodes(start, work, end)
	complete.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_work", "main", "n_end", "in"),
	}

	tests := []struct {
		name  string
		graph *Template
		code  string
		says  string
	}{
		{"no nodes", noNodes, CodeEmptyGraph, "no nodes"},
		{"no start", noStart, CodeMissingStartNode, "no start point"},
		{"no end", noEnd, CodeMissingEndNode, "no end point"},
		{"two starts", twoStarts, CodeDuplicateStartNode, ""},
		{"two ends", twoEnds, CodeDuplicateEndNode, ""},
		{"complete", complete, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.graph.ValidateComplete()
			if tt.code == "" {
				if err != nil {
					t.Fatalf("ValidateComplete() error = %v, want nil", err)
				}
				return
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("ValidateComplete() error = %v, want *ValidationError", err)
			}
			if validation.Code != tt.code {
				t.Fatalf("error code = %q, want %q (error: %v)", validation.Code, tt.code, err)
			}
			if tt.says != "" && !strings.Contains(validation.Message, tt.says) {
				t.Fatalf("the message does not say %q: %v", tt.says, err)
			}
		})
	}
}

// The panel asks for everything the workflow still needs, not just the first
// thing: a blank canvas has no start point and no end point.
func TestCompletenessProblemsReportEveryMissingPiece(t *testing.T) {
	problems := (&Template{SchemaVersion: 1}).CompletenessProblems()
	codes := make(map[string]bool, len(problems))
	for _, problem := range problems {
		codes[problem.Code] = true
		if problem.Message == "" {
			t.Fatalf("problem %#v has no message", problem)
		}
	}
	for _, code := range []string{CodeEmptyGraph, CodeMissingStartNode, CodeMissingEndNode} {
		if !codes[code] {
			t.Fatalf("problems = %#v, want %q among them", problems, code)
		}
	}
}

// A graph that names both ends and holds them together is publishable.
func TestCompletenessProblemsOfACompleteGraph(t *testing.T) {
	graph := graphWithNodes(node("n_start", NodeStart, `{}`), node("n_work", NodeAgent, `{"agent_id":"a"}`), node("n_end", NodeEnd, `{}`))
	graph.Edges = []Edge{
		edge("e1", "n_start", "main", "n_work", "in"),
		edge("e2", "n_work", "main", "n_end", "in"),
	}
	if problems := graph.CompletenessProblems(); len(problems) != 0 {
		t.Fatalf("CompletenessProblems() = %#v, want none", problems)
	}
}
