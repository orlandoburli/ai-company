package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type credential struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Kind      string    `json:"kind"`
	SecretRef string    `json:"secret_ref"`
	Label     string    `json:"label"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type createCredentialRequest struct {
	Kind      string `json:"kind"`
	SecretRef string `json:"secret_ref"`
	Label     string `json:"label"`
}

// registerIntegrationRoutes is intentionally separate so the server/router
// owner can choose where to mount this tenant-scoped registry.
func (s *Server) registerIntegrationRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/integrations", s.authenticated(s.handleListIntegrations))
	mux.Handle("POST /api/integrations", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleCreateIntegration)))
	mux.Handle("DELETE /api/integrations/{id}", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleDeleteIntegration)))
}

func (s *Server) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	rows, err := s.integrationQuery(r.Context(), sc, `SELECT id,tenant_id,kind,secret_ref,label,created_by,created_at FROM integration_credentials ORDER BY kind`, nil)
	if err != nil {
		s.integrationDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"integrations": rows})
}

func (s *Server) handleCreateIntegration(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req createCredentialRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Kind, req.SecretRef, req.Label = strings.TrimSpace(req.Kind), strings.TrimSpace(req.SecretRef), strings.TrimSpace(req.Label)
	switch req.Kind {
	case "github", "jira", "anthropic", "openai":
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", "kind must be github, jira, anthropic, or openai.")
		return
	}
	if req.SecretRef == "" || strings.ContainsRune(req.SecretRef, '\x00') {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", "secret_ref is required.")
		return
	}
	var out credential
	err := s.integrationTx(r.Context(), sc, func(ctx context.Context, tx *sql.Tx) error {
		var by sql.NullString
		err := tx.QueryRowContext(ctx, `INSERT INTO integration_credentials (id,tenant_id,kind,secret_ref,label,created_by)
			VALUES ($1,$2,$3,$4,$5,nullif($6,''))
			ON CONFLICT (tenant_id,kind) DO UPDATE SET secret_ref=EXCLUDED.secret_ref,label=EXCLUDED.label,created_by=EXCLUDED.created_by
			RETURNING id,tenant_id,kind,secret_ref,label,created_by,created_at`, uuid.NewString(), sc.TenantID, req.Kind, req.SecretRef, req.Label, sc.UserID).
			Scan(&out.ID, &out.TenantID, &out.Kind, &out.SecretRef, &out.Label, &by, &out.CreatedAt)
		if by.Valid {
			out.CreatedBy = by.String
		}
		return err
	})
	if err != nil {
		s.integrationDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"integration": out})
}

func (s *Server) handleDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	err := s.integrationTx(r.Context(), sc, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM integration_credentials WHERE id=$1`, r.PathValue("id"))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return sql.ErrNoRows
		}
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "integration_not_found", "That integration does not exist in this tenant.")
		return
	}
	if err != nil {
		s.integrationDBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) integrationQuery(ctx context.Context, sc tenancy.Scope, query string, args []any) ([]credential, error) {
	out := []credential{}
	err := s.integrationTx(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item credential
			var by sql.NullString
			if err := rows.Scan(&item.ID, &item.TenantID, &item.Kind, &item.SecretRef, &item.Label, &by, &item.CreatedAt); err != nil {
				return err
			}
			if by.Valid {
				item.CreatedBy = by.String
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Server) integrationTx(ctx context.Context, sc tenancy.Scope, fn func(context.Context, *sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, sc.TenantID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) integrationDBError(w http.ResponseWriter, err error) {
	if errors.Is(err, tenancy.ErrForbidden) {
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
		return
	}
	s.log.Error("integration credential handler", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
}
