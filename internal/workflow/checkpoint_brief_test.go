package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/work"
)

// briefRepo builds a worktree with one commit and one pending change.
func briefRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-m", "initial commit")
	return dir
}

// briefArtifact finds an artifact summary by kind.
func briefArtifact(artifacts []ArtifactSummary, kind string) (ArtifactSummary, bool) {
	for _, artifact := range artifacts {
		if artifact.Kind == kind {
			return artifact, true
		}
	}
	return ArtifactSummary{}, false
}

func TestBuildCheckpointBriefCarriesTheDecision(t *testing.T) {
	dir := briefRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief, artifacts, err := BuildCheckpointBrief(BriefInput{
		ItemNumber: 42,
		ItemTitle:  "Fix the flaky test",
		Question:   "Ship the fix?",
		Steps: []BriefStep{
			{Name: "implement the fix", Kind: "agent", State: "succeeded", Summary: "Added the retry", Output: "I fixed the retry loop."},
			{Name: "run the tests", Kind: "agent", State: "failed", Summary: "The suite is red"},
			{Name: "ship it", Kind: "checkpoint", State: "waiting"},
		},
		Workdir:    dir,
		Branch:     "aic/item-1-step-1-1",
		BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("brief: %v", err)
	}
	for _, want := range []string{
		"## Fix the flaky test (42)",
		"Ship the fix?",
		"**What happened so far**",
		"implement the fix",
		"succeeded",
		"Added the retry",
		"run the tests",
		"failed",
		"**The agent says**",
		"I fixed the retry loop.",
		"**The change**",
		"aic/item-1-step-1-1",
		"initial commit",
		"1 file changed",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief is missing %q:\n%s", want, brief)
		}
	}
	// The checkpoint's own node is a question, not work.
	if strings.Contains(brief, "ship it") {
		t.Fatalf("brief reports the checkpoint node as work:\n%s", brief)
	}
	branch, ok := briefArtifact(artifacts, "branch")
	if !ok || branch.URI != "aic/item-1-step-1-1" {
		t.Fatalf("branch artifact=%+v", branch)
	}
	commit, ok := briefArtifact(artifacts, "commit")
	if !ok || !strings.Contains(commit.URI, "") || commit.URI == "" {
		t.Fatalf("commit artifact=%+v", commit)
	}
	diff, ok := briefArtifact(artifacts, "diff_summary")
	if !ok || !strings.Contains(diff.URI, "1 file changed") {
		t.Fatalf("diff artifact=%+v", diff)
	}
}

func TestBuildCheckpointBriefMarksTruncations(t *testing.T) {
	long := strings.Repeat("the agent keeps talking and the card cannot hold it all. ", 200)
	brief, _, err := BuildCheckpointBrief(BriefInput{
		ItemNumber: 7,
		ItemTitle:  "Long output",
		Question:   "Approve?",
		Steps: []BriefStep{
			{Name: "talk", Kind: "agent", State: "succeeded", Summary: long, Output: long},
		},
	})
	if err != nil {
		t.Fatalf("brief: %v", err)
	}
	if !strings.Contains(brief, truncationMarker) {
		t.Fatalf("a cut text is not marked:\n%s", brief)
	}
	if len(brief) > briefMaxBytes {
		t.Fatalf("brief is %d bytes, over the %d limit", len(brief), briefMaxBytes)
	}
	if !strings.Contains(brief, "**The agent says**") {
		t.Fatalf("brief lost the agent's words:\n%s", brief)
	}
}

func TestBuildCheckpointBriefWithoutWorktreeOmitsTheChange(t *testing.T) {
	brief, artifacts, err := BuildCheckpointBrief(BriefInput{
		ItemNumber: 3,
		ItemTitle:  "Scratch run",
		Question:   "Approve?",
		Steps:      []BriefStep{{Name: "think", Kind: "agent", State: "succeeded", Summary: "thought about it"}},
	})
	if err != nil {
		t.Fatalf("brief: %v", err)
	}
	if strings.Contains(brief, "**The change**") {
		t.Fatalf("a run without a worktree has no change:\n%s", brief)
	}
	for _, kind := range []string{"branch", "commit", "diff_summary"} {
		if _, ok := briefArtifact(artifacts, kind); ok {
			t.Fatalf("artifact %s without a worktree: %+v", kind, artifacts)
		}
	}
}

func TestBuildCheckpointBriefFailingGitOmitsTheChange(t *testing.T) {
	dir := t.TempDir() // not a repository: git fails
	brief, artifacts, err := BuildCheckpointBrief(BriefInput{
		ItemNumber: 4,
		ItemTitle:  "Broken worktree",
		Question:   "Approve?",
		Workdir:    dir,
		Branch:     "aic/item-1-step-1-1",
		BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("a failing git must not fail the brief: %v", err)
	}
	if strings.Contains(brief, "files changed") {
		t.Fatalf("a failing git produced a diff stat:\n%s", brief)
	}
	if _, ok := briefArtifact(artifacts, "diff_summary"); ok {
		t.Fatalf("a failing git produced a diff artifact: %+v", artifacts)
	}
}

func TestBuildCheckpointBriefCarriesNoSecret(t *testing.T) {
	// An operator's home with a credential in the global git configuration, and
	// a secret in the environment: neither may reach the brief.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tsigningkey = sk-live-operator-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AIC_TEST_SECRET", "sk-live-environment-secret")

	dir := briefRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief, _, err := BuildCheckpointBrief(BriefInput{
		ItemNumber: 9,
		ItemTitle:  "Secret check",
		Question:   "Approve?",
		Steps:      []BriefStep{{Name: "work", Kind: "agent", State: "succeeded", Summary: "done", Output: "done"}},
		Workdir:    dir,
		Branch:     "aic/item-1-step-1-1",
		BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("brief: %v", err)
	}
	for _, secret := range []string{"sk-live-operator-secret", "sk-live-environment-secret", home} {
		if strings.Contains(brief, secret) {
			t.Fatalf("brief carries %q:\n%s", secret, brief)
		}
	}
}

func TestBriefQuestionReadsTheQuestionBack(t *testing.T) {
	brief, _, err := BuildCheckpointBrief(BriefInput{ItemNumber: 1, ItemTitle: "Work", Question: "Ship it?"})
	if err != nil {
		t.Fatalf("brief: %v", err)
	}
	if got := BriefQuestion(brief); got != "Ship it?" {
		t.Fatalf("question=%q", got)
	}
	if got := DefaultCheckpointQuestion("review"); got != "Approve review?" {
		t.Fatalf("default question=%q", got)
	}
	if got := DefaultCheckpointQuestion(""); got == "" {
		t.Fatal("the default question is empty")
	}
}

func TestNodeQuestionReadsTheGraphSnapshot(t *testing.T) {
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "n_gate", Kind: NodeCheckpoint, Name: "review", Type: "human.review", TypeVersion: 1, Config: json.RawMessage(`{"question":"Review this"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "n_plain", Kind: NodeCheckpoint, Name: "ship it", Type: "human.review", TypeVersion: 1, Config: json.RawMessage(`{}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}}
	snapshot, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if question, ok := NodeQuestion(snapshot, "n_gate"); !ok || question != "Review this" {
		t.Fatalf("question=%q ok=%v", question, ok)
	}
	if question, ok := NodeQuestion(snapshot, "n_plain"); !ok || question != "Approve ship it?" {
		t.Fatalf("default question=%q ok=%v", question, ok)
	}
	if _, ok := NodeQuestion(snapshot, "missing"); ok {
		t.Fatal("a node that is not in the snapshot must not answer")
	}
	if question := CheckpointQuestion(nil, "missing", "review", "**The question:** Ship it?"); question != "Ship it?" {
		t.Fatalf("question=%q", question)
	}
	if question := CheckpointQuestion(nil, "missing", "review", ""); question != "Approve review?" {
		t.Fatalf("question=%q", question)
	}
}

// TestEngineCheckpointRecordsTheWorktreeArtifacts checks that the facts of
// the worktree become artifacts of the run — once.
func TestEngineCheckpointRecordsTheWorktreeArtifacts(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	a := f.createAgent(t)
	item := briefItem(t, f, "Fix the flaky test")
	repo := briefRepo(t)
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "work", Kind: NodeAgent, Name: "implement the fix", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"implement"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "gate", Kind: NodeCheckpoint, Name: "ship it", Type: "human.review", TypeVersion: 1, Config: json.RawMessage(`{"question":"Ship the fix?","policy":"retry_step"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{{ID: "work-gate", From: PortRef{Node: "work", Port: "main"}, To: PortRef{Node: "gate", Port: "in"}}}}
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work})
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "artifact-flow", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.StartRun(ctx, f.sc, tpl.ID, item.ID, f.sc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	work, ok := latestStep(steps, "work", "")
	if !ok || work.AgentJobID == "" {
		t.Fatalf("the agent step did not dispatch: %+v", work)
	}
	// The execution worked in a worktree: the step says which one.
	workdir := repo
	if _, err := f.store.UpdateStepRun(ctx, f.sc, work.ID, StepRunPatch{
		Workdir:         &workdir,
		ContextManifest: ptrRaw(rawJSON(map[string]any{"branch": "aic/item-step-1", "workdir": repo})),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := agent.NewWorker(f.agents, agent.NewHub(), []agent.Adapter{&engineAdapter{output: "I fixed the retry loop."}},
		agent.WithWorkerPoll(10*time.Millisecond), agent.WithWorkerCount(2), agent.WithWorkerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker.Start(wctx)
	waitJobState(t, f.agents, f.sc, work.AgentJobID, agent.JobSucceeded)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := f.store.ListCheckpoints(ctx, f.sc, "pending", 10)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("checkpoints=%d err=%v", len(checkpoints), err)
	}
	summary := checkpoints[0].SummaryMD
	for _, want := range []string{"**The change**", "aic/item-step-1", "1 file changed"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("the checkpoint summary is missing %q:\n%s", want, summary)
		}
	}
	artifacts, err := f.store.ListArtifacts(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, artifact := range artifacts {
		kinds[artifact.Kind]++
	}
	for _, kind := range []string{"branch", "commit", "diff_summary"} {
		if kinds[kind] != 1 {
			t.Fatalf("artifact %s recorded %d times: %+v", kind, kinds[kind], artifacts)
		}
	}
}

// TestUpsertRunArtifactRecordsOnce checks that the facts of a worktree are
// recorded once per run: a later execution updates the row.
func TestUpsertRunArtifactRecordsOnce(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	graph := Template{SchemaVersion: 1, Nodes: []Node{{ID: "gate", Kind: NodeCheckpoint, Name: "review", Type: "human.review", TypeVersion: 1, Config: json.RawMessage(`{"policy":"retry_step"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch}}}
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "upsert", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.store.CreateRun(ctx, f.sc, NewRun{TemplateID: tpl.ID, TemplateVersion: tpl.Version, DefinitionSnapshot: mustJSON(graph), StartedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.store.UpsertRunArtifact(ctx, f.sc, NewArtifact{RunID: run.ID, Kind: "branch", URI: "aic/one", Meta: rawJSON(map[string]any{"branch": "aic/one"})})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.UpsertRunArtifact(ctx, f.sc, NewArtifact{RunID: run.ID, Kind: "branch", URI: "aic/two", Meta: rawJSON(map[string]any{"branch": "aic/two"})})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("the artifact was recorded twice: %s and %s", first.ID, second.ID)
	}
	artifacts, err := f.store.ListArtifacts(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].URI != "aic/two" {
		t.Fatalf("artifacts=%+v", artifacts)
	}
}

// briefItem creates the work item a run is about.
func briefItem(t *testing.T, f *engineFixture, title string) work.Item {
	t.Helper()
	ctx := context.Background()
	product, err := f.work.CreateProduct(ctx, f.sc, "Acme", "acme", "https://github.com/acme/p", "")
	if err != nil {
		t.Fatal(err)
	}
	project, err := f.work.CreateProject(ctx, f.sc, product.ID, nil, "Core", "CORE")
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.work.CreateItem(ctx, f.sc, work.NewItem{ProjectID: project.ID, Kind: "bug", Title: title, ActorUserID: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// TestEngineCheckpointSummaryIsTheBrief checks that the checkpoint a human
// decides on carries the work, not the node's name.
func TestEngineCheckpointSummaryIsTheBrief(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	a := f.createAgent(t)
	item := briefItem(t, f, "Fix the flaky test")
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "work", Kind: NodeAgent, Name: "implement the fix", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"implement"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "gate", Kind: NodeCheckpoint, Name: "ship it", Type: "human.review", TypeVersion: 1, Config: json.RawMessage(`{"question":"Ship the fix?","policy":"retry_step"}`), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{{ID: "work-gate", From: PortRef{Node: "work", Port: "main"}, To: PortRef{Node: "gate", Port: "in"}}}}
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work})
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "brief-flow", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.StartRun(ctx, f.sc, tpl.ID, item.ID, f.sc.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	work, ok := latestStep(steps, "work", "")
	if !ok || work.AgentJobID == "" {
		t.Fatalf("the agent step did not dispatch: %+v", work)
	}
	worker := agent.NewWorker(f.agents, agent.NewHub(), []agent.Adapter{&engineAdapter{output: "I fixed the retry loop."}},
		agent.WithWorkerPoll(10*time.Millisecond), agent.WithWorkerCount(2), agent.WithWorkerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker.Start(wctx)
	waitJobState(t, f.agents, f.sc, work.AgentJobID, agent.JobSucceeded)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	checkpoints, err := f.store.ListCheckpoints(ctx, f.sc, "pending", 10)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("checkpoints=%d err=%v", len(checkpoints), err)
	}
	summary := checkpoints[0].SummaryMD
	for _, want := range []string{"Ship the fix?", "Fix the flaky test", "implement the fix", "I fixed the retry loop."} {
		if !strings.Contains(summary, want) {
			t.Fatalf("the checkpoint summary is missing %q:\n%s", want, summary)
		}
	}
	if summary == "ship it" || summary == "gate" {
		t.Fatalf("the checkpoint summary is the node's name:\n%s", summary)
	}
}

func TestDefaultCheckpointQuestionReadsWell(t *testing.T) {
	cases := map[string]string{
		"approve spec":            "Approve spec?",
		"accept the pull request": "Accept the pull request?",
		"accept findings":         "Accept findings?",
		"review the diff":         "Review the diff?",
		"spec":                    "Approve spec?",
		"":                        "Approve this step?",
	}
	for name, want := range cases {
		if got := DefaultCheckpointQuestion(name); got != want {
			t.Errorf("DefaultCheckpointQuestion(%q) = %q, want %q", name, got, want)
		}
	}
}
