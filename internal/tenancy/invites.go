package tenancy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Invitations (FR-TN-7).
//
// The raw token never reaches the database: only its SHA-256 digest is stored,
// the same rule as session tokens. Creation returns the digest-bearing invite to
// the caller, which hands the plaintext token to the invitee once.
//
// Acceptance is the only membership write reachable without a session. It is
// keyed on the invite token and returns no rows for unknown, expired, revoked or
// already-used tokens, so the caller cannot distinguish them.

// Invite is a pending invitation.
type Invite struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// AcceptedInvite is the result of a successful acceptance.
type AcceptedInvite struct {
	TenantID string
	UserID   string
	Email    string
	Role     Role
}

// CreateInvite records an invitation for the session's tenant. The caller is
// responsible for generating the token digest and expiry.
func (s *Store) CreateInvite(ctx context.Context, sessionHash, email string, role Role, tokenHash string, expiresAt time.Time) (Invite, error) {
	inv := Invite{
		ID:        newID(),
		Email:     email,
		Role:      role,
		ExpiresAt: expiresAt,
	}
	var reason sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT public.auth_invite_create($1, $2, $3, $4, $5, $6)`,
		sessionHash, inv.ID, email, string(role), tokenHash, expiresAt).Scan(&reason); err != nil {
		return Invite{}, fmt.Errorf("create invite: %w", err)
	}
	if err := roleError(reason); err != nil {
		return Invite{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT created_at FROM public.auth_invite_list($1) WHERE id = $2`,
		sessionHash, inv.ID).Scan(&inv.CreatedAt); err != nil {
		// The invite exists; a failed read-back is not worth failing the request
		// over, but the timestamp is what the UI sorts by.
		return inv, nil
	}
	return inv, nil
}

// ListInvites returns pending invitations. Non-admins get an empty list.
func (s *Store) ListInvites(ctx context.Context, sessionHash string) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email, role, expires_at, created_at
		  FROM public.auth_invite_list($1)`, sessionHash)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()

	out := []Invite{}
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.ID, &inv.Email, &inv.Role, &inv.ExpiresAt, &inv.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeInvite withdraws a pending invitation.
func (s *Store) RevokeInvite(ctx context.Context, sessionHash, inviteID string) error {
	var reason sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT public.auth_invite_revoke($1, $2)`,
		sessionHash, inviteID).Scan(&reason); err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	return roleError(reason)
}

// PeekInvite returns the tenant, email, role and expiry behind a raw invite
// token digest, without consuming it. No rows means the token is not usable.
func (s *Store) PeekInvite(ctx context.Context, tokenHash string) (tenantName, email string, role Role, expiresAt time.Time, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant_name, email, role, expires_at
		  FROM public.auth_invite_peek($1)`, tokenHash).
		Scan(&tenantName, &email, &role, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("peek invite: %w", err)
	}
	return tenantName, email, role, expiresAt, nil
}

// AcceptInvite joins the invitee to the tenant. passwordHash is used only when
// the email has no account yet; an existing account keeps its password.
func (s *Store) AcceptInvite(ctx context.Context, tokenHash, passwordHash, name string) (AcceptedInvite, error) {
	userID, membershipID := newID(), newID()
	var out AcceptedInvite
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant_id, user_id, email, role
		  FROM public.auth_invite_accept($1, $2, $3, $4, $5)`,
		tokenHash, userID, passwordHash, name, membershipID).
		Scan(&out.TenantID, &out.UserID, &out.Email, &out.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return AcceptedInvite{}, ErrNotFound
	}
	if err != nil {
		return AcceptedInvite{}, fmt.Errorf("accept invite: %w", err)
	}
	return out, nil
}
