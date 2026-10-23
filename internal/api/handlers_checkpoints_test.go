package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

// checkpointFixture starts a run with a checkpoint on it and returns the
// checkpoint's id.
func checkpointFixture(t *testing.T, h *runsHarness, cookie, ownerID string) (string, tenancy.Scope) {
	t.Helper()
	itemID := h.item(cookie)
	templateID := h.publishedTemplate(cookie, "Human flow")
	run := startRun(t, h, cookie, templateID, itemID)
	runID := run["id"].(string)
	sc := tenancy.Scope{TenantID: runTenantID(t, h, cookie), UserID: ownerID, Role: tenancy.RoleOwner}
	step, err := h.store.CreateStepRun(t.Context(), sc, workflow.NewStepRun{RunID: runID, NodeID: "n_gate", Kind: "checkpoint", Name: "review", WorkspaceKind: "scratch"})
	if err != nil {
		t.Fatal(err)
	}
	cp, err := h.store.CreateCheckpoint(t.Context(), sc, workflow.NewCheckpoint{
		RunID: runID, StepRunID: step.ID, NodeID: "n_gate", Lap: 1,
		SummaryMD:  "## Run target (1)\n\n**The question:** Review this\n",
		FieldsSpec: json.RawMessage(`[{"key":"target","type":"string","label":"Target"}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	agentStep, err := h.store.CreateStepRun(t.Context(), sc, workflow.NewStepRun{RunID: runID, NodeID: "n_agent", Kind: "agent", Name: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	integrationStep, err := h.store.CreateStepRun(t.Context(), sc, workflow.NewStepRun{RunID: runID, NodeID: "n_pr", Kind: "integration", Name: "open PR"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateArtifact(t.Context(), sc, workflow.NewArtifact{
		RunID: runID, Kind: "branch", URI: "aic/item-step-1",
		Meta: json.RawMessage(`{"branch":"aic/item-step-1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateArtifact(t.Context(), sc, workflow.NewArtifact{
		RunID: runID, StepRunID: agentStep.ID, Kind: "file", URI: "internal/http/health.go",
		Meta: json.RawMessage(`{"path":"internal/http/health.go","status":"added"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateArtifact(t.Context(), sc, workflow.NewArtifact{
		RunID: runID, StepRunID: integrationStep.ID, Kind: "pr", URI: "https://github.com/acme/app/pull/12",
		Meta: json.RawMessage(`{"number":12}`),
	}); err != nil {
		t.Fatal(err)
	}
	return cp.ID, sc
}

func TestCheckpointResponseCarriesTheEvidence(t *testing.T) {
	h := newRunsHarness(t)
	owner, ownerID, _ := h.register("owner@example.com", "Acme", "acme")
	cpID, sc := checkpointFixture(t, h, owner, ownerID)

	res := h.do("GET", "/api/checkpoints/"+cpID, nil, owner)
	if res.status != http.StatusOK {
		t.Fatalf("get checkpoint: %d %s", res.status, res.body)
	}
	cp := res.json(t)["checkpoint"].(map[string]any)
	if summary, _ := cp["summary_md"].(string); !strings.Contains(summary, "**The question:** Review this") {
		t.Fatalf("summary_md=%q", summary)
	}
	if question, _ := cp["question"].(string); question != "Review this" {
		t.Fatalf("question=%q", question)
	}
	artifacts, ok := cp["artifacts"].([]any)
	if !ok || len(artifacts) != 3 {
		t.Fatalf("artifacts=%v", cp["artifacts"])
	}
	seen := map[string]map[string]any{}
	for _, value := range artifacts {
		artifact := value.(map[string]any)
		seen[artifact["kind"].(string)] = artifact
	}
	if seen["branch"]["uri"] != "aic/item-step-1" {
		t.Fatalf("branch artifact=%v", seen["branch"])
	}
	if seen["file"]["step_name"] != "implement" || seen["file"]["uri"] != "internal/http/health.go" {
		t.Fatalf("agent artifact=%v", seen["file"])
	}
	if seen["pr"]["step_name"] != "open PR" || seen["pr"]["uri"] != "https://github.com/acme/app/pull/12" {
		t.Fatalf("integration artifact=%v", seen["pr"])
	}
	item, ok := cp["item"].(map[string]any)
	if !ok {
		t.Fatalf("item=%v", cp["item"])
	}
	if item["id"] == "" || item["title"] != "Run target" {
		t.Fatalf("item=%v", item)
	}
	if number, ok := item["number"].(float64); !ok || number < 1 {
		t.Fatalf("item number=%v", item["number"])
	}
	// The inbox carries the same evidence as the detail.
	box := h.do("GET", "/api/checkpoints?state=pending", nil, owner)
	if box.status != http.StatusOK {
		t.Fatalf("inbox: %d %s", box.status, box.body)
	}
	listed := box.json(t)["checkpoints"].([]any)
	if len(listed) != 1 {
		t.Fatalf("inbox=%v", listed)
	}
	entry := listed[0].(map[string]any)
	if entry["question"] != "Review this" || entry["item"] == nil || entry["artifacts"] == nil {
		t.Fatalf("inbox entry=%v", entry)
	}
	_ = sc
}

func TestCheckpointResponseSurvivesADeletedItem(t *testing.T) {
	h := newRunsHarness(t)
	owner, ownerID, _ := h.register("owner@example.com", "Acme", "acme")
	cpID, sc := checkpointFixture(t, h, owner, ownerID)
	before := h.do("GET", "/api/checkpoints/"+cpID, nil, owner)
	if before.status != http.StatusOK {
		t.Fatalf("get checkpoint: %d %s", before.status, before.body)
	}
	itemID, _ := before.json(t)["checkpoint"].(map[string]any)["item"].(map[string]any)["id"].(string)
	if itemID == "" {
		t.Fatal("the checkpoint does not name its item")
	}
	items := work.NewStore(h.db)
	if err := items.DeleteItem(t.Context(), sc, itemID, ownerID); err != nil {
		t.Fatalf("delete item: %v", err)
	}
	res := h.do("GET", "/api/checkpoints/"+cpID, nil, owner)
	if res.status != http.StatusOK {
		t.Fatalf("get checkpoint: %d %s", res.status, res.body)
	}
	cp := res.json(t)["checkpoint"].(map[string]any)
	if cp["item"] != nil {
		t.Fatalf("a deleted item must not be named: %v", cp["item"])
	}
	if summary, _ := cp["summary_md"].(string); summary == "" {
		t.Fatalf("summary_md=%v", cp["summary_md"])
	}
	if question, _ := cp["question"].(string); question != "Review this" {
		t.Fatalf("question=%q", question)
	}
}

func TestCheckpointOfAnotherTenantIsNotFound(t *testing.T) {
	h := newRunsHarness(t)
	owner, ownerID, _ := h.register("owner@example.com", "Acme", "acme")
	cpID, _ := checkpointFixture(t, h, owner, ownerID)
	foreign, _, _ := h.register("foreign@example.com", "Globex", "globex")
	res := h.do("GET", "/api/checkpoints/"+cpID, nil, foreign)
	if res.status != http.StatusNotFound || apiCode(t, res) != "checkpoint_not_found" {
		t.Fatalf("cross-tenant checkpoint: %d %s", res.status, res.body)
	}
}
