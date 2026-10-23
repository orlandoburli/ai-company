// Package runner is the registry of runners — the HOW of an agent's
// execution.
//
// The agent registry splits in two:
//
//   - the agent is the WHAT: persona, role, skills, capabilities;
//   - the runner is the transport: a CLI binary or an API model, with the
//     config the adapter needs to run it.
//
// An agent names an ordered chain of runners (the fallback order the runtime
// walks when a runner fails) and the job carries a snapshot of the runner it
// was queued with, so a queued job never changes under the operator's feet.
//
// The package owns two tables: runners (the transport) and agent_runners (the
// chain). Every method is tenant-scoped with the house withTenant pattern —
// the transaction sets app.tenant_id and both tables carry FORCE ROW LEVEL
// SECURITY, so a forgotten tenant predicate is an empty result rather than a
// leak.
package runner

import (
	"encoding/json"
	"errors"
	"time"
)

// Kind is the transport family. It matches the adapter kinds in
// internal/agent/adapters: the runner's kind picks the adapter that runs it.
type Kind string

const (
	KindCLI Kind = "cli"
	KindAPI Kind = "api"
)

// Valid reports whether k is a supported transport kind.
func (k Kind) Valid() bool { return k == KindCLI || k == KindAPI }

// Runner is one transport: how a job is actually run.
type Runner struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenant_id"`
	Name          string          `json:"name"`
	Kind          Kind            `json:"kind"`
	Config        json.RawMessage `json:"config"`
	CredentialRef string          `json:"credential_ref"`
	// BudgetTokens and BudgetCostUSD are the runner's spend limits. Zero
	// means "no budget recorded", which the runtime treats as unlimited.
	BudgetTokens  int64     `json:"budget_tokens"`
	BudgetCostUSD float64   `json:"budget_cost_usd"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// NewRunner is the input for Create. A nil Enabled means enabled.
type NewRunner struct {
	Name          string
	Kind          Kind
	Config        json.RawMessage
	CredentialRef string
	BudgetTokens  int64
	BudgetCostUSD float64
	Enabled       *bool
}

// Patch is a partial update of a runner; a nil field is unchanged. The JSON
// config is a pointer so "not sent" and "{}" stay distinct.
type Patch struct {
	Name          *string
	Kind          *Kind
	Config        *json.RawMessage
	CredentialRef *string
	BudgetTokens  *int64
	BudgetCostUSD *float64
	Enabled       *bool
}

// Ref identifies a runner inside an agent's chain: the id and the name, in
// the chain's order.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CLIConfig is a runner's config_json for kind=cli.
//
// It mirrors agent.CLIConfig field for field — the CLI adapter decodes the
// same JSON — so a runner's config is exactly what the adapter expects.
// Args are passed verbatim and the prompt is always the final argument; Env is
// merged over a fixed allowlist and is for the CLI's own knobs, never for
// platform credentials.
type CLIConfig struct {
	Bin          string            `json:"bin"`
	Args         []string          `json:"args"`
	Env          map[string]string `json:"env,omitempty"`
	OutputFormat string            `json:"output_format,omitempty"` // stream-json|text
	TimeoutSec   int               `json:"timeout_sec,omitempty"`
}

// APIConfig is a runner's config_json for kind=api.
//
// It mirrors agent.APIConfig. Provider is "openai" (or any OpenAI-compatible
// base URL) or "anthropic". KeyEnv names the environment variable holding the
// key; keys are never stored in the database and never returned by the API.
type APIConfig struct {
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	BaseURL   string  `json:"base_url,omitempty"`
	KeyEnv    string  `json:"key_env,omitempty"`
	MaxTokens int     `json:"max_tokens,omitempty"`
	Temp      float64 `json:"temperature,omitempty"`
}

// Errors shared by the store and the API.
var (
	// ErrNotFound is returned for a runner (or an agent) that does not exist
	// in this tenant — including one that belongs to another tenant.
	ErrNotFound = errors.New("runner: not found")
	// ErrConflict is returned when the tenant already has a runner with that
	// name.
	ErrConflict = errors.New("runner: already exists")
	// ErrInvalid is returned for a malformed name, config or budget.
	ErrInvalid = errors.New("runner: invalid value")
	// ErrRunnerInUse is returned when a runner that is bound to an agent is
	// deleted. The delete is refused rather than unbinding the agent: a
	// runner that vanishes under a live agent breaks its execution silently.
	ErrRunnerInUse = errors.New("runner: bound to an agent")
)
