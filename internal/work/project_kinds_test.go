package work_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

// The kinds a project offers, the labels the items use, and the item's family.
//
// The kinds are configuration: a project declares what it offers, and the
// interface presents that list. The labels are not configuration at all — they
// are whatever the items happen to use. The family is the tree around an item:
// what it sits under, and what sits under it.

// --- the kinds a project uses ------------------------------------------------

func TestNewProjectIsSeededWithTheDefaultKinds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	kinds, err := f.st.ListProjectKinds(ctx, f.t1, f.pr1.ID)
	if err != nil {
		t.Fatalf("list kinds: %v", err)
	}
	want := []string{"task", "bug", "epic", "story", "spike"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %+v; want %v", kinds, want)
	}
	for i, k := range kinds {
		if k.Name != want[i] {
			t.Fatalf("kind %d = %q; want %q", i, k.Name, want[i])
		}
		if k.Position != i {
			t.Fatalf("kind %q position = %d; want %d", k.Name, k.Position, i)
		}
	}
	// The seeded list is the constant, not a copy somebody may have edited.
	if len(work.DefaultProjectKinds) != len(want) {
		t.Fatalf("DefaultProjectKinds = %v; want %v", work.DefaultProjectKinds, want)
	}
	for i, name := range work.DefaultProjectKinds {
		if name != want[i] {
			t.Fatalf("DefaultProjectKinds[%d] = %q; want %q", i, name, want[i])
		}
	}
}

func TestSetProjectKindsReplacesAndOrders(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.st.SetProjectKinds(ctx, f.t1, f.pr1.ID, []string{"epic", "task", "chore"}); err != nil {
		t.Fatalf("set kinds: %v", err)
	}
	kinds, err := f.st.ListProjectKinds(ctx, f.t1, f.pr1.ID)
	if err != nil {
		t.Fatalf("list kinds: %v", err)
	}
	want := []string{"epic", "task", "chore"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %+v; want %v", kinds, want)
	}
	for i, k := range kinds {
		if k.Name != want[i] {
			t.Fatalf("kind %d = %q; want %q", i, k.Name, want[i])
		}
		if k.Position != i {
			t.Fatalf("kind %q position = %d; want %d", k.Name, k.Position, i)
		}
	}

	// An empty list clears the project: it may offer nothing at all.
	if err := f.st.SetProjectKinds(ctx, f.t1, f.pr1.ID, nil); err != nil {
		t.Fatalf("clear kinds: %v", err)
	}
	kinds, err = f.st.ListProjectKinds(ctx, f.t1, f.pr1.ID)
	if err != nil {
		t.Fatalf("list kinds: %v", err)
	}
	if len(kinds) != 0 {
		t.Fatalf("kinds = %+v; want none", kinds)
	}
}

func TestProjectKindsAreIsolatedByTenant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Another tenant cannot even see that the project exists.
	kinds, err := f.st.ListProjectKinds(ctx, f.t2, f.pr1.ID)
	if !errors.Is(err, work.ErrNotFound) {
		t.Fatalf("T2 list of T1 kinds: err = %v; want ErrNotFound", err)
	}
	if len(kinds) != 0 {
		t.Fatalf("T2 sees %d kinds of T1's project", len(kinds))
	}

	// Nor write them.
	if err := f.st.SetProjectKinds(ctx, f.t2, f.pr1.ID, []string{"theirs"}); !errors.Is(err, work.ErrNotFound) {
		t.Fatalf("T2 set of T1 kinds: err = %v; want ErrNotFound", err)
	}

	// And T1's list is untouched by the attempt.
	kinds, err = f.st.ListProjectKinds(ctx, f.t1, f.pr1.ID)
	if err != nil {
		t.Fatalf("list kinds: %v", err)
	}
	if len(kinds) != len(work.DefaultProjectKinds) {
		t.Fatalf("T1 kinds = %+v; want the defaults", kinds)
	}
}

func TestInvalidProjectKindNameIsRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	cases := map[string][]string{
		"empty":    {""},
		"blank":    {"   "},
		"too long": {strings.Repeat("x", 41)},
		"twice":    {"task", "task"},
	}
	for name, names := range cases {
		if err := f.st.SetProjectKinds(ctx, f.t1, f.pr1.ID, names); !errors.Is(err, work.ErrInvalid) {
			t.Fatalf("%s: err = %v; want ErrInvalid", name, err)
		}
	}

	// A refused write changes nothing.
	kinds, err := f.st.ListProjectKinds(ctx, f.t1, f.pr1.ID)
	if err != nil {
		t.Fatalf("list kinds: %v", err)
	}
	if len(kinds) != len(work.DefaultProjectKinds) {
		t.Fatalf("kinds = %+v; want the defaults", kinds)
	}
}

func TestEnsureDefaultProjectKindsOnlyFillsAnEmptyProject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// A project that has chosen its own kinds keeps them.
	if err := f.st.SetProjectKinds(ctx, f.t1, f.pr1.ID, []string{"only"}); err != nil {
		t.Fatalf("set kinds: %v", err)
	}
	if err := f.st.EnsureDefaultProjectKinds(ctx, f.t1, f.pr1.ID); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	kinds, err := f.st.ListProjectKinds(ctx, f.t1, f.pr1.ID)
	if err != nil {
		t.Fatalf("list kinds: %v", err)
	}
	if len(kinds) != 1 || kinds[0].Name != "only" {
		t.Fatalf("kinds = %+v; want the project's own list", kinds)
	}

	// An empty project is filled with the defaults.
	if err := f.st.SetProjectKinds(ctx, f.t1, f.pr1.ID, nil); err != nil {
		t.Fatalf("clear kinds: %v", err)
	}
	if err := f.st.EnsureDefaultProjectKinds(ctx, f.t1, f.pr1.ID); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	kinds, err = f.st.ListProjectKinds(ctx, f.t1, f.pr1.ID)
	if err != nil {
		t.Fatalf("list kinds: %v", err)
	}
	if len(kinds) != len(work.DefaultProjectKinds) {
		t.Fatalf("kinds = %+v; want the defaults", kinds)
	}
}

// TestProjectKindsTableExistsWithItsConstraints checks the schema itself, so a
// later migration cannot quietly drop the rules the store relies on.
func TestProjectKindsTableExistsWithItsConstraints(t *testing.T) {
	db := pgtest.FreshDB(t)

	var enabled, forced bool
	if err := db.QueryRow(`
		SELECT relrowsecurity, relforcerowsecurity
		  FROM pg_class WHERE oid = 'public.project_kinds'::regclass`).
		Scan(&enabled, &forced); err != nil {
		t.Fatalf("project_kinds: %v", err)
	}
	if !enabled {
		t.Fatal("row-level security is not enabled on project_kinds")
	}
	if !forced {
		t.Fatal("row-level security is not FORCED on project_kinds")
	}

	var policies int
	if err := db.QueryRow(`
		SELECT count(*) FROM pg_policies
		 WHERE schemaname = 'public' AND tablename = 'project_kinds'
		   AND policyname = 'tenant_isolation'`).Scan(&policies); err != nil {
		t.Fatalf("policies: %v", err)
	}
	if policies != 1 {
		t.Fatalf("project_kinds has %d tenant_isolation policies; want 1", policies)
	}

	var canSelect, canInsert, canUpdate, canDelete bool
	if err := db.QueryRow(`
		SELECT has_table_privilege('aic_app', 'public.project_kinds', 'SELECT'),
		       has_table_privilege('aic_app', 'public.project_kinds', 'INSERT'),
		       has_table_privilege('aic_app', 'public.project_kinds', 'UPDATE'),
		       has_table_privilege('aic_app', 'public.project_kinds', 'DELETE')`).
		Scan(&canSelect, &canInsert, &canUpdate, &canDelete); err != nil {
		t.Fatalf("grants: %v", err)
	}
	if !canSelect || !canInsert || !canUpdate || !canDelete {
		t.Fatalf("aic_app grants: select=%v insert=%v update=%v delete=%v; want all",
			canSelect, canInsert, canUpdate, canDelete)
	}

	// Two uniqueness rules: the kind is unique per project, and the row has a
	// composite key of its own.
	var uniques int
	if err := db.QueryRow(`
		SELECT count(*) FROM pg_constraint
		 WHERE conrelid = 'public.project_kinds'::regclass AND contype = 'u'`).
		Scan(&uniques); err != nil {
		t.Fatalf("uniqueness: %v", err)
	}
	if uniques != 2 {
		t.Fatalf("project_kinds has %d unique constraints; want 2", uniques)
	}
}

// TestProjectKindsSchemaRefusesBadRows checks the rules the store would
// otherwise have to enforce on its own.
func TestProjectKindsSchemaRefusesBadRows(t *testing.T) {
	f := newFixture(t)

	bad := []struct {
		name string
		id   string
		args []any
	}{
		{"empty name", "K1", []any{f.pr1.ID, ""}},
		{"blank name", "K2", []any{f.pr1.ID, "   "}},
		{"name too long", "K3", []any{f.pr1.ID, strings.Repeat("x", 41)}},
		{"name twice", "K4", []any{f.pr1.ID, "task"}},
	}
	for _, c := range bad {
		if _, err := f.owner.Exec(
			`INSERT INTO project_kinds (id, tenant_id, project_id, name) VALUES ($1, 'T1', $2, $3)`,
			c.id, c.args[0], c.args[1]); err == nil {
			t.Fatalf("the schema accepted a %s", c.name)
		}
	}

	// A kind cannot point at another tenant's project: the composite key holds.
	if _, err := f.owner.Exec(
		`INSERT INTO project_kinds (id, tenant_id, project_id, name) VALUES ('K5', 'T1', $1, 'task')`,
		f.pr2.ID); err == nil {
		t.Fatal("the schema accepted a kind of another tenant's project")
	}
}

// --- the labels in use -------------------------------------------------------

func TestLabelsInUseAreDistinctSortedAndScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	side, err := f.st.CreateProject(ctx, f.t1, f.p1.ID, nil, "Side", "SIDE")
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}

	labelled := func(sc tenancy.Scope, projectID string, labels []string) {
		t.Helper()
		if _, err := f.st.CreateItem(ctx, sc, work.NewItem{
			ProjectID: projectID, Kind: "task", Title: "item",
			Labels: labels, ActorUserID: sc.UserID,
		}); err != nil {
			t.Fatalf("create item with labels %v: %v", labels, err)
		}
	}

	labelled(f.t1, f.pr1.ID, []string{"billing"})
	labelled(f.t1, f.pr1.ID, []string{"billing", "import"})
	labelled(f.t1, f.pr1.ID, []string{"  ", " import "})
	labelled(f.t1, f.pr1.ID, nil)
	labelled(f.t1, side.ID, []string{"side"})
	labelled(f.t2, f.pr2.ID, []string{"theirs"})

	// The tenant's labels: distinct, trimmed and sorted. The blank label
	// contributes nothing, the padded one is the same label.
	labels, err := f.st.ListLabels(ctx, f.t1, "")
	if err != nil {
		t.Fatalf("list labels: %v", err)
	}
	want := []string{"billing", "import", "side"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v; want %v", labels, want)
	}
	for i, l := range labels {
		if l != want[i] {
			t.Fatalf("label %d = %q; want %q", i, l, want[i])
		}
	}

	// The project's labels.
	labels, err = f.st.ListLabels(ctx, f.t1, f.pr1.ID)
	if err != nil {
		t.Fatalf("list labels: %v", err)
	}
	want = []string{"billing", "import"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v; want %v", labels, want)
	}
	for i, l := range labels {
		if l != want[i] {
			t.Fatalf("label %d = %q; want %q", i, l, want[i])
		}
	}

	// Another tenant's labels are not the tenant's labels.
	labels, err = f.st.ListLabels(ctx, f.t2, "")
	if err != nil {
		t.Fatalf("list labels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "theirs" {
		t.Fatalf("T2 labels = %v; want [theirs]", labels)
	}
}

// --- the item's family -------------------------------------------------------

// itemOfKind creates an item with a kind of its own, so the family answers
// can be checked against the kind that is stored.
func (f *fixture) itemOfKind(sc tenancy.Scope, projectID, kind, title string) work.Item {
	f.t.Helper()
	it, err := f.st.CreateItem(context.Background(), sc, work.NewItem{
		ProjectID: projectID, Kind: kind, Title: title, ActorUserID: sc.UserID,
	})
	if err != nil {
		f.t.Fatalf("create item %q: %v", title, err)
	}
	return it
}

// childOf creates an item under a parent, with the item's own ordering set.
func (f *fixture) childOf(sc tenancy.Scope, projectID, title, parentID string, sortOrder float64) work.Item {
	f.t.Helper()
	it, err := f.st.CreateItem(context.Background(), sc, work.NewItem{
		ProjectID: projectID, ParentID: &parentID, Kind: "task", Title: title,
		ActorUserID: sc.UserID,
	})
	if err != nil {
		f.t.Fatalf("create child %q: %v", title, err)
	}
	if _, err := f.st.UpdateItem(context.Background(), sc, it.ID,
		work.ItemPatch{SortOrder: &sortOrder}, sc.UserID); err != nil {
		f.t.Fatalf("set sort order of %q: %v", title, err)
	}
	return it
}

func TestItemFamilyReturnsTheParentAndTheChildren(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	epic := f.itemOfKind(f.t1, f.pr1.ID, "epic", "the epic")
	first := f.childOf(f.t1, f.pr1.ID, "first", epic.ID, 2)
	second := f.childOf(f.t1, f.pr1.ID, "second", epic.ID, 1)
	third := f.childOf(f.t1, f.pr1.ID, "third", epic.ID, 1)

	// The epic's family: no parent, and its children in the item's own order
	// (sort_order first, then the number).
	parent, children, err := f.st.GetItemFamily(ctx, f.t1, epic.ID)
	if err != nil {
		t.Fatalf("family: %v", err)
	}
	if parent != nil {
		t.Fatalf("the epic has a parent: %+v", parent)
	}
	wantOrder := []string{second.ID, third.ID, first.ID}
	if len(children) != len(wantOrder) {
		t.Fatalf("children = %+v; want %v", children, wantOrder)
	}
	for i, c := range children {
		if c.ID != wantOrder[i] {
			t.Fatalf("child %d = %s; want %s", i, c.ID, wantOrder[i])
		}
	}

	// The children of an item are what makes it an epic.
	it, err := f.st.GetItem(ctx, f.t1, epic.ID)
	if err != nil {
		t.Fatalf("get epic: %v", err)
	}
	if !it.IsEpic {
		t.Fatal("an item with children is not classified as an epic")
	}

	// A child's family: the parent, and no children of its own.
	parent, children, err = f.st.GetItemFamily(ctx, f.t1, second.ID)
	if err != nil {
		t.Fatalf("family: %v", err)
	}
	if parent == nil {
		t.Fatal("the child has no parent")
	}
	if parent.ID != epic.ID || parent.Number != epic.Number || parent.Title != "the epic" ||
		parent.Kind != "epic" || parent.Status != "backlog" {
		t.Fatalf("parent = %+v; want the epic", parent)
	}
	if len(children) != 0 {
		t.Fatalf("a leaf has children: %+v", children)
	}
}

func TestFamilySurvivesADeletedParent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	epic := f.itemOfKind(f.t1, f.pr1.ID, "epic", "the epic")
	child := f.childOf(f.t1, f.pr1.ID, "child", epic.ID, 0)

	// The cascade would take the child with its parent, so the reference
	// trigger is switched off for this one delete: the child is left pointing
	// at a parent that is not there any more.
	conn, err := f.owner.Conn(ctx)
	if err != nil {
		t.Fatalf("owner connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatalf("disable reference triggers: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM items WHERE id = $1`, epic.ID); err != nil {
		t.Fatalf("delete the parent: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `SET session_replication_role = origin`); err != nil {
		t.Fatalf("restore reference triggers: %v", err)
	}

	parent, children, err := f.st.GetItemFamily(ctx, f.t1, child.ID)
	if err != nil {
		t.Fatalf("family of an item whose parent is gone: %v", err)
	}
	if parent != nil {
		t.Fatalf("parent = %+v; want none", parent)
	}
	if len(children) != 0 {
		t.Fatalf("children = %+v; want none", children)
	}

	// The item itself is still readable: a missing parent is not a broken item.
	it, err := f.st.GetItem(ctx, f.t1, child.ID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if it.ParentID == nil || *it.ParentID != epic.ID {
		t.Fatalf("the item lost its parent link: %+v", it.ParentID)
	}
}
