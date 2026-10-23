package skill

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func newID() string { return strings.ToUpper(uuid.NewString()) }

func (s *Store) withTenant(ctx context.Context, sc tenancy.Scope, fn func(*sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("skill: begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		return fmt.Errorf("skill: set tenant scope: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func requireManager(sc tenancy.Scope) error {
	if !sc.Role.CanManageSettings() {
		return tenancy.ErrForbidden
	}
	return nil
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func validateNew(in NewSkill) error {
	if !slugPattern.MatchString(in.Slug) || strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || len(in.Description) > 10000 {
		return fmt.Errorf("%w: name, slug or description is invalid", ErrInvalid)
	}
	if in.ProductID != nil && strings.TrimSpace(*in.ProductID) == "" {
		return fmt.Errorf("%w: product_id is empty", ErrInvalid)
	}
	return nil
}

const skillColumns = `id, tenant_id, product_id, name, slug, description, current_version, enabled, created_by, created_at`

type scanner interface{ Scan(...any) error }

func scanSkill(row scanner) (Skill, error) {
	var v Skill
	var product, createdBy sql.NullString
	if err := row.Scan(&v.ID, &v.TenantID, &product, &v.Name, &v.Slug, &v.Description,
		&v.CurrentVersion, &v.Enabled, &createdBy, &v.CreatedAt); err != nil {
		return Skill{}, err
	}
	v.ProductID, v.CreatedBy = stringPtr(product), stringPtr(createdBy)
	return v, nil
}

func stringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	value := v.String
	return &value
}

func (s *Store) Create(ctx context.Context, sc tenancy.Scope, in NewSkill) (out Skill, err error) {
	if err := requireManager(sc); err != nil {
		return out, err
	}
	if err := validateNew(in); err != nil {
		return out, err
	}
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		out, err = scanSkill(tx.QueryRowContext(ctx, `INSERT INTO skills
			(id, tenant_id, product_id, name, slug, description, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,'')) RETURNING `+skillColumns,
			newID(), sc.TenantID, in.ProductID, strings.TrimSpace(in.Name), in.Slug,
			in.Description, sc.UserID))
		if err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			if isForeignKeyViolation(err) {
				return ErrNotFound
			}
		}
		return err
	})
	return out, err
}

func (s *Store) Update(ctx context.Context, sc tenancy.Scope, id string, p Patch) (out Skill, err error) {
	if err := requireManager(sc); err != nil {
		return out, err
	}
	if p.Name == nil && p.Slug == nil && p.Description == nil {
		return s.Get(ctx, sc, id)
	}
	if p.Name != nil && (strings.TrimSpace(*p.Name) == "" || len(*p.Name) > 200) ||
		p.Slug != nil && !slugPattern.MatchString(*p.Slug) || p.Description != nil && len(*p.Description) > 10000 {
		return out, fmt.Errorf("%w: invalid skill patch", ErrInvalid)
	}
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		out, err = scanSkill(tx.QueryRowContext(ctx, `UPDATE skills SET
			name = COALESCE($3, name), slug = COALESCE($4, slug), description = COALESCE($5, description)
			WHERE id = $1 AND tenant_id = $2 RETURNING `+skillColumns,
			id, sc.TenantID, p.Name, p.Slug, p.Description))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	})
	return out, err
}

func (s *Store) List(ctx context.Context, sc tenancy.Scope, productID string) ([]Skill, error) {
	var out []Skill
	err := s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		var rows *sql.Rows
		var err error
		if productID == "" {
			rows, err = tx.QueryContext(ctx, `SELECT `+skillColumns+` FROM skills ORDER BY slug, product_id NULLS FIRST`)
		} else {
			rows, err = tx.QueryContext(ctx, `SELECT `+skillColumns+` FROM skills
				WHERE product_id IS NULL OR product_id = $1
				ORDER BY (product_id IS NULL), slug`, productID)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		out = make([]Skill, 0)
		for rows.Next() {
			item, err := scanSkill(rows)
			if err != nil {
				return err
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) Get(ctx context.Context, sc tenancy.Scope, id string) (out Skill, err error) {
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		out, err = scanSkill(tx.QueryRowContext(ctx, `SELECT `+skillColumns+` FROM skills WHERE id=$1 AND tenant_id=$2`, id, sc.TenantID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

// GetBySlug checks a product-local skill first and then a tenant-wide one.
func (s *Store) GetBySlug(ctx context.Context, sc tenancy.Scope, productID, slug string) (out Skill, found bool, err error) {
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		var row *sql.Row
		if productID == "" {
			row = tx.QueryRowContext(ctx, `SELECT `+skillColumns+` FROM skills WHERE slug=$1 AND product_id IS NULL`, slug)
		} else {
			row = tx.QueryRowContext(ctx, `SELECT `+skillColumns+` FROM skills
				WHERE slug=$1 AND (product_id=$2 OR product_id IS NULL)
				ORDER BY (product_id IS NULL) LIMIT 1`, slug, productID)
		}
		out, err = scanSkill(row)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
			return nil
		}
		if err == nil {
			found = true
		}
		return err
	})
	return out, found, err
}

func (s *Store) AddVersion(ctx context.Context, sc tenancy.Scope, skillID string, in NewVersion) (out Version, err error) {
	if err := requireManager(sc); err != nil {
		return out, err
	}
	authorKind := in.AuthorKind
	if authorKind == "" {
		authorKind = "human"
	}
	if (authorKind != "human" && authorKind != "agent_draft") || strings.TrimSpace(in.ContentMD) == "" || len(in.ContentMD) > 1<<20 {
		return out, fmt.Errorf("%w: invalid version content or author_kind", ErrInvalid)
	}
	sha := fmt.Sprintf("%x", sha256.Sum256([]byte(in.ContentMD)))
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		var lockedID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM skills WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, skillID, sc.TenantID).Scan(&lockedID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM skill_versions WHERE skill_id=$1`, skillID).Scan(&version); err != nil {
			return err
		}
		var approver any
		authorID := in.AuthorID
		if authorKind == "human" {
			approver = nullableString(sc.UserID)
			if authorID == nil && sc.UserID != "" {
				authorID = &sc.UserID
			}
		}
		out, err = scanVersion(tx.QueryRowContext(ctx, `INSERT INTO skill_versions
			(id, tenant_id, skill_id, version, content_md, content_sha, author_kind, author_id, approved_by, approved_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,CASE WHEN $9::text IS NULL THEN NULL ELSE now() END)
			RETURNING `+versionColumns, newID(), sc.TenantID, skillID, version, in.ContentMD, sha,
			authorKind, authorID, approver))
		if err != nil {
			return err
		}
		if authorKind == "human" {
			_, err = tx.ExecContext(ctx, `UPDATE skills SET current_version=$3 WHERE id=$1 AND tenant_id=$2`, skillID, sc.TenantID, version)
		}
		return err
	})
	return out, err
}

func (s *Store) Approve(ctx context.Context, sc tenancy.Scope, versionID, actorUserID string) (out Version, err error) {
	if err := requireManager(sc); err != nil {
		return out, err
	}
	if actorUserID == "" || actorUserID != sc.UserID {
		return out, fmt.Errorf("%w: approval actor must be the scoped user", ErrInvalid)
	}
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		out, err = scanVersion(tx.QueryRowContext(ctx, `UPDATE skill_versions SET approved_by=$3, approved_at=now()
			WHERE id=$1 AND tenant_id=$2 AND author_kind='agent_draft' AND approved_at IS NULL
			RETURNING `+versionColumns, versionID, sc.TenantID, actorUserID))
		if errors.Is(err, sql.ErrNoRows) {
			// Make approvals idempotent but do not permit approving a human version.
			out, err = scanVersion(tx.QueryRowContext(ctx, `SELECT `+versionColumns+` FROM skill_versions
				WHERE id=$1 AND tenant_id=$2 AND author_kind='agent_draft' AND approved_at IS NOT NULL`, versionID, sc.TenantID))
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE skills SET current_version=$3 WHERE id=$1 AND tenant_id=$2`, out.SkillID, sc.TenantID, out.Version)
		return err
	})
	return out, err
}

const versionColumns = `id, skill_id, version, content_md, content_sha, author_kind, author_id, approved_by, approved_at, created_at`

func scanVersion(row scanner) (Version, error) {
	var v Version
	var author, approver sql.NullString
	var approvedAt sql.NullTime
	if err := row.Scan(&v.ID, &v.SkillID, &v.Version, &v.ContentMD, &v.ContentSHA, &v.AuthorKind,
		&author, &approver, &approvedAt, &v.CreatedAt); err != nil {
		return Version{}, err
	}
	v.AuthorID, v.ApprovedBy = stringPtr(author), stringPtr(approver)
	if approvedAt.Valid {
		at := approvedAt.Time
		v.ApprovedAt = &at
	}
	return v, nil
}

func (s *Store) Version(ctx context.Context, sc tenancy.Scope, skillID string, version int) (out Version, found bool, err error) {
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		out, err = scanVersion(tx.QueryRowContext(ctx, `SELECT `+versionColumns+` FROM skill_versions
			WHERE skill_id=$1 AND version=$2 AND tenant_id=$3`, skillID, version, sc.TenantID))
		if errors.Is(err, sql.ErrNoRows) {
			err, found = nil, false
		} else if err == nil {
			found = true
		}
		return err
	})
	return out, found, err
}

func (s *Store) CurrentVersion(ctx context.Context, sc tenancy.Scope, skillID string) (out Version, found bool, err error) {
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		out, err = scanVersion(tx.QueryRowContext(ctx, `SELECT v.id, v.skill_id, v.version, v.content_md, v.content_sha,
			v.author_kind, v.author_id, v.approved_by, v.approved_at, v.created_at
			FROM skills s JOIN skill_versions v ON v.skill_id=s.id AND v.version=s.current_version AND v.tenant_id=s.tenant_id
			WHERE s.id=$1 AND s.tenant_id=$2 AND s.current_version > 0 AND v.approved_at IS NOT NULL`, skillID, sc.TenantID))
		if errors.Is(err, sql.ErrNoRows) {
			err, found = nil, false
		} else if err == nil {
			found = true
		}
		return err
	})
	return out, found, err
}

// Versions returns version history newest first, including inert drafts.
func (s *Store) Versions(ctx context.Context, sc tenancy.Scope, skillID string) (out []Version, err error) {
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+versionColumns+` FROM skill_versions
			WHERE skill_id=$1 AND tenant_id=$2 ORDER BY version DESC`, skillID, sc.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = make([]Version, 0)
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) SetEnabled(ctx context.Context, sc tenancy.Scope, id string, enabled bool) (out Skill, err error) {
	if err := requireManager(sc); err != nil {
		return out, err
	}
	err = s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		out, err = scanSkill(tx.QueryRowContext(ctx, `UPDATE skills SET enabled=$3 WHERE id=$1 AND tenant_id=$2 RETURNING `+skillColumns, id, sc.TenantID, enabled))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

func (s *Store) Delete(ctx context.Context, sc tenancy.Scope, id string) error {
	if err := requireManager(sc); err != nil {
		return err
	}
	return s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM skills WHERE id=$1 AND tenant_id=$2`, id, sc.TenantID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return ErrNotFound
		}
		return err
	})
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
