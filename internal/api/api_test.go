package api_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/api"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
)

type harness struct {
	t    *testing.T
	db   *sql.DB
	srv  *httptest.Server
	base string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := pgtest.FreshDB(t)

	// A discarded logger keeps failing-test output readable; the access log is
	// asserted on separately in production, not here.
	h := api.New(db, slog.New(slog.NewTextHandler(io.Discard, nil)),
		api.WithSecureCookies(false)).Handler()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return &harness{t: t, db: db, srv: srv, base: srv.URL}
}

type response struct {
	status int
	body   []byte
	cookie string
	header http.Header
}

func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("decode response %q: %v", string(r.body), err)
	}
	return out
}

func (h *harness) do(method, path string, body any, cookie string) response {
	h.t.Helper()

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, h.base+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: api.SessionCookie, Value: cookie})
	}

	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}

	res := response{status: resp.StatusCode, body: out, header: resp.Header}
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookie && c.Value != "" {
			res.cookie = c.Value
		}
	}
	return res
}

func (h *harness) register(email, password, tenantName, slug string) response {
	h.t.Helper()
	return h.do("POST", "/api/auth/register", map[string]any{
		"email":       email,
		"password":    password,
		"name":        "Test User",
		"tenant_name": tenantName,
		"tenant_slug": slug,
	}, "")
}

const goodPassword = "correct-horse-battery-staple"

func TestHealthz(t *testing.T) {
	h := newHarness(t)
	if res := h.do("GET", "/healthz", nil, ""); res.status != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", res.status)
	}
}

func TestRegisterThenMe(t *testing.T) {
	h := newHarness(t)

	res := h.register("owner@example.com", goodPassword, "Acme", "acme")
	if res.status != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", res.status, res.body)
	}
	if res.cookie == "" {
		t.Fatal("register did not set a session cookie")
	}

	body := res.json(t)
	user, _ := body["user"].(map[string]any)
	if user["email"] != "owner@example.com" {
		t.Errorf("user.email = %v", user["email"])
	}
	tenant, _ := body["tenant"].(map[string]any)
	if tenant["slug"] != "acme" || tenant["role"] != "owner" {
		t.Errorf("tenant = %v, want slug=acme role=owner", tenant)
	}

	me := h.do("GET", "/api/auth/me", nil, res.cookie)
	if me.status != http.StatusOK {
		t.Fatalf("me status = %d, body = %s", me.status, me.body)
	}
	if got := me.json(t)["user"].(map[string]any)["email"]; got != "owner@example.com" {
		t.Errorf("me email = %v", got)
	}

	// The password hash must never appear in a response body, even by accident.
	if strings.Contains(string(res.body), "$argon2id$") || strings.Contains(string(me.body), "$argon2id$") {
		t.Error("password hash leaked in an API response")
	}
}

func TestCookieIsHttpOnlyAndSameSite(t *testing.T) {
	h := newHarness(t)
	h.register("owner@example.com", goodPassword, "Acme", "acme")

	req, _ := http.NewRequest("POST", h.base+"/api/auth/login", strings.NewReader(
		`{"email":"owner@example.com","password":"`+goodPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var found *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookie {
			found = c
		}
	}
	if found == nil {
		t.Fatal("login did not set a cookie")
	}
	if !found.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if found.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", found.SameSite)
	}
	if found.Path != "/" {
		t.Errorf("Path = %q, want /", found.Path)
	}
}

func TestMeWithoutCookie(t *testing.T) {
	h := newHarness(t)
	res := h.do("GET", "/api/auth/me", nil, "")
	if res.status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.status)
	}
	if code := res.json(t)["error"].(map[string]any)["code"]; code != "unauthenticated" {
		t.Errorf("code = %v", code)
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	h := newHarness(t)
	h.register("owner@example.com", goodPassword, "Acme", "acme")

	wrongPassword := h.do("POST", "/api/auth/login", map[string]any{
		"email": "owner@example.com", "password": "not-the-right-password",
	}, "")
	unknownEmail := h.do("POST", "/api/auth/login", map[string]any{
		"email": "nobody@example.com", "password": "not-the-right-password",
	}, "")

	if wrongPassword.status != http.StatusUnauthorized || unknownEmail.status != http.StatusUnauthorized {
		t.Fatalf("statuses = %d and %d, want 401 for both", wrongPassword.status, unknownEmail.status)
	}
	// Byte-identical bodies: anything else is an account-enumeration oracle.
	if !bytes.Equal(wrongPassword.body, unknownEmail.body) {
		t.Errorf("bodies differ:\n wrong password: %s\n unknown email:  %s",
			wrongPassword.body, unknownEmail.body)
	}
	if wrongPassword.cookie != "" || unknownEmail.cookie != "" {
		t.Error("a failed login set a session cookie")
	}
}

func TestDuplicateTenantSlugConflicts(t *testing.T) {
	h := newHarness(t)
	h.register("a@example.com", goodPassword, "Acme", "acme")

	res := h.register("b@example.com", goodPassword, "Acme Two", "acme")
	if res.status != http.StatusConflict {
		t.Errorf("status = %d, want 409; body = %s", res.status, res.body)
	}
}

func TestWeakPasswordRejected(t *testing.T) {
	h := newHarness(t)
	res := h.register("a@example.com", "short", "Acme", "acme")
	if res.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.status)
	}
	if code := res.json(t)["error"].(map[string]any)["code"]; code != "weak_password" {
		t.Errorf("code = %v", code)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	h := newHarness(t)
	res := h.register("owner@example.com", goodPassword, "Acme", "acme")

	if out := h.do("POST", "/api/auth/logout", nil, res.cookie); out.status != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", out.status)
	}

	// The cookie is dead server-side, not merely cleared in the browser.
	if me := h.do("GET", "/api/auth/me", nil, res.cookie); me.status != http.StatusUnauthorized {
		t.Errorf("session still valid after logout: %d", me.status)
	}

	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d session rows remain after logout", n)
	}
}

func TestSwitchTenantRequiresMembership(t *testing.T) {
	h := newHarness(t)
	res := h.register("owner@example.com", goodPassword, "Acme", "acme")

	// A second tenant the user has nothing to do with.
	foreign := h.register("other@example.com", goodPassword, "Globex", "globex")
	foreignTenant := foreign.json(t)["tenant"].(map[string]any)["id"].(string)

	sw := h.do("POST", "/api/auth/switch-tenant", map[string]any{"tenant_id": foreignTenant}, res.cookie)
	if sw.status != http.StatusForbidden {
		t.Errorf("switch to foreign tenant status = %d, want 403", sw.status)
	}
}

func TestSwitchTenantToOwnSecondTenant(t *testing.T) {
	h := newHarness(t)
	res := h.register("owner@example.com", goodPassword, "Acme", "acme")
	userID := res.json(t)["user"].(map[string]any)["id"].(string)

	// Give the same user membership in a second tenant, as an invitation would.
	h.register("second@example.com", goodPassword, "Initech", "initech")
	var secondTenant string
	if err := h.db.QueryRow(`SELECT id FROM tenants WHERE slug = 'initech'`).Scan(&secondTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO tenant_memberships (id, tenant_id, user_id, role) VALUES ($1, $2, $3, 'member')`,
		"mem-test", secondTenant, userID); err != nil {
		t.Fatal(err)
	}

	sw := h.do("POST", "/api/auth/switch-tenant", map[string]any{"tenant_id": secondTenant}, res.cookie)
	if sw.status != http.StatusOK {
		t.Fatalf("switch status = %d, body = %s", sw.status, sw.body)
	}
	if sw.cookie == "" || sw.cookie == res.cookie {
		t.Error("switch did not issue a fresh session token")
	}

	// The new session must be scoped to the new tenant, with the role read from
	// the membership rather than carried over from the old one.
	tenant := sw.json(t)["tenant"].(map[string]any)
	if tenant["id"] != secondTenant {
		t.Errorf("active tenant = %v, want %v", tenant["id"], secondTenant)
	}
	if tenant["role"] != "member" {
		t.Errorf("role = %v, want member", tenant["role"])
	}

	// The old token is revoked, so a stolen copy does not outlive the switch.
	if me := h.do("GET", "/api/auth/me", nil, res.cookie); me.status != http.StatusUnauthorized {
		t.Errorf("old session still valid after switch: %d", me.status)
	}
}

func TestTwoTenantsIsolatedThroughHTTP(t *testing.T) {
	h := newHarness(t)

	a := h.register("a@example.com", goodPassword, "Acme", "acme")
	b := h.register("b@example.com", goodPassword, "Globex", "globex")

	ua := a.json(t)["user"].(map[string]any)["id"].(string)
	ub := b.json(t)["user"].(map[string]any)["id"].(string)
	if ua == ub {
		t.Fatal("two registrations produced one user")
	}

	// Each session reports exactly one tenant, its own.
	for _, tc := range []struct {
		name   string
		cookie string
		slug   string
	}{
		{"tenant A", a.cookie, "acme"},
		{"tenant B", b.cookie, "globex"},
	} {
		me := h.do("GET", "/api/auth/me", nil, tc.cookie)
		if me.status != http.StatusOK {
			t.Fatalf("%s me status = %d", tc.name, me.status)
		}
		body := me.json(t)
		tenant := body["tenant"].(map[string]any)
		if tenant["slug"] != tc.slug {
			t.Errorf("%s active tenant = %v, want %s", tc.name, tenant["slug"], tc.slug)
		}
		tenants := body["tenants"].([]any)
		if len(tenants) != 1 {
			t.Errorf("%s sees %d tenants, want 1", tc.name, len(tenants))
		}
	}
}

func TestUnknownJSONFieldRejected(t *testing.T) {
	h := newHarness(t)
	res := h.do("POST", "/api/auth/register", map[string]any{
		"email": "a@example.com", "password": goodPassword, "name": "A",
		"tenant_name": "Acme", "tenant_slug": "acme",
		"role": "owner", // not a client-settable field
	}, "")
	if res.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown field", res.status)
	}
}
