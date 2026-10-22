// Package adapters implements the agent.Adapter interface for the two M2
// connector kinds (docs/mvp-design.md §6): a process-isolated CLI adapter and
// a stateless one-shot API adapter.
package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
)

// allowEnvKeys is the fixed environment allowlist for CLI children. Per §10
// and FR-AG-6 the operator environment is never inherited: anything matching
// *KEY*, *TOKEN*, *SECRET* or *PASSWORD* (OPENAI_API_KEY, ANTHROPIC_API_KEY,
// GITHUB_TOKEN, AWS_SECRET_ACCESS_KEY, DATABASE_URL, …) never reaches the
// child, and CLIConfig.Env is for the CLI's own knobs (e.g.
// CLAUDE_CONFIG_DIR), never for platform credentials.
//
// The allowlist is necessary but not sufficient isolation: secrets also live
// in files (~/.netrc, ~/.config/gh/hosts.yml, ~/.aws/credentials) and the
// keychain, so §10's other chokepoints (Seatbelt profile, per-run HOME,
// proxy-held keys) still must be applied around this.
var allowEnvKeys = []string{
	"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TERM",
	"TMPDIR", "USER", "SHELL", "TZ",
}

const (
	scanMaxToken = 1 << 20          // 1 MiB scanner token cap
	errTailMax   = 4 << 10          // 4 KiB tail of stderr carried in error text
	killGrace    = 10 * time.Second // SIGTERM → SIGKILL grace for the group
)

// secretRe matches high-signal credential shapes; see Redact.
var secretRe = regexp.MustCompile(strings.Join([]string{
	`sk-[A-Za-z0-9_-]{16,}`,
	`ghp_[A-Za-z0-9]{20,}`,
	`gho_[A-Za-z0-9]{20,}`,
	`github_pat_[A-Za-z0-9_]{20,}`,
	`AKIA[0-9A-Z]{16}`,
	`xox[baprs]-[A-Za-z0-9-]{10,}`,
	`(?i)(bearer|authorization:)\s+[A-Za-z0-9._~+/-]{12,}`,
}, "|"))

// Redact replaces recognised credential shapes with [REDACTED]. It is a last
// line of defense (§10), not the credential control itself.
func Redact(s string) string { return secretRe.ReplaceAllString(s, "[REDACTED]") }

// classifiedError marks every error Run returns with a failure kind. The
// worker reads the kind through agent.Classified (internal/agent.FailureKind).
type classifiedError struct {
	kind string
	err  error
}

func (e *classifiedError) Error() string       { return e.err.Error() }
func (e *classifiedError) Unwrap() error       { return e.err }
func (e *classifiedError) FailureKind() string { return e.kind }

var (
	_ agent.Classified = (*classifiedError)(nil)
	_ agent.Classified = (*APIError)(nil)
)

// classify wraps err so agent.FailureKind sees kind, preserving the original
// message and unwrap chain.
func classify(kind string, err error) error {
	return &classifiedError{kind: kind, err: err}
}

// cliRetryableStrings are stderr markers that suggest a non-zero exit was an
// environmental hiccup (rate limit / timeout) rather than a hard failure.
var cliRetryableStrings = []string{
	"rate limit", "rate-limit", "rate_limit", "ratelimit",
	"timed out", "timeout",
}

func stderrRetryable(tail string) bool {
	low := strings.ToLower(tail)
	for _, s := range cliRetryableStrings {
		if strings.Contains(low, s) {
			return true
		}
	}
	return false
}

func looksLikeCredential(key string) bool {
	up := strings.ToUpper(key)
	return strings.Contains(up, "KEY") || strings.Contains(up, "TOKEN") ||
		strings.Contains(up, "SECRET") || strings.Contains(up, "PASSWORD")
}

// buildEnv constructs the child environment: allowlist from the process, then
// CLIConfig.Env overlaid on top (credential-shaped keys dropped even when the
// operator configured them explicitly).
func buildEnv(cfg agent.CLIConfig) []string {
	env := make(map[string]string, len(allowEnvKeys)+len(cfg.Env))
	for _, k := range allowEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for k, v := range cfg.Env {
		if looksLikeCredential(k) {
			continue
		}
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// cliProc is what Cancel needs to reach from another goroutine. done is
// closed once the run has fully finished, so Cancel signals over a raw pgid
// syscall and never races with cmd internals: before that the pgid is valid
// for kill(2); after that it is a no-op.
type cliProc struct {
	pgid   int
	cancel context.CancelFunc
	done   chan struct{}
}

// CLI runs agent jobs as child processes (claude-code / codex / aider style).
type CLI struct {
	mu    sync.Mutex
	procs map[string]*cliProc
}

// NewCLI returns a CLI adapter.
func NewCLI() *CLI { return &CLI{procs: map[string]*cliProc{}} }

// Kind implements agent.Adapter.
func (c *CLI) Kind() string { return string(agent.KindCLI) }

// Capabilities implements agent.Adapter. The CLI family is code+shell by
// definition and streams via stream-json output; usage is reported when the
// CLI emits a result line, so Usage is on.
func (c *CLI) Capabilities() agent.Capabilities {
	return agent.Capabilities{Code: true, Shell: true, Streaming: true, Usage: true}
}

// Cancel terminates the process group behind jobID. Unknown job IDs are a
// no-op (idempotent); a finished run is likewise safe to cancel.
func (c *CLI) Cancel(jobID string) error {
	c.mu.Lock()
	p, ok := c.procs[jobID]
	c.mu.Unlock()
	if !ok || p == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil // already reaped
	default:
	}
	_ = syscall.Kill(-p.pgid, syscall.SIGTERM) //nolint:errcheck // best effort; watchdog escalates
	p.cancel()
	return nil
}

func (c *CLI) register(jobID string, p *cliProc) {
	c.mu.Lock()
	c.procs[jobID] = p
	c.mu.Unlock()
}

func (c *CLI) deregister(jobID string) {
	c.mu.Lock()
	delete(c.procs, jobID)
	c.mu.Unlock()
}

// Run executes one job. See agent.Adapter for the contract.
func (c *CLI) Run(ctx context.Context, job agent.Job, sink agent.EventSink) (*agent.Result, error) {
	invalid := func(format string, a ...any) error {
		return classify(agent.FailurePermanent,
			fmt.Errorf("%w: cli adapter: %s", agent.ErrInvalid, fmt.Sprintf(format, a...)))
	}
	var cfg agent.CLIConfig
	if len(job.Config) == 0 {
		return nil, invalid("missing config")
	}
	if err := json.Unmarshal(job.Config, &cfg); err != nil {
		return nil, classify(agent.FailurePermanent,
			fmt.Errorf("%w: cli adapter: bad config: %v", agent.ErrInvalid, err))
	}
	if cfg.Bin == "" {
		return nil, invalid("bin is required")
	}
	if strings.ContainsRune(cfg.Bin, 0) {
		return nil, invalid("bin contains NUL")
	}
	for _, a := range cfg.Args {
		if strings.ContainsRune(a, 0) {
			return nil, invalid("args contain NUL")
		}
	}

	// Workdir: the worker normally provisions one (§6 worktrees, and §10:
	// ".." from an agent cwd must not land in the operator's checkout). When
	// unset the adapter still refuses to run in its own cwd — it makes a
	// throwaway directory instead and removes it afterwards.
	workdir := ""
	var tempDir string
	if job.Workdir != nil && strings.TrimSpace(*job.Workdir) != "" {
		workdir = *job.Workdir
	} else {
		d, err := os.MkdirTemp("", "aic-chat-*")
		if err != nil {
			return nil, classify(agent.FailureTransient,
				fmt.Errorf("cli agent %s: workdir: %w", job.ID, err))
		}
		tempDir = d
		workdir = d
	}
	defer func() {
		if tempDir != "" {
			os.RemoveAll(tempDir) //nolint:errcheck // best effort
		}
	}()

	timeout := 300 * time.Second
	if cfg.TimeoutSec > 0 {
		timeout = time.Duration(cfg.TimeoutSec) * time.Second
	}
	if timeout > 3600*time.Second {
		timeout = 3600 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append(append([]string{}, cfg.Args...), job.Prompt) // prompt is always the final argument
	cmd := exec.CommandContext(runCtx, cfg.Bin, args...)
	cmd.Dir = workdir
	cmd.Env = buildEnv(cfg)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own process group; kill goes to -pgid

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, classify(agent.FailurePermanent,
			fmt.Errorf("cli agent %s: stdout pipe: %w", job.ID, err))
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, classify(agent.FailurePermanent,
			fmt.Errorf("cli agent %s: stderr pipe: %w", job.ID, err))
	}

	if err := cmd.Start(); err != nil {
		return nil, classify(agent.FailurePermanent,
			fmt.Errorf("cli agent %s: start %s: %w", job.ID, cfg.Bin, err))
	}

	proc := &cliProc{pgid: cmd.Process.Pid, cancel: cancel, done: make(chan struct{})}
	c.register(job.ID, proc)
	defer c.deregister(job.ID)

	st := &cliRun{cfg: cfg, sink: sink}

	var wg sync.WaitGroup
	wg.Add(2)
	scanErrs := make(chan error, 2)
	go func() {
		defer wg.Done()
		scanErrs <- st.scan(stdout, true)
	}()
	go func() {
		defer wg.Done()
		scanErrs <- st.scan(stderr, false)
	}()

	// Kill the whole group on cancellation/timeout: SIGTERM to -pgid, then
	// SIGKILL after the grace period. The watchdog is the reliable killer;
	// graceDone stops it once cmd.Wait has returned.
	graceDone := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			_ = syscall.Kill(-proc.pgid, syscall.SIGTERM) //nolint:errcheck // best effort
			t := time.NewTimer(killGrace)
			defer t.Stop()
			select {
			case <-t.C:
				_ = syscall.Kill(-proc.pgid, syscall.SIGKILL) //nolint:errcheck // best effort
			case <-graceDone:
			}
		case <-graceDone:
		}
	}()

	// Same group kill on exec's own fast path, plus a WaitDelay backstop.
	cmd.Cancel = func() error { return syscall.Kill(-proc.pgid, syscall.SIGTERM) }
	cmd.WaitDelay = killGrace

	waitErr := cmd.Wait()
	wg.Wait()
	graceDone <- struct{}{}
	close(proc.done)

	if err := runCtx.Err(); err != nil {
		// Cancellation and timeout both come back as ctx errors; the worker
		// classifies them (transient) — FailureKind handles ctx errors too.
		return nil, classify(agent.FailureTransient, fmt.Errorf("cli agent %s: %w", job.ID, err))
	}
	// Both scan goroutines are done; one receive per goroutine, buffered.
	scanFail := <-scanErrs
	if scanFail == nil {
		scanFail = <-scanErrs
	}
	if waitErr != nil {
		tail := strings.TrimSpace(redactTail(st.stderrTail.s))
		// *exec.ExitError renders as "exit status N" / "signal: killed".
		msg := waitErr.Error()
		if tail != "" {
			msg = msg + ": " + tail
		}
		st.emit(agent.Event{Stream: "engine", Level: "error", Kind: "error", Message: msg})
		// A retryable-looking stderr (rate limit / timeout) is transient;
		// everything else is a hard failure.
		kind := agent.FailurePermanent
		if stderrRetryable(st.stderrTail.s) {
			kind = agent.FailureTransient
		}
		return nil, classify(kind, errors.New(msg)) // error text = redacted stderr tail; never the prompt
	}
	if scanFail != nil {
		// An oversized or unreadable stream: surface it, keep the result.
		st.emit(agent.Event{Stream: "engine", Level: "error", Kind: "error",
			Message: "output scan: " + scanFail.Error()})
	}

	out := st.resultText
	if out == "" {
		out = strings.Join(st.chunks, "")
	}
	return &agent.Result{Output: out, TokensIn: st.tokensIn, TokensOut: st.tokensOut, CostUSD: st.cost}, nil
}

// cliRun accumulates everything one CLI job produced.
type cliRun struct {
	cfg  agent.CLIConfig
	sink agent.EventSink

	mu         sync.Mutex
	chunks     []string
	resultText string
	tokensIn   int
	tokensOut  int
	cost       *float64
	stderrTail errTail
}

func (st *cliRun) emit(e agent.Event) {
	e.Message = Redact(e.Message)
	st.mu.Lock()
	st.sink.Emit(e)
	st.mu.Unlock()
}

// scan drains one pipe. stdout=true handles model events (the stream-json
// shapes Claude Code emits); stdout=false accumulates the stderr tail and
// forwards lines as warn-level events.
func (st *cliRun) scan(r io.Reader, stdout bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), scanMaxToken)
	for sc.Scan() {
		if stdout {
			st.stdoutLine(sc.Text())
		} else {
			st.stderrLine(sc.Text())
		}
	}
	return sc.Err()
}

func (st *cliRun) stderrLine(line string) {
	st.mu.Lock()
	st.stderrTail.appendLine(line)
	st.mu.Unlock()
	st.emit(agent.Event{Stream: "stderr", Level: "warn", Kind: "output_chunk", Message: line})
}

func (st *cliRun) stdoutLine(line string) {
	if st.cfg.OutputFormat != "stream-json" {
		st.emit(agent.Event{Stream: "stdout", Level: "info", Kind: "output_chunk", Message: line})
		st.mu.Lock()
		st.chunks = append(st.chunks, line, "\n")
		st.mu.Unlock()
		return
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(line), &probe); err != nil || probe.Type == "" {
		// Malformed or non-JSON line: stream it through, keep the run alive.
		st.emit(agent.Event{Stream: "stdout", Level: "info", Kind: "output_chunk", Message: line})
		return
	}
	st.dispatchJSON(probe.Type, line)
}

func (st *cliRun) dispatchJSON(eventType, line string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	switch eventType {
	case "system":
		var sys struct {
			Subtype string `json:"subtype"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal([]byte(line), &sys) //nolint:errcheck // type already validated
		st.emitLocked(agent.Event{Stream: "engine", Level: "info", Kind: "lifecycle",
			Message: firstNonEmpty(sys.Message, "system "+sys.Subtype)})
	case "assistant":
		var msg struct {
			Message struct {
				Content []ccBlock `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			st.emitLocked(agent.Event{Stream: "stdout", Level: "info", Kind: "output_chunk", Message: line})
			return
		}
		for _, b := range msg.Message.Content {
			switch b.Type {
			case "text":
				st.emitLocked(agent.Event{Stream: "stdout", Level: "info", Kind: "output_chunk", Message: b.Text})
			case "tool_use":
				data, _ := json.Marshal(struct {
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				}{Name: b.Name, Input: b.Input})
				st.emitLocked(agent.Event{Stream: "engine", Level: "info", Kind: "tool_call",
					Message: b.Name, Data: data})
			}
		}
	case "user":
		var msg struct {
			Message struct {
				Content []ccBlock `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			st.emitLocked(agent.Event{Stream: "stdout", Level: "info", Kind: "output_chunk", Message: line})
			return
		}
		for _, b := range msg.Message.Content {
			if b.Type == "tool_result" {
				st.emitLocked(agent.Event{Stream: "engine", Level: "info", Kind: "tool_result", Data: b.Content})
			}
		}
	case "result":
		var res struct {
			Subtype string `json:"subtype"`
			Result  string `json:"result"`
			Usage   struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			TotalCostUSD *float64 `json:"total_cost_usd"`
		}
		if err := json.Unmarshal([]byte(line), &res); err != nil {
			st.emitLocked(agent.Event{Stream: "stdout", Level: "info", Kind: "output_chunk", Message: line})
			return
		}
		st.resultText = res.Result
		st.tokensIn = res.Usage.InputTokens
		st.tokensOut = res.Usage.OutputTokens
		st.cost = res.TotalCostUSD
		usage, _ := json.Marshal(agent.Result{
			TokensIn: res.Usage.InputTokens, TokensOut: res.Usage.OutputTokens, CostUSD: res.TotalCostUSD,
		})
		st.emitLocked(agent.Event{Stream: "engine", Level: "info", Kind: "usage", Data: usage})
		level := "info"
		if res.Subtype != "success" {
			level = "warn"
		}
		st.emitLocked(agent.Event{Stream: "engine", Level: level, Kind: "lifecycle",
			Message: "result " + res.Subtype})
	default:
		// Unknown event type from a newer CLI: stream raw, don't fail.
		st.emitLocked(agent.Event{Stream: "stdout", Level: "info", Kind: "output_chunk", Message: line})
	}
}

// emitLocked is emit, for when st.mu is already held. It releases the lock
// while the (potentially slow) sink call runs.
func (st *cliRun) emitLocked(e agent.Event) {
	st.mu.Unlock()
	defer st.mu.Lock()
	st.emit(e)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ccBlock is one content block of a Claude Code assistant/user message.
type ccBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"` // tool_result payload
}

// errTail keeps a bounded trailing slice of a stream for error reporting.
type errTail struct{ s string }

func (t *errTail) appendLine(line string) {
	t.s += line + "\n"
	if len(t.s) > errTailMax {
		t.s = t.s[len(t.s)-errTailMax:]
	}
}

// redactTail returns redacted, NUL-stripped tail text for error messages.
func redactTail(s string) string { return strings.ReplaceAll(Redact(s), "\x00", "") }
