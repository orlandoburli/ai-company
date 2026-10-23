package dispatch

import (
	"context"
	"database/sql"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

// The runtime adapters for the dispatcher.
//
// The stores are tenant-scoped on purpose, so the two questions that cross a
// tenant boundary — which tenants are there, and how busy is the shared worker
// pool — are answered by two narrow SECURITY DEFINER functions owned by
// aic_auth (migration 0017). Everything else the dispatcher does is
// tenant-scoped, under RLS.

// StoreTenants enumerates the tenants through the narrow definer function.
type StoreTenants struct {
	DB *sql.DB
}

// DispatchTenants returns every tenant with the user who owns it.
func (s StoreTenants) DispatchTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT tenant_id, owner_user_id FROM dispatch_tenants()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []Tenant
	for rows.Next() {
		var tenantID, owner string
		if err := rows.Scan(&tenantID, &owner); err != nil {
			return nil, err
		}
		out = append(out, Tenant{Scope: tenancy.Scope{TenantID: tenantID, UserID: owner, Role: tenancy.RoleOwner}, OwnerUserID: owner})
	}
	return out, rows.Err()
}

// StoreCapacity reports the shared worker pool and the per-agent load.
type StoreCapacity struct {
	DB     *sql.DB
	Agents *agent.Store
}

// RunningJobs counts the jobs the workers are running across every tenant:
// the pool is global, so the count has to cross the tenant boundary.
func (s StoreCapacity) RunningJobs(ctx context.Context) (int, error) {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT dispatch_running_jobs()`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// RunningAgentJobs counts the jobs one agent is running in one tenant.
func (s StoreCapacity) RunningAgentJobs(ctx context.Context, sc tenancy.Scope, agentID string) (int, error) {
	if s.Agents == nil {
		return 0, nil
	}
	return s.Agents.RunningAgentJobs(ctx, sc, agentID)
}

// WorkActivity records that a run was started for an item. Every automatic
// action is attributed, like every human one.
type WorkActivity struct {
	Work *work.Store
}

// RecordRunStarted appends the `item.run_started` activity entry.
func (a WorkActivity) RecordRunStarted(ctx context.Context, sc tenancy.Scope, itemID, actorUserID, templateID, runID string) error {
	if a.Work == nil {
		return nil
	}
	return a.Work.RecordActivity(ctx, sc, itemID, "item.run_started", actorUserID, map[string]any{
		"run_id":      runID,
		"template_id": templateID,
		"actor":       actorUserID,
	})
}
