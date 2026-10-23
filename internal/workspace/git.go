package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const stderrLimit = 2 << 10

// GitError describes a failed git command without including the process
// environment. Stderr is bounded to the last 2 KiB.
type GitError struct {
	Command  []string
	ExitCode int
	Stderr   string
	Stdout   string
	Cause    error
}

func (e *GitError) Error() string {
	command := make([]string, len(e.Command))
	for i, arg := range e.Command {
		command[i] = strconv.Quote(arg)
	}
	msg := "workspace: git " + strings.Join(command, " ")
	if e.ExitCode >= 0 {
		msg += fmt.Sprintf(" (exit %d)", e.ExitCode)
	}
	if e.Stderr != "" {
		msg += ": " + strings.TrimSpace(e.Stderr)
	}
	if e.Stdout != "" {
		if e.Stderr != "" {
			msg += "\n"
		} else {
			msg += ": "
		}
		msg += strings.TrimSpace(e.Stdout)
	}
	if e.Cause != nil && e.Stderr == "" {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap returns the underlying process error, if any.
func (e *GitError) Unwrap() error { return e.Cause }

func (p *Provider) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.gitBin, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &tailBuffer{limit: stderrLimit, buffer: &stderr}
	if err := cmd.Run(); err != nil {
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		full := append([]string{p.gitBin}, args...)
		stdoutText := stdout.String()
		if len(stdoutText) > stderrLimit {
			stdoutText = stdoutText[len(stdoutText)-stderrLimit:]
		}
		return nil, &GitError{Command: full, ExitCode: code, Stderr: stderr.String(), Stdout: stdoutText, Cause: err}
	}
	return stdout.Bytes(), nil
}

type tailBuffer struct {
	limit  int
	buffer *bytes.Buffer
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	if len(data) >= b.limit {
		b.buffer.Reset()
		_, _ = b.buffer.Write(data[len(data)-b.limit:])
		return len(data), nil
	}
	if overflow := b.buffer.Len() + len(data) - b.limit; overflow > 0 {
		current := b.buffer.Bytes()
		remaining := append([]byte(nil), current[overflow:]...)
		b.buffer.Reset()
		_, _ = b.buffer.Write(remaining)
	}
	_, _ = b.buffer.Write(data)
	return len(data), nil
}

type keyedLock struct{ ch chan struct{} }

func (p *Provider) lock(key string) func() {
	p.mu.Lock()
	lock := p.locks[key]
	if lock == nil {
		lock = &keyedLock{ch: make(chan struct{}, 1)}
		lock.ch <- struct{}{}
		p.locks[key] = lock
	}
	p.mu.Unlock()
	<-lock.ch
	return func() { lock.ch <- struct{}{} }
}

func (p *Provider) recordBranch(mirror, branch string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.created[mirror] == nil {
		p.created[mirror] = map[string]struct{}{}
	}
	p.created[mirror][branch] = struct{}{}
}

func (p *Provider) forgetBranch(mirror, branch string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.created[mirror], branch)
}
