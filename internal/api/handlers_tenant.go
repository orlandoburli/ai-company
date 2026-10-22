package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Tenant settings and member management.
//
// Authorisation is enforced three times, deliberately: the route is wrapped in
// requireRole, the database function re-checks the role from the session's own
// membership, and the role accepted from the client is validated against a fixed
// set. The middle check is the one that survives an application bug.

type memberView struct {
	UserID   string       `json:"user_id"`
	Email    string       `json:"email"`
	Name     string       `json:"name"`
	Role     tenancy.Role `json:"role"`
	JoinedAt string       `json:"joined_at"`
}

type updateTenantRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type setRoleRequest struct {
	Role string `json:"role"`
}

func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := tenancy.IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}
	tenant := tenantView{ID: id.TenantID, Role: id.Role}
	for _, m := range id.Memberships {
		if m.TenantID == id.TenantID {
			tenant.Name, tenant.Slug = m.TenantName, m.TenantSlug
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": tenant})
}

func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	var req updateTenantRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	name := strings.TrimSpace(req.Name)
	slug := strings.ToLower(strings.TrimSpace(req.Slug))

	// Empty means "leave unchanged"; anything provided is validated.
	if name != "" && (len(name) > 200) {
		writeError(w, http.StatusBadRequest, "invalid_tenant_name", "Company name must be 200 characters or fewer.")
		return
	}
	if slug != "" && !validSlug(slug) {
		writeError(w, http.StatusBadRequest, "invalid_tenant_slug", "The company handle must be 3-63 characters: lowercase letters, digits and hyphens.")
		return
	}
	if name == "" && slug == "" {
		writeError(w, http.StatusBadRequest, "no_changes", "Provide a name or a handle to change.")
		return
	}

	digest, ok := tenancy.SessionHashFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}

	err := s.tenancy.UpdateTenant(r.Context(), digest, name, slug)
	switch {
	case err == nil:
	case errors.Is(err, tenancy.ErrForbiddenRole):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow changing company settings.")
		return
	case isUniqueViolation(err):
		writeError(w, http.StatusConflict, "conflict", "That company handle is already in use.")
		return
	default:
		s.internal(w, "update tenant", err)
		return
	}

	// Re-resolve so the response reflects what the next request will see, rather
	// than echoing the request body back as if it were stored.
	id, _, err := s.tenancy.ResolveSession(r.Context(), digest)
	if err != nil {
		s.internal(w, "reload tenant", err)
		return
	}
	tenant := tenantView{ID: id.TenantID, Role: id.Role}
	for _, m := range id.Memberships {
		if m.TenantID == id.TenantID {
			tenant.Name, tenant.Slug = m.TenantName, m.TenantSlug
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": tenant})
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	digest, ok := tenancy.SessionHashFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}

	members, err := s.tenancy.ListMembers(r.Context(), digest)
	if err != nil {
		s.internal(w, "list members", err)
		return
	}

	views := make([]memberView, 0, len(members))
	for _, m := range members {
		views = append(views, memberView{
			UserID:   m.UserID,
			Email:    m.Email,
			Name:     m.Name,
			Role:     m.Role,
			JoinedAt: m.JoinedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": views})
}

func (s *Server) handleSetMemberRole(w http.ResponseWriter, r *http.Request) {
	targetUserID := r.PathValue("userID")
	if targetUserID == "" {
		writeError(w, http.StatusBadRequest, "invalid_target", "A member id is required.")
		return
	}

	var req setRoleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	role := tenancy.Role(strings.TrimSpace(req.Role))
	if !role.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_role", "Role must be one of owner, admin, member, guest.")
		return
	}

	digest, ok := tenancy.SessionHashFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}

	err := s.tenancy.SetMemberRole(r.Context(), digest, targetUserID, role)
	switch {
	case err == nil:
	case errors.Is(err, tenancy.ErrForbiddenRole):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow changing members.")
		return
	case errors.Is(err, tenancy.ErrSelfRoleChange):
		writeError(w, http.StatusConflict, "cannot_change_own_role", "You cannot change your own role.")
		return
	case errors.Is(err, tenancy.ErrLastOwner):
		writeError(w, http.StatusConflict, "last_owner", "A company must keep at least one owner.")
		return
	case errors.Is(err, tenancy.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "That person is not a member of this company.")
		return
	case errors.Is(err, tenancy.ErrInvalidRole):
		writeError(w, http.StatusBadRequest, "invalid_role", "Role must be one of owner, admin, member, guest.")
		return
	default:
		s.internal(w, "set member role", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"user_id": targetUserID, "role": role})
}
