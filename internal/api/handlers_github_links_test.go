package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/orlandoburli/ai-company/internal/integration"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

// TestItemGitHubLinks lists the links of an item: several issues and several
// pull requests, of both kinds.
func TestItemGitHubLinks(t *testing.T) {
	h, fake := githubAPIHarnessWith(t)
	ownerCookie, _, tenantID := h.register("links-owner@example.com", "Links owner", "links-owner")
	sc := tenancy.Scope{TenantID: tenantID, Role: tenancy.RoleOwner}
	store := work.NewStore(h.db)
	products, err := store.CreateProduct(context.Background(), sc, "Demo", "demo", "https://github.com/acme/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(context.Background(), sc, products.ID, nil, "Project", "p")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := store.CreateItem(context.Background(), sc, work.NewItem{ProjectID: project.ID, Kind: "bug", Title: "no links"})
	if err != nil {
		t.Fatal(err)
	}
	fake.issues = []integration.Issue{
		{Number: 78, Title: "First issue", State: "open", HTMLURL: "https://github.test/acme/demo/issues/78"},
		{Number: 79, Title: "Second issue", State: "closed", HTMLURL: "https://github.test/acme/demo/issues/79"},
	}
	fake.prs = []integration.PullRequest{
		{Number: 80, Title: "First pull request", Body: "Fixes #78", State: "open",
			Head: "feature/one", Base: "main", HTMLURL: "https://github.test/acme/demo/pull/80"},
		{Number: 81, Title: "Second pull request", Body: "Fixes #78 and #79", State: "closed",
			Head: "feature/two", Base: "main", HTMLURL: "https://github.test/acme/demo/pull/81"},
	}
	if sync := h.do("POST", "/api/github/sync", nil, ownerCookie); sync.status != http.StatusOK {
		t.Fatalf("sync: %d %s", sync.status, sync.body)
	}
	items, err := store.ListItems(context.Background(), sc, work.ItemFilter{ProjectID: project.ID})
	if err != nil || len(items) != 3 {
		t.Fatalf("items: %+v %v", items, err)
	}
	// The item that carries the links is the one that owns issue 78.
	var linked work.Item
	for _, item := range items {
		if item.Title == "First issue" {
			linked = item
		}
	}
	if linked.ID == "" {
		t.Fatalf("no imported item: %+v", items)
	}

	response := h.do("GET", "/api/items/"+linked.ID+"/github-links", nil, ownerCookie)
	if response.status != http.StatusOK {
		t.Fatalf("links: %d %s", response.status, response.body)
	}
	rows, ok := response.json(t)["links"].([]any)
	if !ok {
		t.Fatalf("links body: %s", response.body)
	}
	if len(rows) != 3 {
		t.Fatalf("links: %s", response.body)
	}
	first := rows[0].(map[string]any)
	if first["kind"] != "issue" || first["repo"] != "acme/demo" || first["number"] != float64(78) ||
		first["title"] != "First issue" || first["url"] != "https://github.test/acme/demo/issues/78" ||
		first["state"] != "open" || first["head_branch"] != "" || first["base_branch"] != "" {
		t.Fatalf("issue link: %+v", first)
	}
	second := rows[1].(map[string]any)
	if second["kind"] != "pr" || second["repo"] != "acme/demo" || second["number"] != float64(80) ||
		second["title"] != "First pull request" || second["url"] != "https://github.test/acme/demo/pull/80" ||
		second["state"] != "open" || second["head_branch"] != "feature/one" || second["base_branch"] != "main" {
		t.Fatalf("pull request link: %+v", second)
	}
	third := rows[2].(map[string]any)
	if third["kind"] != "pr" || third["number"] != float64(81) || third["state"] != "closed" ||
		third["head_branch"] != "feature/two" || third["base_branch"] != "main" {
		t.Fatalf("second pull request link: %+v", third)
	}

	// An item without links has an empty list, not an error.
	empty := h.do("GET", "/api/items/"+plain.ID+"/github-links", nil, ownerCookie)
	if empty.status != http.StatusOK {
		t.Fatalf("empty links: %d %s", empty.status, empty.body)
	}
	if rows, ok := empty.json(t)["links"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("empty links body: %s", empty.body)
	}

	// Every role may read the links.
	guestCookie := h.addMember(tenantID, "links-guest@example.com", tenancy.RoleGuest)
	if guest := h.do("GET", "/api/items/"+linked.ID+"/github-links", nil, guestCookie); guest.status != http.StatusOK {
		t.Fatalf("guest links: %d %s", guest.status, guest.body)
	}

	// The item of another tenant is not there at all.
	foreignCookie, _, foreignTenant := h.register("links-other@example.com", "Links other", "links-other")
	foreignScope := tenancy.Scope{TenantID: foreignTenant, Role: tenancy.RoleOwner}
	foreignProduct, err := store.CreateProduct(context.Background(), foreignScope, "Foreign", "foreign", "https://github.com/acme/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	foreignProject, err := store.CreateProject(context.Background(), foreignScope, foreignProduct.ID, nil, "Foreign", "f")
	if err != nil {
		t.Fatal(err)
	}
	foreignItem, err := store.CreateItem(context.Background(), foreignScope, work.NewItem{ProjectID: foreignProject.ID, Kind: "bug", Title: "theirs"})
	if err != nil {
		t.Fatal(err)
	}
	hidden := h.do("GET", "/api/items/"+foreignItem.ID+"/github-links", nil, ownerCookie)
	if hidden.status != http.StatusNotFound || workflowErrCode(t, hidden) != "not_found" {
		t.Fatalf("cross-tenant item: %d %s", hidden.status, hidden.body)
	}
	if own := h.do("GET", "/api/items/"+foreignItem.ID+"/github-links", nil, foreignCookie); own.status != http.StatusOK {
		t.Fatalf("foreign owner: %d %s", own.status, own.body)
	}
}
