// Package pgtest gives each test its own Postgres database, migrated and
// disposable. It is imported by tests in other packages, so it cannot itself be
// a _test.go file.
//
// It refuses to run without AIC_TEST_DSN: these helpers DROP and CREATE
// databases, and the only safe target is one you have nominated explicitly.
package pgtest

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/orlandoburli/ai-company/internal/store/migrations"
)

// DB is a migrated, disposable database plus the DSN needed to reopen it as
// another role. *sql.DB is embedded so callers that only need connectivity can
// ignore the rest.
type DB struct {
	*sql.DB
	DSN string
}

// DSN returns the admin connection string from the environment.
func DSN(t testing.TB) string {
	t.Helper()
	v := os.Getenv("AIC_TEST_DSN")
	if v == "" {
		t.Skip("set AIC_TEST_DSN to a disposable Postgres URL; these tests create and drop databases")
	}
	return v
}

// FreshDB creates a uniquely named database, runs every migration against it,
// and drops it when the test ends. A leftover row from another test therefore
// cannot make this one pass.
func FreshDB(t testing.TB) *DB {
	t.Helper()

	base := DSN(t)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	name := "aic_t_" + identifierSafe(t.Name())

	// WITH (FORCE) terminates stragglers instead of failing the drop; without it a
	// leaked connection turns the next run into "database is being accessed".
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`); err != nil {
		admin.Close()
		t.Fatalf("drop %s: %v", name, err)
	}
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatalf("create %s: %v", name, err)
	}

	dsn := WithDatabase(t, base, name)
	db, err := sql.Open("pgx", dsn)
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

	if _, err := migrationsUp(db); err != nil {
		t.Fatalf("migrate %s: %v", name, err)
	}
	return &DB{DB: db, DSN: dsn}
}

// AsRole opens a separate pool whose every connection runs SET ROLE. Tests use
// it to exercise the application role exactly as deployment does — a role with
// no table privileges on the identity plane — rather than testing as the owner
// and assuming the difference does not matter.
func (d *DB) AsRole(t testing.TB, role string) *sql.DB {
	t.Helper()

	cfg, err := pgx.ParseConfig(d.DSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	quoted := pgx.Identifier{role}.Sanitize()
	cfg.AfterConnect = func(ctx context.Context, c *pgconn.PgConn) error {
		_, err := c.Exec(ctx, "SET ROLE "+quoted).ReadAll()
		return err
	}

	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	return db
}

// WithDatabase swaps the database name in a postgres:// URL while preserving
// query parameters (which is how the local socket and port are specified).
func WithDatabase(t testing.TB, adminDSN, name string) string {
	t.Helper()
	u, err := url.Parse(adminDSN)
	if err != nil || u.Scheme == "" {
		t.Fatalf("AIC_TEST_DSN must be a postgres:// URL, got %q", adminDSN)
	}
	u.Path = "/" + name
	return u.String()
}

// identifierSafe reduces a test name to [a-z0-9] so it is a valid identifier
// fragment and stays inside Postgres' 63-byte limit.
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

func migrationsUp(db *sql.DB) ([]string, error) {
	return migrations.Up(context.Background(), db)
}
