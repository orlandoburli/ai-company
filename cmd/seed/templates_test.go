package main

import (
	"encoding/json"
	"testing"

	"github.com/orlandoburli/ai-company/internal/workflow"
)

// The seeded templates are the graphs the development tenant starts with, and
// they are the engine's fixtures: a diamond with a merge, a checkpoint-only
// graph and a bounded cycle. If one of them stops validating, the seed would
// publish a broken graph, so they are checked here.

func testAgents() map[string]string {
	return map[string]string{
		"pm":       "agent-pm",
		"dev":      "agent-dev",
		"reviewer": "agent-reviewer",
		"qa":       "agent-qa",
	}
}

func TestSeedTemplatesAreValid(t *testing.T) {
	templates, err := seedTemplates(testAgents())
	if err != nil {
		t.Fatalf("seed templates: %v", err)
	}
	if len(templates) != 5 {
		t.Fatalf("templates = %d, want 5", len(templates))
	}
	for _, tpl := range templates {
		if err := tpl.Graph.Validate(); err != nil {
			t.Fatalf("template %q invalid: %v", tpl.Name, err)
		}
		if tpl.MaxConcurrency < 1 {
			t.Fatalf("template %q has max concurrency %d", tpl.Name, tpl.MaxConcurrency)
		}
	}
}

func TestSeedTemplatesRoundTripThroughJSON(t *testing.T) {
	// A template is stored as graph_json, so what the seed builds must survive
	// the JSON round trip the database does.
	templates, err := seedTemplates(testAgents())
	if err != nil {
		t.Fatalf("seed templates: %v", err)
	}
	for _, tpl := range templates {
		raw, err := json.Marshal(tpl.Graph)
		if err != nil {
			t.Fatalf("marshal %q: %v", tpl.Name, err)
		}
		parsed, err := workflow.ParseTemplate(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tpl.Name, err)
		}
		if err := parsed.Validate(); err != nil {
			t.Fatalf("reparsed %q invalid: %v", tpl.Name, err)
		}
		if len(parsed.Nodes) != len(tpl.Graph.Nodes) || len(parsed.Edges) != len(tpl.Graph.Edges) {
			t.Fatalf("round trip changed %q: %d/%d nodes, %d/%d edges",
				tpl.Name, len(parsed.Nodes), len(tpl.Graph.Nodes), len(parsed.Edges), len(tpl.Graph.Edges))
		}
	}
}

func TestBugfixTemplateShape(t *testing.T) {
	tpl, err := bugfixTemplate(testAgents())
	if err != nil {
		t.Fatalf("bugfix template: %v", err)
	}
	if tpl.Name != "Bugfix" || tpl.AppliesTo != "epic" {
		t.Fatalf("unexpected template identity: %v / %q", tpl.Name, tpl.AppliesTo)
	}

	var checkpoints, merges int
	for _, node := range tpl.Graph.Nodes {
		switch node.Kind {
		case workflow.NodeCheckpoint:
			checkpoints++
		case workflow.NodeMerge:
			merges++
		}
	}
	if checkpoints != 2 {
		t.Fatalf("checkpoints = %d, want 2", checkpoints)
	}
	if merges != 1 {
		t.Fatalf("merge nodes = %d, want 1", merges)
	}

	entries := tpl.Graph.EntryNodes()
	if len(entries) != 1 || entries[0] != "n_pm" {
		t.Fatalf("entry nodes = %v, want [n_pm]", entries)
	}

	// The two branches under the implementation must be siblings, so the merge
	// node has two incoming edges.
	join, ok := tpl.Graph.Node("n_join")
	if !ok {
		t.Fatal("merge node n_join missing")
	}
	if join.Workspace != workflow.WorkspaceWorktree {
		t.Fatalf("merge workspace = %q, want worktree", join.Workspace)
	}
	if incoming := tpl.Graph.IncomingEdges("n_join"); len(incoming) != 2 {
		t.Fatalf("merge incoming edges = %d, want 2", len(incoming))
	}
	if descendants := tpl.Graph.Descendants("n_code"); len(descendants) != 5 {
		// n_review, n_qa, n_join, n_gate2, n_close
		t.Fatalf("descendants of n_code = %v", descendants)
	}
}

func TestRepairTemplateHasBoundedCycle(t *testing.T) {
	tpl, err := repairTemplate(testAgents())
	if err != nil {
		t.Fatalf("repair template: %v", err)
	}
	loop, ok := tpl.Graph.Node("n_loop")
	if !ok {
		t.Fatal("loop node missing")
	}
	if loop.Kind != workflow.NodeLoop {
		t.Fatalf("n_loop kind = %q", loop.Kind)
	}
	var cfg map[string]any
	if err := json.Unmarshal(loop.Config, &cfg); err != nil {
		t.Fatalf("loop config: %v", err)
	}
	if limit, _ := cfg["max_iterations"].(float64); limit != 3 {
		t.Fatalf("max_iterations = %v, want 3", cfg["max_iterations"])
	}
	if _, ok := cfg["until"]; !ok {
		t.Fatal("loop has no until condition")
	}

	// The cycle must exist and must pass through the loop node, otherwise the
	// graph would be rejected as an unbudgeted cycle.
	cycles := tpl.Graph.FindCycles()
	if len(cycles) == 0 {
		t.Fatal("repair template has no cycle")
	}
	for _, cycle := range cycles {
		if ok, reason := tpl.Graph.CycleBudgetOK(cycle); !ok {
			t.Fatalf("cycle %v has no iteration budget: %s", cycle, reason)
		}
	}
}

// The local variant exists so the whole engine path (diamond, merge, two
// checkpoints) is demonstrable without an API key: its first node is the CLI
// agent, not the API one.
func TestBugfixLocalTemplateUsesCLIAgents(t *testing.T) {
	tpl, err := bugfixLocalTemplate(testAgents())
	if err != nil {
		t.Fatalf("bugfix local template: %v", err)
	}
	if tpl.Name != "Bugfix (local)" {
		t.Fatalf("name = %q", tpl.Name)
	}
	if err := tpl.Graph.Validate(); err != nil {
		t.Fatalf("template invalid: %v", err)
	}
	for _, node := range tpl.Graph.Nodes {
		if node.ID != "n_pm" && node.ID != "n_close" {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			t.Fatalf("config: %v", err)
		}
		if cfg["agent_id"] != testAgents()["dev"] {
			t.Fatalf("node %s uses agent %v, want the CLI agent", node.ID, cfg["agent_id"])
		}
	}
}

// The GitHub variant is the designed flow: the implementation runs in a
// worktree, the server opens a pull request, the CI is watched, a reviewer
// looks at the diff, and a human decides.
func TestBugfixGitHubTemplateShape(t *testing.T) {
	tpl, err := bugfixGitHubTemplate(testAgents())
	if err != nil {
		t.Fatalf("bugfix github template: %v", err)
	}
	if tpl.Name != "Bugfix (GitHub)" {
		t.Fatalf("name = %q", tpl.Name)
	}
	if err := tpl.Graph.Validate(); err != nil {
		t.Fatalf("template invalid: %v", err)
	}
	var integrations, checkpoints, merges int
	for _, node := range tpl.Graph.Nodes {
		switch node.Kind {
		case workflow.NodeIntegration:
			integrations++
		case workflow.NodeCheckpoint:
			checkpoints++
		case workflow.NodeMerge:
			merges++
		}
	}
	// Three integration nodes: open the pull request, watch the checks, and
	// close the item.
	if integrations != 3 || checkpoints != 2 || merges != 1 {
		t.Fatalf("integrations=%d checkpoints=%d merges=%d, want 3/2/1", integrations, checkpoints, merges)
	}
	done, ok := tpl.Graph.Node("n_done")
	if !ok {
		t.Fatal("n_done missing")
	}
	if done.Kind != workflow.NodeIntegration || done.Type != "item.mark_done" {
		t.Fatalf("n_done = %s/%s, want an item.mark_done integration", done.Kind, done.Type)
	}
	impl, ok := tpl.Graph.Node("n_code")
	if !ok {
		t.Fatal("n_code missing")
	}
	if impl.Workspace != workflow.WorkspaceWorktree {
		t.Fatalf("the implementation must run in a worktree, got %q", impl.Workspace)
	}
}

// The dispatcher picks a template by the item's kind, and a tenant has exactly
// one default (the one with an empty kind) — two defaults would make the
// choice arbitrary.
func TestSeedTemplatesHaveExactlyOneDefault(t *testing.T) {
	templates, err := seedTemplates(testAgents())
	if err != nil {
		t.Fatalf("seed templates: %v", err)
	}
	defaults := 0
	kinds := map[string]bool{}
	for _, tpl := range templates {
		if tpl.AppliesTo == "" {
			defaults++
			continue
		}
		if kinds[tpl.AppliesTo] {
			t.Fatalf("two templates claim the kind %q", tpl.AppliesTo)
		}
		kinds[tpl.AppliesTo] = true
	}
	if defaults != 1 {
		t.Fatalf("defaults = %d, want exactly 1", defaults)
	}
}

func TestSeedTemplatesNeedAgentIDs(t *testing.T) {
	// A template pointing at a missing agent is refused at build time, not at
	// run time.
	if _, err := bugfixTemplate(map[string]string{"pm": "agent-pm"}); err == nil {
		t.Fatal("expected an error for a missing agent id")
	}
}
