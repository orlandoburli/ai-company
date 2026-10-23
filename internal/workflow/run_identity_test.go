package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The run's identity.
//
// The identity of a run's GitHub work is the credential of the runner the
// run's agent used. The reference is a NAME: it travels with the step's
// result, and the secret behind it is fetched at the moment the integration
// needs it.

// fakeTokens is the secret resolver: it knows a few references and refuses
// the others.
type fakeTokens struct {
	mu      sync.Mutex
	secrets map[string]string
}

func (f *fakeTokens) Resolve(_ context.Context, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	secret, ok := f.secrets[ref]
	if !ok {
		return "", errors.New("integration: secret not found: " + ref)
	}
	return secret, nil
}

// recordingFactory builds GitHub clients and remembers the token each one
// was built with: that token is the identity of the run's GitHub work.
type recordingFactory struct {
	mu     sync.Mutex
	tokens []string
}

func (f *recordingFactory) build(token, owner, repo string) GitHubService {
	f.mu.Lock()
	f.tokens = append(f.tokens, token)
	f.mu.Unlock()
	return &fakeIntegrationGitHub{}
}

func (f *recordingFactory) used() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.tokens...)
}

// credentialRunner registers a runner that runs under a credential
// reference.
func (f *resolutionFixture) credentialRunner(sc tenancy.Scope, name, bin, ref string) runner.Runner {
	f.t.Helper()
	r, err := f.runners.Create(context.Background(), sc, runner.NewRunner{
		Name: name, Kind: runner.KindCLI, Config: json.RawMessage(`{"bin":"` + bin + `","args":["-p"]}`),
		CredentialRef: ref,
	})
	if err != nil {
		f.t.Fatalf("create runner %q: %v", name, err)
	}
	return r
}

// registerSecret registers the secret reference the tenant holds for a
// platform.
func (f *resolutionFixture) registerSecret(sc tenancy.Scope, kind, ref string) {
	f.t.Helper()
	err := f.store.withTenant(context.Background(), sc, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO integration_credentials (id,tenant_id,kind,secret_ref) VALUES ($1,$2,$3,$4)`,
			"IC-"+strings.ToUpper(uuid.NewString()[:8]), sc.TenantID, kind, ref)
		return err
	})
	if err != nil {
		f.t.Fatalf("register the %s secret: %v", kind, err)
	}
}

// engineWith builds an engine with the platform connectors wired.
func (f *resolutionFixture) engineWith(svc *Services) *Engine {
	return NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Work: f.work,
		Integrations: svc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// startEngineWorker starts the worker that runs the run's agent jobs.
func startEngineWorker(t *testing.T, f *resolutionFixture, output string) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	worker := agent.NewWorker(f.agents, agent.NewHub(), []agent.Adapter{&engineAdapter{output: output}},
		agent.WithWorkerPoll(10*time.Millisecond), agent.WithWorkerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	worker.Start(ctx)
	return cancel
}

// TestAgentStepCarriesTheRunnersCredential checks that the agent step says
// which credential its runner ran under: the reference is a name, and it is
// what the run's GitHub work follows.
func TestAgentStepCarriesTheRunnersCredential(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	r := f.credentialRunner(f.sc, "claude", "claude", "ai-company/github:agent")
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{r.ID}); err != nil {
		t.Fatal(err)
	}
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "only", Kind: NodeAgent, Name: "only step", Type: "agent.worker", TypeVersion: 1,
			Config: json.RawMessage(`{"agent_id":"` + a.ID + `","prompt_tmpl":"do the work"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{}}
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "credential", Graph: graph, CreatedBy: f.sc.UserID})
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
	if got := stepResultField(t, step, "runner_credential"); got != "ai-company/github:agent" {
		t.Fatalf("the step's runner_credential = %q, want the runner's reference", got)
	}

	// The finished step keeps it: the result is what the integration reads.
	cancel := startEngineWorker(t, f, "the work is done")
	defer cancel()
	waitJobState(t, f.agents, f.sc, step.AgentJobID, agent.JobSucceeded)
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err = f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, _ = latestStep(steps, "only", "")
	if step.State != StepSucceeded {
		t.Fatalf("the step = %+v, want it succeeded", step)
	}
	if got := stepResultField(t, step, "runner_credential"); got != "ai-company/github:agent" {
		t.Fatalf("the finished step's runner_credential = %q, want the runner's reference", got)
	}
}

// stepResultField reads one field out of a step's result.
func stepResultField(t *testing.T, step StepRun, field string) string {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(step.Result, &result); err != nil {
		t.Fatalf("the step's result %s is not JSON: %v", step.Result, err)
	}
	value, _ := result[field].(string)
	return value
}

// identityGraph is a run that works and then opens a pull request.
func identityGraph(agentID, nodeConfig string) Template {
	return Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "work", Kind: NodeAgent, Name: "work", Type: "agent.worker", TypeVersion: 1,
			Config: json.RawMessage(`{"agent_id":"` + agentID + `","prompt_tmpl":"do the work"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "pr", Kind: NodeIntegration, Name: "open pr", Type: "github.open_pr", TypeVersion: 1,
			Config: json.RawMessage(nodeConfig), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{{ID: "work-pr", From: PortRef{Node: "work", Port: "main"}, To: PortRef{Node: "pr", Port: "in"}}}}
}

// runIdentityGraph runs a template to the end and returns the integration
// step.
func runIdentityGraph(t *testing.T, f *resolutionFixture, e *Engine, graph Template) StepRun {
	t.Helper()
	ctx := context.Background()
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "identity-" + strings.ToUpper(uuid.NewString()[:8]), Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
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
	work, ok := latestStep(steps, "work", "")
	if !ok || work.AgentJobID == "" {
		t.Fatalf("the agent node was not dispatched: %+v", work)
	}
	cancel := startEngineWorker(t, f, "the work is done")
	defer cancel()
	waitJobState(t, f.agents, f.sc, work.AgentJobID, agent.JobSucceeded)
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err = f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	pr, _ := latestStep(steps, "pr", "")
	return pr
}

// TestIntegrationRunsUnderTheAgentsCredential checks that the run's GitHub
// work runs under the credential of the runner the run's agent used — not
// under the tenant's registered one.
func TestIntegrationRunsUnderTheAgentsCredential(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	r := f.credentialRunner(f.sc, "claude", "claude", "ai-company/github:agent")
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{r.ID}); err != nil {
		t.Fatal(err)
	}
	f.registerSecret(f.sc, "github", "ai-company/github:tenant")
	tokens := &fakeTokens{secrets: map[string]string{
		"ai-company/github:agent":  "agent-token",
		"ai-company/github:tenant": "tenant-token",
	}}
	factory := &recordingFactory{}
	e := f.engineWith(&Services{Tokens: tokens, GitHubFactory: factory.build})

	pr := runIdentityGraph(t, f, e, identityGraph(a.ID, `{"repo":"acme/demo","base":"main","title":"Fix"}`))
	if pr.State != StepSucceeded {
		t.Fatalf("the integration step = %+v (%s), want it succeeded", pr, pr.Result)
	}
	used := factory.used()
	if len(used) != 1 || used[0] != "agent-token" {
		t.Fatalf("the GitHub client was built with %v, want the agent's credential", used)
	}
}

// TestIntegrationFallsBackToTheTenantsCredential checks that a run whose
// agent carried no credential falls back to the reference the tenant
// registered.
func TestIntegrationFallsBackToTheTenantsCredential(t *testing.T) {
	f := newResolutionFixture(t)
	f.registerSecret(f.sc, "github", "ai-company/github:tenant")
	tokens := &fakeTokens{secrets: map[string]string{"ai-company/github:tenant": "tenant-token"}}
	factory := &recordingFactory{}
	e := f.engineWith(&Services{Tokens: tokens, GitHubFactory: factory.build})

	ctx := context.Background()
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "pr", Kind: NodeIntegration, Name: "open pr", Type: "github.open_pr", TypeVersion: 1,
			Config: json.RawMessage(`{"repo":"acme/demo","base":"main","title":"Fix"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{}}
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "tenant-identity", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
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
	pr, _ := latestStep(steps, "pr", "")
	if pr.State != StepSucceeded {
		t.Fatalf("the integration step = %+v (%s), want it succeeded", pr, pr.Result)
	}
	used := factory.used()
	if len(used) != 1 || used[0] != "tenant-token" {
		t.Fatalf("the GitHub client was built with %v, want the tenant's credential", used)
	}
}

// TestIntegrationFallsBackWhenTheAgentsCredentialDoesNotResolve checks that
// a reference which does not resolve is not an identity: the tenant's
// registered reference takes over.
func TestIntegrationFallsBackWhenTheAgentsCredentialDoesNotResolve(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	r := f.credentialRunner(f.sc, "claude", "claude", "ai-company/github:agent")
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{r.ID}); err != nil {
		t.Fatal(err)
	}
	f.registerSecret(f.sc, "github", "ai-company/github:tenant")
	tokens := &fakeTokens{secrets: map[string]string{
		"ai-company/github:tenant": "tenant-token",
	}}
	factory := &recordingFactory{}
	e := f.engineWith(&Services{Tokens: tokens, GitHubFactory: factory.build})

	pr := runIdentityGraph(t, f, e, identityGraph(a.ID, `{"repo":"acme/demo","base":"main","title":"Fix"}`))
	if pr.State != StepSucceeded {
		t.Fatalf("the integration step = %+v (%s), want it succeeded", pr, pr.Result)
	}
	used := factory.used()
	if len(used) != 1 || used[0] != "tenant-token" {
		t.Fatalf("the GitHub client was built with %v, want the tenant's credential", used)
	}
}

// TestIntegrationKeepsTheNodesOwnCredential checks that the node's own
// reference wins over the run's identity.
func TestIntegrationKeepsTheNodesOwnCredential(t *testing.T) {
	f := newResolutionFixture(t)
	ctx := context.Background()
	r := f.credentialRunner(f.sc, "claude", "claude", "ai-company/github:agent")
	a := f.agent(f.sc, "dev")
	if err := f.runners.SetChain(ctx, f.sc, a.ID, []string{r.ID}); err != nil {
		t.Fatal(err)
	}
	f.registerSecret(f.sc, "github", "ai-company/github:tenant")
	tokens := &fakeTokens{secrets: map[string]string{
		"ai-company/github:agent":  "agent-token",
		"ai-company/github:tenant": "tenant-token",
		"ai-company/github:node":   "node-token",
	}}
	factory := &recordingFactory{}
	e := f.engineWith(&Services{Tokens: tokens, GitHubFactory: factory.build})

	pr := runIdentityGraph(t, f, e, identityGraph(a.ID, `{"repo":"acme/demo","base":"main","title":"Fix","secret_ref":"ai-company/github:node"}`))
	if pr.State != StepSucceeded {
		t.Fatalf("the integration step = %+v (%s), want it succeeded", pr, pr.Result)
	}
	used := factory.used()
	if len(used) != 1 || used[0] != "node-token" {
		t.Fatalf("the GitHub client was built with %v, want the node's own credential", used)
	}
}

// TestIntegrationWithoutAnyCredentialFailsAsUnconfigured checks that a run
// whose references do not resolve leaves the integration without a
// credential: the GitHub service is then not configured.
func TestIntegrationWithoutAnyCredentialFailsAsUnconfigured(t *testing.T) {
	f := newResolutionFixture(t)
	f.registerSecret(f.sc, "github", "ai-company/github:tenant")
	tokens := &fakeTokens{secrets: map[string]string{}}
	factory := &recordingFactory{}
	e := f.engineWith(&Services{Tokens: tokens, GitHubFactory: factory.build})

	ctx := context.Background()
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "pr", Kind: NodeIntegration, Name: "open pr", Type: "github.open_pr", TypeVersion: 1,
			Config: json.RawMessage(`{"repo":"acme/demo","base":"main","title":"Fix"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{}}
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "no-identity", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
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
	pr, _ := latestStep(steps, "pr", "")
	if pr.State != StepFailed {
		t.Fatalf("the integration step = %+v, want it failed", pr)
	}
	if !strings.Contains(string(pr.Result), "GitHub service is not configured") {
		t.Fatalf("the failure does not say the GitHub service is not configured: %s", pr.Result)
	}
	if used := factory.used(); len(used) != 0 {
		t.Fatalf("a GitHub client was built with %v, want none", used)
	}
}
