package integration

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialsResolveEnvironmentOverride(t *testing.T) {
	t.Setenv("AIC_SECRET_ai_company_github_orlandoburli", "only-in-memory-token")
	secret, err := (Credentials{}).Resolve(t.Context(), "ai-company/github:orlandoburli")
	if err != nil || secret != "only-in-memory-token" {
		t.Fatalf("resolve returned %q, %v", secret, err)
	}
	_, err = (Credentials{LookupEnv: func(string) (string, bool) { return "", false }, GOOS: "linux"}).Resolve(t.Context(), "missing/ref")
	if !errors.Is(err, ErrNoSecret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("missing error leaked or was not typed: %v", err)
	}
	if got := Redact("token=only-in-memory-token", secret); got != "token=[REDACTED]" {
		t.Fatalf("redaction: %q", got)
	}
}

func TestCredentialsKeychainLookupWhenSecurityExists(t *testing.T) {
	if _, err := exec.LookPath("security"); err != nil {
		t.Skip("macOS security binary unavailable")
	}
	_, err := (Credentials{LookupEnv: func(string) (string, bool) { return "", false }}).Resolve(t.Context(), "aic-no-such-service/aic-no-such-account")
	if !errors.Is(err, ErrNoSecret) {
		t.Fatalf("missing keychain entry error = %v", err)
	}
}

func TestGitHubClientCreatePRChecksAndSanitizedError(t *testing.T) {
	const token = "fake-secret-token-93845"
	var sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "" {
			t.Error("token appeared in URL")
		}
		if r.Header.Get("Authorization") == "Bearer "+token {
			sawAuth = true
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/demo/pulls":
			var in PullRequestInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Head != "aic/42-step-1" {
				t.Errorf("head = %q", in.Head)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": "open", "title": in.Title, "body": in.Body, "html_url": "https://github.test/pull/7", "head": map[string]any{"ref": in.Head, "sha": "deadbeef"}, "base": map[string]any{"ref": in.Base}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/demo/pulls/7":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": "open", "html_url": "https://github.test/pull/7", "head": map[string]any{"ref": "aic/42-step-1", "sha": "deadbeef"}, "base": map[string]any{"ref": "main"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/demo/commits/deadbeef/check-runs":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"check_runs": []any{map[string]any{"name": "unit", "status": "completed", "conclusion": "failure", "html_url": "https://checks.test/1"}}})
		case r.URL.Path == "/repos/acme/demo/fail":
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmtF := `{"message":"rejected ` + token + `"}`
			_, _ = w.Write([]byte(fmtF))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	gh := NewGitHub(server.Client(), server.URL, token, "acme", "demo")
	pr, err := gh.CreatePullRequest(t.Context(), PullRequestInput{Base: "main", Head: "aic/42-step-1", Title: "Fix", Body: "Details"})
	if err != nil || pr.Number != 7 || pr.URL != "https://github.test/pull/7" {
		t.Fatalf("created PR %#v, %v", pr, err)
	}
	checks, err := gh.ListChecks(t.Context(), 7)
	if err != nil || len(checks) != 1 || checks[0].Status != "failure" {
		t.Fatalf("checks %#v, %v", checks, err)
	}
	err = gh.request(t.Context(), http.MethodGet, "/repos/acme/demo/fail", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 422 || strings.Contains(err.Error(), token) {
		t.Fatalf("typed/sanitized API error: %#v %v", apiErr, err)
	}
	if !sawAuth {
		t.Fatal("authorization header was not sent")
	}
}

func TestGitPushStatusAndNonFastForward(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	worktree := filepath.Join(dir, "work")
	remote := filepath.Join(dir, "remote.git")
	runTestGit(t, "init", "--bare", remote)
	runTestGit(t, "init", worktree)
	runTestGit(t, "-C", worktree, "config", "user.email", "test@example.com")
	runTestGit(t, "-C", worktree, "config", "user.name", "Test")
	runTestGit(t, "-C", worktree, "checkout", "-b", "aic/item-step-1")
	if err := os.WriteFile(filepath.Join(worktree, "file.txt"), []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, "-C", worktree, "add", "file.txt")
	runTestGit(t, "-C", worktree, "commit", "-m", "first commit")
	runTestGit(t, "-C", worktree, "remote", "add", "origin", remote)
	g := NewGit()
	status, err := g.Status(ctx, worktree)
	if err != nil || status.Branch != "aic/item-step-1" || !status.Clean {
		t.Fatalf("status %#v %v", status, err)
	}
	commit, err := g.PushBranch(ctx, worktree, "origin", "aic/item-step-1")
	if err != nil || commit.SHA == "" || commit.Subject != "first commit" {
		t.Fatalf("push %#v %v", commit, err)
	}

	// Point the bare remote's branch at a distinct commit, making the local
	// commit a non-fast-forward update which an ordinary push must reject.
	other := filepath.Join(dir, "other")
	runTestGit(t, "init", other)
	runTestGit(t, "-C", other, "config", "user.email", "test@example.com")
	runTestGit(t, "-C", other, "config", "user.name", "Other")
	if err := os.WriteFile(filepath.Join(other, "other.txt"), []byte("other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, "-C", other, "add", "other.txt")
	runTestGit(t, "-C", other, "commit", "-m", "other")
	var otherSHA string
	otherSHA = runTestGit(t, "-C", other, "rev-parse", "HEAD")
	runTestGit(t, "--git-dir", remote, "fetch", filepath.Join(other, ".git"), otherSHA)
	runTestGit(t, "--git-dir", remote, "update-ref", "refs/heads/aic/item-step-1", otherSHA)
	if _, err := g.PushBranch(ctx, worktree, "origin", "aic/item-step-1"); err == nil {
		t.Fatal("non-fast-forward push unexpectedly succeeded")
	}
	for _, bad := range []string{"aic/-bad-step-1", "aic/item..x-step-1", "aic/item step-1", "-aic/item-step-1", "feature/item-step-1"} {
		if _, err := g.PushBranch(ctx, worktree, "origin", bad); err == nil {
			t.Errorf("accepted invalid branch %q", bad)
		}
	}
}

func runTestGit(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
