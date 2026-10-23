package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

type runsHarness struct {
	t     *testing.T
	db    *sql.DB
	admin *pgtest.DB
	srv   *httptest.Server
	store *workflow.Store
}

type runsResponse struct {
	status int
	body   []byte
	cookie string
}

func newRunsHarness(t *testing.T) *runsHarness {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	store := workflow.NewStore(app)
	agents := agent.NewStore(app)
	engine := workflow.NewEngine(workflow.EngineDeps{Store: store, Agents: agents, Work: work.NewStore(app)})
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSecureCookies(false),
		WithWorkflowStores(store, nil, nil), WithWorkflowEngine(engine))
	mux := http.NewServeMux()
	s.registerWorkflowRunRoutes(mux)
	s.registerCheckpointRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &runsHarness{t: t, db: app, admin: db, srv: srv, store: store}
}

func (h *runsHarness) do(method, path string, body any, cookie string) runsResponse {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	out := runsResponse{status: resp.StatusCode, body: b}
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			out.cookie = c.Value
		}
	}
	return out
}

func (r runsResponse) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("decode response %q: %v", r.body, err)
	}
	return out
}

func (h *runsHarness) register(email, tenant, slug string) (cookie, userID, tenantID string) {
	h.t.Helper()
	r := h.do("POST", "/api/auth/register", map[string]any{"email": email, "password": "correct-horse-battery-staple", "name": "Test User", "tenant_name": tenant, "tenant_slug": slug}, "")
	if r.status != http.StatusCreated {
		h.t.Fatalf("register: %d %s", r.status, r.body)
	}
	root := r.json(h.t)
	return r.cookie, root["user"].(map[string]any)["id"].(string), root["tenant"].(map[string]any)["id"].(string)
}

func (h *runsHarness) addMember(tenantID, email string, role tenancy.Role) string {
	h.t.Helper()
	_, userID, _ := h.register(email, "Extra user", "extra-"+email[:1]+email[2:4])
	if _, err := h.admin.Exec(`INSERT INTO tenant_memberships (id, tenant_id, user_id, role) VALUES ($1,$2,$3,$4)`, "m-"+userID, tenantID, userID, string(role)); err != nil {
		h.t.Fatal(err)
	}
	r := h.do("POST", "/api/auth/login", map[string]any{"email": email, "password": "correct-horse-battery-staple", "tenant_id": tenantID}, "")
	if r.status != http.StatusOK {
		h.t.Fatalf("login member: %d %s", r.status, r.body)
	}
	return r.cookie
}

func (h *runsHarness) item(cookie string) string {
	h.t.Helper()
	p := h.do("POST", "/api/products", map[string]any{"name": "Acme", "slug": "acme"}, cookie)
	if p.status != http.StatusCreated {
		h.t.Fatalf("create product: %d %s", p.status, p.body)
	}
	product := p.json(h.t)["product"].(map[string]any)["id"].(string)
	pr := h.do("POST", "/api/projects", map[string]any{"product_id": product, "name": "Core", "key": "CORE"}, cookie)
	if pr.status != http.StatusCreated {
		h.t.Fatalf("create project: %d %s", pr.status, pr.body)
	}
	project := pr.json(h.t)["project"].(map[string]any)["id"].(string)
	it := h.do("POST", "/api/items", map[string]any{"project_id": project, "kind": "task", "title": "Run target"}, cookie)
	if it.status != http.StatusCreated {
		h.t.Fatalf("create item: %d %s", it.status, it.body)
	}
	return it.json(h.t)["item"].(map[string]any)["id"].(string)
}

func checkpointTemplate(name string) map[string]any {
	return map[string]any{"name": name, "applies_to": "bug", "max_concurrency": 2, "graph": map[string]any{
		"schema_version": 1, "nodes": []any{map[string]any{"id": "n_gate", "kind": "checkpoint", "name": "review", "type": "human.review", "type_version": 1, "config": map[string]any{"question": "Review this", "policy": "retry_step", "fields": []any{map[string]any{"key": "target", "type": "string", "label": "Target branch"}}}, "on_error": "stop_workflow", "workspace": "scratch"}}, "edges": []any{},
	}}
}

func (h *runsHarness) publishedTemplate(cookie, name string) string {
	h.t.Helper()
	created := h.do("POST", "/api/workflow-templates", checkpointTemplate(name), cookie)
	if created.status != http.StatusCreated {
		h.t.Fatalf("create template: %d %s", created.status, created.body)
	}
	id := created.json(h.t)["template"].(map[string]any)["id"].(string)
	pub := h.do("POST", "/api/workflow-templates/"+id+"/publish", nil, cookie)
	if pub.status != http.StatusOK {
		h.t.Fatalf("publish template: %d %s", pub.status, pub.body)
	}
	return id
}

func startRun(t *testing.T, h *runsHarness, cookie, templateID, itemID string) map[string]any {
	t.Helper()
	res := h.do("POST", "/api/items/"+itemID+"/runs", map[string]any{"template_id": templateID}, cookie)
	if res.status != http.StatusAccepted {
		t.Fatalf("start run: %d %s", res.status, res.body)
	}
	return res.json(t)["run"].(map[string]any)
}

func apiCode(t *testing.T, res runsResponse) string {
	t.Helper()
	errorObj, _ := res.json(t)["error"].(map[string]any)
	code, _ := errorObj["code"].(string)
	return code
}

func TestRunsHTTPStartListDetailCancelRolesAndTenantScope(t *testing.T) {
	h := newRunsHarness(t)
	owner, _, tenantID := h.register("owner@example.com", "Acme", "acme")
	itemID := h.item(owner)
	templateID := h.publishedTemplate(owner, "Checkpoint flow")
	run := startRun(t, h, owner, templateID, itemID)
	runID := run["id"].(string)
	if run["state"] != "queued" {
		t.Fatalf("new run state: %v", run)
	}
	list := h.do("GET", "/api/runs?item_id="+itemID+"&limit=50", nil, owner)
	if list.status != http.StatusOK || len(list.json(t)["runs"].([]any)) != 1 {
		t.Fatalf("list runs: %d %s", list.status, list.body)
	}
	detail := h.do("GET", "/api/runs/"+runID, nil, owner)
	if detail.status != http.StatusOK || detail.json(t)["steps"] == nil {
		t.Fatalf("run detail: %d %s", detail.status, detail.body)
	}
	guest := h.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)
	if got := h.do("GET", "/api/runs/"+runID, nil, guest); got.status != http.StatusOK {
		t.Fatalf("guest read run: %d %s", got.status, got.body)
	}
	if got := h.do("POST", "/api/items/"+itemID+"/runs", map[string]any{"template_id": templateID}, guest); got.status != http.StatusForbidden {
		t.Fatalf("guest start run: %d %s", got.status, got.body)
	}
	member := h.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	memberRun := h.do("POST", "/api/items/"+itemID+"/runs", map[string]any{"template_id": templateID}, member)
	if memberRun.status != http.StatusAccepted {
		t.Fatalf("member start run: %d %s", memberRun.status, memberRun.body)
	}
	cancel := h.do("POST", "/api/runs/"+runID+"/cancel", nil, member)
	if cancel.status != http.StatusAccepted || cancel.json(t)["run"].(map[string]any)["state"] != "cancelled" {
		t.Fatalf("cancel: %d %s", cancel.status, cancel.body)
	}
	foreignCookie, _, _ := h.register("foreign@example.com", "Globex", "globex")
	foreignItem := h.item(foreignCookie)
	foreignTemplate := h.publishedTemplate(foreignCookie, "Foreign flow")
	foreignRun := startRun(t, h, foreignCookie, foreignTemplate, foreignItem)
	missing := h.do("GET", "/api/runs/"+foreignRun["id"].(string), nil, owner)
	if missing.status != http.StatusNotFound || apiCode(t, missing) != "run_not_found" {
		t.Fatalf("cross-tenant run: %d %s", missing.status, missing.body)
	}
}

func TestCheckpointInboxDecisionRaceInterventionAndRunLogs(t *testing.T) {
	h := newRunsHarness(t)
	owner, ownerID, _ := h.register("owner@example.com", "Acme", "acme")
	itemID := h.item(owner)
	templateID := h.publishedTemplate(owner, "Human flow")
	run := startRun(t, h, owner, templateID, itemID)
	runID := run["id"].(string)
	sc := tenancy.Scope{TenantID: runTenantID(t, h, owner), UserID: ownerID, Role: tenancy.RoleOwner}
	step, err := h.store.CreateStepRun(t.Context(), sc, workflow.NewStepRun{RunID: runID, NodeID: "n_gate", Kind: "checkpoint", Name: "review", WorkspaceKind: "scratch"})
	if err != nil {
		t.Fatal(err)
	}
	waiting := workflow.StepWaiting
	if _, err = h.store.UpdateStepRun(t.Context(), sc, step.ID, workflow.StepRunPatch{State: &waiting}); err != nil {
		t.Fatal(err)
	}
	cp, err := h.store.CreateCheckpoint(t.Context(), sc, workflow.NewCheckpoint{RunID: runID, StepRunID: step.ID, NodeID: "n_gate", Lap: 1, SummaryMD: "Review this", FieldsSpec: json.RawMessage(`[{"key":"target","type":"string","label":"Target","required":true}]`)})
	if err != nil {
		t.Fatal(err)
	}
	guest := h.addMember(sc.TenantID, "guest2@example.com", tenancy.RoleGuest)
	box := h.do("GET", "/api/checkpoints?state=pending", nil, guest)
	if box.status != http.StatusOK || len(box.json(t)["checkpoints"].([]any)) != 1 || box.json(t)["checkpoints"].([]any)[0].(map[string]any)["fields_spec"] == nil {
		t.Fatalf("pending inbox: %d %s", box.status, box.body)
	}
	if denied := h.do("POST", "/api/checkpoints/"+cp.ID+"/decision", map[string]any{"decision": "approve"}, guest); denied.status != http.StatusForbidden {
		t.Fatalf("guest decision: %d %s", denied.status, denied.body)
	}
	body := map[string]any{"decision": "approve", "fields": map[string]any{"target": "main"}}
	decided := h.do("POST", "/api/checkpoints/"+cp.ID+"/decision", body, owner)
	if decided.status != http.StatusOK || decided.json(t)["checkpoint"].(map[string]any)["state"] != "approved" {
		t.Fatalf("decision: %d %s", decided.status, decided.body)
	}
	race := h.do("POST", "/api/checkpoints/"+cp.ID+"/decision", body, owner)
	if race.status != http.StatusConflict || apiCode(t, race) != "already_decided" {
		t.Fatalf("decision race: %d %s", race.status, race.body)
	}
	decidedList := h.do("GET", "/api/checkpoints?state=decided", nil, owner)
	if decidedList.status != http.StatusOK || len(decidedList.json(t)["checkpoints"].([]any)) != 1 {
		t.Fatalf("decided inbox: %d %s", decidedList.status, decidedList.body)
	}

	// A live waiting execution can receive an inject; redo is also accepted and
	// recorded against the run without needing an agent worker in this API test.
	if updated, err := h.store.GetStepRun(t.Context(), sc, step.ID); err != nil {
		t.Fatal(err)
	} else if updated.State != workflow.StepSucceeded {
		t.Fatalf("decision did not finish waiting step: %s", updated.State)
	}
	step2, err := h.store.CreateStepRun(t.Context(), sc, workflow.NewStepRun{RunID: runID, NodeID: "n_gate", RunIndex: 1, Kind: "checkpoint", Name: "review", WorkspaceKind: "scratch"})
	if err != nil {
		t.Fatal(err)
	}
	injected := h.do("POST", "/api/runs/"+runID+"/intervene", map[string]any{"kind": "inject", "step_run_id": step2.ID, "node_id": "n_gate", "body_md": "Consider main"}, owner)
	if injected.status != http.StatusAccepted {
		t.Fatalf("inject: %d %s", injected.status, injected.body)
	}
	redo := h.do("POST", "/api/runs/"+runID+"/intervene", map[string]any{"kind": "redo", "node_id": "n_gate", "body_md": "Try again"}, owner)
	if redo.status != http.StatusAccepted {
		t.Fatalf("redo: %d %s", redo.status, redo.body)
	}
	interventions := h.do("GET", "/api/runs/"+runID+"/interventions", nil, guest)
	if interventions.status != http.StatusOK || len(interventions.json(t)["interventions"].([]any)) != 2 {
		t.Fatalf("list interventions: %d %s", interventions.status, interventions.body)
	}
	logs := h.do("GET", "/api/runs/"+runID+"/logs?after_seq=0&limit=500", nil, guest)
	if logs.status != http.StatusOK || logs.json(t)["logs"] == nil {
		t.Fatalf("run logs: %d %s", logs.status, logs.body)
	}
}

func runTenantID(t *testing.T, h *runsHarness, cookie string) string {
	t.Helper()
	me := h.do("GET", "/api/auth/me", nil, cookie)
	if me.status != http.StatusOK {
		t.Fatalf("me: %d %s", me.status, me.body)
	}
	return me.json(t)["tenant"].(map[string]any)["id"].(string)
}
