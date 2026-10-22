// Package tenancy resolves every request to a tenant and exposes the scope that
// repository methods require.
//
// The rule the rest of the codebase depends on: a repository method takes a
// Scope as its first argument, and a query that omits tenant_id is a compile-time
// or review-time mistake rather than a silent leak. Postgres RLS is the backstop
// (see migrations/0001_tenancy.sql), not the primary mechanism — defence in
// depth means both.
package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Role is a tenant membership role, ordered from most to least privileged.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleGuest  Role = "guest"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleMember, RoleGuest:
		return true
	}
	return false
}

// CanDecideCheckpoints reports whether the role may approve/reject a checkpoint.
func (r Role) CanDecideCheckpoints() bool { return r == RoleOwner || r == RoleAdmin || r == RoleMember }

// CanManageSettings reports whether the role may change agents, integrations or templates.
func (r Role) CanManageSettings() bool { return r == RoleOwner || r == RoleAdmin }

// IsOwner reports whether the role may manage the tenant itself.
func (r Role) IsOwner() bool { return r == RoleOwner }

// ErrNoScope is returned when code reaches the store without a tenant scope. It
// is deliberately a loud error: the alternative is an unscoped query returning
// every tenant's rows.
var ErrNoScope = errors.New("tenancy: no tenant scope in context")

// ErrForbidden is returned when a role lacks the permission required.
var ErrForbidden = errors.New("tenancy: forbidden")

// Scope identifies the tenant (and acting user) a unit of work runs under. It is
// passed explicitly to every repository method AND carried in context for RLS.
type Scope struct {
	TenantID string
	UserID   string
	Role     Role
}

// Validate reports whether the scope is usable.
func (s Scope) Validate() error {
	if strings.TrimSpace(s.TenantID) == "" {
		return fmt.Errorf("%w: empty tenant id", ErrNoScope)
	}
	if !s.Role.Valid() {
		return fmt.Errorf("%w: unknown role %q", ErrForbidden, s.Role)
	}
	return nil
}

// Require returns the scope from ctx or ErrNoScope.
func Require(ctx context.Context) (Scope, error) {
	s, ok := ctx.Value(scopeKey{}).(Scope)
	if !ok {
		return Scope{}, ErrNoScope
	}
	if err := s.Validate(); err != nil {
		return Scope{}, err
	}
	return s, nil
}

type scopeKey struct{}

// WithScope attaches a scope to ctx. Callers on the request path use this after
// resolving the session; worker goroutines must carry the same scope explicitly,
// because a background job that reaches for context.Background() is the realistic
// cross-tenant leak (mvp-design.md §12).
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// sessionKey carries the resolved identity before a tenant is chosen (the
// user may belong to several tenants).
type sessionKey struct{}

// Identity is a resolved session, before tenant selection.
type Identity struct {
	UserID      string
	Email       string
	Name        string
	Memberships []Membership
	ExpiresAt   time.Time
}

// Membership is one tenant a user belongs to.
type Membership struct {
	TenantID   string
	TenantName string
	TenantSlug string
	Role       Role
}

// WithIdentity attaches a resolved identity to ctx.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, sessionKey{}, id)
}

// IdentityFrom returns the resolved identity, if any.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(sessionKey{}).(Identity)
	return id, ok
}
