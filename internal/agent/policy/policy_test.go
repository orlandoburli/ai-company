package policy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitWrapperRefusesRefTouchingOperations(t *testing.T) {
	repo := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(git, "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	var telemetry []string
	shim, err := New(filepath.Join(t.TempDir(), "shim"), func(format string, args ...any) {
		telemetry = append(telemetry, formatArgs(format, args...))
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer shim.Close()

	denied := [][]string{
		{"push"},
		{"remote", "add", "origin", "https://example.invalid/repo"},
		{"remote", "set-url", "origin", "https://example.invalid/repo"},
		{"update-ref", "refs/heads/agent", "HEAD"},
		{"gc", "--prune"},
		{"reflog", "expire", "--all"},
		{"filter-branch", "--force", "--", "--all"},
		{"worktree", "list"},
		{"-c", "core.fsmonitor=true", "status"},
	}
	for _, args := range denied {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.Command(filepath.Join(shim.Dir, "git"), args...)
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("git %v unexpectedly succeeded: %s", args, out)
			}
			if !strings.Contains(string(out), refusalReason) {
				t.Errorf("refusal %q does not explain why", out)
			}
		})
	}

	for _, args := range [][]string{{"--version"}, {"status"}} {
		cmd := exec.Command(filepath.Join(shim.Dir, "git"), args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("git %v should pass through: %v: %s", args, err, out)
		}
	}
	if err := shim.Close(); err != nil {
		t.Fatal(err)
	}
	if len(telemetry) != len(denied) {
		t.Fatalf("telemetry lines = %d, want %d: %v", len(telemetry), len(denied), telemetry)
	}
	for _, line := range telemetry {
		if !strings.Contains(line, refusalReason) {
			t.Errorf("telemetry %q lacks refusal reason", line)
		}
	}
}

func TestRMWrapperRefusesCatastrophicFormsAndAllowsNormalFile(t *testing.T) {
	root := t.TempDir()
	var telemetry []string
	shim, err := New(filepath.Join(t.TempDir(), "shim"), func(format string, args ...any) {
		telemetry = append(telemetry, formatArgs(format, args...))
	}, []string{"rm -rf custom-target"})
	if err != nil {
		t.Fatal(err)
	}
	defer shim.Close()

	denied := [][]string{
		{"-rf", "/"},
		{"-rf", "/*"},
		{"-rf", "~"},
		{"-rf", root},
		{"-rf", "custom-target"},
	}
	for _, args := range denied {
		cmd := exec.Command(filepath.Join(shim.Dir, "rm"), args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "AIC_POLICY_WORK_ROOT="+root)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("rm %v unexpectedly succeeded", args)
		}
		if !strings.Contains(string(out), "policy: refused") {
			t.Errorf("rm %v output = %q, want refusal", args, out)
		}
	}
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(shim.Dir, "rm"), file)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "AIC_POLICY_WORK_ROOT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rm file.txt should pass through: %v: %s", err, out)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("rm file.txt did not remove file: %v", err)
	}
	if err := shim.Close(); err != nil {
		t.Fatal(err)
	}
	if len(telemetry) != len(denied) {
		t.Fatalf("telemetry lines = %d, want %d: %v", len(telemetry), len(denied), telemetry)
	}
}

func TestShimPathAndCleanup(t *testing.T) {
	base := t.TempDir()
	shim, err := New(filepath.Join(base, "shim"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(shim.Path(), shim.Dir+string(os.PathListSeparator)) {
		t.Fatalf("Path() = %q, shim dir not first", shim.Path())
	}
	if err := shim.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shim.Dir); !os.IsNotExist(err) {
		t.Errorf("Close left shim directory behind: %v", err)
	}
}

const refusalReason = "the server commits and pushes; the agent does not"

func formatArgs(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
