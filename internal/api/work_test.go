package api_test

import (
	"net/http"
	"testing"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Work-management HTTP surface. The store tests cover the SQL guarantees; these
// cover the two things only the request path can get wrong: role gating and
// scope propagation.

func (h *harness) createProductProject(cookie string) (productID, projectID string) {
	h.t.Helper()

	res := h.do("POST", "/api/products", map[string]any{
		"name": "Acme", "slug": "acme", "repo_url": "git@github.com:acme/app.git",
	}, cookie)
	if res.status != http.StatusCreated {
		h.t.Fatalf("create product: %d %s", res.status, res.body)
	}
	productID = res.json(h.t)["product"].(map[string]any)["id"].(string)

	res = h.do("POST", "/api/projects", map[string]any{
		"product_id": productID, "name": "Core", "key": "CORE",
	}, cookie)
	if res.status != http.StatusCreated {
		h.t.Fatalf("create project: %d %s", res.status, res.body)
	}
	projectID = res.json(h.t)["project"].(map[string]any)["id"].(string)
	return productID, projectID
}

func (h *harness) createItem(cookie, projectID, title string, extra map[string]any) map[string]any {
	h.t.Helper()
	body := map[string]any{"project_id": projectID, "kind": "task", "title": title}
	for k, v := range extra {
		body[k] = v
	}
	res := h.do("POST", "/api/items", body, cookie)
	if res.status != http.StatusCreated {
		h.t.Fatalf("create item %q: %d %s", title, res.status, res.body)
	}
	return res.json(h.t)["item"].(map[string]any)
}

func TestWorkItemsDependenciesAndBoard(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	epic := o.createItem(o.ownerCookie, projectID, "Epic", nil)
	blocker := o.createItem(o.ownerCookie, projectID, "Blocker", nil)
	blocked := o.createItem(o.ownerCookie, projectID, "Blocked", map[string]any{
		"parent_id": epic["id"], "priority": "high",
	})

	if blocked["priority"] != "priority:p0" {
		t.Fatalf("priority = %v; want canonical priority:p0", blocked["priority"])
	}
	if blocked["priority_source"] != "manual" {
		t.Fatalf("priority_source = %v; want manual", blocked["priority_source"])
	}

	// Board: seven fixed columns, with the epic and blocked classifications
	// computed by the store rather than guessed by the client.
	board := o.do("GET", "/api/board", nil, o.ownerCookie)
	if board.status != http.StatusOK {
		t.Fatalf("board: %d %s", board.status, board.body)
	}
	columns := board.json(t)["columns"].([]any)
	if len(columns) != 7 {
		t.Fatalf("board has %d columns; want 7", len(columns))
	}
	backlog := columns[0].(map[string]any)
	if backlog["status"] != "backlog" {
		t.Fatalf("first column = %v", backlog["status"])
	}
	for _, raw := range backlog["items"].([]any) {
		it := raw.(map[string]any)
		switch it["id"] {
		case epic["id"]:
			if it["is_epic"] != true {
				t.Fatalf("epic not marked is_epic: %+v", it)
			}
		case blocked["id"]:
			if it["blocked"] != false {
				t.Fatalf("item marked blocked before the edge exists")
			}
		}
	}

	// Add the edge, then re-read: the classification must flip.
	res := o.do("POST", "/api/items/"+blocked["id"].(string)+"/dependencies",
		map[string]any{"blocked_by_id": blocker["id"]}, o.ownerCookie)
	if res.status != http.StatusCreated {
		t.Fatalf("create dependency: %d %s", res.status, res.body)
	}
	depID := res.json(t)["dependency"].(map[string]any)["id"].(string)

	got := o.do("GET", "/api/items/"+blocked["id"].(string), nil, o.ownerCookie)
	if got.status != http.StatusOK {
		t.Fatalf("get item: %d %s", got.status, got.body)
	}
	detail := got.json(t)
	if detail["item"].(map[string]any)["blocked"] != true {
		t.Fatalf("item not blocked after edge: %s", got.body)
	}
	if len(detail["blockers"].([]any)) != 1 {
		t.Fatalf("blockers = %v; want 1", detail["blockers"])
	}
	// The edge names the other item, so a blockers panel needs no extra reads.
	blockerRef := detail["blockers"].([]any)[0].(map[string]any)["other"].(map[string]any)
	if blockerRef["title"] != "Blocker" || blockerRef["id"] != blocker["id"] {
		t.Fatalf("edge did not carry the related item: %+v", blockerRef)
	}

	// A cycle is a 409 with a specific code, not a 500 and not a silent drop.
	cycle := o.do("POST", "/api/items/"+blocker["id"].(string)+"/dependencies",
		map[string]any{"blocked_by_id": blocked["id"]}, o.ownerCookie)
	if cycle.status != http.StatusConflict {
		t.Fatalf("cycle status = %d; want 409 (%s)", cycle.status, cycle.body)
	}
	if code := cycle.json(t)["error"].(map[string]any)["code"]; code != "dependency_cycle" {
		t.Fatalf("cycle code = %v", code)
	}

	self := o.do("POST", "/api/items/"+blocker["id"].(string)+"/dependencies",
		map[string]any{"blocked_by_id": blocker["id"]}, o.ownerCookie)
	if self.status != http.StatusConflict ||
		self.json(t)["error"].(map[string]any)["code"] != "self_dependency" {
		t.Fatalf("self edge: %d %s", self.status, self.body)
	}

	// Removing the edge unblocks it again.
	del := o.do("DELETE", "/api/items/"+blocked["id"].(string)+"/dependencies/"+depID, nil, o.ownerCookie)
	if del.status != http.StatusNoContent {
		t.Fatalf("delete dependency: %d %s", del.status, del.body)
	}
	got = o.do("GET", "/api/items/"+blocked["id"].(string), nil, o.ownerCookie)
	if got.json(t)["item"].(map[string]any)["blocked"] != false {
		t.Fatalf("item still blocked after edge removal")
	}

	// The activity log recorded the whole sequence, attributed.
	act := o.do("GET", "/api/items/"+blocked["id"].(string)+"/activity", nil, o.ownerCookie)
	if act.status != http.StatusOK {
		t.Fatalf("activity: %d %s", act.status, act.body)
	}
	entries := act.json(t)["activity"].([]any)
	if len(entries) < 3 {
		t.Fatalf("activity has %d entries; want at least 3", len(entries))
	}
}

func TestProductRepositoryRoutesWireShapePatchAndRoles(t *testing.T) {
	o := newOwnerHarness(t)
	productID, _ := o.createProductProject(o.ownerCookie)
	products := o.do("GET", "/api/products", nil, o.ownerCookie).json(t)["products"].([]any)
	product := products[0].(map[string]any)
	if product["id"] != productID || product["repo_url"] != "git@github.com:acme/app.git" {
		t.Fatalf("product compatibility shape = %#v", product)
	}
	repositories := product["repositories"].([]any)
	if len(repositories) != 1 || repositories[0].(map[string]any)["repo"] != "acme/app" || repositories[0].(map[string]any)["is_default"] != true {
		t.Fatalf("product repository wire shape = %#v", repositories)
	}

	added := o.do("POST", "/api/products/"+productID+"/repositories", map[string]any{
		"repo": "acme/mobile", "name": "mobile",
	}, o.ownerCookie)
	if added.status != http.StatusCreated {
		t.Fatalf("add repository: %d %s", added.status, added.body)
	}
	addedID := added.json(t)["repository"].(map[string]any)["id"].(string)
	if duplicate := o.do("POST", "/api/products/"+productID+"/repositories", map[string]any{"repo": "acme/mobile"}, o.ownerCookie); duplicate.status != http.StatusConflict {
		t.Fatalf("duplicate repository status=%d body=%s", duplicate.status, duplicate.body)
	}
	if selected := o.do("POST", "/api/products/"+productID+"/repositories/"+addedID+"/default", nil, o.ownerCookie); selected.status != http.StatusOK {
		t.Fatalf("set default: %d %s", selected.status, selected.body)
	}
	patched := o.do("PATCH", "/api/products/"+productID, map[string]any{"repo_url": "https://github.com/acme/desktop.git"}, o.ownerCookie)
	if patched.status != http.StatusOK {
		t.Fatalf("patch default create: %d %s", patched.status, patched.body)
	}
	newDefaultID := ""
	for _, repository := range patched.json(t)["product"].(map[string]any)["repositories"].([]any) {
		entry := repository.(map[string]any)
		if entry["is_default"] == true {
			newDefaultID = entry["id"].(string)
			if entry["repo"] != "acme/desktop" {
				t.Fatalf("PATCH default = %#v", entry)
			}
		}
	}
	if newDefaultID == "" {
		t.Fatal("PATCH did not create and select a new default repository")
	}
	patched = o.do("PATCH", "/api/products/"+productID, map[string]any{"repo_url": "acme/mobile"}, o.ownerCookie)
	if patched.status != http.StatusOK {
		t.Fatalf("PATCH existing default: %d %s", patched.status, patched.body)
	}
	defaultRepo := ""
	for _, repository := range patched.json(t)["product"].(map[string]any)["repositories"].([]any) {
		entry := repository.(map[string]any)
		if entry["is_default"] == true {
			defaultRepo = entry["repo"].(string)
		}
	}
	if defaultRepo != "acme/mobile" {
		t.Fatalf("PATCH did not update existing repository default: %q", defaultRepo)
	}

	tenantID := o.tenantID(o.ownerCookie)
	o.addMember(tenantID, "repo-guest@example.com", tenancy.RoleGuest)
	guest := o.loginTo("repo-guest@example.com", tenantID)
	if denied := o.do("POST", "/api/products/"+productID+"/repositories", map[string]any{"repo": "acme/guest"}, guest); denied.status != http.StatusForbidden {
		t.Fatalf("guest repository write: %d %s", denied.status, denied.body)
	}
	if removed := o.do("DELETE", "/api/products/"+productID+"/repositories/"+addedID, nil, o.ownerCookie); removed.status != http.StatusNoContent {
		t.Fatalf("remove repository: %d %s", removed.status, removed.body)
	}
}

func TestGuestCanReadButNotWriteWork(t *testing.T) {
	o := newOwnerHarness(t)
	tenantID := o.tenantID(o.ownerCookie)
	_, projectID := o.createProductProject(o.ownerCookie)
	item := o.createItem(o.ownerCookie, projectID, "Existing", nil)

	o.addMember(tenantID, "guest@example.com", tenancy.RoleGuest)
	guest := o.loginTo("guest@example.com", tenantID)

	if res := o.do("GET", "/api/items", nil, guest); res.status != http.StatusOK {
		t.Fatalf("guest list: %d %s", res.status, res.body)
	}
	if res := o.do("GET", "/api/items/"+item["id"].(string), nil, guest); res.status != http.StatusOK {
		t.Fatalf("guest read: %d %s", res.status, res.body)
	}

	write := o.do("POST", "/api/items", map[string]any{
		"project_id": projectID, "kind": "task", "title": "nope",
	}, guest)
	if write.status != http.StatusForbidden {
		t.Fatalf("guest create: %d %s; want 403", write.status, write.body)
	}

	patch := o.do("PATCH", "/api/items/"+item["id"].(string),
		map[string]any{"title": "renamed"}, guest)
	if patch.status != http.StatusForbidden {
		t.Fatalf("guest patch: %d %s; want 403", patch.status, patch.body)
	}

	del := o.do("DELETE", "/api/items/"+item["id"].(string), nil, guest)
	if del.status != http.StatusForbidden {
		t.Fatalf("guest delete: %d %s; want 403", del.status, del.body)
	}
}

func TestWorkIsInvisibleAcrossTenants(t *testing.T) {
	h := newHarness(t)

	res := h.register("owner@example.com", goodPassword, "Acme", "acme")
	if res.status != http.StatusCreated {
		t.Fatalf("register acme: %d %s", res.status, res.body)
	}
	acme := res.cookie
	_, projectID := h.createProductProject(acme)
	item := h.createItem(acme, projectID, "Acme secret", nil)

	other := h.register("other@example.com", goodPassword, "Globex", "globex")
	if other.status != http.StatusCreated {
		t.Fatalf("register globex: %d %s", other.status, other.body)
	}
	globex := other.cookie
	_, otherProject := h.createProductProject(globex)

	if got := h.do("GET", "/api/items", nil, globex); got.status != http.StatusOK ||
		len(got.json(t)["items"].([]any)) != 0 {
		t.Fatalf("globex sees acme items: %s", got.body)
	}
	if got := h.do("GET", "/api/items/"+item["id"].(string), nil, globex); got.status != http.StatusNotFound {
		t.Fatalf("globex read acme item: %d %s; want 404", got.status, got.body)
	}
	if got := h.do("GET", "/api/items/"+item["id"].(string)+"/activity", nil, globex); got.status != http.StatusOK ||
		len(got.json(t)["activity"].([]any)) != 0 {
		t.Fatalf("globex sees acme activity: %s", got.body)
	}

	// An edge from a Globex item to an Acme item fails the composite FK, which
	// the store reports as not-found rather than leaking that the row exists.
	own := h.createItem(globex, otherProject, "Globex task", nil)
	edge := h.do("POST", "/api/items/"+own["id"].(string)+"/dependencies",
		map[string]any{"blocked_by_id": item["id"]}, globex)
	if edge.status != http.StatusNotFound {
		t.Fatalf("cross-tenant edge: %d %s; want 404", edge.status, edge.body)
	}
}

func TestQueueOrderingPauseAndPin(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	high := o.createItem(o.ownerCookie, projectID, "High", map[string]any{"priority": "p0"})
	low := o.createItem(o.ownerCookie, projectID, "Low", map[string]any{"priority": "p2"})

	queue := func() []any {
		t.Helper()
		res := o.do("GET", "/api/queue", nil, o.ownerCookie)
		if res.status != http.StatusOK {
			t.Fatalf("queue: %d %s", res.status, res.body)
		}
		body := res.json(t)
		if body["weights_version"] != "pri-base-1" {
			t.Fatalf("weights_version = %v", body["weights_version"])
		}
		return body["queue"].([]any)
	}

	first := func() string {
		t.Helper()
		q := queue()
		if len(q) == 0 {
			t.Fatal("queue empty")
		}
		return q[0].(map[string]any)["item"].(map[string]any)["id"].(string)
	}

	if got := first(); got != high["id"] {
		t.Fatalf("queue front = %s; want the p0 item", got)
	}

	// The score endpoint explains the number from persisted inputs.
	score := o.do("GET", "/api/items/"+high["id"].(string)+"/score", nil, o.ownerCookie)
	if score.status != http.StatusOK {
		t.Fatalf("score: %d %s", score.status, score.body)
	}
	body := score.json(t)
	if body["base_score"].(float64) != 100 {
		t.Fatalf("base_score = %v; want 100", body["base_score"])
	}
	sum := 0.0
	for _, raw := range body["factors"].([]any) {
		sum += raw.(map[string]any)["contribution"].(float64)
	}
	if sum != body["base_score"].(float64) {
		t.Fatalf("factors sum to %v, score is %v", sum, body["base_score"])
	}

	// Pin the low item: ordering only, and it outranks every scored item.
	if res := o.do("POST", "/api/items/"+low["id"].(string)+"/pin", nil, o.ownerCookie); res.status != http.StatusOK {
		t.Fatalf("pin: %d %s", res.status, res.body)
	}
	if got := first(); got != low["id"] {
		t.Fatalf("pinned item is not first: %s", got)
	}

	// Pause outranks pin: the item leaves the dispatch queue entirely.
	pause := o.do("POST", "/api/items/"+low["id"].(string)+"/pause",
		map[string]any{"reason": "waiting on vendor"}, o.ownerCookie)
	if pause.status != http.StatusOK {
		t.Fatalf("pause: %d %s", pause.status, pause.body)
	}
	item := pause.json(t)["item"].(map[string]any)
	if item["paused"] != true || item["paused_by"] == nil || item["pause_reason"] != "waiting on vendor" {
		t.Fatalf("pause not attributed in response: %+v", item)
	}
	for _, raw := range queue() {
		if raw.(map[string]any)["item"].(map[string]any)["id"] == low["id"] {
			t.Fatal("paused item is still in the dispatch queue")
		}
	}

	// Unpause restores eligibility without clearing the pin, so it is first again.
	if res := o.do("POST", "/api/items/"+low["id"].(string)+"/unpause", nil, o.ownerCookie); res.status != http.StatusOK {
		t.Fatalf("unpause: %d %s", res.status, res.body)
	}
	if got := first(); got != low["id"] {
		t.Fatalf("unpaused pinned item is not first: %s", got)
	}

	// Unpin returns it to the position its score earns.
	if res := o.do("POST", "/api/items/"+low["id"].(string)+"/unpin", nil, o.ownerCookie); res.status != http.StatusOK {
		t.Fatalf("unpin: %d %s", res.status, res.body)
	}
	if got := first(); got != high["id"] {
		t.Fatalf("after unpin front = %s; want the p0 item", got)
	}
}

func TestGuestCannotOverrideQueue(t *testing.T) {
	o := newOwnerHarness(t)
	tenantID := o.tenantID(o.ownerCookie)
	_, projectID := o.createProductProject(o.ownerCookie)
	item := o.createItem(o.ownerCookie, projectID, "Task", nil)

	o.addMember(tenantID, "guest2@example.com", tenancy.RoleGuest)
	guest := o.loginTo("guest2@example.com", tenantID)

	if res := o.do("GET", "/api/queue", nil, guest); res.status != http.StatusOK {
		t.Fatalf("guest queue: %d %s", res.status, res.body)
	}
	for _, action := range []string{"pause", "unpause", "pin", "unpin"} {
		res := o.do("POST", "/api/items/"+item["id"].(string)+"/"+action, nil, guest)
		if res.status != http.StatusForbidden {
			t.Fatalf("guest %s: %d %s; want 403", action, res.status, res.body)
		}
	}
}

func TestTreeRollupFiltersAndBulkStatus(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	epic := o.createItem(o.ownerCookie, projectID, "Epic", nil)
	first := o.createItem(o.ownerCookie, projectID, "First", map[string]any{
		"parent_id": epic["id"], "labels": []string{"bug"},
	})
	second := o.createItem(o.ownerCookie, projectID, "Second", map[string]any{
		"parent_id": epic["id"],
	})

	// Close one child; the rollup must count it without counting the parent done.
	if res := o.do("PATCH", "/api/items/"+first["id"].(string),
		map[string]any{"status": "done"}, o.ownerCookie); res.status != http.StatusOK {
		t.Fatalf("close child: %d %s", res.status, res.body)
	}

	tree := o.do("GET", "/api/tree", nil, o.ownerCookie)
	if tree.status != http.StatusOK {
		t.Fatalf("tree: %d %s", tree.status, tree.body)
	}
	projectNode := tree.json(t)["projects"].([]any)[0].(map[string]any)
	progress := projectNode["progress"].(map[string]any)
	if progress["done"].(float64) != 1 || progress["total"].(float64) != 3 {
		t.Fatalf("project progress = %v; want 1/3", progress)
	}

	var epicNode map[string]any
	for _, raw := range projectNode["items"].([]any) {
		node := raw.(map[string]any)
		if node["item"].(map[string]any)["id"] == epic["id"] {
			epicNode = node
		}
	}
	if epicNode == nil {
		t.Fatalf("epic missing from tree: %s", tree.body)
	}
	if len(epicNode["children"].([]any)) != 2 {
		t.Fatalf("epic children = %d; want 2", len(epicNode["children"].([]any)))
	}
	epicProgress := epicNode["progress"].(map[string]any)
	if epicProgress["done"].(float64) != 1 || epicProgress["total"].(float64) != 3 {
		t.Fatalf("epic progress = %v; want 1/3", epicProgress)
	}

	// Bulk status change (FR-ACT-2).
	bulk := o.do("POST", "/api/items/bulk-status", map[string]any{
		"ids": []string{second["id"].(string)}, "status": "todo",
	}, o.ownerCookie)
	if bulk.status != http.StatusOK {
		t.Fatalf("bulk status: %d %s", bulk.status, bulk.body)
	}
	if got := bulk.json(t)["items"].([]any)[0].(map[string]any)["status"]; got != "todo" {
		t.Fatalf("bulk status result = %v", got)
	}

	// Filters: status and label.
	todo := o.do("GET", "/api/items?status=todo", nil, o.ownerCookie)
	if todo.status != http.StatusOK || len(todo.json(t)["items"].([]any)) != 1 {
		t.Fatalf("status filter = %s", todo.body)
	}
	bugs := o.do("GET", "/api/items?label=bug", nil, o.ownerCookie)
	if bugs.status != http.StatusOK || len(bugs.json(t)["items"].([]any)) != 1 {
		t.Fatalf("label filter = %s", bugs.body)
	}
}

func TestAssignmentAndExternalLinks(t *testing.T) {
	o := newOwnerHarness(t)
	_, projectID := o.createProductProject(o.ownerCookie)

	me := o.do("GET", "/api/auth/me", nil, o.ownerCookie).json(t)
	userID := me["user"].(map[string]any)["id"].(string)

	item := o.createItem(o.ownerCookie, projectID, "Linked", map[string]any{
		"assignee_kind":    "human",
		"assignee_user_id": userID,
		"github_issue_url": "https://github.com/acme/app/issues/7",
		"jira_issue_key":   "CORE-7",
		"external_url":     "https://example.com/spec",
		"labels":           []string{"bug", "backend"},
	})
	if item["assignee_kind"] != "human" || item["assignee_user_id"] != userID {
		t.Fatalf("assignment not stored: %+v", item)
	}
	if item["jira_issue_key"] != "CORE-7" || item["github_issue_url"] == "" {
		t.Fatalf("links not stored: %+v", item)
	}

	// Unassign by clearing the fields.
	res := o.do("PATCH", "/api/items/"+item["id"].(string), map[string]any{
		"assignee_kind": "", "assignee_user_id": "",
	}, o.ownerCookie)
	if res.status != http.StatusOK {
		t.Fatalf("unassign: %d %s", res.status, res.body)
	}
	if got := res.json(t)["item"].(map[string]any); got["assignee_kind"] != nil {
		t.Fatalf("assignee_kind not cleared: %+v", got)
	}
}

func TestEmptyOnlyDeletes(t *testing.T) {
	o := newOwnerHarness(t)
	productID, projectID := o.createProductProject(o.ownerCookie)
	item := o.createItem(o.ownerCookie, projectID, "Keep me", nil)

	if res := o.do("DELETE", "/api/products/"+productID, nil, o.ownerCookie); res.status != http.StatusConflict {
		t.Fatalf("delete non-empty product: %d %s; want 409", res.status, res.body)
	}
	if res := o.do("DELETE", "/api/projects/"+projectID, nil, o.ownerCookie); res.status != http.StatusConflict {
		t.Fatalf("delete non-empty project: %d %s; want 409", res.status, res.body)
	}

	if res := o.do("DELETE", "/api/items/"+item["id"].(string), nil, o.ownerCookie); res.status != http.StatusNoContent {
		t.Fatalf("delete item: %d %s", res.status, res.body)
	}
	if res := o.do("DELETE", "/api/projects/"+projectID, nil, o.ownerCookie); res.status != http.StatusNoContent {
		t.Fatalf("delete empty project: %d %s", res.status, res.body)
	}
	if res := o.do("DELETE", "/api/products/"+productID, nil, o.ownerCookie); res.status != http.StatusNoContent {
		t.Fatalf("delete empty product: %d %s", res.status, res.body)
	}

	// Renaming a product is a normal update.
	p2, _ := o.createProductProject(o.ownerCookie)
	res := o.do("PATCH", "/api/products/"+p2, map[string]any{"name": "Renamed"}, o.ownerCookie)
	if res.status != http.StatusOK || res.json(t)["product"].(map[string]any)["name"] != "Renamed" {
		t.Fatalf("rename product: %d %s", res.status, res.body)
	}
}
