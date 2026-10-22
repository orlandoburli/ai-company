// Package migrations applies the embedded SQL migrations in order.
//
// Plain SQL files, applied once, tracked in schema_migrations. No ORM and no
// DSL: the schema stays reviewable in git, which matters because the schema is
// the contract for tenancy and the isolation tests run against it.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.sql
var files embed.FS

// Migration is one versioned SQL file.
type Migration struct {
	Version string
	Name    string
	SQL     string
}

func load() ([]Migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		// NNNN_name.sql
		base := strings.TrimSuffix(e.Name(), ".sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("migration %q: expected NNNN_name.sql", e.Name())
		}
		b, err := files.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: parts[0], Name: parts[1], SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

const createVersionTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    name       TEXT        NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// Up applies every migration not yet recorded, in version order, each in its own
// transaction. A failed migration leaves the version unrecorded and the database
// as it was, so re-running is safe.
func Up(ctx context.Context, db *sql.DB) ([]string, error) {
	if _, err := db.ExecContext(ctx, createVersionTable); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	all, err := load()
	if err != nil {
		return nil, err
	}

	applied := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var ran []string
	for _, m := range all {
		if applied[m.Version] {
			continue
		}
		if err := applyOne(ctx, db, m); err != nil {
			return ran, err
		}
		ran = append(ran, m.Version+"_"+m.Name)
	}
	return ran, nil
}

func applyOne(ctx context.Context, db *sql.DB, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s: %w", m.Version, err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("apply %s_%s: %w", m.Version, m.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
		m.Version, m.Name); err != nil {
		return fmt.Errorf("record %s: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", m.Version, err)
	}
	return nil
}
