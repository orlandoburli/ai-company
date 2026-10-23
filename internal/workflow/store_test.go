package workflow_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

type wfFixture struct {
	db     *sql.DB
	st     *workflow.Store
	t1, t2 tenancy.Scope
}

func wfSetup(t *testing.T) *wfFixture {
	t.Helper()
	database := pgtest.FreshDB(t)
	app := database.AsRole(t, "aic_app")
	app.SetMaxOpenConns(6)
	for _, id := range []string{"T1", "T2"} {
		if _, err := app.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`, id, "Tenant "+id, strings.ToLower(id), "U-"+id, strings.ToLower(id)+"@example.com", "x", "User "+id, "M-"+id); err != nil {
			t.Fatal(err)
		}
	}
	return &wfFixture{db: app, st: workflow.NewStore(app), t1: tenancy.Scope{TenantID: "T1", UserID: "U-T1", Role: tenancy.RoleOwner}, t2: tenancy.Scope{TenantID: "T2", UserID: "U-T2", Role: tenancy.RoleOwner}}
}
func graph() workflow.Template {
	return workflow.Template{SchemaVersion: 1, Nodes: []workflow.Node{{ID: "n", Kind: workflow.NodeAgent, Name: "work", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(`{"agent_id":"A"}`), OnError: workflow.OnErrorStop, Workspace: workflow.WorkspaceScratch}}, Edges: []workflow.Edge{}}
}
func (f *wfFixture) template(t *testing.T, sc tenancy.Scope) workflow.TemplateRow {
	t.Helper()
	x, e := f.st.CreateTemplate(context.Background(), sc, workflow.NewTemplate{Name: "test", Graph: graph(), CreatedBy: sc.UserID})
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func (f *wfFixture) run(t *testing.T, sc tenancy.Scope, template workflow.TemplateRow) workflow.Run {
	t.Helper()
	x, e := f.st.CreateRun(context.Background(), sc, workflow.NewRun{TemplateID: template.ID, TemplateVersion: template.Version, DefinitionSnapshot: template.GraphJSON, StartedBy: sc.UserID})
	if e != nil {
		t.Fatal(e)
	}
	return x
}

func TestWorkflowTemplateLifecycleAndTenantIsolation(t *testing.T) {
	f := wfSetup(t)
	ctx := context.Background()
	if _, err := f.st.CreateTemplate(ctx, f.t1, workflow.NewTemplate{Name: "bad", Graph: workflow.Template{}, CreatedBy: f.t1.UserID}); err == nil {
		t.Fatal("invalid graph was accepted")
	}
	draft := f.template(t, f.t1)
	if draft.Version != 1 || draft.PublishedAt != nil {
		t.Fatalf("draft: %+v", draft)
	}
	updated, err := f.st.UpdateTemplate(ctx, f.t1, draft.ID, workflow.TemplatePatch{AppliesTo: strPtr("bug")})
	if err != nil || updated.AppliesTo != "bug" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	published, err := f.st.PublishTemplate(ctx, f.t1, draft.ID)
	if err != nil || published.PublishedAt == nil {
		t.Fatalf("publish: %+v %v", published, err)
	}
	if _, err = f.st.UpdateTemplate(ctx, f.t1, draft.ID, workflow.TemplatePatch{}); !errors.Is(err, workflow.ErrTemplatePublished) {
		t.Fatalf("edit published: %v", err)
	}
	if err = f.st.DeleteTemplate(ctx, f.t1, draft.ID); !errors.Is(err, workflow.ErrTemplatePublished) {
		t.Fatalf("delete published: %v", err)
	}
	v2, err := f.st.NewTemplateVersion(ctx, f.t1, draft.ID)
	if err != nil || v2.Version != 2 || v2.PublishedAt != nil || string(v2.GraphJSON) != string(draft.GraphJSON) {
		t.Fatalf("version clone: %+v %v", v2, err)
	}
	if _, err = f.st.GetTemplateVersion(ctx, f.t1, "test", 2); err != nil {
		t.Fatal(err)
	}
	f.run(t, f.t1, v2)
	if err = f.st.DeleteTemplate(ctx, f.t1, v2.ID); !errors.Is(err, workflow.ErrTemplateInUse) {
		t.Fatalf("delete referenced template: %v", err)
	}
	if _, err = f.st.GetTemplate(ctx, f.t2, draft.ID); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("cross tenant get: %v", err)
	}
	if err = f.st.DeleteTemplate(ctx, f.t2, draft.ID); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("cross tenant delete: %v", err)
	}
	if got, err := f.st.ListTemplates(ctx, f.t2, false); err != nil || len(got) != 0 {
		t.Fatalf("cross tenant list=%v err=%v", got, err)
	}
	unused, err := f.st.CreateTemplate(ctx, f.t1, workflow.NewTemplate{Name: "unused", Graph: graph(), CreatedBy: f.t1.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.DeleteTemplate(ctx, f.t1, unused.ID); err != nil {
		t.Fatalf("delete draft: %v", err)
	}
}

func TestRunStepClaimTerminalAndSupersede(t *testing.T) {
	f := wfSetup(t)
	ctx := context.Background()
	tpl := f.template(t, f.t1)
	run := f.run(t, f.t1, tpl)
	if _, err := f.st.GetRun(ctx, f.t2, run.ID); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("foreign run: %v", err)
	}
	if _, err := f.st.CreateRun(ctx, f.t2, workflow.NewRun{TemplateID: tpl.ID, TemplateVersion: tpl.Version, DefinitionSnapshot: tpl.GraphJSON}); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("cross-tenant run insert: %v", err)
	}
	step, err := f.st.CreateStepRun(ctx, f.t1, workflow.NewStepRun{RunID: run.ID, NodeID: "n", Kind: "agent", Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.CreateStepRun(ctx, f.t1, workflow.NewStepRun{RunID: run.ID, NodeID: "n", Kind: "agent", Name: "dupe"}); !errors.Is(err, workflow.ErrDuplicateStep) {
		t.Fatalf("duplicate step: %v", err)
	}
	if _, err = f.st.CreateStepRun(ctx, f.t2, workflow.NewStepRun{RunID: run.ID, NodeID: "other", Kind: "agent"}); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("cross-tenant step insert: %v", err)
	}
	var wg sync.WaitGroup
	wins := 0
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := f.st.ClaimStepRun(ctx, f.t1, step.ID, fmt.Sprintf("claim-%d", i), time.Minute)
			if e == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(e, workflow.ErrClaimLost) {
				t.Errorf("claim error: %v", e)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("claim winners=%d", wins)
	}
	claimed, err := f.st.GetStepRun(ctx, f.t1, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.HeartbeatStepRun(ctx, f.t1, step.ID, claimed.ClaimLock, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = f.st.HeartbeatStepRun(ctx, f.t1, step.ID, "wrong", time.Minute); !errors.Is(err, workflow.ErrClaimLost) {
		t.Fatalf("wrong heartbeat: %v", err)
	}
	inputs := map[string]workflow.Receipt{"e": {EdgeID: "e", FromNode: "up", Port: "main", State: workflow.ReceiptDelivered, Value: json.RawMessage(`{"v":1}`)}}
	ports := map[string]json.RawMessage{"main": json.RawMessage(`{"ok":true}`)}
	if _, err = f.st.UpdateStepRun(ctx, f.t1, step.ID, workflow.StepRunPatch{InputEdges: &inputs}); err != nil {
		t.Fatal(err)
	}
	terminal, err := f.st.SetStepTerminal(ctx, f.t1, step.ID, claimed.ClaimLock, workflow.StepSucceeded, json.RawMessage(`{"status":"ok"}`), ports, "")
	if err != nil {
		t.Fatal(err)
	}
	if terminal.State != workflow.StepSucceeded || terminal.InputEdges["e"].State != workflow.ReceiptDelivered || terminal.FinishedAt == nil {
		t.Fatalf("terminal row: %+v", terminal)
	}
	if _, err = f.st.SetStepTerminal(ctx, f.t1, step.ID, claimed.ClaimLock, workflow.StepSucceeded, nil, nil, ""); !errors.Is(err, workflow.ErrClaimLost) {
		t.Fatalf("terminal stale claim: %v", err)
	}
	if err = f.st.SupersedeSteps(ctx, f.t1, run.ID, []string{"n"}); err != nil {
		t.Fatal(err)
	}
	steps, err := f.st.ListStepRuns(ctx, f.t1, run.ID)
	if err != nil || len(steps) != 1 || steps[0].State != workflow.StepSuperseded {
		t.Fatalf("step audit history: %+v err=%v", steps, err)
	}
	if _, err = f.st.SetRunState(ctx, f.t1, run.ID, workflow.RunRunning, "", ""); err != nil {
		t.Fatal(err)
	}
	completed, err := f.st.SetRunState(ctx, f.t1, run.ID, workflow.RunCompleted, "", "")
	if err != nil || completed.FinishedAt == nil {
		t.Fatalf("complete: %+v %v", completed, err)
	}
	if _, err = f.st.CancelRun(ctx, f.t1, run.ID); !errors.Is(err, workflow.ErrTerminal) {
		t.Fatalf("cancel terminal run: %v", err)
	}
}

func TestCheckpointDecisionInterventionsAndArtifacts(t *testing.T) {
	f := wfSetup(t)
	ctx := context.Background()
	tpl := f.template(t, f.t1)
	run := f.run(t, f.t1, tpl)
	step, err := f.st.CreateStepRun(ctx, f.t1, workflow.NewStepRun{RunID: run.ID, NodeID: "n", Kind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	cp, err := f.st.CreateCheckpoint(ctx, f.t1, workflow.NewCheckpoint{RunID: run.ID, StepRunID: step.ID, NodeID: "n", SummaryMD: "approve?", FieldsSpec: json.RawMessage(`[]`)})
	if err != nil || cp.Lap != 1 {
		t.Fatalf("checkpoint: %+v %v", cp, err)
	}
	if _, err = f.st.CreateCheckpoint(ctx, f.t2, workflow.NewCheckpoint{RunID: run.ID, StepRunID: step.ID, NodeID: "n"}); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("cross-tenant checkpoint insert: %v", err)
	}
	if _, err = f.st.DecideCheckpoint(ctx, f.t1, cp.ID, 1, workflow.Decision{Kind: workflow.CheckpointApprovedWithEdit, DecidedBy: f.t1.UserID}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("empty edit decision: %v", err)
	}
	if _, err = f.st.DecideCheckpoint(ctx, f.t1, cp.ID, 1, workflow.Decision{Kind: workflow.CheckpointRejected, DecidedBy: f.t1.UserID}); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("empty rejection: %v", err)
	}
	decision, err := f.st.DecideCheckpoint(ctx, f.t1, cp.ID, 1, workflow.Decision{Kind: workflow.CheckpointApprovedWithEdit, DecisionMD: "use main", Fields: json.RawMessage(`{"branch":"main"}`), DecidedBy: f.t1.UserID})
	if err != nil || decision.State != workflow.CheckpointApprovedWithEdit {
		t.Fatalf("decide: %+v %v", decision, err)
	}
	winner, err := f.st.DecideCheckpoint(ctx, f.t1, cp.ID, 1, workflow.Decision{Kind: workflow.CheckpointRejected, DecisionMD: "no", DecidedBy: f.t1.UserID})
	if !errors.Is(err, workflow.ErrAlreadyDecided) || winner.State != workflow.CheckpointApprovedWithEdit {
		t.Fatalf("first writer outcome: row=%+v err=%v", winner, err)
	}
	secondLap, err := f.st.CreateCheckpoint(ctx, f.t1, workflow.NewCheckpoint{RunID: run.ID, StepRunID: step.ID, NodeID: "n"})
	if err != nil || secondLap.Lap != 2 {
		t.Fatal(err)
	}
	if _, err = f.st.CreateCheckpoint(ctx, f.t1, workflow.NewCheckpoint{RunID: run.ID, StepRunID: step.ID, NodeID: "n", Lap: 2}); err == nil {
		t.Fatal("duplicate checkpoint lap accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := f.st.CreateIntervention(ctx, f.t1, workflow.NewIntervention{RunID: run.ID, NodeID: "n", StepRunID: step.ID, Kind: workflow.InterventionInject, BodyMD: fmt.Sprint(i), ActorKind: "human", ActorID: f.t1.UserID})
			if e != nil {
				t.Errorf("intervention: %v", e)
			}
		}(i)
	}
	wg.Wait()
	if _, err = f.st.CreateIntervention(ctx, f.t2, workflow.NewIntervention{RunID: run.ID, StepRunID: step.ID, Kind: workflow.InterventionStop, ActorKind: "human", ActorID: f.t2.UserID}); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("cross-tenant intervention insert: %v", err)
	}
	interventions, err := f.st.ListInterventions(ctx, f.t1, run.ID)
	if err != nil || len(interventions) != 4 {
		t.Fatalf("interventions %d %v", len(interventions), err)
	}
	seen := map[int]bool{}
	for _, in := range interventions {
		seen[in.Seq] = true
	}
	for i := 1; i <= 4; i++ {
		if !seen[i] {
			t.Fatalf("missing seq %d: %+v", i, interventions)
		}
	}
	if _, err = f.st.MarkInterventionDelivered(ctx, f.t1, interventions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.MarkInterventionDelivered(ctx, f.t1, interventions[0].ID); !errors.Is(err, workflow.ErrTerminal) {
		t.Fatalf("second delivery: %v", err)
	}
	artifact, err := f.st.CreateArtifact(ctx, f.t1, workflow.NewArtifact{RunID: run.ID, StepRunID: step.ID, Kind: "report", URI: "file://report", Meta: json.RawMessage(`{"k":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.CreateArtifact(ctx, f.t2, workflow.NewArtifact{RunID: run.ID, Kind: "report", URI: "file://foreign"}); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("cross-tenant artifact insert: %v", err)
	}
	if got, e := f.st.ListArtifacts(ctx, f.t1, run.ID); e != nil || len(got) != 1 || got[0].ID != artifact.ID {
		t.Fatalf("artifacts: %+v %v", got, e)
	}
	if got, e := f.st.ListCheckpoints(ctx, f.t2, "", 10); e != nil || len(got) != 0 {
		t.Fatalf("foreign checkpoints: %+v %v", got, e)
	}
	if _, err = f.st.GetCheckpoint(ctx, f.t2, cp.ID); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("foreign checkpoint: %v", err)
	}
}

func TestAgentJobIdempotencyKey(t *testing.T) {
	f := wfSetup(t)
	ctx := context.Background()
	agents := agent.NewStore(f.db)
	a, err := agents.CreateAgent(ctx, f.t1, agent.Agent{Name: "worker", Role: agent.RoleDev, Kind: agent.KindCLI, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	tpl := f.template(t, f.t1)
	run := f.run(t, f.t1, tpl)
	step, err := f.st.CreateStepRun(ctx, f.t1, workflow.NewStepRun{RunID: run.ID, NodeID: "n", Kind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	input := agent.NewJob{AgentID: a.ID, Prompt: "run workflow node", StepRunID: step.ID, IdempotencyKey: "workflow:run:node"}
	first, err := agents.CreateJob(ctx, f.t1, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := agents.CreateJob(ctx, f.t1, input)
	if err != nil || second.ID != first.ID {
		t.Fatalf("idempotent replay first=%s second=%s err=%v", first.ID, second.ID, err)
	}
	got, err := agents.GetJobByIdempotencyKey(ctx, f.t1, input.IdempotencyKey)
	if err != nil || got.ID != first.ID || got.StepRunID == nil || *got.StepRunID != step.ID {
		t.Fatalf("lookup: %+v %v", got, err)
	}
}

func strPtr(s string) *string { return &s }

// The join accumulator is the reason a diamond knows which sides completed:
// a terminal upstream execution writes its receipts into the downstream
// execution in the same transaction that marks it terminal. If that ever
// splits into two transactions, a side effect can be committed while the
// receipt is lost — and the merge then waits forever.
func TestTerminalStepWritesDownstreamReceiptsAtomically(t *testing.T) {
	f := wfSetup(t)
	ctx := context.Background()
	tpl := f.template(t, f.t1)
	run := f.run(t, f.t1, tpl)

	upstream, err := f.st.CreateStepRun(ctx, f.t1, workflow.NewStepRun{
		RunID: run.ID, NodeID: "n", RunIndex: 0, Kind: "agent", Name: "work",
	})
	if err != nil {
		t.Fatal(err)
	}
	downstream, err := f.st.CreateStepRun(ctx, f.t1, workflow.NewStepRun{
		RunID: run.ID, NodeID: "n2", RunIndex: 0, Kind: "agent", Name: "next",
	})
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := f.st.ClaimStepRun(ctx, f.t1, upstream.ID, "lock-1", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.State != workflow.StepReady && claimed.State != workflow.StepRunning {
		t.Fatalf("claimed state = %s", claimed.State)
	}

	receipts := map[string]workflow.Receipt{
		"e1": {EdgeID: "e1", FromNode: "n", Port: "main", State: workflow.ReceiptDelivered, Value: json.RawMessage(`{"out":1}`)},
	}
	done, err := f.st.SetStepTerminalWithReceipts(ctx, f.t1, upstream.ID, "lock-1",
		workflow.StepSucceeded, json.RawMessage(`{"status":"succeeded"}`),
		map[string]json.RawMessage{"main": json.RawMessage(`{"out":1}`)}, "",
		[]workflow.DownstreamReceipt{{StepRunID: downstream.ID, Receipts: receipts}})
	if err != nil {
		t.Fatalf("set terminal: %v", err)
	}
	if done.State != workflow.StepSucceeded {
		t.Fatalf("terminal state = %s", done.State)
	}

	after, err := f.st.GetStepRun(ctx, f.t1, downstream.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := after.InputEdges["e1"]
	if !ok {
		t.Fatalf("downstream receipts = %+v, want e1", after.InputEdges)
	}
	if got.State != workflow.ReceiptDelivered || got.Port != "main" {
		t.Fatalf("receipt = %+v", got)
	}
	// The value goes through JSONB, which normalises the formatting, so the
	// comparison is on the decoded value, not the bytes.
	var gotValue, wantValue map[string]any
	if err := json.Unmarshal(got.Value, &gotValue); err != nil {
		t.Fatalf("receipt value is not JSON: %s", got.Value)
	}
	if err := json.Unmarshal([]byte(`{"out":1}`), &wantValue); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(gotValue) != fmt.Sprint(wantValue) {
		t.Fatalf("receipt value = %s, want out=1", got.Value)
	}
}

// A receipt that arrives outside a terminal transition (a merge node
// collecting one more branch) must merge into the accumulator, not replace it.
func TestMergeStepReceiptsKeepsExistingOnes(t *testing.T) {
	f := wfSetup(t)
	ctx := context.Background()
	tpl := f.template(t, f.t1)
	run := f.run(t, f.t1, tpl)

	downstream, err := f.st.CreateStepRun(ctx, f.t1, workflow.NewStepRun{
		RunID: run.ID, NodeID: "n2", RunIndex: 0, Kind: "merge", Name: "join",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.MergeStepReceipts(ctx, f.t1, downstream.ID, map[string]workflow.Receipt{
		"e1": {EdgeID: "e1", State: workflow.ReceiptDelivered},
	}); err != nil {
		t.Fatalf("merge receipts: %v", err)
	}
	after, err := f.st.MergeStepReceipts(ctx, f.t1, downstream.ID, map[string]workflow.Receipt{
		"e2": {EdgeID: "e2", State: workflow.ReceiptDead},
	})
	if err != nil {
		t.Fatalf("merge receipts: %v", err)
	}
	if len(after.InputEdges) != 2 {
		t.Fatalf("receipts = %+v, want two", after.InputEdges)
	}
	if after.InputEdges["e1"].State != workflow.ReceiptDelivered || after.InputEdges["e2"].State != workflow.ReceiptDead {
		t.Fatalf("receipts = %+v", after.InputEdges)
	}
}
