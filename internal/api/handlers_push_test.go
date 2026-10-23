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

func newPushAPIHarness(t *testing.T) *workflowHarness {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSecureCookies(false))
	mux := http.NewServeMux()
	s.registerPushRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &workflowHarness{t: t, db: app, admin: db, srv: srv}
}

func TestPushSubscriptionAPIRolesAndTenantNotFound(t *testing.T) {
	h := newPushAPIHarness(t)
	owner, _, tenantID := h.register("push-owner@example.com", "Push tenant", "push-tenant")
	created := h.do(http.MethodPost, "/api/push/subscriptions", map[string]any{"endpoint": "https://push.example/own", "expirationTime": nil, "keys": map[string]string{"p256dh": "browser-key", "auth": "auth-key"}}, owner)
	if created.status != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", created.status, created.body)
	}
	item := created.json(t)["subscription"].(map[string]any)
	id := item["id"].(string)
	if item["user_id"] == nil {
		t.Fatalf("missing user-bound subscription: %v", item)
	}
	member := h.addMember(tenantID, "member-push@example.com", tenancy.RoleMember)
	guest := h.addMember(tenantID, "guest-push@example.com", tenancy.RoleGuest)
	if got := h.do(http.MethodPost, "/api/push/subscriptions", map[string]any{"endpoint": "https://push.example/member", "p256dh": "k", "auth": "a"}, member); got.status != http.StatusCreated {
		t.Fatalf("member write: %d %s", got.status, got.body)
	}
	if got := h.do(http.MethodGet, "/api/push/subscriptions", nil, guest); got.status != http.StatusOK {
		t.Fatalf("guest read: %d %s", got.status, got.body)
	}
	if denied := h.do(http.MethodPost, "/api/push/subscriptions", map[string]any{"endpoint": "https://push.example/guest", "p256dh": "k", "auth": "a"}, guest); denied.status != http.StatusForbidden {
		t.Fatalf("guest write: %d %s", denied.status, denied.body)
	}
	foreign, _, _ := h.register("push-foreign@example.com", "Foreign", "push-foreign")
	foreignSub := h.do(http.MethodPost, "/api/push/subscriptions", map[string]any{"endpoint": "https://push.example/foreign", "p256dh": "k", "auth": "a"}, foreign)
	foreignID := foreignSub.json(t)["subscription"].(map[string]any)["id"].(string)
	if got := h.do(http.MethodDelete, "/api/push/subscriptions/"+foreignID, nil, owner); got.status != http.StatusNotFound {
		t.Fatalf("cross-tenant delete: %d %s", got.status, got.body)
	}
	list := h.do(http.MethodGet, "/api/push/subscriptions", nil, owner)
	if list.status != http.StatusOK || len(list.json(t)["subscriptions"].([]any)) != 1 {
		t.Fatalf("own-only listing: %d %s", list.status, list.body)
	}
	if got := h.do(http.MethodDelete, "/api/push/subscriptions/"+id, nil, owner); got.status != http.StatusNoContent {
		t.Fatalf("unsubscribe: %d %s", got.status, got.body)
	}
}
