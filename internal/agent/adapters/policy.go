package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/agent/policy"
)

// cliPolicyConfig keeps policy additive without changing the shared agent
// config type used by the other adapter.
type cliPolicyConfig struct {
	agent.CLIConfig
	Policy *policy.Config `json:"policy,omitempty"`
}

func decodeCLIConfig(raw json.RawMessage) (agent.CLIConfig, *policy.Config, error) {
	var cfg cliPolicyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return agent.CLIConfig{}, nil, err
	}
	return cfg.CLIConfig, cfg.Policy, nil
}

// canonicalAllowedRoots resolves symlinks once, so a symlinked path cannot
// disguise a work directory outside the configured roots.
func canonicalAllowedRoots(roots []string) ([]string, error) {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			return nil, fmt.Errorf("allowed_roots contains an empty path")
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("resolve allowed root %q: %w", root, err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("allowed root %q is not a directory", root)
		}
		out = append(out, filepath.Clean(resolved))
	}
	return out, nil
}

func checkAllowedWorkdir(workdir string, roots []string) (string, error) {
	resolved, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	resolved = filepath.Clean(resolved)
	for _, root := range roots {
		rel, err := filepath.Rel(root, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("working directory %q is outside the agent's allowed_roots", resolved)
}

// deniedInvocation matches a configured command prefix against the adapter's
// executable and configured arguments. The prompt is not part of this check.
func deniedInvocation(bin string, args, denied []string) string {
	for _, rule := range denied {
		fields := strings.Fields(rule)
		if len(fields) == 0 || filepath.Base(fields[0]) != filepath.Base(bin) || len(args) < len(fields)-1 {
			continue
		}
		match := true
		for i := 1; i < len(fields); i++ {
			if args[i-1] != fields[i] {
				match = false
				break
			}
		}
		if match {
			return rule
		}
	}
	return ""
}

func containsEnvKey(env []string, key string) bool {
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			return true
		}
	}
	return false
}

func setEnvValue(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			out = append(out, entry)
		}
	}
	return append(out, key+"="+value)
}
