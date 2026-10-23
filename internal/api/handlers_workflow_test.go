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

	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/notification"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

type workflowHarness struct {
	t      *testing.T
	db     *sql.DB
	admin  *pgtest.DB
	srv    *httptest.Server
	store  *workflow.Store
	mem    *memory.Store
	notify *notification.Store
}

type workflowResponse struct {
	status int
	body   []byte
	cookie string
}

func newWorkflowHarness(t *testing.T) *workflowHarness {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithSecureCookies(false),
		WithWorkflowStores(workflow.NewStore(app), memory.NewStore(app), notification.NewStore(app)))
	mux := http.NewServeMux()
	s.registerWorkflowTemplateRoutes(mux)
	s.registerWorkflowValidateRoute(mux)
	s.registerMemoryRoutes(mux)
	s.registerNotificationRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &workflowHarness{
		t: t, db: app, admin: db, srv: srv,
		store: s.workflows, mem: s.memory, notify: s.notify,
	}
}

func (h *workflowHarness) doRaw(method, path, body, cookie string) workflowResponse {
	h.t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, bytes.NewBufferString(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
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
	return workflowResponse{status: resp.StatusCode, body: b}
}

func (h *workflowHarness) do(method, path string, body any, cookie string) workflowResponse {
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
	out := workflowResponse{status: resp.StatusCode, body: b}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == SessionCookie && cookie.Value != "" {
			out.cookie = cookie.Value
		}
	}
	return out
}

func (r workflowResponse) json(t *testing.T) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatalf("decode response %q: %v", r.body, err)
	}
	return body
}

func (h *workflowHarness) register(email, tenant, slug string) (cookie, userID, tenantID string) {
	h.t.Helper()
	res := h.do("POST", "/api/auth/register", map[string]any{
		"email": email, "password": "correct-horse-battery-staple", "name": "Test User",
		"tenant_name": tenant, "tenant_slug": slug,
	}, "")
	if res.status != http.StatusCreated {
		h.t.Fatalf("register %s: %d %s", email, res.status, res.body)
	}
	root := res.json(h.t)
	return res.cookie, root["user"].(map[string]any)["id"].(string), root["tenant"].(map[string]any)["id"].(string)
}

func (h *workflowHarness) addMember(tenantID, email string, role tenancy.Role) string {
	h.t.Helper()
	_, userID, _ := h.register(email, "Extra user", "extra-"+email[:1]+email[2:4])
	if _, err := h.admin.Exec(`INSERT INTO tenant_memberships (id, tenant_id, user_id, role) VALUES ($1,$2,$3,$4)`, "m-"+userID, tenantID, userID, string(role)); err != nil {
		h.t.Fatalf("add member: %v", err)
	}
	res := h.do("POST", "/api/auth/login", map[string]any{
		"email": email, "password": "correct-horse-battery-staple", "tenant_id": tenantID,
	}, "")
	if res.status != http.StatusOK {
		h.t.Fatalf("login %s: %d %s", email, res.status, res.body)
	}
	return res.cookie
}

func templateBody(name string) map[string]any {
	return map[string]any{
		"name": name, "applies_to": "bug", "max_concurrency": 4,
		"graph": map[string]any{
			"schema_version": 1,
			"nodes": []any{map[string]any{
				"id": "n_agent", "kind": "agent", "name": "implement", "type": "agent.dev", "type_version": 1,
				"config": map[string]any{"agent_id": "agent-1"}, "on_error": "stop_workflow", "workspace": "scratch",
			}},
			"edges": []any{},
		},
	}
}

func workflowErrCode(t *testing.T, res workflowResponse) string {
	t.Helper()
	err, ok := res.json(t)["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := err["code"].(string)
	return code
}

func TestWorkflowTemplateHTTPAndRoles(t *testing.T) {
	h := newWorkflowHarness(t)
	ownerCookie, _, tenantID := h.register("owner@example.com", "Acme", "acme")
	created := h.do("POST", "/api/workflow-templates", templateBody("Bugfix"), ownerCookie)
	if created.status != http.StatusCreated {
		t.Fatalf("create template: %d %s", created.status, created.body)
	}
	template := created.json(t)["template"].(map[string]any)
	templateID := template["id"].(string)
	if template["tenant_id"] != tenantID || template["created_by"] == "" || template["graph"] == nil {
		t.Fatalf("template response omitted stored fields: %v", template)
	}

	read := h.do("GET", "/api/workflow-templates/"+templateID, nil, ownerCookie)
	if read.status != http.StatusOK || read.json(t)["template"].(map[string]any)["name"] != "Bugfix" {
		t.Fatalf("get template: %d %s", read.status, read.body)
	}
	list := h.do("GET", "/api/workflow-templates", nil, ownerCookie)
	if list.status != http.StatusOK || len(list.json(t)["templates"].([]any)) != 1 {
		t.Fatalf("list templates: %d %s", list.status, list.body)
	}

	bad := templateBody("Invalid")
	bad["graph"] = map[string]any{"schema_version": 1, "nodes": []any{}, "edges": []any{}}
	invalid := h.do("POST", "/api/workflow-templates", bad, ownerCookie)
	if invalid.status != http.StatusUnprocessableEntity || workflowErrCode(t, invalid) != "template_invalid" {
		t.Fatalf("invalid graph: %d %s", invalid.status, invalid.body)
	}
	if count := len(h.do("GET", "/api/workflow-templates", nil, ownerCookie).json(t)["templates"].([]any)); count != 1 {
		t.Fatalf("invalid graph created a template (count %d)", count)
	}

	published := h.do("POST", "/api/workflow-templates/"+templateID+"/publish", nil, ownerCookie)
	if published.status != http.StatusOK {
		t.Fatalf("publish: %d %s", published.status, published.body)
	}
	patch := h.do("PATCH", "/api/workflow-templates/"+templateID, map[string]any{"name": "Changed"}, ownerCookie)
	if patch.status != http.StatusConflict || workflowErrCode(t, patch) != "template_published" {
		t.Fatalf("edit published template: %d %s", patch.status, patch.body)
	}
	remove := h.do("DELETE", "/api/workflow-templates/"+templateID, nil, ownerCookie)
	if remove.status != http.StatusConflict || workflowErrCode(t, remove) != "template_published" {
		t.Fatalf("delete published template: %d %s", remove.status, remove.body)
	}
	version := h.do("POST", "/api/workflow-templates/"+templateID+"/versions", nil, ownerCookie)
	if version.status != http.StatusCreated || version.json(t)["template"].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("new version: %d %s", version.status, version.body)
	}
	history := h.do("GET", "/api/workflow-templates/"+templateID, nil, ownerCookie).json(t)["template"].(map[string]any)["versions"].([]any)
	if len(history) != 2 {
		t.Fatalf("template version history = %v, want two versions", history)
	}
	if _, hasGraph := history[0].(map[string]any)["graph"]; hasGraph {
		t.Fatalf("template version history included graph bodies: %v", history)
	}
	versionID := version.json(t)["template"].(map[string]any)["id"].(string)
	if deleted := h.do("DELETE", "/api/workflow-templates/"+versionID, nil, ownerCookie); deleted.status != http.StatusNoContent {
		t.Fatalf("delete draft: %d %s", deleted.status, deleted.body)
	}

	memberCookie := h.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	if res := h.do("GET", "/api/workflow-templates", nil, memberCookie); res.status != http.StatusOK {
		t.Fatalf("member read templates: %d %s", res.status, res.body)
	}
	if res := h.do("POST", "/api/workflow-templates", templateBody("Member"), memberCookie); res.status != http.StatusForbidden {
		t.Fatalf("member create template: %d %s", res.status, res.body)
	}
	guestCookie := h.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)
	if res := h.do("GET", "/api/workflow-templates", nil, guestCookie); res.status != http.StatusOK {
		t.Fatalf("guest read templates: %d %s", res.status, res.body)
	}
	if res := h.do("POST", "/api/workflow-templates", templateBody("Guest"), guestCookie); res.status != http.StatusForbidden {
		t.Fatalf("guest create template: %d %s", res.status, res.body)
	}

	foreignCookie, _, _ := h.register("foreign@example.com", "Globex", "globex")
	foreign := h.do("POST", "/api/workflow-templates", templateBody("Foreign"), foreignCookie)
	if foreign.status != http.StatusCreated {
		t.Fatalf("create foreign template: %d %s", foreign.status, foreign.body)
	}
	foreignID := foreign.json(t)["template"].(map[string]any)["id"].(string)
	if res := h.do("GET", "/api/workflow-templates/"+foreignID, nil, ownerCookie); res.status != http.StatusNotFound || workflowErrCode(t, res) != "template_not_found" {
		t.Fatalf("cross-tenant template read: %d %s", res.status, res.body)
	}
}

func TestWorkflowTemplateValidateEndpoint(t *testing.T) {
	h := newWorkflowHarness(t)
	cookie, _, _ := h.register("validate@example.com", "Acme", "acme")
	path := "/api/workflow-templates/validate"
	if unauthenticated := h.do("POST", path, map[string]any{"graph": map[string]any{}}, ""); unauthenticated.status != http.StatusUnauthorized {
		t.Fatalf("validate without session: %d %s", unauthenticated.status, unauthenticated.body)
	}

	brokenGraph := map[string]any{
		"schema_version": 1,
		"nodes": []any{
			map[string]any{"id": "entry", "kind": "agent", "name": "entry", "type": "agent.test", "type_version": 1, "config": map[string]any{"agent_id": "a"}, "on_error": "stop_workflow"},
			map[string]any{"id": "orphan", "kind": "agent", "name": "orphan", "type": "agent.test", "type_version": 1, "config": map[string]any{"agent_id": "b"}, "on_error": "stop_workflow"},
		},
		"edges": []any{map[string]any{"id": "bad-edge", "from": map[string]any{"node": "entry", "port": "unknown"}, "to": map[string]any{"node": "missing", "port": "in"}}},
	}
	broken := h.do("POST", path, map[string]any{"graph": brokenGraph}, cookie)
	if broken.status != http.StatusOK {
		t.Fatalf("validate broken graph: %d %s", broken.status, broken.body)
	}
	problems, ok := broken.json(t)["problems"].([]any)
	if !ok || len(problems) == 0 {
		t.Fatalf("broken graph problems = %v", broken.json(t))
	}
	foundEdge := false
	for _, raw := range problems {
		problem := raw.(map[string]any)
		if problem["edge_id"] == "bad-edge" {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Fatalf("response did not identify bad-edge: %v", problems)
	}

	validGraph := templateBody("valid")["graph"]
	valid := h.do("POST", path, map[string]any{"graph": validGraph}, cookie)
	if valid.status != http.StatusOK || len(valid.json(t)["problems"].([]any)) != 0 {
		t.Fatalf("validate valid graph: %d %s", valid.status, valid.body)
	}
	if malformed := h.doRaw("POST", path, `{"graph":`, cookie); malformed.status != http.StatusBadRequest || workflowErrCode(t, malformed) != "invalid_request" {
		t.Fatalf("malformed validate request: %d %s", malformed.status, malformed.body)
	}
	if nonObject := h.do("POST", path, map[string]any{"graph": []any{}}, cookie); nonObject.status != http.StatusBadRequest || workflowErrCode(t, nonObject) != "invalid_request" {
		t.Fatalf("non-object graph: %d %s", nonObject.status, nonObject.body)
	}
}

func TestMemoryHTTPAppendOnlyAndValidation(t *testing.T) {
	h := newWorkflowHarness(t)
	ownerCookie, _, tenantID := h.register("owner@example.com", "Acme", "acme")
	memberCookie := h.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	first := h.do("POST", "/api/memory", map[string]any{
		"scope_kind": "item", "scope_ref": "item-1", "key": "convention", "content_md": "Use tabs.", "tags": []string{"x"},
	}, memberCookie)
	if first.status != http.StatusCreated {
		t.Fatalf("write memory: %d %s", first.status, first.body)
	}
	firstEntry := first.json(t)["entry"].(map[string]any)
	if firstEntry["revision"].(float64) != 1 {
		t.Fatalf("first revision: %v", firstEntry)
	}
	read := h.do("GET", "/api/memory?scope_kind=item&scope_ref=item-1", nil, ownerCookie)
	if read.status != http.StatusOK || len(read.json(t)["entries"].([]any)) != 1 {
		t.Fatalf("read memory: %d %s", read.status, read.body)
	}
	all := h.do("GET", "/api/memory", nil, ownerCookie)
	if all.status != http.StatusOK || len(all.json(t)["entries"].([]any)) != 1 {
		t.Fatalf("read tenant memory: %d %s", all.status, all.body)
	}
	second := h.do("POST", "/api/memory", map[string]any{
		"scope_kind": "item", "scope_ref": "item-1", "key": "convention", "content_md": "Use spaces.",
	}, memberCookie)
	if second.status != http.StatusCreated {
		t.Fatalf("second memory write: %d %s", second.status, second.body)
	}
	secondEntry := second.json(t)["entry"].(map[string]any)
	if secondEntry["revision"].(float64) != 2 || secondEntry["seq"].(float64) <= firstEntry["seq"].(float64) {
		t.Fatalf("second revision/sequence: %v", secondEntry)
	}
	history := h.do("GET", "/api/memory?scope_kind=item&scope_ref=item-1&key=convention", nil, ownerCookie)
	entries := history.json(t)["entries"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["revision"].(float64) != 2 {
		t.Fatalf("memory history: %d %s", history.status, history.body)
	}
	if entries[1].(map[string]any)["superseded_by"] != secondEntry["id"] {
		t.Fatalf("first revision was not superseded: %v", entries[1])
	}
	runWrite := h.do("POST", "/api/memory", map[string]any{
		"scope_kind": "run", "scope_ref": "run-1", "key": "x", "content_md": "no",
	}, memberCookie)
	if runWrite.status != http.StatusForbidden || workflowErrCode(t, runWrite) != "run_memory_readonly" {
		t.Fatalf("run memory write: %d %s", runWrite.status, runWrite.body)
	}
	unknown := h.do("GET", "/api/memory?scope_kind=unknown&scope_ref=x", nil, ownerCookie)
	if unknown.status != http.StatusBadRequest || workflowErrCode(t, unknown) != "memory_scope_invalid" {
		t.Fatalf("unknown memory scope: %d %s", unknown.status, unknown.body)
	}
}

func TestNotificationHTTPOwnershipAndUnreadCount(t *testing.T) {
	h := newWorkflowHarness(t)
	ownerCookie, ownerID, tenantID := h.register("owner@example.com", "Acme", "acme")
	memberCookie := h.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	memberRes := h.do("GET", "/api/auth/me", nil, memberCookie)
	memberID := memberRes.json(t)["user"].(map[string]any)["id"].(string)
	scope := tenancy.Scope{TenantID: tenantID, UserID: ownerID, Role: tenancy.RoleOwner}
	first, err := h.notify.Create(t.Context(), scope, notification.New{
		UserID: ownerID, Kind: notification.KindRunFailed, TitleMD: "Failed", BodyMD: "Run failed.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.notify.Create(t.Context(), scope, notification.New{
		UserID: ownerID, Kind: notification.KindRunCompleted, TitleMD: "Done",
	}); err != nil {
		t.Fatal(err)
	}
	other, err := h.notify.Create(t.Context(), scope, notification.New{
		UserID: memberID, Kind: notification.KindSyncError, TitleMD: "Private",
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := h.do("GET", "/api/notifications?unread=true", nil, ownerCookie)
	if listed.status != http.StatusOK || len(listed.json(t)["notifications"].([]any)) != 2 || listed.json(t)["unread"].(float64) != 2 {
		t.Fatalf("list notifications: %d %s", listed.status, listed.body)
	}
	marked := h.do("POST", "/api/notifications/"+first.ID+"/read", nil, ownerCookie)
	if marked.status != http.StatusOK {
		t.Fatalf("mark notification read: %d %s", marked.status, marked.body)
	}
	if duplicate := h.do("POST", "/api/notifications/"+first.ID+"/read", nil, ownerCookie); duplicate.status != http.StatusConflict || workflowErrCode(t, duplicate) != "notification_already_read" {
		t.Fatalf("mark already-read notification: %d %s", duplicate.status, duplicate.body)
	}
	remaining := h.do("GET", "/api/notifications", nil, ownerCookie)
	if remaining.status != http.StatusOK || remaining.json(t)["unread"].(float64) != 1 {
		t.Fatalf("unread count after read: %d %s", remaining.status, remaining.body)
	}
	if denied := h.do("POST", "/api/notifications/"+other.ID+"/read", nil, ownerCookie); denied.status != http.StatusNotFound || workflowErrCode(t, denied) != "notification_not_found" {
		t.Fatalf("read another user's notification: %d %s", denied.status, denied.body)
	}
}
