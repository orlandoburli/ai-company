package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/integration"
)

// GitHubService is the small API surface used by integration nodes. Finder
// methods make check-then-act replay safe without coupling the engine to HTTP.
type GitHubService interface {
	GetPullRequest(context.Context, int) (integration.PullRequest, error)
	ListChecks(context.Context, int) ([]integration.Check, error)
	CreatePullRequest(context.Context, integration.PullRequestInput) (integration.PullRequest, error)
	AddComment(context.Context, int, string) (integration.Comment, error)
	GetRef(context.Context, string) (integration.Ref, error)
	FindOpenPullRequest(context.Context, string) (integration.PullRequest, bool, error)
	FindComment(context.Context, int, string) (integration.Comment, bool, error)
}

// GitHubIssueService is optional so existing connectors need not expose issue
// retrieval unless the workflow uses github.fetch_issue.
type GitHubIssueService interface {
	GetIssue(context.Context, int) (integration.Issue, error)
}

type GitService interface {
	PushBranch(context.Context, string, string, string) (integration.Commit, error)
	HeadCommit(context.Context, string) (integration.Commit, error)
	Status(context.Context, string) (integration.Status, error)
}

type TokenSource interface {
	Resolve(context.Context, string) (string, error)
}

type GitHubFactory func(token, owner, repo string) GitHubService

// Services are injected by the engine/runtime. GitHubFactory constructs a
// repo-scoped authenticated client when credentials are available; GitHub is a
// ready-to-use implementation for tests and installations with a shared client.
type Services struct {
	GitHub        GitHubService
	Git           GitService
	Tokens        TokenSource
	GitHubFactory GitHubFactory
}

type IntegrationRequest struct {
	Type, RunID, StepRunID, NodeID, IterationPath string
	Workdir, Branch, RepoURL, ProductID, ItemID   string
	ProductRepositories                           []string
	// MirrorPath is the app-owned bare mirror the worktree was cut from. The
	// push goes from there, not from the agent's worktree: the agent's refs
	// are data, not authority.
	MirrorPath     string
	RunIndex       int
	Config         json.RawMessage
	IdempotencyKey string
	// Inputs are the receipts of the incoming edges. They carry what the
	// upstream execution produced — the pull request number, the branch the
	// work landed on — so a node does not have to guess it.
	Inputs map[string]Receipt
}

// CredentialAwareGit is implemented by the git service that can authenticate a
// push with a credential it resolves at the moment of the call.
type CredentialAwareGit interface {
	WithCredential(integration.CredentialResolver) *integration.Git
}

// Committer is implemented by a git service that can commit the agent's work.
// The agent never commits: the server does, on the branch the server created.
type Committer interface {
	CommitAll(ctx context.Context, workdir, message string) (integration.Commit, error)
}

type IntegrationArtifact struct {
	Kind string          `json:"kind"`
	URI  string          `json:"uri"`
	Meta json.RawMessage `json:"meta"`
}

type IntegrationResult struct {
	Result      json.RawMessage
	OutputPorts map[string]json.RawMessage
	Artifacts   []IntegrationArtifact
}

type IntegrationError struct {
	Kind    FailureKind
	Message string
}

func (e *IntegrationError) Error() string            { return e.Message }
func (e *IntegrationError) FailureKind() FailureKind { return e.Kind }

type integrationConfig struct {
	Repo   string `json:"repo"`
	Remote string `json:"remote"`
	// SecretRef is the reference the integration authenticates with. The
	// engine fills it with the run's identity — the credential of the runner
	// the run's agent used — unless the node names one itself. It is a
	// reference, never a secret: the secret is resolved at the moment the
	// client is built.
	SecretRef     string `json:"secret_ref"`
	Base          string `json:"base"`
	Head          string `json:"head"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	Draft         bool   `json:"draft"`
	PR            int    `json:"pr"`
	Issue         int    `json:"issue"`
	TimeoutS      int    `json:"timeout_s"`
	PollIntervalS int    `json:"poll_interval_s"`
}

func RunIntegration(ctx context.Context, svc Services, in IntegrationRequest) (IntegrationResult, error) {
	var cfg integrationConfig
	if err := json.Unmarshal(in.Config, &cfg); err != nil {
		return IntegrationResult{}, integrationFailure(FailureCapability, "invalid integration config")
	}
	if cfg.Repo == "" && in.RepoURL != "" {
		cfg.Repo = githubRepoFromURL(in.RepoURL)
	}
	if in.ProductRepositories != nil {
		found := false
		for _, repository := range in.ProductRepositories {
			if strings.EqualFold(strings.TrimSpace(repository), strings.TrimSpace(cfg.Repo)) {
				found = true
				break
			}
		}
		if !found {
			return IntegrationResult{}, integrationFailure(FailureCapability,
				fmt.Sprintf("repository %q is not connected to product %q", cfg.Repo, in.ProductID))
		}
	}
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = fmt.Sprintf("run:%s:node:%s:%s:%d", in.RunID, in.NodeID, in.IterationPath, in.RunIndex)
	}
	if in.Type != "github.open_pr" && in.Type != "github.wait_ci" && in.Type != "github.comment" && in.Type != "github.fetch_issue" {
		return IntegrationResult{}, integrationFailure(FailureCapability, fmt.Sprintf("unknown integration type %q", in.Type))
	}
	client, err := integrationClient(ctx, svc, cfg)
	if err != nil {
		return IntegrationResult{}, err
	}
	switch in.Type {
	case "github.open_pr":
		return integrationOpenPR(ctx, svc, client, cfg, in)
	case "github.wait_ci":
		return integrationWaitCI(ctx, client, cfg, in)
	case "github.comment":
		return integrationComment(ctx, client, cfg, in)
	case "github.fetch_issue":
		return integrationFetchIssue(ctx, client, cfg)
	default:
		return IntegrationResult{}, integrationFailure(FailureCapability, "unsupported integration type")
	}
}

func integrationFetchIssue(ctx context.Context, gh GitHubService, cfg integrationConfig) (IntegrationResult, error) {
	if cfg.Issue <= 0 {
		return IntegrationResult{}, integrationFailure(FailureCapability, "github.fetch_issue requires a positive issue number")
	}
	issueClient, ok := gh.(GitHubIssueService)
	if !ok {
		return IntegrationResult{}, integrationFailure(FailureCapability, "GitHub service does not support issue retrieval")
	}
	issue, err := issueClient.GetIssue(ctx, cfg.Issue)
	if err != nil {
		kind := FailureTransient
		var apiErr *integration.APIError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
			kind = FailureDependency
		}
		return IntegrationResult{}, integrationFailure(kind, "could not fetch GitHub issue: "+excerpt(err))
	}
	if issue.Number == 0 {
		issue.Number = cfg.Issue
	}
	repository := cfg.Repo
	uri := fmt.Sprintf("https://github.com/%s/issues/%d", repository, issue.Number)
	meta, _ := json.Marshal(map[string]any{
		"repository": repository, "issue_number": issue.Number, "author": issue.Author,
		"timestamp": issue.CreatedAt, "title": issue.Title, "body": issue.Body,
	})
	return successIntegration(
		map[string]any{"title": issue.Title, "body": issue.Body, "issue_number": issue.Number},
		map[string]any{"title": issue.Title, "body": issue.Body, "issue_number": issue.Number},
		[]IntegrationArtifact{{Kind: "issue_body", URI: uri, Meta: meta}},
	)
}

func integrationClient(ctx context.Context, svc Services, cfg integrationConfig) (GitHubService, error) {
	owner, repo, ok := strings.Cut(cfg.Repo, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return nil, integrationFailure(FailureCapability, "repo must be owner/name")
	}
	if cfg.SecretRef != "" {
		if svc.Tokens == nil {
			return nil, integrationFailure(FailureDependency, "GitHub credential source is not configured")
		}
		token, err := svc.Tokens.Resolve(ctx, cfg.SecretRef)
		if err != nil {
			return nil, integrationFailure(FailureDependency, err.Error())
		}
		if svc.GitHubFactory != nil {
			return svc.GitHubFactory(token, owner, repo), nil
		}
	}
	if svc.GitHub == nil {
		return nil, integrationFailure(FailureDependency, "GitHub service is not configured")
	}
	return svc.GitHub, nil
}

func integrationOpenPR(ctx context.Context, svc Services, gh GitHubService, cfg integrationConfig, in IntegrationRequest) (IntegrationResult, error) {
	if cfg.Head == "" {
		cfg.Head = in.Branch
	}
	if cfg.Head == "" || cfg.Base == "" || cfg.Title == "" {
		return IntegrationResult{}, integrationFailure(FailureCapability, "github.open_pr requires head, base, and title")
	}
	pr, found, err := gh.FindOpenPullRequest(ctx, cfg.Head)
	if err != nil {
		return IntegrationResult{}, integrationFailure(FailureTransient, "could not look up existing pull request: "+excerpt(err))
	}
	if !found {
		// The server commits the agent's work: the agent never commits.
		if in.Workdir != "" {
			if committer, ok := svc.Git.(Committer); ok {
				if _, err := committer.CommitAll(ctx, in.Workdir, "AI Company: work from the run"); err != nil {
					return IntegrationResult{}, integrationFailure(FailureTransient, "could not commit the work")
				}
			}
		}
		if svc.Git != nil && (in.MirrorPath != "" || in.Workdir != "") {
			remote := cfg.Remote
			if remote == "" {
				remote = httpsRemote(cfg.Repo)
			}
			if remote == "" {
				return IntegrationResult{}, integrationFailure(FailureCapability, "github.open_pr needs a repository remote")
			}
			// The push is authenticated with the credential from the keychain:
			// it never appears in the arguments, the environment or a log line.
			if aware, ok := svc.Git.(CredentialAwareGit); ok && svc.Tokens != nil && cfg.SecretRef != "" {
				aware.WithCredential(func(ctx context.Context) (string, error) {
					return svc.Tokens.Resolve(ctx, cfg.SecretRef)
				})
			}
			// The push goes from the app-owned mirror, never from the agent's
			// worktree: the agent's refs are data, not authority.
			pushDir := in.MirrorPath
			if pushDir == "" {
				pushDir = in.Workdir
			}
			if _, err := svc.Git.PushBranch(ctx, pushDir, remote, cfg.Head); err != nil {
				return IntegrationResult{}, integrationFailure(FailureTransient, "could not push integration branch: "+excerpt(err))
			}
		}
		pr, err = gh.CreatePullRequest(ctx, integration.PullRequestInput{Base: cfg.Base, Head: cfg.Head, Title: cfg.Title, Body: cfg.Body, Draft: cfg.Draft})
		if err != nil {
			// A concurrent retry may have created the same branch PR after our
			// lookup. Re-check before surfacing the API failure.
			if existing, yes, lookupErr := gh.FindOpenPullRequest(ctx, cfg.Head); lookupErr == nil && yes {
				pr, found = existing, true
			} else {
				return IntegrationResult{}, integrationFailure(FailureTransient, "could not create pull request: "+excerpt(err))
			}
		}
	}
	if pr.URL == "" {
		pr.URL = pr.HTMLURL
	}
	meta, _ := json.Marshal(map[string]any{"number": pr.Number, "url": pr.URL, "head": cfg.Head, "idempotency_key": in.IdempotencyKey, "reused": found})
	result := map[string]any{"number": pr.Number, "url": pr.URL, "reused": found}
	return successIntegration(result, map[string]any{"number": pr.Number, "url": pr.URL}, []IntegrationArtifact{{Kind: "pr", URI: pr.URL, Meta: meta}})
}

func integrationWaitCI(ctx context.Context, gh GitHubService, cfg integrationConfig, in IntegrationRequest) (IntegrationResult, error) {
	if cfg.PR <= 0 {
		// The pull request the upstream node opened: it reported its number on
		// its output port.
		if number := pullRequestNumber(in.Inputs); number > 0 {
			cfg.PR = number
		}
	}
	if cfg.PR <= 0 {
		// The branch the checks belong to: the node's own `head` when it names
		// one, otherwise the branch the run is executing.
		branch := strings.TrimSpace(cfg.Head)
		if branch == "" {
			branch = in.Branch
		}
		if branch == "" {
			return IntegrationResult{}, integrationFailure(FailureCapability, "github.wait_ci requires pr, a branch, or an execution branch")
		}
		pr, found, err := gh.FindOpenPullRequest(ctx, branch)
		if err != nil {
			return IntegrationResult{}, integrationFailure(FailureTransient, "could not find the open pull request")
		}
		if !found {
			return IntegrationResult{}, integrationFailure(FailureTransient, "no open pull request found for "+branch)
		}
		cfg.PR = pr.Number
	}
	timeout := time.Duration(cfg.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	interval := time.Duration(cfg.PollIntervalS) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		checks, err := gh.ListChecks(ctx, cfg.PR)
		if err != nil {
			return IntegrationResult{}, integrationFailure(FailureTransient, "could not read pull request checks: "+excerpt(err))
		}
		allSuccess := len(checks) > 0
		for _, check := range checks {
			if check.Status == "failure" {
				return IntegrationResult{}, integrationFailure(FailureTransient, "CI check failed: "+check.Name)
			}
			if check.Status != "success" {
				allSuccess = false
			}
		}
		if allSuccess {
			return successIntegration(map[string]any{"pr": cfg.PR, "checks": checks}, map[string]any{"pr": cfg.PR, "checks": checks}, nil)
		}
		wait := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			wait.Stop()
			return IntegrationResult{}, integrationFailure(FailureTransient, "waiting for CI was canceled")
		case <-deadline.C:
			wait.Stop()
			return IntegrationResult{}, integrationFailure(FailureTransient, fmt.Sprintf("timed out waiting for CI checks on pull request #%d", cfg.PR))
		case <-wait.C:
		}
	}
}

// githubRepoFromURL maps a product's git remote to the API's owner/name form.
// Example: git@github.com:orlandoburli/fazgiro.git becomes
// orlandoburli/fazgiro; the SSH URL is never passed to a process here.
// excerpt is a short, safe description of an upstream failure. The GitHub
// API's own message is the useful part ("No commits between main and …"), and
// it never carries the token: the client sends the token in a header and the
// API does not echo it back.
func excerpt(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *integration.APIError
	if errors.As(err, &apiErr) {
		return strings.TrimSpace(apiErr.Message)
	}
	return ""
}

// pullRequestNumber reads the pull request number an upstream node reported.
func pullRequestNumber(inputs map[string]Receipt) int {
	for _, receipt := range inputs {
		if receipt.State != ReceiptDelivered || len(receipt.Value) == 0 {
			continue
		}
		var fields struct {
			Number int `json:"number"`
		}
		if json.Unmarshal(receipt.Value, &fields) == nil && fields.Number > 0 {
			return fields.Number
		}
	}
	return 0
}

// httpsRemote turns an "owner/name" repository into the HTTPS URL a
// credential-authenticated push uses.
func httpsRemote(repo string) string {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return "https://github.com/" + owner + "/" + name + ".git"
}

func githubRepoFromURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	var path string
	if at := strings.LastIndex(value, "@"); at >= 0 {
		if colon := strings.Index(value[at+1:], ":"); colon >= 0 {
			path = value[at+1+colon+1:]
		}
	} else if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		path = strings.TrimPrefix(parsed.Path, "/")
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

func integrationComment(ctx context.Context, gh GitHubService, cfg integrationConfig, in IntegrationRequest) (IntegrationResult, error) {
	if cfg.PR <= 0 || strings.TrimSpace(cfg.Body) == "" {
		return IntegrationResult{}, integrationFailure(FailureCapability, "github.comment requires pr and body")
	}
	digest := sha256.Sum256([]byte(in.IdempotencyKey))
	marker := "<!-- aic-idempotency:" + hex.EncodeToString(digest[:]) + " -->"
	comment, found, err := gh.FindComment(ctx, cfg.PR, marker)
	if err != nil {
		return IntegrationResult{}, integrationFailure(FailureTransient, "could not check existing pull request comments")
	}
	if !found {
		comment, err = gh.AddComment(ctx, cfg.PR, strings.TrimSpace(cfg.Body)+"\n\n"+marker)
		if err != nil {
			return IntegrationResult{}, integrationFailure(FailureTransient, "could not post pull request comment")
		}
	}
	return successIntegration(map[string]any{"pr": cfg.PR, "comment_url": comment.URL, "reused": found}, map[string]any{"comment_url": comment.URL}, nil)
}

func successIntegration(result, output any, artifacts []IntegrationArtifact) (IntegrationResult, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return IntegrationResult{}, err
	}
	port, err := json.Marshal(output)
	if err != nil {
		return IntegrationResult{}, err
	}
	return IntegrationResult{Result: raw, OutputPorts: map[string]json.RawMessage{"main": port}, Artifacts: artifacts}, nil
}

func integrationFailure(kind FailureKind, message string) error {
	return &IntegrationError{Kind: kind, Message: message}
}
