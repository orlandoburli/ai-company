package api_test

import (
	"net/http"
	"testing"
)

// TestTenantSettingsRequireOwnerOrAdmin is the M0 exit criterion: a non-owner
// must be rejected from tenant settings.
func TestTenantSettingsRequireOwnerOrAdmin(t *testing.T) {
	oh := newOwnerHarness(t)
	h, owner := oh.harness, oh.ownerCookie
	tenant := h.tenantID(owner)

	h.addMember(tenant, "member@example.com", "member")
	memberCookie := h.loginTo("member@example.com", tenant)

	h.addMember(tenant, "guest@example.com", "guest")
	guestCookie := h.loginTo("guest@example.com", tenant)

	// Owner succeeds and the change is persisted, not merely echoed.
	res := h.do("PATCH", "/api/tenant", map[string]any{
		"name": "Acme Robotics", "slug": "acme-robotics",
	}, owner)
	if res.status != http.StatusOK {
		t.Fatalf("owner update: %d %s", res.status, res.body)
	}
	after := h.do("GET", "/api/tenant", nil, owner).json(t)["tenant"].(map[string]any)
	if after["name"] != "Acme Robotics" || after["slug"] != "acme-robotics" {
		t.Errorf("tenant after update = %v", after)
	}

	// Member and guest are both refused, by the route gate and by the function.
	for _, tc := range []struct {
		name   string
		cookie string
	}{
		{"member", memberCookie},
		{"guest", guestCookie},
	} {
		out := h.do("PATCH", "/api/tenant", map[string]any{"name": "Hijacked"}, tc.cookie)
		if out.status != http.StatusForbidden {
			t.Errorf("%s update status = %d, want 403", tc.name, out.status)
		}
	}

	// And the refused change did not land.
	final := h.do("GET", "/api/tenant", nil, owner).json(t)["tenant"].(map[string]any)
	if final["name"] != "Acme Robotics" {
		t.Errorf("refused update was applied: name = %v", final["name"])
	}
}

// TestRoleChangeTakesEffectOnNextRequest verifies the role is read from the
// membership per request rather than carried in the session token.
func TestRoleChangeTakesEffectOnNextRequest(t *testing.T) {
	oh := newOwnerHarness(t)
	h, owner := oh.harness, oh.ownerCookie
	tenant := h.tenantID(owner)

	h.addMember(tenant, "promote@example.com", "member")

	// The member cannot manage settings.
	memberCookie := h.loginTo("promote@example.com", tenant)
	if res := h.do("PATCH", "/api/tenant", map[string]any{"name": "X"}, memberCookie); res.status != http.StatusForbidden {
		t.Fatalf("member update = %d, want 403", res.status)
	}

	// Promote them; the *existing* session gains the permission immediately.
	var target string
	if err := h.admin.QueryRow(`SELECT id FROM users WHERE email = 'promote@example.com'`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if res := h.do("PATCH", "/api/tenant/members/"+target, map[string]any{"role": "admin"}, owner); res.status != http.StatusOK {
		t.Fatalf("promote: %d %s", res.status, res.body)
	}

	if res := h.do("PATCH", "/api/tenant", map[string]any{"name": "Acme Two"}, memberCookie); res.status != http.StatusOK {
		t.Errorf("promoted admin still refused: %d %s", res.status, res.body)
	}
}

func TestMemberList(t *testing.T) {
	oh := newOwnerHarness(t)
	h, owner := oh.harness, oh.ownerCookie
	tenant := h.tenantID(owner)
	h.addMember(tenant, "member@example.com", "member")

	res := h.do("GET", "/api/tenant/members", nil, owner)
	if res.status != http.StatusOK {
		t.Fatalf("members: %d %s", res.status, res.body)
	}
	members := res.json(t)["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2", len(members))
	}

	// A plain member can read the list too (names and roles are tenant-visible),
	// but sees exactly the same tenant's members.
	memberCookie := h.loginTo("member@example.com", tenant)
	asMember := h.do("GET", "/api/tenant/members", nil, memberCookie)
	if asMember.status != http.StatusOK {
		t.Fatalf("member list as member: %d", asMember.status)
	}
	if got := len(asMember.json(t)["members"].([]any)); got != 2 {
		t.Errorf("member sees %d members, want 2", got)
	}
}

// TestRoleChangeInvariants covers the three lockout/privilege rules.
func TestRoleChangeInvariants(t *testing.T) {
	oh := newOwnerHarness(t)
	h, owner := oh.harness, oh.ownerCookie
	tenant := h.tenantID(owner)

	h.addMember(tenant, "admin@example.com", "admin")
	adminCookie := h.loginTo("admin@example.com", tenant)

	h.addMember(tenant, "member@example.com", "member")
	var memberID string
	if err := h.admin.QueryRow(`SELECT id FROM users WHERE email = 'member@example.com'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}

	// An admin cannot mint another owner: that would let them manufacture their
	// own privilege level.
	res := h.do("PATCH", "/api/tenant/members/"+memberID, map[string]any{"role": "owner"}, adminCookie)
	if res.status != http.StatusForbidden {
		t.Errorf("admin granting owner = %d, want 403", res.status)
	}

	// ...but can change member <-> guest.
	if res := h.do("PATCH", "/api/tenant/members/"+memberID, map[string]any{"role": "guest"}, adminCookie); res.status != http.StatusOK {
		t.Errorf("admin demoting member = %d, want 200", res.status)
	}

	// Self-change is refused, which is what stops both accidental self-demotion
	// and self-promotion.
	var ownerID string
	if err := h.admin.QueryRow(`SELECT id FROM users WHERE email = 'owner@example.com'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	res = h.do("PATCH", "/api/tenant/members/"+ownerID, map[string]any{"role": "member"}, owner)
	if res.status != http.StatusConflict {
		t.Errorf("self role change = %d, want 409", res.status)
	}
	if code := res.json(t)["error"].(map[string]any)["code"]; code != "cannot_change_own_role" {
		t.Errorf("code = %v", code)
	}
}

// TestLastOwnerCannotBeDemoted reaches the invariant the way it can actually
// happen: an admin demoting the sole remaining owner.
func TestLastOwnerCannotBeDemoted(t *testing.T) {
	oh := newOwnerHarness(t)
	h, owner := oh.harness, oh.ownerCookie
	tenant := h.tenantID(owner)

	h.addMember(tenant, "second@example.com", "member")
	var secondID, ownerID string
	if err := h.admin.QueryRow(`SELECT id FROM users WHERE email = 'second@example.com'`).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	if err := h.admin.QueryRow(`SELECT id FROM users WHERE email = 'owner@example.com'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}

	// Owner promotes second to owner, who then demotes the original owner to
	// admin. Now there is exactly one owner.
	if res := h.do("PATCH", "/api/tenant/members/"+secondID, map[string]any{"role": "owner"}, owner); res.status != http.StatusOK {
		t.Fatalf("promote second: %d %s", res.status, res.body)
	}
	secondCookie := h.loginTo("second@example.com", tenant)
	if res := h.do("PATCH", "/api/tenant/members/"+ownerID, map[string]any{"role": "admin"}, secondCookie); res.status != http.StatusOK {
		t.Fatalf("demote original owner: %d %s", res.status, res.body)
	}

	// The original owner is now an admin; demoting the only owner must fail.
	adminCookie := h.loginTo("owner@example.com", tenant)
	res := h.do("PATCH", "/api/tenant/members/"+secondID, map[string]any{"role": "member"}, adminCookie)
	if res.status != http.StatusConflict {
		t.Fatalf("demote last owner = %d, want 409; body %s", res.status, res.body)
	}
	if code := res.json(t)["error"].(map[string]any)["code"]; code != "last_owner" {
		t.Errorf("code = %v, want last_owner", code)
	}

	var owners int
	if err := h.admin.QueryRow(
		`SELECT count(*) FROM tenant_memberships WHERE tenant_id = $1 AND role = 'owner'`, tenant).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != 1 {
		t.Errorf("%d owners remain, want 1", owners)
	}
}

func TestRoleChangeValidation(t *testing.T) {
	oh := newOwnerHarness(t)
	h, owner := oh.harness, oh.ownerCookie
	tenant := h.tenantID(owner)
	h.addMember(tenant, "m@example.com", "member")

	var id string
	if err := h.admin.QueryRow(`SELECT id FROM users WHERE email = 'm@example.com'`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	if res := h.do("PATCH", "/api/tenant/members/"+id, map[string]any{"role": "superuser"}, owner); res.status != http.StatusBadRequest {
		t.Errorf("invalid role = %d, want 400", res.status)
	}
	if res := h.do("PATCH", "/api/tenant/members/does-not-exist", map[string]any{"role": "member"}, owner); res.status != http.StatusNotFound {
		t.Errorf("unknown member = %d, want 404", res.status)
	}
	if res := h.do("PATCH", "/api/tenant", map[string]any{}, owner); res.status != http.StatusBadRequest {
		t.Errorf("empty update = %d, want 400", res.status)
	}
}
