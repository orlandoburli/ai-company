package workflow

import (
	"errors"
	"reflect"
	"testing"
)

func TestExampleTopology(t *testing.T) {
	graph := mustTemplate(t, exampleTemplate)
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"entries", graph.EntryNodes(), []string{"n_pm"}},
		{"code descendants", graph.Descendants("n_code"), []string{"n_ci", "n_join", "n_pr", "n_review"}},
		{"join ancestors", graph.Ancestors("n_join"), []string{"n_ci", "n_code", "n_gate", "n_pm", "n_review"}},
		{"successors deduplicated", graph.Successors("n_code"), []string{"n_ci", "n_review"}},
		{"predecessors deduplicated", graph.Predecessors("n_join"), []string{"n_ci", "n_review"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
	if node, ok := graph.Node("n_join"); !ok || node.Kind != NodeMerge {
		t.Fatalf("Node(n_join) = (%+v, %t), want merge node", node, ok)
	}
	if got := len(graph.OutgoingEdges("n_code")); got != 2 {
		t.Fatalf("OutgoingEdges(n_code) = %d, want 2", got)
	}
}

func TestFindCyclesAndCycleBudget(t *testing.T) {
	tests := []struct {
		name       string
		graph      *Template
		wantCycles [][]string
		budgetOK   bool
	}{
		{
			name: "cycle without loop",
			graph: graphWithNodes(
				node("a", NodeAgent, `{"agent_id":"a"}`),
				node("b", NodeAgent, `{"agent_id":"b"}`),
			),
			wantCycles: [][]string{{"a", "b", "a"}},
			budgetOK:   false,
		},
		{
			name: "cycle through bounded loop",
			graph: graphWithNodes(
				node("a", NodeAgent, `{"agent_id":"a"}`),
				node("loop", NodeLoop, `{"max_iterations":4,"until":"done"}`),
			),
			wantCycles: [][]string{{"a", "loop", "a"}},
			budgetOK:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.graph.Edges = []Edge{edge("ab", "a", "main", "b", "in"), edge("ba", "b", "main", "a", "in")}
			if tt.name == "cycle through bounded loop" {
				tt.graph.Edges = []Edge{edge("al", "a", "main", "loop", "in"), edge("la", "loop", "body", "a", "in")}
			}
			got := tt.graph.FindCycles()
			if !reflect.DeepEqual(got, tt.wantCycles) {
				t.Fatalf("FindCycles() = %v, want %v", got, tt.wantCycles)
			}
			ok, reason := tt.graph.CycleBudgetOK(got[0])
			if ok != tt.budgetOK {
				t.Fatalf("CycleBudgetOK() = (%t, %q), want %t", ok, reason, tt.budgetOK)
			}
		})
	}
}

func TestLoopCycleReturnEntries(t *testing.T) {
	makeLoopGraph := func(withBudget, withStarter bool) *Template {
		loopConfig := `{"until":true}`
		if withBudget {
			loopConfig = `{"max_iterations":3,"until":true}`
		}
		nodes := []Node{node("loop", NodeLoop, loopConfig), node("body", NodeAgent, `{"agent_id":"worker"}`)}
		graph := graphWithNodes(nodes...)
		graph.Edges = []Edge{
			edge("body-path", "loop", "body", "body", "in"),
			edge("cycle-return", "body", "main", "loop", "in"),
		}
		if withStarter {
			graph.Nodes = append(graph.Nodes, node("starter", NodeAgent, `{"agent_id":"starter"}`))
			graph.Edges = append(graph.Edges, edge("start", "starter", "main", "loop", "in"))
		}
		return graph
	}

	t.Run("loop is the only entry and body is reachable", func(t *testing.T) {
		graph := makeLoopGraph(true, false)
		if err := graph.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if got := graph.EntryNodes(); !reflect.DeepEqual(got, []string{"loop"}) {
			t.Fatalf("EntryNodes() = %v, want [loop]", got)
		}
		if got := graph.Descendants("loop"); !reflect.DeepEqual(got, []string{"body"}) {
			t.Fatalf("Descendants(loop) = %v, want [body]", got)
		}
		cycles := graph.FindCycles()
		if !reflect.DeepEqual(cycles, [][]string{{"body", "loop", "body"}}) {
			t.Fatalf("FindCycles() = %v, want loop cycle", cycles)
		}
	})

	t.Run("cycle without a budget gets cycle error", func(t *testing.T) {
		graph := makeLoopGraph(false, false)
		err := graph.Validate()
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Code != CodeCycleWithoutBudget {
			t.Fatalf("Validate() error = %v, want code %q", err, CodeCycleWithoutBudget)
		}
	})

	t.Run("starter remains the entry", func(t *testing.T) {
		graph := makeLoopGraph(true, true)
		if err := graph.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
		if got := graph.EntryNodes(); !reflect.DeepEqual(got, []string{"starter"}) {
			t.Fatalf("EntryNodes() = %v, want [starter]", got)
		}
		var cycleEdge, ordinaryEdge Edge
		for _, edge := range graph.Edges {
			if edge.ID == "cycle-return" {
				cycleEdge = edge
			}
			if edge.ID == "start" {
				ordinaryEdge = edge
			}
		}
		if !graph.IsCycleReturnEdge(cycleEdge) {
			t.Fatal("IsCycleReturnEdge(cycle-return) = false, want true")
		}
		if graph.IsCycleReturnEdge(ordinaryEdge) {
			t.Fatal("IsCycleReturnEdge(start) = true, want false")
		}
		if got := graph.CycleReturnEdges("loop"); !reflect.DeepEqual(got, []Edge{cycleEdge}) {
			t.Fatalf("CycleReturnEdges(loop) = %v, want [%v]", got, cycleEdge)
		}
	})
}
