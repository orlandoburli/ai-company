package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/failure"
	"github.com/orlandoburli/ai-company/internal/priority"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

type testTenants []Tenant

func (ts testTenants) DispatchTenants(context.Context) ([]Tenant, error) { return ts, nil }

type testCapacity struct {
	global int
	agents map[string]int
}

func (c *testCapacity) RunningJobs(context.Context) (int, error) { return c.global, nil }
func (c *testCapacity) RunningAgentJobs(_ context.Context, _ tenancy.Scope, id string) (int, error) {
	return c.agents[id], nil
}

type testActivity struct {
	mu    sync.Mutex
	items []string
}

func (a *testActivity) RecordRunStarted(_ context.Context, _ tenancy.Scope, item, _, _, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.items = append(a.items, item)
	return nil
}

type dispatchFixture struct {
	app       *sql.DB
	scope     tenancy.Scope
	tenant    Tenant
	work      *work.Store
	workflows *workflow.Store
	agents    *agent.Store
	engine    *workflow.Engine
	capacity  *testCapacity
	activity  *testActivity
	projectID string
	agentID   string
	template  workflow.TemplateRow
}

func newDispatchFixture(t *testing.T, agentLimit int) *dispatchFixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	app.SetMaxOpenConns(8)
	const tenantID, userID = "DISPATCH-T1", "DISPATCH-U1"
	if _, err := app.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`, tenantID, "Dispatch", "dispatch-test", userID, "dispatch@example.com", "x", "Owner", "DISPATCH-M1"); err != nil {
		t.Fatal(err)
	}
	scope := tenancy.Scope{TenantID: tenantID, UserID: userID, Role: tenancy.RoleOwner}
	w := work.NewStore(app)
	product, err := w.CreateProduct(context.Background(), scope, "Test", "dispatch", "", "")
	if err != nil {
		t.Fatal(err)
	}
	project, err := w.CreateProject(context.Background(), scope, product.ID, nil, "Test", "TEST")
	if err != nil {
		t.Fatal(err)
	}
	a := agent.NewStore(app)
	createdBy := userID
	agentRow, err := a.CreateAgent(context.Background(), scope, agent.Agent{
		Name: "dispatch-agent", Role: agent.RoleDev, Kind: agent.KindCLI,
		ConcurrencyLimit: agentLimit, Enabled: true, CreatedBy: &createdBy,
	})
	if err != nil {
		t.Fatal(err)
	}
	wfs := workflow.NewStore(app)
	// The template has to begin and end to be published at all.
	graph := workflow.Template{SchemaVersion: 1, Nodes: []workflow.Node{
		{ID: "start", Kind: workflow.NodeStart, Name: "Start", Type: "flow.start", TypeVersion: 1, Config: json.RawMessage(`{}`), OnError: workflow.OnErrorStop, Workspace: workflow.WorkspaceScratch},
		{ID: "work", Kind: workflow.NodeAgent, Name: "Work", Type: "agent.worker", TypeVersion: 1,
			Config:  json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, agentRow.ID)),
			OnError: workflow.OnErrorStop, Workspace: workflow.WorkspaceScratch},
		{ID: "end", Kind: workflow.NodeEnd, Name: "End", Type: "flow.end", TypeVersion: 1, Config: json.RawMessage(`{}`), OnError: workflow.OnErrorStop, Workspace: workflow.WorkspaceScratch},
	}, Edges: []workflow.Edge{
		{ID: "start-work", From: workflow.PortRef{Node: "start", Port: "main"}, To: workflow.PortRef{Node: "work", Port: "in"}},
		{ID: "work-end", From: workflow.PortRef{Node: "work", Port: "main"}, To: workflow.PortRef{Node: "end", Port: "in"}},
	}}
	tpl, err := wfs.CreateTemplate(context.Background(), scope, workflow.NewTemplate{Name: "dispatch", AppliesTo: "task", Graph: graph, CreatedBy: userID})
	if err != nil {
		t.Fatal(err)
	}
	tpl, err = wfs.PublishTemplate(context.Background(), scope, tpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &dispatchFixture{
		app: app, scope: scope, tenant: Tenant{Scope: scope, OwnerUserID: userID}, work: w,
		workflows: wfs, agents: a, engine: workflow.NewEngine(workflow.EngineDeps{Store: wfs, Work: w, Agents: a}),
		capacity: &testCapacity{agents: map[string]int{}}, activity: &testActivity{},
		projectID: project.ID, agentID: agentRow.ID, template: tpl,
	}
}

func (f *dispatchFixture) newItem(t *testing.T, title string, priority string) work.Item {
	t.Helper()
	var p *string
	if priority != "" {
		p = &priority
	}
	it, err := f.work.CreateItem(context.Background(), f.scope, work.NewItem{
		ProjectID: f.projectID, Kind: "task", Title: title, Priority: p, ActorUserID: f.scope.UserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (f *dispatchFixture) dispatcher(workers int) *Dispatcher {
	return New(Deps{
		Work: f.work, Workflows: f.workflows, Engine: f.engine, Agents: f.agents,
		Tenants: testTenants{f.tenant}, Capacity: f.capacity, Activity: f.activity,
	}, Config{GlobalWorkers: workers})
}

func TestOnceStartsQueueHeadUnderGlobalCapAndIsIdempotent(t *testing.T) {
	f := newDispatchFixture(t, 4)
	first := f.newItem(t, "first", "priority:p0")
	second := f.newItem(t, "second", "priority:p1")
	f.newItem(t, "third", "priority:p2")

	result, err := f.dispatcher(1).Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Started) != 1 || result.Started[0] != first.ID {
		t.Fatalf("started %v, want only queue head %s", result.Started, first.ID)
	}
	if !hasSkip(result.Skipped, second.ID, "global worker pool is full") {
		t.Fatalf("second item was not skipped at global cap: %+v", result.Skipped)
	}
	if result.Considered != 3 {
		t.Fatalf("considered=%d, want 3", result.Considered)
	}
	result, err = f.dispatcher(1).Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasString(result.Started, first.ID) {
		t.Fatalf("active item %s was started twice: %+v", first.ID, result)
	}
	if !hasSkip(result.Skipped, first.ID, "active run") {
		t.Fatalf("active item skip missing: %+v", result.Skipped)
	}
}

func TestOnceRespectsGlobalAndPerAgentBusyCapacity(t *testing.T) {
	f := newDispatchFixture(t, 1)
	it := f.newItem(t, "item", "")
	d := f.dispatcher(2)
	f.capacity.global = 2
	result, err := d.Once(context.Background())
	if err != nil || len(result.Started) != 0 || !hasSkip(result.Skipped, it.ID, "global worker pool is full") {
		t.Fatalf("global busy result=%+v err=%v", result, err)
	}
	f.capacity.global = 0
	f.capacity.agents[f.agentID] = 1
	result, err = d.Once(context.Background())
	if err != nil || len(result.Started) != 0 || !hasSkip(result.Skipped, it.ID, "first agent is at its concurrency limit") {
		t.Fatalf("agent busy result=%+v err=%v", result, err)
	}
}

func TestOnceFiltersIneligibleAndMissingTemplateItems(t *testing.T) {
	f := newDispatchFixture(t, 10)
	paused := f.newItem(t, "paused", "priority:p0")
	if _, err := f.work.PauseItem(context.Background(), f.scope, paused.ID, "later", f.scope.UserID); err != nil {
		t.Fatal(err)
	}
	blocker := f.newItem(t, "blocker", "")
	blocked := f.newItem(t, "blocked", "priority:p0")
	if _, err := f.work.CreateDependency(context.Background(), f.scope, blocked.ID, blocker.ID, f.scope.UserID); err != nil {
		t.Fatal(err)
	}
	unsupported, err := f.work.CreateItem(context.Background(), f.scope, work.NewItem{ProjectID: f.projectID, Kind: "chore", Title: "unsupported", ActorUserID: f.scope.UserID})
	if err != nil {
		t.Fatal(err)
	}
	// One active candidate proves blocked/paused gates do not affect the rest.
	result, err := f.dispatcher(10).Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasString(result.Started, paused.ID) || hasString(result.Started, blocked.ID) {
		t.Fatalf("ineligible item started: %+v", result.Started)
	}
	if !hasSkip(result.Skipped, unsupported.ID, "no template for this kind") {
		t.Fatalf("missing-template reason absent: %+v", result.Skipped)
	}
}

func TestSortQueuePinThenRankThenBaseScore(t *testing.T) {
	newEntry := func(id string, score int, rank *int, pinned *time.Time) work.QueueEntry {
		return work.QueueEntry{Item: work.Item{ID: id}, Score: priorityScore(score), Rank: rank, PinnedAt: pinned}
	}
	one, two := 1, 2
	old := time.Unix(10, 0)
	newer := old.Add(time.Second)
	entries := []work.QueueEntry{
		newEntry("base-high", 900, nil, nil), newEntry("rank-two", 10, &two, nil),
		newEntry("pin-old", 0, &two, &old), newEntry("rank-one", 1, &one, nil),
		newEntry("pin-new", 0, &one, &newer),
	}
	sortQueue(entries)
	want := []string{"pin-new", "pin-old", "rank-one", "rank-two", "base-high"}
	for i := range want {
		if entries[i].Item.ID != want[i] {
			t.Fatalf("order=%v, want %v", itemIDs(entries), want)
		}
	}
}

func TestOnceNeverStartsBackoffOrExhaustedItems(t *testing.T) {
	f := newDispatchFixture(t, 10)
	backing := f.newItem(t, "backing off", "priority:p0")
	exhausted := f.newItem(t, "exhausted", "priority:p0")
	params := failure.Params{XSeconds: 60, Y: 2, MaxBackoffSeconds: 600, MaxFailures: 1}
	fs := failure.NewStore(f.app)
	if _, err := fs.Record(context.Background(), f.scope, failure.Record{ItemID: backing.ID, WorkflowID: f.template.ID, Curve: failure.CurveExponential, Params: params, Version: "bo-exp-1", Kind: "transient"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Record(context.Background(), f.scope, failure.Record{ItemID: exhausted.ID, WorkflowID: f.template.ID, Curve: failure.CurveExponential, Params: params, Version: "bo-exp-1", Kind: "transient"}); err != nil {
		t.Fatal(err)
	}
	// The max-failures setting of one makes the first recorded failure exhaust
	// immediately; create the wait as a separate item with a higher ceiling.
	// Record a future eligibility timestamp for the backing-off row.
	if err := f.updateFailure(backing.ID, `next_eligible_at=now()+interval '1 hour', state='backing_off'`); err != nil {
		t.Fatal(err)
	}
	if err := f.updateFailure(exhausted.ID, `state='exhausted', next_eligible_at=NULL`); err != nil {
		t.Fatal(err)
	}
	result, err := f.dispatcher(10).Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasString(result.Started, backing.ID) || hasString(result.Started, exhausted.ID) {
		t.Fatalf("backoff/exhausted item started: %+v", result.Started)
	}
}

func (f *dispatchFixture) updateFailure(itemID, set string) error {
	tx, err := f.app.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id',$1,true)`, f.scope.TenantID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE item_failures SET `+set+` WHERE item_id=$1`, itemID); err != nil {
		return err
	}
	return tx.Commit()
}

func hasSkip(skips []Skip, id, reason string) bool {
	for _, skip := range skips {
		if skip.ItemID == id && skip.Reason == reason {
			return true
		}
	}
	return false
}
func hasString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func itemIDs(entries []work.QueueEntry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Item.ID
	}
	return out
}

// priorityScore keeps this test focused on queue precedence rather than the
// scoring algorithm.
func priorityScore(value int) priority.Score { return priority.Score{Value: value} }
