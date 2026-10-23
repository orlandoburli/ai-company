package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/command"
	"github.com/orlandoburli/ai-company/internal/failure"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/notification"
	"github.com/orlandoburli/ai-company/internal/push"
	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/skill"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workspace"
)

// EngineDeps are the tenant-scoped services used by the workflow scheduler.
type EngineDeps struct {
	Store         *Store
	Agents        *agent.Store
	Memory        *memory.Store
	Skills        *skill.Store
	Work          *work.Store
	Workspaces    *workspace.Provider
	Notifications *notification.Store
	// Runners is the registry of runners — the HOW half of an agent. A node
	// resolves its runner from the agent's chain and the tenant's runners, so
	// a nil registry leaves the agent nodes failing with a capability failure
	// instead of running on a transport nobody chose.
	Runners *runner.Store
	// Failures carries the per-item retry state: the backoff curve and the
	// exhausted marker.
	Failures *failure.Store
	// Integrations carries the platform connectors (GitHub, git). Nil means
	// integration nodes fail with a capability failure instead of pretending.
	Integrations *Services
	// Push and Commands are the two out-of-band notification channels: the
	// browser push and the user-defined command. Both are optional.
	Push         *push.Sender
	Commands     *command.Runner
	Logger       *slog.Logger
	TickInterval time.Duration
	Lease        time.Duration
}

// Engine is a stateless, replay-driven workflow scheduler.
type Engine struct {
	store         *Store
	agents        *agent.Store
	memory        *memory.Store
	skills        *skill.Store
	work          *work.Store
	workspaces    *workspace.Provider
	notifications *notification.Store
	runners       *runner.Store
	failures      *failure.Store
	integrations  *Services
	push          *push.Sender
	commands      *command.Runner
	logger        *slog.Logger
	tickInterval  time.Duration
	lease         time.Duration
}

// NewEngine builds an engine. The database-backed state remains authoritative;
// no ready-set state is kept between ticks.
func NewEngine(deps EngineDeps) *Engine {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if deps.TickInterval <= 0 {
		deps.TickInterval = 500 * time.Millisecond
	}
	if deps.Lease <= 0 {
		deps.Lease = 2 * time.Minute
	}
	return &Engine{store: deps.Store, agents: deps.Agents, memory: deps.Memory, skills: deps.Skills, work: deps.Work, integrations: deps.Integrations, failures: deps.Failures, push: deps.Push, commands: deps.Commands,
		workspaces: deps.Workspaces, notifications: deps.Notifications, runners: deps.Runners, logger: logger,
		tickInterval: deps.TickInterval, lease: deps.Lease}
}

// Start runs the scheduler in the background, like the agent worker does. The
// loop is stateless: every pass recomputes the ready set from the persisted
// rows, so a restart is just the next tick.
func (e *Engine) Start(ctx context.Context) { go e.Run(ctx) }

// Run runs scheduler passes until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(e.tickInterval)
	defer ticker.Stop()
	for {
		if err := e.Tick(ctx); err != nil && ctx.Err() == nil {
			e.logger.Error("workflow tick failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick runs one pass over active runs. engine_active_runs is the sole
// cross-tenant query; all subsequent reads and writes carry a tenant scope.
func (e *Engine) Tick(ctx context.Context) error {
	if e == nil || e.store == nil {
		return errors.New("workflow engine has no store")
	}
	rows, err := e.store.db.QueryContext(ctx, `SELECT tenant_id,run_id FROM engine_active_runs()`)
	if err != nil {
		return fmt.Errorf("list active workflow runs: %w", err)
	}
	type coordinate struct{ tenant, run string }
	var active []coordinate
	for rows.Next() {
		var c coordinate
		if err := rows.Scan(&c.tenant, &c.run); err != nil {
			rows.Close()
			return err
		}
		active = append(active, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var errs []error
	for _, c := range active {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.tickRun(ctx, tenancy.Scope{TenantID: c.tenant, Role: tenancy.RoleMember}, c.run); err != nil {
			e.logger.Error("workflow run tick failed", "run_id", c.run, "tenant_id", c.tenant, "error", err)
			errs = append(errs, fmt.Errorf("run %s: %w", c.run, err))
		}
	}
	return errors.Join(errs...)
}

// RunOption tunes a run at creation.
type RunOption func(*NewRun)

// WithRunVariables sets the run's own variables — the values the run's
// templates are filled with, and the values a node's `config.runner` may
// resolve ($NAME). The run's variables win over the tenant's defaults.
func WithRunVariables(vars map[string]string) RunOption {
	return func(in *NewRun) {
		if len(vars) == 0 {
			return
		}
		in.Variables = vars
	}
}

// StartRun snapshots the chosen template and queues a run for its first tick.
func (e *Engine) StartRun(ctx context.Context, sc tenancy.Scope, templateID, itemID, actorUserID string, opts ...RunOption) (Run, error) {
	tpl, err := e.store.GetTemplate(ctx, sc, templateID)
	if err != nil {
		return Run{}, err
	}
	if _, err := ParseTemplate(tpl.GraphJSON); err != nil {
		return Run{}, err
	}
	if itemID != "" && e.work != nil {
		if _, err := e.work.GetItem(ctx, sc, itemID); err != nil {
			return Run{}, err
		}
	}
	in := NewRun{TemplateID: tpl.ID, TemplateVersion: tpl.Version,
		ItemID: itemID, DefinitionSnapshot: tpl.GraphJSON, MaxConcurrency: tpl.MaxConcurrency, StartedBy: actorUserID}
	for _, opt := range opts {
		if opt != nil {
			opt(&in)
		}
	}
	run, err := e.store.CreateRun(ctx, sc, in)
	if err != nil {
		return Run{}, err
	}
	if e.agents != nil {
		if _, err := e.agents.EnsureRunThread(ctx, sc, run.ID, "Workflow run", actorUserID); err != nil {
			return run, err
		}
	}
	return run, nil
}

// CancelRun cancels live jobs and nonterminal executions while retaining terminal audit rows.
func (e *Engine) CancelRun(ctx context.Context, sc tenancy.Scope, runID string) (Run, error) {
	steps, err := e.store.ListStepRuns(ctx, sc, runID)
	if err != nil {
		return Run{}, err
	}
	for _, step := range steps {
		if step.State == StepSucceeded || step.State == StepFailed || step.State == StepSkipped || step.State == StepBlocked || step.State == StepCancelled || step.State == StepSuperseded {
			continue
		}
		if step.AgentJobID != "" && e.agents != nil {
			_, cancelErr := e.agents.CancelJob(ctx, sc, step.AgentJobID)
			if cancelErr != nil && !errors.Is(cancelErr, agent.ErrNotRunning) && !errors.Is(cancelErr, agent.ErrNotFound) {
				return Run{}, cancelErr
			}
		}
		state := StepCancelled
		if _, err := e.store.UpdateStepRun(ctx, sc, step.ID, StepRunPatch{State: &state}); err != nil {
			return Run{}, err
		}
	}
	return e.store.CancelRun(ctx, sc, runID)
}

func (e *Engine) tickRun(ctx context.Context, sc tenancy.Scope, runID string) error {
	// A session advisory lock prevents two engine instances from acting on one run.
	conn, err := e.store.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, runID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, runID)
	}()
	run, err := e.store.GetRun(ctx, sc, runID)
	if err != nil {
		return err
	}
	tpl, err := ParseTemplate(run.DefinitionSnapshot)
	if err != nil {
		return fmt.Errorf("parse run snapshot: %w", err)
	}
	steps, err := e.store.ListStepRuns(ctx, sc, runID)
	if err != nil {
		return err
	}
	// Ensure edge targets exist before any upstream terminal transition. The
	// receipt update and terminal state can then be committed atomically.
	for _, n := range tpl.Nodes {
		if _, ok := latestStep(steps, n.ID, ""); ok {
			continue
		}
		created, createErr := e.store.CreateStepRun(ctx, sc, NewStepRun{RunID: runID, NodeID: n.ID,
			RunIndex: 0, Kind: string(n.Kind), Name: n.Name, WorkspaceKind: string(n.Workspace)})
		if createErr != nil && !errors.Is(createErr, ErrDuplicateStep) {
			return createErr
		}
		if createErr == nil {
			steps = append(steps, created)
		}
	}
	if err := e.finalizeJobs(ctx, sc, run, tpl, steps); err != nil {
		return err
	}
	steps, err = e.store.ListStepRuns(ctx, sc, runID)
	if err != nil {
		return err
	}
	persisted := projectSteps(tpl, steps)
	plan, err := Decide(tpl, persisted)
	if err != nil {
		return err
	}
	if err := e.applyGenerated(ctx, sc, run, tpl, steps, plan); err != nil {
		return err
	}
	// Re-evaluate after generated terminals, then dispatch independent ready nodes.
	steps, err = e.store.ListStepRuns(ctx, sc, runID)
	if err != nil {
		return err
	}
	plan, err = Decide(tpl, projectSteps(tpl, steps))
	if err != nil {
		return err
	}
	for _, ready := range plan.Ready {
		if err := e.dispatchStep(ctx, sc, run, tpl, steps, ready); err != nil {
			return err
		}
	}
	steps, err = e.store.ListStepRuns(ctx, sc, runID)
	if err != nil {
		return err
	}
	plan, err = Decide(tpl, projectSteps(tpl, steps))
	if err != nil {
		return err
	}
	// The dispatch above can create the state a generated terminal depends on
	// (a loop that exhausted its budget only knows it after the iteration it
	// just ran). Applying the generated terminals again materializes those
	// steps, so the run state is computed from the rows, not from a plan.
	if err := e.applyGenerated(ctx, sc, run, tpl, steps, plan); err != nil {
		return err
	}
	steps, err = e.store.ListStepRuns(ctx, sc, runID)
	if err != nil {
		return err
	}
	plan, err = Decide(tpl, projectSteps(tpl, steps))
	if err != nil {
		return err
	}
	state := RunState(plan.RunState)
	updated, err := e.store.SetRunState(ctx, sc, runID, state, string(plan.RunFailureKind), plan.RunError)
	if err != nil {
		return err
	}
	if updated.State == RunFailed {
		e.notifyRunFailure(ctx, sc, updated)
		e.recordItemFailure(ctx, sc, updated, string(plan.RunFailureKind), plan.RunError)
	}
	if updated.State == RunCompleted {
		e.clearItemFailure(ctx, sc, updated)
	}
	return nil
}

// recordItemFailure puts the run's item into backoff after a failed run.
//
// The item is not punished for a broken workflow: the failure counter lives
// on the (item, workflow) pair, the curve is the one configured for the
// tenant, and a pause never touches this state — a pause is not a failure.
func (e *Engine) recordItemFailure(ctx context.Context, sc tenancy.Scope, run Run, kind, message string) {
	if e.failures == nil || run.ItemID == "" {
		return
	}
	curve := failure.CurveExponential
	params := failure.DefaultParams(curve)
	if _, err := e.failures.Record(ctx, sc, failure.Record{
		ItemID:     run.ItemID,
		WorkflowID: run.TemplateID,
		Curve:      curve,
		Params:     params,
		Version:    failure.Version("bo-exp-1"),
		Kind:       kind,
		ErrorMD:    message,
		FailedAt:   time.Now(),
	}); err != nil {
		e.logger.Warn("record item failure", "run", run.ID, "item", run.ItemID, "error", err)
	}
}

// clearItemFailure resets the counter after a successful run: the next failure
// starts from the first step of the curve again.
func (e *Engine) clearItemFailure(ctx context.Context, sc tenancy.Scope, run Run) {
	if e.failures == nil || run.ItemID == "" {
		return
	}
	if err := e.failures.Succeed(ctx, sc, run.ItemID, run.TemplateID); err != nil {
		e.logger.Warn("clear item failure", "run", run.ID, "item", run.ItemID, "error", err)
	}
}

func (e *Engine) notifyRunFailure(ctx context.Context, sc tenancy.Scope, run Run) {
	if e.notifications == nil || run.StartedBy == "" {
		return
	}
	payload, _ := json.Marshal(map[string]string{"run_id": run.ID})
	_, err := e.notifications.CreateMany(ctx, sc, notification.New{UserID: run.StartedBy,
		Kind: notification.KindRunFailed, TitleMD: "Workflow failed", BodyMD: run.ErrorText,
		Link: "/runs/" + run.ID, Payload: payload}, []string{run.StartedBy})
	if err != nil {
		e.logger.Warn("notify failed workflow run", "run_id", run.ID, "error", err)
	}
}

func latestStep(steps []StepRun, nodeID, path string) (StepRun, bool) {
	var found StepRun
	for _, step := range steps {
		if step.NodeID == nodeID && step.IterationPath == path && step.State != StepSuperseded && (found.ID == "" || step.RunIndex > found.RunIndex) {
			found = step
		}
	}
	return found, found.ID != ""
}

func decodeConfig(node Node) map[string]json.RawMessage {
	var cfg map[string]json.RawMessage
	_ = json.Unmarshal(node.Config, &cfg)
	if cfg == nil {
		cfg = map[string]json.RawMessage{}
	}
	return cfg
}

func stringConfig(cfg map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(cfg[key], &value)
	return value
}

func rawJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func stepSummary(step StepRun) string {
	var result struct {
		Summary string `json:"summary"`
	}
	_ = json.Unmarshal(step.Result, &result)
	return strings.TrimSpace(result.Summary)
}
