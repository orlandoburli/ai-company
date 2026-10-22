package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Tree assembly (FR-PM-6): products hold nested projects, projects hold nested
// items, and every node reports how much of its own subtree is done.
//
// It is built in memory from two flat, tenant-scoped queries rather than one
// recursive query per node: the depth is bounded, the result is small at MVP
// scale, and the rollup stays a plain post-order walk that is easy to reason
// about.

// Progress is a subtree rollup. A cancelled item counts toward the total but not
// toward done, so a cancelled subtree does not read as complete.
type Progress struct {
	Done    int `json:"done"`
	Total   int `json:"total"`
	Percent int `json:"percent"`
}

// ItemNode is an item and its children.
type ItemNode struct {
	Item     Item       `json:"item"`
	Progress Progress   `json:"progress"`
	Children []ItemNode `json:"children"`
}

// ProjectNode is a project, its items, its sub-projects and its rollup.
type ProjectNode struct {
	Project  Project       `json:"project"`
	Progress Progress      `json:"progress"`
	Items    []ItemNode    `json:"items"`
	Children []ProjectNode `json:"children"`
}

// Tree returns the project and item tree for a product (or every product's tree
// when productID is empty).
func (s *Store) Tree(ctx context.Context, sc tenancy.Scope, productID string) ([]ProjectNode, error) {
	var roots []ProjectNode
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		projects, err := listProjectsTx(ctx, tx, productID)
		if err != nil {
			return err
		}
		items, err := listItemsTx(ctx, tx, ItemFilter{ProjectID: ""})
		if err != nil {
			return err
		}
		roots = assembleTree(projects, items)
		return nil
	})
	return roots, err
}

func assembleTree(projects []Project, items []Item) []ProjectNode {
	itemsByProject := map[string][]Item{}
	for _, it := range items {
		itemsByProject[it.ProjectID] = append(itemsByProject[it.ProjectID], it)
	}
	childrenByProject := map[string][]Project{}
	for _, p := range projects {
		if p.ParentID != nil {
			childrenByProject[*p.ParentID] = append(childrenByProject[*p.ParentID], p)
		}
	}

	built := map[string]ProjectNode{}
	for _, p := range projects {
		built[p.ID] = ProjectNode{
			Project:  p,
			Items:    buildItemNodes(itemsByProject[p.ID]),
			Children: []ProjectNode{},
		}
	}

	var attach func(parentID string, depth int) []ProjectNode
	attach = func(parentID string, depth int) []ProjectNode {
		if depth > 32 {
			return []ProjectNode{}
		}
		out := []ProjectNode{}
		for _, p := range childrenByProject[parentID] {
			node := built[p.ID]
			node.Children = attach(p.ID, depth+1)
			node.Progress = projectProgress(node)
			built[p.ID] = node
			out = append(out, node)
		}
		return out
	}

	roots := []ProjectNode{}
	for _, p := range projects {
		if p.ParentID != nil {
			continue
		}
		node := built[p.ID]
		node.Children = attach(p.ID, 0)
		node.Progress = projectProgress(node)
		roots = append(roots, node)
	}
	return roots
}

func buildItemNodes(items []Item) []ItemNode {
	byID := map[string]Item{}
	children := map[string][]string{}
	order := make([]string, 0, len(items))
	for _, it := range items {
		byID[it.ID] = it
		order = append(order, it.ID)
	}
	roots := []string{}
	for _, it := range items {
		if it.ParentID == nil {
			roots = append(roots, it.ID)
			continue
		}
		if _, ok := byID[*it.ParentID]; !ok {
			// Parent lives in another project: treat as a root here rather than
			// dropping the item from the tree.
			roots = append(roots, it.ID)
			continue
		}
		children[*it.ParentID] = append(children[*it.ParentID], it.ID)
	}

	var build func(id string, depth int) ItemNode
	build = func(id string, depth int) ItemNode {
		it := byID[id]
		node := ItemNode{Item: it, Children: []ItemNode{}}
		if depth < 32 {
			for _, childID := range children[id] {
				node.Children = append(node.Children, build(childID, depth+1))
			}
		}
		node.Progress = itemProgress(node)
		return node
	}

	out := make([]ItemNode, 0, len(roots))
	for _, id := range roots {
		out = append(out, build(id, 0))
	}
	return out
}

func itemProgress(node ItemNode) Progress {
	p := Progress{Total: 1}
	if node.Item.Status == "done" {
		p.Done = 1
	}
	for _, child := range node.Children {
		p.Done += child.Progress.Done
		p.Total += child.Progress.Total
	}
	p.Percent = percent(p)
	return p
}

func projectProgress(node ProjectNode) Progress {
	p := Progress{}
	for _, it := range node.Items {
		p.Done += it.Progress.Done
		p.Total += it.Progress.Total
	}
	for _, child := range node.Children {
		p.Done += child.Progress.Done
		p.Total += child.Progress.Total
	}
	p.Percent = percent(p)
	return p
}

func percent(p Progress) int {
	if p.Total == 0 {
		return 0
	}
	return (p.Done * 100) / p.Total
}

// --- depth -------------------------------------------------------------------

// MaxItemDepth is the design's item-tree bound (docs/mvp-design.md): six levels
// of items, counting the root as one.
const MaxItemDepth = 6

// ErrTooDeep reports that placing an item under the given parent would push
// some node of the tree past MaxItemDepth.
var ErrTooDeep = errors.New("work: item tree would exceed the depth limit")

// CheckItemDepth reports whether placing itemID under parentID keeps the item
// tree within MaxItemDepth. An empty itemID is a new item. On a move the rule
// applies to the deepest descendant of the moving subtree — the node farthest
// from the new parent — not only to the moved item. This is the API's
// enforcement point; the depth-32 caps in the walks above and in ScoreItem stay
// as loop backstops and there is still no database constraint.
func (s *Store) CheckItemDepth(ctx context.Context, sc tenancy.Scope, itemID, parentID string) error {
	return s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		return checkItemDepth(ctx, tx, itemID, parentID)
	})
}

func checkItemDepth(ctx context.Context, tx *sql.Tx, itemID, parentID string) error {
	// Depth of the new parent, root at one. Same shaped CTE as the score
	// walk and capped the same way, so even a corrupt parent chain terminates.
	var parentDepth int
	if parentID != "" {
		var d sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			WITH RECURSIVE chain AS (
				SELECT i.id, i.parent_id, 1 AS depth
				  FROM items i WHERE i.id = $1
				UNION ALL
				SELECT p.id, p.parent_id, c.depth + 1
				  FROM items p JOIN chain c ON p.id = c.parent_id
				 WHERE c.depth < 32
			)
			SELECT max(depth) FROM chain`, parentID).Scan(&d)
		if err != nil {
			return err
		}
		if !d.Valid {
			// The parent row does not exist in this tenant; inventing
			// not-found here would change the shape of existing errors, so
			// defer to the write's own foreign-key failure.
			return nil
		}
		parentDepth = int(d.Int64)
	}

	// Height of the moving subtree below the item itself (the item is at 0).
	var subtreeHeight int
	if itemID != "" {
		var h sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			WITH RECURSIVE below AS (
				SELECT i.id, i.parent_id, 0 AS depth
				  FROM items i WHERE i.id = $1
				UNION ALL
				SELECT c.id, c.parent_id, b.depth + 1
				  FROM items c JOIN below b ON c.parent_id = b.id
				 WHERE b.depth < 32
			)
			SELECT max(depth) FROM below`, itemID).Scan(&h)
		if err != nil {
			return err
		}
		if h.Valid && h.Int64 > int64(subtreeHeight) {
			subtreeHeight = int(h.Int64)
		}
	}

	if parentDepth+subtreeHeight+1 <= MaxItemDepth {
		return nil
	}
	return fmt.Errorf("%w: a descendant would land at level %d; the limit is %d",
		ErrTooDeep, parentDepth+subtreeHeight+1, MaxItemDepth)
}

func listProjectsTx(ctx context.Context, tx *sql.Tx, productID string) ([]Project, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, product_id, parent_id, name, coalesce(key, ''), status, created_at
		  FROM projects
		 WHERE ($1 = '' OR product_id = $1)
		 ORDER BY created_at`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.ProductID, &p.ParentID, &p.Name, &p.Key, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
