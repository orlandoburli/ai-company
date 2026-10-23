// Package notification stores the in-app notification centre: the pending
// checkpoints, failed runs and sync errors a user needs to see (FR-NT-1).
//
// A notification belongs to one user of one tenant. It is written by the
// engine (a checkpoint became pending, a run failed) and read by that user
// only — the store takes both the tenant scope and the user id on every call,
// so a notification can never be listed by another member of the tenant.
package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Kind is the notification type. The set is closed: an unknown kind is a
// programming error, not a runtime condition.
type Kind string

const (
	KindCheckpointPending Kind = "checkpoint_pending"
	KindRunFailed         Kind = "run_failed"
	KindRunCompleted      Kind = "run_completed"
	KindSyncError         Kind = "sync_error"
)

// Valid reports whether the kind is one the store accepts.
func (k Kind) Valid() bool {
	switch k {
	case KindCheckpointPending, KindRunFailed, KindRunCompleted, KindSyncError:
		return true
	}
	return false
}

// Entry is one notification.
type Entry struct {
	ID        string
	TenantID  string
	UserID    string
	Kind      Kind
	TitleMD   string
	BodyMD    string
	Link      string
	Payload   json.RawMessage
	ReadAt    *time.Time
	CreatedAt time.Time
}

// New is the input for creating a notification.
type New struct {
	UserID  string
	Kind    Kind
	TitleMD string
	BodyMD  string
	Link    string
	Payload json.RawMessage
}

// Errors returned by the store.
var (
	ErrNotFound    = errors.New("notification not found")
	ErrInvalidKind = errors.New("invalid notification kind")
	ErrInvalidUser = errors.New("invalid notification user")
	ErrAlreadyRead = errors.New("notification already read")
)

// Store persists notifications in Postgres.
type Store struct {
	db *sql.DB
}

// NewStore builds a store over the given database handle.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// newID mints the row id, the same way the other stores do.
func newID() string { return strings.ToUpper(uuid.NewString()) }

const columns = `
	id, tenant_id, user_id, kind, title_md, body_md, link,
	payload_json, read_at, created_at`

// withTenant runs fn inside a transaction with the tenant set, exactly like
// the other stores in this codebase.
func (s *Store) withTenant(ctx context.Context, sc tenancy.Scope, fn func(context.Context, *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Create inserts one notification for one user of the tenant.
func (s *Store) Create(ctx context.Context, sc tenancy.Scope, in New) (Entry, error) {
	if in.UserID == "" {
		return Entry{}, ErrInvalidUser
	}
	if !in.Kind.Valid() {
		return Entry{}, ErrInvalidKind
	}
	payload := in.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}

	var out Entry
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			INSERT INTO notifications (id, tenant_id, user_id, kind, title_md, body_md, link, payload_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING `+columns,
			newID(), sc.TenantID, in.UserID, string(in.Kind), in.TitleMD, in.BodyMD, in.Link, payload,
		).Scan(&out.ID, &out.TenantID, &out.UserID, &out.Kind, &out.TitleMD, &out.BodyMD,
			&out.Link, &out.Payload, &out.ReadAt, &out.CreatedAt)
	})
	if err != nil {
		return Entry{}, fmt.Errorf("create notification: %w", err)
	}
	return out, nil
}

// CreateMany inserts one notification per recipient in a single transaction.
func (s *Store) CreateMany(ctx context.Context, sc tenancy.Scope, in New, userIDs []string) ([]Entry, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	if !in.Kind.Valid() {
		return nil, ErrInvalidKind
	}
	payload := in.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}

	out := make([]Entry, 0, len(userIDs))
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		for _, userID := range userIDs {
			if userID == "" {
				return ErrInvalidUser
			}
			var e Entry
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO notifications (id, tenant_id, user_id, kind, title_md, body_md, link, payload_json)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				RETURNING `+columns,
				newID(), sc.TenantID, userID, string(in.Kind), in.TitleMD, in.BodyMD, in.Link, payload,
			).Scan(&e.ID, &e.TenantID, &e.UserID, &e.Kind, &e.TitleMD, &e.BodyMD,
				&e.Link, &e.Payload, &e.ReadAt, &e.CreatedAt); err != nil {
				return err
			}
			out = append(out, e)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create notifications: %w", err)
	}
	return out, nil
}

// List returns the notifications of one user, newest first. With unreadOnly
// the read ones are filtered out; the limit caps the page (0 means the
// server default of 50).
//
// The read runs tenant-scoped like every other query: the policy fails closed
// when the tenant is not set, so a missing scope would return nothing at all
// rather than another tenant's rows.
func (s *Store) List(ctx context.Context, sc tenancy.Scope, userID string, unreadOnly bool, limit int) ([]Entry, error) {
	if userID == "" {
		return nil, ErrInvalidUser
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + columns + ` FROM notifications n WHERE n.user_id = $1`
	if unreadOnly {
		query += ` AND n.read_at IS NULL`
	}
	query += ` ORDER BY n.created_at DESC, n.id DESC LIMIT $2`

	var out []Entry
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, userID, limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var e Entry
			if err := rows.Scan(&e.ID, &e.TenantID, &e.UserID, &e.Kind, &e.TitleMD, &e.BodyMD,
				&e.Link, &e.Payload, &e.ReadAt, &e.CreatedAt); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	return out, nil
}

// CountUnread returns how many unread notifications the user has — the nav
// badge number.
func (s *Store) CountUnread(ctx context.Context, sc tenancy.Scope, userID string) (int, error) {
	if userID == "" {
		return 0, ErrInvalidUser
	}
	var count int
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`, userID).Scan(&count)
	})
	if err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return count, nil
}

// MarkRead marks one notification as read. It is a compare-and-swap: a
// notification that is already read returns ErrAlreadyRead, so a double click
// cannot pretend to have read it twice.
func (s *Store) MarkRead(ctx context.Context, sc tenancy.Scope, id, userID string) (Entry, error) {
	if userID == "" {
		return Entry{}, ErrInvalidUser
	}
	var out Entry
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			UPDATE notifications SET read_at = now()
			 WHERE id = $1 AND user_id = $2 AND read_at IS NULL
			RETURNING `+columns, id, userID)
		err := row.Scan(&out.ID, &out.TenantID, &out.UserID, &out.Kind, &out.TitleMD, &out.BodyMD,
			&out.Link, &out.Payload, &out.ReadAt, &out.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			// Either it does not exist, it belongs to someone else, or it is
			// already read. Distinguish the last case for the caller.
			var existing Entry
			err := tx.QueryRowContext(ctx, `
				SELECT `+columns+` FROM notifications n WHERE n.id = $1 AND n.user_id = $2`,
				id, userID).Scan(&existing.ID, &existing.TenantID, &existing.UserID, &existing.Kind,
				&existing.TitleMD, &existing.BodyMD, &existing.Link, &existing.Payload, &existing.ReadAt,
				&existing.CreatedAt)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			return ErrAlreadyRead
		}
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	return out, nil
}
