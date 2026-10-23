package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Issue is the untrusted text and source metadata returned by GitHub.
type Issue struct {
	Number        int      `json:"number"`
	Title         string   `json:"title"`
	Body          string   `json:"body"`
	State         string   `json:"state"`
	HTMLURL       string   `json:"html_url"`
	Author        string   `json:"author"`
	Labels        []string `json:"labels"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	IsPullRequest bool     `json:"is_pull_request"`
}

// GetIssue fetches a repository issue without interpreting its text. Pull
// requests also use the issues endpoint, but this integration expects an issue
// number supplied by trusted node configuration.
func (g *GitHub) GetIssue(ctx context.Context, number int) (Issue, error) {
	if number <= 0 {
		return Issue{}, fmt.Errorf("issue number must be positive")
	}
	var wire struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		HTMLURL   string `json:"html_url"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		PullRequest json.RawMessage `json:"pull_request"`
	}
	if err := g.request(ctx, http.MethodGet, g.repoPath(fmt.Sprintf("/issues/%d", number)), nil, &wire); err != nil {
		return Issue{}, err
	}
	issue := Issue{Number: wire.Number, Title: wire.Title, Body: wire.Body, State: wire.State,
		HTMLURL: wire.HTMLURL, Author: wire.User.Login, CreatedAt: wire.CreatedAt,
		UpdatedAt: wire.UpdatedAt, IsPullRequest: len(wire.PullRequest) > 0 && string(wire.PullRequest) != "null",
		Labels: make([]string, 0, len(wire.Labels))}
	for _, label := range wire.Labels {
		issue.Labels = append(issue.Labels, label.Name)
	}
	return issue, nil
}
