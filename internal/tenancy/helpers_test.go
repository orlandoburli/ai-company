package tenancy_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/orlandoburli/ai-company/internal/store/migrations"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// dsn returns the admin DSN. These tests create and drop databases, so they
// refuse to run without an explicitly disposable target (FR-TN-1, §10).
func dsn(t *testing.T) string {
	t.Helper()
	v := os.Getenv("AIC_TEST_DSN")
	if v == "" {
		t.Skip("set AIC_TEST_DSN to a disposable Postgres URL; these tests create and drop databases")
	}
	return v
}

// withDatabase swaps the database name in a postgres:// URL, preserving
// query parameters (which is how the local socket + port are specified).
func withDatabase(t *testing.T, adminDSN, name string) string {
	t.Helper()
	u, err := url.Parse(adminDSN)
	if err != nil || u.Scheme == "" {
		t.Fatalf("AIC_TEST_DSN must be a postgres:// URL, got %q", adminDSN)
	}
	u.Path = "/" + name
	return u.String()
}

// freshDB creates a database per test, migrates it, and drops it afterwards, so
// a leftover row from one test can never make another pass.
func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	base := dsn(t)

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}

	name := "aic_iso_" + identifierSafe(t.Name())

	// WITH (FORCE) terminates stragglers instead of failing the drop; without it
	// a leaked connection turns into a confusing "database is being accessed"
	// error on the next run.
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`); err != nil {
		admin.Close()
		t.Fatalf("drop %s: %v", name, err)
	}
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatalf("create %s: %v", name, err)
	}

	db, err := sql.Open("pgx", withDatabase(t, base, name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	db.SetMaxOpenConns(8)

	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`); err != nil {
			t.Logf("cleanup: drop %s: %v", name, err)
		}
		admin.Close()
	})

	if _, err := migrations.Up(context.Background(), db); err != nil {
		t.Fatalf("migrate %s: %v", name, err)
	}
	return db
}

// identifierSafe reduces a test name to [a-z0-9] so it is a valid unquoted
// identifier fragment and stays well under Postgres' 63-byte limit.
func identifierSafe(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// appConn returns a connection impersonating the application role with a tenant
// scope applied — i.e. exactly what request-path code looks like.
//
// SET ROLE is used instead of a second credential so the test exercises the same
// role the app will use (non-owner, NOBYPASSRLS) without needing a password
// round-trip. It requires the DSN's user to be a superuser or a member of
// aic_app; the GRANT below is attempted once to make that true.
func appConn(t *testing.T, owner *sql.DB, tenantID string) *sql.Conn {
	t.Helper()
	ctx := context.Background()

	conn, err := owner.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}

	if _, err := conn.ExecContext(ctx, `SET ROLE aic_app`); err != nil {
		// Try to grant membership, then retry once. Failure here is fatal rather
		// than skipped: a silently skipped isolation test is worse than none.
		if _, gerr := conn.ExecContext(ctx, `GRANT aic_app TO CURRENT_USER`); gerr != nil {
			conn.Close()
			t.Fatalf("SET ROLE aic_app: %v (and GRANT failed: %v); connect as a superuser or a member of aic_app", err, gerr)
		}
		if _, err := conn.ExecContext(ctx, `SET ROLE aic_app`); err != nil {
			conn.Close()
			t.Fatalf("SET ROLE aic_app after grant: %v", err)
		}
	}

	// set_config is parameterisable, unlike SET. Passing "" leaves the policy's
	// current_tenant_id() NULL, which is the "no scope" case.
	if _, err := conn.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, false)`, tenantID); err != nil {
		conn.Close()
		t.Fatalf("set tenant scope: %v", err)
	}

	// Session state survives a trip through the pool, so undo it or a later test
	// can inherit a role and a tenant it never asked for.
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
