package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Store is the runner registry over the application connection.
type Store struct {
	db *sql.DB
}

// NewStore builds a runner store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func newID() string { return strings.ToUpper(uuid.NewString()) }

// withTenant runs fn in a transaction scoped to the tenant. Same discipline as
// internal/agent: the setting is transaction-local, so a pooled connection
// cannot carry a tenant into the next request.
func (s *Store) withTenant(ctx context.Context, sc tenancy.Scope, fn func(context.Context, *sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("runner: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx,
		`SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		return fmt.Errorf("runner: set tenant scope: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// mapPGError turns schema errors into typed runner errors.
//   - 23505 → ErrConflict (duplicate name)
//   - 23503 → ErrNotFound, because the only unmet FKs reference a missing
//     parent in this tenant
//   - 23514 → ErrInvalid (shape/state CHECKs)
func mapPGError(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return fmt.Errorf("%w: %s", ErrConflict, pg.ConstraintName)
		case "23503":
			return fmt.Errorf("%w: referenced row does not exist in this tenant", ErrNotFound)
		case "23514":
			return fmt.Errorf("%w: %s", ErrInvalid, pg.ConstraintName)
		}
	}
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

// runnerColumns is the column list every runner read shares.
const runnerColumns = `
	r.id, r.tenant_id, r.name, r.kind, r.config_json, r.credential_ref,
	r.budget_tokens, r.budget_cost_usd, r.enabled, r.created_at, r.updated_at`

func scanRunner(row rowScanner) (Runner, error) {
	var (
		r      Runner
		config []byte
	)
	if err := row.Scan(&r.ID, &r.TenantID, &r.Name, &r.Kind, &config, &r.CredentialRef,
		&r.BudgetTokens, &r.BudgetCostUSD, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Runner{}, err
	}
	r.Config = json.RawMessage(config)
	return r, nil
}

// orJSON returns raw when non-empty, otherwise the schema default.
func orJSON(v json.RawMessage, dflt string) []byte {
	if len(v) > 0 {
		return v
	}
	return []byte(dflt)
}

// raw returns nil for an empty message so the column keeps its default.
func raw(v json.RawMessage) []byte {
	if len(v) == 0 {
		return nil
	}
	return v
}

// Create adds a runner to the tenant. A nil Enabled means enabled; the
// budgets default to zero, which means "no budget recorded".
func (s *Store) Create(ctx context.Context, sc tenancy.Scope, in NewRunner) (Runner, error) {
	if err := validateNew(in); err != nil {
		return Runner{}, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	var out Runner
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		created, err := scanRunner(tx.QueryRowContext(ctx, `
			INSERT INTO runners AS r (id, tenant_id, name, kind, config_json,
			                          credential_ref, budget_tokens, budget_cost_usd, enabled)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9)
			RETURNING`+runnerColumns,
			newID(), sc.TenantID, in.Name, string(in.Kind),
			orJSON(in.Config, "{}"), in.CredentialRef,
			in.BudgetTokens, in.BudgetCostUSD, enabled))
		if err != nil {
			return err
		}
		out = created
		return nil
	})
	if err != nil {
		return Runner{}, mapPGError(err)
	}
	return out, nil
}

// Update applies a patch to one runner and returns the updated row.
//
// The effective row is validated, not just the patch: changing the kind
// without a config that matches it would leave a runner the adapter could
// never run.
func (s *Store) Update(ctx context.Context, sc tenancy.Scope, id string, p Patch) (Runner, error) {
	if p.Name != nil {
		if err := ValidateName(*p.Name); err != nil {
			return Runner{}, err
		}
	}
	if p.Kind != nil && !p.Kind.Valid() {
		return Runner{}, fmt.Errorf("%w: unknown kind %q", ErrInvalid, *p.Kind)
	}
	if p.BudgetTokens != nil && *p.BudgetTokens < 0 {
		return Runner{}, fmt.Errorf("%w: budget_tokens must not be negative", ErrInvalid)
	}
	if p.BudgetCostUSD != nil && *p.BudgetCostUSD < 0 {
		return Runner{}, fmt.Errorf("%w: budget_cost_usd must not be negative", ErrInvalid)
	}
	if p.CredentialRef != nil && strings.ContainsRune(*p.CredentialRef, 0) {
		return Runner{}, fmt.Errorf("%w: credential_ref contains an invalid character", ErrInvalid)
	}

	var out Runner
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		cur, err := scanRunner(tx.QueryRowContext(ctx, `
			SELECT`+runnerColumns+`
			  FROM runners r
			 WHERE r.id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		kind := cur.Kind
		if p.Kind != nil {
			kind = *p.Kind
		}
		config := cur.Config
		if p.Config != nil {
			config = *p.Config
		}
		if err := ValidateConfig(kind, config); err != nil {
			return err
		}

		sets := []string{"updated_at = now()"}
		args := []any{id}
		add := func(expr string, val any) {
			args = append(args, val)
			sets = append(sets, fmt.Sprintf(expr, len(args)))
		}
		if p.Name != nil {
			add("name = $%d", *p.Name)
		}
		if p.Kind != nil {
			add("kind = $%d", string(*p.Kind))
		}
		if p.Config != nil {
			add("config_json = COALESCE($%d::jsonb, '{}'::jsonb)", raw(*p.Config))
		}
		if p.CredentialRef != nil {
			add("credential_ref = $%d", *p.CredentialRef)
		}
		if p.BudgetTokens != nil {
			add("budget_tokens = $%d", *p.BudgetTokens)
		}
		if p.BudgetCostUSD != nil {
			add("budget_cost_usd = $%d", *p.BudgetCostUSD)
		}
		if p.Enabled != nil {
			add("enabled = $%d", *p.Enabled)
		}

		updated, err := scanRunner(tx.QueryRowContext(ctx, `
			UPDATE runners AS r SET `+strings.Join(sets, ", ")+`
			 WHERE r.id = $1
			RETURNING`+runnerColumns, args...))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return Runner{}, mapPGError(err)
	}
	return out, nil
}

// Get returns one runner by id.
func (s *Store) Get(ctx context.Context, sc tenancy.Scope, id string) (Runner, error) {
	var out Runner
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = scanRunner(tx.QueryRowContext(ctx, `
			SELECT`+runnerColumns+`
			  FROM runners r
			 WHERE r.id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return Runner{}, mapPGError(err)
	}
	return out, nil
}

// GetByName resolves a runner by name. The lookup is case-insensitive, like
// the agents' names. The boolean reports whether the runner exists.
func (s *Store) GetByName(ctx context.Context, sc tenancy.Scope, name string) (Runner, bool, error) {
	var out Runner
	found := false
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		r, err := scanRunner(tx.QueryRowContext(ctx, `
			SELECT`+runnerColumns+`
			  FROM runners r
			 WHERE lower(r.name) = lower($1)`, name))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = r, true
		return nil
	})
	if err != nil {
		return Runner{}, false, mapPGError(err)
	}
	return out, found, nil
}

// List returns the tenant's runners, ordered by name.
func (s *Store) List(ctx context.Context, sc tenancy.Scope) ([]Runner, error) {
	out := []Runner{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT`+runnerColumns+`
			  FROM runners r
			 ORDER BY r.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRunner(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, mapPGError(err)
	}
	return out, nil
}

// SetEnabled enables or disables a runner and returns the updated row. A
// disabled runner stays in the chain; the runtime skips it.
func (s *Store) SetEnabled(ctx context.Context, sc tenancy.Scope, id string, enabled bool) (Runner, error) {
	var out Runner
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		updated, err := scanRunner(tx.QueryRowContext(ctx, `
			UPDATE runners AS r
			   SET enabled = $2, updated_at = now()
			 WHERE r.id = $1
			RETURNING`+runnerColumns, id, enabled))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return Runner{}, mapPGError(err)
	}
	return out, nil
}

// Delete removes a runner.
//
// A runner that is bound to an agent is NOT deleted: the delete is refused
// with ErrRunnerInUse, so a live agent cannot lose its transport by accident.
// Unbind it from the agent's chain first.
func (s *Store) Delete(ctx context.Context, sc tenancy.Scope, id string) error {
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var bound int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM agent_runners WHERE runner_id = $1`, id).Scan(&bound); err != nil {
			return err
		}
		if bound > 0 {
			return fmt.Errorf("%w: runner is bound to %d agent(s)", ErrRunnerInUse, bound)
		}
		tag, err := tx.ExecContext(ctx, `DELETE FROM runners WHERE id = $1`, id)
		if err != nil {
			return err
		}
		n, err := tag.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	return mapPGError(err)
}

// SetChain replaces an agent's chain with the given runners, in order: the
// first id is priority 1 (the runner to try first), the second priority 2,
// and so on. An empty list clears the chain.
//
// Every runner must exist in this tenant and may appear only once; the agent
// must exist too. The whole chain is written in one transaction, so an agent
// never has half a chain.
func (s *Store) SetChain(ctx context.Context, sc tenancy.Scope, agentID string, runnerIDs []string) error {
	if strings.TrimSpace(agentID) == "" {
		return fmt.Errorf("%w: agent id is required", ErrInvalid)
	}
	seen := make(map[string]bool, len(runnerIDs))
	for _, id := range runnerIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: the chain may not contain an empty runner id", ErrInvalid)
		}
		if seen[id] {
			return fmt.Errorf("%w: runner %s appears twice in the chain", ErrInvalid, id)
		}
		seen[id] = true
	}

	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var agentExists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1)`, agentID).Scan(&agentExists); err != nil {
			return err
		}
		if !agentExists {
			return fmt.Errorf("%w: agent does not exist in this tenant", ErrNotFound)
		}
		for _, id := range runnerIDs {
			var exists bool
			if err := tx.QueryRowContext(ctx,
				`SELECT EXISTS (SELECT 1 FROM runners WHERE id = $1)`, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("%w: runner %s does not exist in this tenant", ErrNotFound, id)
			}
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agent_runners WHERE agent_id = $1`, agentID); err != nil {
			return err
		}
		for i, id := range runnerIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agent_runners (id, tenant_id, agent_id, runner_id, priority)
				VALUES ($1, $2, $3, $4, $5)`,
				newID(), sc.TenantID, agentID, id, i+1); err != nil {
				return err
			}
		}
		return nil
	})
	return mapPGError(err)
}

// Chain returns the runners an agent may use, in fallback order. An agent
// with no chain yields an empty list.
func (s *Store) Chain(ctx context.Context, sc tenancy.Scope, agentID string) ([]Runner, error) {
	out := []Runner{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT`+runnerColumns+`
			  FROM agent_runners ar
			  JOIN runners r ON r.id = ar.runner_id AND r.tenant_id = ar.tenant_id
			 WHERE ar.agent_id = $1
			 ORDER BY ar.priority`, agentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRunner(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, mapPGError(err)
	}
	return out, nil
}
