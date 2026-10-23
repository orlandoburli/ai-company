package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

// The runner resolution: the variables, the node's choice, the budget guard
// and the recorded switch.
//
// These tests run against the application role, never the owner: a superuser
// bypasses row-level security even with FORCE, which would make every
// isolation assertion here vacuously true.

type resolutionFixture struct {
	t       *testing.T
	db      *sql.DB
	sc      tenancy.Scope
	other   tenancy.Scope
	store   *Store
	agents  *agent.Store
	runners *runner.Store
	work    *work.Store
}

func newResolutionFixture(t *testing.T) *resolutionFixture {
	t.Helper()
	db := pgtest.FreshDB(t).AsRole(t, "aic_app")
	db.SetMaxOpenConns(12)
	for _, id := range []string{"RES-T1", "RES-T2"} {
		if _, err := db.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`,
			id, "Tenant "+id, strings.ToLower(id), "RES-U"+id[len(id)-2:], strings.ToLower(id)+"@example.com",
			"x", "Owner "+id, "RES-M"+id[len(id)-2:]); err != nil {
			t.Fatal(err)
		}
	}
	return &resolutionFixture{
		t: t, db: db,
		sc:      tenancy.Scope{TenantID: "RES-T1", UserID: "RES-UT1", Role: tenancy.RoleOwner},
		other:   tenancy.Scope{TenantID: "RES-T2", UserID: "RES-UT2", Role: tenancy.RoleOwner},
		store:   NewStore(db),
		agents:  agent.NewStore(db),
		runners: runner.NewStore(db),
		work:    work.NewStore(db),
	}
}

func (f *resolutionFixture) cliRunner(sc tenancy.Scope, name, bin string) runner.Runner {
	f.t.Helper()
	r, err := f.runners.Create(context.Background(), sc, runner.NewRunner{
		Name: name, Kind: runner.KindCLI, Config: json.RawMessage(`{"bin":"` + bin + `","args":["-p"]}`),
	})
	if err != nil {
		f.t.Fatalf("create runner %q: %v", name, err)
	}
	return r
}

func (f *resolutionFixture) agent(sc tenancy.Scope, name string) agent.Agent {
	f.t.Helper()
	a, err := f.agents.CreateAgent(context.Background(), sc, agent.Agent{
		Name: name, Role: agent.RoleDev, Enabled: true, SoulMD: "You are " + name + ".", CreatedBy: &sc.UserID,
	})
	if err != nil {
		f.t.Fatalf("create agent %q: %v", name, err)
	}
	return a
}

// recordSpend writes the jobs a runner already ran: the budget is measured on
// the tenant's history, so the history is what the test writes.
func (f *resolutionFixture) recordSpend(sc tenancy.Scope, agentID, runnerID string, tokensIn, tokensOut int, cost float64) {
	f.t.Helper()
	err := f.store.withTenant(context.Background(), sc, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_jobs (id, tenant_id, agent_id, runner_id, runner_kind,
		                                     state, prompt, tokens_in, tokens_out, cost_usd, finished_at)
			VALUES ($1,$2,$3,$4,'cli','succeeded','spent',$5,$6,$7,now())`,
			"J-"+strings.ToUpper(uuid.NewString()), sc.TenantID, agentID, runnerID, tokensIn, tokensOut, cost)
		return err
	})
	if err != nil {
		f.t.Fatalf("record spend: %v", err)
	}
}

func (f *resolutionFixture) engine() *Engine {
	return NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Work: f.work,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// createRun starts a run with one agent node and the given variables.
func (f *resolutionFixture) createRun(vars map[string]string) Run {
	f.t.Helper()
	return f.createRunIn(f.sc, vars)
}

func (f *resolutionFixture) createRunIn(sc tenancy.Scope, vars map[string]string) Run {
	f.t.Helper()
	ctx := context.Background()
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "only", Kind: NodeAgent, Name: "only step", Type: "agent.worker", TypeVersion: 1,
			Config: json.RawMessage(`{"agent_id":"A","prompt_tmpl":"work"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{}}
	tpl, err := f.store.CreateTemplate(ctx, sc, NewTemplate{Name: "resolution-" + strings.ToUpper(uuid.NewString()[:8]), Graph: graph, CreatedBy: sc.UserID})
	if err != nil {
		f.t.Fatal(err)
	}
	run, err := f.engine().StartRun(ctx, sc, tpl.ID, "", sc.UserID, WithRunVariables(vars))
	if err != nil {
		f.t.Fatal(err)
	}
	return run
}

// --- the exported pieces ----------------------------------------------------

func TestMergeVariablesLetsTheRunWin(t *testing.T) {
	defaults := map[string]string{"RUNNER": "claude", "REGION": "eu"}
	runVars := map[string]string{"RUNNER": "codex"}

	got := MergeVariables(runVars, defaults)
	if got["RUNNER"] != "codex" {
		t.Errorf("RUNNER = %q, want the run's value (codex)", got["RUNNER"])
	}
	if got["REGION"] != "eu" {
		t.Errorf("REGION = %q, want the tenant's default (eu)", got["REGION"])
	}
	// The merge copies: the caller's maps are untouched.
	if runVars["REGION"] != "" || defaults["RUNNER"] != "claude" {
		t.Errorf("the merge mutated its inputs: run=%v defaults=%v", runVars, defaults)
	}
	if len(MergeVariables(nil, nil)) != 0 {
		t.Error("merging nothing produced something")
	}
}

func TestSpentIsTheBudgetReached(t *testing.T) {
	unlimited := runner.Runner{BudgetTokens: 0, BudgetCostUSD: 0}
	if Spent(unlimited, RunnerSpend{Tokens: 1 << 30, CostUSD: 1e6}) {
		t.Error("a runner without a budget is never spent")
	}
	tokens := runner.Runner{BudgetTokens: 100}
	if Spent(tokens, RunnerSpend{Tokens: 99}) {
		t.Error("99 of 100 tokens is not spent")
	}
	if !Spent(tokens, RunnerSpend{Tokens: 100}) {
		t.Error("100 of 100 tokens is spent")
	}
	if !Spent(tokens, RunnerSpend{Tokens: 101}) {
		t.Error("101 of 100 tokens is spent")
	}
	cost := runner.Runner{BudgetCostUSD: 1.5}
	if Spent(cost, RunnerSpend{CostUSD: 1.49}) {
		t.Error("1.49 of 1.50 USD is not spent")
	}
	if !Spent(cost, RunnerSpend{CostUSD: 1.5}) {
		t.Error("1.50 of 1.50 USD is spent")
	}
}

func TestWalkChainTakesTheNextRunnerThatMayRun(t *testing.T) {
	first := runner.Runner{ID: "R1", Name: "first", Enabled: true, BudgetTokens: 100}
	second := runner.Runner{ID: "R2", Name: "second", Enabled: false}
	third := runner.Runner{ID: "R3", Name: "third", Enabled: true}
	chain := []runner.Runner{first, second, third}

	spend := func(r runner.Runner) (RunnerSpend, error) {
		if r.ID == "R1" {
			return RunnerSpend{Tokens: 100}, nil
		}
		return RunnerSpend{}, nil
	}

	// The spent runner is skipped, the disabled one too: the third runs.
	got, ok, err := WalkChain(chain, &first, spend, nil)
	if err != nil || !ok || got.ID != "R3" {
		t.Fatalf("walk = %+v (ok %v, err %v), want the third runner", got, ok, err)
	}
	// The walk starts after the runner that was wanted.
	got, ok, err = WalkChain(chain, nil, spend, nil)
	if err != nil || !ok || got.ID != "R3" {
		t.Fatalf("walk from the top = %+v (ok %v, err %v), want the third runner", got, ok, err)
	}
	// A runner that was already tried is skipped.
	got, ok, err = WalkChain(chain, nil, nil, map[string]bool{"R3": true})
	if err != nil || !ok || got.ID != "R1" {
		t.Fatalf("walk with a tried runner = %+v (ok %v, err %v), want the first", got, ok, err)
	}
	// An exhausted chain says so.
	if _, ok, err = WalkChain(chain, &third, spend, nil); err != nil || ok {
		t.Fatalf("walk past the end = (ok %v, err %v), want no runner", ok, err)
	}
}

// --- the resolution ---------------------------------------------------------

func TestResolveRunnerTakesTheNodeChoice(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	first := f.cliRunner(f.sc, "first", "first")
	second := f.cliRunner(f.sc, "second", "second")
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	e := f.engine()
	node := Node{ID: "only", Kind: NodeAgent, Config: json.RawMessage(`{}`)}

	// No choice: the agent's first runner in the chain.
	got, err := e.ResolveRunner(ctx, f.sc, f.createRun(nil), node, a.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("no choice = %+v (err %v), want the chain's first runner", got, err)
	}

	// A name: that runner of the tenant.
	node.Config = json.RawMessage(`{"runner":"second"}`)
	got, err = e.ResolveRunner(ctx, f.sc, f.createRun(nil), node, a.ID)
	if err != nil || got.ID != second.ID {
		t.Fatalf("a literal = %+v (err %v), want the named runner", got, err)
	}

	// A variable: the variable's value, then that runner.
	node.Config = json.RawMessage(`{"runner":"$RUNNER"}`)
	got, err = e.ResolveRunner(ctx, f.sc, f.createRun(map[string]string{"RUNNER": "second"}), node, a.ID)
	if err != nil || got.ID != second.ID {
		t.Fatalf("a variable = %+v (err %v), want the runner the variable names", got, err)
	}

	// An unset variable falls through to the chain's first runner.
	got, err = e.ResolveRunner(ctx, f.sc, f.createRun(nil), node, a.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("an unset variable = %+v (err %v), want the chain's first runner", got, err)
	}

	// A runner that does not exist is an error the node reports.
	node.Config = json.RawMessage(`{"runner":"ghost"}`)
	if _, err = e.ResolveRunner(ctx, f.sc, f.createRun(nil), node, a.ID); err == nil {
		t.Fatal("a runner that does not exist was accepted")
	}
	node.Config = json.RawMessage(`{"runner":"$GHOST"}`)
	if _, err = e.ResolveRunner(ctx, f.sc, f.createRun(map[string]string{"GHOST": "ghost"}), node, a.ID); err == nil {
		t.Fatal("a variable naming a runner that does not exist was accepted")
	}
}

func TestResolveRunnerMergesTheRunOverTheTenantDefaults(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	first := f.cliRunner(f.sc, "first", "first")
	second := f.cliRunner(f.sc, "second", "second")
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetTenantVariables(ctx, f.sc, map[string]string{"RUNNER": "second"}); err != nil {
		t.Fatal(err)
	}
	e := f.engine()
	node := Node{ID: "only", Kind: NodeAgent, Config: json.RawMessage(`{"runner":"$RUNNER"}`)}

	// The tenant's default: the run inherits it.
	got, err := e.ResolveRunner(ctx, f.sc, f.createRun(nil), node, a.ID)
	if err != nil || got.ID != second.ID {
		t.Fatalf("the tenant default = %+v (err %v), want the default's runner", got, err)
	}
	// The run wins over the tenant's default.
	got, err = e.ResolveRunner(ctx, f.sc, f.createRun(map[string]string{"RUNNER": "first"}), node, a.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("the run's variable = %+v (err %v), want the run's runner", got, err)
	}
}

func TestResolveRunnerBudgetGuardSwitchesToTheNextRunner(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	first := f.cliRunner(f.sc, "first", "first")
	second := f.cliRunner(f.sc, "second", "second")
	budget := int64(100)
	if _, err := f.runners.Update(ctx, f.sc, first.ID, runner.Patch{BudgetTokens: &budget}); err != nil {
		t.Fatal(err)
	}
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	// The first runner already spent its budget: 60 in plus 60 out.
	f.recordSpend(f.sc, a.ID, first.ID, 60, 60, 0)

	e := f.engine()
	node := Node{ID: "only", Kind: NodeAgent, Config: json.RawMessage(`{}`)}
	run := f.createRun(nil)

	got, err := e.ResolveRunner(ctx, f.sc, run, node, a.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != second.ID {
		t.Fatalf("the spent runner was not skipped: got %s", got.Name)
	}
	// The switch is recorded in the run's variables: the rest of the run
	// follows the runner that is actually running.
	fresh, err := f.store.GetRun(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Variables["runner"] != "second" {
		t.Fatalf("the switch was not recorded: %v", fresh.Variables)
	}
}

func TestResolveRunnerBudgetExhaustedNamesTheRunner(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	first := f.cliRunner(f.sc, "first", "first")
	budget := int64(100)
	if _, err := f.runners.Update(ctx, f.sc, first.ID, runner.Patch{BudgetTokens: &budget}); err != nil {
		t.Fatal(err)
	}
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{first.ID}); err != nil {
		t.Fatal(err)
	}
	f.recordSpend(f.sc, a.ID, first.ID, 60, 60, 0)

	e := f.engine()
	node := Node{ID: "only", Kind: NodeAgent, Config: json.RawMessage(`{}`)}
	_, err := e.ResolveRunner(ctx, f.sc, f.createRun(nil), node, a.ID)
	if err == nil {
		t.Fatal("a spent runner with an empty chain was accepted")
	}
	if !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("the failure does not name the runner and the budget: %v", err)
	}
}

func TestResolveRunnerSkipsADisabledRunner(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	first := f.cliRunner(f.sc, "first", "first")
	second := f.cliRunner(f.sc, "second", "second")
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runners.SetEnabled(ctx, f.sc, first.ID, false); err != nil {
		t.Fatal(err)
	}
	e := f.engine()
	node := Node{ID: "only", Kind: NodeAgent, Config: json.RawMessage(`{}`)}
	got, err := e.ResolveRunner(ctx, f.sc, f.createRun(nil), node, a.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != second.ID {
		t.Fatalf("the disabled runner was not skipped: got %s", got.Name)
	}
}

func TestResolveRunnerIsTenantScoped(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	f.cliRunner(f.other, "foreign", "foreign")
	a := f.agent(f.sc, "dev")
	e := f.engine()
	node := Node{ID: "only", Kind: NodeAgent, Config: json.RawMessage(`{"runner":"foreign"}`)}
	// Another tenant's runner is invisible: the node cannot run on it.
	if _, err := e.ResolveRunner(ctx, f.sc, f.createRun(nil), node, a.ID); err == nil {
		t.Fatal("a runner of another tenant was resolved")
	}
	// The other tenant can still resolve it: the runner is not gone, it is
	// only invisible from here.
	foreignAgent := f.agent(f.other, "foreign-agent")
	if _, err := e.ResolveRunner(ctx, f.other, f.createRunIn(f.other, nil), node, foreignAgent.ID); err != nil {
		t.Fatalf("the owner tenant cannot resolve its own runner: %v", err)
	}
}

func TestResolveRunnerWithoutARunnerRegistry(t *testing.T) {
	f := newResolutionFixture(t)
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents})
	node := Node{ID: "only", Kind: NodeAgent, Config: json.RawMessage(`{}`)}
	_, err := e.ResolveRunner(context.Background(), f.sc, f.createRun(nil), node, "A")
	if !errors.Is(err, ErrRunnersNotConfigured) {
		t.Fatalf("err = %v, want ErrRunnersNotConfigured", err)
	}
}

// TestEngineWithoutARunnerRegistryFailsTheNode checks that the engine does
// not panic when the runner registry is not wired: the node fails with a
// capability failure that says why.
func TestEngineWithoutARunnerRegistryFailsTheNode(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	a := f.agent(f.sc, "dev")
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "only", Kind: NodeAgent, Name: "only step", Type: "agent.worker", TypeVersion: 1,
			Config: json.RawMessage(`{"agent_id":"` + a.ID + `","prompt_tmpl":"work"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{}}
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "no-runners-" + strings.ToUpper(uuid.NewString()[:8]), Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Work: f.work})
	run, err := e.StartRun(ctx, f.sc, tpl.ID, "", f.sc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, ok := latestStep(steps, "only", "")
	if !ok {
		t.Fatal("the step is missing")
	}
	if step.State != StepFailed || step.FailureKind != string(FailureCapability) {
		t.Fatalf("the step = %+v, want a capability failure", step)
	}
	if !strings.Contains(string(step.Result), "runners are not configured") {
		t.Fatalf("the failure does not say the runners are not configured: %s", step.Result)
	}
}

// --- the engine -------------------------------------------------------------

// TestEngineDispatchCarriesTheRunnerSnapshot checks that the job a node
// queues runs on the resolved runner, with the runner's config as it was when
// the job was queued.
func TestEngineDispatchCarriesTheRunnerSnapshot(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	r := f.cliRunner(f.sc, "claude", "claude")
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{r.ID}); err != nil {
		t.Fatal(err)
	}
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "only", Kind: NodeAgent, Name: "only step", Type: "agent.worker", TypeVersion: 1,
			Config: json.RawMessage(`{"agent_id":"` + a.ID + `","prompt_tmpl":"do the work"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{}}
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "snapshot", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	e := f.engine()
	run, err := e.StartRun(ctx, f.sc, tpl.ID, "", f.sc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, ok := latestStep(steps, "only", "")
	if !ok || step.AgentJobID == "" {
		t.Fatalf("the node was not dispatched: %+v", step)
	}
	job, err := f.agents.GetJob(ctx, f.sc, step.AgentJobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.RunnerID == nil || *job.RunnerID != r.ID {
		t.Fatalf("the job's runner = %v, want %s", job.RunnerID, r.ID)
	}
	if job.RunnerKind != "cli" {
		t.Fatalf("the job's runner kind = %q, want cli", job.RunnerKind)
	}
	var cfg map[string]any
	if err = json.Unmarshal(job.RunnerConfig, &cfg); err != nil || cfg["bin"] != "claude" {
		t.Fatalf("the job's runner config = %s, want the runner's config", job.RunnerConfig)
	}
	// The prompt names the runner it runs on.
	if !strings.Contains(job.Prompt, "You are running as the `claude` runner.") {
		t.Fatalf("the prompt does not name the runner:\n%s", job.Prompt)
	}
	// The step says which runner ran it.
	var result map[string]any
	if err = json.Unmarshal(step.Result, &result); err != nil || result["runner"] != "claude" {
		t.Fatalf("the step's result = %s, want it to name the runner", step.Result)
	}

	// Editing the runner afterwards does not change the queued job.
	newCfg := json.RawMessage(`{"bin":"codex","args":["-p"]}`)
	if _, err = f.runners.Update(ctx, f.sc, r.ID, runner.Patch{Config: &newCfg}); err != nil {
		t.Fatal(err)
	}
	job, err = f.agents.GetJob(ctx, f.sc, step.AgentJobID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(job.RunnerConfig), "claude") {
		t.Fatalf("the queued job followed the runner's edit: %s", job.RunnerConfig)
	}
}

// TestEngineCapabilityFallbackWalksTheRunnerChain checks the design's rule:
// a capability failure tries the agent's fallback chain — the runner chain —
// and only then fails the node.
func TestEngineCapabilityFallbackWalksTheRunnerChain(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	first := f.cliRunner(f.sc, "first", "first")
	second := f.cliRunner(f.sc, "second", "second")
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "only", Kind: NodeAgent, Name: "only step", Type: "agent.worker", TypeVersion: 1,
			Config: json.RawMessage(`{"agent_id":"` + a.ID + `","prompt_tmpl":"please fail"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{}}
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "fallback", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	e := f.engine()
	run, err := e.StartRun(ctx, f.sc, tpl.ID, "", f.sc.UserID)
	if err != nil {
		t.Fatal(err)
	}

	adapter := &engineAdapter{failureWhen: "please fail"}
	worker := agent.NewWorker(f.agents, agent.NewHub(), []agent.Adapter{adapter},
		agent.WithWorkerPoll(10*time.Millisecond), agent.WithWorkerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker.Start(wctx)

	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, ok := latestStep(steps, "only", "")
	if !ok || step.AgentJobID == "" {
		t.Fatalf("the node was not dispatched: %+v", step)
	}
	firstJob, err := f.agents.GetJob(ctx, f.sc, step.AgentJobID)
	if err != nil {
		t.Fatal(err)
	}
	if firstJob.RunnerID == nil || *firstJob.RunnerID != first.ID {
		t.Fatalf("the first job ran on %v, want the chain's first runner", firstJob.RunnerID)
	}
	waitJobState(t, f.agents, f.sc, firstJob.ID, agent.JobFailed)

	// The failure is a capability failure: the step tries the next runner of
	// the chain instead of failing.
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err = f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, ok = latestStep(steps, "only", "")
	if !ok {
		t.Fatal("the step disappeared")
	}
	if step.State == StepFailed {
		t.Fatalf("the node failed instead of trying the next runner: %+v", step)
	}
	if step.AgentJobID == firstJob.ID {
		t.Fatal("the fallback re-used the failed job")
	}
	secondJob, err := f.agents.GetJob(ctx, f.sc, step.AgentJobID)
	if err != nil {
		t.Fatal(err)
	}
	if secondJob.RunnerID == nil || *secondJob.RunnerID != second.ID {
		t.Fatalf("the fallback ran on %v, want the chain's second runner", secondJob.RunnerID)
	}
	// The old job stays for the audit trail.
	if old, err := f.agents.GetJob(ctx, f.sc, firstJob.ID); err != nil || old.State != agent.JobFailed {
		t.Fatalf("the failed job is gone: %+v (err %v)", old, err)
	}

	// The chain is exhausted: the node fails.
	waitJobState(t, f.agents, f.sc, secondJob.ID, agent.JobFailed)
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err = f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, ok = latestStep(steps, "only", "")
	if !ok {
		t.Fatal("the step disappeared")
	}
	if step.State != StepFailed || step.FailureKind != string(FailureCapability) {
		t.Fatalf("the exhausted chain = %+v, want a capability failure", step)
	}
}
