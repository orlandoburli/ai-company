package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// addMember registers a second user (which creates their own tenant) and then
// adds them to the given tenant with an explicit role, the way an invitation
// would. It returns the new user's id.
func (h *harness) addMember(tenantID, email string, role tenancy.Role) string {
	h.t.Helper()

	local := strings.SplitN(email, "@", 2)[0]
	h.register(email, goodPassword, "Extra "+local, "extra-"+local)

	var userID string
	if err := h.admin.QueryRow(`SELECT id FROM users WHERE email = $1`, email).Scan(&userID); err != nil {
		h.t.Fatalf("find %s: %v", email, err)
	}
	if _, err := h.admin.Exec(
		`INSERT INTO tenant_memberships (id, tenant_id, user_id, role) VALUES ($1, $2, $3, $4)`,
		"m-"+userID, tenantID, userID, string(role)); err != nil {
		h.t.Fatalf("add membership: %v", err)
	}
	return userID
}

// loginTo signs in as an existing user, choosing a tenant explicitly (required
// once the user belongs to more than one).
func (h *harness) loginTo(email, tenantID string) string {
	h.t.Helper()
	res := h.do("POST", "/api/auth/login", map[string]any{
		"email": email, "password": goodPassword, "tenant_id": tenantID,
	}, "")
	if res.status != http.StatusOK {
		h.t.Fatalf("login %s: status %d body %s", email, res.status, res.body)
	}
	return res.cookie
}

func (h *harness) tenantID(cookie string) string {
	h.t.Helper()
	res := h.do("GET", "/api/tenant", nil, cookie)
	if res.status != http.StatusOK {
		h.t.Fatalf("get tenant: %d %s", res.status, res.body)
	}
	return res.json(h.t)["tenant"].(map[string]any)["id"].(string)
}

type ownerHarness struct {
	*harness
	ownerCookie string
}

func newOwnerHarness(t *testing.T) ownerHarness {
	t.Helper()
	h := newHarness(t)
	res := h.register("owner@example.com", goodPassword, "Acme", "acme")
	if res.status != http.StatusCreated {
		t.Fatalf("register: %d %s", res.status, res.body)
	}
	return ownerHarness{harness: h, ownerCookie: res.cookie}
}

// newOwnerHarnessFor wraps an existing harness (one built with custom options,
// e.g. rate-limit overrides) with a registered owner.
func newOwnerHarnessFor(t *testing.T, h *harness) ownerHarness {
	t.Helper()
	res := h.register("owner@example.com", goodPassword, "Acme", "acme")
	if res.status != http.StatusCreated {
		t.Fatalf("register: %d %s", res.status, res.body)
	}
	return ownerHarness{harness: h, ownerCookie: res.cookie}
}
