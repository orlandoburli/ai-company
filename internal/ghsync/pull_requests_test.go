package ghsync

import (
	"context"
	"testing"

	"github.com/orlandoburli/ai-company/internal/integration"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// linkByNumber finds a link of a product by the number it points at.
func linkByNumber(t *testing.T, links []Link, number int) Link {
	t.Helper()
	for _, link := range links {
		if link.IssueNumber == number {
			return link
		}
	}
	t.Fatalf("no link for number %d in %+v", number, links)
	return Link{}
}

func TestImportRecordsIssuesAndPullRequests(t *testing.T) {
	f := newGHFixture(t)
	f.gh.issues = []integration.Issue{
		{Number: 7, Title: "Remote bug", State: "open", HTMLURL: "https://github.test/acme/demo/issues/7"},
		// GitHub's issue listing includes pull requests; they are not issues.
		{Number: 8, Title: "A pull request in the issue listing", State: "open", IsPullRequest: true},
	}
	f.gh.prs = []integration.PullRequest{
		{Number: 21, Title: "Fix the bug", Body: "Fixes #7", State: "open",
			Head: "feature/fix", Base: "main", HTMLURL: "https://github.test/acme/demo/pull/21"},
		{Number: 22, Title: "Tidy up", Body: "No mention of an issue here", State: "open",
			Head: "feature/tidy", Base: "main", HTMLURL: "https://github.test/acme/demo/pull/22"},
	}
	result, err := f.service.ImportIssues(context.Background(), f.scope, f.product.ID, "acme/demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ImportedItems) != 1 {
		t.Fatalf("imported items: %+v", result.ImportedItems)
	}
	item := result.ImportedItems[0]

	links, err := f.service.ListLinks(context.Background(), f.scope, f.product.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 {
		t.Fatalf("links: %+v", links)
	}
	issue := linkByNumber(t, links, 7)
	if issue.Kind != linkKindIssue || issue.ItemID == nil || *issue.ItemID != item.ID {
		t.Fatalf("issue link: %+v", issue)
	}
	if issue.HeadBranch != "" || issue.BaseBranch != "" {
		t.Fatalf("an issue has no branches: %+v", issue)
	}
	pr := linkByNumber(t, links, 21)
	if pr.Kind != linkKindPR || pr.ItemID == nil || *pr.ItemID != item.ID {
		t.Fatalf("pull request link: %+v", pr)
	}
	if pr.IssueTitle != "Fix the bug" || pr.IssueURL != "https://github.test/acme/demo/pull/21" ||
		pr.IssueState != "open" || pr.HeadBranch != "feature/fix" || pr.BaseBranch != "main" {
		t.Fatalf("pull request link content: %+v", pr)
	}
	lonely := linkByNumber(t, links, 22)
	if lonely.Kind != linkKindPR || lonely.ItemID != nil {
		t.Fatalf("a pull request that mentions nothing stays unlinked: %+v", lonely)
	}
	if lonely.HeadBranch != "feature/tidy" || lonely.BaseBranch != "main" {
		t.Fatalf("an unlinked pull request keeps its branches: %+v", lonely)
	}
	// The pull request that GitHub listed as an issue is not an issue link.
	for _, link := range links {
		if link.IssueNumber == 8 {
			t.Fatalf("a pull request was imported as an issue: %+v", link)
		}
	}
}

func TestPullRequestIsLinkedToTheItemItMentions(t *testing.T) {
	f := newGHFixture(t)
	f.gh.issues = []integration.Issue{
		{Number: 7, Title: "Remote bug", State: "open", HTMLURL: "https://github.test/acme/demo/issues/7"},
	}
	imported, err := f.service.ImportIssues(context.Background(), f.scope, f.product.ID, "acme/demo")
	if err != nil {
		t.Fatal(err)
	}
	item := imported.ImportedItems[0]
	branched := f.item(t, "branched work")

	f.gh.prs = []integration.PullRequest{
		{Number: 31, Title: "Plain reference", Body: "Fixes #7", State: "open"},
		{Number: 32, Title: "Qualified reference", Body: "Fixes acme/demo#7", State: "open"},
		{Number: 33, Title: "Another repository", Body: "Fixes other/repo#7", State: "open"},
		{Number: 34, Title: "No reference", Body: "Nothing to see here", State: "open"},
		{Number: 35, Title: "Branch reference", Body: "", State: "open",
			Head: "aic/" + branched.ID + "-11111111-1111-1111-1111-111111111111-1"},
	}
	if _, err := f.service.SyncPullRequests(context.Background(), f.scope, f.product.ID, ""); err != nil {
		t.Fatal(err)
	}
	links, err := f.service.ListLinks(context.Background(), f.scope, f.product.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, number := range []int{31, 32} {
		link := linkByNumber(t, links, number)
		if link.ItemID == nil || *link.ItemID != item.ID {
			t.Fatalf("pull request %d should mention the item of issue 7: %+v", number, link)
		}
	}
	for _, number := range []int{33, 34} {
		link := linkByNumber(t, links, number)
		if link.ItemID != nil {
			t.Fatalf("pull request %d mentions nothing of this tenant: %+v", number, link)
		}
	}
	branchedLink := linkByNumber(t, links, 35)
	if branchedLink.ItemID == nil || *branchedLink.ItemID != branched.ID {
		t.Fatalf("a pull request from an item branch mentions that item: %+v", branchedLink)
	}
}

func TestPullRequestLinkIsReleasedWhenItStopsMentioning(t *testing.T) {
	f := newGHFixture(t)
	f.gh.issues = []integration.Issue{
		{Number: 7, Title: "Remote bug", State: "open", HTMLURL: "https://github.test/acme/demo/issues/7"},
	}
	f.gh.prs = []integration.PullRequest{
		{Number: 21, Title: "Fix the bug", Body: "Fixes #7", State: "open"},
	}
	if _, err := f.service.ImportIssues(context.Background(), f.scope, f.product.ID, ""); err != nil {
		t.Fatal(err)
	}
	links, err := f.service.ListLinks(context.Background(), f.scope, f.product.ID)
	if err != nil {
		t.Fatal(err)
	}
	if link := linkByNumber(t, links, 21); link.ItemID == nil {
		t.Fatalf("the pull request should be linked: %+v", link)
	}

	// The body is edited: the pull request mentions the item no longer.
	f.gh.prs[0].Body = "The mention is gone"
	if _, err := f.service.SyncPullRequests(context.Background(), f.scope, f.product.ID, ""); err != nil {
		t.Fatal(err)
	}
	links, err = f.service.ListLinks(context.Background(), f.scope, f.product.ID)
	if err != nil {
		t.Fatal(err)
	}
	link := linkByNumber(t, links, 21)
	if link.ItemID != nil {
		t.Fatalf("a link that no longer matches is released: %+v", link)
	}
	if link.Kind != linkKindPR || link.IssueTitle != "Fix the bug" {
		t.Fatalf("the released link is still recorded: %+v", link)
	}
}

func TestIssueKeepsItsLinkAcrossSyncs(t *testing.T) {
	f := newGHFixture(t)
	f.gh.issues = []integration.Issue{
		{Number: 7, Title: "Remote bug", State: "open", HTMLURL: "https://github.test/acme/demo/issues/7"},
	}
	first, err := f.service.ImportIssues(context.Background(), f.scope, f.product.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ImportedItems) != 1 {
		t.Fatalf("imported items: %+v", first.ImportedItems)
	}
	item := first.ImportedItems[0]
	second, err := f.service.ImportIssues(context.Background(), f.scope, f.product.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ImportedItems) != 0 {
		t.Fatalf("a linked issue must not be imported twice: %+v", second.ImportedItems)
	}
	links, err := f.service.ListLinks(context.Background(), f.scope, f.product.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("links: %+v", links)
	}
	if links[0].ItemID == nil || *links[0].ItemID != item.ID {
		t.Fatalf("the issue keeps its link: %+v", links[0])
	}
}

func TestPullRequestLinksAreTenantIsolated(t *testing.T) {
	f := newGHFixture(t)
	f.gh.issues = []integration.Issue{
		{Number: 7, Title: "Remote bug", State: "open", HTMLURL: "https://github.test/acme/demo/issues/7"},
	}
	f.gh.prs = []integration.PullRequest{
		{Number: 21, Title: "Fix the bug", Body: "Fixes #7", State: "open"},
	}
	imported, err := f.service.ImportIssues(context.Background(), f.scope, f.product.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	item := imported.ImportedItems[0]

	if _, err := f.db.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`,
		"GH-T2", "Other tenant", "gh-test-2", "GH-U2", "gh2@example.com", "x", "Other user", "GH-M2"); err != nil {
		t.Fatal(err)
	}
	other := tenancy.Scope{TenantID: "GH-T2", UserID: "GH-U2", Role: tenancy.RoleOwner}
	otherProduct, err := f.work.CreateProduct(context.Background(), other, "Other product", "other-product", "https://github.com/acme/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	otherLinks, err := f.service.ListLinks(context.Background(), other, otherProduct.ID)
	if err != nil || len(otherLinks) != 0 {
		t.Fatalf("foreign links visible: %+v %v", otherLinks, err)
	}
	// The other tenant's sync of the same repository sees nothing of the
	// first tenant's links.
	if _, err := f.service.SyncPullRequests(context.Background(), other, otherProduct.ID, ""); err != nil {
		t.Fatal(err)
	}
	otherLinks, err = f.service.ListLinks(context.Background(), other, otherProduct.ID)
	if err != nil || len(otherLinks) != 1 {
		t.Fatalf("the other tenant's own links: %+v %v", otherLinks, err)
	}
	if otherLinks[0].ItemID != nil {
		t.Fatalf("the other tenant's pull request mentions nothing of its own: %+v", otherLinks[0])
	}
	mine, err := f.service.ListItemLinks(context.Background(), f.scope, item.ID)
	if err != nil || len(mine) != 2 {
		t.Fatalf("the item's own links: %+v %v", mine, err)
	}
	theirs, err := f.service.ListItemLinks(context.Background(), other, item.ID)
	if err != nil || len(theirs) != 0 {
		t.Fatalf("foreign links visible: %+v %v", theirs, err)
	}
}

func TestReferencedIssues(t *testing.T) {
	cases := []struct {
		body string
		want []issueReference
	}{
		{"Fixes #12", []issueReference{{number: 12}}},
		{"fixes acme/demo#12", []issueReference{{repo: "acme/demo", number: 12}}},
		{"see acme/demo#12 and #13", []issueReference{{repo: "acme/demo", number: 12}, {number: 13}}},
		{"#0 is not a reference", nil},
		{"no reference here", nil},
		{"## a heading", nil},
	}
	for _, tc := range cases {
		got := referencedIssues(tc.body)
		if len(got) != len(tc.want) {
			t.Fatalf("%q -> %+v, want %+v", tc.body, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%q -> %+v, want %+v", tc.body, got, tc.want)
			}
		}
	}
}

func TestItemIDFromBranch(t *testing.T) {
	if got := itemIDFromBranch("aic/3F2504E0-4F89-41D3-9A0C-0305E82C3301-11111111-1111-1111-1111-111111111111-1"); got != "3F2504E0-4F89-41D3-9A0C-0305E82C3301" {
		t.Fatalf("item id from branch = %q", got)
	}
	if got := itemIDFromBranch("feature/whatever"); got != "" {
		t.Fatalf("item id from an unmanaged branch = %q", got)
	}
}
