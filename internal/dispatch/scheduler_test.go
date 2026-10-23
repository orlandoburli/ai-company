package dispatch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/ranking"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type schedulerTestRunner struct {
	mu       sync.Mutex
	calls    int
	cancel   context.CancelFunc
	firstErr bool
}

func (r *schedulerTestRunner) Run(_ context.Context, _ tenancy.Scope) (ranking.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.firstErr && r.calls == 1 {
		return ranking.Run{}, errors.New("expected test ranking failure")
	}
	if r.calls >= 4 {
		r.cancel()
	}
	return ranking.Run{State: "ready"}, nil
}

func TestSchedulerRunsAtStartupAndContinuesAfterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &schedulerTestRunner{cancel: cancel, firstErr: true}
	var logBuffer bytes.Buffer
	s := NewScheduler(nil, Config{
		Tenants: testTenants{
			{Scope: tenancy.Scope{TenantID: "T1"}},
			{Scope: tenancy.Scope{TenantID: "T2"}},
		},
		Logger: slog.New(slog.NewTextHandler(&logBuffer, nil)), RankingInterval: time.Millisecond,
	})
	s.runner = runner
	s.Run(ctx)
	if runner.calls != 4 {
		t.Fatalf("runner calls=%d, want 4 (two tenants at startup and next interval)", runner.calls)
	}
	if !bytes.Contains(logBuffer.Bytes(), []byte("ranking run failed")) {
		t.Fatalf("failure was not logged: %s", logBuffer.String())
	}
}
