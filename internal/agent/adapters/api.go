package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/orlandoburli/ai-company/internal/agent"
)

// Default model endpoints; APIConfig.BaseURL overrides them for tests and
// for OpenAI-compatible self-hosted servers.
const (
	openAIDefaultBase    = "https://api.openai.com/v1"
	anthropicDefaultBase = "https://api.anthropic.com/v1"
	anthropicVersion     = "2023-06-01"

	// anthropicMaxTokensDefault: anthropic requires max_tokens on every call.
	anthropicMaxTokensDefault = 1024
	respBodyLimit             = 1 << 20 // 1 MiB read of any response body
)

// API runs agent jobs as a single stateless completion call (no tool loop) —
// the pm/research/review path from §6.
type API struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// NewAPI returns an API adapter.
func NewAPI() *API { return &API{cancels: map[string]context.CancelFunc{}} }

// Kind implements agent.Adapter.
func (a *API) Kind() string { return string(agent.KindAPI) }

// Capabilities implements agent.Adapter. One completion call: no code, no
// shell, no streaming; usage comes back from the provider.
func (a *API) Capabilities() agent.Capabilities {
	return agent.Capabilities{Streaming: false, Usage: true}
}

func (a *API) register(jobID string, cancel context.CancelFunc) {
	a.mu.Lock()
	a.cancels[jobID] = cancel
	a.mu.Unlock()
}

func (a *API) deregister(jobID string) {
	a.mu.Lock()
	delete(a.cancels, jobID)
	a.mu.Unlock()
}

// Cancel cancels the in-flight HTTP request for jobID. Unknown job IDs and
// finished runs are no-ops (idempotent).
func (a *API) Cancel(jobID string) error {
	a.mu.Lock()
	cancel, ok := a.cancels[jobID]
	a.mu.Unlock()
	if ok {
		cancel()
	}
	return nil
}

// Run executes one job: a single completion call, then done.
func (a *API) Run(ctx context.Context, job agent.Job, sink agent.EventSink) (*agent.Result, error) {
	invalid := func(format string, a ...any) error {
		return classify(agent.FailurePermanent,
			fmt.Errorf("%w: api adapter: %s", agent.ErrInvalid, fmt.Sprintf(format, a...)))
	}
	var cfg agent.APIConfig
	if len(job.Config) == 0 {
		return nil, invalid("missing config")
	}
	if err := json.Unmarshal(job.Config, &cfg); err != nil {
		return nil, classify(agent.FailurePermanent,
			fmt.Errorf("%w: api adapter: bad config: %v", agent.ErrInvalid, err))
	}
	if cfg.Provider != "openai" && cfg.Provider != "anthropic" {
		return nil, invalid("provider must be openai or anthropic, got %q", cfg.Provider)
	}
	if cfg.Model == "" {
		return nil, invalid("model is required")
	}
	if cfg.KeyEnv == "" {
		return nil, invalid("key_env is required")
	}
	key := os.Getenv(cfg.KeyEnv)
	if key == "" {
		// Non-secret by construction: the name, never the value — keys are
		// never stored, logged or returned (m2-contract.md §Agents).
		return nil, classify(agent.FailurePermanent,
			fmt.Errorf("api agent %s: environment variable %s is not set (config.key_env)",
				job.ID, cfg.KeyEnv))
	}

	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if base == "" {
		if cfg.Provider == "openai" {
			base = openAIDefaultBase
		} else {
			base = anthropicDefaultBase
		}
	}
	path := "/chat/completions"
	if cfg.Provider == "anthropic" {
		path = "/messages"
	}

	body, err := json.Marshal(buildPayload(cfg, job.Prompt))
	if err != nil {
		return nil, classify(agent.FailurePermanent, fmt.Errorf("api agent %s: payload: %w", job.ID, err))
	}

	callCtx, cancel := context.WithCancel(ctx)
	a.register(job.ID, cancel)
	defer a.deregister(job.ID)

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, classify(agent.FailurePermanent, fmt.Errorf("api agent %s: request: %w", job.ID, err))
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Provider == "openai" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", anthropicVersion)
	}

	// No client-level Timeout: the run ctx (worker lease/cancel) is the
	// deadline; a call that hangs until Cancel is exactly the contract.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		// Cancellation/deadline from above wins; both classify as transient.
		if cerr := callCtx.Err(); cerr != nil {
			return nil, classify(agent.FailureTransient,
				fmt.Errorf("api agent %s: provider %s: %w", job.ID, cfg.Provider, cerr))
		}
		return nil, classify(agent.FailureTransient, fmt.Errorf("api agent %s: %w", job.ID, err))
	}
	defer resp.Body.Close() //nolint:errcheck // close of a response body

	raw, err := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if err != nil {
		return nil, classify(agent.FailureTransient,
			fmt.Errorf("api agent %s: read response: %w", job.ID, err))
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{
			Kind:   httpFailureKind(resp.StatusCode),
			Status: resp.StatusCode,
			Body:   redactBody(raw),
		}
	}

	text, tin, tout, perr := parseResponse(cfg.Provider, raw)
	if perr != nil {
		// Truncated or unexpected 200 responses from the provider are a
		// provider-side hiccup, not a caller mistake: transient.
		return nil, &APIError{Kind: agent.FailureTransient, Status: resp.StatusCode,
			Body: redactBody(raw), err: fmt.Errorf("api agent %s: %w", job.ID, perr)}
	}

	sink.Emit(agent.Event{Stream: "engine", Level: "info", Kind: "output_chunk", Message: Redact(text)})
	usage, _ := json.Marshal(agent.Result{TokensIn: tin, TokensOut: tout})
	sink.Emit(agent.Event{Stream: "engine", Level: "info", Kind: "usage", Data: usage})

	return &agent.Result{Output: text, TokensIn: tin, TokensOut: tout}, nil
}

func buildPayload(cfg agent.APIConfig, prompt string) map[string]any {
	msgs := []map[string]string{{"role": "user", "content": prompt}}
	if cfg.Provider == "openai" {
		p := map[string]any{"model": cfg.Model, "messages": msgs}
		if cfg.MaxTokens > 0 {
			p["max_tokens"] = cfg.MaxTokens
		}
		if cfg.Temp > 0 {
			p["temperature"] = cfg.Temp
		}
		return p
	}
	mt := cfg.MaxTokens
	if mt <= 0 {
		mt = anthropicMaxTokensDefault
	}
	p := map[string]any{"model": cfg.Model, "max_tokens": mt, "messages": msgs}
	if cfg.Temp > 0 {
		p["temperature"] = cfg.Temp
	}
	return p
}

func parseResponse(provider string, raw []byte) (text string, in, out int, err error) {
	if provider == "openai" {
		var r struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", 0, 0, fmt.Errorf("parse openai response: %w", err)
		}
		if len(r.Choices) == 0 {
			return "", 0, 0, errors.New("openai response has no choices")
		}
		return r.Choices[0].Message.Content, r.Usage.PromptTokens, r.Usage.CompletionTokens, nil
	}
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", 0, 0, fmt.Errorf("parse anthropic response: %w", err)
	}
	var sb strings.Builder
	for _, b := range r.Content {
		if b.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		}
	}
	return sb.String(), r.Usage.InputTokens, r.Usage.OutputTokens, nil
}

// APIError is the structured failure an API call produces. It implements
// agent.Classified, so agent.FailureKind (the worker's entry point) reads the
// kind straight off it.
type APIError struct {
	Kind   string // rate_limited | auth | transient | capability | permanent
	Status int    // HTTP status, 0 for non-HTTP failures
	Body   string // redacted, truncated response body — request headers are never included
	err    error
}

// FailureKind implements agent.Classified.
func (e *APIError) FailureKind() string { return e.Kind }

// Error implements error.
func (e *APIError) Error() string {
	b := e.Body
	if b == "" {
		b = "(empty body)"
	}
	msg := "api agent: provider call failed"
	if e.Status != 0 {
		msg = fmt.Sprintf("api agent: http %d", e.Status)
	}
	if e.err != nil {
		return fmt.Sprintf("%s: %s: %s", msg, e.err, b)
	}
	return msg + ": " + b
}

// Unwrap exposes the underlying cause (e.g. context.DeadlineExceeded).
func (e *APIError) Unwrap() error { return e.err }

func httpFailureKind(status int) string {
	switch {
	case status == 429:
		return agent.FailureRateLimit
	case status == 401 || status == 403:
		return agent.FailureAuth
	case status >= 500:
		return agent.FailureTransient
	case status == 400 || status == 404 || status == 422:
		return agent.FailureCapability
	default:
		return agent.FailurePermanent
	}
}

// FailureKind classifies an adapter error for the worker's retry/failure
// policy. The worker should use agent.FailureKind(err) instead — agent errors
// carry the kind via agent.Classified, and this helper only ships the extra
// net/EOF heuristics CLI tests find convenient.
func FailureKind(err error) string {
	if k := agent.FailureKind(err); k != agent.FailurePermanent {
		return k
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return agent.FailureTransient
	}
	return agent.FailurePermanent
}

// redactBody truncates a response body for inclusion in error text.
func redactBody(raw []byte) string {
	const max = 2 << 10
	s := redactTail(strings.ReplaceAll(string(raw), "\n", " "))
	if len(s) > max {
		return s[:max] + "…[truncated]"
	}
	return s
}
