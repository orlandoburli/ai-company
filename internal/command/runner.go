// Package command implements the administrator-configured command notification channel.
package command

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type Event struct {
	Kind                             string // checkpoint_pending | run_failed | run_completed
	RunID, ItemID, Title, Body, Link string
}

type Channel struct {
	ID              string   `json:"id"`
	TenantID        string   `json:"tenant_id"`
	Name            string   `json:"name"`
	CommandTemplate string   `json:"command_template"`
	Events          []string `json:"events"`
	Enabled         bool     `json:"enabled"`
}

type Result struct {
	ChannelID string
	ExitCode  int
	Output    string
	Err       string
}

var ErrInvalid = errors.New("invalid command channel")

type Runner struct {
	db      *sql.DB
	log     *slog.Logger
	timeout time.Duration
}

func NewRunner(db *sql.DB, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{db: db, log: log, timeout: 10 * time.Second}
}

// Run executes each enabled command channel that subscribes to this event. A
// failure is isolated to its channel and never prevents later channels running.
func (r *Runner) Run(ctx context.Context, sc tenancy.Scope, ev Event) []Result {
	channels, err := List(ctx, r.db, sc)
	if err != nil {
		r.log.Error("notification command channels list failed", "err", err)
		return []Result{{Err: err.Error()}}
	}
	results := make([]Result, 0)
	for _, ch := range channels {
		if !ch.Enabled || !watches(ch.Events, ev.Kind) {
			continue
		}
		res := r.runOne(ctx, ch, ev)
		results = append(results, res)
		r.log.Info("command notification completed", "channel_id", res.ChannelID, "exit_code", res.ExitCode, "output", res.Output, "err", res.Err)
	}
	return results
}

func (r *Runner) runOne(ctx context.Context, ch Channel, ev Event) Result {
	result := Result{ChannelID: ch.ID, ExitCode: -1}
	args, err := renderArgs(ch.CommandTemplate, ev)
	if err != nil {
		result.Err = err.Error()
		return result
	}
	if len(args) == 0 || args[0] == "" {
		result.Err = "command executable is required"
		return result
	}
	if strings.Contains(args[0], "{{") {
		result.Err = "placeholders are not allowed in the executable"
		return result
	}
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = scrubbedEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out boundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err = cmd.Start(); err != nil {
		result.Err = err.Error()
		return result
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	waited := false
	select {
	case err = <-wait:
		waited = true
	case <-runCtx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case waitErr := <-wait:
			err = waitErr
			waited = true
		case <-time.After(200 * time.Millisecond):
		}
		// The direct child may exit on TERM while grandchildren remain in its
		// process group, so escalate the group regardless of the child's status.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if !waited {
			err = <-wait
		}
	}
	result.Output = redact(out.String())
	if err == nil {
		result.ExitCode = 0
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.Err = "command timed out"
		return result
	}
	result.Err = err.Error()
	return result
}

const outputLimit = 4096

type boundedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := outputLimit - b.b.Len()
	if left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		_, _ = b.b.Write(p)
	}
	return n, nil
}
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.b.String()
	if len(s) >= outputLimit {
		return s + "…[truncated]"
	}
	return s
}

var placeholderRE = regexp.MustCompile(`\{\{(event\.kind|run\.id|item\.id|title|body|link)\}\}`)

// renderArgs tokenizes a configuration template, not a shell program. Values
// are substituted into argv elements after tokenization and are never evaluated.
func renderArgs(template string, ev Event) ([]string, error) {
	words, err := splitTemplate(template)
	if err != nil {
		return nil, err
	}
	values := map[string]string{"event.kind": ev.Kind, "run.id": ev.RunID, "item.id": ev.ItemID, "title": ev.Title, "body": ev.Body, "link": ev.Link}
	for i, w := range words {
		unexpanded := placeholderRE.ReplaceAllString(w, "")
		if strings.Contains(unexpanded, "{{") || strings.Contains(unexpanded, "}}") {
			return nil, fmt.Errorf("unknown command placeholder")
		}
		words[i] = placeholderRE.ReplaceAllStringFunc(w, func(match string) string {
			key := strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}")
			return values[key]
		})
		if strings.ContainsRune(words[i], 0) {
			return nil, fmt.Errorf("command argument contains NUL")
		}
	}
	return words, nil
}

func splitTemplate(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	quote := rune(0)
	escaped := false
	started := false
	flush := func() {
		if started {
			out = append(out, b.String())
			b.Reset()
			started = false
		}
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			started = true
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			flush()
			continue
		}
		b.WriteRune(r)
		started = true
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape in command template")
	}
	flush()
	return out, nil
}

func watches(events []string, kind string) bool {
	for _, e := range events {
		if normalizeEvent(e) == normalizeEvent(kind) {
			return true
		}
	}
	return false
}
func normalizeEvent(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "_")
}

var secretShape = regexp.MustCompile(`(sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|gho_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|(?i)bearer\s+[A-Za-z0-9._~+/-]{12,})`)

func redact(s string) string { return secretShape.ReplaceAllString(s, "[REDACTED]") }

func scrubbedEnv() []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true, "TMPDIR": true, "USER": true, "SHELL": true, "TZ": true}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if allowed[key] && !strings.Contains(upper, "KEY") && !strings.Contains(upper, "TOKEN") && !strings.Contains(upper, "SECRET") && !strings.Contains(upper, "PASSWORD") {
			env = append(env, entry)
		}
	}
	return env
}

// List returns the tenant's configured command channels. Templates are
// configuration (not credentials); credentials needed by the invoked program
// belong in the OS keychain and are not included in this response.
func List(ctx context.Context, db *sql.DB, sc tenancy.Scope) ([]Channel, error) {
	out := []Channel{}
	err := withTenant(ctx, db, sc, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,tenant_id,name,command_template,events_json,enabled FROM notification_channels WHERE kind='command' ORDER BY created_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Channel
			var events []byte
			if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.CommandTemplate, &events, &c.Enabled); err != nil {
				return err
			}
			if err := json.Unmarshal(events, &c.Events); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func Create(ctx context.Context, db *sql.DB, sc tenancy.Scope, c Channel) (Channel, error) {
	if err := validateChannel(c); err != nil {
		return Channel{}, err
	}
	events, _ := json.Marshal(c.Events)
	err := withTenant(ctx, db, sc, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `INSERT INTO notification_channels (id,tenant_id,kind,name,command_template,events_json,enabled,created_by) VALUES ($1,$2,'command',$3,$4,$5,$6,nullif($7,'')) RETURNING id,tenant_id,name,command_template,events_json,enabled`, uuid.NewString(), sc.TenantID, c.Name, c.CommandTemplate, events, c.Enabled, sc.UserID).Scan(&c.ID, &c.TenantID, &c.Name, &c.CommandTemplate, &events, &c.Enabled)
	})
	if err == nil {
		_ = json.Unmarshal(events, &c.Events)
	}
	return c, err
}

type Patch struct {
	Name            *string
	CommandTemplate *string
	Events          *[]string
	Enabled         *bool
}

func Update(ctx context.Context, db *sql.DB, sc tenancy.Scope, id string, p Patch) (Channel, error) {
	var c Channel
	var events []byte
	err := withTenant(ctx, db, sc, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT id,tenant_id,name,command_template,events_json,enabled FROM notification_channels WHERE id=$1 AND kind='command'`, id).Scan(&c.ID, &c.TenantID, &c.Name, &c.CommandTemplate, &events, &c.Enabled); err != nil {
			return err
		}
		if err := json.Unmarshal(events, &c.Events); err != nil {
			return err
		}
		if p.Name != nil {
			c.Name = *p.Name
		}
		if p.CommandTemplate != nil {
			c.CommandTemplate = *p.CommandTemplate
		}
		if p.Events != nil {
			c.Events = *p.Events
		}
		if p.Enabled != nil {
			c.Enabled = *p.Enabled
		}
		if err := validateChannel(c); err != nil {
			return err
		}
		events, _ = json.Marshal(c.Events)
		return tx.QueryRowContext(ctx, `UPDATE notification_channels SET name=$2,command_template=$3,events_json=$4,enabled=$5 WHERE id=$1 AND kind='command' RETURNING id,tenant_id,name,command_template,events_json,enabled`, id, c.Name, c.CommandTemplate, events, c.Enabled).Scan(&c.ID, &c.TenantID, &c.Name, &c.CommandTemplate, &events, &c.Enabled)
	})
	if err == nil {
		_ = json.Unmarshal(events, &c.Events)
	}
	return c, err
}

func Delete(ctx context.Context, db *sql.DB, sc tenancy.Scope, id string) error {
	return withTenant(ctx, db, sc, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1 AND kind='command'`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return sql.ErrNoRows
		}
		return err
	})
}

func validateChannel(c Channel) error {
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.CommandTemplate) == "" || len(c.Events) == 0 {
		return fmt.Errorf("%w: name, command_template and at least one event are required", ErrInvalid)
	}
	if strings.ContainsRune(c.CommandTemplate, 0) {
		return fmt.Errorf("%w: command_template contains NUL", ErrInvalid)
	}
	args, err := splitTemplate(c.CommandTemplate)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(args) == 0 || strings.Contains(args[0], "{{") {
		return fmt.Errorf("%w: command executable must be static", ErrInvalid)
	}
	if isShell(args[0]) && len(args) > 2 && args[1] == "-c" && placeholderRE.MatchString(args[2]) {
		return fmt.Errorf("%w: event placeholders cannot be embedded in a shell program", ErrInvalid)
	}
	for _, event := range c.Events {
		switch normalizeEvent(event) {
		case "checkpoint_pending", "run_failed", "run_completed":
		default:
			return fmt.Errorf("%w: unsupported event %q", ErrInvalid, event)
		}
	}
	return nil
}

func isShell(path string) bool {
	switch filepath.Base(path) {
	case "sh", "bash", "dash", "zsh", "ksh":
		return true
	default:
		return false
	}
}

func withTenant(ctx context.Context, db *sql.DB, sc tenancy.Scope, fn func(*sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, sc.TenantID); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
