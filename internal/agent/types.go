// Package agent implements the agent registry, chat threads and the job
// runtime described in docs/mvp-design.md §6.
//
// The runtime has three seams:
//
//   - Store: tenant-scoped persistence for agents, threads, messages, jobs and
//     their append-only logs. Every method takes a tenancy.Scope.
//   - Adapter: runs one job and emits events. CLI and API adapters implement
//     the same interface, so the job model is uniform (FR-AG-4).
//   - Worker: claims queued jobs from Postgres, enforces the global and
//     per-agent concurrency limits, and turns adapter events into rows.
//
// The cross-tenant claim is the one operation that cannot be tenant-scoped: a
// background worker does not have a session. It goes through a narrowly scoped
// SECURITY DEFINER function (worker_claim_job) owned by aic_auth, exactly like
// the identity functions in 0002 — there is still no privileged connection.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Role is the agent's job family (FR-AG-1).
type Role string

const (
	RolePM         Role = "pm"
	RoleDev        Role = "dev"
	RoleReviewer   Role = "reviewer"
	RoleQA         Role = "qa"
	RoleResearcher Role = "researcher"
)

// Valid reports whether r is one of the roles the design names.
func (r Role) Valid() bool {
	switch r {
	case RolePM, RoleDev, RoleReviewer, RoleQA, RoleResearcher:
		return true
	}
	return false
}

// Kind is the connector family.
type Kind string

const (
	KindCLI Kind = "cli"
	KindAPI Kind = "api"
)

// Valid reports whether k is a supported connector kind.
func (k Kind) Valid() bool { return k == KindCLI || k == KindAPI }

// JobState is the lifecycle of one agent job (FR-AG-4).
type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

// Terminal reports whether no further transition is allowed.
func (s JobState) Terminal() bool {
	return s == JobSucceeded || s == JobFailed || s == JobCancelled
}

// Context kinds a thread can be bound to (FR-CH-1). M2 implements item,
// product and free threads; run and checkpoint threads arrive with M3.
const (
	ContextItem    = "item"
	ContextProduct = "product"
	ContextFree    = "free"
	ContextRun     = "run"
)

// Author kinds for messages.
const (
	AuthorHuman  = "human"
	AuthorAgent  = "agent"
	AuthorSystem = "system"
)

// Agent is a tenant-scoped agent registry row.
//
// The agent is the WHAT: persona, role, skills, capabilities. The transport —
// a CLI binary or an API model — moved to the runner registry
// (internal/runner), and an agent names an ordered chain of runners.
//
// Kind, Config and Fallbacks are the LEGACY transport columns. They stay in
// the schema for this release (nothing reads them any more; a later
// migration drops them) and are no longer part of the API's agent shape,
// which is why they are not serialised.
type Agent struct {
	ID               string          `json:"id"`
	TenantID         string          `json:"tenant_id"`
	Name             string          `json:"name"`
	Role             Role            `json:"role"`
	Kind             Kind            `json:"-"`
	Config           json.RawMessage `json:"-"`
	Capabilities     json.RawMessage `json:"capabilities"`
	ConcurrencyLimit int             `json:"concurrency_limit"`
	Enabled          bool            `json:"enabled"`
	SoulMD           string          `json:"soul_md"`
	SkillsPolicy     json.RawMessage `json:"skills_policy"`
	Fallbacks        json.RawMessage `json:"-"`
	CreatedBy        *string         `json:"created_by"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// Thread is a conversation bound to an item, a product, or nothing (free).
type Thread struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	ContextKind string    `json:"context_kind"`
	ItemID      *string   `json:"item_id"`
	ProductID   *string   `json:"product_id"`
	RunID       *string   `json:"run_id"`
	Title       string    `json:"title"`
	CreatedBy   *string   `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// Message is one entry in a thread's timeline.
type Message struct {
	ID            string          `json:"id"`
	Seq           int64           `json:"seq"`
	TenantID      string          `json:"tenant_id"`
	ThreadID      string          `json:"thread_id"`
	AuthorKind    string          `json:"author_kind"`
	AuthorUserID  *string         `json:"author_user_id"`
	AuthorAgentID *string         `json:"author_agent_id"`
	BodyMD        string          `json:"body_md"`
	Artifacts     json.RawMessage `json:"artifacts"`
	CreatedAt     time.Time       `json:"created_at"`
}

// Job is one adapter execution. A job may belong to a thread (chat), an item,
// or neither (a bare run); M3 adds step_run_id.
type Job struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	AgentID  string `json:"agent_id"`
	// AgentName, AgentRole, Config and Capabilities are filled in by the worker
	// from the registry row; they are not columns on agent_jobs.
	AgentName    string          `json:"-"`
	AgentRole    Role            `json:"-"`
	Config       json.RawMessage `json:"-"`
	Capabilities json.RawMessage `json:"-"`
	// RunnerID, RunnerKind and RunnerConfig are the SNAPSHOT of the runner
	// the job was queued with: the id of the runner, its transport kind and
	// its config. A queued job never changes when somebody edits the runner,
	// and the worker never has to read the runner to know how to run.
	//
	// They are empty for a LEGACY job — one queued before the agent/runner
	// split. The worker falls back to the agent's own kind and config for
	// those, so nothing that is already in flight breaks.
	RunnerID       *string         `json:"runner_id"`
	RunnerKind     string          `json:"runner_kind"`
	RunnerConfig   json.RawMessage `json:"runner_config"`
	ThreadID       *string         `json:"thread_id"`
	MessageID      *string         `json:"message_id"`
	ItemID         *string         `json:"item_id"`
	StepRunID      *string         `json:"step_run_id"`
	IdempotencyKey *string         `json:"idempotency_key"`
	State          JobState        `json:"state"`
	Attempt        int             `json:"attempt"`
	MaxAttempts    int             `json:"max_attempts"`
	Prompt         string          `json:"-"`
	Context        json.RawMessage `json:"context"`
	Workdir        *string         `json:"workdir"`
	TokensIn       int             `json:"tokens_in"`
	TokensOut      int             `json:"tokens_out"`
	CostUSD        *float64        `json:"cost_usd"`
	Result         json.RawMessage `json:"result"`
	FailureKind    *string         `json:"failure_kind"`
	ErrorText      *string         `json:"error"`
	CreatedBy      *string         `json:"created_by"`
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      *time.Time      `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
}

// LogEntry is one append-only job event, ordered by the global Seq cursor.
// Stream/Level/Kind mirror job_logs; Data carries structured payloads.
type LogEntry struct {
	Seq       int64           `json:"seq"`
	TenantID  string          `json:"-"`
	JobID     string          `json:"job_id"`
	Stream    string          `json:"stream"`
	Level     string          `json:"level"`
	Kind      string          `json:"kind"`
	Message   string          `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"ts"`
}

// Event is what an adapter emits while it runs. The worker turns each event
// into a job_logs row and forwards it to live subscribers.
type Event struct {
	Stream  string          `json:"stream,omitempty"`  // stdout|stderr|engine|adapter
	Level   string          `json:"level,omitempty"`   // debug|info|warn|error
	Kind    string          `json:"kind"`              // output_chunk|tool_call|tool_result|usage|lifecycle|error
	Message string          `json:"message,omitempty"` // human-readable, may be redacted
	Data    json.RawMessage `json:"data,omitempty"`
}

// Result is what an adapter returns once the run finishes.
type Result struct {
	Output    string          `json:"output"`
	TokensIn  int             `json:"tokens_in"`
	TokensOut int             `json:"tokens_out"`
	CostUSD   *float64        `json:"cost_usd,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Capabilities mirrors what a connector can do (FR-AG-2/3).
type Capabilities struct {
	Code      bool `json:"code"`
	Shell     bool `json:"shell"`
	Streaming bool `json:"streaming"`
	Usage     bool `json:"usage"`
}

// EventSink receives events synchronously from an adapter. Implementations must
// not block for long; the worker persists and fans out.
type EventSink interface {
	Emit(Event)
}

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc func(Event)

// Emit implements EventSink.
func (f EventSinkFunc) Emit(e Event) { f(e) }

// Adapter runs one job. Run must respect ctx cancellation and return promptly;
// Cancel is the out-of-band path used by the API, keyed by job id.
type Adapter interface {
	Kind() string
	Capabilities() Capabilities
	Run(ctx context.Context, job Job, sink EventSink) (*Result, error)
	Cancel(jobID string) error
}

// CLIConfig is agents.config_json for kind=cli.
//
// Args are passed verbatim and the prompt is always the final argument; that
// keeps stdin free for the CLI's own use and matches the way claude-code and
// codex take a one-shot prompt. Env is merged over a fixed allowlist — it is
// for the CLI's own knobs, never for platform credentials.
type CLIConfig struct {
	Bin          string            `json:"bin"`
	Args         []string          `json:"args"`
	Env          map[string]string `json:"env,omitempty"`
	OutputFormat string            `json:"output_format,omitempty"` // stream-json|text
	TimeoutSec   int               `json:"timeout_sec,omitempty"`
}

// APIConfig is agents.config_json for kind=api.
//
// Provider is "openai" (also any OpenAI-compatible base URL) or "anthropic".
// KeyEnv names the environment variable holding the key; keys are never stored
// in the database or returned by the API.
type APIConfig struct {
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	BaseURL   string  `json:"base_url,omitempty"`
	KeyEnv    string  `json:"key_env,omitempty"`
	MaxTokens int     `json:"max_tokens,omitempty"`
	Temp      float64 `json:"temperature,omitempty"`
}

// Errors shared by the store, worker and API.
var (
	ErrNotFound     = errors.New("agent: not found")
	ErrConflict     = errors.New("agent: already exists")
	ErrInvalid      = errors.New("agent: invalid value")
	ErrDisabled     = errors.New("agent: disabled")
	ErrQueueFull    = errors.New("agent: too many queued jobs")
	ErrNotRunning   = errors.New("agent: job is not running")
	ErrUnauthorized = errors.New("agent: not permitted")
)
