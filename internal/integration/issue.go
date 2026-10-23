package integration

import (
	"context"
	"fmt"
	"net/http"
)

// Issue is the untrusted text and source metadata returned by GitHub.
type Issue struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
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
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := g.request(ctx, http.MethodGet, g.repoPath(fmt.Sprintf("/issues/%d", number)), nil, &wire); err != nil {
		return Issue{}, err
	}
	return Issue{Number: wire.Number, Title: wire.Title, Body: wire.Body, Author: wire.User.Login, CreatedAt: wire.CreatedAt}, nil
}
