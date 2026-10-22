package tenancy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Store resolves sessions and memberships. These queries run BEFORE a tenant
// scope exists, so they execute as the table owner rather than through RLS.
type Store struct {
	db *sql.DB
}

// NewStore builds a tenancy store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ErrNotFound is returned when a session, user or membership does not exist.
var ErrNotFound = errors.New("tenancy: not found")

func newID() string { return strings.ToUpper(uuid.NewString()) }

// RegisterTenant creates a tenant and its first user (the owner) atomically.
// The owner is tenant #1 for a self-hosted single-operator install, and the
// registrant of a tenant created through the UI.
func (s *Store) RegisterTenant(ctx context.Context, email, passwordHash, name, tenantName, slug string) (Identity, Membership, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, Membership{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UTC()
	tenantID, userID := newID(), newID()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tenants (id, name, slug, created_at) VALUES ($1, $2, $3, $4)`,
		tenantID, tenantName, slug, now); err != nil {
		return Identity{}, Membership{}, fmt.Errorf("insert tenant: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, name, created_at) VALUES ($1, $2, $3, $4, $5)`,
		userID, strings.ToLower(strings.TrimSpace(email)), passwordHash, name, now); err != nil {
		return Identity{}, Membership{}, fmt.Errorf("insert user: %w", err)
	}
	membershipID := newID()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tenant_memberships (id, tenant_id, user_id, role, created_at) VALUES ($1, $2, $3, $4, $5)`,
		membershipID, tenantID, userID, RoleOwner, now); err != nil {
		return Identity{}, Membership{}, fmt.Errorf("insert membership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Identity{}, Membership{}, fmt.Errorf("commit: %w", err)
	}

	return Identity{
			UserID: userID, Email: email, Name: name,
		},
		Membership{TenantID: tenantID, TenantName: tenantName, TenantSlug: slug, Role: RoleOwner},
		nil
}

// ResolveSession returns the identity and memberships behind a session token
// hash. Expired sessions are rejected rather than returned.
func (s *Store) ResolveSession(ctx context.Context, tokenHash string) (Identity, []Membership, error) {
	var (
		userID, email, name string
		expiresAt           time.Time
		tenantID            string
	)
	// session_id intentionally not selected
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.name, s.expires_at, s.tenant_id
		  FROM sessions s
		  JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1`, tokenHash).
		Scan(&userID, &email, &name, &expiresAt, &tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, nil, ErrNotFound
	}
	if err != nil {
		return Identity{}, nil, fmt.Errorf("resolve session: %w", err)
	}
	if time.Now().After(expiresAt) {
		return Identity{}, nil, ErrNotFound
	}

	memberships, err := s.membershipsFor(ctx, userID)
	if err != nil {
		return Identity{}, nil, err
	}
	return Identity{
		UserID: userID, Email: email, Name: name,
		Memberships: memberships, ExpiresAt: expiresAt,
	}, memberships, nil
}

func (s *Store) membershipsFor(ctx context.Context, userID string) ([]Membership, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.slug, m.role
		  FROM tenant_memberships m
		  JOIN tenants t ON t.id = m.tenant_id
		 WHERE m.user_id = $1
		 ORDER BY t.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("memberships: %w", err)
	}
	defer rows.Close()

	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.TenantID, &m.TenantName, &m.TenantSlug, &m.Role); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CreateSession issues a session for a user in a tenant they belong to. It
// refuses to mint a session for a tenant the user is not a member of.
func (s *Store) CreateSession(ctx context.Context, userID, tenantID, tokenHash string, ttl time.Duration) (string, error) {
	var allowed bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM tenant_memberships
		                WHERE user_id = $1 AND tenant_id = $2)`,
		userID, tenantID).Scan(&allowed)
	if err != nil {
		return "", fmt.Errorf("check membership: %w", err)
	}
	if !allowed {
		// Refusing here matters: otherwise a valid session token could be
		// paired with a tenant the user has no membership in.
		return "", ErrNotFound
	}

	id := newID()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, tenant_id, token_hash, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, userID, tenantID, tokenHash, time.Now().UTC().Add(ttl), time.Now().UTC()); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return id, nil
}
