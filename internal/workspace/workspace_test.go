package workspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/workspace"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func sourceRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "init", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	git(t, repo, "config", "user.name", "Workspace Test")
	git(t, repo, "config", "user.email", "workspace@example.test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "base.txt")
	git(t, repo, "commit", "-m", "base")
	return repo
}

func prepare(t *testing.T, p *workspace.Provider, source, run, step, item string, attempt int) workspace.Workspace {
	t.Helper()
	ws, err := p.Prepare(context.Background(), workspace.Request{
		ProductID: "product", SourceURL: source, RunID: run, StepRunID: step,
		ItemID: item, Attempt: attempt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestMirrorCreationReuseAndNodeWorktrees(t *testing.T) {
	requireGit(t)
	source := sourceRepo(t)
	p := workspace.NewProvider(workspace.WithSupportDir(t.TempDir()))

	first := prepare(t, p, source, "run-one", "step-a", "item-1", 1)
	second := prepare(t, p, "", "run-one", "step-b", "item-1", 1)
	if first.MirrorPath != second.MirrorPath || first.Dir == second.Dir {
		t.Fatalf("workspaces = %+v and %+v; want reused mirror and separate directories", first, second)
	}
	if first.Branch != "aic/item-1-step-a-1" || second.Branch != "aic/item-1-step-b-1" {
		t.Fatalf("branches = %q, %q", first.Branch, second.Branch)
	}
	retry := prepare(t, p, "", "run-one", "step-a-retry", "item-1", 2)
	if retry.Branch != "aic/item-1-step-a-retry-2" || retry.Dir == first.Dir {
		t.Fatalf("retry workspace = %+v; want a fresh attempt branch and directory", retry)
	}
	emptyComponent, err := p.Prepare(context.Background(), workspace.Request{
		ProductID: "product", RunID: "run-one", StepRunID: "step-empty", Attempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if emptyComponent.Branch != "aic/x-step-empty-1" {
		t.Fatalf("empty item component branch = %q; want aic/x-step-empty-1", emptyComponent.Branch)
	}
	for _, ws := range []workspace.Workspace{first, second, retry, emptyComponent} {
		if got := git(t, ws.MirrorPath, "rev-parse", "--is-bare-repository"); got != "true" {
			t.Fatalf("mirror bare state = %q", got)
		}
		if data, err := os.ReadFile(filepath.Join(ws.Dir, "base.txt")); err != nil || string(data) != "base\n" {
			t.Fatalf("worktree base file = %q, %v", data, err)
		}
		if got := git(t, ws.Dir, "branch", "--show-current"); got != ws.Branch {
			t.Fatalf("current branch = %q; want %q", got, ws.Branch)
		}
	}
	if got := p.MirrorPath("product"); got != first.MirrorPath {
		t.Fatalf("MirrorPath = %q; want %q", got, first.MirrorPath)
	}
}

func TestIntegrationMergeAndConflict(t *testing.T) {
	requireGit(t)
	t.Run("successful merge", func(t *testing.T) {
		source := sourceRepo(t)
		p := workspace.NewProvider(workspace.WithSupportDir(t.TempDir()))
		node := prepare(t, p, source, "merge-run", "step-1", "item", 1)
		integration, err := p.Prepare(context.Background(), workspace.Request{
			ProductID: "product", RunID: "merge-run", Integration: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if integration.Dir != filepath.Join(filepath.Dir(node.Dir), "__integration__") || !integration.IsPrimary {
			t.Fatalf("integration workspace = %+v", integration)
		}
		if err := os.WriteFile(filepath.Join(node.Dir, "merged.txt"), []byte("from node\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(t, node.Dir, "add", "merged.txt")
		git(t, node.Dir, "commit", "-m", "node change")
		if err := p.Merge(context.Background(), integration, node.Branch); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(filepath.Join(integration.Dir, "merged.txt")); err != nil || string(data) != "from node\n" {
			t.Fatalf("merged file = %q, %v", data, err)
		}
		if err := p.Merge(context.Background(), node, node.Branch); !errors.Is(err, workspace.ErrNotIntegration) {
			t.Fatalf("merge to node worktree error = %v; want ErrNotIntegration", err)
		}
	})

	t.Run("conflict retains worktree", func(t *testing.T) {
		source := sourceRepo(t)
		p := workspace.NewProvider(workspace.WithSupportDir(t.TempDir()))
		node := prepare(t, p, source, "conflict-run", "step-1", "item", 1)
		integration, err := p.Prepare(context.Background(), workspace.Request{
			ProductID: "product", RunID: "conflict-run", Integration: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(node.Dir, "base.txt"), []byte("node\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(t, node.Dir, "add", "base.txt")
		git(t, node.Dir, "commit", "-m", "node conflict change")
		if err := os.WriteFile(filepath.Join(integration.Dir, "base.txt"), []byte("integration\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(t, integration.Dir, "add", "base.txt")
		git(t, integration.Dir, "commit", "-m", "integration conflict change")

		err = p.Merge(context.Background(), integration, node.Branch)
		if !errors.Is(err, workspace.ErrMergeConflict) {
			t.Fatalf("merge error = %v; want ErrMergeConflict", err)
		}
		var gitErr *workspace.GitError
		if !errors.As(err, &gitErr) || !strings.Contains(gitErr.Error(), "CONFLICT") {
			t.Fatalf("merge error lacks git conflict stderr: %#v", err)
		}
		if _, err := os.Stat(integration.Dir); err != nil {
			t.Fatalf("conflicted worktree was removed: %v", err)
		}
	})
}

func TestRemoveAndRemoveRunDeleteBranches(t *testing.T) {
	requireGit(t)
	source := sourceRepo(t)
	p := workspace.NewProvider(workspace.WithSupportDir(t.TempDir()))
	removed := prepare(t, p, source, "remove-run", "step-remove", "item", 1)
	if err := p.Remove(context.Background(), removed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removed.Dir); !os.IsNotExist(err) {
		t.Fatalf("removed worktree still exists; stat error = %v", err)
	}
	if gitErr := exec.Command("git", "-C", removed.MirrorPath, "show-ref", "--verify", "--quiet", "refs/heads/"+removed.Branch).Run(); gitErr == nil {
		t.Fatalf("removed branch %q still exists", removed.Branch)
	}

	node := prepare(t, p, "", "remove-run", "step-run", "item", 2)
	integration, err := p.Prepare(context.Background(), workspace.Request{ProductID: "product", RunID: "remove-run", Integration: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveRun(context.Background(), "remove-run"); err != nil {
		t.Fatal(err)
	}
	for _, ws := range []workspace.Workspace{node, integration} {
		if _, err := os.Stat(ws.Dir); !os.IsNotExist(err) {
			t.Fatalf("run worktree %q still exists; stat error = %v", ws.Dir, err)
		}
		if err := exec.Command("git", "-C", ws.MirrorPath, "show-ref", "--verify", "--quiet", "refs/heads/"+ws.Branch).Run(); err == nil {
			t.Fatalf("run branch %q still exists", ws.Branch)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(node.Dir))); !os.IsNotExist(err) {
		t.Fatalf("run directory still exists; stat error = %v", err)
	}
}

func TestUnsafeIDsAndMissingMirror(t *testing.T) {
	requireGit(t)
	p := workspace.NewProvider(workspace.WithSupportDir(t.TempDir()))
	_, err := p.Prepare(context.Background(), workspace.Request{ProductID: "../escape", RunID: "run", StepRunID: "step", ItemID: "item"})
	if !errors.Is(err, workspace.ErrUnsafePath) {
		t.Fatalf("unsafe product error = %v; want ErrUnsafePath", err)
	}
	_, err = p.Prepare(context.Background(), workspace.Request{ProductID: "product", RunID: "bad/run", StepRunID: "step", ItemID: "item"})
	if !errors.Is(err, workspace.ErrUnsafePath) {
		t.Fatalf("unsafe run error = %v; want ErrUnsafePath", err)
	}
	_, err = p.Prepare(context.Background(), workspace.Request{ProductID: "missing", RunID: "run", StepRunID: "step", ItemID: "item"})
	if !errors.Is(err, workspace.ErrNoMirror) {
		t.Fatalf("missing mirror error = %v; want ErrNoMirror", err)
	}
}
