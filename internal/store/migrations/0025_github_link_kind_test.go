package migrations_test

import (
	"os"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
)

// linkKindTenant sets up the tenant and product a link row needs.
func linkKindTenant(t *testing.T, db *pgtest.DB) {
	t.Helper()
	if _, err := db.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`,
		"GH-KIND", "Kind tenant", "gh-kind", "GH-KIND-U1", "kind@example.com", "x", "Kind user", "GH-KIND-M1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO products (id,tenant_id,name,slug) VALUES ('GH-KIND-PRODUCT','GH-KIND','Demo','demo')`); err != nil {
		t.Fatal(err)
	}
}

// TestGithubLinkKindMigration checks what the link table gained: the kind of
// the row, and the branches a pull request was opened from.
func TestGithubLinkKindMigration(t *testing.T) {
	db := pgtest.FreshDB(t)

	for _, column := range []string{"kind", "head_branch", "base_branch"} {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_name = 'github_issue_links' AND column_name = $1)`, column).Scan(&exists); err != nil {
			t.Fatalf("read columns: %v", err)
		}
		if !exists {
			t.Errorf("github_issue_links.%s is missing", column)
		}
	}

	var definition string
	if err := db.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'github_issue_links_kind_check'`).Scan(&definition); err != nil {
		t.Fatalf("read the kind constraint: %v", err)
	}
	if !strings.Contains(definition, "'issue'") || !strings.Contains(definition, "'pr'") {
		t.Errorf("the kind constraint does not allow exactly issue and pr: %s", definition)
	}

	linkKindTenant(t, db)
	// A row without a kind is an issue.
	if _, err := db.Exec(`INSERT INTO github_issue_links
		(id,tenant_id,product_id,repo,issue_number,issue_state)
		VALUES ('GH-KIND-DEFAULT','GH-KIND','GH-KIND-PRODUCT','acme/demo',1,'open')`); err != nil {
		t.Fatalf("insert a link row: %v", err)
	}
	var kind string
	if err := db.QueryRow(`SELECT kind FROM github_issue_links WHERE id='GH-KIND-DEFAULT'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "issue" {
		t.Errorf("the default kind is %q, want 'issue'", kind)
	}
	// A pull request is a kind of its own.
	if _, err := db.Exec(`INSERT INTO github_issue_links
		(id,tenant_id,product_id,repo,issue_number,kind,issue_state,head_branch,base_branch)
		VALUES ('GH-KIND-PR','GH-KIND','GH-KIND-PRODUCT','acme/demo',2,'pr','open','feature/x','main')`); err != nil {
		t.Fatalf("insert a pull request link: %v", err)
	}
	// Anything else is not a kind.
	if _, err := db.Exec(`INSERT INTO github_issue_links
		(id,tenant_id,product_id,repo,issue_number,kind,issue_state)
		VALUES ('GH-KIND-BAD','GH-KIND','GH-KIND-PRODUCT','acme/demo',3,'banana','open')`); err == nil {
		t.Error("a link row of an unknown kind was accepted")
	} else if !strings.Contains(err.Error(), "github_issue_links_kind_check") {
		t.Errorf("the unknown kind failed for the wrong reason: %v", err)
	}
}

// TestGithubLinkKindMigrationBackfillsExistingRows replays the migration on a
// table that still has the rows of before it: those rows describe issues.
func TestGithubLinkKindMigrationBackfillsExistingRows(t *testing.T) {
	db := pgtest.FreshDB(t)
	script, err := os.ReadFile("0025_github_link_kind.sql")
	if err != nil {
		t.Fatalf("read the migration: %v", err)
	}
	linkKindTenant(t, db)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck

	// The table as it was before the migration.
	for _, statement := range []string{
		`ALTER TABLE github_issue_links DROP CONSTRAINT github_issue_links_kind_check`,
		`ALTER TABLE github_issue_links DROP COLUMN kind`,
		`ALTER TABLE github_issue_links DROP COLUMN head_branch`,
		`ALTER TABLE github_issue_links DROP COLUMN base_branch`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO github_issue_links
		(id,tenant_id,product_id,repo,issue_number,issue_state,issue_title,issue_url)
		VALUES ('GH-KIND-LINK','GH-KIND','GH-KIND-PRODUCT','acme/demo',7,'open','Legacy issue','https://github.test/acme/demo/issues/7')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(string(script)); err != nil {
		t.Fatalf("apply the migration: %v", err)
	}

	var kind, head, base string
	if err := tx.QueryRow(`SELECT kind,head_branch,base_branch FROM github_issue_links WHERE id='GH-KIND-LINK'`).
		Scan(&kind, &head, &base); err != nil {
		t.Fatalf("read the migrated row: %v", err)
	}
	if kind != "issue" {
		t.Errorf("the existing row is %q, want 'issue'", kind)
	}
	if head != "" || base != "" {
		t.Errorf("an issue has no branches: head=%q base=%q", head, base)
	}
	if _, err := tx.Exec(`UPDATE github_issue_links SET kind='pr' WHERE id='GH-KIND-LINK'`); err != nil {
		t.Errorf("a pull request link was rejected: %v", err)
	}
	// The last statement of the transaction, so its failure does not hide the
	// ones before it.
	if _, err := tx.Exec(`UPDATE github_issue_links SET kind='banana' WHERE id='GH-KIND-LINK'`); err == nil {
		t.Error("a link row of an unknown kind was accepted")
	}
}
