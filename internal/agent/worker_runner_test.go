package agent_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The worker routes on the JOB's runner.
//
// The agent is the WHAT; the runner is the HOW. A queued job carries the
// runner it was queued with, so the worker runs it on that transport with
// that config. A legacy job — one queued before the split — carries no
// runner and falls back to the agent's own kind and config, so nothing that
// is already in flight breaks.

type recordingAdapter struct {
	kind string

	mu   sync.Mutex
	jobs []agent.Job
}

func (a *recordingAdapter) Kind() string                     { return a.kind }
func (a *recordingAdapter) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (a *recordingAdapter) Run(ctx context.Context, job agent.Job, sink agent.EventSink) (*agent.Result, error) {
	a.mu.Lock()
	a.jobs = append(a.jobs, job)
	a.mu.Unlock()
	return &agent.Result{Output: "done on " + a.kind}, nil
}
func (a *recordingAdapter) Cancel(string) error { return nil }

func (a *recordingAdapter) last(t *testing.T) agent.Job {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.jobs) == 0 {
		t.Fatalf("the %s adapter never ran", a.kind)
	}
	return a.jobs[len(a.jobs)-1]
}

func startWorkerWith(t *testing.T, st *agent.Store, adapters ...agent.Adapter) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := agent.NewWorker(st, agent.NewHub(), adapters,
		agent.WithWorkerPoll(10*time.Millisecond),
		agent.WithWorkerLease(2*time.Second),
		agent.WithWorkerLogger(quietLogger()))
	w.Start(ctx)
	return cancel
}

func TestWorkerRoutesOnTheJobsRunner(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("dev", 2)
	cli := &recordingAdapter{kind: "cli"}
	api := &recordingAdapter{kind: "api"}
	cancel := startWorkerWith(t, f.agent, cli, api)
	defer cancel()

	// A job with a runner snapshot runs on that runner, with that config.
	job, err := f.agent.CreateJob(context.Background(), f.sc, agent.NewJob{
		AgentID: a.ID, Prompt: "hello",
		RunnerID: "RUN-1", RunnerKind: "api",
		RunnerConfig: json.RawMessage(`{"provider":"openai","model":"gpt-4o-mini","key_env":"OPENAI_API_KEY"}`),
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	waitJob(t, f.agent, f.sc, job.ID, agent.JobSucceeded)

	ran := api.last(t)
	if ran.ID != job.ID {
		t.Fatalf("the api adapter ran %s, want %s", ran.ID, job.ID)
	}
	if got := configField(t, ran.Config, "model"); got != "gpt-4o-mini" {
		t.Fatalf("the adapter's config = %s, want the runner's config", ran.Config)
	}
	if len(cli.jobs) != 0 {
		t.Fatalf("the cli adapter ran %d jobs, want none", len(cli.jobs))
	}
}

// configField reads one field out of a stored JSON config.
func configField(t *testing.T, config json.RawMessage, field string) string {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(config, &obj); err != nil {
		t.Fatalf("config %q is not JSON: %v", string(config), err)
	}
	v, _ := obj[field].(string)
	return v
}

func TestWorkerFallsBackToTheAgentForALegacyJob(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("legacy", 2)
	cli := &recordingAdapter{kind: "cli"}
	api := &recordingAdapter{kind: "api"}
	cancel := startWorkerWith(t, f.agent, cli, api)
	defer cancel()

	// A job queued before the split carries no runner.
	job, err := f.agent.CreateJob(context.Background(), f.sc, agent.NewJob{
		AgentID: a.ID, Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	waitJob(t, f.agent, f.sc, job.ID, agent.JobSucceeded)

	ran := cli.last(t)
	if ran.ID != job.ID {
		t.Fatalf("the cli adapter ran %s, want %s", ran.ID, job.ID)
	}
	// The agent's own config fills the adapter's config.
	var cfg map[string]any
	if err := json.Unmarshal(ran.Config, &cfg); err != nil || cfg["bin"] != "echo" {
		t.Fatalf("the adapter's config = %s, want the agent's config", ran.Config)
	}
	if len(api.jobs) != 0 {
		t.Fatalf("the api adapter ran %d jobs, want none", len(api.jobs))
	}
}

func TestJobCarriesTheRunnerSnapshot(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("snapshot", 2)
	ctx := context.Background()
	job, err := f.agent.CreateJob(ctx, f.sc, agent.NewJob{
		AgentID: a.ID, Prompt: "hello",
		RunnerID: "RUN-9", RunnerKind: "cli",
		RunnerConfig: json.RawMessage(`{"bin":"claude","args":["-p"]}`),
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	stored, err := f.agent.GetJob(ctx, f.sc, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RunnerID == nil || *stored.RunnerID != "RUN-9" {
		t.Fatalf("the job's runner = %v, want RUN-9", stored.RunnerID)
	}
	if stored.RunnerKind != "cli" {
		t.Fatalf("the job's runner kind = %q, want cli", stored.RunnerKind)
	}
	if got := configField(t, stored.RunnerConfig, "bin"); got != "claude" {
		t.Fatalf("the job's runner config = %s", stored.RunnerConfig)
	}
	// A job without a runner carries none.
	legacy, err := f.agent.CreateJob(ctx, f.sc, agent.NewJob{AgentID: a.ID, Prompt: "legacy"})
	if err != nil {
		t.Fatalf("create legacy job: %v", err)
	}
	stored, err = f.agent.GetJob(ctx, f.sc, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RunnerID != nil || stored.RunnerKind != "" {
		t.Fatalf("a legacy job carries a runner: %+v", stored)
	}
}

var _ tenancy.Scope
