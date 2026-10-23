package usage_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/usage"
	"github.com/orlandoburli/ai-company/internal/work"
)

type fixture struct {
	t      *testing.T
	app    *sql.DB
	work   *work.Store
	agents *agent.Store
	t1     tenancy.Scope
	t2     tenancy.Scope
	p1     work.Project
	p2     work.Project
	item1  work.Item
	item2  work.Item
	agent1 agent.Agent
	agent2 agent.Agent
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	app.SetMaxOpenConns(6)
	for _, id := range []string{"U1", "U2"} {
		if _, err := app.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`,
			"T"+id, "Tenant "+id, strings.ToLower(id), id, strings.ToLower(id)+"@example.com", "x", "User "+id, "M"+id); err != nil {
			t.Fatalf("register tenant %s: %v", id, err)
		}
	}
	f := &fixture{
		t: t, app: app, work: work.NewStore(app), agents: agent.NewStore(app),
		t1: tenancy.Scope{TenantID: "TU1", UserID: "U1", Role: tenancy.RoleOwner},
		t2: tenancy.Scope{TenantID: "TU2", UserID: "U2", Role: tenancy.RoleOwner},
	}
	ctx := context.Background()
	prod1, err := f.work.CreateProduct(ctx, f.t1, "Acme", "acme", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.work.CreateProduct(ctx, f.t1, "Empty product", "empty-product", "", ""); err != nil {
		t.Fatal(err)
	}
	f.p1, err = f.work.CreateProject(ctx, f.t1, prod1.ID, nil, "Core", "CORE")
	if err != nil {
		t.Fatal(err)
	}
	prod2, err := f.work.CreateProduct(ctx, f.t2, "Globex", "globex", "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.p2, err = f.work.CreateProject(ctx, f.t2, prod2.ID, nil, "Core", "CORE")
	if err != nil {
		t.Fatal(err)
	}
	f.item1 = f.createItem(f.t1, f.p1.ID, "Acme item")
	f.item2 = f.createItem(f.t2, f.p2.ID, "Globex item")
	f.agent1 = f.createAgent(f.t1, "agent-one")
	f.agent2 = f.createAgent(f.t2, "agent-two")
	return f
}

func (f *fixture) createItem(sc tenancy.Scope, projectID, title string) work.Item {
	f.t.Helper()
	it, err := f.work.CreateItem(context.Background(), sc, work.NewItem{
		ProjectID: projectID, Kind: "task", Title: title, ActorUserID: sc.UserID,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return it
}

func (f *fixture) createAgent(sc tenancy.Scope, name string) agent.Agent {
	f.t.Helper()
	a, err := f.agents.CreateAgent(context.Background(), sc, agent.Agent{
		Name: name, Role: agent.RoleDev, Kind: agent.KindCLI, SoulMD: "Test agent", Enabled: true,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

type jobSeed struct {
	state     string
	created   time.Time
	started   time.Time
	finished  time.Time
	tokensIn  int
	tokensOut int
	cost      float64
	failure   bool
	itemID    string
}

func (f *fixture) addJob(sc tenancy.Scope, a agent.Agent, seed jobSeed) string {
	f.t.Helper()
	ctx := context.Background()
	stepID := fmt.Sprintf("STEP-%s", strings.ToUpper(agentIDShort(a.ID)+time.Now().Format("150405.000000000")))
	runID := "RUN-" + stepID
	templateID := "TPL-" + sc.TenantID
	if err := f.inTenant(sc, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workflow_templates (id, tenant_id, name, graph_json)
			VALUES ($1,$2,'usage-test','{}'::jsonb) ON CONFLICT (id) DO NOTHING`, templateID, sc.TenantID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workflow_runs (id, tenant_id, template_id, template_version, item_id, definition_snapshot_json)
			VALUES ($1,$2,$3,1,nullif($4,''),'{}'::jsonb)`, runID, sc.TenantID, templateID, seed.itemID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO step_runs (id, tenant_id, run_id, node_id, kind, name)
			VALUES ($1,$2,$3,'node','agent','Implement')`, stepID, sc.TenantID, runID)
		return err
	}); err != nil {
		f.t.Fatalf("create workflow context: %v", err)
	}
	job, err := f.agents.CreateJob(ctx, sc, agent.NewJob{
		AgentID: a.ID, ItemID: seed.itemID, StepRunID: stepID, Prompt: "run usage test",
	})
	if err != nil {
		f.t.Fatalf("create job: %v", err)
	}
	if err := f.inTenant(sc, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE agent_jobs
			   SET state = $2, created_at = $3, started_at = $4, finished_at = $5,
			       tokens_in = $6, tokens_out = $7, cost_usd = $8,
			       failure_kind = CASE WHEN $9 THEN 'test_failure' ELSE NULL END,
			       claim_lock = CASE WHEN $2 = 'running' THEN 'usage-test' ELSE NULL END,
			       claim_expires_at = CASE WHEN $2 = 'running' THEN now() + interval '1 hour' ELSE NULL END
			 WHERE id = $1`, job.ID, seed.state, seed.created, nullTime(seed.started), nullTime(seed.finished),
			seed.tokensIn, seed.tokensOut, seed.cost, seed.failure)
		return err
	}); err != nil {
		f.t.Fatalf("set job usage: %v", err)
	}
	return job.ID
}

func (f *fixture) inTenant(sc tenancy.Scope, fn func(*sql.Tx) error) error {
	f.t.Helper()
	ctx := context.Background()
	tx, err := f.app.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, sc.TenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func agentIDShort(id string) string {
	if len(id) > 5 {
		return id[:5]
	}
	return id
}

func TestAggregateTenantWindowAndJobUsage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	anchor := time.Now().UTC().Truncate(time.Second)
	since, until := anchor, anchor.Add(5*time.Hour)
	item := f.item1.ID
	base := jobSeed{state: "succeeded", itemID: item, created: since, started: since.Add(time.Minute), finished: since.Add(3 * time.Minute), tokensIn: 100, tokensOut: 40, cost: .25}
	jobID := f.addJob(f.t1, f.agent1, base)
	failed := base
	failed.state, failed.created = "failed", since.Add(time.Hour)
	failed.started, failed.finished = since.Add(time.Hour), since.Add(time.Hour+2*time.Minute)
	failed.tokensIn, failed.tokensOut, failed.cost, failed.failure = 30, 10, .10, true
	f.addJob(f.t1, f.agent1, failed)
	running := base
	running.state, running.created = "running", since.Add(2*time.Hour)
	running.finished, running.started = time.Time{}, since.Add(2*time.Hour)
	running.tokensIn, running.tokensOut, running.cost = 7, 3, .05
	f.addJob(f.t1, f.agent1, running)
	chat := base
	chat.itemID, chat.created = "", since.Add(30*time.Minute)
	f.addJob(f.t1, f.agent1, chat)
	before := base
	before.created = since.Add(-time.Second)
	f.addJob(f.t1, f.agent1, before)
	after := base
	after.created = until
	f.addJob(f.t1, f.agent1, after)
	foreign := base
	foreign.itemID, foreign.created = f.item2.ID, since.Add(time.Minute)
	f.addJob(f.t2, f.agent2, foreign)

	buckets, err := usage.Aggregate(ctx, f.app, f.t1, usage.Options{Since: since, Until: until})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 {
		t.Fatalf("got %d buckets, want only Acme with jobs: %+v", len(buckets), buckets)
	}
	b := buckets[0]
	if b.ProductName != "Acme" || b.Jobs != 3 || b.TokensIn != 137 || b.TokensOut != 53 || b.CostUSD != .4 || b.Failures != 1 {
		t.Fatalf("unexpected totals (chat/window/foreign jobs must not count): %+v", b)
	}
	if b.WallTime != 4*time.Minute {
		t.Fatalf("wall time = %s, want 4m (running job is skipped)", b.WallTime)
	}
	if b.ProductID == "" {
		t.Fatal("bucket omitted product id")
	}

	other, err := usage.Aggregate(ctx, f.app, f.t2, usage.Options{Since: since, Until: until})
	if err != nil || len(other) != 1 || other[0].Jobs != 1 {
		t.Fatalf("foreign tenant's own aggregate: %+v, %v", other, err)
	}
	// Product filters are honored, while products without jobs are omitted.
	filtered, err := usage.Aggregate(ctx, f.app, f.t1, usage.Options{Since: since, Until: until, ProductID: "no-such-product"})
	if err != nil || len(filtered) != 0 {
		t.Fatalf("empty product filter result = %+v, %v", filtered, err)
	}

	jobs, err := usage.PerJob(ctx, f.app, f.t1, item, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 5 {
		t.Fatalf("per-job returned %d item jobs, want 5 (it is not window-filtered): %+v", len(jobs), jobs)
	}
	var found, foundFailure, foundRunning bool
	for _, j := range jobs {
		if j.ID == jobID {
			found = true
			if j.Agent != "agent-one" || j.Step != "Implement" || j.TokensIn != 100 || j.TokensOut != 40 || j.CostUSD != .25 || j.WallTime != 2*time.Minute || j.Failure {
				t.Fatalf("unexpected per-job usage: %+v", j)
			}
		}
		if j.State == "failed" {
			foundFailure = j.Failure && j.WallTime == 2*time.Minute
		}
		if j.State == "running" {
			foundRunning = j.WallTime == 0
		}
	}
	if !found {
		t.Fatalf("job %s absent from per-job rows: %+v", jobID, jobs)
	}
	if !foundFailure || !foundRunning {
		t.Fatalf("failure/running usage rows not represented correctly: %+v", jobs)
	}
}

func TestAggregateRequiresTenantScope(t *testing.T) {
	f := setup(t)
	if _, err := usage.Aggregate(context.Background(), f.app, tenancy.Scope{}, usage.Options{}); err == nil {
		t.Fatal("aggregation accepted an empty tenant scope")
	}
}
