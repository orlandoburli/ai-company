package api

import (
	"net/http"
	"testing"

	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

// The run's variables and the tenant's defaults.
//
// A run is started with the variables it needs ("RUNNER": "claude"); the
// tenant keeps the defaults every run inherits. Reading the defaults is open
// to every role; changing them is a settings surface, so owner/admin only.

func TestRunStartCarriesTheVariables(t *testing.T) {
	h := newRunsHarness(t)
	owner, ownerID, tenantID := h.register("owner@example.com", "Acme", "acme")
	itemID := h.item(owner)
	templateID := h.publishedTemplate(owner, "Variables flow")

	res := h.do("POST", "/api/items/"+itemID+"/runs", map[string]any{
		"template_id": templateID,
		"variables":   map[string]string{"RUNNER": "claude", "REGION": "eu"},
	}, owner)
	if res.status != http.StatusAccepted {
		t.Fatalf("start run: %d %s", res.status, res.body)
	}
	run := res.json(t)["run"].(map[string]any)
	vars, ok := run["variables"].(map[string]any)
	if !ok {
		t.Fatalf("the run carries no variables: %v", run)
	}
	if vars["RUNNER"] != "claude" || vars["REGION"] != "eu" {
		t.Fatalf("the run's variables = %v", vars)
	}

	// They are stored, not only echoed.
	sc := tenancy.Scope{TenantID: tenantID, UserID: ownerID, Role: tenancy.RoleOwner}
	stored, err := h.store.GetRun(t.Context(), sc, run["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Variables["RUNNER"] != "claude" || stored.Variables["REGION"] != "eu" {
		t.Fatalf("the stored variables = %v", stored.Variables)
	}

	// The run's detail carries them too.
	detail := h.do("GET", "/api/runs/"+run["id"].(string), nil, owner)
	if detail.status != http.StatusOK {
		t.Fatalf("run detail: %d %s", detail.status, detail.body)
	}
	detailVars, _ := detail.json(t)["run"].(map[string]any)["variables"].(map[string]any)
	if detailVars["RUNNER"] != "claude" {
		t.Fatalf("the run detail's variables = %v", detailVars)
	}
}

func TestTenantVariablesReadAndWrite(t *testing.T) {
	h := newRunsHarness(t)
	owner, _, tenantID := h.register("owner@example.com", "Acme", "acme")
	member := h.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	guest := h.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)

	// The owner writes the defaults.
	put := h.do("PUT", "/api/tenant/variables", map[string]any{
		"variables": map[string]string{"RUNNER": "claude"},
	}, owner)
	if put.status != http.StatusOK {
		t.Fatalf("write variables: %d %s", put.status, put.body)
	}
	if got := put.json(t)["variables"].(map[string]any)["RUNNER"]; got != "claude" {
		t.Fatalf("the written variables = %v", put.json(t)["variables"])
	}

	// Every role may read them.
	for name, cookie := range map[string]string{"owner": owner, "member": member, "guest": guest} {
		get := h.do("GET", "/api/tenant/variables", nil, cookie)
		if get.status != http.StatusOK {
			t.Fatalf("%s read variables: %d %s", name, get.status, get.body)
		}
		vars, _ := get.json(t)["variables"].(map[string]any)
		if vars["RUNNER"] != "claude" {
			t.Fatalf("%s read %v, want the tenant's default", name, vars)
		}
	}

	// Only the owner and an admin may write them.
	for name, cookie := range map[string]string{"member": member, "guest": guest} {
		res := h.do("PUT", "/api/tenant/variables", map[string]any{
			"variables": map[string]string{"RUNNER": "codex"},
		}, cookie)
		if res.status != http.StatusForbidden {
			t.Fatalf("%s write variables: %d %s, want 403", name, res.status, res.body)
		}
	}
	// The defaults did not change.
	get := h.do("GET", "/api/tenant/variables", nil, owner)
	if got := get.json(t)["variables"].(map[string]any)["RUNNER"]; got != "claude" {
		t.Fatalf("the defaults changed to %v", got)
	}

	// A name that is not an identifier is refused.
	bad := h.do("PUT", "/api/tenant/variables", map[string]any{
		"variables": map[string]string{"1RUNNER": "claude"},
	}, owner)
	if bad.status != http.StatusUnprocessableEntity {
		t.Fatalf("a bad variable name: %d %s, want 422", bad.status, bad.body)
	}
}

func TestTenantVariablesAreTenantScoped(t *testing.T) {
	h := newRunsHarness(t)
	owner, _, _ := h.register("owner@example.com", "Acme", "acme")
	foreign, _, _ := h.register("foreign@example.com", "Globex", "globex")

	h.do("PUT", "/api/tenant/variables", map[string]any{
		"variables": map[string]string{"RUNNER": "claude"},
	}, owner)
	got := h.do("GET", "/api/tenant/variables", nil, foreign)
	if got.status != http.StatusOK {
		t.Fatalf("read variables: %d %s", got.status, got.body)
	}
	vars, _ := got.json(t)["variables"].(map[string]any)
	if len(vars) != 0 {
		t.Fatalf("another tenant sees %v", vars)
	}
}

// The engine keeps the run's variables: the run start stores them, and the
// engine reads them back.
func TestRunVariablesSurviveTheStore(t *testing.T) {
	h := newRunsHarness(t)
	owner, ownerID, tenantID := h.register("owner@example.com", "Acme", "acme")
	sc := tenancy.Scope{TenantID: tenantID, UserID: ownerID, Role: tenancy.RoleOwner}
	templateID := h.publishedTemplate(owner, "Store flow")

	run, err := h.store.CreateRun(t.Context(), sc, workflow.NewRun{
		TemplateID: templateID, TemplateVersion: 1, Variables: map[string]string{"RUNNER": "claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Variables["RUNNER"] != "claude" {
		t.Fatalf("the new run's variables = %v", run.Variables)
	}
	updated, err := h.store.SetRunVariables(t.Context(), sc, run.ID, map[string]string{"RUNNER": "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Variables["RUNNER"] != "codex" {
		t.Fatalf("the updated variables = %v", updated.Variables)
	}
}
