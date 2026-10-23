package main

import (
	"encoding/json"
	"fmt"

	"github.com/orlandoburli/ai-company/internal/workflow"
)

// Seeded workflow templates.
//
// These are the graphs the development tenant starts with. They are pure
// builders: they take the agent ids of the tenant (so a template always points
// at real agents) and return a validated graph. Publishing them into
// `workflow_templates` is the seed's job; the shapes here follow
// `docs/mvp-design.md` §4 "Seeded templates" and §5.
//
// Three shapes are covered on purpose, because they are the ones the engine
// must get right:
//
//   - Bugfix: a diamond (two branches under one implementation) joined by a
//     `merge` node, with two human checkpoints around it.
//   - Review: the smallest useful graph — one agent, one checkpoint.
//   - Repair: a bounded cycle. The loop's body runs an agent, the body edge
//     returns to the loop's `in` port, and `max_iterations` is the only
//     backstop (a timeout would be the wrong one: a body can park behind a
//     human for days).
type seedTemplate struct {
	Name           string
	AppliesTo      string
	MaxConcurrency int
	Graph          workflow.Template
}

// seedTemplates builds every seeded template for the given tenant. The
// agents map is keyed by agent name ("pm", "dev", "reviewer", "qa").
func seedTemplates(agents map[string]string) ([]seedTemplate, error) {
	builders := []func(map[string]string) (seedTemplate, error){
		bugfixTemplate,
		bugfixLocalTemplate,
		bugfixGitHubTemplate,
		reviewTemplate,
		repairTemplate,
	}
	out := make([]seedTemplate, 0, len(builders))
	for _, build := range builders {
		tpl, err := build(agents)
		if err != nil {
			return nil, err
		}
		if err := tpl.Graph.Validate(); err != nil {
			return nil, fmt.Errorf("seed template %q is invalid: %w", tpl.Name, err)
		}
		out = append(out, tpl)
	}
	return out, nil
}

// bugfixTemplate: pm.spec → checkpoint → dev.implement → (review ∥ qa) → merge
// → checkpoint → close. The two branches under `implement` are what the merge
// node reconciles; they run concurrently.
func bugfixTemplate(agents map[string]string) (seedTemplate, error) {
	spec, err := agentNode("n_pm", "spec", "agent.pm", 2, agents["pm"],
		"Write a specification for the item: what to change, how to verify it, and the risks.",
		map[string]string{"spec_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}
	gate, err := checkpointNode("n_gate", "approve spec", "checkpoint.approval", 1,
		map[string]any{"policy": "retry_step", "max_cycles": 2})
	if err != nil {
		return seedTemplate{}, err
	}
	impl, err := agentNode("n_code", "implement", "agent.coder", 3, agents["dev"],
		"Implement the approved specification. Keep the change small and reversible; do not commit.",
		map[string]string{"summary_md": "string", "changed_files": "array"})
	if err != nil {
		return seedTemplate{}, err
	}
	// The node that touches code gets its own worktree, cut from the app-owned
	// mirror — never from the operator's checkout.
	impl.Workspace = workflow.WorkspaceWorktree
	review, err := agentNode("n_review", "review", "agent.reviewer", 1, agents["reviewer"],
		"Review the implementation for correctness and scope. List findings; do not change code.",
		map[string]string{"findings_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}
	qa, err := agentNode("n_qa", "check", "agent.qa", 1, agents["qa"],
		"Check the implementation against the specification and list what still fails.",
		map[string]string{"report_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}
	join, err := mergeNode("n_join", "reconcile", "merge.all", 1, "all")
	if err != nil {
		return seedTemplate{}, err
	}
	gate2, err := checkpointNode("n_gate2", "accept result", "checkpoint.approval", 1,
		map[string]any{"policy": "retry_step", "max_cycles": 1})
	if err != nil {
		return seedTemplate{}, err
	}
	close, err := agentNode("n_close", "summarise", "agent.pm", 2, agents["pm"],
		"Summarise the result for the item: what changed, what is verified, what a human must do next.",
		map[string]string{"summary_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}

	return seedTemplate{
		Name: "Bugfix",
		// Not the tenant's default: the designed flow needs the pm agent (an
		// API agent), and the dispatcher picks a template by the item's kind.
		AppliesTo:      "epic",
		MaxConcurrency: 4,
		Graph: workflow.Template{
			SchemaVersion: 1,
			Nodes: []workflow.Node{
				spec, gate, impl, review, qa, join, gate2, close,
			},
			Edges: []workflow.Edge{
				edge("e1", "n_pm", "main", "n_gate", "in"),
				edge("e2", "n_gate", "approved", "n_code", "in"),
				edge("e3", "n_code", "main", "n_review", "in"),
				edge("e4", "n_code", "main", "n_qa", "in"),
				edge("e5", "n_review", "main", "n_join", "in"),
				edge("e6", "n_qa", "main", "n_join", "in"),
				edge("e7", "n_join", "main", "n_gate2", "in"),
				edge("e8", "n_gate2", "approved", "n_close", "in"),
			},
		},
	}, nil
}

// bugfixGitHubTemplate is the designed Bugfix flow against a real repository:
// the implementation runs in a worktree, the server opens a pull request, the
// CI is watched, a reviewer looks at the diff, and a human decides whether the
// pull request is merged.
//
// The integration nodes carry the repository in their config; the branch comes
// from the execution (the worktree the implementation ran in) and the title
// and body may use the {{item.title}} placeholder.
func bugfixGitHubTemplate(agents map[string]string) (seedTemplate, error) {
	spec, err := agentNode("n_pm", "spec", "agent.pm", 2, agents["dev"],
		"Read the item and write what has to change, in two or three sentences, and say which file it touches. "+
			"Do not write code yet, and do not ask a question — a human approves the result.",
		map[string]string{"spec_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}
	gate, err := checkpointNode("n_gate", "approve spec", "checkpoint.approval", 1,
		map[string]any{"policy": "retry_step", "max_cycles": 2})
	if err != nil {
		return seedTemplate{}, err
	}
	impl, err := agentNode("n_code", "implement", "agent.coder", 3, agents["dev"],
		"Implement the approved specification. It is in your context, as the previous step's output. "+
			"You are working in a worktree cut for this step and you have permission to edit the files in it: "+
			"do not ask for permission, do not commit, do not push — the server commits and opens the pull request. "+
			"Keep the change small and add a test for it.",
		map[string]string{"summary_md": "string", "changed_files": "array"})
	if err != nil {
		return seedTemplate{}, err
	}
	impl.Workspace = workflow.WorkspaceWorktree
	pr, err := integrationNode("n_pr", "open pull request", "github.open_pr", 1, map[string]any{
		"repo":  "orlandoburli/fazgiro",
		"base":  "main",
		"title": "{{item.title}}",
		"body":  "Opened by AI Company for item {{item.id}}.\n\n{{item.description}}",
	})
	if err != nil {
		return seedTemplate{}, err
	}
	ci, err := integrationNode("n_ci", "wait for ci", "github.wait_ci", 1, map[string]any{
		"repo":      "orlandoburli/fazgiro",
		"timeout_s": 900,
	})
	if err != nil {
		return seedTemplate{}, err
	}
	review, err := agentNode("n_review", "review the diff", "agent.reviewer", 1, agents["reviewer"],
		"Review the change that was just made in the pull request. List what is wrong with it; do not change anything.",
		map[string]string{"findings_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}
	join, err := mergeNode("n_join", "reconcile", "merge.all", 1, "all")
	if err != nil {
		return seedTemplate{}, err
	}
	gate2, err := checkpointNode("n_gate2", "accept the pull request", "checkpoint.approval", 1,
		map[string]any{"policy": "abort"})
	if err != nil {
		return seedTemplate{}, err
	}
	closeNode, err := agentNode("n_close", "summarise", "agent.pm", 2, agents["dev"],
		"Summarise for the item: what changed, what the checks said, and what a human must do next (the pull request is not merged by the system).",
		map[string]string{"summary_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}
	// The designed flow ends with the item closed: the work is done, the
	// checks passed and a human approved the result.
	closeItem, err := integrationNode("n_done", "close the item", "item.mark_done", 1, map[string]any{})
	if err != nil {
		return seedTemplate{}, err
	}

	return seedTemplate{
		Name:           "Bugfix (GitHub)",
		AppliesTo:      "",
		MaxConcurrency: 4,
		Graph: workflow.Template{
			SchemaVersion: 1,
			Nodes: []workflow.Node{
				spec, gate, impl, pr, ci, review, join, gate2, closeNode, closeItem,
			},
			Edges: []workflow.Edge{
				edge("e1", "n_pm", "main", "n_gate", "in"),
				edge("e2", "n_gate", "approved", "n_code", "in"),
				edge("e3", "n_code", "main", "n_pr", "in"),
				edge("e4", "n_pr", "main", "n_ci", "in"),
				edge("e5", "n_pr", "main", "n_review", "in"),
				edge("e6", "n_ci", "main", "n_join", "in"),
				edge("e7", "n_review", "main", "n_join", "in"),
				edge("e8", "n_join", "main", "n_gate2", "in"),
				edge("e9", "n_gate2", "approved", "n_close", "in"),
				edge("e10", "n_close", "main", "n_done", "in"),
			},
		},
	}, nil
}

// bugfixLocalTemplate is the Bugfix graph with CLI agents only.
//
// The designed Bugfix starts with the pm agent, which is an API agent: it
// needs OPENAI_API_KEY, and without it the very first node fails. This
// variant runs the same shape — a diamond under one implementation, joined by
// a merge node, with two checkpoints — on the CLI agents, so the whole
// engine path (two concurrent branches, a join, two human decisions) is
// demonstrable on a machine with no API key.
func bugfixLocalTemplate(agents map[string]string) (seedTemplate, error) {
	tpl, err := bugfixTemplate(agents)
	if err != nil {
		return seedTemplate{}, err
	}
	tpl.Name = "Bugfix (local)"
	// The CLI-only variant is the one for a bug: the designed Bugfix needs
	// the pm agent, which is an API agent.
	tpl.AppliesTo = "bug"
	for i := range tpl.Graph.Nodes {
		node := &tpl.Graph.Nodes[i]
		if node.ID != "n_pm" && node.ID != "n_close" {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			return seedTemplate{}, err
		}
		cfg["agent_id"] = agents["dev"]
		raw, err := json.Marshal(cfg)
		if err != nil {
			return seedTemplate{}, err
		}
		node.Config = raw
	}
	return tpl, nil
}

// reviewTemplate: one agent, one checkpoint. The smallest graph that still
// exercises the human-in-the-loop path.
func reviewTemplate(agents map[string]string) (seedTemplate, error) {
	review, err := agentNode("n_review", "review", "agent.reviewer", 1, agents["reviewer"],
		"Review the item's work and report findings. Do not change anything.",
		map[string]string{"findings_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}
	gate, err := checkpointNode("n_gate", "accept findings", "checkpoint.approval", 1,
		map[string]any{"policy": "abort"})
	if err != nil {
		return seedTemplate{}, err
	}
	return seedTemplate{
		Name: "Review",
		// A manual template: the kind is its own, so the dispatcher never
		// starts it on its own.
		AppliesTo:      "review",
		MaxConcurrency: 2,
		Graph: workflow.Template{
			SchemaVersion: 1,
			Nodes:         []workflow.Node{review, gate},
			Edges: []workflow.Edge{
				edge("e1", "n_review", "main", "n_gate", "in"),
			},
		},
	}, nil
}

// repairTemplate: a bounded cycle. The loop's body runs one agent; the body
// edge returns to the loop's `in` port, which is the cycle; `done` leaves the
// loop when the condition holds, and `max_iterations` is the hard stop.
func repairTemplate(agents map[string]string) (seedTemplate, error) {
	fix, err := agentNode("n_fix", "fix", "agent.coder", 3, agents["dev"],
		"Fix the failing checks. Change as little as possible and explain what you did.",
		map[string]string{"summary_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}
	fix.Workspace = workflow.WorkspaceWorktree
	check, err := agentNode("n_check", "check", "agent.qa", 1, agents["qa"],
		"Run the checks and report whether they pass.",
		map[string]string{"report_md": "string", "passed": "boolean"})
	if err != nil {
		return seedTemplate{}, err
	}
	loop, err := loopNode("n_loop", "repair until green", "loop.repair", 1,
		map[string]any{"max_iterations": 3, "until": "checks_passed"})
	if err != nil {
		return seedTemplate{}, err
	}
	done, err := agentNode("n_close", "report", "agent.pm", 2, agents["dev"],
		"Report the final state: what was fixed, what was verified, what is still open.",
		map[string]string{"summary_md": "string"})
	if err != nil {
		return seedTemplate{}, err
	}
	return seedTemplate{
		Name: "Repair",
		// A manual template, like Review: the kind is its own.
		AppliesTo:      "repair",
		MaxConcurrency: 2,
		Graph: workflow.Template{
			SchemaVersion: 1,
			Nodes:         []workflow.Node{loop, fix, check, done},
			Edges: []workflow.Edge{
				edge("e1", "n_loop", "body", "n_fix", "in"),
				edge("e2", "n_fix", "main", "n_check", "in"),
				edge("e3", "n_check", "main", "n_loop", "in"),
				edge("e4", "n_loop", "done", "n_close", "in"),
			},
		},
	}, nil
}

// agentNode builds an `agent` node with a rendered config object.
func agentNode(id, name, nodeType string, typeVersion int, agentID, prompt string, outputs map[string]string) (workflow.Node, error) {
	if agentID == "" {
		return workflow.Node{}, fmt.Errorf("seed template node %q has no agent id", id)
	}
	config := map[string]any{
		"agent_id":    agentID,
		"prompt_tmpl": prompt,
	}
	if len(outputs) > 0 {
		config["outputs"] = outputs
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return workflow.Node{}, err
	}
	return workflow.Node{
		ID:          id,
		Kind:        workflow.NodeAgent,
		Name:        name,
		Type:        nodeType,
		TypeVersion: typeVersion,
		Config:      raw,
		OnError:     workflow.OnErrorStop,
		Workspace:   workflow.WorkspaceScratch,
	}, nil
}

// checkpointNode builds a `checkpoint` node. The policy decides what a
// rejection does: retry the step, abort the run, or jump to another node.
func checkpointNode(id, name, nodeType string, typeVersion int, config map[string]any) (workflow.Node, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return workflow.Node{}, err
	}
	return workflow.Node{
		ID:          id,
		Kind:        workflow.NodeCheckpoint,
		Name:        name,
		Type:        nodeType,
		TypeVersion: typeVersion,
		Config:      raw,
		OnError:     workflow.OnErrorStop,
		Workspace:   workflow.WorkspaceScratch,
	}, nil
}

// mergeNode builds a `merge` node: the first-class join that reconciles
// branches. `all` waits for every receipt, `any` takes the first, `n_of_m`
// takes n.
func mergeNode(id, name, nodeType string, typeVersion int, mode string) (workflow.Node, error) {
	raw, err := json.Marshal(map[string]any{"mode": mode})
	if err != nil {
		return workflow.Node{}, err
	}
	return workflow.Node{
		ID:          id,
		Kind:        workflow.NodeMerge,
		Name:        name,
		Type:        nodeType,
		TypeVersion: typeVersion,
		Config:      raw,
		OnError:     workflow.OnErrorStop,
		// A merge reconciles branches, so it lives in the run's integration
		// workspace rather than a throwaway one.
		Workspace: workflow.WorkspaceWorktree,
	}, nil
}

// integrationNode builds an `integration` node: a deterministic, idempotent
// action of the platform (github.open_pr, github.wait_ci, …). The type
// version pins the implementation.
func integrationNode(id, name, nodeType string, typeVersion int, config map[string]any) (workflow.Node, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return workflow.Node{}, err
	}
	return workflow.Node{
		ID:          id,
		Kind:        workflow.NodeIntegration,
		Name:        name,
		Type:        nodeType,
		TypeVersion: typeVersion,
		Config:      raw,
		OnError:     workflow.OnErrorStop,
		Workspace:   workflow.WorkspaceScratch,
	}, nil
}

// loopNode builds a `loop` node. `max_iterations` is mandatory: a timeout is
// the wrong backstop for a body that can wait on a human for days.
func loopNode(id, name, nodeType string, typeVersion int, config map[string]any) (workflow.Node, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return workflow.Node{}, err
	}
	return workflow.Node{
		ID:          id,
		Kind:        workflow.NodeLoop,
		Name:        name,
		Type:        nodeType,
		TypeVersion: typeVersion,
		Config:      raw,
		OnError:     workflow.OnErrorStop,
		Workspace:   workflow.WorkspaceScratch,
	}, nil
}

// edge builds one edge between two named ports.
func edge(id, fromNode, fromPort, toNode, toPort string) workflow.Edge {
	return workflow.Edge{
		ID:   id,
		From: workflow.PortRef{Node: fromNode, Port: fromPort},
		To:   workflow.PortRef{Node: toNode, Port: toPort},
	}
}
