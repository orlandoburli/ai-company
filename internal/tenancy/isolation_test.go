package tenancy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The isolation test is the exit criterion for M0 and runs from the first
// milestone (FR-TN-1, mvp-design.md §10). It asserts three independent things:
//
//  1. the app role never sees another tenant's rows,
//  2. a query that forgets the tenant predicate returns zero rows rather than
//     everything (Postgres RLS as the backstop, not the only mechanism),
//  3. code reaching the store without a scope fails loudly.
//
// Each positive case is paired with a control, because "saw 0 rows" is also what
// a missing GRANT looks like — and a vacuous isolation test passes forever.

func TestRegisterTenantCreatesOwnerMembership(t *testing.T) {
	db := freshDB(t)
	s := tenancy.NewStore(db)

	id, m := register(t, s, "solo@example.com", "Solo", "Solo Co", "solo-co")
	if id.UserID == "" || m.TenantID == "" {
		t.Fatal("registration returned empty ids")
	}

	// The owner pool must be able to read this: it is the login path.
	var role string
	if err := db.QueryRow(
		`SELECT role FROM tenant_memberships WHERE user_id = $1`, id.UserID).Scan(&role); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if tenancy.Role(role) != tenancy.RoleOwner {
		t.Errorf("first registrant role = %q, want owner", role)
	}
}

func TestAppRoleSeesOnlyItsTenant(t *testing.T) {
	db := freshDB(t)
	s := tenancy.NewStore(db)

	_, a := register(t, s, "a@example.com", "A", "Tenant A", "tenant-a")
	bUser, b := register(t, s, "b@example.com", "B", "Tenant B", "tenant-b")
	if a.TenantID == b.TenantID {
		t.Fatalf("both registrations produced tenant %q", a.TenantID)
	}

	// Unscoped count per tenant: A must see exactly one membership, not two.
	cases := []struct {
		name   string
		tenant string
		want   int
	}{
		{"tenant A scope", a.TenantID, 1},
		{"tenant B scope", b.TenantID, 1},
		{"no scope (fails closed)", "", 0},
	}
	for _, c := range cases {
		conn := appConn(t, db, c.tenant)
		var n int
		if err := conn.QueryRowContext(context.Background(),
			`SELECT count(*) FROM tenant_memberships`).Scan(&n); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if n != c.want {
			t.Errorf("%s: saw %d memberships, want %d", c.name, n, c.want)
		}
	}

	// Targeted read: A cannot fetch B's membership by its primary key, yet can
	// still fetch its own. The second half is the control that proves the first
	// half is RLS and not a broken grant.
	conn := appConn(t, db, a.TenantID)
	var got int
	if err := conn.QueryRowContext(context.Background(),
		`SELECT count(*) FROM tenant_memberships WHERE user_id = $1`, bUser.UserID).Scan(&got); err != nil {
		t.Fatalf("read tenant B membership as tenant A: %v", err)
	}
	if got != 0 {
		t.Errorf("tenant A read %d of tenant B's memberships by user id, want 0", got)
	}

	if err := conn.QueryRowContext(context.Background(),
		`SELECT count(*) FROM tenant_memberships WHERE tenant_id = $1`, a.TenantID).Scan(&got); err != nil {
		t.Fatalf("control read of own tenant: %v", err)
	}
	if got != 1 {
		t.Fatalf("control failed: tenant A reads %d of its own memberships, want 1 (RLS may be over-blocking)", got)
	}
}

// TestRLSConfigured checks the schema itself, so a future migration cannot
// silently drop the policies while the behavioural tests above still pass by
// accident of GRANTs.
func TestRLSConfigured(t *testing.T) {
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
			t.Errorf("%s: RLS not enabled", table)
		}
		// FORCE is intentionally off for the identity plane (login must read
		// before a tenant is known). Asserted explicitly so turning it on is a
		// deliberate change with a failing test, not a silent login outage.
		if forced {
			t.Errorf("%s: RLS is FORCED; login resolution runs on the owner pool and would see zero rows", table)
		}

		var policies int
		if err := db.QueryRow(
			`SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = $1`,
			table).Scan(&policies); err != nil {
			t.Fatalf("%s policies: %v", table, err)
		}
		if policies == 0 {
			t.Errorf("%s: RLS enabled with no policy (denies everything, including legitimately scoped reads)", table)
		}
	}

	// The app role must not be able to read password hashes at all.
	var canReadHash bool
	err := db.QueryRow(`
		SELECT has_column_privilege('aic_app', 'users', 'password_hash', 'SELECT')`).Scan(&canReadHash)
	if err != nil {
		t.Fatalf("has_column_privilege: %v", err)
	}
	if canReadHash {
		t.Error("aic_app can SELECT users.password_hash; column grant is not in effect")
	}
}

// TestOwnerPoolResolvesIdentity records the identity-plane exception honestly:
// the owner pool sees both tenants' memberships, and that is required for login.
// It is a privilege of the connection, not a property of the schema.
func TestOwnerPoolResolvesIdentity(t *testing.T) {
	db := freshDB(t)
	s := tenancy.NewStore(db)
	register(t, s, "a@example.com", "A", "Tenant A", "tenant-a")
	register(t, s, "b@example.com", "B", "Tenant B", "tenant-b")

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM tenant_memberships`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("owner pool sees %d memberships, want 2; login resolution would be impossible", n)
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

// TestSessionRefusesForeignTenant covers the case a valid token would otherwise
// reach another tenant: minting a session for a tenant the user never joined.
func TestSessionRefusesForeignTenant(t *testing.T) {
	db := freshDB(t)
	s := tenancy.NewStore(db)

	aUser, a := register(t, s, "a@example.com", "A", "Tenant A", "tenant-a")
	_, b := register(t, s, "b@example.com", "B", "Tenant B", "tenant-b")
	ctx := context.Background()

	if _, err := s.CreateSession(ctx, aUser.UserID, b.TenantID, "tok-foreign", 0); err == nil {
		t.Error("minted a session for a tenant the user is not a member of")
	}
	if _, err := s.CreateSession(ctx, aUser.UserID, "nonexistent-tenant", "tok-ghost", 0); err == nil {
		t.Error("minted a session for a nonexistent tenant")
	}
	if _, err := s.CreateSession(ctx, aUser.UserID, a.TenantID, "tok-own", 0); err != nil {
		t.Errorf("control: could not mint a legitimate session: %v", err)
	}
}

// TestResolveSessionRejectsExpired guards the other half: an expired token must
// not resolve even when the membership is valid.
func TestResolveSessionRejectsExpired(t *testing.T) {
	db := freshDB(t)
	s := tenancy.NewStore(db)
	aUser, a := register(t, s, "a@example.com", "A", "Tenant A", "tenant-a")
	ctx := context.Background()

	// A negative ttl puts expires_at unambiguously in the past.
	if _, err := s.CreateSession(ctx, aUser.UserID, a.TenantID, "tok-expired", -time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveSession(ctx, "tok-expired"); err == nil {
		t.Error("expired session resolved")
	} else if !errors.Is(err, tenancy.ErrNotFound) {
		t.Errorf("expired session returned %v, want ErrNotFound", err)
	}

	if _, err := s.CreateSession(ctx, aUser.UserID, a.TenantID, "tok-later", time.Minute); err != nil {
		t.Fatal(err)
	}
	id, memberships, err := s.ResolveSession(ctx, "tok-later")
	if err != nil {
		t.Fatalf("live session did not resolve: %v", err)
	}
	if id.UserID != aUser.UserID {
		t.Errorf("resolved user %q, want %q", id.UserID, aUser.UserID)
	}
	if len(memberships) != 1 {
		t.Errorf("resolved %d memberships, want 1", len(memberships))
	}
}
