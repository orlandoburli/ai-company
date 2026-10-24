package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The kinds a project offers.
//
// items.kind stays free text (0004): a user names their own kinds, and there is
// deliberately no enum. What a project needs is the list it offers — the
// vocabulary the interface presents when somebody picks a kind for a new item.
// The list is an offer, not a constraint: an item may keep a kind its project
// no longer lists, and nothing here validates items.kind.

// ProjectKind is one kind a project offers, and where it sits in the order.
type ProjectKind struct {
	Name     string `json:"name"`
	Position int    `json:"position"`
}

// DefaultProjectKinds is the vocabulary a new project starts with. It is a
// constant of the code, not a setting: a tenant cannot change it, and a project
// may replace it with a list of its own.
var DefaultProjectKinds = []string{"task", "bug", "epic", "story", "spike"}

// MaxProjectKindName is the longest kind name a project may offer. The same
// bound is a CHECK on the column, so the rule holds for every writer.
const MaxProjectKindName = 40

// defaultProjectKindNames returns the defaults as a fresh slice, so a caller
// that edits the list cannot edit the constant.
func defaultProjectKindNames() []string {
	return append([]string(nil), DefaultProjectKinds...)
}

// ListProjectKinds returns the kinds a project offers, in the project's order.
// A project of another tenant is not found rather than empty: the list is
// configuration, and an empty answer would hide a typo in a project id.
func (s *Store) ListProjectKinds(ctx context.Context, sc tenancy.Scope, projectID string) ([]ProjectKind, error) {
	out := []ProjectKind{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireProjectTx(ctx, tx, projectID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT k.name, k.position
			  FROM project_kinds k
			 WHERE k.project_id = $1
			 ORDER BY k.position, k.name`, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k ProjectKind
			if err := rows.Scan(&k.Name, &k.Position); err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, mapPGError(err)
	}
	return out, nil
}

// SetProjectKinds replaces a project's kinds with the given names, in the
// given order. An empty list clears the project: a project may offer no kinds
// at all, and the interface then falls back to free text.
func (s *Store) SetProjectKinds(ctx context.Context, sc tenancy.Scope, projectID string, names []string) error {
	clean, err := normalizeProjectKindNames(names)
	if err != nil {
		return err
	}
	err = s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireProjectTx(ctx, tx, projectID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM project_kinds WHERE project_id = $1`, projectID); err != nil {
			return err
		}
		return insertProjectKinds(ctx, tx, sc.TenantID, projectID, clean)
	})
	return mapPGError(err)
}

// EnsureDefaultProjectKinds seeds the default kinds when the project has none.
// A project that has chosen its own kinds keeps them; this only fills an empty
// list, so it is safe to call on every read of a project's configuration.
func (s *Store) EnsureDefaultProjectKinds(ctx context.Context, sc tenancy.Scope, projectID string) error {
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if err := requireProjectTx(ctx, tx, projectID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM project_kinds WHERE project_id = $1`, projectID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		return insertProjectKinds(ctx, tx, sc.TenantID, projectID, defaultProjectKindNames())
	})
	return mapPGError(err)
}

// normalizeProjectKindNames trims the names and rejects the ones that cannot
// be stored: an empty name, a name longer than the column allows, and the same
// name twice — the last would fail the uniqueness rule deeper in the stack,
// with a message nobody can act on.
func normalizeProjectKindNames(names []string) ([]string, error) {
	clean := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, fmt.Errorf("%w: a kind name cannot be empty", ErrInvalid)
		}
		if len(name) > MaxProjectKindName {
			return nil, fmt.Errorf("%w: the kind name %q is longer than %d characters",
				ErrInvalid, name, MaxProjectKindName)
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: the kind %q is listed twice", ErrInvalid, name)
		}
		seen[name] = true
		clean = append(clean, name)
	}
	return clean, nil
}

// insertProjectKinds writes the names in order, one row each: the order is the
// project's, and it is stored rather than derived from the insertion time.
func insertProjectKinds(ctx context.Context, tx *sql.Tx, tenantID, projectID string, names []string) error {
	for i, name := range names {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO project_kinds (id, tenant_id, project_id, name, position)
			VALUES ($1, $2, $3, $4, $5)`,
			newID(), tenantID, projectID, name, i); err != nil {
			return err
		}
	}
	return nil
}

// requireProjectTx reports whether the project exists in this tenant. The
// lookup runs through row-level security, so a project of another tenant is
// simply not there.
func requireProjectTx(ctx context.Context, tx *sql.Tx, projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return ErrNotFound
	}
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)`, projectID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// --- the labels in use ------------------------------------------------------

// ListLabels returns the labels the tenant's items already use: distinct,
// trimmed, non-empty and sorted. It is the autocomplete's vocabulary, derived
// from the data rather than declared, so there is no label table to keep in
// step with the items.
//
// The project filter is optional: without it the answer is the tenant's labels.
func (s *Store) ListLabels(ctx context.Context, sc tenancy.Scope, projectID string) ([]string, error) {
	out := []string{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		// The labels live in a JSONB array on the item, so the vocabulary is
		// the array unnested: every element of every item's labels, one row
		// each. A row whose labels_json is not an array contributes nothing
		// rather than breaking the query.
		rows, err := tx.QueryContext(ctx, `
			SELECT DISTINCT btrim(elem #>> '{}') AS label
			  FROM items i
			  CROSS JOIN LATERAL jsonb_array_elements(
			      CASE WHEN jsonb_typeof(i.labels_json) = 'array'
			           THEN i.labels_json ELSE '[]'::jsonb END) AS elem
			 WHERE ($1 = '' OR i.project_id = $1)
			   AND jsonb_typeof(elem) = 'string'
			   AND btrim(elem #>> '{}') <> ''
			 ORDER BY label`, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var label string
			if err := rows.Scan(&label); err != nil {
				return err
			}
			out = append(out, label)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, mapPGError(err)
	}
	return out, nil
}

// --- the item's family ------------------------------------------------------

// ItemSummary is the minimum an item's family needs to be readable: enough to
// name the item and show what state it is in.
type ItemSummary struct {
	ID       string  `json:"id"`
	Number   int64   `json:"number"`
	Title    string  `json:"title"`
	Kind     string  `json:"kind"`
	Status   string  `json:"status"`
	Priority *string `json:"priority"`
}

// GetItemFamily returns an item's parent and its direct children.
//
// The parent is the item's own parent, when it has one and that parent is still
// there; a parent that has been deleted leaves the item without a parent
// rather than making the answer an error. The children are the direct
// children only — the tree keeps its own shape elsewhere — ordered the way the
// item's own ordering says: sort_order first, then the number.
func (s *Store) GetItemFamily(ctx context.Context, sc tenancy.Scope, itemID string) (*ItemSummary, []ItemSummary, error) {
	var parent *ItemSummary
	children := []ItemSummary{}

	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var parentID *string
		if err := tx.QueryRowContext(ctx,
			`SELECT parent_id FROM items WHERE id = $1`, itemID).Scan(&parentID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		if parentID != nil && *parentID != "" {
			var p ItemSummary
			err := tx.QueryRowContext(ctx, `
				SELECT id, number, title, kind, status, priority
				  FROM items WHERE id = $1`, *parentID).
				Scan(&p.ID, &p.Number, &p.Title, &p.Kind, &p.Status, &p.Priority)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				// The parent is gone: the item simply has no parent now.
			case err != nil:
				return err
			default:
				parent = &p
			}
		}

		rows, err := tx.QueryContext(ctx, `
			SELECT id, number, title, kind, status, priority
			  FROM items
			 WHERE parent_id = $1
			 ORDER BY sort_order, number`, itemID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c ItemSummary
			if err := rows.Scan(&c.ID, &c.Number, &c.Title, &c.Kind, &c.Status, &c.Priority); err != nil {
				return err
			}
			children = append(children, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, mapPGError(err)
	}
	return parent, children, nil
}
