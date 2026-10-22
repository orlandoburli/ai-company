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

// Store resolves sessions, memberships and tenants.
//
// Every method here goes through a SECURITY DEFINER function (see
// migrations/0002_identity_force_rls.sql) because the application role has no
// table privileges on the identity plane and every tenant-scoped table is
// FORCE ROW LEVEL SECURITY. There is deliberately no privileged connection
// available to request-path code: the privileged surface is a short list of
// functions, each keyed on a session token or an email rather than on a
// caller-supplied "which tenant" parameter.
type Store struct {
	db *sql.DB
}

// NewStore builds a tenancy store over the application connection.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ErrNotFound is returned when a session, user or membership does not exist.
var ErrNotFound = errors.New("tenancy: not found")

func newID() string { return strings.ToUpper(uuid.NewString()) }

// RegisterTenant creates a tenant, its first user (the owner) and the owner
// membership in one statement, so a tenant never exists without an owner.
func (s *Store) RegisterTenant(ctx context.Context, email, passwordHash, name, tenantName, slug string) (Identity, Membership, error) {
	tenantID, userID, membershipID := newID(), newID(), newID()
	email = strings.ToLower(strings.TrimSpace(email))

	if _, err := s.db.ExecContext(ctx, `
		SELECT public.auth_register_tenant($1, $2, $3, $4, $5, $6, $7, $8)`,
		tenantID, tenantName, slug, userID, email, passwordHash, name, membershipID); err != nil {
		return Identity{}, Membership{}, fmt.Errorf("register tenant: %w", err)
	}

	m := Membership{TenantID: tenantID, TenantName: tenantName, TenantSlug: slug, Role: RoleOwner}
	return Identity{
		UserID: userID, Email: email, Name: name,
		TenantID: tenantID, Role: RoleOwner,
		Memberships: []Membership{m},
	}, m, nil
}

// ResolveSession returns the identity and memberships behind a session token
// digest. Expiry is evaluated in Postgres against the database clock, so a
// skewed application host cannot extend a session.
func (s *Store) ResolveSession(ctx context.Context, tokenHash string) (Identity, []Membership, error) {
	var (
		sessionID, userID, tenantID, email, name string
		expiresAt                                time.Time
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT session_id, user_id, tenant_id, email, name, expires_at
		  FROM public.auth_resolve_session($1)`, tokenHash).
		Scan(&sessionID, &userID, &tenantID, &email, &name, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, nil, ErrNotFound
	}
	if err != nil {
		return Identity{}, nil, fmt.Errorf("resolve session: %w", err)
	}

	// Memberships are derived from the same token inside SQL, so this call cannot
	// be pointed at another user.
	memberships, err := s.sessionMemberships(ctx, tokenHash)
	if err != nil {
		return Identity{}, nil, err
	}

	// The role is read from the membership on every request, so a demotion takes
	// effect immediately. A session whose membership has been revoked stops
	// resolving even though the session row still exists.
	var active Membership
	found := false
	for _, m := range memberships {
		if m.TenantID == tenantID {
			active, found = m, true
			break
		}
	}
	if !found {
		return Identity{}, nil, ErrNotFound
	}

	return Identity{
		UserID: userID, Email: email, Name: name,
		TenantID: active.TenantID, Role: active.Role,
		Memberships: memberships, ExpiresAt: expiresAt,
	}, memberships, nil
}

func (s *Store) sessionMemberships(ctx context.Context, tokenHash string) ([]Membership, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id, tenant_name, tenant_slug, role
		  FROM public.auth_session_memberships($1)`, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("session memberships: %w", err)
	}
	defer rows.Close()
	return scanMemberships(rows)
}

// MembershipsForUser lists every tenant a user belongs to. Used by login, where
// the user id comes from auth_user_by_email and not from the request body.
func (s *Store) MembershipsForUser(ctx context.Context, userID string) ([]Membership, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id, tenant_name, tenant_slug, role
		  FROM public.auth_memberships_for_user($1)`, userID)
	if err != nil {
		return nil, fmt.Errorf("memberships: %w", err)
	}
	defer rows.Close()
	return scanMemberships(rows)
}

func scanMemberships(rows *sql.Rows) ([]Membership, error) {
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

// CreateSession issues a session for a user in a tenant they belong to. The
// membership check happens inside the function, so a code path that skips the
// application-level check still cannot mint a cross-tenant session.
func (s *Store) CreateSession(ctx context.Context, userID, tenantID, tokenHash string, ttl time.Duration) (string, error) {
	id := newID()
	var created bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT public.auth_create_session($1, $2, $3, $4, $5)`,
		id, userID, tenantID, tokenHash, time.Now().UTC().Add(ttl)).
		Scan(&created); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	if !created {
		return "", ErrNotFound
	}
	return id, nil
}

// DeleteSession revokes a session by its token digest. Deleting an already
// absent session is not an error: logout must be idempotent.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx, `SELECT public.auth_delete_session($1)`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions revokes every session for a user, used on password change
// and on account disable.
func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	if _, err := s.db.ExecContext(ctx, `SELECT public.auth_delete_user_sessions($1)`, userID); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

// User is the identity-plane record needed to authenticate. PasswordHash is read
// only through auth_user_by_email; the app role has no column privilege that
// would let it select users at all.
type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
}

// FindUserByEmail returns the user with this email. It returns ErrNotFound for
// unknown addresses; callers must not surface that distinction, because it turns
// login into an account-enumeration oracle.
func (s *Store) FindUserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT id, email, name, password_hash
		  FROM public.auth_user_by_email($1)`,
		strings.TrimSpace(email)).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("find user: %w", err)
	}
	return u, nil
}
