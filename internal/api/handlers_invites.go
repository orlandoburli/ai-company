package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/auth"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Invitations (FR-TN-7).
//
// There is no email delivery in the MVP, so creation returns the one-time token
// to the inviter, who passes the link on. That is a deliberate, stated gap: the
// token is single-use, expiring, revocable and stored only as a digest, so the
// security properties hold even without a mailer.

const inviteTTL = 7 * 24 * time.Hour

type createInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type acceptInviteRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req createInviteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	hash, ok := tenancy.SessionHashFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}

	email := normalizeEmail(req.Email)
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "invalid_email", "Enter a valid email address.")
		return
	}

	role := tenancy.Role(strings.ToLower(strings.TrimSpace(req.Role)))
	if req.Role == "" {
		role = tenancy.RoleMember
	}
	// An invitation can never create an owner; that is a database CHECK as well.
	if role != tenancy.RoleAdmin && role != tenancy.RoleMember && role != tenancy.RoleGuest {
		writeError(w, http.StatusBadRequest, "invalid_role", "Role must be admin, member or guest.")
		return
	}

	token, digest, err := auth.NewSessionToken()
	if err != nil {
		s.internal(w, "generate invite token", err)
		return
	}

	inv, err := s.tenancy.CreateInvite(r.Context(), hash, email, role, digest,
		time.Now().Add(inviteTTL))
	switch {
	case err == nil:
	case errors.Is(err, tenancy.ErrAlreadyMember):
		writeError(w, http.StatusConflict, "already_member", "That person is already a member.")
		return
	case errors.Is(err, tenancy.ErrAlreadyInvited):
		writeError(w, http.StatusConflict, "already_invited", "There is already a pending invitation for that email.")
		return
	case errors.Is(err, tenancy.ErrForbiddenRole):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow inviting people.")
		return
	case errors.Is(err, tenancy.ErrInvalidRole):
		writeError(w, http.StatusBadRequest, "invalid_role", "Role must be admin, member or guest.")
		return
	default:
		s.internal(w, "create invite", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"invite": inv,
		// Returned exactly once; only the digest is stored.
		"token":      token,
		"accept_url": "/accept-invite?token=" + token,
	})
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	hash, ok := tenancy.SessionHashFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}
	invites, err := s.tenancy.ListInvites(r.Context(), hash)
	if err != nil {
		s.internal(w, "list invites", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	hash, ok := tenancy.SessionHashFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
		return
	}
	err := s.tenancy.RevokeInvite(r.Context(), hash, r.PathValue("id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, tenancy.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "That invitation is no longer pending.")
	case errors.Is(err, tenancy.ErrForbiddenRole):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow revoking invitations.")
	default:
		s.internal(w, "revoke invite", err)
	}
}

// handlePeekInvite is unauthenticated: the accept page needs the tenant name and
// role before the person has an account.
func (s *Server) handlePeekInvite(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		writeError(w, http.StatusBadRequest, "invalid_invite", "This invitation link is incomplete.")
		return
	}
	tenantName, email, role, expiresAt, err := s.tenancy.PeekInvite(r.Context(), auth.HashToken(token))
	if errors.Is(err, tenancy.ErrNotFound) {
		writeError(w, http.StatusNotFound, "invalid_invite", "This invitation is no longer valid.")
		return
	}
	if err != nil {
		s.internal(w, "peek invite", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_name": tenantName,
		"email":       email,
		"role":        role,
		"expires_at":  expiresAt,
	})
}

// handleAcceptInvite consumes the token, creates the account when the email is
// new, and signs the person in to the tenant they were invited to.
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req acceptInviteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeError(w, http.StatusBadRequest, "invalid_invite", "This invitation link is incomplete.")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_name", "Enter your name.")
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

	acc, err := s.tenancy.AcceptInvite(r.Context(), auth.HashToken(token), hash, name)
	if errors.Is(err, tenancy.ErrNotFound) {
		// Unknown, expired, revoked and already-used are deliberately
		// indistinguishable.
		writeError(w, http.StatusBadRequest, "invalid_invite", "This invitation is no longer valid.")
		return
	}
	if err != nil {
		s.internal(w, "accept invite", err)
		return
	}

	s.startSession(w, r, acc.UserID, acc.TenantID, http.StatusCreated)
}
