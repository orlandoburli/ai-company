package dispatch

import (
	"context"
	"log/slog"
	"time"

	"github.com/orlandoburli/ai-company/internal/ranking"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type rankingRunner interface {
	Run(context.Context, tenancy.Scope) (ranking.Run, error)
}

// Scheduler runs one ranking pass at startup and then at the configured hourly
// interval (one hour by default).
type Scheduler struct {
	runner   rankingRunner
	tenants  TenantSource
	logger   *slog.Logger
	interval time.Duration
}

func NewScheduler(runner *ranking.Runner, cfg Config) *Scheduler {
	interval := cfg.RankingInterval
	if interval <= 0 {
		interval = time.Hour
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{runner: runner, tenants: cfg.Tenants, logger: logger, interval: interval}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		s.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) once(ctx context.Context) {
	if s.runner == nil {
		s.logger.Error("ranking scheduler has no runner")
		return
	}
	if s.tenants == nil {
		s.logger.Error("ranking scheduler has no tenant source")
		return
	}
	tenants, err := s.tenants.DispatchTenants(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("list tenants for ranking", "error", err)
		}
		return
	}
	for _, tenant := range tenants {
		if ctx.Err() != nil {
			return
		}
		scope := tenant.Scope
		if scope.Role == "" {
			scope.Role = tenancy.RoleOwner
		}
		if _, err := s.runner.Run(ctx, scope); err != nil && ctx.Err() == nil {
			s.logger.Error("ranking run failed", "tenant_id", scope.TenantID, "error", err)
		}
	}
}
