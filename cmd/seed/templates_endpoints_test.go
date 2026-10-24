package main

import (
	"testing"

	"github.com/orlandoburli/ai-company/internal/workflow"
)

// The seeded templates are what a new tenant sees, so they show the new
// vocabulary: every graph says where the flow begins and where it ends.
func TestSeedTemplatesHaveExplicitStartAndEnd(t *testing.T) {
	templates, err := seedTemplates(testAgents())
	if err != nil {
		t.Fatalf("seed templates: %v", err)
	}
	for _, tpl := range templates {
		if err := tpl.Graph.Validate(); err != nil {
			t.Fatalf("template %q invalid: %v", tpl.Name, err)
		}
		var starts, ends int
		for _, node := range tpl.Graph.Nodes {
			switch node.Kind {
			case workflow.NodeStart:
				starts++
				if node.Type != "flow.start" || node.TypeVersion != 1 {
					t.Fatalf("template %q start node type = %q/%d, want flow.start/1", tpl.Name, node.Type, node.TypeVersion)
				}
			case workflow.NodeEnd:
				ends++
				if node.Type != "flow.end" || node.TypeVersion != 1 {
					t.Fatalf("template %q end node type = %q/%d, want flow.end/1", tpl.Name, node.Type, node.TypeVersion)
				}
			}
		}
		if starts != 1 || ends != 1 {
			t.Fatalf("template %q has %d start and %d end nodes, want 1 and 1", tpl.Name, starts, ends)
		}

		// The start comes first, the end comes last.
		if tpl.Graph.Nodes[0].Kind != workflow.NodeStart {
			t.Fatalf("template %q does not begin with its start node", tpl.Name)
		}
		if tpl.Graph.Nodes[len(tpl.Graph.Nodes)-1].Kind != workflow.NodeEnd {
			t.Fatalf("template %q does not end with its end node", tpl.Name)
		}

		// The start is the only entry of the flow, and the end is where the
		// flow finishes.
		entries := tpl.Graph.EntryNodes()
		start, ok := tpl.Graph.StartNode()
		if !ok {
			t.Fatalf("template %q has no start node", tpl.Name)
		}
		if len(entries) != 1 || entries[0] != start {
			t.Fatalf("template %q entry nodes = %v, want [%s]", tpl.Name, entries, start)
		}
		end, ok := tpl.Graph.EndNode()
		if !ok {
			t.Fatalf("template %q has no end node", tpl.Name)
		}
		if successors := tpl.Graph.Successors(end); len(successors) != 0 {
			t.Fatalf("template %q end node has successors %v", tpl.Name, successors)
		}
		if incoming := tpl.Graph.IncomingEdges(end); len(incoming) == 0 {
			t.Fatalf("template %q end node has no incoming edge", tpl.Name)
		}
		if outgoing := tpl.Graph.OutgoingEdges(start); len(outgoing) != 1 {
			t.Fatalf("template %q start node has %d outgoing edges, want 1", tpl.Name, len(outgoing))
		}
	}
}

// The seeded templates are the engine's fixtures: a graph that names its
// endpoints still has to run from the first node to the last one.
func TestSeedTemplateStartFeedsTheFirstNode(t *testing.T) {
	templates, err := seedTemplates(testAgents())
	if err != nil {
		t.Fatalf("seed templates: %v", err)
	}
	for _, tpl := range templates {
		start, _ := tpl.Graph.StartNode()
		end, _ := tpl.Graph.EndNode()
		successors := tpl.Graph.Successors(start)
		if len(successors) != 1 {
			t.Fatalf("template %q start node feeds %v", tpl.Name, successors)
		}
		first := successors[0]
		if first == end {
			t.Fatalf("template %q start node feeds the end node", tpl.Name)
		}
		// The flow reaches the end from the node before it.
		predecessors := tpl.Graph.Predecessors(end)
		if len(predecessors) == 0 {
			t.Fatalf("template %q end node has no predecessor", tpl.Name)
		}
		// The end is downstream of the start: the flow reaches it.
		found := false
		for _, id := range tpl.Graph.Descendants(start) {
			if id == end {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("template %q end node %q is not downstream of the start", tpl.Name, end)
		}
	}
}
