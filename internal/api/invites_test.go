package api_test

import (
	"net/http"
	"testing"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Invitations: the point is that a second person joins an existing tenant as the
// role they were invited with, and that the token is single-use.

func TestInviteAcceptCreatesMemberOfTheInvitedTenant(t *testing.T) {
	o := newOwnerHarness(t)
	tenantID := o.tenantID(o.ownerCookie)

	res := o.do("POST", "/api/invites", map[string]any{
		"email": "new@example.com", "role": "member",
	}, o.ownerCookie)
	if res.status != http.StatusCreated {
		t.Fatalf("create invite: %d %s", res.status, res.body)
	}
	created := res.json(t)
	token := created["token"].(string)
	invite := created["invite"].(map[string]any)
	if token == "" || invite["role"] != "member" {
		t.Fatalf("invite response = %s", res.body)
	}
	if invite["email"] != "new@example.com" {
		t.Fatalf("invite email = %v", invite["email"])
	}

	// The token is not stored in plaintext; only its digest reaches the database.
	var stored string
	if err := o.admin.QueryRow(`SELECT token_hash FROM tenant_invites WHERE id = $1`,
		invite["id"]).Scan(&stored); err != nil {
		t.Fatalf("read invite: %v", err)
	}
	if stored == token {
		t.Fatal("invite token stored in plaintext")
	}

	// Peek before accepting: shows the tenant and role, does not consume it.
	peek := o.do("GET", "/api/auth/invite?token="+token, nil, "")
	if peek.status != http.StatusOK {
		t.Fatalf("peek: %d %s", peek.status, peek.body)
	}
	if peek.json(t)["role"] != "member" || peek.json(t)["tenant_name"] != "Acme" {
		t.Fatalf("peek = %s", peek.body)
	}

	accept := o.do("POST", "/api/auth/accept-invite", map[string]any{
		"token": token, "password": goodPassword, "name": "New Person",
	}, "")
	if accept.status != http.StatusCreated {
		t.Fatalf("accept: %d %s", accept.status, accept.body)
	}
	body := accept.json(t)
	if body["tenant"].(map[string]any)["id"] != tenantID {
		t.Fatalf("joined wrong tenant: %s", accept.body)
	}
	if body["tenant"].(map[string]any)["role"] != "member" {
		t.Fatalf("role = %v; want member", body["tenant"].(map[string]any)["role"])
	}
	memberCookie := accept.cookie
	if memberCookie == "" {
		t.Fatal("accept did not start a session")
	}

	// The new member is visible to the owner, with the invited role.
	members := o.do("GET", "/api/tenant/members", nil, o.ownerCookie)
	if members.status != http.StatusOK {
		t.Fatalf("members: %d %s", members.status, members.body)
	}
	var found bool
	for _, raw := range members.json(t)["members"].([]any) {
		m := raw.(map[string]any)
		if m["email"] == "new@example.com" {
			found = true
			if m["role"] != "member" {
				t.Fatalf("member role = %v; want member", m["role"])
			}
		}
	}
	if !found {
		t.Fatalf("invited user is not a member: %s", members.body)
	}

	// A member can write work (FR-TN-8), unlike a guest.
	_, projectID := o.createProductProject(o.ownerCookie)
	if res := o.do("POST", "/api/items", map[string]any{
		"project_id": projectID, "kind": "task", "title": "member can write",
	}, memberCookie); res.status != http.StatusCreated {
		t.Fatalf("member create item: %d %s", res.status, res.body)
	}

	// Members do not see the invitation admin plane, and the invitation is gone.
	invites := o.do("GET", "/api/invites", nil, memberCookie)
	if invites.status != http.StatusOK || len(invites.json(t)["invites"].([]any)) != 0 {
		t.Fatalf("member invite list = %s", invites.body)
	}

	// The same token cannot be used twice.
	again := o.do("POST", "/api/auth/accept-invite", map[string]any{
		"token": token, "password": goodPassword, "name": "New Person",
	}, "")
	if again.status != http.StatusBadRequest {
		t.Fatalf("second accept: %d %s; want 400", again.status, again.body)
	}
}

func TestInviteRoleIsConstrainedAndDuplicatesRefused(t *testing.T) {
	o := newOwnerHarness(t)

	owner := o.do("POST", "/api/invites", map[string]any{
		"email": "boss@example.com", "role": "owner",
	}, o.ownerCookie)
	if owner.status != http.StatusBadRequest {
		t.Fatalf("owner invite: %d %s; want 400", owner.status, owner.body)
	}

	first := o.do("POST", "/api/invites", map[string]any{
		"email": "dup@example.com", "role": "member",
	}, o.ownerCookie)
	if first.status != http.StatusCreated {
		t.Fatalf("first invite: %d %s", first.status, first.body)
	}
	second := o.do("POST", "/api/invites", map[string]any{
		"email": "dup@example.com", "role": "admin",
	}, o.ownerCookie)
	if second.status != http.StatusConflict {
		t.Fatalf("duplicate invite: %d %s; want 409", second.status, second.body)
	}
}

func TestInviteRevokeAndExpiry(t *testing.T) {
	o := newOwnerHarness(t)

	created := o.do("POST", "/api/invites", map[string]any{
		"email": "revoked@example.com", "role": "member",
	}, o.ownerCookie)
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.body)
	}
	body := created.json(t)
	token := body["token"].(string)
	inviteID := body["invite"].(map[string]any)["id"].(string)

	if res := o.do("DELETE", "/api/invites/"+inviteID, nil, o.ownerCookie); res.status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", res.status, res.body)
	}
	if res := o.do("GET", "/api/auth/invite?token="+token, nil, ""); res.status != http.StatusNotFound {
		t.Fatalf("peek revoked: %d %s; want 404", res.status, res.body)
	}
	if res := o.do("POST", "/api/auth/accept-invite", map[string]any{
		"token": token, "password": goodPassword, "name": "Nope",
	}, ""); res.status != http.StatusBadRequest {
		t.Fatalf("accept revoked: %d %s; want 400", res.status, res.body)
	}

	// Expiry is evaluated by the database clock, so advancing the row is enough
	// to prove it is checked rather than assumed.
	expired := o.do("POST", "/api/invites", map[string]any{
		"email": "expired@example.com", "role": "member",
	}, o.ownerCookie)
	if expired.status != http.StatusCreated {
		t.Fatalf("create expiring invite: %d %s", expired.status, expired.body)
	}
	ebody := expired.json(t)
	expiredID := ebody["invite"].(map[string]any)["id"].(string)
	if _, err := o.admin.Exec(
		`UPDATE tenant_invites SET expires_at = now() - interval '1 day' WHERE id = $1`, expiredID); err != nil {
		t.Fatalf("expire invite: %v", err)
	}
	if res := o.do("GET", "/api/auth/invite?token="+ebody["token"].(string), nil, ""); res.status != http.StatusNotFound {
		t.Fatalf("peek expired: %d %s; want 404", res.status, res.body)
	}
	if res := o.do("POST", "/api/auth/accept-invite", map[string]any{
		"token": ebody["token"].(string), "password": goodPassword, "name": "Late",
	}, ""); res.status != http.StatusBadRequest {
		t.Fatalf("accept expired: %d %s; want 400", res.status, res.body)
	}
}

func TestGuestCannotInvite(t *testing.T) {
	o := newOwnerHarness(t)
	tenantID := o.tenantID(o.ownerCookie)
	o.addMember(tenantID, "guest3@example.com", tenancy.RoleGuest)
	guest := o.loginTo("guest3@example.com", tenantID)

	res := o.do("POST", "/api/invites", map[string]any{
		"email": "someone@example.com", "role": "member",
	}, guest)
	if res.status != http.StatusForbidden {
		t.Fatalf("guest invite: %d %s; want 403", res.status, res.body)
	}
}
