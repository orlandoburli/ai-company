package ghsync

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/orlandoburli/ai-company/internal/integration"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// TokenClient is a GitHub client that authenticates with one token and
// serves any repository of the account. The token is never logged and never
// leaves the request headers.
type TokenClient struct {
	Token string
}

// CredentialRef returns the tenant's registered secret reference for GitHub —
// the *reference* to a secret held in the OS keychain, never the secret. It is
// the reference the keychain resolver is pointed at.
func CredentialRef(ctx context.Context, db *sql.DB, sc tenancy.Scope) (string, error) {
	if err := sc.Validate(); err != nil {
		return "", err
	}
	if db == nil {
		return "", errors.New("ghsync: database is not configured")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, sc.TenantID); err != nil {
		return "", err
	}
	var ref string
	if err := tx.QueryRowContext(ctx,
		`SELECT secret_ref FROM integration_credentials WHERE kind = 'github'`).Scan(&ref); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return ref, nil
}

func (c TokenClient) client(repo string) (*integration.GitHub, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, ErrInvalidRepository
	}
	return integration.NewGitHub(nil, integration.DefaultGitHubURL, c.Token, owner, name), nil
}

// ListIssues lists the repository's issues (pull requests excluded by the
// client).
func (c TokenClient) ListIssues(ctx context.Context, repo, state string, limit int) ([]integration.Issue, error) {
	client, err := c.client(repo)
	if err != nil {
		return nil, err
	}
	return client.ListIssues(ctx, state, limit)
}

// GetIssue reads one issue.
func (c TokenClient) GetIssue(ctx context.Context, repo string, number int) (integration.Issue, error) {
	client, err := c.client(repo)
	if err != nil {
		return integration.Issue{}, err
	}
	return client.GetIssue(ctx, number)
}

// ListPullRequests lists the repository's pull requests.
func (c TokenClient) ListPullRequests(ctx context.Context, repo, state string, limit int) ([]integration.PullRequest, error) {
	client, err := c.client(repo)
	if err != nil {
		return nil, err
	}
	return client.ListPullRequests(ctx, state, limit)
}

// UpdateIssueTitle renames an issue.
func (c TokenClient) UpdateIssueTitle(ctx context.Context, repo string, number int, title string) (integration.Issue, error) {
	client, err := c.client(repo)
	if err != nil {
		return integration.Issue{}, err
	}
	return client.UpdateIssueTitle(ctx, number, title)
}
