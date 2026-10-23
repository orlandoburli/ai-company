package api_test

import (
	"net/http"
	"testing"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// cliRunnerBody is a cli runner: a binary to run and how to run it.
func cliRunnerBody(name string) map[string]any {
	return map[string]any{
		"name": name,
		"kind": "cli",
		"config": map[string]any{
			"bin":           "echo",
			"args":          []string{"hello"},
			"output_format": "text",
		},
	}
}

func apiRunnerBody(name string) map[string]any {
	return map[string]any{
		"name":           name,
		"kind":           "api",
		"credential_ref": "keychain:openai",
		"config": map[string]any{
			"provider":   "openai",
			"model":      "gpt-4o-mini",
			"key_env":    "OPENAI_API_KEY",
			"max_tokens": 1024,
		},
	}
}

func createRunner(t *testing.T, o ownerHarness, body map[string]any) map[string]any {
	t.Helper()
	res := o.do("POST", "/api/runners", body, o.ownerCookie)
	if res.status != http.StatusCreated {
		t.Fatalf("create runner: %d %s", res.status, res.body)
	}
	return res.json(t)["runner"].(map[string]any)
}

func TestRunnerRegistryCRUD(t *testing.T) {
	o := newOwnerHarness(t)

	runner := createRunner(t, o, cliRunnerBody("dev-cli"))
	runnerID := runner["id"].(string)
	if runner["name"] != "dev-cli" || runner["kind"] != "cli" || runner["enabled"] != true {
		t.Fatalf("unexpected runner: %v", runner)
	}
	if runner["credential_ref"] != "" {
		t.Errorf("credential_ref = %v, want empty", runner["credential_ref"])
	}
	cfg, ok := runner["config"].(map[string]any)
	if !ok || cfg["bin"] != "echo" {
		t.Fatalf("the runner did not carry its config: %v", runner)
	}

	res := o.do("GET", "/api/runners/"+runnerID, nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("get runner: %d %s", res.status, res.body)
	}
	if res.json(t)["runner"].(map[string]any)["id"] != runnerID {
		t.Fatalf("get runner returned %s", res.body)
	}

	res = o.do("GET", "/api/runners", nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("list runners: %d %s", res.status, res.body)
	}
	if len(res.json(t)["runners"].([]any)) != 1 {
		t.Fatalf("list runners returned %s", res.body)
	}

	// A patch touches only what it names.
	res = o.do("PATCH", "/api/runners/"+runnerID, map[string]any{
		"config":        map[string]any{"bin": "codex"},
		"budget_tokens": 1000,
	}, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("patch runner: %d %s", res.status, res.body)
	}
	patched := res.json(t)["runner"].(map[string]any)
	if patched["kind"] != "cli" {
		t.Errorf("the patch changed the kind: %v", patched["kind"])
	}
	if patched["budget_tokens"].(float64) != 1000 {
		t.Errorf("budget_tokens = %v, want 1000", patched["budget_tokens"])
	}
	if patched["config"].(map[string]any)["bin"] != "codex" {
		t.Errorf("config = %v", patched["config"])
	}

	res = o.do("DELETE", "/api/runners/"+runnerID, nil, o.ownerCookie)
	if res.status != http.StatusNoContent {
		t.Fatalf("delete runner: %d %s", res.status, res.body)
	}
	res = o.do("GET", "/api/runners/"+runnerID, nil, o.ownerCookie)
	if res.status != http.StatusNotFound || errCode(t, res) != "runner_not_found" {
		t.Fatalf("get after delete: %d %s", res.status, res.body)
	}
}

func TestRunnerRegistryValidation(t *testing.T) {
	o := newOwnerHarness(t)

	// A duplicate name is a conflict.
	createRunner(t, o, cliRunnerBody("dev-cli"))
	res := o.do("POST", "/api/runners", cliRunnerBody("dev-cli"), o.ownerCookie)
	if res.status != http.StatusConflict || errCode(t, res) != "runner_name_taken" {
		t.Fatalf("duplicate runner: %d %s", res.status, res.body)
	}

	// A cli runner without a binary could never run.
	bad := cliRunnerBody("no-bin")
	bad["config"] = map[string]any{"args": []string{"hello"}}
	res = o.do("POST", "/api/runners", bad, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity || errCode(t, res) != "invalid_config" {
		t.Fatalf("cli without a bin: %d %s", res.status, res.body)
	}

	// An api runner without a model could never run.
	bad = apiRunnerBody("no-model")
	bad["config"] = map[string]any{"provider": "openai", "key_env": "OPENAI_API_KEY"}
	res = o.do("POST", "/api/runners", bad, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity || errCode(t, res) != "invalid_config" {
		t.Fatalf("api without a model: %d %s", res.status, res.body)
	}

	// A name that is not mentionable-shaped is refused.
	bad = cliRunnerBody("Not Valid")
	res = o.do("POST", "/api/runners", bad, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity || errCode(t, res) != "invalid_request" {
		t.Fatalf("bad name: %d %s", res.status, res.body)
	}

	// An unknown kind is refused.
	bad = cliRunnerBody("pigeon")
	bad["kind"] = "pigeon"
	res = o.do("POST", "/api/runners", bad, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown kind: %d %s", res.status, res.body)
	}

	// A runner that exists may be patched into an unrunnable shape only if
	// the config follows the kind.
	runner := createRunner(t, o, apiRunnerBody("pm"))
	res = o.do("PATCH", "/api/runners/"+runner["id"].(string), map[string]any{
		"kind": "cli",
	}, o.ownerCookie)
	if res.status != http.StatusUnprocessableEntity || errCode(t, res) != "invalid_config" {
		t.Fatalf("kind change without a matching config: %d %s", res.status, res.body)
	}
}

func TestRunnerRegistryRoles(t *testing.T) {
	o := newOwnerHarness(t)
	tenantID := o.tenantID(o.ownerCookie)
	runner := createRunner(t, o, cliRunnerBody("dev-cli"))
	runnerID := runner["id"].(string)

	// A member reads the registry but does not manage it.
	o.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	memberCookie := o.loginTo("member@example.com", tenantID)
	res := o.do("GET", "/api/runners", nil, memberCookie)
	if res.status != http.StatusOK {
		t.Fatalf("member list runners: %d %s", res.status, res.body)
	}
	res = o.do("POST", "/api/runners", cliRunnerBody("member-runner"), memberCookie)
	if res.status != http.StatusForbidden || errCode(t, res) != "forbidden" {
		t.Fatalf("member create runner: %d %s, want 403", res.status, res.body)
	}
	res = o.do("PATCH", "/api/runners/"+runnerID, map[string]any{"budget_tokens": 10}, memberCookie)
	if res.status != http.StatusForbidden {
		t.Fatalf("member patch runner: %d %s, want 403", res.status, res.body)
	}
	res = o.do("DELETE", "/api/runners/"+runnerID, nil, memberCookie)
	if res.status != http.StatusForbidden {
		t.Fatalf("member delete runner: %d %s, want 403", res.status, res.body)
	}

	// A guest reads too.
	o.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)
	guestCookie := o.loginTo("guest@example.com", tenantID)
	res = o.do("GET", "/api/runners", nil, guestCookie)
	if res.status != http.StatusOK {
		t.Fatalf("guest list runners: %d %s", res.status, res.body)
	}
	res = o.do("POST", "/api/runners", cliRunnerBody("guest-runner"), guestCookie)
	if res.status != http.StatusForbidden {
		t.Fatalf("guest create runner: %d %s, want 403", res.status, res.body)
	}
}

func TestRunnerCrossTenantIsInvisible(t *testing.T) {
	o := newOwnerHarness(t)
	runner := createRunner(t, o, cliRunnerBody("dev-cli"))
	runnerID := runner["id"].(string)

	// A second tenant in the same database: registering creates the user and
	// their own tenant, where they are the owner.
	other := o.register("other@example.com", goodPassword, "Globex", "globex")
	if other.status != http.StatusCreated {
		t.Fatalf("register the other tenant: %d %s", other.status, other.body)
	}
	otherCookie := other.cookie

	res := o.do("GET", "/api/runners/"+runnerID, nil, otherCookie)
	if res.status != http.StatusNotFound || errCode(t, res) != "runner_not_found" {
		t.Fatalf("cross-tenant read: %d %s", res.status, res.body)
	}
	res = o.do("PATCH", "/api/runners/"+runnerID, map[string]any{"budget_tokens": 10}, otherCookie)
	if res.status != http.StatusNotFound {
		t.Fatalf("cross-tenant patch: %d %s, want 404", res.status, res.body)
	}
	res = o.do("DELETE", "/api/runners/"+runnerID, nil, otherCookie)
	if res.status != http.StatusNotFound {
		t.Fatalf("cross-tenant delete: %d %s, want 404", res.status, res.body)
	}
	res = o.do("GET", "/api/runners", nil, otherCookie)
	if res.status != http.StatusOK {
		t.Fatalf("cross-tenant list: %d %s", res.status, res.body)
	}
	if len(res.json(t)["runners"].([]any)) != 0 {
		t.Fatalf("another tenant's runner is visible: %s", res.body)
	}
}

func TestRunnerInUseIsRefused(t *testing.T) {
	o := newOwnerHarness(t)
	runner := createRunner(t, o, cliRunnerBody("dev-cli"))
	runnerID := runner["id"].(string)

	// Bind the runner to an agent.
	agent := createAgent(t, o, map[string]any{
		"name":    "dev",
		"role":    "dev",
		"runners": []string{runnerID},
	})
	if len(agent["runners"].([]any)) != 1 {
		t.Fatalf("the chain was not written: %v", agent["runners"])
	}

	// A bound runner cannot be deleted: the agent would lose its transport.
	res := o.do("DELETE", "/api/runners/"+runnerID, nil, o.ownerCookie)
	if res.status != http.StatusConflict || errCode(t, res) != "runner_in_use" {
		t.Fatalf("delete a bound runner: %d %s", res.status, res.body)
	}
	res = o.do("GET", "/api/runners/"+runnerID, nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("the runner was deleted anyway: %d %s", res.status, res.body)
	}

	// Unbind it, and the delete goes through.
	res = o.do("PATCH", "/api/agents/"+agent["id"].(string), map[string]any{"runners": []string{}}, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("clear the chain: %d %s", res.status, res.body)
	}
	res = o.do("DELETE", "/api/runners/"+runnerID, nil, o.ownerCookie)
	if res.status != http.StatusNoContent {
		t.Fatalf("delete after unbinding: %d %s", res.status, res.body)
	}
}
