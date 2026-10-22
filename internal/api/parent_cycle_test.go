package api_test

import (
	"net/http"
	"testing"
)

// Parent-cycle rejection. The database trigger (AIC04) is the enforcement
// point: moving a row under its own descendant passes the depth arithmetic,
// so only the ancestor-chain walk can see it. The API surfaces it as 422
// parent_cycle, for items and projects alike.

func TestItemMoveUnderOwnDescendantRejected(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	a := o.createItem(o.ownerCookie, projectID, "A", nil)
	b := o.createItem(o.ownerCookie, projectID, "B", map[string]any{"parent_id": a["id"]})
	c := o.createItem(o.ownerCookie, projectID, "C", map[string]any{"parent_id": b["id"]})

	// Depth-wise this would land A at 3 and C at 5 — the depth rule passes it,
	// but it folds the chain into a loop, so the trigger must refuse it.
	mv := o.do("PATCH", "/api/items/"+a["id"].(string),
		map[string]any{"parent_id": c["id"]}, o.ownerCookie)
	if mv.status != http.StatusUnprocessableEntity {
		t.Fatalf("move under descendant: %d %s; want 422", mv.status, mv.body)
	}
	if code := mv.json(t)["error"].(map[string]any)["code"]; code != "parent_cycle" {
		t.Fatalf("code = %v; want parent_cycle", code)
	}

	// The refused move left the row alone.
	got := o.do("GET", "/api/items/"+a["id"].(string), nil, o.ownerCookie)
	if got.status != http.StatusOK {
		t.Fatalf("get item after refused move: %d %s", got.status, got.body)
	}
	if p := got.json(t)["item"].(map[string]any)["parent_id"]; p != nil {
		t.Fatalf("item gained a parent after a refused move: %v", p)
	}
}

func TestProjectMoveUnderOwnDescendantRejected(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)
	productID := o.do("GET", "/api/products", nil, o.ownerCookie).
		json(t)["products"].([]any)[0].(map[string]any)["id"].(string)

	res := o.do("POST", "/api/projects", map[string]any{
		"product_id": productID, "parent_id": projectID, "name": "Q",
	}, o.ownerCookie)
	if res.status != http.StatusCreated {
		t.Fatalf("create nested project Q: %d %s", res.status, res.body)
	}

	// Moving the parent under its own child loops the project tree.
	mv := o.do("PATCH", "/api/projects/"+projectID,
		map[string]any{"parent_id": res.json(t)["project"].(map[string]any)["id"]}, o.ownerCookie)
	if mv.status != http.StatusUnprocessableEntity {
		t.Fatalf("project move under descendant: %d %s; want 422", mv.status, mv.body)
	}
	if code := mv.json(t)["error"].(map[string]any)["code"]; code != "parent_cycle" {
		t.Fatalf("code = %v; want parent_cycle", code)
	}

	projects := o.do("GET", "/api/projects", nil, o.ownerCookie).json(t)["projects"].([]any)
	for _, raw := range projects {
		p := raw.(map[string]any)
		if p["id"] == projectID && p["parent_id"] != nil {
			t.Fatalf("project gained a parent after a refused move: %v", p["parent_id"])
		}
	}
}

func TestLegalSiblingMoveStillAllowed(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	// Items: C moves under D, D being C's uncle — a plain subtree relocation.
	a := o.createItem(o.ownerCookie, projectID, "A", nil)
	_ = o.createItem(o.ownerCookie, projectID, "B", map[string]any{"parent_id": a["id"]})
	c := o.createItem(o.ownerCookie, projectID, "C", nil)
	d := o.createItem(o.ownerCookie, projectID, "D", map[string]any{"parent_id": a["id"]})

	itemMove := o.do("PATCH", "/api/items/"+c["id"].(string),
		map[string]any{"parent_id": d["id"]}, o.ownerCookie)
	if itemMove.status != http.StatusOK {
		t.Fatalf("legal item move: %d %s; want 200", itemMove.status, itemMove.body)
	}
	if p := itemMove.json(t)["item"].(map[string]any)["parent_id"]; p != d["id"] {
		t.Fatalf("item parent = %v; want %v", p, d["id"])
	}

	// Projects: R under Q is just a different branch of the same product.
	productID := o.do("GET", "/api/products", nil, o.ownerCookie).
		json(t)["products"].([]any)[0].(map[string]any)["id"].(string)
	q := o.do("POST", "/api/projects", map[string]any{
		"product_id": productID, "parent_id": projectID, "name": "Q",
	}, o.ownerCookie)
	if q.status != http.StatusCreated {
		t.Fatalf("create project Q: %d %s", q.status, q.body)
	}
	r := o.do("POST", "/api/projects", map[string]any{
		"product_id": productID, "parent_id": projectID, "name": "R",
	}, o.ownerCookie)
	if r.status != http.StatusCreated {
		t.Fatalf("create project R: %d %s", r.status, r.body)
	}
	projectMove := o.do("PATCH", "/api/projects/"+r.json(t)["project"].(map[string]any)["id"].(string),
		map[string]any{"parent_id": q.json(t)["project"].(map[string]any)["id"]}, o.ownerCookie)
	if projectMove.status != http.StatusOK {
		t.Fatalf("legal project move: %d %s; want 200", projectMove.status, projectMove.body)
	}
}
