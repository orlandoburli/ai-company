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

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
)

type usageHTTPHarness struct {
	t   *testing.T
	db  *sql.DB
	srv *httptest.Server
}

func newUsageHTTPHarness(t *testing.T) *usageHTTPHarness {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSecureCookies(false))
	mux := http.NewServeMux()
	s.registerUsageRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &usageHTTPHarness{t: t, db: db.DB, srv: srv}
}

func (h *usageHTTPHarness) request(method, path string, body any, cookie string) (int, []byte, string) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
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
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	var session string
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			session = c.Value
		}
	}
	return resp.StatusCode, data, session
}

func TestUsageHTTPValidationAndOpenRead(t *testing.T) {
	h := newUsageHTTPHarness(t)
	status, _, _ := h.request("GET", "/api/usage", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous usage request status = %d, want 401", status)
	}
	status, body, cookie := h.request("POST", "/api/auth/register", map[string]any{
		"email": "usage@example.com", "password": "correct-horse-battery-staple", "name": "Usage User",
		"tenant_name": "Usage Tenant", "tenant_slug": "usage-tenant",
	}, "")
	if status != http.StatusCreated || cookie == "" {
		t.Fatalf("register: status %d body %s", status, body)
	}
	var ownerRegistration map[string]any
	if err := json.Unmarshal(body, &ownerRegistration); err != nil {
		t.Fatal(err)
	}
	tenantID := ownerRegistration["tenant"].(map[string]any)["id"].(string)

	status, body, _ = h.request("GET", "/api/usage?since=not-a-time", nil, cookie)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid timestamp status = %d body %s", status, body)
	}
	var invalid map[string]any
	if err := json.Unmarshal(body, &invalid); err != nil {
		t.Fatal(err)
	}
	if invalid["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("invalid timestamp response: %s", body)
	}

	status, body, _ = h.request("GET", "/api/usage?since=2025-01-01T00:00:00Z&until=2025-02-01T00:00:00Z", nil, cookie)
	if status != http.StatusOK {
		t.Fatalf("usage request: status %d body %s", status, body)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	var buckets, jobs []any
	if err := json.Unmarshal(response["usage"], &buckets); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(response["jobs"], &jobs); err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 0 || len(jobs) != 0 {
		t.Fatalf("empty usage response: %s", body)
	}

	// A guest membership can read usage too; the route is authenticated but has
	// no write-role capability gate.
	status, body, _ = h.request("POST", "/api/auth/register", map[string]any{
		"email": "guest@example.com", "password": "correct-horse-battery-staple", "name": "Guest",
		"tenant_name": "Guest Tenant", "tenant_slug": "guest-tenant",
	}, "")
	if status != http.StatusCreated {
		t.Fatalf("register guest: %d %s", status, body)
	}
	var guestRegistration map[string]any
	if err := json.Unmarshal(body, &guestRegistration); err != nil {
		t.Fatal(err)
	}
	guestID := guestRegistration["user"].(map[string]any)["id"].(string)
	if _, err := h.db.Exec(`INSERT INTO tenant_memberships (id, tenant_id, user_id, role) VALUES ($1,$2,$3,'guest')`,
		"usage-guest-membership", tenantID, guestID); err != nil {
		t.Fatal(err)
	}
	status, body, guestCookie := h.request("POST", "/api/auth/login", map[string]any{
		"email": "guest@example.com", "password": "correct-horse-battery-staple", "tenant_id": tenantID,
	}, "")
	if status != http.StatusOK || guestCookie == "" {
		t.Fatalf("login guest: %d %s", status, body)
	}
	status, body, _ = h.request("GET", "/api/usage", nil, guestCookie)
	if status != http.StatusOK {
		t.Fatalf("guest usage read: %d %s", status, body)
	}
}
