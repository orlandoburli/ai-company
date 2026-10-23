package skill

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type testFixture struct {
	db  *pgtest.DB
	app *sql.DB
	st  *Store
	t1  tenancy.Scope
	t2  tenancy.Scope
}

func newTestFixture(t *testing.T) *testFixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	app.SetMaxOpenConns(6)
	for _, id := range []string{"T1", "T2"} {
		if _, err := app.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`, id,
			"Tenant "+id, strings.ToLower(id), "U-"+id, strings.ToLower(id)+"@example.com", "x", "User "+id, "M-"+id); err != nil {
			t.Fatalf("seed tenant %s: %v", id, err)
		}
	}
	return &testFixture{
		db: db, app: app, st: NewStore(app),
		t1: tenancy.Scope{TenantID: "T1", UserID: "U-T1", Role: tenancy.RoleOwner},
		t2: tenancy.Scope{TenantID: "T2", UserID: "U-T2", Role: tenancy.RoleOwner},
	}
}

func addSkill(t *testing.T, f *testFixture, sc tenancy.Scope, slug, description, content string) Skill {
	t.Helper()
	item, err := f.st.Create(context.Background(), sc, NewSkill{Name: slug, Slug: slug, Description: description})
	if err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	if content != "" {
		if _, err := f.st.AddVersion(context.Background(), sc, item.ID, NewVersion{ContentMD: content}); err != nil {
			t.Fatalf("add version %s: %v", slug, err)
		}
		item, err = f.st.Get(context.Background(), sc, item.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	return item
}

func TestVersioningApprovalSHAAndAppendOnly(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	item, err := f.st.Create(ctx, f.t1, NewSkill{Name: "Go style", Slug: "go-style", Description: "Go formatting and style"})
	if err != nil {
		t.Fatal(err)
	}
	human, err := f.st.AddVersion(ctx, f.t1, item.ID, NewVersion{ContentMD: "gofmt -w ."})
	if err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256([]byte("gofmt -w ."))
	if human.Version != 1 || human.ContentSHA != hex.EncodeToString(sha[:]) || human.ApprovedAt == nil || human.ApprovedBy == nil {
		t.Fatalf("human version metadata: %+v", human)
	}
	current, found, err := f.st.CurrentVersion(ctx, f.t1, item.ID)
	if err != nil || !found || current.ID != human.ID {
		t.Fatalf("current human version: found=%v version=%+v err=%v", found, current, err)
	}
	draft, err := f.st.AddVersion(ctx, f.t1, item.ID, NewVersion{ContentMD: "draft instructions", AuthorKind: "agent_draft", AuthorID: ptr("AGENT-1")})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Version != 2 || draft.ApprovedAt != nil {
		t.Fatalf("draft was not inert: %+v", draft)
	}
	if _, found, err := f.st.CurrentVersion(ctx, f.t1, item.ID); err != nil || !found || current.ID != human.ID {
		t.Fatalf("draft replaced approved current version: found=%v err=%v", found, err)
	}
	approved, err := f.st.Approve(ctx, f.t1, draft.ID, f.t1.UserID)
	if err != nil || approved.ApprovedAt == nil || approved.ApprovedBy == nil {
		t.Fatalf("approve draft: %+v err=%v", approved, err)
	}
	current, found, err = f.st.CurrentVersion(ctx, f.t1, item.ID)
	if err != nil || !found || current.ID != approved.ID {
		t.Fatalf("approved draft not current: %+v found=%v err=%v", current, found, err)
	}
	versions, err := f.st.Versions(ctx, f.t1, item.ID)
	if err != nil || len(versions) != 2 || versions[0].Version != 2 {
		t.Fatalf("version history: %+v err=%v", versions, err)
	}

	// Exercise the database guard through the application's transaction and RLS.
	tx, err := f.app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, f.t1.TenantID); err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE skill_versions SET content_md='tampered' WHERE id=$1`, human.ID)
	_ = tx.Rollback()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "AIC08" {
		t.Fatalf("approved content update error = %v, want SQLSTATE AIC08", err)
	}
}

func TestSelectionTrustedMatchingBoundAndTenantIsolation(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	one := addSkill(t, f, f.t1, "go-build", "Procedures for building Go applications", "go build ./...")
	addSkill(t, f, f.t1, "go-test", "Run Go test suites and diagnose failures", "go test ./...")
	addSkill(t, f, f.t1, "rust", "Rust crate packaging and release procedures", "cargo test")
	foreign := addSkill(t, f, f.t2, "go-foreign", "Go build procedures", "foreign tenant text")
	disabled := addSkill(t, f, f.t1, "go-disabled", "Go build disabled procedure", "should not attach")
	if _, err := f.st.SetEnabled(ctx, f.t1, disabled.ID, false); err != nil {
		t.Fatal(err)
	}
	draft := addSkill(t, f, f.t1, "go-draft", "Go build draft procedure", "")
	if _, err := f.st.AddVersion(ctx, f.t1, draft.ID, NewVersion{ContentMD: "inert", AuthorKind: "agent_draft"}); err != nil {
		t.Fatal(err)
	}

	selected, err := Select(ctx, f.t1, f.st, SelectOptions{Task: "Build the Go application", MaxAttach: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(selected))
	for _, item := range selected {
		got[item.Skill.Slug] = true
		if item.Ref != fmt.Sprintf("skill:%s@%d", item.Skill.Slug, item.Version.Version) || item.Reason != "description matched task" {
			t.Errorf("selection metadata: %+v", item)
		}
	}
	if !got[one.Slug] || got["rust"] || got[disabled.Slug] || got[draft.Slug] || got[foreign.Slug] {
		t.Fatalf("trusted selector chose unexpected skills: %v", got)
	}

	bounded, err := Select(ctx, f.t1, f.st, SelectOptions{Task: "Go build test", MaxAttach: 1})
	if err != nil || len(bounded) != 1 {
		t.Fatalf("max_attach bound: %d, %v", len(bounded), err)
	}
	first, err := Select(ctx, f.t1, f.st, SelectOptions{Task: "Go build test", MaxAttach: 10})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Select(ctx, f.t1, f.st, SelectOptions{Task: "Go build test", MaxAttach: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("unstable selected count: %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Ref != second[i].Ref || first[i].Score != second[i].Score {
			t.Fatalf("selection order is nondeterministic: %+v then %+v", first, second)
		}
	}

	// Task text alone cannot force attachment when the trusted description does
	// not share any terms with it; only an explicit slug is an alternate selector.
	noMatch := addSkill(t, f, f.t1, "alien-task-selector", "Unrelated deployment conventions", "no matching procedure")
	none, err := Select(ctx, f.t1, f.st, SelectOptions{Task: noMatch.Slug, MaxAttach: 10})
	if err != nil || len(none) != 0 {
		t.Fatalf("task text matched unrelated trusted description: %+v err=%v", none, err)
	}
	explicit, err := Select(ctx, f.t1, f.st, SelectOptions{Keys: []string{"go-build"}})
	if err != nil || len(explicit) != 1 || explicit[0].Skill.ID != one.ID {
		t.Fatalf("explicit selector: %+v err=%v", explicit, err)
	}
}

func TestCRUDRolesSlugUniquenessAndCrossTenant(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	member := tenancy.Scope{TenantID: f.t1.TenantID, UserID: f.t1.UserID, Role: tenancy.RoleMember}
	if _, err := f.st.Create(ctx, member, NewSkill{Name: "No", Slug: "no"}); !errors.Is(err, tenancy.ErrForbidden) {
		t.Fatalf("member create = %v, want forbidden", err)
	}
	created := addSkill(t, f, f.t1, "unique", "trusted description", "content")
	if _, err := f.st.Create(ctx, f.t1, NewSkill{Name: "Duplicate", Slug: "unique"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate slug = %v, want conflict", err)
	}
	if _, err := f.st.Get(ctx, f.t2, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign get = %v, want not found", err)
	}
	list, err := f.st.List(ctx, f.t2, "")
	if err != nil || len(list) != 0 {
		t.Fatalf("foreign list = %+v, %v", list, err)
	}
	updated, err := f.st.Update(ctx, f.t1, created.ID, Patch{Description: ptr("new trusted description")})
	if err != nil || updated.Description != "new trusted description" {
		t.Fatalf("update: %+v err=%v", updated, err)
	}
	updated, err = f.st.SetEnabled(ctx, f.t1, created.ID, false)
	if err != nil || updated.Enabled {
		t.Fatalf("disable: %+v err=%v", updated, err)
	}
	if err := f.st.Delete(ctx, f.t1, created.ID); err != nil {
		t.Fatal(err)
	}
}

func ptr(s string) *string { return &s }
