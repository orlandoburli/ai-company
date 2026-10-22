package agent_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

type workerFixture struct {
	t     *testing.T
	agent *agent.Store
	work  *work.Store
	sc    tenancy.Scope
	item  work.Item
}

func newWorkerFixture(t *testing.T) *workerFixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	if _, err := app.Exec(`SELECT auth_register_tenant($1, $2, $3, $4, $5, $6, $7, $8)`,
		"T1", "Tenant One", "t1", "U1", "owner@example.com", "x", "Owner", "M1"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	sc := tenancy.Scope{TenantID: "T1", UserID: "U1", Role: tenancy.RoleOwner}
	ws := work.NewStore(app)
	ctx := context.Background()
	p, err := ws.CreateProduct(ctx, sc, "Acme", "acme", "", "")
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	pr, err := ws.CreateProject(ctx, sc, p.ID, nil, "Core", "CORE")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	item, err := ws.CreateItem(ctx, sc, work.NewItem{ProjectID: pr.ID, Kind: "task", Title: "Chat item", ActorUserID: "U1"})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	return &workerFixture{t: t, agent: agent.NewStore(app), work: ws, sc: sc, item: item}
}

func (f *workerFixture) createAgent(name string, limit int) agent.Agent {
	f.t.Helper()
	a, err := f.agent.CreateAgent(context.Background(), f.sc, agent.Agent{
		Name:             name,
		Role:             agent.RoleDev,
		Kind:             agent.KindCLI,
		Config:           json.RawMessage(`{"bin":"echo"}`),
		ConcurrencyLimit: limit,
		Enabled:          true,
		SoulMD:           "test soul",
		CreatedBy:        &f.sc.UserID,
	})
	if err != nil {
		f.t.Fatalf("create agent: %v", err)
	}
	return a
}

func (f *workerFixture) createChatJob(a agent.Agent, prompt string) (agent.Thread, agent.Job) {
	f.t.Helper()
	ctx := context.Background()
	th, err := f.agent.EnsureItemThread(ctx, f.sc, f.item.ID, f.sc.UserID)
	if err != nil {
		f.t.Fatalf("ensure thread: %v", err)
	}
	msg, err := f.agent.AddMessage(ctx, f.sc, agent.NewMessage{
		ThreadID:     th.ID,
		AuthorKind:   agent.AuthorHuman,
		AuthorUserID: f.sc.UserID,
		BodyMD:       prompt,
	})
	if err != nil {
		f.t.Fatalf("add message: %v", err)
	}
	job, err := f.agent.CreateJob(ctx, f.sc, agent.NewJob{
		AgentID:   a.ID,
		ThreadID:  th.ID,
		MessageID: msg.ID,
		ItemID:    f.item.ID,
		Prompt:    prompt,
		CreatedBy: f.sc.UserID,
	})
	if err != nil {
		f.t.Fatalf("create job: %v", err)
	}
	return th, job
}

type fakeAdapter struct {
	mu       sync.Mutex
	active   int
	maxSeen  int
	started  chan string
	block    bool
	delay    time.Duration
	output   string
	runErr   error
	cancels  map[string]context.CancelFunc
	cancelMu sync.Mutex
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{started: make(chan string, 16), output: "agent reply", cancels: map[string]context.CancelFunc{}}
}

func (a *fakeAdapter) Kind() string                     { return "cli" }
func (a *fakeAdapter) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (a *fakeAdapter) Run(ctx context.Context, job agent.Job, sink agent.EventSink) (*agent.Result, error) {
	a.mu.Lock()
	a.active++
	if a.active > a.maxSeen {
		a.maxSeen = a.active
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.active--
		a.mu.Unlock()
	}()

	a.cancelMu.Lock()
	a.cancels[job.ID] = func() {}
	a.cancelMu.Unlock()
	select {
	case a.started <- job.ID:
	default:
	}

	if a.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if a.delay > 0 {
		select {
		case <-time.After(a.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if a.runErr != nil {
		return nil, a.runErr
	}
	sink.Emit(agent.Event{Stream: "stdout", Level: "info", Kind: "output_chunk", Message: "thinking"})
	return &agent.Result{Output: a.output, TokensIn: 3, TokensOut: 5}, nil
}

func (a *fakeAdapter) Cancel(string) error { return nil }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startWorker(t *testing.T, st *agent.Store, ad agent.Adapter, opts ...agent.WorkerOption) (context.CancelFunc, *agent.Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	base := []agent.WorkerOption{
		agent.WithWorkerPoll(10 * time.Millisecond),
		agent.WithWorkerLease(2 * time.Second),
		agent.WithWorkerLogger(quietLogger()),
	}
	w := agent.NewWorker(st, agent.NewHub(), []agent.Adapter{ad}, append(base, opts...)...)
	w.Start(ctx)
	return cancel, w
}

func waitJob(t *testing.T, st *agent.Store, sc tenancy.Scope, id string, want agent.JobState) agent.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := st.GetJob(context.Background(), sc, id)
		if err == nil && job.State == want {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, _ := st.GetJob(context.Background(), sc, id)
	t.Fatalf("job %s state = %s, want %s", id, job.State, want)
	return agent.Job{}
}

func TestWorkerRunsChatJobAndPostsReply(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("dev", 2)
	th, job := f.createChatJob(a, "@dev hello")

	ad := newFakeAdapter()
	ad.output = "hello from the agent"
	cancel, _ := startWorker(t, f.agent, ad)
	defer cancel()

	done := waitJob(t, f.agent, f.sc, job.ID, agent.JobSucceeded)
	if done.Attempt != 1 || done.TokensIn != 3 || done.TokensOut != 5 {
		t.Fatalf("succeeded job = %+v", done)
	}

	msgs, err := f.agent.ListMessages(context.Background(), f.sc, th.ID, 0, 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 2 || msgs[1].AuthorKind != agent.AuthorAgent || msgs[1].BodyMD != "hello from the agent" {
		t.Fatalf("messages = %+v; want human + agent reply", msgs)
	}
	if msgs[1].AuthorAgentID == nil || *msgs[1].AuthorAgentID != a.ID {
		t.Fatalf("agent reply author = %v, want %s", msgs[1].AuthorAgentID, a.ID)
	}

	logs, err := f.agent.ListLogs(context.Background(), f.sc, job.ID, 0, 100)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) == 0 || logs[0].Kind != "output_chunk" {
		t.Fatalf("logs = %+v; want adapter events persisted", logs)
	}
}

func TestWorkerHonoursPerAgentConcurrency(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("serial", 1)
	_, j1 := f.createChatJob(a, "one")
	_, j2 := f.createChatJob(a, "two")

	ad := newFakeAdapter()
	ad.delay = 80 * time.Millisecond
	cancel, _ := startWorker(t, f.agent, ad, agent.WithWorkerCount(2))
	defer cancel()

	waitJob(t, f.agent, f.sc, j1.ID, agent.JobSucceeded)
	waitJob(t, f.agent, f.sc, j2.ID, agent.JobSucceeded)

	ad.mu.Lock()
	max := ad.maxSeen
	ad.mu.Unlock()
	if max != 1 {
		t.Fatalf("max concurrent runs for one agent = %d, want 1", max)
	}
}

func TestWorkerCancellationLeavesJobCancelled(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("blocker", 1)
	_, job := f.createChatJob(a, "wait")

	ad := newFakeAdapter()
	ad.block = true
	cancel, w := startWorker(t, f.agent, ad)
	defer cancel()

	select {
	case <-ad.started:
	case <-time.After(3 * time.Second):
		t.Fatal("adapter never started")
	}

	if _, err := f.agent.CancelJob(context.Background(), f.sc, job.ID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	w.Cancel(job.ID)

	waitJob(t, f.agent, f.sc, job.ID, agent.JobCancelled)
	time.Sleep(50 * time.Millisecond)
	cur, err := f.agent.GetJob(context.Background(), f.sc, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if cur.State != agent.JobCancelled {
		t.Fatalf("cancelled job was resurrected as %s", cur.State)
	}
}

type permanentError struct{}

func (permanentError) Error() string       { return "bad request" }
func (permanentError) FailureKind() string { return agent.FailurePermanent }

func TestWorkerFailsPermanentErrorAndPostsSystemMessage(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("failing", 1)
	th, job := f.createChatJob(a, "fail please")

	ad := newFakeAdapter()
	ad.runErr = permanentError{}
	cancel, _ := startWorker(t, f.agent, ad)
	defer cancel()

	waitJob(t, f.agent, f.sc, job.ID, agent.JobFailed)
	msgs, err := f.agent.ListMessages(context.Background(), f.sc, th.ID, 0, 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 2 || msgs[1].AuthorKind != agent.AuthorSystem || !strings.Contains(msgs[1].BodyMD, "bad request") {
		t.Fatalf("messages = %+v; want a system failure message", msgs)
	}
}

func TestWorkerRetriesTransientFailure(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("flaky", 1)

	ad := &flakyAdapter{}
	th, job := f.createChatJob(a, "retry please")
	cancel, _ := startWorker(t, f.agent, ad)
	defer cancel()

	// The first attempt fails transiently; the worker requeues with a 10s
	// backoff, so this test only asserts the requeue, not the second run.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := f.agent.GetJob(context.Background(), f.sc, job.ID)
		if err == nil && cur.State == agent.JobQueued && cur.Attempt == 1 {
			msgs, _ := f.agent.ListMessages(context.Background(), f.sc, th.ID, 0, 100)
			if len(msgs) != 1 {
				t.Fatalf("transient retry should not write a failure message: %+v", msgs)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cur, _ := f.agent.GetJob(context.Background(), f.sc, job.ID)
	t.Fatalf("job = %+v; want queued after a transient first attempt", cur)
}

type flakyAdapter struct{ fakeAdapter }

func (a *flakyAdapter) Run(ctx context.Context, job agent.Job, sink agent.EventSink) (*agent.Result, error) {
	sink.Emit(agent.Event{Stream: "engine", Level: "warn", Kind: "error", Message: "temporary"})
	return nil, &transientError{}
}

type transientError struct{}

func (transientError) Error() string       { return "temporary upstream failure" }
func (transientError) FailureKind() string { return agent.FailureTransient }
