package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orlandoburli/ai-company/internal/integration"
)

type issueTestGitHub struct {
	fakeIntegrationGitHub
	issue integration.Issue
	err   error
}

func (f *issueTestGitHub) GetIssue(context.Context, int) (integration.Issue, error) {
	return f.issue, f.err
}

func TestFetchIssueProducesUntrustedArtifactData(t *testing.T) {
	gh := &issueTestGitHub{issue: integration.Issue{Number: 42, Title: "Title", Body: "Issue body", Author: "reporter", CreatedAt: "2026-10-20T12:00:00Z"}}
	result, err := RunIntegration(t.Context(), Services{GitHub: gh}, IntegrationRequest{
		Type: "github.fetch_issue", Config: json.RawMessage(`{"repo":"owner/name","issue":42}`),
	})
	if err != nil || len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "issue_body" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	var output map[string]any
	var meta map[string]any
	if err := json.Unmarshal(result.OutputPorts["main"], &output); err != nil || output["title"] != "Title" || output["body"] != "Issue body" {
		t.Fatalf("output=%v error=%v", output, err)
	}
	if err := json.Unmarshal(result.Artifacts[0].Meta, &meta); err != nil || meta["repository"] != "owner/name" || meta["issue_number"] != float64(42) || meta["author"] != "reporter" {
		t.Fatalf("artifact meta=%v error=%v", meta, err)
	}
}
