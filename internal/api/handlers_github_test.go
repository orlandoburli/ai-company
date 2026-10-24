package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orlandoburli/ai-company/internal/ghsync"
	"github.com/orlandoburli/ai-company/internal/integration"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

type apiFakeGitHub struct {
	get    map[int]integration.Issue
	issues []integration.Issue
	prs    []integration.PullRequest
}

func (f *apiFakeGitHub) ListIssues(context.Context, string, string, int) ([]integration.Issue, error) {
	return append([]integration.Issue(nil), f.issues...), nil
}
func (f *apiFakeGitHub) GetIssue(_ context.Context, _ string, number int) (integration.Issue, error) {
	if issue, ok := f.get[number]; ok {
		return issue, nil
	}
	for _, issue := range f.issues {
		if issue.Number == number {
			return issue, nil
		}
	}
	return integration.Issue{}, &integration.APIError{Status: http.StatusNotFound, Message: "Not Found"}
}
func (f *apiFakeGitHub) ListPullRequests(context.Context, string, string, int) ([]integration.PullRequest, error) {
	return append([]integration.PullRequest(nil), f.prs...), nil
}
func (f *apiFakeGitHub) UpdateIssueTitle(_ context.Context, repo string, number int, title string) (integration.Issue, error) {
	issue, ok := f.get[number]
	if !ok {
		return integration.Issue{}, &integration.APIError{Status: http.StatusNotFound, Message: "Not Found"}
	}
	issue.Title = title
	issue.HTMLURL = "https://github.test/" + repo + "/issues/" + string(rune('0'+number))
	f.get[number] = issue
	return issue, nil
}

func githubAPIHarness(t *testing.T) *workflowHarness {
	t.Helper()
	h, _ := githubAPIHarnessWith(t)
	return h
}

// githubAPIHarnessWith builds the harness and hands back the fake GitHub
// client, so a test can seed the repository it syncs.
func githubAPIHarnessWith(t *testing.T) (*workflowHarness, *apiFakeGitHub) {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	app.SetMaxOpenConns(10)
	fake := &apiFakeGitHub{get: map[int]integration.Issue{
		78: {Number: 78, Title: "Issue to link", State: "open", HTMLURL: "https://github.test/acme/demo/issues/78"},
	}}
	service := ghsync.New(ghsync.Deps{DB: app, GitHub: fake, Work: work.NewStore(app), Workflows: workflow.NewStore(app)})
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSecureCookies(false), WithGitHubSyncService(service))
	mux := http.NewServeMux()
	s.registerGitHubRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &workflowHarness{t: t, db: app, admin: db, srv: srv}, fake
}

func TestGitHubHTTPRolesLinkConflictAndTenant404(t *testing.T) {
	h := githubAPIHarness(t)
	ownerCookie, _, tenantID := h.register("gh-owner@example.com", "GitHub owner", "gh-owner")
	sc := tenancy.Scope{TenantID: tenantID, Role: tenancy.RoleOwner}
	products, err := work.NewStore(h.db).CreateProduct(context.Background(), sc, "Demo", "demo", "https://github.com/acme/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	project, err := work.NewStore(h.db).CreateProject(context.Background(), sc, products.ID, nil, "Project", "p")
	if err != nil {
		t.Fatal(err)
	}
	first, err := work.NewStore(h.db).CreateItem(context.Background(), sc, work.NewItem{ProjectID: project.ID, Kind: "bug", Title: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := work.NewStore(h.db).CreateItem(context.Background(), sc, work.NewItem{ProjectID: project.ID, Kind: "bug", Title: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if sync := h.do("POST", "/api/github/sync", nil, ownerCookie); sync.status != http.StatusOK {
		t.Fatalf("owner sync: %d %s", sync.status, sync.body)
	}
	memberCookie := h.addMember(tenantID, "gh-member@example.com", tenancy.RoleMember)
	guestCookie := h.addMember(tenantID, "gh-guest@example.com", tenancy.RoleGuest)
	if denied := h.do("POST", "/api/github/sync", nil, memberCookie); denied.status != http.StatusForbidden {
		t.Fatalf("member sync: %d %s", denied.status, denied.body)
	}
	if denied := h.do("POST", "/api/items/"+first.ID+"/github-link", map[string]any{"product_id": products.ID, "issue_number": 78}, guestCookie); denied.status != http.StatusForbidden {
		t.Fatalf("guest link: %d %s", denied.status, denied.body)
	}
	linked := h.do("POST", "/api/items/"+first.ID+"/github-link", map[string]any{"product_id": products.ID, "issue_number": 78}, memberCookie)
	if linked.status != http.StatusCreated {
		t.Fatalf("member link: %d %s", linked.status, linked.body)
	}
	conflict := h.do("POST", "/api/items/"+second.ID+"/github-link", map[string]any{"product_id": products.ID, "issue_number": 78}, ownerCookie)
	if conflict.status != http.StatusConflict || workflowErrCode(t, conflict) != "github_link_conflict" {
		t.Fatalf("link conflict: %d %s", conflict.status, conflict.body)
	}
	missingIssue := h.do("POST", "/api/items/"+second.ID+"/github-link", map[string]any{"product_id": products.ID, "issue_number": 999}, ownerCookie)
	if missingIssue.status != http.StatusNotFound || workflowErrCode(t, missingIssue) != "issue_not_found" {
		t.Fatalf("missing issue: %d %s", missingIssue.status, missingIssue.body)
	}
	list := h.do("GET", "/api/github/issues?product_id="+products.ID, nil, ownerCookie)
	if list.status != http.StatusOK || len(list.json(t)["links"].([]any)) != 1 {
		t.Fatalf("list links: %d %s", list.status, list.body)
	}
	foreignCookie, _, foreignTenant := h.register("gh-other@example.com", "Other", "gh-other")
	foreignScope := tenancy.Scope{TenantID: foreignTenant, Role: tenancy.RoleOwner}
	foreign, err := work.NewStore(h.db).CreateProduct(context.Background(), foreignScope, "Foreign", "foreign", "https://github.com/acme/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if hidden := h.do("GET", "/api/github/issues?product_id="+foreign.ID, nil, ownerCookie); hidden.status != http.StatusNotFound || workflowErrCode(t, hidden) != "item_not_found" {
		t.Fatalf("cross-tenant product read: %d %s", hidden.status, hidden.body)
	}
	if own := h.do("GET", "/api/github/issues?product_id="+foreign.ID, nil, foreignCookie); own.status != http.StatusOK {
		t.Fatalf("foreign owner list: %d %s", own.status, own.body)
	}
}

func TestGitHubSyncRequiresCredential(t *testing.T) {
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSecureCookies(false))
	mux := http.NewServeMux()
	s.registerGitHubRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	h := &workflowHarness{t: t, db: app, admin: db, srv: srv}
	cookie, _, _ := h.register("gh-no-credential@example.com", "No credential", "gh-no-credential")
	result := h.do("POST", "/api/github/sync", nil, cookie)
	if result.status != http.StatusServiceUnavailable || workflowErrCode(t, result) != "github_not_configured" {
		t.Fatalf("missing credential: %d %s", result.status, result.body)
	}
}
