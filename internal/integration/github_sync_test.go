package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubIssueAndPullRequestLists(t *testing.T) {
	var authorized bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorized = r.Header.Get("Authorization") == "Bearer test-token"
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/demo/issues":
			if r.URL.Query().Get("state") != "all" || r.URL.Query().Get("per_page") != "10" {
				t.Errorf("issue query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[
				{"number":3,"title":"issue","body":"untrusted","state":"open","html_url":"https://github.test/issues/3","user":{"login":"reporter"},"labels":[{"name":"bug"}],"created_at":"2026-10-20T12:00:00Z","updated_at":"2026-10-21T12:00:00Z"},
				{"number":4,"title":"PR","state":"closed","pull_request":{"url":"https://api.github.test/pulls/4"}}
			]`))
		case "/repos/acme/demo/issues/3":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 3, "title": "issue", "body": "body", "state": "open", "html_url": "https://github.test/issues/3", "user": map[string]string{"login": "reporter"}, "labels": []any{map[string]string{"name": "bug"}}, "created_at": "2026-10-20T12:00:00Z", "updated_at": "2026-10-21T12:00:00Z"})
		case "/repos/acme/demo/pulls":
			if r.URL.Query().Get("state") != "closed" {
				t.Errorf("merged filter should request closed PRs: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[
				{"number":9,"state":"closed","merged":true,"merged_at":"2026-10-22T12:00:00Z","html_url":"https://github.test/pull/9","head":{"ref":"aic/item-step-1","sha":"abc"},"base":{"ref":"main"}},
				{"number":10,"state":"closed","merged":false,"html_url":"https://github.test/pull/10","head":{"ref":"other","sha":"def"},"base":{"ref":"main"}}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	gh := NewGitHub(server.Client(), server.URL, "test-token", "acme", "demo")
	issues, err := gh.ListIssues(t.Context(), "all", 10)
	if err != nil || len(issues) != 2 || issues[0].Number != 3 || issues[0].Author != "reporter" || len(issues[0].Labels) != 1 || issues[0].UpdatedAt == "" || issues[1].IsPullRequest != true {
		t.Fatalf("issues = %#v, %v", issues, err)
	}
	issue, err := gh.GetIssue(t.Context(), 3)
	if err != nil || issue.State != "open" || issue.HTMLURL == "" || issue.Author != "reporter" {
		t.Fatalf("issue = %#v, %v", issue, err)
	}
	prs, err := gh.ListPullRequests(t.Context(), "merged", 20)
	if err != nil || len(prs) != 1 || !prs[0].Merged || prs[0].MergedAt == nil {
		t.Fatalf("merged PRs = %#v, %v", prs, err)
	}
	if !authorized {
		t.Fatal("GitHub token header was not sent")
	}
}
