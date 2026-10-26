package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

type engineFixture struct {
	db      *sql.DB
	sc      tenancy.Scope
	store   *Store
	agents  *agent.Store
	runners *runner.Store
	work    *work.Store
}

func newEngineFixture(t *testing.T) *engineFixture {
	t.Helper()
	db := pgtest.FreshDB(t).AsRole(t, "aic_app")
	db.SetMaxOpenConns(12)
	if _, err := db.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`, "ENG-T1", "Engine tenant", "eng-t1", "ENG-U1", "engine@example.com", "x", "Engine owner", "ENG-M1"); err != nil {
		t.Fatal(err)
	}
	return &engineFixture{db: db, sc: tenancy.Scope{TenantID: "ENG-T1", UserID: "ENG-U1", Role: tenancy.RoleOwner}, store: NewStore(db), agents: agent.NewStore(db), runners: runner.NewStore(db), work: work.NewStore(db)}
}

func (f *engineFixture) createAgent(t *testing.T) agent.Agent {
	t.Helper()
	a, err := f.agents.CreateAgent(context.Background(), f.sc, agent.Agent{Name: "engine-agent", Role: agent.RoleDev, Kind: agent.KindCLI, Config: json.RawMessage(`{"bin":"unused"}`), Enabled: true, SoulMD: "Always be helpful.", CreatedBy: &f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	// The agent is the WHAT; the runner is the HOW. Every agent carries the
	// runner it runs on, and the chain is the fallback order.
	r, err := f.runners.Create(context.Background(), f.sc, runner.NewRunner{Name: "engine-runner", Kind: runner.KindCLI, Config: json.RawMessage(`{"bin":"unused"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.runners.SetChain(context.Background(), f.sc, a.ID, []string{r.ID}); err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *engineFixture) createRun(t *testing.T, graph Template) Run {
	t.Helper()
	tpl, err := f.store.CreateTemplate(context.Background(), f.sc, NewTemplate{Name: "engine-test", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	run, err := e.StartRun(context.Background(), f.sc, tpl.ID, "", f.sc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestStartRunRefusesDisabledTemplateAsConfigurationFailure(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	// The template has to begin and end to be published at all.
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "start", Kind: NodeStart, Name: "start", Type: "flow.start", TypeVersion: 1, Config: json.RawMessage(`{}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "n", Kind: NodeAgent, Name: "work", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(`{"agent_id":"agent"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "end", Kind: NodeEnd, Name: "end", Type: "flow.end", TypeVersion: 1, Config: json.RawMessage(`{}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{
		{ID: "start-n", From: PortRef{Node: "start", Port: "main"}, To: PortRef{Node: "n", Port: "in"}},
		{ID: "n-end", From: PortRef{Node: "n", Port: "main"}, To: PortRef{Node: "end", Port: "in"}},
	}}
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "retired", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.PublishTemplate(ctx, f.sc, tpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.DisableTemplate(ctx, f.sc, tpl.ID); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(EngineDeps{Store: f.store})
	if _, err = e.StartRun(ctx, f.sc, tpl.ID, "", f.sc.UserID); !errors.Is(err, ErrTemplateDisabled) {
		t.Fatalf("StartRun disabled template error = %v", err)
	} else {
		var classified interface{ FailureKind() FailureKind }
		if !errors.As(err, &classified) || classified.FailureKind() != FailureCapability {
			t.Fatalf("disabled template failure classification = %v, want capability", err)
		}
	}
}

type engineAdapter struct {
	output      string
	failure     error
	failureWhen string
}

func (a *engineAdapter) Kind() string                     { return "cli" }
func (a *engineAdapter) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (a *engineAdapter) Run(ctx context.Context, job agent.Job, _ agent.EventSink) (*agent.Result, error) {
	if a.failure != nil {
		return nil, a.failure
	}
	if a.failureWhen != "" && strings.Contains(job.Prompt, a.failureWhen) {
		return nil, engineFailure("capability")
	}
	return &agent.Result{Output: a.output, TokensIn: 1, TokensOut: 2}, nil
}
func (a *engineAdapter) Cancel(string) error { return nil }

type engineFailure string

func (e engineFailure) Error() string { return string(e) }

func agentGraph(a agent.Agent) Template {
	return Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "first", Kind: NodeAgent, Name: "first step", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"first"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "second", Kind: NodeAgent, Name: "second step", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"second"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{{ID: "first-second", From: PortRef{Node: "first", Port: "main"}, To: PortRef{Node: "second", Port: "in"}}}}
}

func waitJobState(t *testing.T, st *agent.Store, sc tenancy.Scope, id string, want agent.JobState) agent.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := st.GetJob(context.Background(), sc, id)
		if err == nil && j.State == want {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := st.GetJob(context.Background(), sc, id)
	t.Fatalf("job state=%s want=%s", j.State, want)
	return agent.Job{}
}

func TestEngineLinearRunReplayAndIdempotency(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	a := f.createAgent(t)
	run := f.createRun(t, agentGraph(a))
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work})
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := latestStep(steps, "first", "")
	if !ok || first.State != StepRunning || first.AgentJobID == "" {
		t.Fatalf("first execution not queued: %+v", first)
	}
	key := fmt.Sprintf("run:%s:first::0", run.ID)
	firstJob, err := f.agents.GetJobByIdempotencyKey(ctx, f.sc, key)
	if err != nil {
		t.Fatalf("first job: %v", err)
	}
	if firstJob.ID != first.AgentJobID {
		t.Fatalf("step job %s != idempotency row %s", first.AgentJobID, firstJob.ID)
	}
	// Replaying an unchanged state must attach to the same job, not make another.
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.agents.GetJobByIdempotencyKey(ctx, f.sc, key); err != nil {
		t.Fatalf("replay job: %v", err)
	}
	worker := agent.NewWorker(f.agents, agent.NewHub(), []agent.Adapter{&engineAdapter{output: "first complete"}}, agent.WithWorkerPoll(10*time.Millisecond), agent.WithWorkerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker.Start(wctx)
	waitJobState(t, f.agents, f.sc, first.AgentJobID, agent.JobSucceeded)
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err = f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := latestStep(steps, "second", "")
	if !ok || second.State != StepRunning || second.AgentJobID == "" {
		t.Fatalf("second node did not dispatch after receipt: %+v", second)
	}
	waitJobState(t, f.agents, f.sc, second.AgentJobID, agent.JobSucceeded)
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	finished, err := f.store.GetRun(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != RunCompleted {
		t.Fatalf("run state=%s want completed", finished.State)
	}
}

func TestEngineFailureRoutingReceipts(t *testing.T) {
	tpl := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "fail", Kind: NodeAgent, Name: "fail", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(`{"agent_id":"A"}`), OnError: OnErrorContinueError, Workspace: WorkspaceScratch},
		{ID: "ok", Kind: NodeAgent, Name: "ok", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(`{"agent_id":"B"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{
		{ID: "main-edge", From: PortRef{Node: "fail", Port: "main"}, To: PortRef{Node: "ok", Port: "in"}},
		{ID: "error-edge", From: PortRef{Node: "fail", Port: "error"}, To: PortRef{Node: "ok", Port: "in"}},
	}}
	step := PersistedStep{NodeID: "fail", State: StepFailed, Kind: NodeAgent, OnError: OnErrorContinueError, FailureKind: FailureCapability}
	receipts := EmitReceipts(&tpl, step)
	if len(receipts) != 2 {
		t.Fatalf("main edge receipt=%+v", receipts)
	}
	byEdge := map[string]Receipt{}
	for _, receipt := range receipts {
		byEdge[receipt.EdgeID] = receipt
	}
	if byEdge["main-edge"].State != ReceiptDead || byEdge["error-edge"].State != ReceiptDelivered {
		t.Fatalf("continue_error_output receipts=%+v", byEdge)
	}
	step.OnError = OnErrorStop
	plan, err := Decide(&tpl, []PersistedStep{step})
	if err != nil {
		t.Fatal(err)
	}
	if plan.RunState != "failed" {
		t.Fatalf("stop_workflow plan state=%s", plan.RunState)
	}
}

func TestEngineCheckpointCASDecision(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	graph := Template{SchemaVersion: 1, Nodes: []Node{{ID: "gate", Kind: NodeCheckpoint, Name: "review", Type: "human.review", TypeVersion: 1, Config: json.RawMessage(`{"policy":"retry_step","max_cycles":3,"fields":[{"key":"decision","type":"choice","label":"Decision","required":true,"options":["accept","revise"]}]}`), OnError: OnErrorStop, Workspace: WorkspaceScratch}}, Edges: []Edge{}}
	run := f.createRun(t, graph)
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db)})
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate, ok := latestStep(steps, "gate", "")
	if !ok || gate.State != StepWaiting {
		t.Fatalf("gate state=%+v", gate)
	}
	checkpoints, err := f.store.ListCheckpoints(ctx, f.sc, "pending", 10)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("checkpoints=%d err=%v", len(checkpoints), err)
	}
	var fieldSpecs []struct {
		Key, Type, Label string
		Required         bool
		Options          []string
	}
	if err = json.Unmarshal(checkpoints[0].FieldsSpec, &fieldSpecs); err != nil || len(fieldSpecs) != 1 || fieldSpecs[0].Key != "decision" || fieldSpecs[0].Type != "choice" || fieldSpecs[0].Label != "Decision" || !fieldSpecs[0].Required || len(fieldSpecs[0].Options) != 2 {
		t.Fatalf("checkpoint field spec=%s err=%v", checkpoints[0].FieldsSpec, err)
	}
	fieldValues := json.RawMessage(`{"decision":"revise"}`)
	decided, err := e.DecideCheckpoint(ctx, f.sc, checkpoints[0].ID, Decision{Kind: CheckpointRejected, DecisionMD: "please revise", Fields: fieldValues}, f.sc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	var storedFields map[string]string
	if err = json.Unmarshal(decided.Fields, &storedFields); err != nil || decided.State != CheckpointRejected || storedFields["decision"] != "revise" {
		t.Fatalf("decision=%s fields=%s", decided.State, decided.Fields)
	}
	if _, err = e.DecideCheckpoint(ctx, f.sc, checkpoints[0].ID, Decision{Kind: CheckpointApproved}, f.sc.UserID); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second decision=%v", err)
	}
	steps, err = f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok = latestStep(steps, "gate", ""); !ok {
		t.Fatal("retry did not create next execution")
	}
	var maxIndex int
	var oldSuperseded bool
	for _, s := range steps {
		if s.NodeID == "gate" && s.RunIndex > maxIndex {
			maxIndex = s.RunIndex
		}
		if s.ID == gate.ID && s.State == StepSuperseded {
			oldSuperseded = true
		}
	}
	if maxIndex != 1 || !oldSuperseded {
		t.Fatalf("checkpoint retry history: max=%d superseded=%v steps=%+v", maxIndex, oldSuperseded, steps)
	}
}

func TestEngineRunTenantIsolation(t *testing.T) {
	f := newEngineFixture(t)
	if _, err := f.store.GetRun(context.Background(), tenancy.Scope{TenantID: "OTHER", Role: tenancy.RoleOwner}, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign run lookup=%v", err)
	}
	if strings.TrimSpace(f.sc.TenantID) == "" {
		t.Fatal("fixture tenant is empty")
	}
}

func TestEngineDiamondDispatchAndMergeReceipts(t *testing.T) {
	for _, failBranch := range []bool{false, true} {
		name := "both branches succeed"
		if failBranch {
			name = "dead branch blocks merge"
		}
		t.Run(name, func(t *testing.T) {
			f := newEngineFixture(t)
			ctx := context.Background()
			a := f.createAgent(t)
			cfg := func(prompt string) json.RawMessage {
				return json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":%q}`, a.ID, prompt))
			}
			graph := Template{SchemaVersion: 1, Nodes: []Node{
				{ID: "start", Kind: NodeAgent, Name: "start", Type: "agent.worker", TypeVersion: 1, Config: cfg("start"), OnError: OnErrorStop, Workspace: WorkspaceScratch},
				{ID: "left", Kind: NodeAgent, Name: "left", Type: "agent.worker", TypeVersion: 1, Config: cfg("left"), OnError: OnErrorContinueError, Workspace: WorkspaceScratch},
				{ID: "right", Kind: NodeAgent, Name: "right", Type: "agent.worker", TypeVersion: 1, Config: cfg("right"), OnError: OnErrorContinueError, Workspace: WorkspaceScratch},
				{ID: "join", Kind: NodeMerge, Name: "join", Type: "merge.all", TypeVersion: 1, Config: json.RawMessage(`{"mode":"all"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
			}, Edges: []Edge{
				{ID: "start-left", From: PortRef{Node: "start", Port: "main"}, To: PortRef{Node: "left", Port: "in"}},
				{ID: "start-right", From: PortRef{Node: "start", Port: "main"}, To: PortRef{Node: "right", Port: "in"}},
				{ID: "left-join", From: PortRef{Node: "left", Port: "main"}, To: PortRef{Node: "join", Port: "in"}},
				{ID: "right-join", From: PortRef{Node: "right", Port: "main"}, To: PortRef{Node: "join", Port: "in"}},
			}}
			run := f.createRun(t, graph)
			adapter := &engineAdapter{output: "ok"}
			if failBranch {
				adapter.failureWhen = "right"
			}
			worker := agent.NewWorker(f.agents, agent.NewHub(), []agent.Adapter{adapter}, agent.WithWorkerPoll(10*time.Millisecond), agent.WithWorkerCount(2), agent.WithWorkerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			wctx, cancel := context.WithCancel(ctx)
			defer cancel()
			worker.Start(wctx)
			e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work})
			if err := e.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			steps, _ := f.store.ListStepRuns(ctx, f.sc, run.ID)
			start, _ := latestStep(steps, "start", "")
			waitJobState(t, f.agents, f.sc, start.AgentJobID, agent.JobSucceeded)
			if err := e.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			steps, _ = f.store.ListStepRuns(ctx, f.sc, run.ID)
			left, _ := latestStep(steps, "left", "")
			right, _ := latestStep(steps, "right", "")
			if left.State != StepRunning || right.State != StepRunning {
				t.Fatalf("branches were not dispatched concurrently: left=%s right=%s", left.State, right.State)
			}
			waitJobState(t, f.agents, f.sc, left.AgentJobID, agent.JobSucceeded)
			wantRight := agent.JobSucceeded
			if failBranch {
				wantRight = agent.JobFailed
			}
			waitJobState(t, f.agents, f.sc, right.AgentJobID, wantRight)
			if err := e.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			finished, err := f.store.GetRun(ctx, f.sc, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failBranch {
				if finished.State != RunFailed {
					t.Fatalf("dead merge branch run state=%s", finished.State)
				}
			} else if finished.State != RunCompleted {
				t.Fatalf("successful diamond run state=%s", finished.State)
			}
			steps, _ = f.store.ListStepRuns(ctx, f.sc, run.ID)
			join, ok := latestStep(steps, "join", "")
			if !ok {
				t.Fatal("merge execution missing")
			}
			if failBranch && join.State != StepBlocked {
				t.Fatalf("merge state=%s want blocked", join.State)
			}
			if !failBranch && join.State != StepSucceeded {
				t.Fatalf("merge state=%s want succeeded", join.State)
			}
			if failBranch {
				thread, err := f.agents.EnsureRunThread(ctx, f.sc, run.ID, "Workflow run", f.sc.UserID)
				if err != nil {
					t.Fatal(err)
				}
				messages, err := f.agents.ListMessages(ctx, f.sc, thread.ID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, m := range messages {
					if m.AuthorKind == agent.AuthorSystem && strings.Contains(m.BodyMD, "Agent run failed") {
						found = true
					}
				}
				if !found {
					t.Fatal("agent failure system message missing from run thread")
				}
			}
		})
	}
}

// driveRun ticks the engine until the run reaches a terminal state. The agent
// jobs are given the time to finish in between the ticks.
func driveRun(t *testing.T, f *engineFixture, e *Engine, run Run, ctx context.Context) Run {
	t.Helper()
	for i := 0; i < 20; i++ {
		if err := e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range steps {
			if step.State == StepRunning && step.AgentJobID != "" {
				waitJobState(t, f.agents, f.sc, step.AgentJobID, agent.JobSucceeded)
			}
		}
		finished, err := f.store.GetRun(ctx, f.sc, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finished.State == RunCompleted || finished.State == RunFailed {
			return finished
		}
	}
	finished, err := f.store.GetRun(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return finished
}

// A graph that says where the flow begins and where it ends: the start hands
// the flow on without running anything, and the end finishes the run.
func TestEngineStartAndEndCompleteTheRun(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	a := f.createAgent(t)
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "start", Kind: NodeStart, Name: "start", Type: "flow.start", TypeVersion: 1, Config: json.RawMessage(`{}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "work", Kind: NodeAgent, Name: "work", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"work"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "end", Kind: NodeEnd, Name: "end", Type: "flow.end", TypeVersion: 1, Config: json.RawMessage(`{}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{
		{ID: "start-work", From: PortRef{Node: "start", Port: "main"}, To: PortRef{Node: "work", Port: "in"}},
		{ID: "work-end", From: PortRef{Node: "work", Port: "main"}, To: PortRef{Node: "end", Port: "in"}},
	}}
	run := f.createRun(t, graph)
	worker := agent.NewWorker(f.agents, agent.NewHub(), []agent.Adapter{&engineAdapter{output: "done"}}, agent.WithWorkerPoll(10*time.Millisecond), agent.WithWorkerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker.Start(wctx)
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work})

	// The start node is not work: it succeeds on the first tick, and no agent
	// ran for it.
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	start, ok := latestStep(steps, "start", "")
	if !ok || start.State != StepSucceeded {
		t.Fatalf("the start node did not succeed: %+v", start)
	}
	if start.AgentJobID != "" {
		t.Fatalf("the start node ran a job: %s", start.AgentJobID)
	}

	// The flow moved on to the agent behind the start.
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err = f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	work, ok := latestStep(steps, "work", "")
	if !ok || work.State != StepRunning || work.AgentJobID == "" {
		t.Fatalf("the agent did not run after the start: %+v", work)
	}

	// The end node takes the flow's last word, and the run is complete.
	finished := driveRun(t, f, e, run, ctx)
	if finished.State != RunCompleted {
		t.Fatalf("run state=%s want completed", finished.State)
	}
	steps, err = f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	end, ok := latestStep(steps, "end", "")
	if !ok || end.State != StepSucceeded {
		t.Fatalf("the end node did not succeed: %+v", end)
	}
	if end.AgentJobID != "" {
		t.Fatalf("the end node ran a job: %s", end.AgentJobID)
	}
}

// The end node ends the run: a branch the flow left behind does not keep it
// running.
func TestEngineEndNodeEndsTheRun(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	a := f.createAgent(t)
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "start", Kind: NodeStart, Name: "start", Type: "flow.start", TypeVersion: 1, Config: json.RawMessage(`{}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "work", Kind: NodeAgent, Name: "work", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"work"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "side", Kind: NodeAgent, Name: "side", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"side"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "end", Kind: NodeEnd, Name: "end", Type: "flow.end", TypeVersion: 1, Config: json.RawMessage(`{}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{
		{ID: "start-work", From: PortRef{Node: "start", Port: "main"}, To: PortRef{Node: "work", Port: "in"}},
		{ID: "work-end", From: PortRef{Node: "work", Port: "main"}, To: PortRef{Node: "end", Port: "in"}},
		{ID: "work-side", From: PortRef{Node: "work", Port: "main"}, To: PortRef{Node: "side", Port: "in"}},
	}}
	run := f.createRun(t, graph)
	worker := agent.NewWorker(f.agents, agent.NewHub(), []agent.Adapter{&engineAdapter{output: "done"}}, agent.WithWorkerPoll(10*time.Millisecond), agent.WithWorkerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker.Start(wctx)
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work})

	// Tick until the flow reaches the end node.
	for i := 0; i < 10; i++ {
		if err := e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		end, ok := latestStep(steps, "end", "")
		if ok && end.State == StepSucceeded {
			break
		}
		for _, step := range steps {
			if step.NodeID == "work" && step.State == StepRunning && step.AgentJobID != "" {
				waitJobState(t, f.agents, f.sc, step.AgentJobID, agent.JobSucceeded)
			}
		}
	}
	finished, err := f.store.GetRun(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != RunCompleted {
		t.Fatalf("run state=%s want completed: the end node ends the run", finished.State)
	}
	// The side branch was still in flight when the run finished: the flow had
	// already reached its end.
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	side, ok := latestStep(steps, "side", "")
	if !ok || side.State != StepRunning {
		t.Fatalf("the side branch should still be running: %+v", side)
	}
}

// A graph without a start and without an end behaves exactly as it always
// did: the flow begins at its entry node and finishes when its last step is
// terminal.
func TestEngineWithoutStartAndEndBehavesAsBefore(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	a := f.createAgent(t)
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "first", Kind: NodeAgent, Name: "first", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"first"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "second", Kind: NodeAgent, Name: "second", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"second"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{
		{ID: "first-second", From: PortRef{Node: "first", Port: "main"}, To: PortRef{Node: "second", Port: "in"}},
	}}
	run := f.createRun(t, graph)
	worker := agent.NewWorker(f.agents, agent.NewHub(), []agent.Adapter{&engineAdapter{output: "done"}}, agent.WithWorkerPoll(10*time.Millisecond), agent.WithWorkerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker.Start(wctx)
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work})

	finished := driveRun(t, f, e, run, ctx)
	if finished.State != RunCompleted {
		t.Fatalf("run state=%s want completed", finished.State)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		step, ok := latestStep(steps, id, "")
		if !ok || step.State != StepSucceeded {
			t.Fatalf("%s did not succeed: %+v", id, step)
		}
	}
}

func TestEngineCancelWaitingRun(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	graph := Template{SchemaVersion: 1, Nodes: []Node{{ID: "gate", Kind: NodeCheckpoint, Name: "review", Type: "human.review", TypeVersion: 1, Config: json.RawMessage(`{"policy":"retry_step"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch}}, Edges: []Edge{}}
	run := f.createRun(t, graph)
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners})
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	cancelled, err := e.CancelRun(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != RunCancelled {
		t.Fatalf("run state=%s", cancelled.State)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.State == StepWaiting || step.State == StepPending || step.State == StepReady {
			t.Fatalf("nonterminal step survived cancellation: %s", step.State)
		}
	}
}

func TestEngineRedoUsesDescendantsAndPreservesSibling(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	cfg := json.RawMessage(`{"agent_id":"A"}`)
	node := func(id string) Node {
		return Node{ID: id, Kind: NodeAgent, Name: id, Type: "agent.worker", TypeVersion: 1, Config: cfg, OnError: OnErrorStop, Workspace: WorkspaceScratch}
	}
	graph := Template{SchemaVersion: 1, Nodes: []Node{node("root"), node("redo"), node("sibling"), node("descendant")}, Edges: []Edge{
		{ID: "root-redo", From: PortRef{Node: "root", Port: "main"}, To: PortRef{Node: "redo", Port: "in"}},
		{ID: "root-sibling", From: PortRef{Node: "root", Port: "main"}, To: PortRef{Node: "sibling", Port: "in"}},
		{ID: "redo-descendant", From: PortRef{Node: "redo", Port: "main"}, To: PortRef{Node: "descendant", Port: "in"}},
	}}
	run := f.createRun(t, graph)
	var rows []StepRun
	for _, id := range []string{"redo", "sibling", "descendant"} {
		n, _ := graph.Node(id)
		step, err := f.store.CreateStepRun(ctx, f.sc, NewStepRun{RunID: run.ID, NodeID: id, Kind: string(n.Kind), Name: n.Name, RunIndex: 0})
		if err != nil {
			t.Fatal(err)
		}
		state := StepSucceeded
		if _, err = f.store.UpdateStepRun(ctx, f.sc, step.ID, StepRunPatch{State: &state}); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, step)
	}
	e := NewEngine(EngineDeps{Store: f.store, Memory: memory.NewStore(f.db)})
	if err := e.RedoNode(ctx, f.sc, run.ID, "redo", "revise this step", f.sc.UserID); err != nil {
		t.Fatal(err)
	}
	updated, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"redo", "descendant"} {
		latest, ok := latestStep(updated, id, "")
		if !ok || latest.RunIndex != 1 || latest.State != StepPending {
			t.Fatalf("%s not recreated pending: %+v", id, latest)
		}
	}
	sibling, ok := latestStep(updated, "sibling", "")
	if !ok || sibling.RunIndex != 0 || sibling.State != StepSucceeded {
		t.Fatalf("untouched sibling changed: %+v", sibling)
	}
	for _, old := range rows {
		if old.NodeID == "sibling" {
			continue
		}
		found := false
		for _, after := range updated {
			if after.ID == old.ID && after.State == StepSuperseded {
				found = true
			}
		}
		if !found {
			t.Fatalf("old %s execution was not superseded", old.NodeID)
		}
	}
}
