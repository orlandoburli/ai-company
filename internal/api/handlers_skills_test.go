package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type skillAPIHarness struct {
	t     *testing.T
	admin *pgtest.DB
	srv   *httptest.Server
}

type skillAPIResponse struct {
	status int
	body   []byte
	cookie string
}

func newSkillAPIHarness(t *testing.T) *skillAPIHarness {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSecureCookies(false))
	mux := http.NewServeMux()
	s.registerSkillRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &skillAPIHarness{t: t, admin: db, srv: srv}
}

func (h *skillAPIHarness) do(method, path string, body any, cookie string) skillAPIResponse {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
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
	encoded, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	out := skillAPIResponse{status: resp.StatusCode, body: encoded}
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			out.cookie = c.Value
		}
	}
	return out
}

func (r skillAPIResponse) json(t *testing.T) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(r.body, &result); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
	return result
}

func (h *skillAPIHarness) register(email, tenant, slug string) (cookie, userID, tenantID string) {
	h.t.Helper()
	res := h.do("POST", "/api/auth/register", map[string]any{
		"email": email, "password": "correct-horse-battery-staple", "name": "Test User",
		"tenant_name": tenant, "tenant_slug": slug,
	}, "")
	if res.status != http.StatusCreated {
		h.t.Fatalf("register: %d %s", res.status, res.body)
	}
	body := res.json(h.t)
	return res.cookie, body["user"].(map[string]any)["id"].(string), body["tenant"].(map[string]any)["id"].(string)
}

func (h *skillAPIHarness) addMember(tenantID, email string, role tenancy.Role) string {
	h.t.Helper()
	_, userID, _ := h.register(email, "Other tenant", "other-"+email[:1]+email[2:4])
	if _, err := h.admin.Exec(`INSERT INTO tenant_memberships (id, tenant_id, user_id, role) VALUES ($1,$2,$3,$4)`, "m-"+userID, tenantID, userID, string(role)); err != nil {
		h.t.Fatalf("membership: %v", err)
	}
	res := h.do("POST", "/api/auth/login", map[string]any{
		"email": email, "password": "correct-horse-battery-staple", "tenant_id": tenantID,
	}, "")
	if res.status != http.StatusOK {
		h.t.Fatalf("login: %d %s", res.status, res.body)
	}
	return res.cookie
}

func skillErrorCode(t *testing.T, res skillAPIResponse) string {
	t.Helper()
	errorValue, ok := res.json(t)["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := errorValue["code"].(string)
	return code
}

func TestSkillsHTTPRolesVersionsAndResponses(t *testing.T) {
	h := newSkillAPIHarness(t)
	owner, _, tenantID := h.register("owner@example.com", "Acme", "acme")
	create := h.do("POST", "/api/skills", map[string]any{
		"name": "Go conventions", "slug": "go-conventions", "description": "Go formatting and tests",
	}, owner)
	if create.status != http.StatusCreated {
		t.Fatalf("create: %d %s", create.status, create.body)
	}
	item := create.json(t)["skill"].(map[string]any)
	id := item["id"].(string)
	if item["tenant_id"] != tenantID || item["slug"] != "go-conventions" {
		t.Fatalf("created skill shape: %v", item)
	}
	version := h.do("POST", "/api/skills/"+id+"/versions", map[string]any{"content_md": "gofmt -w ."}, owner)
	if version.status != http.StatusCreated || version.json(t)["version"].(map[string]any)["content_sha"] == "" {
		t.Fatalf("create version: %d %s", version.status, version.body)
	}
	if version.json(t)["version"].(map[string]any)["approved_at"] == nil {
		t.Fatal("human-authored version was not approved")
	}
	read := h.do("GET", "/api/skills/"+id, nil, owner)
	if read.status != http.StatusOK || len(read.json(t)["versions"].([]any)) != 1 {
		t.Fatalf("detail: %d %s", read.status, read.body)
	}
	list := h.do("GET", "/api/skills", nil, owner)
	if list.status != http.StatusOK || len(list.json(t)["skills"].([]any)) != 1 {
		t.Fatalf("list: %d %s", list.status, list.body)
	}
	patch := h.do("PATCH", "/api/skills/"+id, map[string]any{"description": "Updated description"}, owner)
	if patch.status != http.StatusOK || patch.json(t)["skill"].(map[string]any)["description"] != "Updated description" {
		t.Fatalf("patch: %d %s", patch.status, patch.body)
	}
	if disabled := h.do("POST", "/api/skills/"+id+"/enable", map[string]any{"enabled": false}, owner); disabled.status != http.StatusOK || disabled.json(t)["skill"].(map[string]any)["enabled"] != false {
		t.Fatalf("disable: %d %s", disabled.status, disabled.body)
	}
	if enabled := h.do("PATCH", "/api/skills/"+id, map[string]any{"enabled": true}, owner); enabled.status != http.StatusOK || enabled.json(t)["skill"].(map[string]any)["enabled"] != true {
		t.Fatalf("enable via patch: %d %s", enabled.status, enabled.body)
	}

	draft := h.do("POST", "/api/skills/"+id+"/versions", map[string]any{
		"content_md": "agent proposal", "author_kind": "agent_draft", "author_id": "AGENT-1",
	}, owner)
	if draft.status != http.StatusCreated || draft.json(t)["version"].(map[string]any)["approved_at"] != nil {
		t.Fatalf("draft: %d %s", draft.status, draft.body)
	}
	draftNumber := draft.json(t)["version"].(map[string]any)["version"].(float64)
	approved := h.do("POST", "/api/skills/"+id+"/versions/"+strconv.Itoa(int(draftNumber))+"/approve", nil, owner)
	if approved.status != http.StatusOK || approved.json(t)["version"].(map[string]any)["approved_at"] == nil {
		t.Fatalf("approve: %d %s", approved.status, approved.body)
	}
	if deleted := h.do("DELETE", "/api/skills/"+id, nil, owner); deleted.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.status, deleted.body)
	}
}

func TestSkillsHTTPRolesTenantIsolationAndDuplicate(t *testing.T) {
	h := newSkillAPIHarness(t)
	owner, _, tenantID := h.register("owner@example.com", "Acme", "acme")
	member := h.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	guest := h.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)
	create := h.do("POST", "/api/skills", map[string]any{"name": "Build", "slug": "build", "description": "Build code"}, owner)
	if create.status != http.StatusCreated {
		t.Fatalf("create: %d %s", create.status, create.body)
	}
	id := create.json(t)["skill"].(map[string]any)["id"].(string)
	draft := h.do("POST", "/api/skills/"+id+"/versions", map[string]any{
		"content_md": "pending proposal", "author_kind": "agent_draft", "author_id": "AGENT-1",
	}, owner)
	if draft.status != http.StatusCreated {
		t.Fatalf("create draft for role check: %d %s", draft.status, draft.body)
	}
	draftVersion := int(draft.json(t)["version"].(map[string]any)["version"].(float64))
	if duplicate := h.do("POST", "/api/skills", map[string]any{"name": "Duplicate", "slug": "build"}, owner); duplicate.status != http.StatusConflict || skillErrorCode(t, duplicate) != "skill_slug_taken" {
		t.Fatalf("duplicate: %d %s", duplicate.status, duplicate.body)
	}
	for _, cookie := range []string{member, guest} {
		if got := h.do("GET", "/api/skills/"+id, nil, cookie); got.status != http.StatusOK {
			t.Fatalf("read as role cookie=%s: %d %s", cookie, got.status, got.body)
		}
	}
	for _, cookie := range []string{member, guest} {
		if denied := h.do("POST", "/api/skills", map[string]any{"name": "No", "slug": "no"}, cookie); denied.status != http.StatusForbidden {
			t.Fatalf("write as role: %d %s", denied.status, denied.body)
		}
		if denied := h.do("POST", "/api/skills/"+id+"/enable", map[string]any{"enabled": false}, cookie); denied.status != http.StatusForbidden {
			t.Fatalf("enable as role: %d %s", denied.status, denied.body)
		}
		if denied := h.do("POST", "/api/skills/"+id+"/versions/"+strconv.Itoa(draftVersion)+"/approve", nil, cookie); denied.status != http.StatusForbidden {
			t.Fatalf("approve as role: %d %s", denied.status, denied.body)
		}
	}
	foreign, _, _ := h.register("foreign@example.com", "Globex", "globex")
	other := h.do("POST", "/api/skills", map[string]any{"name": "Foreign", "slug": "foreign"}, foreign)
	if other.status != http.StatusCreated {
		t.Fatalf("create foreign: %d %s", other.status, other.body)
	}
	foreignID := other.json(t)["skill"].(map[string]any)["id"].(string)
	if hidden := h.do("GET", "/api/skills/"+foreignID, nil, owner); hidden.status != http.StatusNotFound || skillErrorCode(t, hidden) != "skill_not_found" {
		t.Fatalf("cross-tenant read: %d %s", hidden.status, hidden.body)
	}
	invalid := h.do("POST", "/api/skills", map[string]any{"name": "Invalid", "slug": "BAD SLUG"}, owner)
	if invalid.status != http.StatusBadRequest || skillErrorCode(t, invalid) != "invalid_request" {
		t.Fatalf("invalid request: %d %s", invalid.status, invalid.body)
	}
}
