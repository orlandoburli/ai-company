package tenancy_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The isolation tests are the exit criterion for M0 (FR-TN-1, mvp-design.md §10).
// They are deliberately written against the *application* role, because a test
// that runs as the owner proves nothing about what the app can reach.
//
// The guarantee is layered, and each layer is asserted separately:
//
//  1. the app role has no table privileges on the identity plane at all,
//  2. tenant-scoped tables are ENABLE + FORCE ROW LEVEL SECURITY, so even a
//     non-superuser owner is bound by policy,
//  3. access is possible only through SECURITY DEFINER functions whose
//     parameters cannot name another tenant's data,
//  4. code reaching the store without a scope fails loudly.

// TestAppRoleHasNoIdentityTableAccess is the primary assertion. It is stronger
// than "the policy filtered the rows": with no table privilege there is no query
// shape, forgotten predicate or new code path that can read across tenants
// directly.
func TestAppRoleHasNoIdentityTableAccess(t *testing.T) {
	db := freshDB(t)
	app := db.AsRole(t, "aic_app")

	for _, table := range []string{"tenants", "users", "tenant_memberships", "sessions"} {
		var n int
		err := app.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
		if err == nil {
			t.Errorf("aic_app could read %s (%d rows); it must have no table privilege on the identity plane", table, n)
			continue
		}
		if !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: expected permission denied, got %v", table, err)
		}

		// Writes are refused too, so an INSERT cannot be used as an oracle.
		if _, err := app.Exec(`INSERT INTO ` + table + ` DEFAULT VALUES`); err == nil {
			t.Errorf("aic_app could write %s", table)
		}
	}
}

// TestRLSIsForcedOnTenantTables checks the schema itself, so a later migration
// cannot quietly downgrade to ENABLE while the behavioural tests keep passing.
func TestRLSIsForcedOnTenantTables(t *testing.T) {
	db := freshDB(t)

	for _, table := range []string{"tenant_memberships", "sessions"} {
		var enabled, forced bool
		err := db.QueryRow(`
			SELECT relrowsecurity, relforcerowsecurity
			  FROM pg_class
			 WHERE oid = $1::regclass`, "public."+table).Scan(&enabled, &forced)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if !enabled {
			t.Errorf("%s: RLS is not enabled", table)
		}
		if !forced {
			t.Errorf("%s: RLS is not FORCED; a non-superuser owner would bypass every policy", table)
		}

		var policies int
		if err := db.QueryRow(
			`SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = $1`,
			table).Scan(&policies); err != nil {
			t.Fatalf("%s policies: %v", table, err)
		}
		if policies == 0 {
			t.Errorf("%s: RLS enabled with no policy", table)
		}
	}
}

// TestDefinerRoleIsClosed pins the properties that make the function surface a
// controlled exception rather than a second superuser. A BYPASSRLS role that can
// log in is a database-wide bypass with a password: it must never exist.
func TestDefinerRoleIsClosed(t *testing.T) {
	db := freshDB(t)

	var canLogin, bypassRLS, isSuper bool
	err := db.QueryRow(`
		SELECT rolcanlogin, rolbypassrls, rolsuper
		  FROM pg_roles WHERE rolname = 'aic_auth'`).Scan(&canLogin, &bypassRLS, &isSuper)
	if err != nil {
		t.Fatalf("read aic_auth: %v", err)
	}
	if canLogin {
		t.Error("aic_auth can log in; the privileged role must be unreachable as a credential")
	}
	if !bypassRLS {
		t.Error("aic_auth lacks BYPASSRLS; its functions would be bound by policies and unable to resolve sessions")
	}
	if isSuper {
		t.Error("aic_auth is a superuser; ownership of a few functions is the intended privilege, not the whole cluster")
	}

	// Functions default to EXECUTE for PUBLIC; if that were left in place every
	// role would inherit the privileged surface.
	var publicExecute bool
	if err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM pg_proc p
			  JOIN pg_namespace n ON n.oid = p.pronamespace
			 WHERE n.nspname = 'public'
			   AND p.proname LIKE 'auth_%'
			   AND has_function_privilege('public', p.oid, 'EXECUTE')
		)`).Scan(&publicExecute); err != nil {
		t.Fatalf("check PUBLIC execute: %v", err)
	}
	if publicExecute {
		t.Error("a public.auth_* function is executable by PUBLIC; revoke EXECUTE before granting to aic_app")
	}
}

// TestRegisterTenantCreatesOwnerMembership covers the intended path end to end
// through the function surface.
func TestRegisterTenantCreatesOwnerMembership(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)

	id, m := register(t, s, "solo@example.com", "Solo", "Solo Co", "solo-co")
	if id.UserID == "" || m.TenantID == "" || id.TenantID != m.TenantID {
		t.Fatalf("inconsistent registration result: %+v / %+v", id, m)
	}
	if id.Role != tenancy.RoleOwner {
		t.Errorf("registered identity role = %q, want owner", id.Role)
	}

	// Verified through the owner connection, which can see the row the app
	// created but could not read back.
	var role, hash string
	if err := db.QueryRow(
		`SELECT role FROM tenant_memberships WHERE user_id = $1`, id.UserID).Scan(&role); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if tenancy.Role(role) != tenancy.RoleOwner {
		t.Errorf("stored role = %q, want owner", role)
	}
	if err := db.QueryRow(
		`SELECT password_hash FROM users WHERE id = $1`, id.UserID).Scan(&hash); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if hash != "hash-solo@example.com" {
		t.Errorf("stored hash = %q; registration must persist the hash it was given", hash)
	}
}

// TestTwoTenantsIsolatedThroughStore exercises the real request path: two
// tenants, two stores over the app role, no cross-visibility.
func TestTwoTenantsIsolatedThroughStore(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)
	ctx := context.Background()

	aID, a := register(t, s, "a@example.com", "A", "Tenant A", "tenant-a")
	bID, b := register(t, s, "b@example.com", "B", "Tenant B", "tenant-b")
	if a.TenantID == b.TenantID {
		t.Fatalf("both registrations produced tenant %q", a.TenantID)
	}

	// Each session sees exactly its own tenant.
	for _, tc := range []struct {
		name   string
		user   string
		tenant string
	}{
		{"A", aID.UserID, a.TenantID},
		{"B", bID.UserID, b.TenantID},
	} {
		if _, err := s.CreateSession(ctx, tc.user, tc.tenant, "tok-"+tc.name, time.Hour); err != nil {
			t.Fatalf("%s: create session: %v", tc.name, err)
		}
		id, memberships, err := s.ResolveSession(ctx, "tok-"+tc.name)
		if err != nil {
			t.Fatalf("%s: resolve: %v", tc.name, err)
		}
		if id.TenantID != tc.tenant {
			t.Errorf("%s resolved tenant %q, want %q", tc.name, id.TenantID, tc.tenant)
		}
		if len(memberships) != 1 {
			t.Errorf("%s sees %d memberships, want 1", tc.name, len(memberships))
		}
	}

	// And a session cannot be minted across the boundary, even though the
	// function takes both ids as parameters.
	if _, err := s.CreateSession(ctx, aID.UserID, b.TenantID, "tok-cross", time.Hour); !errors.Is(err, tenancy.ErrNotFound) {
		t.Errorf("cross-tenant session mint = %v, want ErrNotFound", err)
	}
	if n := countAsOwner(t, db.DB, "sessions"); n != 2 {
		t.Errorf("%d session rows exist, want 2 (the refused mint must not leave a row)", n)
	}
}

func TestScopeRequired(t *testing.T) {
	if _, err := tenancy.Require(context.Background()); err == nil {
		t.Fatal("expected ErrNoScope for a context with no scope")
	}

	ok := tenancy.Scope{TenantID: "t1", UserID: "u1", Role: tenancy.RoleMember}
	if _, err := tenancy.Require(tenancy.WithScope(context.Background(), ok)); err != nil {
		t.Errorf("valid scope rejected: %v", err)
	}

	// A typo must not silently widen access.
	bad := tenancy.Scope{TenantID: "t1", UserID: "u1", Role: tenancy.Role("adminstrator")}
	if _, err := tenancy.Require(tenancy.WithScope(context.Background(), bad)); err == nil {
		t.Error("unknown role accepted")
	}

	empty := tenancy.Scope{TenantID: "  ", UserID: "u1", Role: tenancy.RoleMember}
	if _, err := tenancy.Require(tenancy.WithScope(context.Background(), empty)); err == nil {
		t.Error("empty tenant id accepted")
	}
}

func TestRoleCapabilities(t *testing.T) {
	cases := []struct {
		role       tenancy.Role
		decide     bool
		manage     bool
		ownsTenant bool
	}{
		{tenancy.RoleOwner, true, true, true},
		{tenancy.RoleAdmin, true, true, false},
		{tenancy.RoleMember, true, false, false},
		{tenancy.RoleGuest, false, false, false},
	}
	for _, c := range cases {
		if c.role.CanDecideCheckpoints() != c.decide {
			t.Errorf("%s CanDecideCheckpoints = %v, want %v", c.role, !c.decide, c.decide)
		}
		if c.role.CanManageSettings() != c.manage {
			t.Errorf("%s CanManageSettings = %v, want %v", c.role, !c.manage, c.manage)
		}
		if c.role.IsOwner() != c.ownsTenant {
			t.Errorf("%s IsOwner = %v, want %v", c.role, !c.ownsTenant, c.ownsTenant)
		}
	}
}

// TestSessionRefusesForeignTenant covers the token-pairing path: a valid
// credential must not be usable against a tenant the user never joined.
func TestSessionRefusesForeignTenant(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)
	ctx := context.Background()

	aUser, _ := register(t, s, "a@example.com", "A", "Tenant A", "tenant-a")
	_, b := register(t, s, "b@example.com", "B", "Tenant B", "tenant-b")

	if _, err := s.CreateSession(ctx, aUser.UserID, b.TenantID, "tok-foreign", 0); !errors.Is(err, tenancy.ErrNotFound) {
		t.Errorf("foreign tenant = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateSession(ctx, aUser.UserID, "nonexistent-tenant", "tok-ghost", 0); !errors.Is(err, tenancy.ErrNotFound) {
		t.Errorf("nonexistent tenant = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateSession(ctx, aUser.UserID, aUser.TenantID, "tok-own", time.Hour); err != nil {
		t.Errorf("control: legitimate session refused: %v", err)
	}
}

// TestResolveSessionRejectsExpired confirms expiry is evaluated by the database,
// not by a Go-side comparison against a possibly skewed host clock.
func TestResolveSessionRejectsExpired(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)
	ctx := context.Background()

	a, _ := register(t, s, "a@example.com", "A", "Tenant A", "tenant-a")

	if _, err := s.CreateSession(ctx, a.UserID, a.TenantID, "tok-expired", -time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveSession(ctx, "tok-expired"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Errorf("expired session = %v, want ErrNotFound", err)
	}

	if _, err := s.CreateSession(ctx, a.UserID, a.TenantID, "tok-live", time.Hour); err != nil {
		t.Fatal(err)
	}
	id, memberships, err := s.ResolveSession(ctx, "tok-live")
	if err != nil {
		t.Fatalf("live session did not resolve: %v", err)
	}
	if id.UserID != a.UserID || id.TenantID != a.TenantID {
		t.Errorf("resolved %+v, want user %s tenant %s", id, a.UserID, a.TenantID)
	}
	if len(memberships) != 1 {
		t.Errorf("resolved %d memberships, want 1", len(memberships))
	}
}

// TestLogoutDeletesRow covers revocation: after deleting the session the token
// must not resolve, and the row must be gone rather than merely expired.
func TestLogoutDeletesRow(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)
	ctx := context.Background()

	a, _ := register(t, s, "a@example.com", "A", "Tenant A", "tenant-a")
	if _, err := s.CreateSession(ctx, a.UserID, a.TenantID, "tok-bye", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "tok-bye"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := s.ResolveSession(ctx, "tok-bye"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Errorf("revoked session still resolves: %v", err)
	}
	if n := countAsOwner(t, db.DB, "sessions"); n != 0 {
		t.Errorf("%d session rows remain after logout", n)
	}
}

// TestFunctionLevelAuthorization is the defence-in-depth assertion: even if the
// HTTP route gate were removed or bypassed, the database must still refuse a
// member who tries to change tenant settings or other people's roles. Without
// this, "authorisation is enforced twice" would be a comment rather than a fact.
func TestFunctionLevelAuthorization(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)
	ctx := context.Background()

	owner, m := register(t, s, "owner@example.com", "Owner", "Acme", "acme")

	// A second user, invited as a plain member of the owner's tenant.
	second, _ := register(t, s, "member@example.com", "Member", "Their Own Co", "their-own-co")
	if _, err := db.Exec(
		`INSERT INTO tenant_memberships (id, tenant_id, user_id, role) VALUES ($1, $2, $3, 'member')`,
		"invite-1", m.TenantID, second.UserID); err != nil {
		t.Fatalf("invite: %v", err)
	}

	if _, err := s.CreateSession(ctx, second.UserID, m.TenantID, "tok-member", time.Hour); err != nil {
		t.Fatalf("member session: %v", err)
	}
	if _, err := s.CreateSession(ctx, owner.UserID, m.TenantID, "tok-owner", time.Hour); err != nil {
		t.Fatalf("owner session: %v", err)
	}

	// The member cannot rename the tenant, even calling the function directly.
	if err := s.UpdateTenant(ctx, "tok-member", "Hijacked", ""); !errors.Is(err, tenancy.ErrForbiddenRole) {
		t.Errorf("member UpdateTenant = %v, want ErrForbiddenRole", err)
	}
	// Nor change anyone's role.
	if err := s.SetMemberRole(ctx, "tok-member", second.UserID, tenancy.RoleAdmin); !errors.Is(err, tenancy.ErrForbiddenRole) {
		t.Errorf("member SetMemberRole = %v, want ErrForbiddenRole", err)
	}

	// The owner can, which is the control proving the refusals above are
	// authorisation and not a broken function.
	if err := s.UpdateTenant(ctx, "tok-owner", "Acme Renamed", ""); err != nil {
		t.Errorf("owner UpdateTenant: %v", err)
	}
	if err := s.SetMemberRole(ctx, "tok-owner", second.UserID, tenancy.RoleAdmin); err != nil {
		t.Errorf("owner SetMemberRole: %v", err)
	}

	// A token that was never issued cannot reach the functions either: the tenant
	// is derived from the session, so there is nothing to point at.
	if err := s.UpdateTenant(ctx, "tok-forged", "Nope", ""); !errors.Is(err, tenancy.ErrUnauthenticated) {
		t.Errorf("forged token = %v, want ErrUnauthenticated", err)
	}
}
