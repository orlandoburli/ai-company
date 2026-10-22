package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
)

// collector records everything a sink receives.
type collector struct {
	mu     sync.Mutex
	events []agent.Event
}

func (c *collector) sink() agent.EventSink { return c }

func (c *collector) Emit(e agent.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *collector) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.events))
	for i, e := range c.events {
		out[i] = e.Kind
	}
	return out
}

func (c *collector) find(kind string) (agent.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Kind == kind {
			return e, true
		}
	}
	return agent.Event{}, false
}

// script writes an executable shell script and returns its path.
func script(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func cliJob(id, bin string, mutate func(*agent.CLIConfig)) agent.Job {
	cfg := agent.CLIConfig{Bin: bin}
	if mutate != nil {
		mutate(&cfg)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return agent.Job{ID: id, Config: raw, Prompt: "the prompt"}
}
func TestCLIStreamJSONResult(t *testing.T) {
	bin := script(t, "claude", `
echo '{"type":"system","subtype":"init","message":"initializing"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"bash","input":{"cmd":"ls"}}]}}'
echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"cat output"}]}}'
echo '{"type":"result","subtype":"success","result":"final answer","usage":{"input_tokens":11,"output_tokens":22},"total_cost_usd":0.42}'
`)
	var c collector
	res, err := NewCLI().Run(context.Background(), cliJob("j1", bin, func(cfg *agent.CLIConfig) {
		cfg.OutputFormat = "stream-json"
	}), c.sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Output != "final answer" {
		t.Errorf("Output = %q, want final answer", res.Output)
	}
	if res.TokensIn != 11 || res.TokensOut != 22 {
		t.Errorf("tokens = %d/%d, want 11/22", res.TokensIn, res.TokensOut)
	}
	if res.CostUSD == nil || *res.CostUSD != 0.42 {
		t.Errorf("CostUSD = %v, want 0.42", res.CostUSD)
	}
	for _, want := range []string{"output_chunk", "tool_call", "tool_result", "usage", "lifecycle"} {
		if _, ok := c.find(want); !ok {
			t.Errorf("missing %q event; kinds = %v", want, c.kinds())
		}
	}
	if e, ok := c.find("tool_call"); !ok || e.Message != "bash" {
		t.Errorf("tool_call = %+v ok=%v, want name bash", e, ok)
	}
}

func TestCLINonZeroExitStderrTail(t *testing.T) {
	bin := script(t, "boom", `echo boom >&2; exit 3`)
	var c collector
	res, err := NewCLI().Run(context.Background(), cliJob("j2", bin, nil), c.sink())
	if err == nil {
		t.Fatalf("want error, got result %+v", res)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q lacks stderr tail", err)
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("error %q lacks exit status", err)
	}
	if strings.Contains(err.Error(), "the prompt") {
		t.Errorf("error %q must not contain the prompt", err)
	}
	if got := agent.FailureKind(err); got != agent.FailurePermanent {
		t.Errorf("agent.FailureKind = %q, want permanent", got)
	}
	assertClassified(t, err)
}

// A non-zero exit whose stderr looks like a rate limit or timeout classifies
// as transient for the worker's retry policy.
func TestCLIRetryableStderrIsTransient(t *testing.T) {
	bin := script(t, "ratelimit", `echo 'fatal: rate limit exceeded, retry later' >&2; exit 1`)
	_, err := NewCLI().Run(context.Background(), cliJob("j14", bin, nil), (&collector{}).sink())
	if err == nil {
		t.Fatal("want error")
	}
	if got := agent.FailureKind(err); got != agent.FailureTransient {
		t.Errorf("agent.FailureKind = %q, want transient", got)
	}
	assertClassified(t, err)
}

// Every Run error must carry a agent.Classified implementation in its unwrap
// chain; the worker relies on it.
func assertClassified(t *testing.T, err error) {
	t.Helper()
	var c agent.Classified
	if !errors.As(err, &c) {
		t.Errorf("error %v does not implement agent.Classified", err)
		return
	}
	if c.FailureKind() == "" {
		t.Errorf("agent.Classified kind is empty")
	}
}

func TestCLICancelKillsGroup(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel.txt")
	bin := script(t, "sleeper", fmt.Sprintf(`
sleep 30
echo done > %s
`, shQuote(sentinel)))

	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		res *agent.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		r, e := NewCLI().Run(ctx, cliJob("j3", bin, nil), (&collector{}).sink())
		done <- outcome{r, e}
	}()

	time.Sleep(300 * time.Millisecond) // let the sleep start
	cancel()

	select {
	case o := <-done:
		if o.err == nil {
			t.Fatalf("want error, got %+v", o.res)
		}
		if !errors.Is(o.err, context.Canceled) {
			t.Errorf("error %v does not wrap context.Canceled", o.err)
		}
		if agent.FailureKind(o.err) != agent.FailureTransient {
			t.Errorf("agent.FailureKind = %q, want transient", agent.FailureKind(o.err))
		}
		assertClassified(t, o.err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	// Give the killed group a moment; the sentinel must never appear.
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("child survived cancellation: sentinel exists")
	}
}

func TestCLITimeout(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel.txt")
	bin := script(t, "slow", fmt.Sprintf(`
sleep 30
echo done > %s
`, shQuote(sentinel)))

	cfgJob := cliJob("j4", bin, func(cfg *agent.CLIConfig) { cfg.TimeoutSec = 1 })
	res, err := NewCLI().Run(context.Background(), cfgJob, (&collector{}).sink())
	if err == nil {
		t.Fatalf("want error, got %+v", res)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not wrap DeadlineExceeded", err)
	}
	if agent.FailureKind(err) != agent.FailureTransient {
		t.Errorf("agent.FailureKind = %q, want transient", agent.FailureKind(err))
	}
	assertClassified(t, err)
	if _, serr := os.Stat(sentinel); !os.IsNotExist(serr) {
		t.Errorf("sentinel written despite timeout")
	}
}

func TestCLIEnvIsolation(t *testing.T) {
	bin := script(t, "dumpenv", `env`)
	t.Setenv("OPENAI_API_KEY", "sk-parentsecret0000000000")
	t.Setenv("GITHUB_TOKEN", "ghp_parenttokenaaaaaaaaaaaaaaa")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "awssecretvalue")
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("TERM", "dumb")

	var c collector
	res, err := NewCLI().Run(context.Background(), cliJob("j5", bin, func(cfg *agent.CLIConfig) {
		cfg.Env = map[string]string{
			"ANTHROPIC_API_KEY": "sk-anthropicsecret",
			"CLAUDE_CONFIG_DIR": "/tmp/claude-cfg",
		}
	}), c.sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := res.Output
	for _, forbidden := range []string{"OPENAI_API_KEY", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY",
		"DATABASE_URL", "ANTHROPIC_API_KEY"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("env leak: output contains %q", forbidden)
		}
	}
	for _, want := range []string{"PATH=", "HOME=", "TERM=dumb"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if !strings.Contains(out, "CLAUDE_CONFIG_DIR=/tmp/claude-cfg") {
		t.Errorf("config.Env CLI knob dropped")
	}
	// Values must be gone too, not just names.
	if strings.Contains(out, "sk-parentsecret") || strings.Contains(out, "ghp_") {
		t.Errorf("secret value leaked into child env")
	}
}

func TestCLIWorkdir(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	bin := script(t, "whereami", `pwd`)
	wd := dir
	job := cliJob("j6", bin, nil)
	job.Workdir = &wd
	res, err := NewCLI().Run(context.Background(), job, (&collector{}).sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(res.Output); got != real {
		t.Errorf("pwd = %q, want %q", got, real)
	}
}

func TestCLITextModeOutput(t *testing.T) {
	bin := script(t, "echoer", `
echo line one
echo line two
echo line three
`)
	var c collector
	res, err := NewCLI().Run(context.Background(), cliJob("j7", bin, nil), c.sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "line one\nline two\nline three\n"
	if res.Output != want {
		t.Errorf("Output = %q, want %q", res.Output, want)
	}
	if res.TokensIn != 0 || res.TokensOut != 0 || res.CostUSD != nil {
		t.Errorf("usage should be empty in text mode, got %+v", res)
	}
	if _, ok := c.find("usage"); ok {
		t.Errorf("unexpected usage event in text mode")
	}
}

func TestCLIDefaultWorkdirAndCleanup(t *testing.T) {
	bin := script(t, "whereami", `pwd`)
	res, err := NewCLI().Run(context.Background(), cliJob("j8", bin, nil), (&collector{}).sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(res.Output)
	if !strings.Contains(got, "aic-chat-") {
		t.Errorf("pwd = %q, want the aic-chat temp dir", got)
	}
	if _, err := os.Stat(got); err == nil {
		t.Errorf("temp workdir %q was not removed", got)
	}
}

func TestCLIInvalidConfig(t *testing.T) {
	c := NewCLI()
	job := agent.Job{ID: "j9", Config: nil, Prompt: "p"}
	if _, err := c.Run(context.Background(), job, (&collector{}).sink()); err == nil {
		t.Error("nil config should error")
	} else {
		assertClassified(t, err)
		if agent.FailureKind(err) != agent.FailurePermanent {
			t.Errorf("invalid config → agent.FailureKind = %q, want permanent", agent.FailureKind(err))
		}
		if !errors.Is(err, agent.ErrInvalid) {
			t.Errorf("invalid config error must wrap agent.ErrInvalid")
		}
	}
	job = cliJob("j10", "", nil) // empty bin
	if _, err := c.Run(context.Background(), job, (&collector{}).sink()); err == nil {
		t.Error("empty bin should error")
	}
	job = cliJob("j11", "bin-with\x00-nul", nil)
	if _, err := c.Run(context.Background(), job, (&collector{}).sink()); err == nil {
		t.Error("NUL bin should error")
	}
}

func TestCLICancelUnknownIsNoop(t *testing.T) {
	if err := NewCLI().Cancel("no-such-job"); err != nil {
		t.Errorf("Cancel(unknown) = %v, want nil", err)
	}
}

func TestCLIPromptIsFinalArg(t *testing.T) {
	bin := script(t, "argdump", `printf '%s\n' "$@"`)
	cfgJob := cliJob("j12", bin, func(cfg *agent.CLIConfig) {
		cfg.Args = []string{"-p", "--verbose"}
	})
	res, err := NewCLI().Run(context.Background(), cfgJob, (&collector{}).sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "-p\n--verbose\nthe prompt\n"
	if res.Output != want {
		t.Errorf("argv = %q, want %q", res.Output, want)
	}
}

func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"key is sk-proj-abcdef1234567890abcdef", "key is [REDACTED]"},
		{"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ12 rest", "[REDACTED] rest"},
		{"gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ12 rest", "[REDACTED] rest"},
		{"github_pat_ABCDEFGHIJKLM NOP", "github_pat_ABCDEFGHIJKLM NOP"}, // too short, kept
		{"github_pat_ABCDEFGHIJKLMNOPQRSTUVwx rest", "[REDACTED] rest"},
		{"AWS AKIA1234567890ABCDEF ok", "AWS [REDACTED] ok"},
		{"xoxb-abcdefghijklmn rest", "[REDACTED] rest"},
		{"curl -H 'Authorization: Bearer abcdef1234567890'", "curl -H 'Authorization: [REDACTED]'"},
		{"Bearer abcdef1234567890abcdef", "[REDACTED]"},
		{"authorization: abcdef1234567890abcdef", "[REDACTED]"},
		{"nothing secret here", "nothing secret here"},
		{"sk-short", "sk-short"},
		{"AKIAIOSFODNN7EXAMPLE", "[REDACTED]"},
	}
	for _, tc := range cases {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Redaction flows through emitted events end to end.
func TestCLIEventMessagesAreRedacted(t *testing.T) {
	bin := script(t, "leaky", `echo 'sending Bearer abcdefghijklmnopqrst' >&2; exit 1`)
	var c collector
	_, err := NewCLI().Run(context.Background(), cliJob("j13", bin, nil), c.sink())
	if err == nil {
		t.Fatal("want error")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	sawWarn := false
	for _, e := range c.events {
		if e.Level == "warn" && e.Stream == "stderr" {
			sawWarn = true
		}
		if strings.Contains(e.Message, "abcdefghijklmnopqrst") {
			t.Errorf("event leaked token: %+v", e)
		}
	}
	if !sawWarn {
		t.Errorf("no stderr warn event received; kinds=%v", c.kinds())
	}
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
