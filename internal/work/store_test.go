package work_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

// These tests run against the application role, never the owner: a superuser
// bypasses row-level security even with FORCE, which would make every isolation
// assertion here vacuously true.

type fixture struct {
	t     *testing.T
	app   *sql.DB
	owner *sql.DB // schema owner: only for assertions no application role may make
	st    *work.Store
	t1    tenancy.Scope
	t2    tenancy.Scope
	p1    work.Product
	pr1   work.Project
	p2    work.Product
	pr2   work.Project
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")

	for _, id := range []string{"T1", "T2"} {
		if _, err := app.Exec(`SELECT auth_register_tenant($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, "Tenant "+id, strings.ToLower(id), "U-"+id, strings.ToLower(id)+"@example.com",
			"x", "User "+id, "M-"+id); err != nil {
			t.Fatalf("seed tenant %s: %v", id, err)
		}
	}

	f := &fixture{
		t:     t,
		app:   app,
		owner: db.DB,
		st:    work.NewStore(app),
		t1:    tenancy.Scope{TenantID: "T1", UserID: "U-T1", Role: tenancy.RoleOwner},
		t2:    tenancy.Scope{TenantID: "T2", UserID: "U-T2", Role: tenancy.RoleOwner},
	}
	ctx := context.Background()

	var err error
	if f.p1, err = f.st.CreateProduct(ctx, f.t1, "Acme", "acme", "git@github.com:acme/app.git", ""); err != nil {
		t.Fatalf("create product T1: %v", err)
	}
	if f.pr1, err = f.st.CreateProject(ctx, f.t1, f.p1.ID, nil, "Core", "CORE"); err != nil {
		t.Fatalf("create project T1: %v", err)
	}
	if f.p2, err = f.st.CreateProduct(ctx, f.t2, "Globex", "globex", "", ""); err != nil {
		t.Fatalf("create product T2: %v", err)
	}
	if f.pr2, err = f.st.CreateProject(ctx, f.t2, f.p2.ID, nil, "Core", "CORE"); err != nil {
		t.Fatalf("create project T2: %v", err)
	}
	return f
}

func (f *fixture) item(sc tenancy.Scope, projectID, title string) work.Item {
	f.t.Helper()
	it, err := f.st.CreateItem(context.Background(), sc, work.NewItem{
		ProjectID: projectID, Kind: "task", Title: title, ActorUserID: sc.UserID,
	})
	if err != nil {
		f.t.Fatalf("create item %q: %v", title, err)
	}
	return it
}

func TestItemNumbersArePerTenant(t *testing.T) {
	f := newFixture(t)

	a := f.item(f.t1, f.pr1.ID, "first")
	b := f.item(f.t1, f.pr1.ID, "second")
	c := f.item(f.t2, f.pr2.ID, "other tenant first")

	if a.Number != 1 || b.Number != 2 {
		t.Fatalf("T1 numbers = %d, %d; want 1, 2", a.Number, b.Number)
	}
	// A per-tenant counter, not a global sequence: tenant 2 must start at 1 even
	// though tenant 1 already has two items.
	if c.Number != 1 {
		t.Fatalf("T2 first number = %d; want 1", c.Number)
	}
}

func TestContentIsIsolatedByRLS(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	mine := f.item(f.t1, f.pr1.ID, "tenant one secret")

	items, err := f.st.ListItems(ctx, f.t2, work.ItemFilter{})
	if err != nil {
		t.Fatalf("list items as T2: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("T2 sees %d T1 items; want 0", len(items))
	}

	if _, err := f.st.GetItem(ctx, f.t2, mine.ID); !errors.Is(err, work.ErrNotFound) {
		t.Fatalf("T2 get T1 item: err = %v; want ErrNotFound", err)
	}

	products, err := f.st.ListProducts(ctx, f.t2)
	if err != nil {
		t.Fatalf("list products as T2: %v", err)
	}
	for _, p := range products {
		if p.ID == f.p1.ID {
			t.Fatalf("T2 sees T1 product %s", p.ID)
		}
	}

	// Even a direct UPDATE by id must match nothing, because the row is invisible.
	projects, err := f.st.ListProjects(ctx, f.t2, f.p1.ID)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("T2 sees %d projects of T1 product; want 0", len(projects))
	}
}

func TestProductRepositoriesLifecycleAndTenantScope(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	initial, err := f.st.ListProductRepositories(ctx, f.t1, f.p1.ID)
	if err != nil || len(initial) != 1 || initial[0].Repo != "acme/app" || !initial[0].IsDefault {
		t.Fatalf("migrated repository list = %+v, %v", initial, err)
	}

	added, err := f.st.AddProductRepository(ctx, f.t1, f.p1.ID, "https://github.com/acme/mobile.git", "mobile", false)
	if err != nil || added.Repo != "acme/mobile" || added.Name != "mobile" || added.IsDefault {
		t.Fatalf("add repository = %+v, %v", added, err)
	}
	if _, err := f.st.AddProductRepository(ctx, f.t1, f.p1.ID, "ACME/MOBILE", "duplicate", false); !errors.Is(err, work.ErrConflict) {
		t.Fatalf("duplicate repository error = %v, want conflict", err)
	}
	if _, err := f.st.AddProductRepository(ctx, f.t1, f.p2.ID, "acme/other", "cross-tenant", false); !errors.Is(err, work.ErrNotFound) {
		t.Fatalf("other tenant product error = %v, want not found", err)
	}
	if _, err := f.st.ListProductRepositories(ctx, f.t2, f.p1.ID); !errors.Is(err, work.ErrNotFound) {
		t.Fatalf("foreign repository list error = %v, want not found", err)
	}

	if err := f.st.SetDefaultProductRepository(ctx, f.t1, f.p1.ID, added.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.st.ListProductRepositories(ctx, f.t1, f.p1.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("repositories after set default = %+v, %v", got, err)
	}
	defaults := 0
	for _, repository := range got {
		if repository.IsDefault {
			defaults++
			if repository.ID != added.ID {
				t.Fatalf("old default was not demoted: %+v", repository)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("default count = %d, want exactly one", defaults)
	}
	products, err := f.st.ListProducts(ctx, f.t1)
	if err != nil || products[0].RepoURL != "https://github.com/acme/mobile" || len(products[0].Repositories) != 2 {
		t.Fatalf("product compatibility shape = %+v, %v", products, err)
	}
	if err := f.st.RemoveProductRepository(ctx, f.t1, f.p1.ID, initial[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err = f.st.ListProductRepositories(ctx, f.t1, f.p1.ID)
	if err != nil || len(got) != 1 || got[0].ID != added.ID || !got[0].IsDefault {
		t.Fatalf("repositories after removal = %+v, %v", got, err)
	}
}

func TestDependencyRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a := f.item(f.t1, f.pr1.ID, "a")
	b := f.item(f.t1, f.pr1.ID, "b")
	c := f.item(f.t1, f.pr1.ID, "c")

	if _, err := f.st.CreateDependency(ctx, f.t1, a.ID, b.ID, f.t1.UserID); err != nil {
		t.Fatalf("a blocked_by b: %v", err)
	}
	if _, err := f.st.CreateDependency(ctx, f.t1, b.ID, c.ID, f.t1.UserID); err != nil {
		t.Fatalf("b blocked_by c: %v", err)
	}

	// c -> a would close a -> b -> c -> a.
	if _, err := f.st.CreateDependency(ctx, f.t1, c.ID, a.ID, f.t1.UserID); !errors.Is(err, work.ErrCycle) {
		t.Fatalf("cycle: err = %v; want ErrCycle", err)
	}

	if _, err := f.st.CreateDependency(ctx, f.t1, a.ID, a.ID, f.t1.UserID); !errors.Is(err, work.ErrSelfDependency) {
		t.Fatalf("self edge: err = %v; want ErrSelfDependency", err)
	}

	if _, err := f.st.CreateDependency(ctx, f.t1, a.ID, b.ID, f.t1.UserID); !errors.Is(err, work.ErrConflict) {
		t.Fatalf("duplicate edge: err = %v; want ErrConflict", err)
	}

	// A dependency must not span tenants even when both ids are real.
	theirs := f.item(f.t2, f.pr2.ID, "theirs")
	if _, err := f.st.CreateDependency(ctx, f.t1, a.ID, theirs.ID, f.t1.UserID); !errors.Is(err, work.ErrNotFound) {
		t.Fatalf("cross-tenant edge: err = %v; want ErrNotFound", err)
	}
}

func TestBoardClassification(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	parent := f.item(f.t1, f.pr1.ID, "parent")
	blocker := f.item(f.t1, f.pr1.ID, "blocker")
	blocked := f.item(f.t1, f.pr1.ID, "blocked")

	if _, err := f.st.UpdateItem(ctx, f.t1, blocked.ID, work.ItemPatch{
		ParentID: strptr(parent.ID),
	}, f.t1.UserID); err != nil {
		t.Fatalf("set parent: %v", err)
	}
	if _, err := f.st.CreateDependency(ctx, f.t1, blocked.ID, blocker.ID, f.t1.UserID); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	byID := map[string]work.Item{}
	items, err := f.st.ListItems(ctx, f.t1, work.ItemFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, it := range items {
		byID[it.ID] = it
	}

	if !byID[parent.ID].IsEpic {
		t.Fatalf("parent not classified as epic: %+v", byID[parent.ID])
	}
	if byID[blocker.ID].IsEpic {
		t.Fatalf("blocker classified as epic")
	}
	if !byID[blocked.ID].Blocked {
		t.Fatalf("blocked item not classified as blocked")
	}

	// Closing the blocker clears the classification without deleting the edge.
	if _, err := f.st.UpdateItem(ctx, f.t1, blocker.ID, work.ItemPatch{
		Status: strptr("done"),
	}, f.t1.UserID); err != nil {
		t.Fatalf("close blocker: %v", err)
	}
	it, err := f.st.GetItem(ctx, f.t1, blocked.ID)
	if err != nil {
		t.Fatalf("get blocked: %v", err)
	}
	if it.Blocked {
		t.Fatalf("item still blocked after its blocker closed")
	}
}

func TestActivityLogIsAppendOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	it := f.item(f.t1, f.pr1.ID, "logged")
	entries, err := f.st.ListActivity(ctx, f.t1, it.ID, 10)
	if err != nil {
		t.Fatalf("list activity: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "item.created" {
		t.Fatalf("activity = %+v; want one item.created", entries)
	}
	if entries[0].ActorKind != "user" || entries[0].ActorID == nil || *entries[0].ActorID != f.t1.UserID {
		t.Fatalf("activity not attributed: %+v", entries[0])
	}

	// The app role has no UPDATE/DELETE privilege on the log at all.
	if _, err := f.app.Exec(`UPDATE activity_log SET action = 'tampered'`); err == nil {
		t.Fatal("app role could update the activity log")
	}
	if _, err := f.app.Exec(`DELETE FROM activity_log`); err == nil {
		t.Fatal("app role could delete from the activity log")
	}
}

func TestPriorityNormalisation(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"p0", "priority:p0"},
		{"high", "priority:p0"},
		{"P1", "priority:p1"},
		{"medium", "priority:p1"},
		{"priority:p2", "priority:p2"},
		{"low", "priority:p2"},
	}
	for _, c := range cases {
		got, err := work.NormalizePriority(&c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got == nil || *got != c.want {
			t.Fatalf("%q -> %v; want %q", c.in, got, c.want)
		}
	}
	for _, clear := range []string{"", "none"} {
		got, err := work.NormalizePriority(&clear)
		if err != nil || got != nil {
			t.Fatalf("%q: got %v, %v; want nil, nil", clear, got, err)
		}
	}
	if _, err := work.NormalizePriority(strptr("urgent")); !errors.Is(err, work.ErrInvalid) {
		t.Fatalf("unknown priority: err = %v; want ErrInvalid", err)
	}
}

func strptr(s string) *string { return &s }

func TestPauseIsAttributedAndSurvivesRestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	it := f.item(f.t1, f.pr1.ID, "pause me")
	if _, err := f.st.UpdateItem(ctx, f.t1, it.ID, work.ItemPatch{Priority: strptr("p1")}, f.t1.UserID); err != nil {
		t.Fatalf("set priority: %v", err)
	}

	paused, err := f.st.PauseItem(ctx, f.t1, it.ID, "waiting on vendor", f.t1.UserID)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.PausedAt == nil || paused.PausedBy == nil || *paused.PausedBy != f.t1.UserID {
		t.Fatalf("pause not attributed: %+v", paused)
	}
	if paused.PauseReason != "waiting on vendor" {
		t.Fatalf("reason = %q", paused.PauseReason)
	}
	// Pause is eligibility, not content: the label is untouched.
	if paused.Priority == nil || *paused.Priority != "priority:p1" {
		t.Fatalf("priority changed by pause: %v", paused.Priority)
	}

	// A new store over the same database is the restart: the pause is a column,
	// not scheduler memory.
	afterRestart := work.NewStore(f.app)
	reloaded, err := afterRestart.GetItem(ctx, f.t1, it.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.PausedAt == nil || !reloaded.Paused {
		t.Fatalf("pause did not survive reload: %+v", reloaded)
	}

	// And it is excluded from dispatch while a pin cannot change that.
	queue, err := afterRestart.Queue(ctx, f.t1, work.ItemFilter{})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	for _, e := range queue {
		if e.Item.ID == it.ID {
			t.Fatalf("paused item appeared in the queue")
		}
	}

	if _, err := afterRestart.PinItem(ctx, f.t1, it.ID, f.t1.UserID); err != nil {
		t.Fatalf("pin: %v", err)
	}
	queue, err = afterRestart.Queue(ctx, f.t1, work.ItemFilter{})
	if err != nil {
		t.Fatalf("queue after pin: %v", err)
	}
	for _, e := range queue {
		if e.Item.ID == it.ID {
			t.Fatalf("pinned paused item appeared in the queue; pause must outrank pin")
		}
	}

	if _, err := afterRestart.UnpauseItem(ctx, f.t1, it.ID, f.t1.UserID); err != nil {
		t.Fatalf("unpause: %v", err)
	}
	queue, err = afterRestart.Queue(ctx, f.t1, work.ItemFilter{})
	if err != nil {
		t.Fatalf("queue after unpause: %v", err)
	}
	var found bool
	for _, e := range queue {
		if e.Item.ID == it.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("unpaused item did not return to the queue")
	}

	// Every override left a log entry with the actor.
	entries, err := afterRestart.ListActivity(ctx, f.t1, it.ID, 50)
	if err != nil {
		t.Fatalf("activity: %v", err)
	}
	actions := map[string]bool{}
	for _, e := range entries {
		if e.ActorID == nil || *e.ActorID != f.t1.UserID {
			t.Fatalf("override entry not attributed: %+v", e)
		}
		actions[e.Action] = true
	}
	for _, want := range []string{"item.paused", "item.pinned", "item.unpaused"} {
		if !actions[want] {
			t.Fatalf("missing %s in activity: %v", want, actions)
		}
	}
}

func TestPinnedOutranksHigherScoreAndNewestPinWins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	high := f.item(f.t1, f.pr1.ID, "high priority")
	if _, err := f.st.UpdateItem(ctx, f.t1, high.ID, work.ItemPatch{Priority: strptr("p0")}, f.t1.UserID); err != nil {
		t.Fatalf("set p0: %v", err)
	}
	low := f.item(f.t1, f.pr1.ID, "low priority")
	if _, err := f.st.UpdateItem(ctx, f.t1, low.ID, work.ItemPatch{Priority: strptr("p2")}, f.t1.UserID); err != nil {
		t.Fatalf("set p2: %v", err)
	}

	front := func() string {
		t.Helper()
		queue, err := f.st.Queue(ctx, f.t1, work.ItemFilter{})
		if err != nil {
			t.Fatalf("queue: %v", err)
		}
		if len(queue) == 0 {
			t.Fatalf("queue is empty")
		}
		return queue[0].Item.ID
	}

	if got := front(); got != high.ID {
		t.Fatalf("unpinned front = %s; want the p0 item", got)
	}

	if _, err := f.st.PinItem(ctx, f.t1, low.ID, f.t1.UserID); err != nil {
		t.Fatalf("pin low: %v", err)
	}
	if got := front(); got != low.ID {
		t.Fatalf("after pinning low, front = %s; want low", got)
	}

	if _, err := f.st.PinItem(ctx, f.t1, high.ID, f.t1.UserID); err != nil {
		t.Fatalf("pin high: %v", err)
	}
	if got := front(); got != high.ID {
		t.Fatalf("most recent pin did not win: front = %s", got)
	}

	// Closing clears the pin, so a closed item cannot keep occupying rank 1.
	if _, err := f.st.UpdateItem(ctx, f.t1, high.ID, work.ItemPatch{Status: strptr("done")}, f.t1.UserID); err != nil {
		t.Fatalf("close: %v", err)
	}
	closed, err := f.st.GetItem(ctx, f.t1, high.ID)
	if err != nil {
		t.Fatalf("get closed: %v", err)
	}
	if closed.PinnedAt != nil || closed.PinnedBy != nil {
		t.Fatalf("pin survived closure: %+v", closed)
	}
	if got := front(); got != low.ID {
		t.Fatalf("after closing the pin, front = %s; want low", got)
	}
}

func TestScoreInheritsPriorityAndCountsUnblocks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	epic := f.item(f.t1, f.pr1.ID, "epic")
	if _, err := f.st.UpdateItem(ctx, f.t1, epic.ID, work.ItemPatch{Priority: strptr("p0")}, f.t1.UserID); err != nil {
		t.Fatalf("set epic priority: %v", err)
	}

	child := f.item(f.t1, f.pr1.ID, "child")
	if _, err := f.st.UpdateItem(ctx, f.t1, child.ID, work.ItemPatch{
		ParentID: strptr(epic.ID),
	}, f.t1.UserID); err != nil {
		t.Fatalf("parent child: %v", err)
	}

	score, _, err := f.st.ScoreItem(ctx, f.t1, child.ID)
	if err != nil {
		t.Fatalf("score child: %v", err)
	}
	var inherited int
	for _, factor := range score.Factors {
		if factor.Key == "priority" {
			inherited = factor.Contribution
		}
	}
	if inherited != 100 {
		t.Fatalf("child did not inherit p0: priority contribution = %d", inherited)
	}

	// Root <- a <- b: the root transitively unblocks two open items.
	a := f.item(f.t1, f.pr1.ID, "a")
	b := f.item(f.t1, f.pr1.ID, "b")
	if _, err := f.st.CreateDependency(ctx, f.t1, a.ID, epic.ID, f.t1.UserID); err != nil {
		t.Fatalf("a blocked by epic: %v", err)
	}
	if _, err := f.st.CreateDependency(ctx, f.t1, b.ID, a.ID, f.t1.UserID); err != nil {
		t.Fatalf("b blocked by a: %v", err)
	}

	score, _, err = f.st.ScoreItem(ctx, f.t1, epic.ID)
	if err != nil {
		t.Fatalf("score epic: %v", err)
	}
	var unblocks int
	for _, factor := range score.Factors {
		if factor.Key == "unblocks" {
			unblocks = factor.Contribution
		}
	}
	if unblocks != 2*25 {
		t.Fatalf("unblocks contribution = %d; want %d", unblocks, 2*25)
	}
}
