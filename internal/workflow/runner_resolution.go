package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The runner resolution.
//
// An agent is the WHAT (persona, role, skills); a runner is the HOW (a CLI
// binary or an API model, with its config). A workflow node may name the
// runner it wants, and the engine picks one. The order is exactly:
//
//  1. the variables — the run's own variables merged OVER the tenant's
//     defaults, so the run wins;
//  2. the node's choice — `config.runner`: empty takes the agent's first
//     runner in its chain, a name takes that runner of the tenant, and
//     `$NAME` takes the variable's value and then that runner by name (an
//     unset variable falls through to the chain's first runner);
//  3. the budget guard — a runner whose cumulative spend for the tenant has
//     reached its budget is skipped in favour of the next runner of the
//     chain, and a disabled runner is skipped the same way;
//  4. the switch — when the resolution moved past the runner the node asked
//     for, the new runner's name is written into the run's variables (under
//     the variable the node referenced, or under `runner` when the node named
//     a runner outright) and the switch is logged, so the rest of the run
//     follows it.
//
// A runner that does not exist is an error the node reports as a capability
// failure: the run cannot invent a transport.

// ErrRunnersNotConfigured is returned when the engine has no runner registry
// wired. The node fails with a capability failure that says so — the engine
// never pretends a runner exists.
var ErrRunnersNotConfigured = errors.New("workflow: the runners are not configured")

// defaultRunnerVariable is the key a switch is recorded under when the node
// named a runner outright and there is no variable to write it into.
const defaultRunnerVariable = "runner"

// RunnerSpend is one runner's cumulative spend for one tenant: the tokens and
// the cost of every job the tenant ran on that runner.
type RunnerSpend struct {
	Tokens  int64
	CostUSD float64
}

// Spent reports whether the runner has used up its budget.
//
// A zero budget is unlimited: budget_tokens and budget_cost_usd are limits,
// not quotas, and a runner without a recorded budget never runs out. The
// spend is spent when it REACHES the budget, not when it passes it.
func Spent(r runner.Runner, spend RunnerSpend) bool {
	if r.BudgetTokens > 0 && spend.Tokens >= r.BudgetTokens {
		return true
	}
	if r.BudgetCostUSD > 0 && spend.CostUSD >= r.BudgetCostUSD {
		return true
	}
	return false
}

// MergeVariables merges the run's variables over the tenant's defaults.
//
// The run wins: a variable the run sets is the one the run uses, and the
// tenant's default is only the value for a variable the run left alone.
func MergeVariables(runVars, defaults map[string]string) map[string]string {
	out := make(map[string]string, len(defaults)+len(runVars))
	for name, value := range defaults {
		out[name] = value
	}
	for name, value := range runVars {
		out[name] = value
	}
	return out
}

// WalkChain returns the first runner of the chain that may run the step:
// enabled, within its budget, and not already tried for this step.
//
// The walk starts AFTER the runner the node wanted, because the chain is the
// fallback order: the next runner is the one that follows, never one that
// was already passed over. A runner that is not part of the chain (the node
// named one the agent does not carry) starts the walk at the top.
//
// The spend is asked for per runner through the spend function, so a chain is
// walked against the budget as it is read. A nil spend function treats every
// runner as within its budget.
func WalkChain(chain []runner.Runner, after *runner.Runner, spend func(runner.Runner) (RunnerSpend, error), tried map[string]bool) (runner.Runner, bool, error) {
	start := 0
	if after != nil {
		for i, r := range chain {
			if r.ID == after.ID {
				start = i + 1
				break
			}
		}
	}
	for _, r := range chain[start:] {
		if tried[r.ID] {
			continue
		}
		if !r.Enabled {
			continue
		}
		if spend != nil {
			used, err := spend(r)
			if err != nil {
				return runner.Runner{}, false, err
			}
			if Spent(r, used) {
				continue
			}
		}
		return r, true, nil
	}
	return runner.Runner{}, false, nil
}

// ResolveRunner picks the runner for one node execution.
//
// The run carries the variables the resolution reads; the agent carries the
// chain the resolution walks. The resolution never mutates the agent: the
// switch is recorded in the run's variables, and the run's variables are
// what the rest of the run follows.
func (e *Engine) ResolveRunner(ctx context.Context, sc tenancy.Scope, run Run, node Node, agentID string) (runner.Runner, error) {
	if e.runners == nil {
		return runner.Runner{}, fmt.Errorf("%w: the node cannot pick a runner", ErrRunnersNotConfigured)
	}

	// 1. The variables: the run's own over the tenant's defaults.
	defaults, err := e.tenantVariables(ctx, sc)
	if err != nil {
		return runner.Runner{}, err
	}
	runVars := e.runVariables(ctx, sc, run)
	vars := MergeVariables(runVars, defaults)

	// 2. The node's choice.
	cfg := decodeConfig(node)
	choice := strings.TrimSpace(stringConfig(cfg, "runner"))
	chain, err := e.runners.Chain(ctx, sc, agentID)
	if err != nil {
		return runner.Runner{}, err
	}

	var (
		wanted runner.Runner
		varKey string
	)
	switch {
	case choice == "":
		// No choice: the agent's first runner in the chain.
		if len(chain) == 0 {
			return runner.Runner{}, fmt.Errorf("the agent %s has no runner to run on", agentID)
		}
		wanted = chain[0]
	case strings.HasPrefix(choice, "$"):
		varKey = strings.TrimPrefix(choice, "$")
		name := strings.TrimSpace(vars[varKey])
		if name == "" {
			// An unset variable falls through to the chain's first runner.
			if len(chain) == 0 {
				return runner.Runner{}, fmt.Errorf("the agent %s has no runner to run on", agentID)
			}
			wanted = chain[0]
			break
		}
		found, ok, err := e.runners.GetByName(ctx, sc, name)
		if err != nil {
			return runner.Runner{}, err
		}
		if !ok {
			return runner.Runner{}, fmt.Errorf("the runner %q named by $%s does not exist in this tenant", name, varKey)
		}
		wanted = found
	default:
		found, ok, err := e.runners.GetByName(ctx, sc, choice)
		if err != nil {
			return runner.Runner{}, err
		}
		if !ok {
			return runner.Runner{}, fmt.Errorf("the runner %q does not exist in this tenant", choice)
		}
		wanted = found
	}

	// 3. The budget guard — and the disabled runner, which is skipped the
	// same way.
	chosen := wanted
	if !wanted.Enabled {
		next, ok, err := WalkChain(chain, &wanted, e.spendFor(ctx, sc), nil)
		if err != nil {
			return runner.Runner{}, err
		}
		if !ok {
			return runner.Runner{}, fmt.Errorf("the runner %q is disabled and no other runner of the agent's chain is available", wanted.Name)
		}
		chosen = next
	} else {
		used, err := e.runnerSpend(ctx, sc, wanted.ID)
		if err != nil {
			return runner.Runner{}, err
		}
		if Spent(wanted, used) {
			next, ok, err := WalkChain(chain, &wanted, e.spendFor(ctx, sc), nil)
			if err != nil {
				return runner.Runner{}, err
			}
			if !ok {
				return runner.Runner{}, fmt.Errorf("the budget of the runner %q is spent and no other runner of the agent's chain is available", wanted.Name)
			}
			chosen = next
		}
	}

	// 4. The switch is recorded, so the rest of the run follows it.
	if chosen.ID != wanted.ID {
		key := varKey
		if key == "" {
			key = defaultRunnerVariable
		}
		// The switch is written into the RUN's variables — not into the
		// tenant's defaults, which every other run still inherits.
		runVars[key] = chosen.Name
		if _, err := e.store.SetRunVariables(ctx, sc, run.ID, runVars); err != nil {
			return runner.Runner{}, err
		}
		e.logger.Info("workflow runner switch",
			"run_id", run.ID, "node_id", node.ID, "agent_id", agentID,
			"from", wanted.Name, "to", chosen.Name, "variable", key)
	}
	return chosen, nil
}

// runVariables returns the run's variables as they are stored right now.
//
// The run the caller holds may be a tick old: a switch recorded by an earlier
// node in this tick is already written, and the rest of the run has to follow
// it. When the run cannot be read, the caller's copy is used as it is.
//
// The returned map is a copy: the caller may write the switch into it without
// touching the run it was handed.
func (e *Engine) runVariables(ctx context.Context, sc tenancy.Scope, run Run) map[string]string {
	out := map[string]string{}
	for name, value := range run.Variables {
		out[name] = value
	}
	if e.store == nil || run.ID == "" {
		return out
	}
	fresh, err := e.store.GetRun(ctx, sc, run.ID)
	if err != nil {
		return out
	}
	out = map[string]string{}
	for name, value := range fresh.Variables {
		out[name] = value
	}
	return out
}

// tenantVariables returns the tenant's default variables.
func (e *Engine) tenantVariables(ctx context.Context, sc tenancy.Scope) (map[string]string, error) {
	if e.store == nil {
		return map[string]string{}, nil
	}
	return e.store.TenantVariables(ctx, sc)
}

// runnerSpend returns the runner's cumulative spend for the tenant.
//
// The spend is the sum of every job the tenant ran on that runner: the
// tokens (in and out) and the cost. It is cumulative across the tenant's
// runs, not per run: a runner's budget is the tenant's budget for that
// transport.
func (e *Engine) runnerSpend(ctx context.Context, sc tenancy.Scope, runnerID string) (RunnerSpend, error) {
	var spend RunnerSpend
	if e.store == nil || runnerID == "" {
		return spend, nil
	}
	err := e.store.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT COALESCE(sum(tokens_in), 0) + COALESCE(sum(tokens_out), 0),
			       COALESCE(sum(cost_usd), 0)
			  FROM agent_jobs
			 WHERE runner_id = $1`, runnerID).Scan(&spend.Tokens, &spend.CostUSD)
	})
	if err != nil {
		return RunnerSpend{}, err
	}
	return spend, nil
}

// spendFor is the budget lookup the chain walk uses.
func (e *Engine) spendFor(ctx context.Context, sc tenancy.Scope) func(runner.Runner) (RunnerSpend, error) {
	return func(r runner.Runner) (RunnerSpend, error) {
		return e.runnerSpend(ctx, sc, r.ID)
	}
}

// --- the variables ----------------------------------------------------------

// TenantVariables returns the tenant's default variables, by name.
//
// These are the values a run inherits: the run's own variables win, and a
// variable nobody set is simply absent.
func (s *Store) TenantVariables(ctx context.Context, sc tenancy.Scope) (map[string]string, error) {
	out := map[string]string{}
	if s == nil {
		return out, nil
	}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT name, value FROM tenant_variables ORDER BY name`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var name, value string
			if e := rows.Scan(&name, &value); e != nil {
				return e
			}
			out[name] = value
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetTenantVariables replaces the tenant's default variables.
//
// The whole set is replaced in one transaction: a tenant's defaults are one
// document, and a half-written set is a set of defaults nobody chose.
func (s *Store) SetTenantVariables(ctx context.Context, sc tenancy.Scope, vars map[string]string) error {
	if s == nil {
		return errors.New("workflow: no store")
	}
	return s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `DELETE FROM tenant_variables`); e != nil {
			return e
		}
		for name, value := range vars {
			if _, e := tx.ExecContext(ctx,
				`INSERT INTO tenant_variables (tenant_id, name, value) VALUES ($1, $2, $3)`,
				sc.TenantID, name, value); e != nil {
				return e
			}
		}
		return nil
	})
}

// SetRunVariables replaces the run's variables.
//
// The engine writes the variables when a runner switch happens, so the rest
// of the run follows the runner that is actually running.
func (s *Store) SetRunVariables(ctx context.Context, sc tenancy.Scope, runID string, vars map[string]string) (Run, error) {
	encoded, err := encodeVariables(vars)
	if err != nil {
		return Run{}, err
	}
	var out Run
	err = s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanRun(tx.QueryRowContext(ctx,
			`UPDATE workflow_runs SET variables_json=$2::jsonb WHERE id=$1 RETURNING `+runCols, runID, encoded))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		return Run{}, mapError(err)
	}
	return out, nil
}
