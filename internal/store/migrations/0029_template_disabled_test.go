package migrations_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
)

func TestTemplateDisabledMigrationImmutabilityRules(t *testing.T) {
	db := pgtest.FreshDB(t)
	var nullable, comment string
	if err := db.QueryRow(`
		SELECT is_nullable, col_description('public.workflow_templates'::regclass, ordinal_position)
		  FROM information_schema.columns
		 WHERE table_schema='public' AND table_name='workflow_templates' AND column_name='disabled_at'`).Scan(&nullable, &comment); err != nil {
		t.Fatal(err)
	}
	if nullable != "YES" || !strings.Contains(comment, "history") || !strings.Contains(comment, "cannot run") || !strings.Contains(comment, "deleted") {
		t.Fatalf("disabled_at schema/comment = nullable %q, comment %q", nullable, comment)
	}

	var owner, searchPath string
	if err := db.QueryRow(`
		SELECT pg_get_userbyid(p.proowner), array_to_string(p.proconfig, ',')
		  FROM pg_proc p
		 WHERE p.oid='public.workflow_templates_published_immutable()'::regprocedure`).Scan(&owner, &searchPath); err != nil {
		t.Fatal(err)
	}
	var migrationRole string
	if err := db.QueryRow(`SELECT current_user`).Scan(&migrationRole); err != nil {
		t.Fatal(err)
	}
	if owner != migrationRole || !strings.Contains(searchPath, "search_path=pg_catalog, public") {
		t.Fatalf("trigger function owner/search_path = %q/%q, want %q and pinned path", owner, searchPath, migrationRole)
	}

	if _, err := db.Exec(`INSERT INTO tenants(id,name,slug) VALUES ('TPL-RETIRE','Retire','tpl-retire')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"TPL-ENABLED", "TPL-DISABLED"} {
		if _, err := db.Exec(`INSERT INTO workflow_templates(id,tenant_id,name,version,graph_json,published_at) VALUES ($1,'TPL-RETIRE',$1,1,'{"nodes":[]}'::jsonb,now())`, id); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	assertImmutable := func(label string, err error) {
		t.Helper()
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "AIC07" || pg.Message != "published_workflow_template_is_immutable" {
			t.Fatalf("%s error = %v, want published immutability AIC07", label, err)
		}
	}
	if _, err := db.Exec(`UPDATE workflow_templates SET name='changed' WHERE id='TPL-ENABLED'`); err == nil {
		t.Fatal("ordinary published-template edit was allowed")
	} else {
		assertImmutable("ordinary edit", err)
	}
	if _, err := db.Exec(`UPDATE workflow_templates SET graph_json='{"nodes":[1]}'::jsonb WHERE id='TPL-ENABLED'`); err == nil {
		t.Fatal("published graph edit was allowed")
	} else {
		assertImmutable("graph edit", err)
	}
	if _, err := db.Exec(`UPDATE workflow_templates SET disabled_at=now() WHERE id='TPL-ENABLED'`); err != nil {
		t.Fatalf("disable published template: %v", err)
	}
	if _, err := db.Exec(`UPDATE workflow_templates SET disabled_at=NULL WHERE id='TPL-ENABLED'`); err != nil {
		t.Fatalf("enable published template: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM workflow_templates WHERE id='TPL-ENABLED'`); err == nil {
		t.Fatal("enabled published-template delete was allowed")
	} else {
		assertImmutable("enabled delete", err)
	}
	if _, err := db.Exec(`UPDATE workflow_templates SET disabled_at=now() WHERE id='TPL-DISABLED'`); err != nil {
		t.Fatalf("disable second published template: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM workflow_templates WHERE id='TPL-DISABLED'`); err != nil {
		t.Fatalf("delete disabled published template: %v", err)
	}
}
