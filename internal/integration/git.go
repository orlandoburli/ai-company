package integration

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var validBranch = regexp.MustCompile(`^aic/[A-Za-z0-9][A-Za-z0-9_-]*-[A-Za-z0-9][A-Za-z0-9_-]*-[0-9]+$`)

type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type Status struct {
	Branch  string   `json:"branch"`
	Clean   bool     `json:"clean"`
	Changes []string `json:"changes"`
}

// CredentialResolver returns the secret a push authenticates with. The
// secret is resolved at the moment of the call and never reaches a command
// line: it is written to a file with owner-only permissions and read back by
// git's askpass helper.
type CredentialResolver func(ctx context.Context) (string, error)

// Git is the server-side git. It runs the locked-down configuration the design
// requires (no system config, no global config, no hooks, no ssh command, an
// empty HOME) — the app acts for the operator through the keychain, never
// through the operator's files.
type Git struct {
	credential CredentialResolver
}

func NewGit() *Git { return &Git{} }

// WithCredential gives the git service the credential it authenticates a push
// with.
func (g *Git) WithCredential(fn CredentialResolver) *Git {
	g.credential = fn
	return g
}

// CommitAll stages and commits everything in the worktree. The agent never
// commits: the server commits the work, on the branch the server created.
func (g *Git) CommitAll(ctx context.Context, workdir, message string) (Commit, error) {
	if strings.TrimSpace(workdir) == "" {
		return Commit{}, errors.New("git worktree directory is required")
	}
	if strings.TrimSpace(message) == "" {
		message = "AI Company: work from the run"
	}
	if err := runGit(ctx, workdir, nil, "add", "--all"); err != nil {
		return Commit{}, err
	}
	// An empty tree is not an error: there may be nothing to commit.
	if err := runGit(ctx, workdir, nil, "commit", "--no-verify", "-m", message); err != nil {
		var out string
		if err := runGit(ctx, workdir, &out, "status", "--porcelain"); err != nil {
			return Commit{}, err
		}
		if strings.TrimSpace(out) == "" {
			return g.HeadCommit(ctx, workdir)
		}
		return Commit{}, err
	}
	return g.HeadCommit(ctx, workdir)
}

func (g *Git) HeadCommit(ctx context.Context, workdir string) (Commit, error) {
	var out string
	if err := runGit(ctx, workdir, &out, "log", "-1", "--format=%H%x00%s"); err != nil {
		return Commit{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(out), "\x00", 2)
	if len(parts) != 2 || parts[0] == "" {
		return Commit{}, errors.New("git HEAD is unavailable")
	}
	return Commit{SHA: parts[0], Subject: strings.TrimSpace(parts[1])}, nil
}

func (g *Git) PushBranch(ctx context.Context, workdir, remote, branch string) (Commit, error) {
	if !validBranch.MatchString(branch) || strings.Contains(branch, "..") || strings.HasPrefix(branch, "-") {
		return Commit{}, errors.New("invalid integration branch name")
	}
	if strings.TrimSpace(remote) == "" || strings.HasPrefix(remote, "-") {
		return Commit{}, errors.New("invalid git remote")
	}
	commit, err := g.HeadCommit(ctx, workdir)
	if err != nil {
		return Commit{}, err
	}
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	if err := runGitWithCredential(ctx, workdir, nil, g.credential, "push", "--porcelain", remote, refspec); err != nil {
		return Commit{}, err
	}
	return commit, nil
}

func (g *Git) Status(ctx context.Context, workdir string) (Status, error) {
	var out string
	if err := runGit(ctx, workdir, &out, "status", "--porcelain=v1", "--branch"); err != nil {
		return Status{}, err
	}
	s := Status{Clean: true}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") {
			s.Branch = strings.TrimPrefix(strings.SplitN(line, "...", 2)[0], "## ")
			continue
		}
		if line != "" {
			s.Clean = false
			s.Changes = append(s.Changes, strings.TrimSpace(line[2:]))
		}
	}
	return s, scanner.Err()
}

// runGitWithCredential runs git with a credential available to git's askpass
// helper. The secret is written to a file with owner-only permissions and
// removed when the command finishes; it never appears in the arguments, the
// environment or an error message.
func runGitWithCredential(ctx context.Context, dir string, stdout *string, credential CredentialResolver, args ...string) error {
	if credential == nil {
		return runGit(ctx, dir, stdout, args...)
	}
	secret, err := credential(ctx)
	if err != nil {
		return errors.New("git credential is unavailable")
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("git credential is empty")
	}
	dir2, err := os.MkdirTemp("", "aic-git-cred-*")
	if err != nil {
		return errors.New("git credential directory could not be created")
	}
	defer os.RemoveAll(dir2) //nolint:errcheck // the secret dies with the directory

	tokenFile := dir2 + "/token"
	if err := os.WriteFile(tokenFile, []byte(secret), 0o600); err != nil {
		return errors.New("git credential could not be written")
	}
	askpass := dir2 + "/askpass.sh"
	script := "#!/bin/sh\ncat " + tokenFile + "\n"
	if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
		return errors.New("git askpass helper could not be written")
	}
	return runGitEnv(ctx, dir, stdout, askpass, args...)
}

func runGit(ctx context.Context, dir string, stdout *string, args ...string) error {
	return runGitEnv(ctx, dir, stdout, "", args...)
}

func runGitEnv(ctx context.Context, dir string, stdout *string, askpass string, args ...string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("git worktree directory is required")
	}
	// Disable config/hook/credential inheritance. Git is invoked directly with
	// argv (never through a shell) and receives only basic platform variables.
	allArgs := append([]string{
		"-c", "credential.helper=",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "gc.auto=0",
		"-c", "protocol.ext.allow=never",
		"-c", "core.sshCommand=/usr/bin/false",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", allArgs...)
	cmd.Dir = dir
	home, err := os.MkdirTemp("", "aic-git-home-*")
	if err != nil {
		return errors.New("git temporary home could not be created")
	}
	defer os.RemoveAll(home) //nolint:errcheck // per-command empty HOME
	cmd.Env = gitEnvironment(home)
	if askpass != "" {
		for i, entry := range cmd.Env {
			if strings.HasPrefix(entry, "GIT_ASKPASS=") {
				cmd.Env[i] = "GIT_ASKPASS=" + askpass
			}
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output strings.Builder
	if stdout != nil {
		cmd.Stdout = &output
	} else {
		cmd.Stdout = io.Discard
	}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 10 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	err = cmd.Run()
	if cmd.Process != nil && ctx.Err() != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("git operation canceled")
		}
		return errors.New("git operation failed")
	}
	if stdout != nil {
		*stdout = output.String()
	}
	return nil
}

func gitEnvironment(home string) []string {
	env := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"HOME=" + home,
	}
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "USER"} {
		if key == "HOME" {
			continue
		}
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
