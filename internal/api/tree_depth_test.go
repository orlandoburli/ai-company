package api_test

import (
	"net/http"
	"testing"
)

// Item tree depth (docs/mvp-design.md): six levels of items, the root counting
// as one. Enforced at the API on create with a parent and on re-parent; the
// store keeps its walk guard as a backstop.

func TestItemTreeDepthLimit(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	// Six levels are the design's bound and must succeed up to exactly there:
	// a chain of four, then children at five and six.
	n1 := o.createItem(o.ownerCookie, projectID, "One", nil)
	n2 := o.createItem(o.ownerCookie, projectID, "Two", map[string]any{"parent_id": n1["id"]})
	n3 := o.createItem(o.ownerCookie, projectID, "Three", map[string]any{"parent_id": n2["id"]})
	n4 := o.createItem(o.ownerCookie, projectID, "Four", map[string]any{"parent_id": n3["id"]})
	m5 := o.createItem(o.ownerCookie, projectID, "Five", map[string]any{"parent_id": n4["id"]})
	m6 := o.createItem(o.ownerCookie, projectID, "Six", map[string]any{"parent_id": m5["id"]})

	// The seventh level is refused with 422 tree_too_deep.
	bad := o.do("POST", "/api/items", map[string]any{
		"project_id": projectID, "kind": "task", "title": "Seven",
		"parent_id": m6["id"],
	}, o.ownerCookie)
	if bad.status != http.StatusUnprocessableEntity {
		t.Fatalf("depth-7 create: %d %s; want 422", bad.status, bad.body)
	}
	if code := bad.json(t)["error"].(map[string]any)["code"]; code != "tree_too_deep" {
		t.Fatalf("depth-7 code = %v; want tree_too_deep", code)
	}

	// A second chain reaching depth 5, to move into.
	p1 := o.createItem(o.ownerCookie, projectID, "Chain A", nil)
	p2 := o.createItem(o.ownerCookie, projectID, "Chain B", map[string]any{"parent_id": p1["id"]})
	p3 := o.createItem(o.ownerCookie, projectID, "Chain C", map[string]any{"parent_id": p2["id"]})
	p4 := o.createItem(o.ownerCookie, projectID, "Chain D", map[string]any{"parent_id": p3["id"]})
	p5 := o.createItem(o.ownerCookie, projectID, "Chain E", map[string]any{"parent_id": p4["id"]})

	// Re-parenting applies the rule to the deepest descendant of the moving
	// subtree: Four (whose subtree continues Five→Six) under Chain E would put
	// Six at level 8, so the move is refused even though Four itself would land
	// at 6. The refused move must leave the item unchanged.
	before := o.do("GET", "/api/items/"+n4["id"].(string), nil, o.ownerCookie)
	if before.status != http.StatusOK {
		t.Fatalf("read item before move: %d %s", before.status, before.body)
	}
	wantParent := before.json(t)["item"].(map[string]any)["parent_id"]

	mv := o.do("PATCH", "/api/items/"+n4["id"].(string),
		map[string]any{"parent_id": p5["id"]}, o.ownerCookie)
	if mv.status != http.StatusUnprocessableEntity {
		t.Fatalf("too-deep move: %d %s; want 422", mv.status, mv.body)
	}
	if code := mv.json(t)["error"].(map[string]any)["code"]; code != "tree_too_deep" {
		t.Fatalf("move code = %v; want tree_too_deep", code)
	}
	after := o.do("GET", "/api/items/"+n4["id"].(string), nil, o.ownerCookie)
	if after.status != http.StatusOK {
		t.Fatalf("read item after move: %d %s", after.status, after.body)
	}
	if got := after.json(t)["item"].(map[string]any)["parent_id"]; got != wantParent {
		t.Fatalf("refused move changed parent_id: %v, want %v", got, wantParent)
	}

	// A move that keeps every node within the limit is allowed: Six is a leaf
	// at level 6 and stays at level 6 under Chain E.
	ok := o.do("PATCH", "/api/items/"+m6["id"].(string),
		map[string]any{"parent_id": p5["id"]}, o.ownerCookie)
	if ok.status != http.StatusOK {
		t.Fatalf("legal move: %d %s", ok.status, ok.body)
	}
}
