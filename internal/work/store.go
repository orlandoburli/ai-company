// Package work is the work-management core: products, projects, work items,
// dependency edges and the activity log.
//
// Every method takes a tenancy.Scope and runs inside a transaction that sets
// `app.tenant_id` locally, which is what makes the FORCE ROW LEVEL SECURITY
// policies apply. There is no code path here that queries a content table
// without a scope — the transaction helper refuses to start without one.
package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Store is the work-management store.
type Store struct {
	db *sql.DB
}

// NewStore builds a store over the application connection.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var (
	ErrNotFound       = errors.New("work: not found")
	ErrConflict       = errors.New("work: already exists")
	ErrCycle          = errors.New("work: dependency would create a cycle")
	ErrSelfDependency = errors.New("work: an item cannot depend on itself")
	ErrInvalid        = errors.New("work: invalid value")
	ErrParentCycle    = errors.New("work: parent link would make the tree circular")
)

// SQLSTATEs raised by the schema for tree and log rules.
const (
	codeDependencyCycle = "AIC01"
	codeSelfDependency  = "AIC02"
	codeAppendOnly      = "AIC03"
	codeTreeCycle       = "AIC04"
)

func newID() string { return strings.ToUpper(uuid.NewString()) }

// withTenant runs fn in a transaction scoped to the tenant. The setting is
// transaction-local, so a connection returned to the pool cannot carry a tenant
// into the next request.
func (s *Store) withTenant(ctx context.Context, sc tenancy.Scope, fn func(context.Context, *sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx,
		`SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		return fmt.Errorf("set tenant scope: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// --- products --------------------------------------------------------------

// Product is a repo-backed product line.
type Product struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	RepoURL        string    `json:"repo_url,omitempty"`
	JiraProjectKey string    `json:"jira_project_key,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateProduct adds a product to the tenant.
func (s *Store) CreateProduct(ctx context.Context, sc tenancy.Scope, name, slug, repoURL, jiraKey string) (Product, error) {
	var p Product
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		p = Product{ID: newID(), Name: name, Slug: slug, RepoURL: repoURL, JiraProjectKey: jiraKey}
		return tx.QueryRowContext(ctx, `
			INSERT INTO products (id, tenant_id, name, slug, repo_url, jira_project_key)
			VALUES ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''))
			RETURNING created_at`,
			p.ID, sc.TenantID, name, slug, repoURL, jiraKey).Scan(&p.CreatedAt)
	})
	if err != nil {
		return Product{}, mapPGError(err)
	}
	return p, nil
}

// ListProducts returns the tenant's products.
func (s *Store) ListProducts(ctx context.Context, sc tenancy.Scope) ([]Product, error) {
	out := []Product{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, name, slug, coalesce(repo_url, ''), coalesce(jira_project_key, ''), created_at
			  FROM products ORDER BY created_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Product
			if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.RepoURL, &p.JiraProjectKey, &p.CreatedAt); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// --- projects --------------------------------------------------------------

// Project is a nested container of items inside a product.
type Project struct {
	ID        string    `json:"id"`
	ProductID string    `json:"product_id"`
	ParentID  *string   `json:"parent_id,omitempty"`
	Name      string    `json:"name"`
	Key       string    `json:"key,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateProject adds a project (optionally nested) to a product.
func (s *Store) CreateProject(ctx context.Context, sc tenancy.Scope, productID string, parentID *string, name, key string) (Project, error) {
	var p Project
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		p = Project{ID: newID(), ProductID: productID, ParentID: parentID, Name: name, Key: key, Status: "active"}
		return tx.QueryRowContext(ctx, `
			INSERT INTO projects (id, tenant_id, product_id, parent_id, name, key)
			VALUES ($1, $2, $3, nullif($4, ''), $5, nullif($6, ''))
			RETURNING created_at`,
			p.ID, sc.TenantID, productID, deref(parentID), name, key).Scan(&p.CreatedAt)
	})
	if err != nil {
		return Project{}, mapPGError(err)
	}
	return p, nil
}

// ListProjects returns projects, optionally filtered by product.
func (s *Store) ListProjects(ctx context.Context, sc tenancy.Scope, productID string) ([]Project, error) {
	out := []Project{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, product_id, parent_id, name, coalesce(key, ''), status, created_at
			  FROM projects
			 WHERE ($1 = '' OR product_id = $1)
			 ORDER BY created_at`, productID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Project
			if err := rows.Scan(&p.ID, &p.ProductID, &p.ParentID, &p.Name, &p.Key, &p.Status, &p.CreatedAt); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// --- items -----------------------------------------------------------------

// Item is a work item. Kind is free-form: there is deliberately no enum.
type Item struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"project_id"`
	Number         int64      `json:"number"`
	ParentID       *string    `json:"parent_id,omitempty"`
	Kind           string     `json:"kind"`
	Title          string     `json:"title"`
	DescriptionMD  string     `json:"description_md"`
	Status         string     `json:"status"`
	Priority       *string    `json:"priority"`
	PrioritySource *string    `json:"priority_source"`
	PausedAt       *time.Time `json:"paused_at,omitempty"`
	PausedBy       *string    `json:"paused_by,omitempty"`
	PauseReason    string     `json:"pause_reason,omitempty"`
	PinnedAt       *time.Time `json:"pinned_at,omitempty"`
	PinnedBy       *string    `json:"pinned_by,omitempty"`
	DueDate        *time.Time `json:"due_date,omitempty"`
	Labels         []string   `json:"labels"`
	AssigneeKind   *string    `json:"assignee_kind,omitempty"`
	AssigneeUserID *string    `json:"assignee_user_id,omitempty"`
	GithubIssueURL string     `json:"github_issue_url,omitempty"`
	JiraIssueKey   string     `json:"jira_issue_key,omitempty"`
	ExternalURL    string     `json:"external_url,omitempty"`
	SortOrder      float64    `json:"sort_order"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// Derived from the dependency graph and the subtree, never stored.
	Blocked bool `json:"blocked"`
	IsEpic  bool `json:"is_epic"`

	// Convenience mirrors of the timestamps, so a client can ask "paused?"
	// without comparing against null.
	Paused bool `json:"paused"`
	Pinned bool `json:"pinned"`
}

const itemColumns = `
	i.id, i.project_id, i.number, i.parent_id, i.kind, i.title, i.description_md,
	i.status, i.priority, i.priority_source, i.paused_at, i.paused_by,
	coalesce(i.pause_reason, ''), i.pinned_at, i.pinned_by,
	i.due_date, i.labels_json, i.assignee_kind, i.assignee_user_id,
	coalesce(i.github_issue_url, ''), coalesce(i.jira_issue_key, ''),
	coalesce(i.external_url, ''), i.sort_order, i.created_at, i.updated_at,`

// blocked / is_epic computed per row. "Open" means not done and not cancelled.
const itemComputed = `
	EXISTS (
		SELECT 1 FROM item_dependencies d
		  JOIN items b ON b.id = d.to_item_id AND b.tenant_id = d.tenant_id
		 WHERE d.tenant_id = i.tenant_id AND d.from_item_id = i.id
		   AND b.status NOT IN ('done', 'cancelled')
	) AS blocked,
	EXISTS (
		SELECT 1 FROM items c
		 WHERE c.tenant_id = i.tenant_id AND c.parent_id = i.id
		   AND c.status NOT IN ('done', 'cancelled')
	) AS is_epic`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItem(r rowScanner) (Item, error) {
	var (
		it     Item
		labels []byte
	)
	err := r.Scan(&it.ID, &it.ProjectID, &it.Number, &it.ParentID, &it.Kind, &it.Title,
		&it.DescriptionMD, &it.Status, &it.Priority, &it.PrioritySource, &it.PausedAt,
		&it.PausedBy, &it.PauseReason, &it.PinnedAt, &it.PinnedBy, &it.DueDate, &labels,
		&it.AssigneeKind, &it.AssigneeUserID, &it.GithubIssueURL, &it.JiraIssueKey,
		&it.ExternalURL, &it.SortOrder, &it.CreatedAt, &it.UpdatedAt,
		&it.Blocked, &it.IsEpic)
	if err != nil {
		return Item{}, err
	}
	if len(labels) > 0 {
		if err := json.Unmarshal(labels, &it.Labels); err != nil {
			return Item{}, fmt.Errorf("decode labels: %w", err)
		}
	}
	if it.Labels == nil {
		it.Labels = []string{}
	}
	it.Paused = it.PausedAt != nil
	it.Pinned = it.PinnedAt != nil
	return it, nil
}

// NewItem is the input for creating a work item.
type NewItem struct {
	ProjectID      string
	ParentID       *string
	Kind           string
	Title          string
	DescriptionMD  string
	Status         string
	Priority       *string
	DueDate        *time.Time
	Labels         []string
	AssigneeKind   *string
	AssigneeUserID *string
	GithubIssueURL string
	JiraIssueKey   string
	ExternalURL    string
	ActorUserID    string
}

// CreateItem assigns the next per-tenant number and records the creation in the
// activity log, atomically.
func (s *Store) CreateItem(ctx context.Context, sc tenancy.Scope, in NewItem) (Item, error) {
	if strings.TrimSpace(in.Title) == "" {
		return Item{}, fmt.Errorf("%w: title is required", ErrInvalid)
	}
	if in.Kind == "" {
		return Item{}, fmt.Errorf("%w: kind is required", ErrInvalid)
	}
	if in.Status == "" {
		in.Status = "backlog"
	}
	priority, err := NormalizePriority(in.Priority)
	if err != nil {
		return Item{}, err
	}
	labels := in.Labels
	if labels == nil {
		labels = []string{}
	}

	var it Item
	err = s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var number int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO tenant_counters (tenant_id, name, value) VALUES ($1, 'item_number', 1)
			ON CONFLICT (tenant_id, name) DO UPDATE SET value = tenant_counters.value + 1
			RETURNING value`, sc.TenantID).Scan(&number); err != nil {
			return fmt.Errorf("assign number: %w", err)
		}

		id := newID()
		created, err := scanItem(tx.QueryRowContext(ctx, `
			INSERT INTO items AS i (id, tenant_id, project_id, number, parent_id, kind, title,
			                   description_md, status, priority, priority_source, due_date, labels_json,
			                   assignee_kind, assignee_user_id, github_issue_url, jira_issue_key, external_url)
			VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9, $10,
			        CASE WHEN $10::text IS NULL THEN NULL ELSE 'manual' END, $11, $12,
			        $13, nullif($14, ''), nullif($15, ''), nullif($16, ''), nullif($17, ''))
			RETURNING `+itemColumns+itemComputed,
			id, sc.TenantID, in.ProjectID, number, deref(in.ParentID), in.Kind, in.Title,
			in.DescriptionMD, in.Status, priority, in.DueDate, mustJSON(labels),
			in.AssigneeKind, deref(in.AssigneeUserID), in.GithubIssueURL, in.JiraIssueKey,
			in.ExternalURL))
		if err != nil {
			return err
		}
		it = created

		return recordActivity(ctx, tx, sc, it.ID, in.ActorUserID, "item.created", nil, it)
	})
	if err != nil {
		return Item{}, mapPGError(err)
	}
	return it, nil
}

// ItemFilter narrows a listing.
type ItemFilter struct {
	ProjectID      string
	Status         string
	Label          string
	AssigneeUserID string
	Limit          int
}

// ListItems returns items with their computed blocked/epic state. The board view
// is this query grouped by status — the classification lives in one place.
func (s *Store) ListItems(ctx context.Context, sc tenancy.Scope, f ItemFilter) ([]Item, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	var out []Item
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = listItemsTx(ctx, tx, f)
		return err
	})
	return out, err
}

func listItemsTx(ctx context.Context, tx *sql.Tx, f ItemFilter) ([]Item, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 500
	}
	out := []Item{}
	rows, err := tx.QueryContext(ctx, `
		SELECT `+itemColumns+itemComputed+`
		  FROM items i
		 WHERE ($1 = '' OR i.project_id = $1)
		   AND ($2 = '' OR i.status = $2)
		   AND ($3 = '' OR i.labels_json ? $3)
		   AND ($4 = '' OR i.assignee_user_id = $4)
		 ORDER BY i.number
		 LIMIT $5`, f.ProjectID, f.Status, f.Label, f.AssigneeUserID, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetItem returns one item.
func (s *Store) GetItem(ctx context.Context, sc tenancy.Scope, id string) (Item, error) {
	var it Item
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		it, err = scanItem(tx.QueryRowContext(ctx, `
			SELECT `+itemColumns+itemComputed+`
			  FROM items i WHERE i.id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return it, err
}

// ItemPatch carries a partial update. A nil field is unchanged; for nullable
// fields an empty string clears the value. That convention is explicit because
// the alternative -- a second "clear" flag per column -- is worse.
type ItemPatch struct {
	Title          *string
	DescriptionMD  *string
	Kind           *string
	Status         *string
	Priority       *string
	ParentID       *string
	DueDate        *string
	SortOrder      *float64
	Labels         *[]string
	AssigneeKind   *string
	AssigneeUserID *string
	GithubIssueURL *string
	JiraIssueKey   *string
	ExternalURL    *string
}

// UpdateItem applies a patch and records it with before/after state.
func (s *Store) UpdateItem(ctx context.Context, sc tenancy.Scope, id string, p ItemPatch, actorUserID string) (Item, error) {
	var out Item
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		before, err := scanItem(tx.QueryRowContext(ctx, `
			SELECT `+itemColumns+itemComputed+` FROM items i WHERE i.id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		sets := []string{"updated_at = now()"}
		args := []any{id}
		add := func(expr string, val any) {
			args = append(args, val)
			sets = append(sets, fmt.Sprintf(expr, len(args)))
		}

		if p.Title != nil {
			add("title = $%d", *p.Title)
		}
		if p.DescriptionMD != nil {
			add("description_md = $%d", *p.DescriptionMD)
		}
		if p.Kind != nil {
			add("kind = $%d", *p.Kind)
		}
		if p.Status != nil {
			add("status = $%d", *p.Status)
			// A pin is cleared when the item closes (FR-QUE-4); an override that
			// outlives its target is a lie the board would keep showing.
			if *p.Status == "done" || *p.Status == "cancelled" {
				sets = append(sets, "pinned_at = NULL", "pinned_by = NULL")
			}
		}
		if p.Priority != nil {
			norm, err := NormalizePriority(p.Priority)
			if err != nil {
				return err
			}
			args = append(args, norm)
			sets = append(sets, fmt.Sprintf("priority = $%d", len(args)))
			if norm != nil {
				args = append(args, "manual")
				sets = append(sets, fmt.Sprintf("priority_source = $%d", len(args)))
			} else {
				sets = append(sets, "priority_source = NULL")
			}
		}
		if p.ParentID != nil {
			add("parent_id = nullif($%d, '')", *p.ParentID)
		}
		if p.DueDate != nil {
			add("due_date = nullif($%d, '')::date", *p.DueDate)
		}
		if p.SortOrder != nil {
			add("sort_order = $%d", *p.SortOrder)
		}
		if p.Labels != nil {
			add("labels_json = $%d::jsonb", mustJSON(*p.Labels))
		}
		if p.AssigneeKind != nil {
			// Empty means unassign; the schema's CHECK rejects any other kind.
			add("assignee_kind = nullif($%d, '')", *p.AssigneeKind)
		}
		if p.AssigneeUserID != nil {
			add("assignee_user_id = nullif($%d, '')", *p.AssigneeUserID)
		}
		if p.GithubIssueURL != nil {
			add("github_issue_url = nullif($%d, '')", *p.GithubIssueURL)
		}
		if p.JiraIssueKey != nil {
			add("jira_issue_key = nullif($%d, '')", *p.JiraIssueKey)
		}
		if p.ExternalURL != nil {
			add("external_url = nullif($%d, '')", *p.ExternalURL)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE items SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
			return err
		}
		out, err = scanItem(tx.QueryRowContext(ctx, `
			SELECT `+itemColumns+itemComputed+` FROM items i WHERE i.id = $1`, id))
		if err != nil {
			return err
		}
		return recordActivity(ctx, tx, sc, id, actorUserID, "item.updated", before, out)
	})
	if err != nil {
		return Item{}, mapPGError(err)
	}
	return out, nil
}

// DeleteItem removes an item; incident dependency edges cascade.
func (s *Store) DeleteItem(ctx context.Context, sc tenancy.Scope, id, actorUserID string) error {
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		before, err := scanItem(tx.QueryRowContext(ctx, `
			SELECT `+itemColumns+itemComputed+` FROM items i WHERE i.id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE id = $1`, id); err != nil {
			return err
		}
		// Logged after the delete; item_id intentionally dangles so the record
		// survives the item it describes.
		return recordActivity(ctx, tx, sc, id, actorUserID, "item.deleted", before, nil)
	})
	return mapPGError(err)
}

// --- dependencies ----------------------------------------------------------

// Dependency is a blocked_by edge: ItemID waits on BlockedByID.
type Dependency struct {
	ID          string    `json:"id"`
	ItemID      string    `json:"item_id"`
	BlockedByID string    `json:"blocked_by_id"`
	Kind        string    `json:"kind"`
	CreatedAt   time.Time `json:"created_at"`

	// Other is the item at the far end of the edge: the blocker for a
	// dependent's edge, the dependent for a blocker's. Included so a UI can name
	// what blocks what without a request per edge.
	Other ItemRef `json:"other"`
}

// ItemRef is the minimum an edge needs to be readable.
type ItemRef struct {
	ID     string `json:"id"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// CreateDependency adds an edge, rejecting self-edges and cycles in SQL so the
// rule holds for every caller, including future bulk import.
func (s *Store) CreateDependency(ctx context.Context, sc tenancy.Scope, itemID, blockedByID, actorUserID string) (Dependency, error) {
	var d Dependency
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		d = Dependency{ID: newID(), ItemID: itemID, BlockedByID: blockedByID, Kind: "blocked_by"}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO item_dependencies (id, tenant_id, from_item_id, to_item_id, kind, created_by)
			VALUES ($1, $2, $3, $4, 'blocked_by', nullif($5, ''))
			RETURNING created_at`,
			d.ID, sc.TenantID, itemID, blockedByID, actorUserID).Scan(&d.CreatedAt); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT id, number, title, status FROM items WHERE id = $1`, blockedByID).
			Scan(&d.Other.ID, &d.Other.Number, &d.Other.Title, &d.Other.Status); err != nil {
			return err
		}
		return recordActivity(ctx, tx, sc, itemID, actorUserID, "item.dependency_added", nil, d)
	})
	if err != nil {
		return Dependency{}, mapPGError(err)
	}
	return d, nil
}

// DeleteDependency removes an edge by id.
func (s *Store) DeleteDependency(ctx context.Context, sc tenancy.Scope, id, actorUserID string) error {
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var itemID string
		if err := tx.QueryRowContext(ctx, `
			DELETE FROM item_dependencies WHERE id = $1 RETURNING from_item_id`, id).Scan(&itemID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		return recordActivity(ctx, tx, sc, itemID, actorUserID, "item.dependency_removed", nil, nil)
	})
	return mapPGError(err)
}

// Dependencies returns the item's blockers and its direct dependents.
func (s *Store) Dependencies(ctx context.Context, sc tenancy.Scope, itemID string) (blockers, dependents []Dependency, err error) {
	blockers, dependents = []Dependency{}, []Dependency{}
	err = s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT d.id, d.from_item_id, d.to_item_id, d.kind, d.created_at,
			       o.id, o.number, o.title, o.status
			  FROM item_dependencies d
			  JOIN items o
			    ON o.id = CASE WHEN d.from_item_id = $1 THEN d.to_item_id ELSE d.from_item_id END
			 WHERE d.from_item_id = $1 OR d.to_item_id = $1
			 ORDER BY d.created_at`, itemID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Dependency
			if err := rows.Scan(&d.ID, &d.ItemID, &d.BlockedByID, &d.Kind, &d.CreatedAt,
				&d.Other.ID, &d.Other.Number, &d.Other.Title, &d.Other.Status); err != nil {
				return err
			}
			if d.ItemID == itemID {
				blockers = append(blockers, d)
			} else {
				dependents = append(dependents, d)
			}
		}
		return rows.Err()
	})
	return blockers, dependents, err
}

// --- activity --------------------------------------------------------------

// Activity is one immutable log entry.
type Activity struct {
	ID        string    `json:"id"`
	ItemID    *string   `json:"item_id,omitempty"`
	Action    string    `json:"action"`
	ActorKind string    `json:"actor_kind"`
	ActorID   *string   `json:"actor_user_id,omitempty"`
	Before    []byte    `json:"before,omitempty"`
	After     []byte    `json:"after,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ListActivity returns an item's history, newest first.
func (s *Store) ListActivity(ctx context.Context, sc tenancy.Scope, itemID string, limit int) ([]Activity, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := []Activity{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, item_id, action, actor_kind, actor_user_id, before_json, after_json, created_at
			  FROM activity_log
			 WHERE ($1 = '' OR item_id = $1)
			 ORDER BY created_at DESC
			 LIMIT $2`, itemID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Activity
			if err := rows.Scan(&a.ID, &a.ItemID, &a.Action, &a.ActorKind, &a.ActorID,
				&a.Before, &a.After, &a.CreatedAt); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

func recordActivity(ctx context.Context, tx *sql.Tx, sc tenancy.Scope, itemID, actorUserID, action string, before, after any) error {
	var b, a []byte
	if before != nil {
		b = mustJSON(before)
	}
	if after != nil {
		a = mustJSON(after)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO activity_log (id, tenant_id, item_id, actor_kind, actor_user_id, action, before_json, after_json)
		VALUES ($1, $2, nullif($3, ''), 'user', nullif($4, ''), $5, $6::jsonb, $7::jsonb)`,
		newID(), sc.TenantID, itemID, actorUserID, action, b, a)
	return err
}

// --- helpers ---------------------------------------------------------------

// NormalizePriority accepts the documented aliases and returns the canonical
// label form. An empty string or "none" clears the priority.
func NormalizePriority(in *string) (*string, error) {
	if in == nil {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(*in)) {
	case "", "none":
		return nil, nil
	case "priority:p0", "p0", "high":
		return strptr("priority:p0"), nil
	case "priority:p1", "p1", "medium":
		return strptr("priority:p1"), nil
	case "priority:p2", "p2", "low":
		return strptr("priority:p2"), nil
	default:
		return nil, fmt.Errorf("%w: priority %q", ErrInvalid, *in)
	}
}

func mapPGError(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case codeDependencyCycle:
			return ErrCycle
		case codeSelfDependency:
			return ErrSelfDependency
		case codeTreeCycle:
			return ErrParentCycle
		case codeAppendOnly:
			return fmt.Errorf("work: activity log is append-only")
		case "23505":
			return fmt.Errorf("%w: %s", ErrConflict, pg.ConstraintName)
		case "23503":
			return fmt.Errorf("%w: referenced row does not exist in this tenant", ErrNotFound)
		case "23514":
			return fmt.Errorf("%w: %s", ErrInvalid, pg.ConstraintName)
		}
	}
	return err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strptr(s string) *string { return &s }

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("work: marshal: " + err.Error())
	}
	return b
}

// --- product and project maintenance ---------------------------------------

// ProductPatch is a partial update; nil leaves a field unchanged and an empty
// string clears a nullable one.
type ProductPatch struct {
	Name           *string
	Slug           *string
	RepoURL        *string
	JiraProjectKey *string
}

// UpdateProduct renames or re-points a product.
func (s *Store) UpdateProduct(ctx context.Context, sc tenancy.Scope, id string, p ProductPatch, actorUserID string) (Product, error) {
	var out Product
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		before, err := scanProduct(tx.QueryRowContext(ctx, `
			SELECT id, name, slug, coalesce(repo_url, ''), coalesce(jira_project_key, ''), created_at
			  FROM products WHERE id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		sets := []string{}
		args := []any{id}
		add := func(expr string, val any) {
			args = append(args, val)
			sets = append(sets, fmt.Sprintf(expr, len(args)))
		}
		if p.Name != nil {
			add("name = $%d", *p.Name)
		}
		if p.Slug != nil {
			add("slug = $%d", *p.Slug)
		}
		if p.RepoURL != nil {
			add("repo_url = nullif($%d, '')", *p.RepoURL)
		}
		if p.JiraProjectKey != nil {
			add("jira_project_key = nullif($%d, '')", *p.JiraProjectKey)
		}
		if len(sets) > 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE products SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
				return err
			}
		}
		out, err = scanProduct(tx.QueryRowContext(ctx, `
			SELECT id, name, slug, coalesce(repo_url, ''), coalesce(jira_project_key, ''), created_at
			  FROM products WHERE id = $1`, id))
		if err != nil {
			return err
		}
		return recordActivity(ctx, tx, sc, "", actorUserID, "product.updated", before, out)
	})
	if err != nil {
		return Product{}, mapPGError(err)
	}
	return out, nil
}

// DeleteProduct removes a product only when it is empty. Cascading a delete
// through every project and item is never what a mis-click should mean.
func (s *Store) DeleteProduct(ctx context.Context, sc tenancy.Scope, id, actorUserID string) error {
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM projects WHERE product_id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: product still has projects", ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM products WHERE id = $1`, id); err != nil {
			return err
		}
		return recordActivity(ctx, tx, sc, "", actorUserID, "product.deleted", nil, nil)
	})
	return mapPGError(err)
}

func scanProduct(r rowScanner) (Product, error) {
	var p Product
	err := r.Scan(&p.ID, &p.Name, &p.Slug, &p.RepoURL, &p.JiraProjectKey, &p.CreatedAt)
	return p, err
}

// ProjectPatch is a partial update.
type ProjectPatch struct {
	Name        *string
	Key         *string
	Description *string
	Status      *string
	ParentID    *string
}

// UpdateProject changes a project or archives it.
func (s *Store) UpdateProject(ctx context.Context, sc tenancy.Scope, id string, p ProjectPatch, actorUserID string) (Project, error) {
	var out Project
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		before, err := scanProject(tx.QueryRowContext(ctx, `
			SELECT id, product_id, parent_id, name, coalesce(key, ''), status, created_at
			  FROM projects WHERE id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		sets := []string{}
		args := []any{id}
		add := func(expr string, val any) {
			args = append(args, val)
			sets = append(sets, fmt.Sprintf(expr, len(args)))
		}
		if p.Name != nil {
			add("name = $%d", *p.Name)
		}
		if p.Key != nil {
			add("key = nullif($%d, '')", *p.Key)
		}
		if p.Description != nil {
			add("description = nullif($%d, '')", *p.Description)
		}
		if p.Status != nil {
			add("status = $%d", *p.Status)
		}
		if p.ParentID != nil {
			add("parent_id = nullif($%d, '')", *p.ParentID)
		}
		if len(sets) > 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
				return err
			}
		}
		out, err = scanProject(tx.QueryRowContext(ctx, `
			SELECT id, product_id, parent_id, name, coalesce(key, ''), status, created_at
			  FROM projects WHERE id = $1`, id))
		if err != nil {
			return err
		}
		return recordActivity(ctx, tx, sc, "", actorUserID, "project.updated", before, out)
	})
	if err != nil {
		return Project{}, mapPGError(err)
	}
	return out, nil
}

// DeleteProject removes an empty project. Items and sub-projects must be moved
// or removed first.
func (s *Store) DeleteProject(ctx context.Context, sc tenancy.Scope, id, actorUserID string) error {
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM items WHERE project_id = $1)
			    OR EXISTS (SELECT 1 FROM projects WHERE parent_id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: project is not empty", ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, id); err != nil {
			return err
		}
		return recordActivity(ctx, tx, sc, "", actorUserID, "project.deleted", nil, nil)
	})
	return mapPGError(err)
}

func scanProject(r rowScanner) (Project, error) {
	var p Project
	err := r.Scan(&p.ID, &p.ProductID, &p.ParentID, &p.Name, &p.Key, &p.Status, &p.CreatedAt)
	return p, err
}

// BulkSetStatus applies one status to several items in a single transaction
// (FR-ACT-2). Each item still gets its own before/after log entry, so a bulk
// change is auditable per item rather than as one opaque action.
func (s *Store) BulkSetStatus(ctx context.Context, sc tenancy.Scope, ids []string, status, actorUserID string) ([]Item, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: no items given", ErrInvalid)
	}
	if len(ids) > 100 {
		return nil, fmt.Errorf("%w: at most 100 items per bulk change", ErrInvalid)
	}
	out := make([]Item, 0, len(ids))
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		for _, id := range ids {
			before, err := getItemTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE items
				   SET status = $2, updated_at = now(),
				       pinned_at = CASE WHEN $2 IN ('done', 'cancelled') THEN NULL ELSE pinned_at END,
				       pinned_by = CASE WHEN $2 IN ('done', 'cancelled') THEN NULL ELSE pinned_by END
				 WHERE id = $1`, id, status); err != nil {
				return err
			}
			after, err := getItemTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := recordActivity(ctx, tx, sc, id, actorUserID, "item.status_changed", before, after); err != nil {
				return err
			}
			out = append(out, after)
		}
		return nil
	})
	if err != nil {
		return nil, mapPGError(err)
	}
	return out, nil
}
