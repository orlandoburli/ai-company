package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/migrations"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
)

func TestProductRepositoriesMigrationMovesLegacyDefaultAndKeepsColumn(t *testing.T) {
	db := pgtest.FreshDB(t)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`DROP TABLE product_repositories`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version='0030'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tenants(id,name,slug) VALUES ('MIG-REPO','Migration','migration-repo')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SELECT set_config('app.tenant_id','MIG-REPO',false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO products(id,tenant_id,name,slug,repo_url)
		VALUES ('MIG-PRODUCT','MIG-REPO','Legacy','legacy','git@github.com:orlandoburli/project-erp.git')`); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}

	var repo string
	var isDefault bool
	if err := db.QueryRow(`SELECT repo,is_default FROM product_repositories WHERE product_id='MIG-PRODUCT'`).Scan(&repo, &isDefault); err != nil {
		t.Fatal(err)
	}
	if repo != "orlandoburli/project-erp" || !isDefault {
		t.Fatalf("migrated repository = %q default=%v", repo, isDefault)
	}
	var legacy string
	if err := db.QueryRow(`SELECT repo_url FROM products WHERE id='MIG-PRODUCT'`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != "git@github.com:orlandoburli/project-erp.git" {
		t.Fatalf("legacy repo_url was dropped or changed: %q", legacy)
	}

	var rlsEnabled, rlsForced bool
	if err := db.QueryRow(`SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE oid='product_repositories'::regclass`).Scan(&rlsEnabled, &rlsForced); err != nil {
		t.Fatal(err)
	}
	if !rlsEnabled || !rlsForced {
		t.Fatalf("RLS enabled/forced = %v/%v", rlsEnabled, rlsForced)
	}
	var policy, grants bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='product_repositories' AND policyname='tenant_isolation'),
		EXISTS (SELECT 1 FROM information_schema.role_table_grants WHERE table_name='product_repositories' AND grantee='aic_app' AND privilege_type='DELETE')`).Scan(&policy, &grants); err != nil {
		t.Fatal(err)
	}
	if !policy || !grants {
		t.Fatalf("tenant policy/grants = %v/%v", policy, grants)
	}
	var hasCompositeCascade, tableComment bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_constraint
		WHERE conrelid='product_repositories'::regclass AND contype='f'
		  AND pg_get_constraintdef(oid) LIKE '%(product_id, tenant_id)%'
		  AND pg_get_constraintdef(oid) LIKE '%ON DELETE CASCADE%')`).Scan(&hasCompositeCascade); err != nil {
		t.Fatal(err)
	}
	if !hasCompositeCascade {
		t.Fatal("product foreign key is not composite with ON DELETE CASCADE")
	}
	var comment string
	if err := db.QueryRow(`SELECT obj_description('product_repositories'::regclass,'pg_class')`).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	tableComment = strings.Contains(comment, "not the source of truth")
	if !tableComment {
		t.Fatalf("table comment does not document legacy repo_url: %q", comment)
	}
	if _, err := db.Exec(`INSERT INTO product_repositories(id,tenant_id,product_id,repo)
		VALUES ('MIG-DUP','MIG-REPO','MIG-PRODUCT','orlandoburli/project-erp')`); err == nil {
		t.Fatal("duplicate repository per product was allowed")
	}
	if _, err := db.Exec(`INSERT INTO product_repositories(id,tenant_id,product_id,repo,is_default)
		VALUES ('MIG-SECOND-DEFAULT','MIG-REPO','MIG-PRODUCT','orlandoburli/another',true)`); err == nil {
		t.Fatal("a second default repository was allowed")
	}
}
