package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

func integrationAPIHarness(t *testing.T) *workflowHarness {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSecureCookies(false))
	mux := http.NewServeMux()
	s.registerIntegrationRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &workflowHarness{t: t, db: app, admin: db, srv: srv}
}

func TestIntegrationCredentialHTTPRolesIsolationAndRedaction(t *testing.T) {
	h := integrationAPIHarness(t)
	ownerCookie, _, tenantID := h.register("owner@example.com", "Acme", "acme")
	secretValue := "never-store-this-actual-secret"
	created := h.do("POST", "/api/integrations", map[string]any{"kind": "github", "secret_ref": "ai-company/github:orlandoburli", "label": "GitHub"}, ownerCookie)
	if created.status != http.StatusCreated {
		t.Fatalf("create integration: %d %s", created.status, created.body)
	}
	entry := created.json(t)["integration"].(map[string]any)
	if entry["kind"] != "github" || entry["secret_ref"] != "ai-company/github:orlandoburli" || entry["tenant_id"] != tenantID {
		t.Fatalf("unexpected integration response: %v", entry)
	}
	if strings.Contains(string(created.body), secretValue) {
		t.Fatal("create response disclosed a secret")
	}
	list := h.do("GET", "/api/integrations", nil, ownerCookie)
	if list.status != http.StatusOK || len(list.json(t)["integrations"].([]any)) != 1 {
		t.Fatalf("list integrations: %d %s", list.status, list.body)
	}
	if strings.Contains(string(list.body), secretValue) {
		t.Fatal("list response disclosed a secret")
	}
	if bad := h.do("POST", "/api/integrations", map[string]any{"kind": "unknown", "secret_ref": "a/b"}, ownerCookie); bad.status != http.StatusUnprocessableEntity || workflowErrCode(t, bad) != "invalid_request" {
		t.Fatalf("bad kind: %d %s", bad.status, bad.body)
	}
	if missing := h.do("POST", "/api/integrations", map[string]any{"kind": "jira", "secret_ref": ""}, ownerCookie); missing.status != http.StatusUnprocessableEntity {
		t.Fatalf("empty ref: %d %s", missing.status, missing.body)
	}

	adminCookie := h.addMember(tenantID, "admin@example.com", tenancy.RoleAdmin)
	if read := h.do("GET", "/api/integrations", nil, adminCookie); read.status != http.StatusOK {
		t.Fatalf("admin read: %d %s", read.status, read.body)
	}
	memberCookie := h.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	if read := h.do("GET", "/api/integrations", nil, memberCookie); read.status != http.StatusOK {
		t.Fatalf("member read: %d %s", read.status, read.body)
	}
	guestCookie := h.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)
	if read := h.do("GET", "/api/integrations", nil, guestCookie); read.status != http.StatusOK {
		t.Fatalf("guest read: %d %s", read.status, read.body)
	}
	if denied := h.do("POST", "/api/integrations", map[string]any{"kind": "jira", "secret_ref": "x/y"}, memberCookie); denied.status != http.StatusForbidden {
		t.Fatalf("member write: %d %s", denied.status, denied.body)
	}
	if denied := h.do("DELETE", "/api/integrations/"+entry["id"].(string), nil, guestCookie); denied.status != http.StatusForbidden {
		t.Fatalf("guest delete: %d %s", denied.status, denied.body)
	}
	updated := h.do("POST", "/api/integrations", map[string]any{"kind": "github", "secret_ref": "ai-company/new-account", "label": "Updated"}, adminCookie)
	if updated.status != http.StatusCreated || updated.json(t)["integration"].(map[string]any)["label"] != "Updated" {
		t.Fatalf("admin create/update: %d %s", updated.status, updated.body)
	}

	foreignCookie, _, _ := h.register("foreign@example.com", "Globex", "globex")
	foreign := h.do("POST", "/api/integrations", map[string]any{"kind": "github", "secret_ref": "foreign/service", "label": "Foreign"}, foreignCookie)
	if foreign.status != http.StatusCreated {
		t.Fatalf("foreign create: %d %s", foreign.status, foreign.body)
	}
	foreignID := foreign.json(t)["integration"].(map[string]any)["id"].(string)
	if res := h.do("DELETE", "/api/integrations/"+foreignID, nil, ownerCookie); res.status != http.StatusNotFound || workflowErrCode(t, res) != "integration_not_found" {
		t.Fatalf("cross tenant delete: %d %s", res.status, res.body)
	}
	id := entry["id"].(string)
	if removed := h.do("DELETE", "/api/integrations/"+id, nil, ownerCookie); removed.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", removed.status, removed.body)
	}
	if gone := h.do("DELETE", "/api/integrations/"+id, nil, ownerCookie); gone.status != http.StatusNotFound || workflowErrCode(t, gone) != "integration_not_found" {
		t.Fatalf("missing delete: %d %s", gone.status, gone.body)
	}
}
