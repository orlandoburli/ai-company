package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/orlandoburli/ai-company/internal/ghsync"
	"github.com/orlandoburli/ai-company/internal/integration"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

// GitHub service injection allows deployments and tests to provide a configured
// service without changing the Server's stable constructor surface.
var githubServices sync.Map // map[*Server]*ghsync.Service

// WithGitHubSyncService injects a GitHub sync implementation (also useful for
// callers with a custom credential or API endpoint).
func WithGitHubSyncService(service *ghsync.Service) Option {
	return func(s *Server) {
		if service != nil {
			githubServices.Store(s, service)
		}
	}
}

// registerGitHubRoutes installs the tenant-scoped issue import/link/sync API.
func (s *Server) registerGitHubRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/github/issues", s.authenticated(s.handleGitHubIssues))
	mux.Handle("POST /api/github/sync", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleGitHubSync)))
	mux.Handle("POST /api/items/{id}/github-link", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleGitHubLink)))
}

func (s *Server) handleGitHubIssues(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	productID := strings.TrimSpace(r.URL.Query().Get("product_id"))
	if productID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "product_id is required.")
		return
	}
	svc, err := s.githubSyncService(r.Context(), sc)
	if err != nil {
		s.writeGitHubError(w, err)
		return
	}
	links, err := svc.ListLinks(r.Context(), sc, productID)
	if err != nil {
		s.writeGitHubError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

func (s *Server) handleGitHubSync(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	svc, err := s.githubSyncService(r.Context(), sc)
	if err != nil {
		s.writeGitHubError(w, err)
		return
	}
	products, err := s.work.ListProducts(r.Context(), sc)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	result := ghsync.Result{ImportedItems: []work.Item{}, UpdatedTitles: []work.Item{}, CompletedItems: []work.Item{}, CompletedRuns: []workflow.Run{}}
	for _, product := range products {
		if strings.TrimSpace(product.RepoURL) == "" {
			continue
		}
		for _, op := range []func(context.Context, tenancy.Scope, string, string) (ghsync.Result, error){
			svc.ImportIssues, svc.SyncIssues, svc.SyncMergedPullRequests,
		} {
			part, err := op(r.Context(), sc, product.ID, product.RepoURL)
			if err != nil {
				s.writeGitHubError(w, err)
				return
			}
			result.ImportedItems = append(result.ImportedItems, part.ImportedItems...)
			result.UpdatedTitles = append(result.UpdatedTitles, part.UpdatedTitles...)
			result.CompletedItems = append(result.CompletedItems, part.CompletedItems...)
			result.CompletedRuns = append(result.CompletedRuns, part.CompletedRuns...)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

type githubLinkRequest struct {
	ProductID   string `json:"product_id"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
}

func (s *Server) handleGitHubLink(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req githubLinkRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ProductID) == "" || req.IssueNumber <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "product_id and a positive issue_number are required.")
		return
	}
	svc, err := s.githubSyncService(r.Context(), sc)
	if err != nil {
		s.writeGitHubError(w, err)
		return
	}
	link, err := svc.LinkIssue(r.Context(), sc, r.PathValue("id"), req.ProductID, req.Repo, req.IssueNumber)
	if err != nil {
		s.writeGitHubError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"link": link})
}

func (s *Server) githubSyncService(ctx context.Context, sc tenancy.Scope) (*ghsync.Service, error) {
	if injected, ok := githubServices.Load(s); ok {
		return injected.(*ghsync.Service), nil
	}
	var secretRef string
	err := s.integrationTx(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT secret_ref FROM integration_credentials WHERE kind='github'`).Scan(&secretRef)
		if errors.Is(err, sql.ErrNoRows) {
			return ghsync.ErrGitHubNotConfigured
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	token, err := (integration.Credentials{}).Resolve(ctx, secretRef)
	if err != nil {
		return nil, ghsync.ErrGitHubNotConfigured
	}
	return ghsync.New(ghsync.Deps{
		DB: s.db, GitHub: githubTokenClient{token: token}, Work: s.work,
		Workflows: workflow.NewStore(s.db), Logger: s.log,
	}), nil
}

type githubTokenClient struct{ token string }

func (c githubTokenClient) client(repo string) (*integration.GitHub, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, ghsync.ErrInvalidRepository
	}
	return integration.NewGitHub(nil, integration.DefaultGitHubURL, c.token, owner, name), nil
}

func (c githubTokenClient) ListIssues(ctx context.Context, repo, state string, limit int) ([]integration.Issue, error) {
	client, err := c.client(repo)
	if err != nil {
		return nil, err
	}
	return client.ListIssues(ctx, state, limit)
}
func (c githubTokenClient) GetIssue(ctx context.Context, repo string, number int) (integration.Issue, error) {
	client, err := c.client(repo)
	if err != nil {
		return integration.Issue{}, err
	}
	return client.GetIssue(ctx, number)
}
func (c githubTokenClient) ListPullRequests(ctx context.Context, repo, state string, limit int) ([]integration.PullRequest, error) {
	client, err := c.client(repo)
	if err != nil {
		return nil, err
	}
	return client.ListPullRequests(ctx, state, limit)
}
func (c githubTokenClient) UpdateIssueTitle(ctx context.Context, repo string, number int, title string) (integration.Issue, error) {
	client, err := c.client(repo)
	if err != nil {
		return integration.Issue{}, err
	}
	return client.UpdateIssueTitle(ctx, number, title)
}

func (s *Server) writeGitHubError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ghsync.ErrGitHubNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "github_not_configured", "Connect a GitHub credential before syncing.")
	case errors.Is(err, ghsync.ErrProductWithoutRepository):
		writeError(w, http.StatusUnprocessableEntity, "github_product_without_repository", "That product does not have a valid GitHub repository.")
	case errors.Is(err, ghsync.ErrLinkConflict):
		writeError(w, http.StatusConflict, "github_link_conflict", "That GitHub issue is already linked to another item.")
	case errors.Is(err, ghsync.ErrIssueNotFound):
		writeError(w, http.StatusNotFound, "issue_not_found", "That GitHub issue does not exist in this repository.")
	case errors.Is(err, ghsync.ErrItemNotFound), errors.Is(err, ghsync.ErrProductNotFound), errors.Is(err, work.ErrNotFound):
		writeError(w, http.StatusNotFound, "item_not_found", "That item or product does not exist in this tenant.")
	case errors.Is(err, tenancy.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
	case errors.Is(err, ghsync.ErrInvalidRepository):
		writeError(w, http.StatusUnprocessableEntity, "invalid_repository", "The repository must match the product's GitHub repository.")
	default:
		var apiErr *integration.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "issue_not_found", "That GitHub issue or repository was not found.")
			return
		}
		s.log.Error("GitHub sync handler", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", fmt.Sprintf("GitHub synchronization failed: %v", err))
	}
}
