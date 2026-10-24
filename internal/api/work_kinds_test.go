package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The kinds a project offers, the labels in use, and the item's family.
//
// The store tests cover the SQL guarantees; these cover what only the request
// path can get wrong: the role gates, the wire shapes, and the fact that a
// guest sees the same vocabulary as everybody else.

func TestProjectKindsAreSeededAndReplaceable(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	res := o.do("GET", "/api/projects/"+projectID+"/kinds", nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("get kinds: %d %s", res.status, res.body)
	}
	kinds := res.json(t)["kinds"].([]any)
	want := []string{"task", "bug", "epic", "story", "spike"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %s; want %v", res.body, want)
	}
	for i, raw := range kinds {
		k := raw.(map[string]any)
		if k["name"] != want[i] {
			t.Fatalf("kind %d = %v; want %q", i, k["name"], want[i])
		}
		if k["position"] != float64(i) {
			t.Fatalf("kind %q position = %v; want %d", k["name"], k["position"], i)
		}
	}

	// Replace the list, in the order given.
	put := o.do("PUT", "/api/projects/"+projectID+"/kinds",
		map[string]any{"kinds": []string{"epic", "task"}}, o.ownerCookie)
	if put.status != http.StatusOK {
		t.Fatalf("put kinds: %d %s", put.status, put.body)
	}
	kinds = put.json(t)["kinds"].([]any)
	if len(kinds) != 2 {
		t.Fatalf("kinds = %s; want two", put.body)
	}
	if kinds[0].(map[string]any)["name"] != "epic" ||
		kinds[0].(map[string]any)["position"] != float64(0) ||
		kinds[1].(map[string]any)["name"] != "task" ||
		kinds[1].(map[string]any)["position"] != float64(1) {
		t.Fatalf("kinds = %s; want epic then task", put.body)
	}

	// The same list may be sent back in the shape the endpoint returns.
	put = o.do("PUT", "/api/projects/"+projectID+"/kinds",
		map[string]any{"kinds": []map[string]any{{"name": "chore"}}}, o.ownerCookie)
	if put.status != http.StatusOK {
		t.Fatalf("put kinds as objects: %d %s", put.status, put.body)
	}
	kinds = put.json(t)["kinds"].([]any)
	if len(kinds) != 1 || kinds[0].(map[string]any)["name"] != "chore" {
		t.Fatalf("kinds = %s; want chore", put.body)
	}

	// An empty list clears the project.
	put = o.do("PUT", "/api/projects/"+projectID+"/kinds",
		map[string]any{"kinds": []string{}}, o.ownerCookie)
	if put.status != http.StatusOK {
		t.Fatalf("clear kinds: %d %s", put.status, put.body)
	}
	if got := put.json(t)["kinds"].([]any); len(got) != 0 {
		t.Fatalf("kinds = %s; want none", put.body)
	}

	// The list is what the interface offers, not a constraint on the data: an
	// item keeps a kind the project no longer lists.
	item := o.createItem(o.ownerCookie, projectID, "Still a bug", map[string]any{"kind": "bug"})
	if item["kind"] != "bug" {
		t.Fatalf("the item's kind = %v; want bug", item["kind"])
	}
}

func TestOnlyOwnerAndAdminMayChangeProjectKinds(t *testing.T) {
	o := newOwnerHarness(t)
	tenantID := o.tenantID(o.ownerCookie)
	_, projectID := o.createProductProject(o.ownerCookie)

	o.addMember(tenantID, "admin@example.com", tenancy.RoleAdmin)
	admin := o.loginTo("admin@example.com", tenantID)
	o.addMember(tenantID, "member@example.com", tenancy.RoleMember)
	member := o.loginTo("member@example.com", tenantID)
	o.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)
	guest := o.loginTo("guest@example.com", tenantID)

	// Everybody may read the list.
	for name, cookie := range map[string]string{"admin": admin, "member": member, "guest": guest} {
		res := o.do("GET", "/api/projects/"+projectID+"/kinds", nil, cookie)
		if res.status != http.StatusOK {
			t.Fatalf("%s read kinds: %d %s", name, res.status, res.body)
		}
	}

	// An admin may change it.
	res := o.do("PUT", "/api/projects/"+projectID+"/kinds",
		map[string]any{"kinds": []string{"epic"}}, admin)
	if res.status != http.StatusOK {
		t.Fatalf("admin put kinds: %d %s", res.status, res.body)
	}

	// A member and a guest may not.
	for name, cookie := range map[string]string{"member": member, "guest": guest} {
		res := o.do("PUT", "/api/projects/"+projectID+"/kinds",
			map[string]any{"kinds": []string{"nope"}}, cookie)
		if res.status != http.StatusForbidden {
			t.Fatalf("%s put kinds: %d %s; want 403", name, res.status, res.body)
		}
	}
}

func TestInvalidProjectKindIsRejected(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	for _, kinds := range [][]string{{""}, {"   "}, {strings.Repeat("x", 41)}, {"task", "task"}} {
		res := o.do("PUT", "/api/projects/"+projectID+"/kinds",
			map[string]any{"kinds": kinds}, o.ownerCookie)
		if res.status != http.StatusBadRequest {
			t.Fatalf("put kinds %v: %d %s; want 400", kinds, res.status, res.body)
		}
	}

	// The list is unchanged: a refused write changes nothing.
	res := o.do("GET", "/api/projects/"+projectID+"/kinds", nil, o.ownerCookie)
	if got := len(res.json(t)["kinds"].([]any)); got != 5 {
		t.Fatalf("kinds = %d; want the five defaults", got)
	}
}

func TestLabelsEndpointReturnsTheLabelsInUse(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	o.createItem(o.ownerCookie, projectID, "One", map[string]any{
		"labels": []string{"billing", "import"},
	})
	o.createItem(o.ownerCookie, projectID, "Two", map[string]any{
		"labels": []string{"billing"},
	})
	o.createItem(o.ownerCookie, projectID, "Three", nil)

	res := o.do("GET", "/api/labels?project_id="+projectID, nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("labels: %d %s", res.status, res.body)
	}
	labels := res.json(t)["labels"].([]any)
	want := []string{"billing", "import"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %s; want %v", res.body, want)
	}
	for i, l := range labels {
		if l != want[i] {
			t.Fatalf("label %d = %v; want %q", i, l, want[i])
		}
	}

	// Without the project, the tenant's labels.
	res = o.do("GET", "/api/labels", nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("labels: %d %s", res.status, res.body)
	}
	labels = res.json(t)["labels"].([]any)
	if len(labels) != len(want) {
		t.Fatalf("labels = %s; want %v", res.body, want)
	}

	// A guest may read the vocabulary too.
	tenantID := o.tenantID(o.ownerCookie)
	o.addMember(tenantID, "guest-labels@example.com", tenancy.RoleGuest)
	guest := o.loginTo("guest-labels@example.com", tenantID)
	if res := o.do("GET", "/api/labels", nil, guest); res.status != http.StatusOK {
		t.Fatalf("guest labels: %d %s", res.status, res.body)
	}
}

func TestItemDetailExposesTheFamily(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	epic := o.createItem(o.ownerCookie, projectID, "Epic", map[string]any{"kind": "epic"})
	child := o.createItem(o.ownerCookie, projectID, "Child", map[string]any{
		"parent_id": epic["id"], "priority": "p1",
	})

	// The epic's detail: no parent, and the child is named.
	res := o.do("GET", "/api/items/"+epic["id"].(string), nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("get epic: %d %s", res.status, res.body)
	}
	body := res.json(t)
	if body["parent"] != nil {
		t.Fatalf("the epic has a parent: %v", body["parent"])
	}
	children := body["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("children = %v; want one", body["children"])
	}
	c := children[0].(map[string]any)
	if c["id"] != child["id"] || c["title"] != "Child" || c["kind"] != "task" ||
		c["status"] != "backlog" {
		t.Fatalf("child = %+v; want the child item", c)
	}
	if c["priority"] != "priority:p1" {
		t.Fatalf("child priority = %v; want priority:p1", c["priority"])
	}

	// The child's detail: the parent is named, and it has no children.
	res = o.do("GET", "/api/items/"+child["id"].(string), nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("get child: %d %s", res.status, res.body)
	}
	body = res.json(t)
	parent, ok := body["parent"].(map[string]any)
	if !ok {
		t.Fatalf("parent = %v; want the epic", body["parent"])
	}
	if parent["id"] != epic["id"] || parent["title"] != "Epic" || parent["kind"] != "epic" ||
		parent["status"] != "backlog" {
		t.Fatalf("parent = %+v; want the epic", parent)
	}
	if parent["number"] == nil {
		t.Fatalf("parent has no number: %+v", parent)
	}
	if len(parent) != 5 {
		t.Fatalf("parent = %+v; want id, number, title, kind and status", parent)
	}
	if len(c) != 6 {
		t.Fatalf("child = %+v; want id, number, title, kind, status and priority", c)
	}
	if got := body["children"].([]any); len(got) != 0 {
		t.Fatalf("children = %v; want none", got)
	}
}

func TestItemDetailOfALeafHasNoFamily(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	leaf := o.createItem(o.ownerCookie, projectID, "Leaf", nil)

	res := o.do("GET", "/api/items/"+leaf["id"].(string), nil, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("get leaf: %d %s", res.status, res.body)
	}
	body := res.json(t)
	if body["parent"] != nil {
		t.Fatalf("a leaf has a parent: %v", body["parent"])
	}
	if got := body["children"].([]any); len(got) != 0 {
		t.Fatalf("a leaf has children: %v", got)
	}
}
