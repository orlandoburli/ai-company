// Package dispatch starts workflows for eligible work items.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/ranking"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

const defaultInterval = 30 * time.Second

// Tenant identifies a tenant the dispatcher may act for. OwnerUserID is the
// fallback actor when an item has no user assignee.
type Tenant struct {
	Scope       tenancy.Scope
	OwnerUserID string
}

// TenantSource is the cross-tenant enumeration boundary. The stores themselves
// intentionally accept only tenant-scoped operations.
type TenantSource interface {
	DispatchTenants(context.Context) ([]Tenant, error)
}

// CapacitySource reports the shared worker pool and per-agent running jobs.
// Counts must include jobs from all tenants for RunningJobs, because the worker
// pool is global.
type CapacitySource interface {
	RunningJobs(context.Context) (int, error)
	RunningAgentJobs(context.Context, tenancy.Scope, string) (int, error)
}

// ActivityRecorder persists the auditable item.run_started event. It is
// separate from workflow creation because work's activity log is tenant-scoped.
type ActivityRecorder interface {
	RecordRunStarted(context.Context, tenancy.Scope, string, string, string, string) error
}

type Config struct {
	Interval        time.Duration
	GlobalWorkers   int
	Logger          *slog.Logger
	Tenants         TenantSource
	Capacity        CapacitySource
	RankingInterval time.Duration
}

type Deps struct {
	Work      *work.Store
	Workflows *workflow.Store
	Engine    *workflow.Engine
	Agents    *agent.Store
	Rankings  *ranking.Store

	// TenantSource and CapacitySource bridge intentionally private cross-tenant
	// and worker-runtime state to this tenant-scoped dispatcher.
	Tenants  TenantSource
	Capacity CapacitySource
	Activity ActivityRecorder
}

type Dispatcher struct {
	deps Deps
	cfg  Config
}

func New(deps Deps, cfg Config) *Dispatcher {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.GlobalWorkers <= 0 {
		cfg.GlobalWorkers = 2
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Tenants == nil {
		cfg.Tenants = deps.Tenants
	}
	if cfg.Capacity == nil {
		cfg.Capacity = deps.Capacity
	}
	return &Dispatcher{deps: deps, cfg: cfg}
}

type Result struct {
	Started    []string `json:"started"`
	Skipped    []Skip   `json:"skipped"`
	Considered int      `json:"considered"`
}

type Skip struct {
	ItemID string `json:"item_id"`
	Reason string `json:"reason"`
}

func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()
	for {
		if _, err := d.Once(ctx); err != nil && ctx.Err() == nil {
			d.cfg.Logger.Error("dispatch pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) Once(ctx context.Context) (Result, error) {
	var result Result
	if d == nil || d.deps.Work == nil || d.deps.Workflows == nil || d.deps.Engine == nil || d.deps.Agents == nil {
		return result, errors.New("dispatch: missing required store or engine")
	}
	if d.cfg.Tenants == nil {
		return result, errors.New("dispatch: tenant source is required")
	}
	if d.cfg.Capacity == nil {
		return result, errors.New("dispatch: capacity source is required")
	}
	if d.deps.Activity == nil {
		return result, errors.New("dispatch: activity recorder is required")
	}
	tenants, err := d.cfg.Tenants.DispatchTenants(ctx)
	if err != nil {
		return result, fmt.Errorf("list dispatch tenants: %w", err)
	}
	running, err := d.cfg.Capacity.RunningJobs(ctx)
	if err != nil {
		return result, fmt.Errorf("count running jobs: %w", err)
	}
	reservedGlobal := 0
	reservedAgents := map[string]int{}
	startedItems := map[string]bool{}
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		sc := tenant.Scope
		if sc.Role == "" {
			sc.Role = tenancy.RoleOwner
		}
		queue, err := d.deps.Work.Queue(ctx, sc, work.ItemFilter{})
		if err != nil {
			return result, fmt.Errorf("queue tenant %s: %w", sc.TenantID, err)
		}

		// Queue applies all persisted readiness gates before returning entries.
		// Active runs are another eligibility gate; remove them before ordering
		// the remaining candidates.
		candidates := make([]work.QueueEntry, 0, len(queue))
		for _, entry := range queue {
			result.Considered++
			if startedItems[entry.Item.ID] {
				result.Skipped = append(result.Skipped, Skip{ItemID: entry.Item.ID, Reason: "already started in this pass"})
				continue
			}
			runs, err := d.deps.Workflows.ListRuns(ctx, sc, workflow.RunFilter{ItemID: entry.Item.ID, Limit: 500})
			if err != nil {
				return result, fmt.Errorf("list runs for item %s: %w", entry.Item.ID, err)
			}
			if hasActiveRun(runs) {
				result.Skipped = append(result.Skipped, Skip{ItemID: entry.Item.ID, Reason: "active run"})
				continue
			}
			candidates = append(candidates, entry)
		}
		sortQueue(candidates)

		var templates []workflow.TemplateRow
		for _, entry := range candidates {
			item := entry.Item
			if running+reservedGlobal >= d.cfg.GlobalWorkers {
				result.Skipped = append(result.Skipped, Skip{ItemID: item.ID, Reason: "global worker pool is full"})
				break
			}
			if templates == nil {
				templates, err = d.deps.Workflows.ListTemplates(ctx, sc, true)
				if err != nil {
					return result, fmt.Errorf("list templates for tenant %s: %w", sc.TenantID, err)
				}
			}
			tpl, ok := templateFor(templates, item.Kind)
			if !ok {
				result.Skipped = append(result.Skipped, Skip{ItemID: item.ID, Reason: "no template for this kind"})
				continue
			}
			agentID, err := firstAgentID(tpl.GraphJSON)
			if err != nil {
				result.Skipped = append(result.Skipped, Skip{ItemID: item.ID, Reason: "template has no usable first agent node"})
				continue
			}
			agentRow, err := d.deps.Agents.GetAgent(ctx, sc, agentID)
			if err != nil {
				result.Skipped = append(result.Skipped, Skip{ItemID: item.ID, Reason: "template's first agent is unavailable"})
				continue
			}
			if !agentRow.Enabled {
				result.Skipped = append(result.Skipped, Skip{ItemID: item.ID, Reason: "template's first agent is disabled"})
				continue
			}
			used, ok := reservedAgents[agentRow.ID]
			if !ok {
				used, err = d.cfg.Capacity.RunningAgentJobs(ctx, sc, agentRow.ID)
				if err != nil {
					return result, fmt.Errorf("count jobs for agent %s: %w", agentRow.ID, err)
				}
			}
			if used >= agentRow.ConcurrencyLimit {
				result.Skipped = append(result.Skipped, Skip{ItemID: item.ID, Reason: "first agent is at its concurrency limit"})
				continue
			}
			actor := tenant.OwnerUserID
			if actor == "" {
				actor = sc.UserID
			}
			if item.AssigneeUserID != nil && *item.AssigneeUserID != "" {
				actor = *item.AssigneeUserID
			}
			run, err := d.deps.Engine.StartRun(ctx, sc, tpl.ID, item.ID, actor)
			if err != nil {
				result.Skipped = append(result.Skipped, Skip{ItemID: item.ID, Reason: "could not start run: " + err.Error()})
				continue
			}
			if err := d.deps.Activity.RecordRunStarted(ctx, sc, item.ID, actor, tpl.ID, run.ID); err != nil {
				return result, fmt.Errorf("record run-started activity for item %s: %w", item.ID, err)
			}
			result.Started = append(result.Started, item.ID)
			startedItems[item.ID] = true
			reservedGlobal++
			reservedAgents[agentRow.ID] = used + 1
		}
	}
	return result, nil
}

func hasActiveRun(runs []workflow.Run) bool {
	for _, run := range runs {
		switch run.State {
		case workflow.RunCompleted, workflow.RunFailed, workflow.RunCancelled:
			continue
		default:
			return true
		}
	}
	return false
}

func sortQueue(entries []work.QueueEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if (a.StartNowAt != nil) != (b.StartNowAt != nil) {
			return a.StartNowAt != nil
		}
		if a.StartNowAt != nil && !a.StartNowAt.Equal(*b.StartNowAt) {
			return a.StartNowAt.After(*b.StartNowAt)
		}
		if (a.PinnedAt != nil) != (b.PinnedAt != nil) {
			return a.PinnedAt != nil
		}
		if a.PinnedAt != nil && !a.PinnedAt.Equal(*b.PinnedAt) {
			return a.PinnedAt.After(*b.PinnedAt)
		}
		if a.Rank != nil && b.Rank != nil && *a.Rank != *b.Rank {
			return *a.Rank < *b.Rank
		}
		if (a.Rank != nil) != (b.Rank != nil) {
			return a.Rank != nil
		}
		if a.Score.Value != b.Score.Value {
			return a.Score.Value > b.Score.Value
		}
		if a.Item.Number != b.Item.Number {
			return a.Item.Number < b.Item.Number
		}
		return a.Item.ID < b.Item.ID
	})
}

func templateFor(templates []workflow.TemplateRow, kind string) (workflow.TemplateRow, bool) {
	var defaultTemplate *workflow.TemplateRow
	for i := range templates {
		if templates[i].PublishedAt == nil {
			continue
		}
		if templates[i].AppliesTo == kind {
			return templates[i], true
		}
		if templates[i].AppliesTo == "" && defaultTemplate == nil {
			defaultTemplate = &templates[i]
		}
	}
	if defaultTemplate != nil {
		return *defaultTemplate, true
	}
	return workflow.TemplateRow{}, false
}

func firstAgentID(raw json.RawMessage) (string, error) {
	var graph workflow.Template
	if err := json.Unmarshal(raw, &graph); err != nil {
		return "", err
	}
	for _, node := range graph.Nodes {
		if node.Kind != workflow.NodeAgent {
			continue
		}
		var config struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(node.Config, &config); err != nil {
			return "", err
		}
		if config.AgentID == "" {
			return "", errors.New("agent_id is empty")
		}
		return config.AgentID, nil
	}
	return "", errors.New("no agent node")
}
