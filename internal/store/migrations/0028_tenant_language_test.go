package migrations_test

import (
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
)

// The language is a company setting and a run's memory of it. The column and
// its check are the schema-level guarantee: the product speaks English and
// Brazilian Portuguese, and nothing else may be stored.
func TestTenantLanguageMigration(t *testing.T) {
	db := pgtest.FreshDB(t)

	for _, column := range []struct{ table, name string }{
		{"tenants", "language"},
		{"workflow_runs", "language"},
	} {
		var nullable, defaultValue string
		err := db.QueryRow(`
			SELECT is_nullable, coalesce(column_default, '')
			  FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`,
			column.table, column.name).Scan(&nullable, &defaultValue)
		if err != nil {
			t.Fatalf("%s.%s: %v", column.table, column.name, err)
		}
		if nullable != "NO" {
			t.Errorf("%s.%s is nullable; a run must always know its language", column.table, column.name)
		}
		if !strings.Contains(defaultValue, "'en'") {
			t.Errorf("%s.%s default = %q, want 'en'", column.table, column.name, defaultValue)
		}
	}

	// The check is what refuses everything the product does not speak.
	for _, tc := range []struct {
		table  string
		column string
	}{
		{"tenants", "language"},
		{"workflow_runs", "language"},
	} {
		var definition string
		if err := db.QueryRow(`
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			 WHERE conrelid = $1::regclass AND pg_get_constraintdef(oid) LIKE '%' || $2 || '%'`,
			"public."+tc.table, tc.column).Scan(&definition); err != nil {
			t.Fatalf("read the %s.%s constraint: %v", tc.table, tc.column, err)
		}
		for _, language := range []string{"'en'", "'pt-BR'"} {
			if !strings.Contains(definition, language) {
				t.Errorf("the %s.%s constraint does not allow %s: %s", tc.table, tc.column, language, definition)
			}
		}
	}
}

// The column is the company's, and the company row is in the identity plane:
// adding the column must not open a door the application role did not have.
func TestTenantLanguageKeepsTheIdentityPlaneClosed(t *testing.T) {
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")

	if _, err := app.Exec(`SELECT language FROM tenants`); err == nil {
		t.Error("aic_app could read the tenants' language directly; the identity plane must stay closed")
	}
	if _, err := app.Exec(`UPDATE tenants SET language = 'pt-BR'`); err == nil {
		t.Error("aic_app could write the tenants' language directly; the identity plane must stay closed")
	}

	// The two functions are the only door, and they are not a public one.
	var publicExecute bool
	if err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM pg_proc p
			 WHERE p.pronamespace = 'public'::regnamespace
			   AND p.proname IN ('auth_tenant_language', 'auth_update_tenant_language')
			   AND has_function_privilege('public', p.oid, 'EXECUTE')
		)`).Scan(&publicExecute); err != nil {
		t.Fatal(err)
	}
	if publicExecute {
		t.Error("a language function is executable by PUBLIC; revoke EXECUTE before granting to aic_app")
	}
	for _, name := range []string{"auth_tenant_language", "auth_update_tenant_language"} {
		var granted bool
		if err := db.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM pg_proc p
				 WHERE p.pronamespace = 'public'::regnamespace
				   AND p.proname = $1
				   AND has_function_privilege('aic_app', p.oid, 'EXECUTE')
			)`, name).Scan(&granted); err != nil {
			t.Fatal(err)
		}
		if !granted {
			t.Errorf("aic_app cannot execute %s; the application would have no door to the language", name)
		}
	}
}

// The check refuses a language the product does not speak, on both columns.
func TestTenantLanguageCheckRefusesUnknownLanguages(t *testing.T) {
	db := pgtest.FreshDB(t)

	if _, err := db.Exec(`INSERT INTO tenants (id, name, slug, language) VALUES ('LANG-1', 'Lang', 'lang-1', 'pt-BR')`); err != nil {
		t.Fatalf("a supported language was refused: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tenants (id, name, slug, language) VALUES ('LANG-2', 'Lang', 'lang-2', 'fr')`); err == nil {
		t.Error("an unsupported language was stored on the tenant")
	}

	// The same for a run: the column is checked too.
	if _, err := db.Exec(`INSERT INTO workflow_templates (id, tenant_id, name, version, graph_json) VALUES ('LANG-TPL', 'LANG-1', 'lang', 1, '{}'::jsonb)`); err != nil {
		t.Fatalf("insert template: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_runs (id, tenant_id, template_id, template_version, definition_snapshot_json, language) VALUES ('LANG-RUN', 'LANG-1', 'LANG-TPL', 1, '{}'::jsonb, 'pt-BR')`); err != nil {
		t.Fatalf("a supported language was refused on a run: %v", err)
	}
	if _, err := db.Exec(`UPDATE workflow_runs SET language = 'es' WHERE id = 'LANG-RUN'`); err == nil {
		t.Error("an unsupported language was stored on a run")
	}
}
