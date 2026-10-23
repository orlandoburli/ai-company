// Package policy provides best-effort command shims for CLI agents.
//
// These shims are accident prevention and telemetry, not a security boundary:
// agents can alter their environment or invoke binaries by absolute path. The
// actual filesystem and network bounds must come from the process sandbox.
package policy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config is the per-agent command and filesystem policy in config_json.
type Config struct {
	AllowedRoots   []string `json:"allowed_roots,omitempty"`
	DeniedCommands []string `json:"denied_commands,omitempty"`
	AllowNetwork   bool     `json:"allow_network,omitempty"`
}

// Shim describes the policy directory to put in front of the agent's PATH.
type Shim struct {
	Dir    string
	Log    func(format string, args ...any)
	Denied []string

	logFile string
}

// New writes git and rm wrappers into dir. An empty dir creates a temporary
// directory. Denied rules are data, so each agent can extend the rm denylist.
func New(dir string, log func(string, ...any), denied []string) (*Shim, error) {
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "aic-policy-")
		if err != nil {
			return nil, fmt.Errorf("create policy directory: %w", err)
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create policy directory: %w", err)
	}
	cleanOnError := true
	defer func() {
		if cleanOnError {
			_ = os.RemoveAll(dir)
		}
	}()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("find real git: %w", err)
	}
	rmBin, err := exec.LookPath("rm")
	if err != nil {
		return nil, fmt.Errorf("find real rm: %w", err)
	}
	logPath := filepath.Join(dir, "refusals.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create policy telemetry file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close policy telemetry file: %w", err)
	}

	gitScript := gitWrapper(shellQuote(gitBin), shellQuote(logPath))
	rmScript := rmWrapper(shellQuote(rmBin), shellQuote(logPath), denied)
	for name, body := range map[string]string{"git": gitScript, "rm": rmScript} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o555); err != nil {
			return nil, fmt.Errorf("write %s policy wrapper: %w", name, err)
		}
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, fmt.Errorf("set policy directory mode: %w", err)
	}
	cleanOnError = false
	return &Shim{Dir: dir, Log: log, Denied: append([]string(nil), denied...), logFile: logPath}, nil
}

// Path returns a PATH value with this shim directory first.
func (s *Shim) Path() string {
	return s.PathWith(os.Getenv("PATH"))
}

// PathWith prepends the shim to a caller-provided PATH value.
func (s *Shim) PathWith(path string) string {
	if path == "" {
		return s.Dir
	}
	return s.Dir + string(os.PathListSeparator) + path
}

// Close reports refusals to Log and removes the shim directory.
func (s *Shim) Close() error {
	if s == nil {
		return nil
	}
	data, readErr := os.ReadFile(s.logFile)
	if s.Log != nil && len(data) > 0 {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				s.Log("%s", line)
			}
		}
	}
	removeErr := os.RemoveAll(s.Dir)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	return removeErr
}

func gitWrapper(realGit, logFile string) string {
	return `#!/bin/sh
deny() {
  msg="policy: refused git $1; the server commits and pushes; the agent does not"
  printf '%s\n' "$msg" >&2
  printf '%s\n' "$msg" >> ` + logFile + `
  exit 126
}
prev=
for arg do
  case "$arg" in
    push|update-ref|filter-branch|worktree) deny "$arg" ;;
    add|set-url) if [ "$prev" = remote ]; then deny "remote $arg"; fi ;;
    --prune|--prune=*) if [ "$prev" = gc ]; then deny "gc $arg"; fi ;;
    expire) if [ "$prev" = reflog ]; then deny "reflog expire"; fi ;;
    core.*) if [ "$prev" = -c ]; then deny "-c $arg"; fi ;;
  esac
  prev=$arg
done
exec ` + realGit + ` "$@"
`
}

func rmWrapper(realRM, logFile string, denied []string) string {
	var configured strings.Builder
	for _, rule := range denied {
		fields := strings.Fields(rule)
		if len(fields) < 2 || filepath.Base(fields[0]) != "rm" {
			continue
		}
		fmt.Fprintf(&configured, "  %s) deny %s ;;\n", shellQuote(strings.Join(fields[1:], " ")), shellQuote(rule))
	}
	return `#!/bin/sh
deny() {
  msg="policy: refused $1"
  printf '%s\n' "$msg" >&2
  printf '%s\n' "$msg" >> ` + logFile + `
  exit 126
}
case "$*" in
` + configured.String() + `  *) ;;
esac
recursive=0
force=0
root=0
for arg do
  case "$arg" in
    -rf|-fr) recursive=1; force=1 ;;
    -r|-R|--recursive) recursive=1 ;;
    -f|--force) force=1 ;;
  esac
  if [ "$arg" = / ] || [ "$arg" = '/*' ] || [ "$arg" = '~' ] ||
     [ "$arg" = "$HOME" ] || [ "$arg" = "$AIC_POLICY_WORK_ROOT" ] ||
     [ "$arg" = . ] || [ "$arg" = ./ ]; then
    root=1
  fi
done
if [ "$recursive" = 1 ] && [ "$force" = 1 ] && [ "$root" = 1 ]; then
  deny "rm -rf of a filesystem root, home, or the agent working root; the server owns the workspace lifecycle"
fi
exec ` + realRM + ` "$@"
`
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
