package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

func errCode(t *testing.T, res response) string {
	t.Helper()
	e, ok := res.json(t)["error"].(map[string]any)
	if !ok {
		return ""
	}
	c, _ := e["code"].(string)
	return c
}

func createAgent(t *testing.T, o ownerHarness, body map[string]any) map[string]any {
	t.Helper()
	res := o.do("POST", "/api/agents", body, o.ownerCookie)
	if res.status != http.StatusCreated {
		t.Fatalf("create agent: %d %s", res.status, res.body)
	}
	return res.json(t)["agent"].(map[string]any)
}

// agentBody is an agent without a transport: the transport lives on the
// runner, and the agent names a chain of runners.
func agentBody(name string) map[string]any {
	return map[string]any{
		"name":    name,
		"role":    "dev",
		"soul_md": "You are a test agent.",
	}
}

// cliAgentBody is the legacy shape: the transport fields are accepted and
// ignored, so an old client keeps working.
func cliAgentBody(name string) map[string]any {
	body := agentBody(name)
	body["kind"] = "cli"
	body["config"] = map[string]any{
		"bin":           "echo",
		"args":          []string{"hello"},
		"output_format": "text",
	}
	return body
}

func TestAgentRegistryValidationAndRoles(t *testing.T) {
	o := newOwnerHarness(t)
	tenantID := o.tenantID(o.ownerCookie)

	// Omitted concurrency defaults to 2 rather than failing validation.
	agent := createAgent(t, o, agentBody("dev"))
	if got := agent["concurrency_limit"].(float64); got != 2 {
		t.Fatalf("default concurrency = %v, want 2", got)
	}
	if agent["name"] != "dev" || agent["enabled"] != true {
		t.Fatalf("unexpected agent: %v", agent)
	}
	agentID := agent["id"].(string)

	// The transport moved to the runner registry: the agent carries a chain
	// of runners, not a kind and a config.
	if _, ok := agent["kind"]; ok {
		t.Errorf("the agent still carries a kind: %v", agent["kind"])
	}
	if _, ok := agent["config"]; ok {
		t.Errorf("the agent still carries a config: %v", agent["config"])
	}
	chain, ok := agent["runners"].([]any)
	if !ok || len(chain) != 0 {
		t.Fatalf("runners = %v, want an empty chain", agent["runners"])
	}

	// Duplicate names are a name conflict, not an in-use conflict.
	res := o.do("POST", "/api/agents", agentBody("dev"), o.ownerCookie)
	if res.status != http.StatusConflict || errCode(t, res) != "agent_name_taken" {
		t.Fatalf("duplicate agent: %d %s", res.status, res.body)
	}

	// The legacy transport fields are accepted and ignored, not rejected.
	res = o.do("POST", "/api/agents", cliAgentBody("legacy"), o.ownerCookie)
	if res.status != http.StatusCreated {
		t.Fatalf("legacy agent: %d %s", res.status, res.body)
	}
	legacy := res.json(t)["agent"].(map[string]any)
	if _, ok := legacy["kind"]; ok {
		t.Errorf("the legacy kind came back: %v", legacy["kind"])
	}
	if len(legacy["runners"].([]any)) != 0 {
		t.Errorf("the legacy agent has a chain: %v", legacy["runners"])
	}

	// Name must be mentionable.
	badName := agentBody("Not Valid")
	res = o.do("POST", "/api/agents", badName, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad name: %d %s", res.status, res.body)
	}

	// Patch: omitted fields are unchanged, empty soul clears.
	res = o.do("PATCH", "/api/agents/"+agentID, map[string]any{"soul_md": ""}, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("patch agent: %d %s", res.status, res.body)
	}
	patched := res.json(t)["agent"].(map[string]any)
	if patched["soul_md"] != "" || patched["concurrency_limit"].(float64) != 2 {
		t.Fatalf("patch semantics wrong: %v", patched)
	}

	// A member can read the registry but cannot manage it.
	o.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	memberCookie := o.loginTo("member@example.com", tenantID)
	res = o.do("GET", "/api/agents", nil, memberCookie)
	if res.status != http.StatusOK {
		t.Fatalf("member list agents: %d %s", res.status, res.body)
	}
	res = o.do("POST", "/api/agents", agentBody("member-agent"), memberCookie)
	if res.status != http.StatusForbidden {
		t.Fatalf("member create agent: %d %s, want 403", res.status, res.body)
	}

	// A guest can read but not write work.
	o.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)
	guestCookie := o.loginTo("guest@example.com", tenantID)
	res = o.do("GET", "/api/agents", nil, guestCookie)
	if res.status != http.StatusOK {
		t.Fatalf("guest list agents: %d %s", res.status, res.body)
	}
	res = o.do("POST", "/api/agents", agentBody("guest-agent"), guestCookie)
	if res.status != http.StatusForbidden {
		t.Fatalf("guest create agent: %d %s, want 403", res.status, res.body)
	}
}

// TestAgentRunnerChain checks that the agent CRUD carries the chain: the
// runner ids and names, in order.
func TestAgentRunnerChain(t *testing.T) {
	o := newOwnerHarness(t)

	first := createRunner(t, o, cliRunnerBody("dev-cli"))
	second := createRunner(t, o, cliRunnerBody("dev-fallback"))
	firstID := first["id"].(string)
	secondID := second["id"].(string)

	// The chain is given in order and comes back in order.
	agent := createAgent(t, o, map[string]any{
		"name":    "dev",
		"role":    "dev",
		"runners": []string{firstID, secondID},
	})
	chain, ok := agent["runners"].([]any)
	if !ok || len(chain) != 2 {
		t.Fatalf("runners = %v, want two", agent["runners"])
	}
	if chain[0].(map[string]any)["id"] != firstID || chain[1].(map[string]any)["id"] != secondID {
		t.Fatalf("the chain is out of order: %v", chain)
	}
	if chain[0].(map[string]any)["name"] != "dev-cli" {
		t.Fatalf("the chain does not carry the runner names: %v", chain)
	}

	agentID := agent["id"].(string)

	// The list carries the chain too.
	res := o.do("GET", "/api/agents", nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("list agents: %d %s", res.status, res.body)
	}
	agents := res.json(t)["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(agents))
	}
	if len(agents[0].(map[string]any)["runners"].([]any)) != 2 {
		t.Fatalf("the list does not carry the chain: %v", agents[0])
	}

	// A patch replaces the chain.
	res = o.do("PATCH", "/api/agents/"+agentID, map[string]any{"runners": []string{secondID}}, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("patch chain: %d %s", res.status, res.body)
	}
	chain = res.json(t)["agent"].(map[string]any)["runners"].([]any)
	if len(chain) != 1 || chain[0].(map[string]any)["id"] != secondID {
		t.Fatalf("the chain was not replaced: %v", chain)
	}

	// An empty chain clears it.
	res = o.do("PATCH", "/api/agents/"+agentID, map[string]any{"runners": []string{}}, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("clear chain: %d %s", res.status, res.body)
	}
	chain = res.json(t)["agent"].(map[string]any)["runners"].([]any)
	if len(chain) != 0 {
		t.Fatalf("the chain was not cleared: %v", chain)
	}

	// A runner that does not exist is refused, and nothing is written.
	res = o.do("PATCH", "/api/agents/"+agentID, map[string]any{"runners": []string{"NOPE"}}, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity || errCode(t, res) != "invalid_request" {
		t.Fatalf("unknown runner: %d %s", res.status, res.body)
	}
	res = o.do("GET", "/api/agents/"+agentID, nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("get agent: %d %s", res.status, res.body)
	}
	if len(res.json(t)["agent"].(map[string]any)["runners"].([]any)) != 0 {
		t.Fatal("a refused chain was written anyway")
	}

	// A runner of another tenant is not reachable either.
	other := o.register("other@example.com", goodPassword, "Globex", "globex")
	if other.status != http.StatusCreated {
		t.Fatalf("register the other tenant: %d %s", other.status, other.body)
	}
	foreign := createRunner(t, ownerHarness{harness: o.harness, ownerCookie: other.cookie}, cliRunnerBody("dev-cli"))
	res = o.do("PATCH", "/api/agents/"+agentID, map[string]any{
		"runners": []string{foreign["id"].(string)},
	}, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("cross-tenant runner: %d %s", res.status, res.body)
	}
}

func TestAgentChatMentionsJobsAndCancellation(t *testing.T) {
	o := newOwnerHarness(t)
	tenantID := o.tenantID(o.ownerCookie)
	_, projectID := o.createProductProject(o.ownerCookie)
	item := o.createItem(o.ownerCookie, projectID, "Chat target", nil)
	itemID := item["id"].(string)

	agent := createAgent(t, o, cliAgentBody("dev"))
	agentID := agent["id"].(string)

	// Get-or-create the item thread; a second call returns the same row.
	res := o.do("POST", "/api/items/"+itemID+"/thread", nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("item thread: %d %s", res.status, res.body)
	}
	thread := res.json(t)["thread"].(map[string]any)
	threadID := thread["id"].(string)
	if thread["context_kind"] != "item" || thread["item_id"] != itemID {
		t.Fatalf("unexpected thread: %v", thread)
	}
	res = o.do("POST", "/api/items/"+itemID+"/thread", nil, o.ownerCookie)
	if res.status != http.StatusOK || res.json(t)["thread"].(map[string]any)["id"] != threadID {
		t.Fatalf("thread get-or-create not idempotent: %d %s", res.status, res.body)
	}

	// Unknown mention fails and writes nothing.
	res = o.do("POST", "/api/threads/"+threadID+"/messages", map[string]any{"body_md": "@ghost look"}, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity || errCode(t, res) != "unknown_agent" {
		t.Fatalf("unknown mention: %d %s", res.status, res.body)
	}
	res = o.do("GET", "/api/threads/"+threadID+"/messages", nil, o.ownerCookie)
	if n := len(res.json(t)["messages"].([]any)); n != 0 {
		t.Fatalf("unknown mention wrote %d messages", n)
	}

	// A mentioned, enabled agent gets exactly one queued job.
	res = o.do("POST", "/api/threads/"+threadID+"/messages", map[string]any{"body_md": "@dev please look"}, o.ownerCookie)
	if res.status != http.StatusCreated {
		t.Fatalf("post message: %d %s", res.status, res.body)
	}
	created := res.json(t)
	jobs := created["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	job := jobs[0].(map[string]any)
	jobID := job["id"].(string)
	if job["state"] != "queued" || job["agent_id"] != agentID {
		t.Fatalf("unexpected job: %v", job)
	}

	// Disabled agents are refused without writing.
	res = o.do("PATCH", "/api/agents/"+agentID, map[string]any{"enabled": false}, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("disable agent: %d %s", res.status, res.body)
	}
	res = o.do("POST", "/api/threads/"+threadID+"/messages", map[string]any{"body_md": "@dev again"}, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity || errCode(t, res) != "agent_disabled" {
		t.Fatalf("disabled mention: %d %s", res.status, res.body)
	}
	res = o.do("PATCH", "/api/agents/"+agentID, map[string]any{"enabled": true}, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("re-enable agent: %d %s", res.status, res.body)
	}

	// The manual run button records instructions as a human message and queues
	// a job; without instructions it queues only the job.
	res = o.do("POST", "/api/threads/"+threadID+"/jobs", map[string]any{
		"agent_id": agentID, "instructions": "Summarise the item.",
	}, o.ownerCookie)
	if res.status != http.StatusAccepted {
		t.Fatalf("run agent: %d %s", res.status, res.body)
	}
	run := res.json(t)
	runJobID := run["job"].(map[string]any)["id"].(string)
	if run["message"] == nil {
		t.Fatal("instructions should be recorded as a message")
	}

	res = o.do("GET", "/api/threads/"+threadID+"/jobs", nil, o.ownerCookie)
	if res.status != http.StatusOK || len(res.json(t)["jobs"].([]any)) < 2 {
		t.Fatalf("thread jobs: %d %s", res.status, res.body)
	}

	// Logs endpoint is tenant-scoped and starts empty.
	res = o.do("GET", "/api/jobs/"+jobID+"/logs", nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("job logs: %d %s", res.status, res.body)
	}

	// Guests cannot post or run.
	tenantID = o.tenantID(o.ownerCookie)
	o.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)
	guestCookie := o.loginTo("guest@example.com", tenantID)
	res = o.do("POST", "/api/threads/"+threadID+"/messages", map[string]any{"body_md": "hi"}, guestCookie)
	if res.status != http.StatusForbidden {
		t.Fatalf("guest post: %d %s", res.status, res.body)
	}
	res = o.do("POST", "/api/threads/"+threadID+"/jobs", map[string]any{"agent_id": agentID}, guestCookie)
	if res.status != http.StatusForbidden {
		t.Fatalf("guest run: %d %s", res.status, res.body)
	}

	// Cancel a queued job, then refuse a second cancel.
	res = o.do("POST", "/api/jobs/"+jobID+"/cancel", nil, o.ownerCookie)
	if res.status != http.StatusAccepted || res.json(t)["job"].(map[string]any)["state"] != "cancelled" {
		t.Fatalf("cancel job: %d %s", res.status, res.body)
	}
	res = o.do("POST", "/api/jobs/"+jobID+"/cancel", nil, o.ownerCookie)
	if res.status != http.StatusConflict || errCode(t, res) != "job_terminal" {
		t.Fatalf("second cancel: %d %s", res.status, res.body)
	}

	// SSE replays the terminal state and closes with `done`.
	res = o.do("GET", "/api/jobs/"+jobID+"/events?after_seq=0", nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("sse: %d %s", res.status, res.body)
	}
	body := string(res.body)
	if !strings.Contains(body, "event: state") || !strings.Contains(body, "event: done") {
		t.Fatalf("sse frames missing: %q", body)
	}
	if !strings.Contains(body, "cancelled") {
		t.Fatalf("sse state should carry the cancelled job: %q", body)
	}

	// A second tenant cannot see the thread or job.
	other := newOwnerHarness(t)
	res = other.do("GET", "/api/threads/"+threadID+"/messages", nil, other.ownerCookie)
	if res.status != http.StatusNotFound {
		t.Fatalf("cross-tenant thread read: %d %s", res.status, res.body)
	}
	res = other.do("GET", "/api/jobs/"+runJobID, nil, other.ownerCookie)
	if res.status != http.StatusNotFound {
		t.Fatalf("cross-tenant job read: %d %s", res.status, res.body)
	}
}
