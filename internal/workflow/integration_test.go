package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/integration"
)

type fakeIntegrationGitHub struct {
	pr       integration.PullRequest
	open     bool
	creates  int
	comments []integration.Comment
	checks   []integration.Check
}

func (f *fakeIntegrationGitHub) GetPullRequest(context.Context, int) (integration.PullRequest, error) {
	return f.pr, nil
}
func (f *fakeIntegrationGitHub) ListChecks(context.Context, int) ([]integration.Check, error) {
	return f.checks, nil
}
func (f *fakeIntegrationGitHub) CreatePullRequest(_ context.Context, in integration.PullRequestInput) (integration.PullRequest, error) {
	f.creates++
	f.open = true
	f.pr = integration.PullRequest{Number: 12, State: "open", Head: in.Head, Base: in.Base, URL: "https://github.test/pull/12"}
	return f.pr, nil
}
func (f *fakeIntegrationGitHub) AddComment(_ context.Context, n int, body string) (integration.Comment, error) {
	c := integration.Comment{ID: int64(len(f.comments) + 1), Body: body, URL: "https://github.test/pull/12#comment"}
	f.comments = append(f.comments, c)
	return c, nil
}
func (f *fakeIntegrationGitHub) GetRef(context.Context, string) (integration.Ref, error) {
	return integration.Ref{}, nil
}
func (f *fakeIntegrationGitHub) FindOpenPullRequest(_ context.Context, head string) (integration.PullRequest, bool, error) {
	return f.pr, f.open && f.pr.Head == head, nil
}
func (f *fakeIntegrationGitHub) FindComment(_ context.Context, _ int, marker string) (integration.Comment, bool, error) {
	for _, c := range f.comments {
		if strings.Contains(c.Body, marker) {
			return c, true, nil
		}
	}
	return integration.Comment{}, false, nil
}

type fakeIntegrationGit struct{ pushes int }

func (g *fakeIntegrationGit) PushBranch(context.Context, string, string, string) (integration.Commit, error) {
	g.pushes++
	return integration.Commit{SHA: "abc123", Subject: "change"}, nil
}
func (*fakeIntegrationGit) HeadCommit(context.Context, string) (integration.Commit, error) {
	return integration.Commit{SHA: "abc123", Subject: "change"}, nil
}
func (*fakeIntegrationGit) Status(context.Context, string) (integration.Status, error) {
	return integration.Status{Clean: true}, nil
}

func TestIntegrationOpenPRCheckThenActAndArtifact(t *testing.T) {
	gh, git := &fakeIntegrationGitHub{}, &fakeIntegrationGit{}
	svc := Services{GitHub: gh, Git: git}
	in := IntegrationRequest{Type: "github.open_pr", RunID: "run1", NodeID: "pr", IterationPath: "loop:1", RunIndex: 2, Workdir: "/work", Config: json.RawMessage(`{"repo":"acme/demo","base":"main","head":"aic/item-step-1","title":"Fix","body":"Done"}`)}
	first, err := RunIntegration(t.Context(), svc, in)
	if err != nil || gh.creates != 1 || git.pushes != 1 || len(first.Artifacts) != 1 || first.Artifacts[0].Kind != "pr" {
		t.Fatalf("first result %#v err %v creates=%d pushes=%d", first, err, gh.creates, git.pushes)
	}
	var result map[string]any
	_ = json.Unmarshal(first.Result, &result)
	if result["number"] != float64(12) {
		t.Fatalf("result %v", result)
	}
	second, err := RunIntegration(t.Context(), svc, in)
	if err != nil || gh.creates != 1 || git.pushes != 1 {
		t.Fatalf("replay result %#v err %v creates=%d pushes=%d", second, err, gh.creates, git.pushes)
	}
	_ = json.Unmarshal(second.Result, &result)
	if result["reused"] != true {
		t.Fatalf("replay not reported as reused: %v", result)
	}
}

func TestIntegrationOpenPRUsesExecutionRepoAndBranchDefaults(t *testing.T) {
	gh, git := &fakeIntegrationGitHub{}, &fakeIntegrationGit{}
	result, err := RunIntegration(t.Context(), Services{GitHub: gh, Git: git}, IntegrationRequest{
		Type: "github.open_pr", RunID: "run-demo", StepRunID: "step-demo", NodeID: "open-pr",
		Workdir: "/run/worktree", Branch: "aic/item-step-2",
		RepoURL: "git@github.com:orlandoburli/fazgiro.git", ProductID: "fazgiro",
		Config: json.RawMessage(`{"base":"main","title":"FazGiro fix"}`),
	})
	if err != nil || gh.creates != 1 || git.pushes != 1 || len(result.Artifacts) != 1 {
		t.Fatalf("defaulted open PR: result=%#v err=%v creates=%d pushes=%d", result, err, gh.creates, git.pushes)
	}
	if gh.pr.Head != "aic/item-step-2" || gh.pr.Base != "main" {
		t.Fatalf("PR refs did not use execution branch/default base: %#v", gh.pr)
	}
}

func TestIntegrationWaitCIFailureAndTimeout(t *testing.T) {
	gh := &fakeIntegrationGitHub{checks: []integration.Check{{Name: "tests", Status: "failure"}}}
	_, err := RunIntegration(t.Context(), Services{GitHub: gh}, IntegrationRequest{Type: "github.wait_ci", Config: json.RawMessage(`{"repo":"acme/demo","pr":12,"timeout_s":1,"poll_interval_s":1}`)})
	assertIntegrationFailure(t, err, FailureTransient, "tests")
	gh.checks = []integration.Check{{Name: "tests", Status: "pending"}}
	started := time.Now()
	_, err = RunIntegration(t.Context(), Services{GitHub: gh}, IntegrationRequest{Type: "github.wait_ci", Config: json.RawMessage(`{"repo":"acme/demo","pr":12,"timeout_s":1,"poll_interval_s":1}`)})
	assertIntegrationFailure(t, err, FailureTransient, "timed out")
	if time.Since(started) > 3*time.Second {
		t.Fatal("timeout poll exceeded expected bound")
	}
}

func TestIntegrationWaitCIFindsRunPRByExecutionBranch(t *testing.T) {
	gh := &fakeIntegrationGitHub{
		pr:     integration.PullRequest{Number: 12, State: "open", Head: "aic/item-step-1"},
		open:   true,
		checks: []integration.Check{{Name: "tests", Status: "success"}},
	}
	result, err := RunIntegration(t.Context(), Services{GitHub: gh}, IntegrationRequest{
		Type: "github.wait_ci", Branch: "aic/item-step-1",
		RepoURL: "git@github.com:orlandoburli/fazgiro.git",
		Config:  json.RawMessage(`{"timeout_s":1,"poll_interval_s":1}`),
	})
	if err != nil {
		t.Fatalf("wait on discovered PR: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(result.Result, &out); err != nil || out["pr"] != float64(12) {
		t.Fatalf("wait result did not record discovered PR: %v, err=%v", out, err)
	}
}

func TestIntegrationCommentIdempotentAndUnknownCapability(t *testing.T) {
	gh := &fakeIntegrationGitHub{}
	svc := Services{GitHub: gh}
	in := IntegrationRequest{Type: "github.comment", RunID: "r", NodeID: "c", Config: json.RawMessage(`{"repo":"acme/demo","pr":12,"body":"hello"}`)}
	if _, err := RunIntegration(t.Context(), svc, in); err != nil {
		t.Fatal(err)
	}
	if _, err := RunIntegration(t.Context(), svc, in); err != nil {
		t.Fatal(err)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("posted %d duplicate comments", len(gh.comments))
	}
	_, err := RunIntegration(t.Context(), svc, IntegrationRequest{Type: "github.merge_pr", Config: json.RawMessage(`{"repo":"acme/demo"}`)})
	assertIntegrationFailure(t, err, FailureCapability, "unknown integration type")
}

func assertIntegrationFailure(t *testing.T, err error, want FailureKind, contains string) {
	t.Helper()
	var typed *IntegrationError
	if !errors.As(err, &typed) || typed.Kind != want || !strings.Contains(err.Error(), contains) {
		t.Fatalf("failure = %#v (%v), want %s containing %q", typed, err, want, contains)
	}
}
