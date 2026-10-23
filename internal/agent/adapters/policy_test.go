package adapters

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/agent/policy"
)

func policyCLIJob(id, bin string, args []string, roots, denied []string, allowNetwork bool) agent.Job {
	config := struct {
		Bin    string         `json:"bin"`
		Args   []string       `json:"args"`
		Policy *policy.Config `json:"policy,omitempty"`
	}{Bin: bin, Args: args, Policy: &policy.Config{
		AllowedRoots: roots, DeniedCommands: denied, AllowNetwork: allowNetwork,
	}}
	raw, _ := json.Marshal(config)
	return agent.Job{ID: id, Config: raw, Prompt: "policy test prompt"}
}

func TestCLIPolicyShimIsFirstInChildPATH(t *testing.T) {
	workdir := t.TempDir()
	wd := workdir
	job := policyCLIJob("policy-path", "sh", []string{"-c", "command -v git"}, []string{workdir}, nil, false)
	job.Workdir = &wd
	result, err := NewCLI().Run(context.Background(), job, (&collector{}).sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(result.Output)
	if !strings.Contains(got, "aic-policy-") || !strings.HasSuffix(got, "/git") {
		t.Fatalf("command -v git = %q, want shim wrapper first in PATH", got)
	}
}

func TestCLIPolicyRefusesGitPushAndLogsTelemetry(t *testing.T) {
	workdir := t.TempDir()
	wd := workdir
	job := policyCLIJob("policy-push", "sh", []string{"-c", "git push"}, []string{workdir}, nil, false)
	job.Workdir = &wd
	var events collector
	_, err := NewCLI().Run(context.Background(), job, events.sink())
	if err == nil {
		t.Fatal("git push should be refused")
	}
	if !strings.Contains(err.Error(), "the server commits and pushes; the agent does not") {
		t.Errorf("error %q does not explain refusal", err)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	sawPolicyTelemetry := false
	sawNetworkContext := false
	for _, event := range events.events {
		if event.Kind == "policy_refusal" && strings.Contains(event.Message, "git push") {
			sawPolicyTelemetry = true
		}
		if event.Kind == "policy_context" && strings.Contains(string(event.Data), `"allow_network":false`) {
			sawNetworkContext = true
		}
	}
	if !sawPolicyTelemetry {
		t.Errorf("job log missing policy refusal telemetry: %+v", events.events)
	}
	if !sawNetworkContext {
		t.Errorf("job log missing allow_network context: %+v", events.events)
	}
}

func TestCLIRefusesWorkdirOutsideAllowedRootsBeforeStart(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	marker := filepath.Join(outside, "started")
	bin := script(t, "mark-start", "touch "+shQuote(marker))
	wd := outside
	job := policyCLIJob("policy-root", bin, nil, []string{allowed}, nil, false)
	job.Workdir = &wd
	_, err := NewCLI().Run(context.Background(), job, (&collector{}).sink())
	if err == nil {
		t.Fatal("outside workdir should be refused")
	}
	if got := agent.FailureKind(err); got != agent.FailureCapability {
		t.Errorf("FailureKind = %q, want capability", got)
	}
	if !strings.Contains(err.Error(), "outside the agent's allowed_roots") {
		t.Errorf("error = %q, want clear allowed_roots explanation", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("child started despite rejected workdir: %v", err)
	}
}

func TestCLIWithoutPolicyRemainsUsable(t *testing.T) {
	bin := script(t, "plain", `echo unchanged`)
	result, err := NewCLI().Run(context.Background(), cliJob("policy-absent", bin, nil), (&collector{}).sink())
	if err != nil {
		t.Fatalf("Run without policy: %v", err)
	}
	if result.Output != "unchanged\n" {
		t.Errorf("output = %q, want unchanged output", result.Output)
	}
}
