// Package usage aggregates adapter execution usage for tenant-owned work.
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Bucket is the usage total for one product during a time window.
type Bucket struct {
	ProductID   string        `json:"product_id"`
	ProductName string        `json:"product_name"`
	Jobs        int           `json:"jobs"`
	TokensIn    int           `json:"tokens_in"`
	TokensOut   int           `json:"tokens_out"`
	CostUSD     float64       `json:"cost_usd"`
	WallTime    time.Duration `json:"wall_time"`
	Failures    int           `json:"failures"`
}

// JobUsage is one agent job's persisted usage, with its agent and workflow step.
type JobUsage struct {
	ID        string        `json:"id"`
	AgentID   string        `json:"agent_id"`
	Agent     string        `json:"agent"`
	Step      string        `json:"step"`
	State     string        `json:"state"`
	TokensIn  int           `json:"tokens_in"`
	TokensOut int           `json:"tokens_out"`
	CostUSD   float64       `json:"cost_usd"`
	WallTime  time.Duration `json:"wall_time"`
	Failure   bool          `json:"failure"`
}

// Options selects the inclusive-start, exclusive-end aggregation window.
type Options struct {
	Since     time.Time
	Until     time.Time
	ProductID string
}

// withTenant runs fn in a transaction scoped to the tenant. Transaction-local
// configuration ensures a pooled connection cannot leak tenant state.
func withTenant(ctx context.Context, db *sql.DB, sc tenancy.Scope, fn func(*sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("usage: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		return fmt.Errorf("usage: set tenant scope: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Aggregate returns one row for every product with at least one attributed job
// in the requested window. Jobs without a workflow item/product (notably chat
// jobs) are deliberately excluded: product attribution follows the explicit
// job -> step run -> workflow run -> item -> project -> product chain below.
// Running jobs still contribute their known token/cost values, but no wall time
// is added until finished_at is known.
func Aggregate(ctx context.Context, db *sql.DB, sc tenancy.Scope, opts Options) ([]Bucket, error) {
	now := time.Now()
	if opts.Since.IsZero() {
		opts.Since = now.Add(-30 * 24 * time.Hour)
	}
	if opts.Until.IsZero() {
		opts.Until = now
	}
	out := []Bucket{}
	err := withTenant(ctx, db, sc, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT p.id, p.name,
			       count(*)::int,
			       coalesce(sum(j.tokens_in), 0)::int,
			       coalesce(sum(j.tokens_out), 0)::int,
			       coalesce(sum(j.cost_usd), 0)::double precision,
			       -- An unfinished job has unknown wall time and contributes nothing.
			       coalesce(sum(CASE
			           WHEN j.started_at IS NOT NULL AND j.finished_at IS NOT NULL
			           THEN extract(epoch FROM (j.finished_at - j.started_at))
			           ELSE 0
			       END), 0)::double precision,
			       count(*) FILTER (WHERE j.state = 'failed')::int
			  FROM agent_jobs j
			  JOIN step_runs sr ON sr.id = j.step_run_id AND sr.tenant_id = j.tenant_id
			  JOIN workflow_runs wr ON wr.id = sr.run_id AND wr.tenant_id = sr.tenant_id
			  JOIN items i ON i.id = wr.item_id AND i.tenant_id = wr.tenant_id
			  JOIN projects pr ON pr.id = i.project_id AND pr.tenant_id = i.tenant_id
			  JOIN products p ON p.id = pr.product_id AND p.tenant_id = pr.tenant_id
			 WHERE j.created_at >= $1 AND j.created_at < $2
			   AND ($3 = '' OR p.id = $3)
			 GROUP BY p.id, p.name
			 ORDER BY p.name, p.id`, opts.Since, opts.Until, opts.ProductID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b Bucket
			var wallSeconds float64
			if err := rows.Scan(&b.ProductID, &b.ProductName, &b.Jobs, &b.TokensIn,
				&b.TokensOut, &b.CostUSD, &wallSeconds, &b.Failures); err != nil {
				return err
			}
			b.WallTime = time.Duration(wallSeconds * float64(time.Second))
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// PerJob returns up to limit usage rows for an item, newest job first. Direct
// item jobs and workflow jobs whose run names the item are both included.
func PerJob(ctx context.Context, db *sql.DB, sc tenancy.Scope, itemID string, limit int) ([]JobUsage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	out := []JobUsage{}
	err := withTenant(ctx, db, sc, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT j.id, j.agent_id, a.name, coalesce(sr.name, ''), j.state,
			       j.tokens_in, j.tokens_out, coalesce(j.cost_usd, 0)::double precision,
			       CASE WHEN j.started_at IS NOT NULL AND j.finished_at IS NOT NULL
			            THEN extract(epoch FROM (j.finished_at - j.started_at))::double precision
			            ELSE 0::double precision END,
			       (j.state = 'failed')
		  FROM agent_jobs j
		  JOIN agents a ON a.id = j.agent_id AND a.tenant_id = j.tenant_id
		  LEFT JOIN step_runs sr ON sr.id = j.step_run_id AND sr.tenant_id = j.tenant_id
		  LEFT JOIN workflow_runs wr ON wr.id = sr.run_id AND wr.tenant_id = sr.tenant_id
		 WHERE j.item_id = $1 OR wr.item_id = $1
		 ORDER BY j.created_at DESC, j.id DESC
		 LIMIT $2`, itemID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j JobUsage
			var wallSeconds float64
			if err := rows.Scan(&j.ID, &j.AgentID, &j.Agent, &j.Step, &j.State,
				&j.TokensIn, &j.TokensOut, &j.CostUSD, &wallSeconds, &j.Failure); err != nil {
				return err
			}
			j.WallTime = time.Duration(wallSeconds * float64(time.Second))
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}
