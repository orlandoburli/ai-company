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
	// Language is the language the company works in. It is optional: an empty
	// value leaves the setting unchanged, and an unknown one is refused.
	Language string `json:"language"`
}

// tenantSettingsView is the company's own settings: who it is, and the
// language its work is done in.
//
// The language is a company setting, not a per-user preference, so it belongs
// with the tenant's identity rather than with the session.
type tenantSettingsView struct {
	tenantView
	Language tenancy.Language `json:"language"`
}

type setRoleRequest struct {
	Role string `json:"role"`
}

// The tenant's default variables.
//
// They are what every run of this tenant inherits: the run's own variables
// win, and a variable nobody set is simply absent. Reading them is open to
// every role — a default is configuration everybody may need to see;
// changing them is a settings surface, so owner/admin only.
type tenantVariablesRequest struct {
	Variables map[string]string `json:"variables"`
}

// registerTenantVariableRoutes mounts the tenant's default variables.
func (s *Server) registerTenantVariableRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/tenant/variables", s.authenticated(s.handleGetTenantVariables))
	mux.Handle("PUT /api/tenant/variables",
		s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handlePutTenantVariables)))
}

func (s *Server) handleGetTenantVariables(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.variables == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "The variable store is not configured.")
		return
	}
	vars, err := s.variables.TenantVariables(r.Context(), sc)
	if err != nil {
		s.internal(w, "read tenant variables", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"variables": vars})
}

func (s *Server) handlePutTenantVariables(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.variables == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "The variable store is not configured.")
		return
	}
	var req tenantVariablesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Variables == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "A variables object is required.")
		return
	}
	clean := make(map[string]string, len(req.Variables))
	for name, value := range req.Variables {
		name = strings.TrimSpace(name)
		if !validVariableName(name) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_request",
				"A variable name starts with a letter or an underscore and holds letters, digits and underscores only (64 characters at most).")
			return
		}
		clean[name] = value
	}
	if err := s.variables.SetTenantVariables(r.Context(), sc, clean); err != nil {
		s.internal(w, "write tenant variables", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"variables": clean})
}

// validVariableName matches the CHECK on tenant_variables.name: the name is
// referenced from a template's config as $NAME, so it is a plain identifier.
func validVariableName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := tenancy.IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}
	tenant := tenantSettingsView{tenantView: tenantView{ID: id.TenantID, Role: id.Role}}
	for _, m := range id.Memberships {
		if m.TenantID == id.TenantID {
			tenant.Name, tenant.Slug = m.TenantName, m.TenantSlug
			break
		}
	}
	language, err := s.tenantLanguage(r)
	if err != nil {
		s.internal(w, "read tenant language", err)
		return
	}
	tenant.Language = language
	writeJSON(w, http.StatusOK, map[string]any{"tenant": tenant})
}

// tenantLanguage reads the language the company works in.
//
// The read goes through the identity plane's function surface, keyed on the
// session, so it can only ever return the tenant the request acts in.
func (s *Server) tenantLanguage(r *http.Request) (tenancy.Language, error) {
	digest, ok := tenancy.SessionHashFrom(r.Context())
	if !ok {
		return "", tenancy.ErrUnauthenticated
	}
	return s.tenancy.TenantLanguage(r.Context(), digest)
}

func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	var req updateTenantRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	name := strings.TrimSpace(req.Name)
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	language := strings.TrimSpace(req.Language)

	// Empty means "leave unchanged"; anything provided is validated.
	if name != "" && (len(name) > 200) {
		writeError(w, http.StatusBadRequest, "invalid_tenant_name", "Company name must be 200 characters or fewer.")
		return
	}
	if slug != "" && !validSlug(slug) {
		writeError(w, http.StatusBadRequest, "invalid_tenant_slug", "The company handle must be 3-63 characters: lowercase letters, digits and hyphens.")
		return
	}
	if language != "" && !tenancy.Language(language).Valid() {
		writeError(w, http.StatusBadRequest, "invalid_request", "Language must be one of en, pt-BR.")
		return
	}
	if name == "" && slug == "" && language == "" {
		writeError(w, http.StatusBadRequest, "no_changes", "Provide a name, a handle or a language to change.")
		return
	}

	digest, ok := tenancy.SessionHashFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}

	if name != "" || slug != "" {
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
	}

	if language != "" {
		err := s.tenancy.SetTenantLanguage(r.Context(), digest, tenancy.Language(language))
		switch {
		case err == nil:
		case errors.Is(err, tenancy.ErrForbiddenRole):
			writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow changing company settings.")
			return
		case errors.Is(err, tenancy.ErrInvalidLanguage):
			writeError(w, http.StatusBadRequest, "invalid_request", "Language must be one of en, pt-BR.")
			return
		case errors.Is(err, tenancy.ErrUnauthenticated):
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		default:
			s.internal(w, "update tenant language", err)
			return
		}
	}

	// Re-resolve so the response reflects what the next request will see, rather
	// than echoing the request body back as if it were stored.
	id, _, err := s.tenancy.ResolveSession(r.Context(), digest)
	if err != nil {
		s.internal(w, "reload tenant", err)
		return
	}
	tenant := tenantSettingsView{tenantView: tenantView{ID: id.TenantID, Role: id.Role}}
	for _, m := range id.Memberships {
		if m.TenantID == id.TenantID {
			tenant.Name, tenant.Slug = m.TenantName, m.TenantSlug
			break
		}
	}
	current, err := s.tenantLanguage(r)
	if err != nil {
		s.internal(w, "read tenant language", err)
		return
	}
	tenant.Language = current
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
