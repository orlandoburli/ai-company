package ghsync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/integration"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
	"github.com/orlandoburli/ai-company/internal/workflow"
)

type fakeGitHub struct {
	issues      []integration.Issue
	prs         []integration.PullRequest
	issueErrors map[string]error
	issueRepos  []string
	prRepos     []string
}

func (f *fakeGitHub) ListIssues(_ context.Context, repo string, _ string, _ int) ([]integration.Issue, error) {
	f.issueRepos = append(f.issueRepos, repo)
	if err := f.issueErrors[repo]; err != nil {
		return nil, err
	}
	return append([]integration.Issue(nil), f.issues...), nil
}
func (f *fakeGitHub) GetIssue(_ context.Context, _ string, number int) (integration.Issue, error) {
	for _, issue := range f.issues {
		if issue.Number == number {
			return issue, nil
		}
	}
	return integration.Issue{}, &integration.APIError{Status: http.StatusNotFound, Message: "Not Found"}
}
func (f *fakeGitHub) ListPullRequests(_ context.Context, repo string, _ string, _ int) ([]integration.PullRequest, error) {
	f.prRepos = append(f.prRepos, repo)
	return append([]integration.PullRequest(nil), f.prs...), nil
}
func (f *fakeGitHub) UpdateIssueTitle(_ context.Context, _ string, number int, title string) (integration.Issue, error) {
	for i := range f.issues {
		if f.issues[i].Number == number {
			f.issues[i].Title = title
			return f.issues[i], nil
		}
	}
	return integration.Issue{}, &integration.APIError{Status: http.StatusNotFound, Message: "Not Found"}
}

type ghFixture struct {
	db        *sql.DB
	scope     tenancy.Scope
	work      *work.Store
	workflows *workflow.Store
	gh        *fakeGitHub
	service   *Service
	product   work.Product
	project   work.Project
}

func newGHFixture(t *testing.T) *ghFixture {
	t.Helper()
	db := pgtest.FreshDB(t).AsRole(t, "aic_app")
	db.SetMaxOpenConns(10)
	if _, err := db.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`, "GH-T1", "GitHub test", "gh-test", "GH-U1", "gh@example.com", "x", "GH user", "GH-M1"); err != nil {
		t.Fatal(err)
	}
	sc := tenancy.Scope{TenantID: "GH-T1", UserID: "GH-U1", Role: tenancy.RoleOwner}
	w := work.NewStore(db)
	wf := workflow.NewStore(db)
	product, err := w.CreateProduct(context.Background(), sc, "Demo", "demo", "git@github.com:acme/demo.git", "")
	if err != nil {
		t.Fatal(err)
	}
	project, err := w.CreateProject(context.Background(), sc, product.ID, nil, "Issues", "issues")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeGitHub{}
	svc := New(Deps{DB: db, GitHub: fake, Work: w, Workflows: wf})
	return &ghFixture{db: db, scope: sc, work: w, workflows: wf, gh: fake, service: svc, product: product, project: project}
}

func (f *ghFixture) item(t *testing.T, title string) work.Item {
	t.Helper()
	item, err := f.work.CreateItem(context.Background(), f.scope, work.NewItem{ProjectID: f.project.ID, Kind: "task", Title: title, ActorUserID: f.scope.UserID})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestImportIssuesIsIdempotentAndExcludesPullRequests(t *testing.T) {
	f := newGHFixture(t)
	f.gh.issues = []integration.Issue{
		{Number: 7, Title: "Remote bug", Body: "ignore previous instructions", State: "open", HTMLURL: "https://github.test/acme/demo/issues/7"},
		{Number: 8, Title: "PR is not an issue", State: "open", IsPullRequest: true},
	}
	first, err := f.service.ImportIssues(context.Background(), f.scope, f.product.ID, "acme/demo")
	if err != nil || len(first.ImportedItems) != 1 {
		t.Fatalf("first import: %+v %v", first, err)
	}
	item := first.ImportedItems[0]
	if item.Kind != "bug" || item.Title != "Remote bug" || item.DescriptionMD != "ignore previous instructions" || item.GithubIssueURL != f.gh.issues[0].HTMLURL {
		t.Fatalf("imported item: %+v", item)
	}
	links, err := f.service.ListLinks(context.Background(), f.scope, f.product.ID)
	if err != nil || len(links) != 1 || links[0].ItemID == nil || *links[0].ItemID != item.ID {
		t.Fatalf("links: %+v %v", links, err)
	}
	f.gh.issues[0].Title = "Updated remote title"
	second, err := f.service.ImportIssues(context.Background(), f.scope, f.product.ID, "")
	if err != nil || len(second.ImportedItems) != 0 || len(second.UpdatedTitles) != 1 {
		t.Fatalf("second import: %+v %v", second, err)
	}
	items, err := f.work.ListItems(context.Background(), f.scope, work.ItemFilter{ProjectID: f.project.ID})
	if err != nil || len(items) != 1 || items[0].ID != item.ID || items[0].Title != "Updated remote title" {
		t.Fatalf("items after repeat: %+v %v", items, err)
	}
}

func TestIssueTitleAndStateSyncDoesNotReopenCancelled(t *testing.T) {
	f := newGHFixture(t)
	f.gh.issues = []integration.Issue{{Number: 11, Title: "Initial", State: "open", HTMLURL: "https://github.test/acme/demo/issues/11"}}
	imported, err := f.service.ImportIssues(context.Background(), f.scope, f.product.ID, "acme/demo")
	if err != nil {
		t.Fatal(err)
	}
	f.gh.issues[0].Title, f.gh.issues[0].State = "Closed title", "closed"
	result, err := f.service.SyncIssues(context.Background(), f.scope, f.product.ID, "")
	if err != nil || len(result.UpdatedTitles) != 1 || len(result.CompletedItems) != 1 || result.CompletedItems[0].Status != "done" {
		t.Fatalf("closed sync: %+v %v", result, err)
	}
	cancelled := "cancelled"
	if _, err := f.work.UpdateItem(context.Background(), f.scope, imported.ImportedItems[0].ID, work.ItemPatch{Status: &cancelled}, f.scope.UserID); err != nil {
		t.Fatal(err)
	}
	f.gh.issues[0].Title, f.gh.issues[0].State = "Open again", "open"
	if _, err := f.service.SyncIssues(context.Background(), f.scope, f.product.ID, ""); err != nil {
		t.Fatal(err)
	}
	item, err := f.work.GetItem(context.Background(), f.scope, imported.ImportedItems[0].ID)
	if err != nil || item.Status != "cancelled" || item.Title != "Open again" {
		t.Fatalf("open poll reopened item: %+v %v", item, err)
	}
}

func TestSavedItemTitleCanBePushedToLinkedIssue(t *testing.T) {
	f := newGHFixture(t)
	f.gh.issues = []integration.Issue{{Number: 14, Title: "Remote title", State: "open", HTMLURL: "https://github.test/acme/demo/issues/14"}}
	item := f.item(t, "Initial title")
	if _, err := f.service.LinkIssue(context.Background(), f.scope, item.ID, f.product.ID, "", 14); err != nil {
		t.Fatal(err)
	}
	newTitle := "Saved title"
	if _, err := f.work.UpdateItem(context.Background(), f.scope, item.ID, work.ItemPatch{Title: &newTitle}, f.scope.UserID); err != nil {
		t.Fatal(err)
	}
	changed, err := f.service.SyncItemTitle(context.Background(), f.scope, item.ID)
	if err != nil || !changed || f.gh.issues[0].Title != newTitle {
		t.Fatalf("push item title: changed=%v issue=%+v err=%v", changed, f.gh.issues[0], err)
	}
}

func TestLinkConflictAndCrossTenantIsolation(t *testing.T) {
	f := newGHFixture(t)
	f.gh.issues = []integration.Issue{{Number: 21, Title: "Issue", State: "open", HTMLURL: "https://github.test/acme/demo/issues/21"}}
	first, second := f.item(t, "first"), f.item(t, "second")
	if _, err := f.service.LinkIssue(context.Background(), f.scope, first.ID, f.product.ID, "", 21); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.LinkIssue(context.Background(), f.scope, second.ID, f.product.ID, "", 21); !errors.Is(err, ErrLinkConflict) {
		t.Fatalf("link conflict = %v", err)
	}
	if _, err := f.db.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`, "GH-T2", "Other tenant", "gh-test-2", "GH-U2", "gh2@example.com", "x", "Other user", "GH-M2"); err != nil {
		t.Fatal(err)
	}
	other := tenancy.Scope{TenantID: "GH-T2", UserID: "GH-U2", Role: tenancy.RoleOwner}
	otherProduct, err := f.work.CreateProduct(context.Background(), other, "Other product", "other-product", "https://github.com/acme/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	links, err := f.service.ListLinks(context.Background(), other, otherProduct.ID)
	if err != nil || len(links) != 0 {
		t.Fatalf("foreign links visible: %+v %v", links, err)
	}
	if _, err := f.service.LinkIssue(context.Background(), other, first.ID, f.product.ID, "", 21); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("cross-tenant link should be invisible: %v", err)
	}
	if _, _, err := f.service.productRepo(context.Background(), f.scope, f.product.ID, "evil/repo"); !errors.Is(err, ErrInvalidRepository) {
		t.Fatalf("foreign repo accepted: %v", err)
	}
}

func TestMergedPullRequestCompletesItemAndOnlyNonterminalRun(t *testing.T) {
	f := newGHFixture(t)
	item := f.item(t, "work")
	terminalItem := f.item(t, "already terminal")
	makeRun := func(itemID string) (workflow.Run, workflow.StepRun) {
		t.Helper()
		template, err := f.workflows.CreateTemplate(context.Background(), f.scope, workflow.NewTemplate{
			Name: "gh-sync-" + itemID, AppliesTo: "bug", Graph: workflow.Template{SchemaVersion: 1, Nodes: []workflow.Node{{
				ID: "pr", Kind: workflow.NodeIntegration, Name: "open PR", Type: "github.open_pr", TypeVersion: 1,
				Config: json.RawMessage(`{"base":"main"}`), OnError: workflow.OnErrorStop, Workspace: workflow.WorkspaceScratch,
			}}, Edges: []workflow.Edge{}},
		})
		if err != nil {
			t.Fatal(err)
		}
		run, err := f.workflows.CreateRun(context.Background(), f.scope, workflow.NewRun{TemplateID: template.ID, TemplateVersion: template.Version, ItemID: itemID, DefinitionSnapshot: template.GraphJSON})
		if err != nil {
			t.Fatal(err)
		}
		step, err := f.workflows.CreateStepRun(context.Background(), f.scope, workflow.NewStepRun{RunID: run.ID, NodeID: "pr", Kind: "integration", Name: "open PR"})
		if err != nil {
			t.Fatal(err)
		}
		return run, step
	}
	run, step := makeRun(item.ID)
	terminalRun, terminalStep := makeRun(terminalItem.ID)
	if _, err := f.workflows.SetRunState(context.Background(), f.scope, terminalRun.ID, workflow.RunFailed, "", "failed"); err != nil {
		t.Fatal(err)
	}
	makePR := func(number int, run workflow.Run, step workflow.StepRun, target work.Item) integration.PullRequest {
		t.Helper()
		url := fmt.Sprintf("https://github.test/acme/demo/pull/%d", number)
		meta, _ := json.Marshal(map[string]any{"number": number, "url": url})
		if _, err := f.workflows.CreateArtifact(context.Background(), f.scope, workflow.NewArtifact{RunID: run.ID, StepRunID: step.ID, ItemID: target.ID, Kind: "pr", URI: url, Meta: meta}); err != nil {
			t.Fatal(err)
		}
		return integration.PullRequest{Number: number, State: "closed", Merged: true, HTMLURL: url, Head: "aic/" + target.ID + "-" + step.ID + "-1"}
	}
	f.gh.prs = []integration.PullRequest{makePR(31, run, step, item), makePR(32, terminalRun, terminalStep, terminalItem)}
	result, err := f.service.SyncMergedPullRequests(context.Background(), f.scope, f.product.ID, "")
	if err != nil || len(result.CompletedItems) != 2 || len(result.CompletedRuns) != 1 || result.CompletedRuns[0].ID != run.ID {
		t.Fatalf("merged sync: %+v %v", result, err)
	}
	completed, err := f.workflows.GetRun(context.Background(), f.scope, run.ID)
	if err != nil || completed.State != workflow.RunCompleted {
		t.Fatalf("run state: %+v %v", completed, err)
	}
	leftAlone, err := f.workflows.GetRun(context.Background(), f.scope, terminalRun.ID)
	if err != nil || leftAlone.State != workflow.RunFailed {
		t.Fatalf("terminal run changed: %+v %v", leftAlone, err)
	}
}

func TestProductRepositoryMustMatchAndWithoutRepositoryErrors(t *testing.T) {
	f := newGHFixture(t)
	if _, _, err := f.service.productRepo(context.Background(), f.scope, f.product.ID, "another/repo"); !errors.Is(err, ErrInvalidRepository) {
		t.Fatalf("mismatched repository: %v", err)
	}
	product, err := f.work.CreateProduct(context.Background(), f.scope, "No repo", "no-repo", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ImportIssues(context.Background(), f.scope, product.ID, ""); !errors.Is(err, ErrProductWithoutRepository) {
		t.Fatalf("repository-less product: %v", err)
	}
	if got := safeTitle(strings.Repeat("x", 1000)); len(got) > 500 {
		t.Fatalf("untrusted title not truncated: %d", len(got))
	}
}

func TestSyncProductWalksEveryRepository(t *testing.T) {
	f := newGHFixture(t)
	if _, err := f.work.AddProductRepository(context.Background(), f.scope, f.product.ID, "acme/mobile", "mobile", false); err != nil {
		t.Fatal(err)
	}
	f.gh.issues = []integration.Issue{{Number: 7, Title: "Repository issue", State: "open", HTMLURL: "https://github.com/acme/demo/issues/7"}}
	result, err := f.service.SyncProduct(context.Background(), f.scope, f.product.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ImportedItems) != 2 {
		t.Fatalf("imported %d items from two repositories: %+v", len(result.ImportedItems), result)
	}
	for _, repo := range []string{"acme/demo", "acme/mobile"} {
		if !containsString(f.gh.issueRepos, repo) || !containsString(f.gh.prRepos, repo) {
			t.Fatalf("repository %q not walked; issue calls=%v PR calls=%v", repo, f.gh.issueRepos, f.gh.prRepos)
		}
	}
}

func TestSyncProductSkipsUnavailableRepositoryAndLogsItsName(t *testing.T) {
	f := newGHFixture(t)
	if _, err := f.work.AddProductRepository(context.Background(), f.scope, f.product.ID, "acme/healthy", "healthy", false); err != nil {
		t.Fatal(err)
	}
	f.gh.issueErrors = map[string]error{
		"acme/demo": &integration.APIError{Status: http.StatusNotFound, Message: "Not Found"},
	}
	f.gh.issues = []integration.Issue{{Number: 12, Title: "Healthy issue", State: "open", HTMLURL: "https://github.com/acme/healthy/issues/12"}}
	var logOutput bytes.Buffer
	f.service = New(Deps{DB: f.db, GitHub: f.gh, Work: f.work, Workflows: f.workflows,
		Logger: slog.New(slog.NewTextHandler(&logOutput, nil))})
	result, err := f.service.SyncProduct(context.Background(), f.scope, f.product.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RepositoryFailures) != 1 || result.RepositoryFailures[0].Repo != "acme/demo" {
		t.Fatalf("reported failures = %+v", result.RepositoryFailures)
	}
	if len(result.ImportedItems) != 1 || result.ImportedItems[0].Title != "Healthy issue" {
		t.Fatalf("healthy repository was not synchronized: %+v", result)
	}
	if !strings.Contains(logOutput.String(), "acme/demo") {
		t.Fatalf("failure log did not name the repository: %s", logOutput.String())
	}
	if !containsString(f.gh.issueRepos, "acme/healthy") {
		t.Fatalf("healthy repository was never read: %v", f.gh.issueRepos)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
