package command

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type commandFixture struct {
	app *sql.DB
	sc  tenancy.Scope
}

func newCommandFixture(t *testing.T) *commandFixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	if _, err := app.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`, "C1", "Command tenant", "command-tenant", "U-C1", "command@example.com", "x", "Command owner", "M-C1"); err != nil {
		t.Fatal(err)
	}
	return &commandFixture{app: app, sc: tenancy.Scope{TenantID: "C1", UserID: "U-C1", Role: tenancy.RoleOwner}}
}

func TestRenderArgsKeepsValuesAsSingleArguments(t *testing.T) {
	args, err := renderArgs(`/usr/bin/printf '%s|%s' {{title}} {{event.kind}}`, Event{Title: "hello world; $(touch pwned)", Kind: "checkpoint_pending"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/printf", "%s|%s", "hello world; $(touch pwned)", "checkpoint_pending"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv=%q want %q", args, want)
	}
}

func TestRejectsUntrustedPlaceholderInsideShellProgram(t *testing.T) {
	err := validateChannel(Channel{Name: "unsafe", CommandTemplate: `/bin/sh -c 'echo {{title}}'`, Events: []string{"run_failed"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("shell interpolation accepted: %v", err)
	}
}

func TestRunnerWatchesEventsIsolatesFailureAndScrubsEnvironment(t *testing.T) {
	f := newCommandFixture(t)
	ctx := context.Background()
	if _, err := Create(ctx, f.app, f.sc, Channel{Name: "unmatched", CommandTemplate: "/usr/bin/echo wrong", Events: []string{"run_failed"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, f.app, f.sc, Channel{Name: "failure", CommandTemplate: "/usr/bin/false", Events: []string{"checkpoint_pending"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, f.app, f.sc, Channel{Name: "success", CommandTemplate: "/usr/bin/printf '%s|%s' {{title}} {{run.id}}", Events: []string{"checkpoint pending"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, f.app, f.sc, Channel{Name: "environment", CommandTemplate: "/usr/bin/env", Events: []string{"checkpoint_pending"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	r := NewRunner(f.app, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ev := Event{Kind: "checkpoint_pending", RunID: "run-7", Title: "title with spaces"}
	results := r.Run(ctx, f.sc, ev)
	if len(results) != 3 {
		t.Fatalf("ran %d channels, want 3: %+v", len(results), results)
	}
	channels, err := List(ctx, f.app, f.sc)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, c := range channels {
		names[c.ID] = c.Name
	}
	byName := map[string]Result{}
	for _, res := range results {
		byName[names[res.ChannelID]] = res
	}
	if byName["failure"].ExitCode != 1 {
		t.Fatalf("failed channel exit: %+v", byName["failure"])
	}
	if byName["success"].ExitCode != 0 || byName["success"].Output != "title with spaces|run-7" {
		t.Fatalf("substituted argv/output incorrect: %+v", byName["success"])
	}
	if byName["environment"].ExitCode != 0 || strings.Contains(byName["environment"].Output, "OPENAI_API_KEY") || strings.Contains(byName["environment"].Output, "must-not-leak") {
		t.Fatalf("credential reached environment: %+v", byName["environment"])
	}
}

func TestRunnerArgumentVectorAndTimeout(t *testing.T) {
	// The helper is this test binary itself; the JSON confirms the exact argv
	// observed by the child, not a reconstructed shell command.
	f := newCommandFixture(t)
	ctx := context.Background()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	template := bin + ` -test.run=TestCommandArgvHelper -- {{title}} {{item.id}}`
	if _, err := Create(ctx, f.app, f.sc, Channel{Name: "argv", CommandTemplate: template, Events: []string{"run_failed"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(f.app, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := r.Run(ctx, f.sc, Event{Kind: "run_failed", Title: "a b", ItemID: "I-1"})
	if len(got) != 1 || got[0].ExitCode != 0 {
		t.Fatalf("argv run: %+v", got)
	}
	output := got[0].Output
	if end := strings.Index(output, "]"); end >= 0 {
		output = output[:end+1]
	}
	var actual []string
	if err := json.Unmarshal([]byte(output), &actual); err != nil {
		t.Fatalf("output not argv JSON: %q: %v", got[0].Output, err)
	}
	if len(actual) != 2 || actual[0] != "a b" || actual[1] != "I-1" {
		t.Fatalf("child argv=%q", actual)
	}
	if _, err := Create(ctx, f.app, f.sc, Channel{Name: "timeout", CommandTemplate: "/bin/sleep 5", Events: []string{"run_completed"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	r.timeout = 100 * time.Millisecond
	out := r.Run(ctx, f.sc, Event{Kind: "run_completed"})
	if len(out) != 1 || out[0].Err != "command timed out" {
		t.Fatalf("timeout result: %+v", out)
	}
}

func TestCommandArgvHelper(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		return
	}
	args := os.Args[separator+1:]
	b, _ := json.Marshal(args)
	_, _ = os.Stdout.Write(b)
}
