package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/priority"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

// Work management: products, projects, items, dependency edges and the board.
//
// Every mutating route is wrapped in requireRole(CanWriteWork) at the route
// table, and every store call carries the request scope, which sets the tenant
// for row-level security. The two checks are independent: the role check decides
// whether the action is allowed, the scope decides whose data it can touch.

// scope resolves the request scope or writes the 401. Middleware has already
// resolved it, so a failure here means the route was not wrapped in authenticated.
func (s *Server) scope(w http.ResponseWriter, r *http.Request) (tenancy.Scope, bool) {
	sc, err := tenancy.Require(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return tenancy.Scope{}, false
	}
	return sc, true
}

type createProductRequest struct {
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	RepoURL        string `json:"repo_url"`
	JiraProjectKey string `json:"jira_project_key"`
}

func (s *Server) handleListProducts(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	products, err := s.work.ListProducts(r.Context(), sc)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": products})
}

func (s *Server) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	var req createProductRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(req.Name)
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	if name == "" || slug == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Name and slug are required.")
		return
	}
	p, err := s.work.CreateProduct(r.Context(), sc, name, slug,
		strings.TrimSpace(req.RepoURL), strings.TrimSpace(req.JiraProjectKey))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"product": p})
}

type createProjectRequest struct {
	ProductID string `json:"product_id"`
	ParentID  string `json:"parent_id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	projects, err := s.work.ListProjects(r.Context(), sc, r.URL.Query().Get("product_id"))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.ProductID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Name and product_id are required.")
		return
	}
	var parentID *string
	if p := strings.TrimSpace(req.ParentID); p != "" {
		parentID = &p
	}
	pr, err := s.work.CreateProject(r.Context(), sc, req.ProductID, parentID,
		strings.TrimSpace(req.Name), strings.TrimSpace(req.Key))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": pr})
}

// The kinds a project offers.
//
// An item's kind is free text: the user names their own kinds, and there is
// deliberately no enum. What a project declares is the list it offers — the
// vocabulary the interface presents when somebody picks a kind. The list is an
// offer, not a constraint: an item keeps its kind even after the project stops
// offering it.
//
// Reading the list is open to every role; changing it is a settings surface,
// so owner/admin only.

// setProjectKindsRequest is the list a project offers. The names may be given
// as plain strings or as objects carrying a name, so the endpoint takes the
// shape it returns as well as the simpler list of names.
type setProjectKindsRequest struct {
	Kinds []projectKindName `json:"kinds"`
}

// projectKindName accepts either a bare name or an object with a name.
type projectKindName struct {
	Name string
}

func (k *projectKindName) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		k.Name = name
		return nil
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	k.Name = obj.Name
	return nil
}

// handleGetProjectKinds returns the kinds a project offers, in order.
func (s *Server) handleGetProjectKinds(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	kinds, err := s.work.ListProjectKinds(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kinds": kinds})
}

// handlePutProjectKinds replaces the project's kinds with the given list, in
// the given order. An empty list clears the project.
func (s *Server) handlePutProjectKinds(w http.ResponseWriter, r *http.Request) {
	var req setProjectKindsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if req.Kinds == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "A kinds list is required.")
		return
	}
	names := make([]string, 0, len(req.Kinds))
	for _, k := range req.Kinds {
		names = append(names, k.Name)
	}
	if err := s.work.SetProjectKinds(r.Context(), sc, r.PathValue("id"), names); err != nil {
		s.writeWorkError(w, err)
		return
	}
	// Re-read rather than echo the request: the stored order is the answer.
	kinds, err := s.work.ListProjectKinds(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kinds": kinds})
}

// handleListLabels returns the labels the tenant's items already use — the
// autocomplete's vocabulary, derived from the items rather than declared. The
// project filter is optional: without it the answer is the tenant's labels.
func (s *Server) handleListLabels(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	labels, err := s.work.ListLabels(r.Context(), sc, strings.TrimSpace(r.URL.Query().Get("project_id")))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": labels})
}

type createItemRequest struct {
	ProjectID      string   `json:"project_id"`
	ParentID       string   `json:"parent_id"`
	Kind           string   `json:"kind"`
	Title          string   `json:"title"`
	DescriptionMD  string   `json:"description_md"`
	Status         string   `json:"status"`
	Priority       *string  `json:"priority"`
	DueDate        string   `json:"due_date"`
	Labels         []string `json:"labels"`
	AssigneeKind   string   `json:"assignee_kind"`
	AssigneeUserID string   `json:"assignee_user_id"`
	GithubIssueURL string   `json:"github_issue_url"`
	JiraIssueKey   string   `json:"jira_issue_key"`
	ExternalURL    string   `json:"external_url"`
}

func (s *Server) handleCreateItem(w http.ResponseWriter, r *http.Request) {
	var req createItemRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "project_id is required.")
		return
	}
	var parentID *string
	if p := strings.TrimSpace(req.ParentID); p != "" {
		parentID = &p
	}
	// Depth bound (docs/mvp-design.md): six item levels, root counting as one.
	// Checked before the write so the client sees the 422; the store keeps its
	// own walk guard as a backstop.
	if parentID != nil {
		if err := s.work.CheckItemDepth(r.Context(), sc, "", *parentID); err != nil {
			s.writeWorkError(w, err)
			return
		}
	}
	var due *time.Time
	if d := strings.TrimSpace(req.DueDate); d != "" {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "due_date must be YYYY-MM-DD.")
			return
		}
		due = &t
	}
	kind := strings.TrimSpace(req.AssigneeKind)
	if kind != "" && kind != "human" && kind != "agent" {
		writeError(w, http.StatusBadRequest, "invalid_request", "assignee_kind must be human or agent.")
		return
	}
	it, err := s.work.CreateItem(r.Context(), sc, work.NewItem{
		ProjectID:      req.ProjectID,
		ParentID:       parentID,
		Kind:           strings.TrimSpace(req.Kind),
		Title:          strings.TrimSpace(req.Title),
		DescriptionMD:  req.DescriptionMD,
		Status:         strings.TrimSpace(req.Status),
		Priority:       req.Priority,
		DueDate:        due,
		Labels:         req.Labels,
		AssigneeKind:   optional(req.AssigneeKind),
		AssigneeUserID: optional(req.AssigneeUserID),
		GithubIssueURL: strings.TrimSpace(req.GithubIssueURL),
		JiraIssueKey:   strings.TrimSpace(req.JiraIssueKey),
		ExternalURL:    strings.TrimSpace(req.ExternalURL),
		ActorUserID:    sc.UserID,
	})
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": it})
}

func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	items, err := s.work.ListItems(r.Context(), sc, itemFilterFrom(r))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// itemParentView is the parent's shape in the item detail response: enough to
// know what the item sits under, and nothing else.
type itemParentView struct {
	ID     string `json:"id"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// parentViewOf maps the store's summary onto the parent's wire shape. An item
// without a parent has none: the answer is null, not an empty object.
func parentViewOf(p *work.ItemSummary) *itemParentView {
	if p == nil {
		return nil
	}
	return &itemParentView{
		ID: p.ID, Number: p.Number, Title: p.Title, Kind: p.Kind, Status: p.Status,
	}
}

// handleGetItem returns one item with everything that frames it: the edges it
// waits on, the item it sits under, and the children that make it an epic.
func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	it, err := s.work.GetItem(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	blockers, dependents, err := s.work.Dependencies(r.Context(), sc, it.ID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	parent, children, err := s.work.GetItemFamily(r.Context(), sc, it.ID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item":       it,
		"blockers":   blockers,
		"dependents": dependents,
		"parent":     parentViewOf(parent),
		"children":   children,
	})
}

type patchItemRequest struct {
	Title          *string   `json:"title"`
	DescriptionMD  *string   `json:"description_md"`
	Kind           *string   `json:"kind"`
	Status         *string   `json:"status"`
	Priority       *string   `json:"priority"`
	ParentID       *string   `json:"parent_id"`
	DueDate        *string   `json:"due_date"`
	SortOrder      *float64  `json:"sort_order"`
	Labels         *[]string `json:"labels"`
	AssigneeKind   *string   `json:"assignee_kind"`
	AssigneeUserID *string   `json:"assignee_user_id"`
	GithubIssueURL *string   `json:"github_issue_url"`
	JiraIssueKey   *string   `json:"jira_issue_key"`
	ExternalURL    *string   `json:"external_url"`
}

func (s *Server) handlePatchItem(w http.ResponseWriter, r *http.Request) {
	var req patchItemRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	// A non-empty parent_id re-parents the item. The depth rule applies to the
	// whole moving subtree: its deepest descendant, not just the item, must
	// stay within six levels.
	if req.ParentID != nil {
		if parent := strings.TrimSpace(*req.ParentID); parent != "" {
			if err := s.work.CheckItemDepth(r.Context(), sc, r.PathValue("id"), parent); err != nil {
				s.writeWorkError(w, err)
				return
			}
		}
	}
	it, err := s.work.UpdateItem(r.Context(), sc, r.PathValue("id"), work.ItemPatch{
		Title:          req.Title,
		DescriptionMD:  req.DescriptionMD,
		Kind:           req.Kind,
		Status:         req.Status,
		Priority:       req.Priority,
		ParentID:       req.ParentID,
		DueDate:        req.DueDate,
		SortOrder:      req.SortOrder,
		Labels:         req.Labels,
		AssigneeKind:   req.AssigneeKind,
		AssigneeUserID: req.AssigneeUserID,
		GithubIssueURL: req.GithubIssueURL,
		JiraIssueKey:   req.JiraIssueKey,
		ExternalURL:    req.ExternalURL,
	}, sc.UserID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	// A linked GitHub issue keeps the same title as its item (the design's
	// "title/state sync on item save"). A failure here is not the item update's
	// failure: the item is saved either way, and the next poll re-syncs.
	if req.Title != nil && *req.Title != "" {
		if svc, svcErr := s.githubSyncService(r.Context(), sc); svcErr == nil {
			if _, syncErr := svc.SyncItemTitle(r.Context(), sc, it.ID); syncErr != nil {
				s.log.Warn("sync the GitHub issue title", "item", it.ID, "error", syncErr)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": it})
}

func (s *Server) handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := s.work.DeleteItem(r.Context(), sc, r.PathValue("id"), sc.UserID); err != nil {
		s.writeWorkError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type createDependencyRequest struct {
	BlockedByID string `json:"blocked_by_id"`
}

func (s *Server) handleListDependencies(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	blockers, dependents, err := s.work.Dependencies(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blockers": blockers, "dependents": dependents})
}

func (s *Server) handleCreateDependency(w http.ResponseWriter, r *http.Request) {
	var req createDependencyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.BlockedByID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "blocked_by_id is required.")
		return
	}
	d, err := s.work.CreateDependency(r.Context(), sc, r.PathValue("id"), req.BlockedByID, sc.UserID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"dependency": d})
}

func (s *Server) handleDeleteDependency(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := s.work.DeleteDependency(r.Context(), sc, r.PathValue("depID"), sc.UserID); err != nil {
		s.writeWorkError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleItemActivity(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	entries, err := s.work.ListActivity(r.Context(), sc, r.PathValue("id"), 100)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activity": entries})
}

// boardStatuses is the fixed column order. Status is a closed set in the schema,
// so the board does not need to discover its columns.
var boardStatuses = []string{
	"backlog", "todo", "in_progress", "waiting_human", "in_review", "done", "cancelled",
}

type boardColumn struct {
	Status string      `json:"status"`
	Items  []work.Item `json:"items"`
}

// handleBoard returns items grouped into columns, with blocked and epic already
// classified by the store query. Blocked means an open blocker; epic means it has
// an open child. Those are the two distinctions the board actually renders.
func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	items, err := s.work.ListItems(r.Context(), sc, itemFilterFrom(r))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	byStatus := make(map[string][]work.Item, len(boardStatuses))
	for _, it := range items {
		byStatus[it.Status] = append(byStatus[it.Status], it)
	}
	columns := make([]boardColumn, 0, len(boardStatuses))
	for _, st := range boardStatuses {
		col := boardColumn{Status: st, Items: byStatus[st]}
		if col.Items == nil {
			col.Items = []work.Item{}
		}
		columns = append(columns, col)
	}
	writeJSON(w, http.StatusOK, map[string]any{"columns": columns})
}

func (s *Server) writeWorkError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, work.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "That item does not exist in this tenant.")
	case errors.Is(err, work.ErrCycle):
		writeError(w, http.StatusConflict, "dependency_cycle", "That dependency would create a cycle.")
	case errors.Is(err, work.ErrSelfDependency):
		writeError(w, http.StatusConflict, "self_dependency", "An item cannot depend on itself.")
	case errors.Is(err, work.ErrParentCycle):
		writeError(w, http.StatusUnprocessableEntity, "parent_cycle",
			"That move would make the tree circular: an item cannot be moved under its own descendant.")
	case errors.Is(err, work.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "That already exists.")
	case errors.Is(err, work.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, work.ErrTooDeep):
		writeError(w, http.StatusUnprocessableEntity, "tree_too_deep",
			"The item tree is limited to 6 levels (the root counts as 1), and this change would push a descendant past that.")
	case errors.Is(err, tenancy.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
	default:
		s.log.Error("work handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

// --- queue overrides and scoring -------------------------------------------

type pauseRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) handlePauseItem(w http.ResponseWriter, r *http.Request) {
	var req pauseRequest
	// A reason is optional, but the body is still parsed when present so a typo
	// is a 400 rather than a silently dropped field.
	if r.ContentLength > 0 && !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	it, err := s.work.PauseItem(r.Context(), sc, r.PathValue("id"), strings.TrimSpace(req.Reason), sc.UserID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": it})
}

func (s *Server) handleUnpauseItem(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	it, err := s.work.UnpauseItem(r.Context(), sc, r.PathValue("id"), sc.UserID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": it})
}

func (s *Server) handlePinItem(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	it, err := s.work.PinItem(r.Context(), sc, r.PathValue("id"), sc.UserID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": it})
}

func (s *Server) handleUnpinItem(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	it, err := s.work.UnpinItem(r.Context(), sc, r.PathValue("id"), sc.UserID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": it})
}

// handleQueue returns the dispatch order: pinned first, then base score. It is
// computed from persisted state on every request, never from a cached run.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	entries, err := s.work.Queue(r.Context(), sc, itemFilterFrom(r))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queue":           entries,
		"weights_version": priority.WeightsVersion,
	})
}

// handleItemScore returns the reproducible base score and its factor breakdown.
func (s *Server) handleItemScore(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	score, it, err := s.work.ScoreItem(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item_id":         it.ID,
		"number":          it.Number,
		"ineligible":      priority.IneligibleReason(priorityItem(it)),
		"base_score":      score.Value,
		"weights_version": score.WeightsVersion,
		"factors":         score.Factors,
	})
}

// priorityItem maps the stored item onto the scorer's input. Kept small and
// explicit so adding a scoring input is a deliberate change, not an accident.
func priorityItem(it work.Item) priority.Item {
	return priority.Item{
		ID:        it.ID,
		Number:    it.Number,
		Status:    it.Status,
		Priority:  it.Priority,
		Labels:    it.Labels,
		CreatedAt: it.CreatedAt,
		Paused:    it.Paused,
		Blocked:   it.Blocked,
		IsEpic:    it.IsEpic,
		PinnedAt:  it.PinnedAt,
	}
}

// --- products, projects, tree and bulk changes ------------------------------

type updateProductRequest struct {
	Name           *string `json:"name"`
	Slug           *string `json:"slug"`
	RepoURL        *string `json:"repo_url"`
	JiraProjectKey *string `json:"jira_project_key"`
}

func (s *Server) handlePatchProduct(w http.ResponseWriter, r *http.Request) {
	var req updateProductRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	p, err := s.work.UpdateProduct(r.Context(), sc, r.PathValue("id"), work.ProductPatch{
		Name: req.Name, Slug: req.Slug, RepoURL: req.RepoURL, JiraProjectKey: req.JiraProjectKey,
	}, sc.UserID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"product": p})
}

func (s *Server) handleDeleteProduct(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := s.work.DeleteProduct(r.Context(), sc, r.PathValue("id"), sc.UserID); err != nil {
		s.writeWorkError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type updateProjectRequest struct {
	Name        *string `json:"name"`
	Key         *string `json:"key"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
	ParentID    *string `json:"parent_id"`
}

func (s *Server) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	var req updateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	p, err := s.work.UpdateProject(r.Context(), sc, r.PathValue("id"), work.ProjectPatch{
		Name: req.Name, Key: req.Key, Description: req.Description,
		Status: req.Status, ParentID: req.ParentID,
	}, sc.UserID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": p})
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := s.work.DeleteProject(r.Context(), sc, r.PathValue("id"), sc.UserID); err != nil {
		s.writeWorkError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTree returns nested projects and items with subtree rollups (FR-PM-6).
func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	nodes, err := s.work.Tree(r.Context(), sc, r.URL.Query().Get("product_id"))
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": nodes})
}

type bulkStatusRequest struct {
	IDs    []string `json:"ids"`
	Status string   `json:"status"`
}

// handleBulkStatus changes several items' status at once (FR-ACT-2), logging each.
func (s *Server) handleBulkStatus(w http.ResponseWriter, r *http.Request) {
	var req bulkStatusRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(req.Status)
	switch status {
	case "backlog", "todo", "in_progress", "waiting_human", "in_review", "done", "cancelled":
	default:
		writeError(w, http.StatusBadRequest, "invalid_status", "Unknown status.")
		return
	}
	items, err := s.work.BulkSetStatus(r.Context(), sc, req.IDs, status, sc.UserID)
	if err != nil {
		s.writeWorkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// itemFilterFrom maps the query string to a store filter. Unknown parameters are
// ignored rather than erroring, so a board URL stays shareable as filters evolve.
func itemFilterFrom(r *http.Request) work.ItemFilter {
	q := r.URL.Query()
	return work.ItemFilter{
		ProjectID:      q.Get("project_id"),
		Status:         q.Get("status"),
		Label:          q.Get("label"),
		AssigneeUserID: q.Get("assignee_user_id"),
	}
}

// optional turns an empty form value into a nil pointer, which the store reads as
// "not set" rather than "clear".
func optional(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}
