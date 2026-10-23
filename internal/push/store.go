// Package push stores browser subscriptions and delivers Web Push notifications.
package push

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Subscription is one browser's push subscription.
type Subscription struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	Endpoint  string    `json:"endpoint"`
	P256DH    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	ErrNotFound = errors.New("push subscription not found")
	ErrInvalid  = errors.New("invalid push subscription")
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) withTenant(ctx context.Context, sc tenancy.Scope, fn func(*sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, sc.TenantID); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Subscribe inserts or updates the row for this endpoint, allowing a browser to resubscribe.
func (s *Store) Subscribe(ctx context.Context, sc tenancy.Scope, in Subscription) (Subscription, error) {
	if strings.TrimSpace(in.UserID) == "" || strings.TrimSpace(in.Endpoint) == "" || strings.TrimSpace(in.P256DH) == "" || strings.TrimSpace(in.Auth) == "" || strings.ContainsRune(in.Endpoint, 0) {
		return Subscription{}, ErrInvalid
	}
	var out Subscription
	err := s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `INSERT INTO push_subscriptions (id,tenant_id,user_id,endpoint,p256dh,auth)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (endpoint) DO UPDATE SET tenant_id=EXCLUDED.tenant_id,
			user_id=EXCLUDED.user_id,p256dh=EXCLUDED.p256dh,auth=EXCLUDED.auth,created_at=now()
			RETURNING id,tenant_id,user_id,endpoint,p256dh,auth,created_at`, uuid.NewString(), sc.TenantID, in.UserID, in.Endpoint, in.P256DH, in.Auth).
			Scan(&out.ID, &out.TenantID, &out.UserID, &out.Endpoint, &out.P256DH, &out.Auth, &out.CreatedAt)
	})
	if err != nil {
		return Subscription{}, fmt.Errorf("subscribe push endpoint: %w", err)
	}
	return out, nil
}

func (s *Store) Unsubscribe(ctx context.Context, sc tenancy.Scope, id string) error {
	err := s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id=$1 AND user_id=$2`, id, sc.UserID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return ErrNotFound
		}
		return err
	})
	return err
}

func (s *Store) List(ctx context.Context, sc tenancy.Scope, userID string) ([]Subscription, error) {
	return s.list(ctx, sc, `SELECT id,tenant_id,user_id,endpoint,p256dh,auth,created_at FROM push_subscriptions WHERE user_id=$1 ORDER BY created_at`, userID)
}

func (s *Store) ListAll(ctx context.Context, sc tenancy.Scope) ([]Subscription, error) {
	return s.list(ctx, sc, `SELECT id,tenant_id,user_id,endpoint,p256dh,auth,created_at FROM push_subscriptions ORDER BY created_at`)
}

func (s *Store) list(ctx context.Context, sc tenancy.Scope, query string, args ...any) ([]Subscription, error) {
	out := []Subscription{}
	err := s.withTenant(ctx, sc, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x Subscription
			if err := rows.Scan(&x.ID, &x.TenantID, &x.UserID, &x.Endpoint, &x.P256DH, &x.Auth, &x.CreatedAt); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}
