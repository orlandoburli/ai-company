// Package api is the HTTP surface. Everything here is tenant-scoped: a request
// either resolves to a session with an active tenant, or it is rejected before
// any handler runs.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/orlandoburli/ai-company/internal/auth"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

// SessionCookie carries the opaque session token. It is HttpOnly so script
// cannot read it, and SameSite=Lax so a cross-site form post cannot ride it.
const SessionCookie = "aic_session"

// Server holds the dependencies of the HTTP surface.
type Server struct {
	db           *sql.DB
	tenancy      *tenancy.Store
	work         *work.Store
	log          *slog.Logger
	secureCookie bool
	sessionTTL   time.Duration
}

// Option configures a Server.
type Option func(*Server)

// WithSecureCookies sets the Secure attribute on the session cookie. It must be
// on whenever the app is served over HTTPS; it is off by default only so that
// local http:// development works.
func WithSecureCookies(v bool) Option { return func(s *Server) { s.secureCookie = v } }

// WithSessionTTL sets how long a session stays valid.
func WithSessionTTL(d time.Duration) Option { return func(s *Server) { s.sessionTTL = d } }

// New builds a server. A nil logger falls back to slog.Default.
func New(db *sql.DB, log *slog.Logger, opts ...Option) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		db:         db,
		tenancy:    tenancy.NewStore(db),
		work:       work.NewStore(db),
		log:        log,
		sessionTTL: 30 * 24 * time.Hour,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Handler returns the routed, middleware-wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n") //nolint:errcheck
	})

	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.Handle("GET /api/auth/me", s.authenticated(s.handleMe))
	mux.Handle("POST /api/auth/switch-tenant", s.authenticated(s.handleSwitchTenant))
	mux.HandleFunc("GET /api/auth/invite", s.handlePeekInvite)
	mux.HandleFunc("POST /api/auth/accept-invite", s.handleAcceptInvite)

	mux.Handle("GET /api/tenant", s.authenticated(s.handleGetTenant))
	mux.Handle("PATCH /api/tenant",
		s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleUpdateTenant)))
	mux.Handle("GET /api/tenant/members", s.authenticated(s.handleListMembers))
	mux.Handle("GET /api/invites", s.authenticated(s.handleListInvites))
	mux.Handle("POST /api/invites",
		s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleCreateInvite)))
	mux.Handle("DELETE /api/invites/{id}",
		s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleRevokeInvite)))
	mux.Handle("PATCH /api/tenant/members/{userID}",
		s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleSetMemberRole)))

	// Work management. Reads are open to every member (guests included);
	// writes are gated on CanWriteWork, which excludes guests.
	mux.Handle("GET /api/products", s.authenticated(s.handleListProducts))
	mux.Handle("POST /api/products",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleCreateProduct)))
	mux.Handle("PATCH /api/products/{id}",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handlePatchProduct)))
	mux.Handle("DELETE /api/products/{id}",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleDeleteProduct)))

	mux.Handle("GET /api/projects", s.authenticated(s.handleListProjects))
	mux.Handle("POST /api/projects",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleCreateProject)))
	mux.Handle("PATCH /api/projects/{id}",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handlePatchProject)))
	mux.Handle("DELETE /api/projects/{id}",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleDeleteProject)))
	mux.Handle("GET /api/tree", s.authenticated(s.handleTree))
	mux.Handle("POST /api/items/bulk-status",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleBulkStatus)))

	mux.Handle("GET /api/items", s.authenticated(s.handleListItems))
	mux.Handle("POST /api/items",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleCreateItem)))
	mux.Handle("GET /api/items/{id}", s.authenticated(s.handleGetItem))
	mux.Handle("PATCH /api/items/{id}",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handlePatchItem)))
	mux.Handle("DELETE /api/items/{id}",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleDeleteItem)))
	mux.Handle("GET /api/items/{id}/dependencies", s.authenticated(s.handleListDependencies))
	mux.Handle("POST /api/items/{id}/dependencies",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleCreateDependency)))
	mux.Handle("DELETE /api/items/{id}/dependencies/{depID}",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleDeleteDependency)))
	mux.Handle("GET /api/items/{id}/activity", s.authenticated(s.handleItemActivity))
	mux.Handle("GET /api/items/{id}/score", s.authenticated(s.handleItemScore))
	mux.Handle("POST /api/items/{id}/pause",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handlePauseItem)))
	mux.Handle("POST /api/items/{id}/unpause",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleUnpauseItem)))
	mux.Handle("POST /api/items/{id}/pin",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handlePinItem)))
	mux.Handle("POST /api/items/{id}/unpin",
		s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleUnpinItem)))
	mux.Handle("GET /api/queue", s.authenticated(s.handleQueue))

	mux.Handle("GET /api/board", s.authenticated(s.handleBoard))

	return s.recoverer(s.accessLog(mux))
}

// authenticated resolves the session cookie into an Identity and a Scope, and
// refuses the request otherwise. Handlers downstream can rely on both being
// present, so no content handler needs its own auth check.
func (s *Server) authenticated(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil || strings.TrimSpace(c.Value) == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}

		digest := auth.HashToken(c.Value)
		id, _, err := s.tenancy.ResolveSession(r.Context(), digest)
		if err != nil {
			// Expired, revoked, or forged — indistinguishable to the client, and
			// the cookie is cleared so the browser stops presenting it.
			if !errors.Is(err, tenancy.ErrNotFound) {
				s.log.Error("resolve session", "err", err)
			}
			s.clearSessionCookie(w)
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}

		ctx := tenancy.WithIdentity(r.Context(), id)
		ctx = tenancy.WithScope(ctx, id.Scope())
		ctx = tenancy.WithSessionHash(ctx, digest)
		next(w, r.WithContext(ctx))
	})
}

// requireRole wraps a handler with a role capability check. Authorisation lives
// here rather than in each handler so a missing check is visible in the route
// table.
func (s *Server) requireRole(can func(tenancy.Role) bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := tenancy.IdentityFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		if !can(id.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
			return
		}
		next(w, r)
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.sessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// --- middleware ------------------------------------------------------------

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "panic", fmt.Sprint(rec), "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

// --- JSON helpers ----------------------------------------------------------

const maxBodyBytes = 64 << 10

// decodeJSON reads a bounded, strict JSON body. Unknown fields are rejected so a
// typo in a client payload is a 400 rather than a silently ignored security
// field.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "The request body is not valid JSON.")
		return false
	}
	// A second value in the stream means the client sent something unintended.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "The request body must contain one JSON object.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if body != nil {
		json.NewEncoder(w).Encode(body) //nolint:errcheck
	}
}

type errorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: apiError{Code: code, Message: message}})
}

// isUniqueViolation reports whether err is a Postgres unique-constraint error,
// which is how a duplicate email or tenant slug surfaces.
func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
