package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Worker claims queued jobs from Postgres and runs them. The queue is the
// database, not a channel: a crash leaves queued rows behind and a lease
// sweeper puts orphaned running rows back. In-process state only mirrors the
// concurrency limits (global workers + per-agent semaphore).
type Worker struct {
	store    *Store
	hub      *Hub
	adapters map[string]Adapter
	log      *slog.Logger
	workerID string

	workers int
	lease   time.Duration
	poll    time.Duration
	reap    time.Duration

	slots chan struct{}

	mu      sync.Mutex
	active  map[string]int // agent id -> running jobs
	limits  map[string]int // agent id -> concurrency_limit (filled when a job loads)
	cancels map[string]context.CancelFunc
}

// WorkerOption tunes the worker; the defaults are the design's MVP values.
type WorkerOption func(*Worker)

// WithWorkerCount sets the global concurrency (default 2).
func WithWorkerCount(n int) WorkerOption {
	return func(w *Worker) {
		if n > 0 {
			w.workers = n
		}
	}
}

// WithWorkerLease sets the job lease (default 2 minutes).
func WithWorkerLease(d time.Duration) WorkerOption {
	return func(w *Worker) {
		if d > 0 {
			w.lease = d
		}
	}
}

// WithWorkerPoll sets the queue poll interval (default 500ms).
func WithWorkerPoll(d time.Duration) WorkerOption {
	return func(w *Worker) {
		if d > 0 {
			w.poll = d
		}
	}
}

// WithWorkerLogger sets the logger.
func WithWorkerLogger(l *slog.Logger) WorkerOption {
	return func(w *Worker) {
		if l != nil {
			w.log = l
		}
	}
}

// NewWorker builds a worker over the store and adapters.
func NewWorker(store *Store, hub *Hub, adapters []Adapter, opts ...WorkerOption) *Worker {
	w := &Worker{
		store:    store,
		hub:      hub,
		adapters: make(map[string]Adapter, len(adapters)),
		log:      slog.Default(),
		workerID: fmt.Sprintf("worker-%d", time.Now().UnixNano()),
		workers:  2,
		lease:    2 * time.Minute,
		poll:     500 * time.Millisecond,
		reap:     30 * time.Second,
		slots:    make(chan struct{}, 2),
		active:   make(map[string]int),
		limits:   make(map[string]int),
		cancels:  make(map[string]context.CancelFunc),
	}
	for _, a := range adapters {
		if a != nil {
			w.adapters[a.Kind()] = a
		}
	}
	for _, opt := range opts {
		opt(w)
	}
	w.slots = make(chan struct{}, w.workers)
	return w
}

// Start runs the worker in the background until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) { go w.Run(ctx) }

// Run blocks, claiming and running jobs until ctx is cancelled. On shutdown,
// in-flight jobs are requeued by their goroutines so a restart picks them up.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("agent worker started", "worker", w.workerID, "workers", w.workers)
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	lastReap := time.Time{}
	for {
		select {
		case <-ctx.Done():
			w.log.Info("agent worker stopping", "worker", w.workerID)
			return
		case <-ticker.C:
		}

		if time.Since(lastReap) >= w.reap {
			lastReap = time.Now()
			if n, err := w.store.ReapJobs(ctx, 3); err != nil {
				if ctx.Err() == nil {
					w.log.Warn("reap expired jobs", "error", err)
				}
			} else if n > 0 {
				w.log.Warn("requeued jobs with expired leases", "count", n)
			}
		}

		w.dispatch(ctx)
	}
}

// dispatch fills the free global slots with claimed jobs. It stops as soon as
// the queue is empty or no slot is free.
func (w *Worker) dispatch(ctx context.Context) {
	for {
		select {
		case w.slots <- struct{}{}:
		case <-ctx.Done():
			return
		default:
			return
		}

		skip := w.saturatedAgents()
		tenantID, jobID, err := w.store.ClaimNextJob(ctx, w.workerID, w.lease, skip)
		if err != nil {
			<-w.slots
			if !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
				w.log.Warn("claim job", "error", err)
			}
			return
		}
		go func() {
			defer func() { <-w.slots }()
			w.runOne(ctx, tenantID, jobID)
		}()
	}
}

func (w *Worker) saturatedAgents() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for id, n := range w.active {
		if lim, ok := w.limits[id]; ok && n >= lim {
			out = append(out, id)
		}
	}
	return out
}

func (w *Worker) runOne(ctx context.Context, tenantID, jobID string) {
	sc := tenancy.Scope{TenantID: tenantID, Role: tenancy.RoleMember}

	job, err := w.store.GetJob(ctx, sc, jobID)
	if err != nil {
		w.log.Warn("load claimed job", "job", jobID, "error", err)
		_ = w.store.FinishJob(ctx, sc, jobID, w.workerID, JobFailed, nil, FailureCapability, "job disappeared before it ran")
		return
	}

	agentRow, err := w.store.GetAgent(ctx, sc, job.AgentID)
	if err != nil || !agentRow.Enabled {
		msg := "agent is not available"
		if errors.Is(err, ErrNotFound) {
			msg = "agent no longer exists"
		}
		_ = w.store.FinishJob(ctx, sc, jobID, w.workerID, JobFailed, nil, FailureCapability, msg)
		w.publishState(sc, jobID)
		return
	}

	// Re-check concurrency now that the limit is known. A claim may have raced
	// past the in-memory skip list; requeueing for a second is cheaper than
	// running a third job for one agent.
	w.mu.Lock()
	w.limits[agentRow.ID] = agentRow.ConcurrencyLimit
	if w.active[agentRow.ID] >= agentRow.ConcurrencyLimit {
		w.mu.Unlock()
		_ = w.store.RequeueJob(ctx, sc, jobID, w.workerID, time.Second, FailureTransient, "agent at concurrency limit")
		w.publishState(sc, jobID)
		return
	}
	w.active[agentRow.ID]++
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.active[agentRow.ID]--
		w.mu.Unlock()
	}()

	adapterID := string(agentRow.Kind)
	adapter := w.adapters[adapterID]
	if adapter == nil {
		_ = w.store.FinishJob(ctx, sc, jobID, w.workerID, JobFailed, nil, FailureCapability, "no adapter registered for kind "+adapterID)
		w.publishState(sc, jobID)
		return
	}

	job.AgentName = agentRow.Name
	job.AgentRole = agentRow.Role
	job.Config = agentRow.Config
	job.Capabilities = agentRow.Capabilities

	jobCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancels[job.ID] = cancel
	w.mu.Unlock()
	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.cancels, job.ID)
		w.mu.Unlock()
	}()

	hbDone := make(chan struct{})
	go w.heartbeat(ctx, sc, job.ID, hbDone)
	defer close(hbDone)

	sink := EventSinkFunc(func(ev Event) {
		if ev.Kind == "" {
			ev.Kind = "output_chunk"
		}
		entry, err := w.store.AppendLog(ctx, sc, job.ID, ev)
		if err != nil {
			if ctx.Err() == nil {
				w.log.Warn("append job log", "job", job.ID, "error", err)
			}
			return
		}
		w.hub.Publish(StreamEvent{Type: "log", Log: &entry})
	})

	res, runErr := adapter.Run(jobCtx, job, sink)

	// The job may have been cancelled through the API while the adapter was
	// shutting down. Terminal state wins; never resurrect it.
	if cur, err := w.store.GetJob(ctx, sc, job.ID); err == nil && cur.State == JobCancelled {
		w.publishState(sc, job.ID)
		return
	}

	if runErr != nil {
		kind := FailureKind(runErr)
		text := truncateErr(runErr.Error())

		if errors.Is(runErr, context.Canceled) && ctx.Err() != nil {
			// Server shutdown, not a failure: hand the job back to the queue.
			_ = w.store.RequeueJob(context.Background(), sc, job.ID, w.workerID, 0, FailureTransient, "worker stopped")
			w.publishState(sc, job.ID)
			return
		}

		if kind == FailureTransient && job.Attempt < job.MaxAttempts {
			delay := backoff(job.Attempt)
			_, _ = w.store.AppendLog(ctx, sc, job.ID, Event{Stream: "engine", Level: "warn", Kind: "error", Message: fmt.Sprintf("transient failure, retrying in %s: %s", delay, text)})
			if err := w.store.RequeueJob(ctx, sc, job.ID, w.workerID, delay, kind, text); err != nil {
				w.log.Warn("requeue job", "job", job.ID, "error", err)
			}
			w.publishState(sc, job.ID)
			return
		}

		_, _ = w.store.AppendLog(ctx, sc, job.ID, Event{Stream: "engine", Level: "error", Kind: "error", Message: text})
		// The failure notice and the terminal state land together, so a reader
		// that sees "failed" always finds the explanation next to it.
		notice := w.systemMessage(sc, job, "Agent run failed: "+text)
		if err := w.store.FinishJobWithMessage(ctx, sc, job.ID, w.workerID, JobFailed, nil, notice, kind, text); err != nil {
			w.log.Warn("finish failed job", "job", job.ID, "error", err)
		}
		w.publishState(sc, job.ID)
		return
	}

	if res == nil {
		res = &Result{}
	}
	// The reply and the terminal state land together: a reader that sees
	// "succeeded" always finds the answer next to it, and a job cancelled
	// meanwhile never gets a reply (the transaction rolls back with it).
	var reply *NewMessage
	if job.ThreadID != nil {
		out := strings.TrimSpace(res.Output)
		if out == "" {
			out = "_The agent finished without producing a reply._"
		}
		reply = &NewMessage{
			ThreadID:      *job.ThreadID,
			AuthorKind:    AuthorAgent,
			AuthorAgentID: agentRow.ID,
			BodyMD:        out,
		}
	}
	if err := w.store.FinishJobWithMessage(ctx, sc, job.ID, w.workerID, JobSucceeded, res, reply, "", ""); err != nil {
		if !errors.Is(err, ErrNotRunning) {
			w.log.Warn("finish job", "job", job.ID, "error", err)
		}
		w.publishState(sc, job.ID)
		return
	}
	w.publishState(sc, job.ID)
}

func (w *Worker) heartbeat(ctx context.Context, sc tenancy.Scope, jobID string, done <-chan struct{}) {
	interval := w.lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.store.HeartbeatJob(ctx, sc, jobID, w.workerID, w.lease); err != nil && !errors.Is(err, ErrNotRunning) && ctx.Err() == nil {
				w.log.Warn("heartbeat job", "job", jobID, "error", err)
			}
		}
	}
}

// systemMessage builds the system notice for a job, or nil when the job has
// no thread to post into.
func (w *Worker) systemMessage(sc tenancy.Scope, job Job, body string) *NewMessage {
	if job.ThreadID == nil {
		return nil
	}
	return &NewMessage{
		ThreadID:   *job.ThreadID,
		AuthorKind: AuthorSystem,
		BodyMD:     body,
	}
}

func (w *Worker) postSystemMessage(sc tenancy.Scope, job Job, body string) {
	if job.ThreadID == nil {
		return
	}
	if _, err := w.store.AddMessage(context.Background(), sc, NewMessage{
		ThreadID:   *job.ThreadID,
		AuthorKind: AuthorSystem,
		BodyMD:     truncateErr(body),
	}); err != nil {
		w.log.Warn("post system message", "job", job.ID, "error", err)
	}
}

func (w *Worker) publishState(sc tenancy.Scope, jobID string) {
	job, err := w.store.GetJob(context.Background(), sc, jobID)
	if err != nil {
		return
	}
	w.hub.Publish(StreamEvent{Type: "state", Job: &job})
}

// Cancel stops a running adapter. The job row was already moved to cancelled by
// the API; Cancel only interrupts the process/request.
func (w *Worker) Cancel(jobID string) {
	w.mu.Lock()
	c := w.cancels[jobID]
	w.mu.Unlock()
	if c != nil {
		c()
	}
}

func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := 10 * time.Second
	for i := 1; i < attempt && d < 5*time.Minute; i++ {
		d *= 2
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

func truncateErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 4000 {
		return s
	}
	return s[:4000] + "…"
}
