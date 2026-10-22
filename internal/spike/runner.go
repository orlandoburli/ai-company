package spike

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Result is the recorded outcome of one probe.
type Result struct {
	Name     string
	Args     []string
	ExitCode int
	Stdout   string
	Stderr   string
	Err      error
	Want     string // human-readable expectation, for the log
}

// OK reports whether the probe matched its expectation.
func (r Result) OK(match func(Result) bool) bool { return match(r) }

func (r Result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-46s exit=%-4d", r.Name, r.ExitCode)
	if r.ExitCode != 0 {
		fmt.Fprintf(&b, " %s", strings.TrimSpace(r.Stderr))
	}
	return b.String()
}

// SandboxExec runs a command under sandbox-exec with the given profile.
// The command is described as argv (not a shell string) so tests stay honest
// about what is actually permitted.
func SandboxExec(ctx context.Context, profilePath string, argv []string, env []string, cwd string) Result {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	args := append([]string{"-f", profilePath}, argv...)
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", args...)
	cmd.Dir = cwd
	if env != nil {
		cmd.Env = env // nil => inherit (used only for control experiments)
	}
	// Own process group so a hung probe can be killed as a tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb

	err := cmd.Run()
	res := Result{Args: argv, Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
	} else if err != nil {
		res.Err = err
	}
	return res
}

// WriteProfile renders the profile to disk, returning its path.
func WriteProfile(dir string, p Paths) (string, error) {
	path := dir + "/profile.sb"
	if err := os.WriteFile(path, []byte(p.Profile()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
