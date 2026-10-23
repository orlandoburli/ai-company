package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultGitHubURL = "https://api.github.com"

type PullRequest struct {
	Number         int        `json:"number"`
	State          string     `json:"state"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	Head           string     `json:"head"`
	Base           string     `json:"base"`
	Mergeable      *bool      `json:"mergeable,omitempty"`
	MergeableState string     `json:"mergeable_state,omitempty"`
	URL            string     `json:"url"`
	HTMLURL        string     `json:"html_url"`
	HeadSHA        string     `json:"head_sha"`
	Merged         bool       `json:"merged"`
	MergedAt       *time.Time `json:"merged_at,omitempty"`
}

// Issue is a GitHub issue. Pull requests are returned by GitHub's issues API
// too; IsPullRequest identifies those rows so issue importers can exclude them.
type Issue struct {
	Number        int       `json:"number"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	State         string    `json:"state"`
	HTMLURL       string    `json:"html_url"`
	Author        string    `json:"author"`
	Labels        []string  `json:"labels"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	IsPullRequest bool      `json:"is_pull_request"`
}

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

type PullRequestInput struct {
	Base  string `json:"base"`
	Head  string `json:"head"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Draft bool   `json:"draft"`
}

type Comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	URL  string `json:"html_url"`
}

type Ref struct {
	Name string `json:"ref"`
	SHA  string `json:"sha"`
}

// APIError reports a sanitized non-success response from GitHub.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub API returned HTTP %d: %s", e.Status, e.Message)
}

type GitHub struct {
	http  *http.Client
	base  string
	token string
	owner string
	repo  string
}

func NewGitHub(client *http.Client, baseURL, token, owner, repo string) *GitHub {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultGitHubURL
	}
	return &GitHub{http: client, base: strings.TrimRight(baseURL, "/"), token: token, owner: owner, repo: repo}
}

func (g *GitHub) repoPath(tail string) string {
	return "/repos/" + url.PathEscape(g.owner) + "/" + url.PathEscape(g.repo) + tail
}

func (g *GitHub) GetPullRequest(ctx context.Context, number int) (PullRequest, error) {
	var wire struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Mergeable      *bool      `json:"mergeable"`
		MergeableState string     `json:"mergeable_state"`
		Merged         bool       `json:"merged"`
		MergedAt       *time.Time `json:"merged_at"`
		URL            string     `json:"url"`
		HTMLURL        string     `json:"html_url"`
	}
	err := g.request(ctx, http.MethodGet, g.repoPath(fmt.Sprintf("/pulls/%d", number)), nil, &wire)
	return PullRequest{Number: wire.Number, State: wire.State, Title: wire.Title, Body: wire.Body, Head: wire.Head.Ref, Base: wire.Base.Ref, Mergeable: wire.Mergeable, MergeableState: wire.MergeableState, URL: wire.HTMLURL, HTMLURL: wire.HTMLURL, HeadSHA: wire.Head.SHA, Merged: wire.Merged, MergedAt: wire.MergedAt}, err
}

// ListIssues returns a bounded page of issues. GitHub also includes pull
// requests in this endpoint, which callers can identify with IsPullRequest.
func (g *GitHub) ListIssues(ctx context.Context, state string, limit int) ([]Issue, error) {
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return nil, fmt.Errorf("invalid GitHub issue state %q", state)
	}
	limit = githubLimit(limit)
	query := url.Values{"state": {state}, "per_page": {fmt.Sprint(limit)}}
	var rows []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		CreatedAt   time.Time       `json:"created_at"`
		UpdatedAt   time.Time       `json:"updated_at"`
		PullRequest json.RawMessage `json:"pull_request"`
	}
	if err := g.request(ctx, http.MethodGet, g.repoPath("/issues")+"?"+query.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(rows))
	for _, row := range rows {
		issue := Issue{Number: row.Number, Title: row.Title, Body: row.Body, State: row.State,
			HTMLURL: row.HTMLURL, Author: row.User.Login, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt, IsPullRequest: len(row.PullRequest) > 0 && string(row.PullRequest) != "null",
			Labels: make([]string, 0, len(row.Labels))}
		for _, label := range row.Labels {
			issue.Labels = append(issue.Labels, label.Name)
		}
		out = append(out, issue)
	}
	return out, nil
}

// GetIssue fetches one issue. IsPullRequest is set for pull requests as well.
func (g *GitHub) GetIssue(ctx context.Context, number int) (Issue, error) {
	var row struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		CreatedAt   time.Time       `json:"created_at"`
		UpdatedAt   time.Time       `json:"updated_at"`
		PullRequest json.RawMessage `json:"pull_request"`
	}
	if err := g.request(ctx, http.MethodGet, g.repoPath(fmt.Sprintf("/issues/%d", number)), nil, &row); err != nil {
		return Issue{}, err
	}
	issue := Issue{Number: row.Number, Title: row.Title, Body: row.Body, State: row.State,
		HTMLURL: row.HTMLURL, Author: row.User.Login, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, IsPullRequest: len(row.PullRequest) > 0 && string(row.PullRequest) != "null",
		Labels: make([]string, 0, len(row.Labels))}
	for _, label := range row.Labels {
		issue.Labels = append(issue.Labels, label.Name)
	}
	return issue, nil
}

// UpdateIssueTitle pushes an item's title to its linked GitHub issue.
func (g *GitHub) UpdateIssueTitle(ctx context.Context, number int, title string) (Issue, error) {
	var row struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		CreatedAt   time.Time       `json:"created_at"`
		UpdatedAt   time.Time       `json:"updated_at"`
		PullRequest json.RawMessage `json:"pull_request"`
	}
	if err := g.request(ctx, http.MethodPatch, g.repoPath(fmt.Sprintf("/issues/%d", number)), map[string]string{"title": title}, &row); err != nil {
		return Issue{}, err
	}
	out := Issue{Number: row.Number, Title: row.Title, Body: row.Body, State: row.State,
		HTMLURL: row.HTMLURL, Author: row.User.Login, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, IsPullRequest: len(row.PullRequest) > 0 && string(row.PullRequest) != "null",
		Labels: make([]string, 0, len(row.Labels))}
	for _, label := range row.Labels {
		out.Labels = append(out.Labels, label.Name)
	}
	return out, nil
}

// ListPullRequests returns PRs in the requested state. GitHub's list API has
// no "merged" state, so that filter is applied to closed PRs locally.
func (g *GitHub) ListPullRequests(ctx context.Context, state string, limit int) ([]PullRequest, error) {
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "merged" && state != "all" {
		return nil, fmt.Errorf("invalid GitHub pull request state %q", state)
	}
	queryState := state
	if state == "merged" {
		queryState = "closed"
	}
	limit = githubLimit(limit)
	query := url.Values{"state": {queryState}, "per_page": {fmt.Sprint(limit)}}
	var rows []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Mergeable      *bool      `json:"mergeable"`
		MergeableState string     `json:"mergeable_state"`
		URL            string     `json:"url"`
		HTMLURL        string     `json:"html_url"`
		Merged         bool       `json:"merged"`
		MergedAt       *time.Time `json:"merged_at"`
	}
	if err := g.request(ctx, http.MethodGet, g.repoPath("/pulls")+"?"+query.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	out := make([]PullRequest, 0, len(rows))
	for _, row := range rows {
		pr := PullRequest{Number: row.Number, State: row.State, Title: row.Title, Body: row.Body,
			Head: row.Head.Ref, Base: row.Base.Ref, Mergeable: row.Mergeable,
			MergeableState: row.MergeableState, URL: row.HTMLURL, HTMLURL: row.HTMLURL,
			HeadSHA: row.Head.SHA, Merged: row.Merged, MergedAt: row.MergedAt}
		if state == "merged" && !pr.Merged {
			continue
		}
		out = append(out, pr)
	}
	return out, nil
}

func githubLimit(limit int) int {
	if limit <= 0 || limit > 100 {
		return 100
	}
	return limit
}

func (g *GitHub) ListChecks(ctx context.Context, number int) ([]Check, error) {
	pr, err := g.GetPullRequest(ctx, number)
	if err != nil {
		return nil, err
	}
	var response struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			DetailsURL string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := g.request(ctx, http.MethodGet, g.repoPath("/commits/"+url.PathEscape(pr.HeadSHA)+"/check-runs"), nil, &response); err != nil {
		return nil, err
	}
	out := make([]Check, 0, len(response.CheckRuns))
	for _, run := range response.CheckRuns {
		status := run.Status
		if status == "completed" {
			switch run.Conclusion {
			case "success", "neutral", "skipped":
				status = "success"
			case "failure", "cancelled", "timed_out", "action_required":
				status = "failure"
			default:
				status = "pending"
			}
		}
		if status != "success" && status != "failure" && status != "pending" && status != "queued" {
			status = "pending"
		}
		out = append(out, Check{Name: run.Name, Status: status, URL: run.DetailsURL})
	}
	return out, nil
}

func (g *GitHub) CreatePullRequest(ctx context.Context, in PullRequestInput) (PullRequest, error) {
	var wire struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Mergeable      *bool  `json:"mergeable"`
		MergeableState string `json:"mergeable_state"`
		URL            string `json:"url"`
		HTMLURL        string `json:"html_url"`
	}
	err := g.request(ctx, http.MethodPost, g.repoPath("/pulls"), in, &wire)
	return PullRequest{Number: wire.Number, State: wire.State, Title: wire.Title, Body: wire.Body, Head: wire.Head.Ref, Base: wire.Base.Ref, Mergeable: wire.Mergeable, MergeableState: wire.MergeableState, URL: wire.HTMLURL, HTMLURL: wire.HTMLURL, HeadSHA: wire.Head.SHA}, err
}

func (g *GitHub) AddComment(ctx context.Context, number int, body string) (Comment, error) {
	var out Comment
	err := g.request(ctx, http.MethodPost, g.repoPath(fmt.Sprintf("/issues/%d/comments", number)), map[string]string{"body": body}, &out)
	return out, err
}

func (g *GitHub) GetRef(ctx context.Context, ref string) (Ref, error) {
	if isCommitSHA(ref) {
		var wire struct {
			SHA string `json:"sha"`
		}
		err := g.request(ctx, http.MethodGet, g.repoPath("/git/commits/"+url.PathEscape(ref)), nil, &wire)
		return Ref{Name: ref, SHA: wire.SHA}, err
	}
	name := ref
	if !strings.HasPrefix(name, "refs/") {
		name = "refs/heads/" + name
	}
	name = strings.TrimPrefix(name, "refs/")
	var wire struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := g.request(ctx, http.MethodGet, g.repoPath("/git/ref/"+url.PathEscape(name)), nil, &wire)
	return Ref{Name: wire.Ref, SHA: wire.Object.SHA}, err
}

func isCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// FindOpenPullRequest supports check-then-act idempotency for branch PRs.
func (g *GitHub) FindOpenPullRequest(ctx context.Context, head string) (PullRequest, bool, error) {
	query := url.Values{"state": {"open"}, "head": {g.owner + ":" + head}, "per_page": {"100"}}
	var rows []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		HTMLURL        string `json:"html_url"`
		URL            string `json:"url"`
		Mergeable      *bool  `json:"mergeable"`
		MergeableState string `json:"mergeable_state"`
	}
	err := g.request(ctx, http.MethodGet, g.repoPath("/pulls")+"?"+query.Encode(), nil, &rows)
	if err != nil {
		return PullRequest{}, false, err
	}
	for _, p := range rows {
		if p.Head.Ref == head {
			return PullRequest{Number: p.Number, State: p.State, Title: p.Title, Body: p.Body, Head: p.Head.Ref, Base: p.Base.Ref, HeadSHA: p.Head.SHA, URL: p.HTMLURL, HTMLURL: p.HTMLURL, Mergeable: p.Mergeable, MergeableState: p.MergeableState}, true, nil
		}
	}
	return PullRequest{}, false, nil
}

// FindComment scans existing PR comments for an exact idempotency marker.
func (g *GitHub) FindComment(ctx context.Context, number int, marker string) (Comment, bool, error) {
	var rows []Comment
	if err := g.request(ctx, http.MethodGet, g.repoPath(fmt.Sprintf("/issues/%d/comments?per_page=100", number)), nil, &rows); err != nil {
		return Comment{}, false, err
	}
	for _, comment := range rows {
		if strings.Contains(comment.Body, marker) {
			return comment, true, nil
		}
	}
	return Comment{}, false, nil
}

func (g *GitHub) request(ctx context.Context, method, path string, body, dst any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, reader)
	if err != nil {
		return fmt.Errorf("GitHub request could not be created")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return errors.New("GitHub request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return errors.New("GitHub response could not be read")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var api struct {
			Message string `json:"message"`
			Errors  []struct {
				Resource string `json:"resource"`
				Field    string `json:"field"`
				Code     string `json:"code"`
				Message  string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(data, &api)
		msg := strings.TrimSpace(Redact(api.Message, g.token))
		// GitHub's generic "Validation Failed" says nothing useful on its own;
		// the detail is in the errors array.
		for _, detail := range api.Errors {
			text := strings.TrimSpace(detail.Message)
			if text == "" {
				text = strings.TrimSpace(detail.Field)
			}
			if text == "" {
				text = strings.TrimSpace(detail.Code)
			}
			if text == "" {
				continue
			}
			if msg != "" {
				msg += ": "
			}
			msg += text
			if len(msg) > 400 {
				break
			}
		}
		if msg == "" {
			msg = "request failed"
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if dst == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return errors.New("GitHub returned an invalid response")
	}
	return nil
}
