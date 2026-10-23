package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

func newCommandAPIHarness(t *testing.T) *workflowHarness {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSecureCookies(false))
	mux := http.NewServeMux()
	s.registerCommandRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &workflowHarness{t: t, db: app, admin: db, srv: srv}
}

func TestCommandChannelAPIRoleGatesAndTenantIsolation(t *testing.T) {
	h := newCommandAPIHarness(t)
	owner, _, tenantID := h.register("command-owner@example.com", "Command tenant", "command-tenant")
	created := h.do(http.MethodPost, "/api/notification-channels", map[string]any{"name": "Desktop", "command_template": "/usr/bin/notify-send {{title}}", "events": []string{"checkpoint_pending"}}, owner)
	if created.status != http.StatusCreated {
		t.Fatalf("create channel: %d %s", created.status, created.body)
	}
	item := created.json(t)["channel"].(map[string]any)
	id := item["id"].(string)
	if _, ok := item["command_template"]; !ok {
		t.Fatalf("template omitted: %v", item)
	}
	member := h.addMember(tenantID, "member-command@example.com", tenancy.RoleMember)
	guest := h.addMember(tenantID, "guest-command@example.com", tenancy.RoleGuest)
	for _, cookie := range []string{member, guest} {
		if got := h.do(http.MethodGet, "/api/notification-channels", nil, cookie); got.status != http.StatusOK {
			t.Fatalf("read channel: %d %s", got.status, got.body)
		}
	}
	if got := h.do(http.MethodPost, "/api/notification-channels", map[string]any{"name": "bad", "command_template": "/bin/echo", "events": []string{"run_failed"}}, member); got.status != http.StatusForbidden {
		t.Fatalf("member created arbitrary code: %d %s", got.status, got.body)
	}
	if got := h.do(http.MethodPatch, "/api/notification-channels/"+id, map[string]any{"enabled": false}, guest); got.status != http.StatusForbidden {
		t.Fatalf("guest patched channel: %d %s", got.status, got.body)
	}
	if got := h.do(http.MethodPatch, "/api/notification-channels/"+id, map[string]any{"enabled": false}, owner); got.status != http.StatusOK {
		t.Fatalf("owner patch: %d %s", got.status, got.body)
	}
	foreign, _, _ := h.register("command-foreign@example.com", "Foreign", "command-foreign")
	foreignChannel := h.do(http.MethodPost, "/api/notification-channels", map[string]any{"name": "Foreign", "command_template": "/bin/echo", "events": []string{"run_failed"}}, foreign)
	foreignID := foreignChannel.json(t)["channel"].(map[string]any)["id"].(string)
	if got := h.do(http.MethodDelete, "/api/notification-channels/"+foreignID, nil, owner); got.status != http.StatusNotFound {
		t.Fatalf("cross-tenant delete: %d %s", got.status, got.body)
	}
}
