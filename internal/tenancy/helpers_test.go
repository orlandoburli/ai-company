package tenancy_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// freshDB is a per-test, migrated, disposable database. It also exposes a
// superuser connection for assertions about rows the application role cannot
// read.
func freshDB(t *testing.T) *pgtest.DB {
	t.Helper()
	return pgtest.FreshDB(t)
}

// appStore builds the store over the *application* role, which owns nothing and
// cannot bypass RLS. Testing through the owner connection would hide exactly the
// privilege mistakes these tests exist to catch.
func appStore(t *testing.T, db *pgtest.DB) *tenancy.Store {
	t.Helper()
	app := db.AsRole(t, "aic_app")
	var role string
	if err := app.QueryRow(`SELECT current_user`).Scan(&role); err != nil {
		t.Fatalf("read current_user: %v", err)
	}
	if role != "aic_app" {
		t.Fatalf("store is running as %q, not aic_app; privilege assertions would be meaningless", role)
	}
	return tenancy.NewStore(app)
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

// countAsOwner reads a table through the migration role, which is a superuser in
// the test environment and therefore bypasses RLS even where FORCE is set. It is
// used only to verify what the application role cannot see.
func countAsOwner(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
