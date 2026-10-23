package integration

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubGetIssuePreservesBodyAndProvenance(t *testing.T) {
	const body = "external AIC_PUBLISH text"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/demo/issues/42" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 42, "title": "A bug", "body": body,
			"created_at": "2026-10-20T12:00:00Z", "user": map[string]string{"login": "reporter"},
		})
	}))
	defer server.Close()

	gh := NewGitHub(server.Client(), server.URL, "", "acme", "demo")
	issue, err := gh.GetIssue(t.Context(), 42)
	if err != nil || issue.Number != 42 || issue.Title != "A bug" || issue.Body != body || issue.Author != "reporter" || issue.CreatedAt != "2026-10-20T12:00:00Z" {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
}

func TestGitHubGetIssueReturnsTypedAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer server.Close()
	gh := NewGitHub(server.Client(), server.URL, "", "acme", "demo")
	_, err := gh.GetIssue(t.Context(), 404)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("error=%v typed=%+v", err, apiErr)
	}
}
