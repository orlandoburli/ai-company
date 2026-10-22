package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/orlandoburli/ai-company/internal/auth"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// --- payload types ---------------------------------------------------------

type userView struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type tenantView struct {
	ID   string       `json:"id"`
	Name string       `json:"name"`
	Slug string       `json:"slug"`
	Role tenancy.Role `json:"role"`
}

type meResponse struct {
	User    userView     `json:"user"`
	Tenant  tenantView   `json:"tenant"`
	Tenants []tenantView `json:"tenants"`
}

type registerRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	Name       string `json:"name"`
	TenantName string `json:"tenant_name"`
	TenantSlug string `json:"tenant_slug"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// TenantID is optional: omit it when the user belongs to exactly one tenant.
	TenantID string `json:"tenant_id"`
}

type switchTenantRequest struct {
	TenantID string `json:"tenant_id"`
}

// --- handlers --------------------------------------------------------------

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	email := normalizeEmail(req.Email)
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "invalid_email", "Enter a valid email address.")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_name", "Enter your name.")
		return
	}
	tenantName := strings.TrimSpace(req.TenantName)
	if tenantName == "" || len(tenantName) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_tenant_name", "Enter a company name.")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(req.TenantSlug))
	if !validSlug(slug) {
		writeError(w, http.StatusBadRequest, "invalid_tenant_slug", "The company handle must be 3-63 characters: lowercase letters, digits and hyphens.")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrWeakPassword) {
			writeError(w, http.StatusBadRequest, "weak_password", err.Error())
			return
		}
		s.internal(w, "hash password", err)
		return
	}

	// Registration creates the tenant and its owner in one transaction; there is
	// no state where a tenant exists without an owner.
	id, m, err := s.tenancy.RegisterTenant(r.Context(), email, hash, name, tenantName, slug)
	if err != nil {
		if isUniqueViolation(err) {
			// Deliberately vague: distinguishing email from slug would reveal
			// which addresses already have accounts.
			writeError(w, http.StatusConflict, "conflict", "That email or company handle is already in use.")
			return
		}
		s.internal(w, "register tenant", err)
		return
	}

	s.startSession(w, r, id.UserID, m.TenantID, http.StatusCreated)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := normalizeEmail(req.Email)

	// The limiter key is client IP + lowercased email (see clientKey for why
	// the IP is r.RemoteAddr). The counter is charged only on a failed
	// verification below, and cleared on success, so a burst of wrong
	// passwords exhausts the bucket while a legitimate attempt that happens to
	// follow one is not locked out.
	key := clientKey(r) + "\x00" + email

	user, err := s.tenancy.FindUserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, tenancy.ErrNotFound) {
		s.internal(w, "find user", err)
		return
	}

	// Always spend the same work: an unknown email takes one dummy Argon2id
	// verification, so response time does not reveal whether the account exists.
	if err := auth.VerifyOrDummy(user.PasswordHash, req.Password); err != nil {
		if !s.rateAllow(w, BucketLogin, key) {
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Email or password is incorrect.")
		return
	}
	s.rateClear(BucketLogin, key)

	memberships, err := s.tenancy.MembershipsForUser(r.Context(), user.ID)
	if err != nil {
		s.internal(w, "list memberships", err)
		return
	}
	if len(memberships) == 0 {
		writeError(w, http.StatusForbidden, "no_tenant", "This account is not a member of any company.")
		return
	}

	tenantID, ok := chooseTenant(w, memberships, req.TenantID)
	if !ok {
		return
	}
	s.startSession(w, r, user.ID, tenantID, http.StatusOK)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		if err := s.tenancy.DeleteSession(r.Context(), auth.HashToken(c.Value)); err != nil {
			// Logout must not fail on a database hiccup: the cookie is cleared
			// regardless, so the client is signed out even if the row lingers.
			s.log.Error("delete session", "err", err)
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id, ok := tenancy.IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}
	writeJSON(w, http.StatusOK, meFrom(id))
}

func (s *Server) handleSwitchTenant(w http.ResponseWriter, r *http.Request) {
	var req switchTenantRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id, ok := tenancy.IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}

	// Membership is checked against the identity's own list, then again inside
	// CreateSession, which refuses to mint a session for a non-member tenant.
	if !id.CanActIn(req.TenantID) {
		writeError(w, http.StatusForbidden, "forbidden", "You are not a member of that company.")
		return
	}

	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		if err := s.tenancy.DeleteSession(r.Context(), auth.HashToken(c.Value)); err != nil {
			s.internal(w, "revoke previous session", err)
			return
		}
	}
	s.startSession(w, r, id.UserID, req.TenantID, http.StatusOK)
}

// --- session plumbing ------------------------------------------------------

// startSession mints a session, sets the cookie and returns the resolved
// identity, so every entry point (register, login, switch) answers with the same
// shape and the same guarantees.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID, tenantID string, status int) {
	token, digest, err := auth.NewSessionToken()
	if err != nil {
		s.internal(w, "generate session token", err)
		return
	}
	if _, err := s.tenancy.CreateSession(r.Context(), userID, tenantID, digest, s.sessionTTL); err != nil {
		if errors.Is(err, tenancy.ErrNotFound) {
			writeError(w, http.StatusForbidden, "forbidden", "You are not a member of that company.")
			return
		}
		s.internal(w, "create session", err)
		return
	}
	s.setSessionCookie(w, token)

	// Resolve rather than assemble: the response must describe the same state a
	// subsequent request will see, including the role read from the membership.
	id, _, err := s.tenancy.ResolveSession(r.Context(), digest)
	if err != nil {
		s.internal(w, "resolve new session", err)
		return
	}
	writeJSON(w, status, meFrom(id))
}

// chooseTenant picks the tenant for a login. With one membership it is implied;
// with several the client must choose, and the response lists the options.
func chooseTenant(w http.ResponseWriter, memberships []tenancy.Membership, requested string) (string, bool) {
	if requested != "" {
		for _, m := range memberships {
			if m.TenantID == requested {
				return m.TenantID, true
			}
		}
		writeError(w, http.StatusForbidden, "forbidden", "You are not a member of that company.")
		return "", false
	}
	if len(memberships) == 1 {
		return memberships[0].TenantID, true
	}

	views := make([]tenantView, 0, len(memberships))
	for _, m := range memberships {
		views = append(views, tenantView{ID: m.TenantID, Name: m.TenantName, Slug: m.TenantSlug, Role: m.Role})
	}
	writeJSON(w, http.StatusBadRequest, struct {
		Error apiError     `json:"error"`
		Items []tenantView `json:"tenants"`
	}{
		Error: apiError{Code: "tenant_selection_required", Message: "Choose which company to sign in to."},
		Items: views,
	})
	return "", false
}

func meFrom(id tenancy.Identity) meResponse {
	views := make([]tenantView, 0, len(id.Memberships))
	for _, m := range id.Memberships {
		views = append(views, tenantView{ID: m.TenantID, Name: m.TenantName, Slug: m.TenantSlug, Role: m.Role})
	}
	active := tenantView{ID: id.TenantID, Role: id.Role}
	for _, v := range views {
		if v.ID == id.TenantID {
			active = v
			break
		}
	}
	return meResponse{
		User:    userView{ID: id.UserID, Email: id.Email, Name: id.Name},
		Tenant:  active,
		Tenants: views,
	}
}

func (s *Server) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
}

// --- validation ------------------------------------------------------------

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func validSlug(s string) bool { return slugRe.MatchString(s) }

// validEmail is intentionally permissive: the only authority on whether an
// address works is a delivery attempt, and a strict regex rejects valid
// addresses. It rejects obvious nonsense and bounds length.
func validEmail(s string) bool {
	if len(s) < 3 || len(s) > 254 {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n,;<>\"") {
		return false
	}
	return strings.Contains(s[at+1:], ".")
}

func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
