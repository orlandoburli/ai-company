package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
)

// The worker's credential.
//
// A runner names a REFERENCE to a secret, never the secret itself. The worker
// resolves that reference at the moment the job runs, so the secret is never
// at rest in the database, and the adapter receives it in the job it runs.

// fakeSecrets is a resolver: it answers every reference with the same secret,
// or with an error.
type fakeSecrets struct {
	mu     sync.Mutex
	asked  []string
	secret string
	err    error
}

func (f *fakeSecrets) Resolve(_ context.Context, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, ref)
	if f.err != nil {
		return "", f.err
	}
	return f.secret, nil
}

// askedFor lists the references the resolver was asked about.
func (f *fakeSecrets) askedFor() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.asked...)
}

// syncBuffer collects the log lines, so a test can prove that a secret never
// reaches one.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startWorkerWithOptions starts a worker with the given options on top of the
// test defaults.
func startWorkerWithOptions(t *testing.T, st *agent.Store, adapters []agent.Adapter, opts ...agent.WorkerOption) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	base := []agent.WorkerOption{
		agent.WithWorkerPoll(10 * time.Millisecond),
		agent.WithWorkerLease(2 * time.Second),
	}
	w := agent.NewWorker(st, agent.NewHub(), adapters, append(base, opts...)...)
	w.Start(ctx)
	return cancel
}

func TestWorkerResolvesTheRunnersCredential(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("credentialed", 2)
	r := f.createRunner("credentialed-runner", "ai-company/github:agent")
	secrets := &fakeSecrets{secret: "sk-agent-secret"}
	logs := &syncBuffer{}
	ad := &recordingAdapter{kind: "cli"}
	cancel := startWorkerWithOptions(t, f.agent, []agent.Adapter{ad},
		agent.WithSecretResolver(secrets),
		agent.WithRunnerStore(f.runners),
		agent.WithWorkerLogger(slog.New(slog.NewTextHandler(logs, nil))))
	defer cancel()

	job, err := f.agent.CreateJob(context.Background(), f.sc, agent.NewJob{
		AgentID: a.ID, Prompt: "hello",
		RunnerID: r.ID, RunnerKind: "cli",
		RunnerConfig: json.RawMessage(`{"bin":"echo","args":["-p"]}`),
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	waitJob(t, f.agent, f.sc, job.ID, agent.JobSucceeded)

	// The adapter receives the secret, resolved at execution time.
	ran := ad.last(t)
	if ran.Credential != "sk-agent-secret" {
		t.Fatalf("the adapter's credential = %q, want the resolved secret", ran.Credential)
	}
	if ran.CredentialRef != "ai-company/github:agent" {
		t.Fatalf("the adapter's credential reference = %q, want the runner's reference", ran.CredentialRef)
	}
	if asked := secrets.askedFor(); len(asked) != 1 || asked[0] != "ai-company/github:agent" {
		t.Fatalf("the resolver was asked for %v, want the runner's reference", asked)
	}
	// The secret never reaches a log line.
	if strings.Contains(logs.String(), "sk-agent-secret") {
		t.Fatalf("the secret reached the log:\n%s", logs.String())
	}
	// The stored job carries no secret: the reference lives in the runner.
	stored, err := f.agent.GetJob(context.Background(), f.sc, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.Credential != "" || stored.CredentialRef != "" {
		t.Fatalf("the stored job carries a credential: %+v", stored)
	}
}

func TestWorkerLeavesTheCredentialEmptyWhenTheReferenceDoesNotResolve(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("unresolved", 2)
	r := f.createRunner("unresolved-runner", "ai-company/github:missing")
	secrets := &fakeSecrets{err: errors.New("integration: secret not found")}
	logs := &syncBuffer{}
	ad := &recordingAdapter{kind: "cli"}
	cancel := startWorkerWithOptions(t, f.agent, []agent.Adapter{ad},
		agent.WithSecretResolver(secrets),
		agent.WithRunnerStore(f.runners),
		agent.WithWorkerLogger(slog.New(slog.NewTextHandler(logs, nil))))
	defer cancel()

	job, err := f.agent.CreateJob(context.Background(), f.sc, agent.NewJob{
		AgentID: a.ID, Prompt: "hello",
		RunnerID: r.ID, RunnerKind: "cli",
		RunnerConfig: json.RawMessage(`{"bin":"echo","args":["-p"]}`),
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// The job still runs: the adapter falls back to its own environment
	// variable when the job carries no credential.
	waitJob(t, f.agent, f.sc, job.ID, agent.JobSucceeded)

	ran := ad.last(t)
	if ran.Credential != "" {
		t.Fatalf("the adapter's credential = %q, want none", ran.Credential)
	}
	// The reference is still there: it is what the adapter names when it has
	// no credential at all.
	if ran.CredentialRef != "ai-company/github:missing" {
		t.Fatalf("the adapter's credential reference = %q, want the reference that did not resolve", ran.CredentialRef)
	}
	// The warning names the reference, and no secret.
	if !strings.Contains(logs.String(), "ai-company/github:missing") {
		t.Fatalf("the warning does not name the reference:\n%s", logs.String())
	}
}

func TestWorkerLeavesALegacyJobWithoutACredential(t *testing.T) {
	f := newWorkerFixture(t)
	a := f.createAgent("legacy-credential", 2)
	secrets := &fakeSecrets{secret: "sk-agent-secret"}
	ad := &recordingAdapter{kind: "cli"}
	cancel := startWorkerWithOptions(t, f.agent, []agent.Adapter{ad},
		agent.WithSecretResolver(secrets),
		agent.WithRunnerStore(f.runners),
		agent.WithWorkerLogger(quietLogger()))
	defer cancel()

	// A job queued before the split carries no runner: nothing to resolve.
	job, err := f.agent.CreateJob(context.Background(), f.sc, agent.NewJob{
		AgentID: a.ID, Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	waitJob(t, f.agent, f.sc, job.ID, agent.JobSucceeded)

	ran := ad.last(t)
	if ran.Credential != "" || ran.CredentialRef != "" {
		t.Fatalf("a legacy job carries a credential: %+v", ran)
	}
	if asked := secrets.askedFor(); len(asked) != 0 {
		t.Fatalf("the resolver was asked for %v, want nothing", asked)
	}
}
