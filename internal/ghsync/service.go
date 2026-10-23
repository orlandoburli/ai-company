// Package ghsync imports and synchronizes tenant work items with GitHub.
package ghsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/orlandoburli/ai-company/internal/integration"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

var (
	ErrProductNotFound          = errors.New("ghsync: product not found")
	ErrProductWithoutRepository = errors.New("ghsync: product has no GitHub repository")
	ErrIssueNotFound            = errors.New("ghsync: issue not found")
	ErrItemNotFound             = errors.New("ghsync: item not found")
	ErrLinkConflict             = errors.New("ghsync: issue is linked to another item")
	ErrGitHubNotConfigured      = errors.New("ghsync: GitHub is not configured")
	ErrInvalidRepository        = errors.New("ghsync: invalid repository")
)

// GitHubService is the repo-aware subset required by the sync service.
// Repo names are canonical owner/name values, not caller-supplied URLs.
type GitHubService interface {
	ListIssues(context.Context, string, string, int) ([]integration.Issue, error)
	GetIssue(context.Context, string, int) (integration.Issue, error)
	ListPullRequests(context.Context, string, string, int) ([]integration.PullRequest, error)
	UpdateIssueTitle(context.Context, string, int, string) (integration.Issue, error)
}

type Deps struct {
	DB        *sql.DB
	GitHub    GitHubService
	Work      *work.Store
	Workflows *workflow.Store
	Logger    *slog.Logger
}

type Service struct {
	db        *sql.DB
	github    GitHubService
	work      *work.Store
	workflows *workflow.Store
	logger    *slog.Logger
}

func New(deps Deps) *Service {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: deps.DB, github: deps.GitHub, work: deps.Work,
		workflows: deps.Workflows, logger: logger}
}

// Link is the tenant-scoped mapping between one GitHub issue and an item.
type Link struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenant_id"`
	ProductID    string     `json:"product_id"`
	Repo         string     `json:"repo"`
	IssueNumber  int        `json:"issue_number"`
	ItemID       *string    `json:"item_id,omitempty"`
	IssueState   string     `json:"issue_state"`
	IssueTitle   string     `json:"issue_title"`
	IssueURL     string     `json:"issue_url"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Result names the individual changes so callers can report exactly what a
// periodic or manual synchronization did.
type Result struct {
	ImportedItems  []work.Item    `json:"imported_items"`
	UpdatedTitles  []work.Item    `json:"updated_titles"`
	CompletedItems []work.Item    `json:"completed_items"`
	CompletedRuns  []workflow.Run `json:"completed_runs"`
}

func emptyResult() Result {
	return Result{ImportedItems: []work.Item{}, UpdatedTitles: []work.Item{},
		CompletedItems: []work.Item{}, CompletedRuns: []workflow.Run{}}
}

func (s *Service) ImportIssues(ctx context.Context, sc tenancy.Scope, productID, repo string) (Result, error) {
	repo, _, err := s.productRepo(ctx, sc, productID, repo)
	if err != nil {
		return Result{}, err
	}
	if s.github == nil {
		return Result{}, ErrGitHubNotConfigured
	}
	issues, err := s.github.ListIssues(ctx, repo, "all", 100)
	if err != nil {
		return Result{}, err
	}
	out := emptyResult()
	for _, issue := range issues {
		if issue.IsPullRequest || issue.Number <= 0 {
			continue
		}
		link, found, err := s.getLink(ctx, sc, repo, issue.Number)
		if err != nil {
			return out, err
		}
		created := false
		var item work.Item
		if found && link.ItemID != nil {
			item, err = s.work.GetItem(ctx, sc, *link.ItemID)
			if err != nil {
				if errors.Is(err, work.ErrNotFound) {
					link.ItemID = nil
				} else {
					return out, err
				}
			}
		}
		if link.ItemID == nil {
			projectID, err := s.projectForImport(ctx, sc, productID)
			if err != nil {
				return out, err
			}
			status := "backlog"
			if issue.State == "closed" {
				status = "done"
			}
			item, err = s.work.CreateItem(ctx, sc, work.NewItem{
				ProjectID: projectID, Kind: "bug", Title: safeTitle(issue.Title),
				DescriptionMD: safeBody(issue.Body), Status: status,
				GithubIssueURL: safeURL(issue.HTMLURL), ActorUserID: sc.UserID,
			})
			if err != nil {
				return out, err
			}
			created = true
		}
		item, changedTitle, completed, err := s.syncIssueItem(ctx, sc, item, issue)
		if err != nil {
			return out, err
		}
		link, err = s.saveLink(ctx, sc, link, found, productID, repo, issue, item.ID)
		if err != nil {
			return out, err
		}
		_ = link
		if created {
			out.ImportedItems = append(out.ImportedItems, item)
		}
		if changedTitle {
			out.UpdatedTitles = append(out.UpdatedTitles, item)
		}
		if completed {
			out.CompletedItems = append(out.CompletedItems, item)
		}
	}
	return out, nil
}

func (s *Service) SyncIssues(ctx context.Context, sc tenancy.Scope, productID, repo string) (Result, error) {
	repo, _, err := s.productRepo(ctx, sc, productID, repo)
	if err != nil {
		return Result{}, err
	}
	if s.github == nil {
		return Result{}, ErrGitHubNotConfigured
	}
	issues, err := s.github.ListIssues(ctx, repo, "all", 100)
	if err != nil {
		return Result{}, err
	}
	remote := make(map[int]integration.Issue, len(issues))
	for _, issue := range issues {
		if !issue.IsPullRequest {
			remote[issue.Number] = issue
		}
	}
	links, err := s.listLinks(ctx, sc, productID, repo)
	if err != nil {
		return Result{}, err
	}
	out := emptyResult()
	for _, link := range links {
		if link.ItemID == nil {
			continue
		}
		issue, ok := remote[link.IssueNumber]
		if !ok {
			continue
		}
		item, err := s.work.GetItem(ctx, sc, *link.ItemID)
		if errors.Is(err, work.ErrNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		item, titleChanged, completed, err := s.syncIssueItem(ctx, sc, item, issue)
		if err != nil {
			return out, err
		}
		if _, err := s.saveLink(ctx, sc, link, true, productID, repo, issue, item.ID); err != nil {
			return out, err
		}
		if titleChanged {
			out.UpdatedTitles = append(out.UpdatedTitles, item)
		}
		if completed {
			out.CompletedItems = append(out.CompletedItems, item)
		}
	}
	return out, nil
}

// LinkIssue associates an existing item in productID with a real GitHub issue.
func (s *Service) LinkIssue(ctx context.Context, sc tenancy.Scope, itemID, productID, repo string, issueNumber int) (Link, error) {
	repo, _, err := s.productRepo(ctx, sc, productID, repo)
	if err != nil {
		return Link{}, err
	}
	if s.github == nil {
		return Link{}, ErrGitHubNotConfigured
	}
	item, err := s.work.GetItem(ctx, sc, itemID)
	if errors.Is(err, work.ErrNotFound) {
		return Link{}, ErrItemNotFound
	}
	if err != nil {
		return Link{}, err
	}
	projects, err := s.work.ListProjects(ctx, sc, productID)
	if err != nil {
		return Link{}, err
	}
	inProduct := false
	for _, project := range projects {
		if project.ID == item.ProjectID {
			inProduct = true
			break
		}
	}
	if !inProduct {
		return Link{}, ErrItemNotFound
	}
	issue, err := s.github.GetIssue(ctx, repo, issueNumber)
	if err != nil {
		if isGitHubNotFound(err) {
			return Link{}, ErrIssueNotFound
		}
		return Link{}, err
	}
	if issue.IsPullRequest || issue.Number != issueNumber {
		return Link{}, ErrIssueNotFound
	}
	link, found, err := s.getLink(ctx, sc, repo, issueNumber)
	if err != nil {
		return Link{}, err
	}
	if found && link.ItemID != nil && *link.ItemID != itemID {
		return Link{}, ErrLinkConflict
	}
	item, _, _, err = s.syncIssueItem(ctx, sc, item, issue)
	if err != nil {
		return Link{}, err
	}
	return s.saveLink(ctx, sc, link, found, productID, repo, issue, item.ID)
}

// SyncItemTitle pushes a saved item's title to its linked GitHub issue. Call it
// after work.Store.UpdateItem succeeds; it is deliberately separate from the
// database item update because GitHub is an external side effect.
func (s *Service) SyncItemTitle(ctx context.Context, sc tenancy.Scope, itemID string) (bool, error) {
	if s.github == nil {
		return false, ErrGitHubNotConfigured
	}
	item, err := s.work.GetItem(ctx, sc, itemID)
	if errors.Is(err, work.ErrNotFound) {
		return false, ErrItemNotFound
	}
	if err != nil {
		return false, err
	}
	link, err := s.linkForItem(ctx, sc, itemID)
	if err != nil {
		return false, err
	}
	if link == nil {
		return false, nil
	}
	if link.IssueTitle == item.Title {
		return false, nil
	}
	issue, err := s.github.UpdateIssueTitle(ctx, link.Repo, link.IssueNumber, safeTitle(item.Title))
	if err != nil {
		return false, err
	}
	if _, err := s.saveLink(ctx, sc, *link, true, link.ProductID, link.Repo, issue, itemID); err != nil {
		return false, err
	}
	return true, nil
}

// ListLinks returns the issue mappings for a product in the caller's tenant.
func (s *Service) ListLinks(ctx context.Context, sc tenancy.Scope, productID string) ([]Link, error) {
	products, err := s.work.ListProducts(ctx, sc)
	if err != nil {
		return nil, err
	}
	found := false
	for _, product := range products {
		if product.ID == productID {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrProductNotFound
	}
	return s.listLinks(ctx, sc, productID, "")
}

func (s *Service) SyncMergedPullRequests(ctx context.Context, sc tenancy.Scope, productID, repo string) (Result, error) {
	repo, _, err := s.productRepo(ctx, sc, productID, repo)
	if err != nil {
		return Result{}, err
	}
	if s.github == nil {
		return Result{}, ErrGitHubNotConfigured
	}
	prs, err := s.github.ListPullRequests(ctx, repo, "merged", 100)
	if err != nil {
		return Result{}, err
	}
	out := emptyResult()
	for _, pr := range prs {
		if !pr.Merged {
			continue
		}
		itemIDs, runIDs, err := s.mergedPRTargets(ctx, sc, productID, repo, pr)
		if err != nil {
			return out, err
		}
		for _, itemID := range itemIDs {
			item, err := s.work.GetItem(ctx, sc, itemID)
			if errors.Is(err, work.ErrNotFound) {
				continue
			}
			if err != nil {
				return out, err
			}
			if item.Status != "done" && item.Status != "cancelled" {
				status := "done"
				item, err = s.work.UpdateItem(ctx, sc, item.ID, work.ItemPatch{Status: &status}, "")
				if err != nil {
					return out, err
				}
				out.CompletedItems = append(out.CompletedItems, item)
			}
		}
		for _, runID := range runIDs {
			run, err := s.workflows.GetRun(ctx, sc, runID)
			if errors.Is(err, workflow.ErrNotFound) {
				continue
			}
			if err != nil {
				return out, err
			}
			if run.State == workflow.RunCompleted || run.State == workflow.RunFailed || run.State == workflow.RunCancelled {
				continue
			}
			run, err = s.workflows.SetRunState(ctx, sc, run.ID, workflow.RunCompleted, "", "")
			if err != nil {
				return out, err
			}
			out.CompletedRuns = append(out.CompletedRuns, run)
		}
	}
	return out, nil
}

func (s *Service) productRepo(ctx context.Context, sc tenancy.Scope, productID, requested string) (string, work.Product, error) {
	if s.work == nil || s.db == nil {
		return "", work.Product{}, errors.New("ghsync: stores are not configured")
	}
	products, err := s.work.ListProducts(ctx, sc)
	if err != nil {
		return "", work.Product{}, err
	}
	var product work.Product
	for _, candidate := range products {
		if candidate.ID == productID {
			product = candidate
			break
		}
	}
	if product.ID == "" {
		return "", work.Product{}, ErrProductNotFound
	}
	repo, err := parseRepo(product.RepoURL)
	if err != nil || repo == "" {
		return "", work.Product{}, ErrProductWithoutRepository
	}
	if strings.TrimSpace(requested) != "" {
		wanted, err := parseRepo(requested)
		if err != nil {
			return "", work.Product{}, ErrInvalidRepository
		}
		if wanted != repo {
			return "", work.Product{}, ErrInvalidRepository
		}
	}
	return repo, product, nil
}

func parseRepo(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	value = strings.TrimSuffix(value, ".git")
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil {
			return "", err
		}
		if u.Host != "github.com" && u.Host != "www.github.com" {
			return "", ErrInvalidRepository
		}
		value = strings.Trim(u.Path, "/")
	} else if strings.Contains(value, ":") {
		// SCP-style git@github.com:owner/repo remotes.
		prefix, suffix, ok := strings.Cut(value, ":")
		if ok {
			if !strings.HasSuffix(prefix, "@github.com") && prefix != "github.com" {
				return "", ErrInvalidRepository
			}
			value = suffix
		}
	}
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || !repoPart.MatchString(parts[0]) || !repoPart.MatchString(parts[1]) {
		return "", ErrInvalidRepository
	}
	return parts[0] + "/" + parts[1], nil
}

var repoPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func (s *Service) projectForImport(ctx context.Context, sc tenancy.Scope, productID string) (string, error) {
	projects, err := s.work.ListProjects(ctx, sc, productID)
	if err != nil {
		return "", err
	}
	if len(projects) != 0 {
		return projects[0].ID, nil
	}
	project, err := s.work.CreateProject(ctx, sc, productID, nil, "GitHub Issues", "github")
	if err != nil {
		return "", err
	}
	return project.ID, nil
}

func (s *Service) syncIssueItem(ctx context.Context, sc tenancy.Scope, item work.Item, issue integration.Issue) (work.Item, bool, bool, error) {
	title := safeTitle(issue.Title)
	titleChanged := title != item.Title
	completed := false
	patch := work.ItemPatch{}
	if titleChanged {
		patch.Title = &title
	}
	if issue.State == "closed" && item.Status != "done" && item.Status != "cancelled" {
		status := "done"
		patch.Status = &status
		completed = true
	}
	if patch.Title == nil && patch.Status == nil {
		return item, false, false, nil
	}
	updated, err := s.work.UpdateItem(ctx, sc, item.ID, patch, "")
	return updated, titleChanged, completed, err
}

func (s *Service) getLink(ctx context.Context, sc tenancy.Scope, repo string, number int) (Link, bool, error) {
	var link Link
	found := false
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var itemID sql.NullString
		var synced sql.NullTime
		err := tx.QueryRowContext(ctx, `SELECT id,tenant_id,product_id,repo,issue_number,item_id,issue_state,issue_title,issue_url,last_synced_at,created_at,updated_at
			FROM github_issue_links WHERE repo=$1 AND issue_number=$2`, repo, number).
			Scan(&link.ID, &link.TenantID, &link.ProductID, &link.Repo, &link.IssueNumber, &itemID,
				&link.IssueState, &link.IssueTitle, &link.IssueURL, &synced, &link.CreatedAt, &link.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if itemID.Valid {
			link.ItemID = &itemID.String
		}
		if synced.Valid {
			link.LastSyncedAt = &synced.Time
		}
		found = true
		return nil
	})
	return link, found, err
}

func (s *Service) listLinks(ctx context.Context, sc tenancy.Scope, productID, repo string) ([]Link, error) {
	out := []Link{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,tenant_id,product_id,repo,issue_number,item_id,issue_state,issue_title,issue_url,last_synced_at,created_at,updated_at
			FROM github_issue_links WHERE product_id=$1 AND ($2='' OR repo=$2) ORDER BY issue_number`, productID, repo)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			link, err := scanLink(rows)
			if err != nil {
				return err
			}
			out = append(out, link)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) linkForItem(ctx context.Context, sc tenancy.Scope, itemID string) (*Link, error) {
	var result *Link
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		row, err := scanLink(tx.QueryRowContext(ctx, `SELECT id,tenant_id,product_id,repo,issue_number,item_id,issue_state,issue_title,issue_url,last_synced_at,created_at,updated_at
			FROM github_issue_links WHERE item_id=$1 ORDER BY updated_at DESC LIMIT 1`, itemID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result = &row
		return nil
	})
	return result, err
}

func (s *Service) saveLink(ctx context.Context, sc tenancy.Scope, old Link, found bool, productID, repo string, issue integration.Issue, itemID string) (Link, error) {
	issue.State = normalizedIssueState(issue.State)
	issue.Title = safeTitle(issue.Title)
	issue.HTMLURL = safeURL(issue.HTMLURL)
	var out Link
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if found {
			var item sql.NullString
			var synced sql.NullTime
			return tx.QueryRowContext(ctx, `UPDATE github_issue_links SET item_id=$2,issue_state=$3,issue_title=$4,issue_url=$5,last_synced_at=now(),updated_at=now()
				WHERE id=$1 RETURNING id,tenant_id,product_id,repo,issue_number,item_id,issue_state,issue_title,issue_url,last_synced_at,created_at,updated_at`,
				old.ID, itemID, issue.State, issue.Title, issue.HTMLURL).Scan(&out.ID, &out.TenantID, &out.ProductID,
				&out.Repo, &out.IssueNumber, &item, &out.IssueState, &out.IssueTitle, &out.IssueURL,
				&synced, &out.CreatedAt, &out.UpdatedAt)
		}
		var item sql.NullString
		var synced sql.NullTime
		return tx.QueryRowContext(ctx, `INSERT INTO github_issue_links (id,tenant_id,product_id,repo,issue_number,item_id,issue_state,issue_title,issue_url,last_synced_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
			RETURNING id,tenant_id,product_id,repo,issue_number,item_id,issue_state,issue_title,issue_url,last_synced_at,created_at,updated_at`,
			uuid.NewString(), sc.TenantID, productID, repo, issue.Number, itemID, issue.State, issue.Title, issue.HTMLURL).
			Scan(&out.ID, &out.TenantID, &out.ProductID, &out.Repo, &out.IssueNumber, &item,
				&out.IssueState, &out.IssueTitle, &out.IssueURL, &synced, &out.CreatedAt, &out.UpdatedAt)
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return Link{}, ErrLinkConflict
		}
		return Link{}, err
	}
	if itemID != "" {
		out.ItemID = &itemID
	}
	if out.LastSyncedAt == nil {
		now := time.Now().UTC()
		out.LastSyncedAt = &now
	}
	return out, nil
}

func (s *Service) mergedPRTargets(ctx context.Context, sc tenancy.Scope, productID, repo string, pr integration.PullRequest) ([]string, []string, error) {
	items := map[string]bool{}
	runs := map[string]bool{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if pr.Head != "" {
			rows, err := tx.QueryContext(ctx, `SELECT DISTINCT i.id, sr.run_id
				FROM items i JOIN projects p ON p.id=i.project_id AND p.tenant_id=i.tenant_id
				JOIN step_runs sr ON sr.tenant_id=i.tenant_id
				JOIN artifacts a ON a.step_run_id=sr.id AND a.tenant_id=sr.tenant_id AND a.kind='pr'
				WHERE p.product_id=$1 AND sr.run_id IS NOT NULL
				  AND $2 = 'aic/' || i.id || '-' || sr.id || '-' || substring($2 from length('aic/' || i.id || '-' || sr.id || '-') + 1)
				  AND substring($2 from length('aic/' || i.id || '-' || sr.id || '-') + 1) ~ '^[0-9]+$'
				  AND (a.meta_json->>'number'=$3 OR a.uri=$4)`, productID, pr.Head, fmt.Sprint(pr.Number), pr.HTMLURL)
			if err != nil {
				return err
			}
			for rows.Next() {
				var itemID, runID string
				if err := rows.Scan(&itemID, &runID); err != nil {
					rows.Close()
					return err
				}
				items[itemID], runs[runID] = true, true
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		}
		// A manually linked issue is an alternate item reference when the branch
		// is unavailable (for example a PR created outside this service).
		var itemID sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT item_id FROM github_issue_links WHERE repo=$1 AND issue_number=$2`, repo, pr.Number).Scan(&itemID)
		if err == nil && itemID.Valid {
			items[itemID.String] = true
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Artifact metadata remains authoritative for the run even when branch
		// resolution is not possible.
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT run_id FROM artifacts
			WHERE kind='pr' AND (meta_json->>'number'=$1 OR uri=$2)`, fmt.Sprint(pr.Number), pr.HTMLURL)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var runID sql.NullString
			if err := rows.Scan(&runID); err != nil {
				return err
			}
			if runID.Valid {
				runs[runID.String] = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	itemIDs, runIDs := make([]string, 0, len(items)), make([]string, 0, len(runs))
	for id := range items {
		itemIDs = append(itemIDs, id)
	}
	for id := range runs {
		runIDs = append(runIDs, id)
	}
	return itemIDs, runIDs, nil
}

func (s *Service) withTenant(ctx context.Context, sc tenancy.Scope, fn func(context.Context, *sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	if s.db == nil {
		return errors.New("ghsync: database is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, sc.TenantID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

type rowScanner interface{ Scan(...any) error }

func scanLink(row rowScanner) (Link, error) {
	var link Link
	var itemID sql.NullString
	var synced sql.NullTime
	err := row.Scan(&link.ID, &link.TenantID, &link.ProductID, &link.Repo, &link.IssueNumber,
		&itemID, &link.IssueState, &link.IssueTitle, &link.IssueURL, &synced, &link.CreatedAt, &link.UpdatedAt)
	if itemID.Valid {
		link.ItemID = &itemID.String
	}
	if synced.Valid {
		link.LastSyncedAt = &synced.Time
	}
	return link, err
}

func normalizedIssueState(state string) string {
	if strings.EqualFold(state, "closed") {
		return "closed"
	}
	return "open"
}

func safeTitle(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if value == "" {
		return "Untitled GitHub issue"
	}
	return truncateUTF8(value, 500)
}

func safeBody(value string) string { return truncateUTF8(strings.ToValidUTF8(value, "�"), 200<<10) }

func safeURL(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 2048 {
		return truncateUTF8(value, 2048)
	}
	return value
}

func truncateUTF8(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func isGitHubNotFound(err error) bool {
	var apiErr *integration.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 404
}

// Ensure the JSON fields stored in artifacts are not accidentally evaluated;
// this helper also makes malformed metadata safe to ignore in future callers.
func decodeArtifactMeta(raw []byte) map[string]any {
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	return value
}
