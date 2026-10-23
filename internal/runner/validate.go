package runner

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// namePattern keeps runner names mentionable-shaped: lowercase alphanumerics
// with dashes or inner underscores, at most 64 characters — the same shape the
// agents' names use, so a runner and an agent can be named alike.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// envNameRE matches an environment variable name. A runner never holds a
// secret: `key_env` names the variable that holds the key, and the CLI's env
// block is for the CLI's own knobs.
var envNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// maxConfigBytes caps the transport config, so a runner row cannot hold a
// payload that is really a file.
const maxConfigBytes = 32 << 10

// ValidateName checks a runner's name.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: name %q must match a-z0-9 with - or _ (max 64)", ErrInvalid, name)
	}
	return nil
}

// ValidateConfig checks a transport config against its kind. The rules are the
// agent's own config rules, moved onto the transport with it:
//
//   - a cli runner needs a binary to run;
//   - an api runner needs a provider and a model;
//   - key_env names an environment variable, never a secret.
//
// The config is opaque to the store — it is stored as JSON — but it is not
// opaque to the runner's own kind, so a config that the adapter could never
// run is refused at the boundary rather than at execution time.
func ValidateConfig(kind Kind, config json.RawMessage) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, kind)
	}
	if len(config) > maxConfigBytes {
		return fmt.Errorf("%w: config is over %d bytes", ErrInvalid, maxConfigBytes)
	}
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	var obj map[string]any
	if err := json.Unmarshal(config, &obj); err != nil {
		return fmt.Errorf("%w: config must be a JSON object", ErrInvalid)
	}
	if obj == nil {
		return fmt.Errorf("%w: config must be a JSON object", ErrInvalid)
	}

	switch kind {
	case KindCLI:
		var cfg CLIConfig
		if err := strictUnmarshal(config, &cfg); err != nil {
			return fmt.Errorf("%w: config: %v", ErrInvalid, err)
		}
		if strings.TrimSpace(cfg.Bin) == "" {
			return fmt.Errorf("%w: config.bin is required for a cli runner", ErrInvalid)
		}
		if strings.ContainsRune(cfg.Bin, 0) {
			return fmt.Errorf("%w: config.bin contains an invalid character", ErrInvalid)
		}
		for _, a := range cfg.Args {
			if strings.ContainsRune(a, 0) {
				return fmt.Errorf("%w: config.args contains an invalid character", ErrInvalid)
			}
		}
		if cfg.TimeoutSec < 0 || cfg.TimeoutSec > 3600 {
			return fmt.Errorf("%w: config.timeout_sec must be between 0 and 3600", ErrInvalid)
		}
		switch cfg.OutputFormat {
		case "", "stream-json", "text":
		default:
			return fmt.Errorf("%w: config.output_format must be \"stream-json\" or \"text\"", ErrInvalid)
		}
		for k := range cfg.Env {
			if !envNameRE.MatchString(k) {
				return fmt.Errorf("%w: config.env key %q is not a valid environment variable name", ErrInvalid, k)
			}
		}
	case KindAPI:
		var cfg APIConfig
		if err := strictUnmarshal(config, &cfg); err != nil {
			return fmt.Errorf("%w: config: %v", ErrInvalid, err)
		}
		if cfg.Provider != "openai" && cfg.Provider != "anthropic" {
			return fmt.Errorf("%w: config.provider must be \"openai\" or \"anthropic\"", ErrInvalid)
		}
		if strings.TrimSpace(cfg.Model) == "" {
			return fmt.Errorf("%w: config.model is required for an api runner", ErrInvalid)
		}
		if !envNameRE.MatchString(cfg.KeyEnv) {
			return fmt.Errorf("%w: config.key_env must name an environment variable (e.g. OPENAI_API_KEY)", ErrInvalid)
		}
		if cfg.BaseURL != "" {
			u, err := url.Parse(cfg.BaseURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("%w: config.base_url must be an http(s) URL", ErrInvalid)
			}
		}
		if cfg.MaxTokens < 0 || cfg.MaxTokens > 200000 {
			return fmt.Errorf("%w: config.max_tokens must be between 0 and 200000", ErrInvalid)
		}
	}
	return nil
}

// validateNew checks a whole runner definition.
func validateNew(in NewRunner) error {
	if err := ValidateName(in.Name); err != nil {
		return err
	}
	if err := ValidateConfig(in.Kind, in.Config); err != nil {
		return err
	}
	if in.BudgetTokens < 0 {
		return fmt.Errorf("%w: budget_tokens must not be negative", ErrInvalid)
	}
	if in.BudgetCostUSD < 0 {
		return fmt.Errorf("%w: budget_cost_usd must not be negative", ErrInvalid)
	}
	if strings.ContainsRune(in.CredentialRef, 0) {
		return fmt.Errorf("%w: credential_ref contains an invalid character", ErrInvalid)
	}
	return nil
}

// strictUnmarshal decodes JSON and refuses unknown fields: a typo in a
// transport config is an error, not a silently ignored setting.
func strictUnmarshal(raw []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
