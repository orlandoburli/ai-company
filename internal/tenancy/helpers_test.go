package tenancy_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// freshDB is a per-test, migrated, disposable database (see internal/store/postgres/pgtest).
func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtest.FreshDB(t)
}

// appConn returns a connection impersonating the application role with a tenant
// scope applied — exactly what request-path code looks like.
//
// SET ROLE is used instead of a second credential so the test exercises the role
// the app will really use (non-owner, NOBYPASSRLS) without a password
// round-trip. It requires the DSN's user to be a superuser or a member of
// aic_app; the GRANT below makes that true once.
func appConn(t *testing.T, owner *sql.DB, tenantID string) *sql.Conn {
	t.Helper()
	ctx := context.Background()

	conn, err := owner.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}

	if _, err := conn.ExecContext(ctx, `SET ROLE aic_app`); err != nil {
		// Failure is fatal rather than skipped: a silently skipped isolation test
		// is worse than no isolation test.
		if _, gerr := conn.ExecContext(ctx, `GRANT aic_app TO CURRENT_USER`); gerr != nil {
			conn.Close()
			t.Fatalf("SET ROLE aic_app: %v (and GRANT failed: %v); connect as a superuser or a member of aic_app", err, gerr)
		}
		if _, err := conn.ExecContext(ctx, `SET ROLE aic_app`); err != nil {
			conn.Close()
			t.Fatalf("SET ROLE aic_app after grant: %v", err)
		}
	}

	// set_config is parameterisable, unlike SET. An empty value leaves the
	// policy's current_tenant_id() NULL, which is the "no scope" case.
	if _, err := conn.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, false)`, tenantID); err != nil {
		conn.Close()
		t.Fatalf("set tenant scope: %v", err)
	}

	// Session state survives a trip through the pool, so undo it or a later test
	// inherits a role and tenant it never asked for.
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, `RESET ROLE`)
		_, _ = conn.ExecContext(ctx, `SELECT set_config('app.tenant_id', '', false)`)
		conn.Close()
	})
	return conn
}

// register creates a tenant plus its owner user.
func register(t *testing.T, s *tenancy.Store, email, name, tenantName, slug string) (tenancy.Identity, tenancy.Membership) {
	t.Helper()
	id, m, err := s.RegisterTenant(context.Background(), email, "hash-"+email, name, tenantName, slug)
	if err != nil {
		t.Fatalf("register %s: %v", email, err)
	}
	return id, m
}
