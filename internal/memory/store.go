package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func newID() string { return strings.ToUpper(uuid.NewString()) }

// withTenant scopes every transaction to the tenant using transaction-local RLS.
func (s *Store) withTenant(ctx context.Context, sc tenancy.Scope, fn func(context.Context, *sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		return fmt.Errorf("set tenant scope: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func validScope(scope Scope) error {
	switch scope.Kind {
	case ScopeRun, ScopeItem, ScopeAgent, ScopeWorkspace:
		if strings.TrimSpace(scope.Ref) == "" {
			return fmt.Errorf("%w: empty ref", ErrInvalidScope)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidScope, scope.Kind)
	}
	return nil
}

func validWrite(w Write) error {
	if err := validScope(w.Scope); err != nil {
		return err
	}
	if strings.TrimSpace(w.Key) == "" {
		return ErrInvalidKey
	}
	switch w.Source {
	case SourceStepOutput, SourceCheckpoint, SourceIntervention, SourceAgentMarker, SourceIntegration, SourceHuman:
	default:
		return fmt.Errorf("memory: invalid source %q", w.Source)
	}
	if len(w.ContentJSON) > 0 && !json.Valid(w.ContentJSON) {
		return fmt.Errorf("memory: invalid content JSON")
	}
	return nil
}

const entryColumns = `id, tenant_id, scope_kind, scope_ref, key, revision,
	content_md, content_json, source, source_run_id, source_step_run_id,
	source_user_id, tags_json, token_estimate, seq, superseded_by, created_at`

type rowScanner interface{ Scan(...any) error }

func scanEntry(row rowScanner) (Entry, error) {
	var e Entry
	var kind string
	var contentJSON, tagsJSON []byte
	var runID, stepID, userID, superseded sql.NullString
	if err := row.Scan(&e.ID, &e.TenantID, &kind, &e.Scope.Ref, &e.Key, &e.Revision,
		&e.ContentMD, &contentJSON, &e.Source, &runID, &stepID, &userID,
		&tagsJSON, &e.TokenEstimate, &e.Seq, &superseded, &e.CreatedAt); err != nil {
		return Entry{}, err
	}
	e.Scope.Kind = ScopeKind(kind)
	e.ContentJSON = json.RawMessage(contentJSON)
	if len(tagsJSON) > 0 {
		if err := json.Unmarshal(tagsJSON, &e.Tags); err != nil {
			return Entry{}, err
		}
	}
	if e.Tags == nil {
		e.Tags = []string{}
	}
	e.SourceRunID = runID.String
	e.SourceStepRunID = stepID.String
	e.SourceUserID = userID.String
	e.SupersededBy = superseded.String
	return e, nil
}

func tokenEstimate(content string) int {
	n := len([]rune(content))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

func scopeLockKey(tenant string, scope Scope) string {
	return tenant + ":memory:" + string(scope.Kind) + ":" + scope.Ref
}

func lockScope(ctx context.Context, tx *sql.Tx, tenant string, scope Scope) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, scopeLockKey(tenant, scope))
	return err
}

// Write appends an engine-authored revision. Human-facing callers should use WriteHuman.
func (s *Store) Write(ctx context.Context, sc tenancy.Scope, w Write) (Entry, error) {
	entries, err := s.writeBatch(ctx, sc, []Write{w})
	if err != nil {
		return Entry{}, err
	}
	return entries[0], nil
}

// WriteHuman appends a human-attributed entry and refuses run-scoped memory.
func (s *Store) WriteHuman(ctx context.Context, sc tenancy.Scope, w Write) (Entry, error) {
	if w.Scope.Kind == ScopeRun {
		return Entry{}, ErrRunScopeReadonly
	}
	w.Source = SourceHuman
	w.SourceUserID = sc.UserID
	return s.Write(ctx, sc, w)
}

// WriteBatch appends all writes in one transaction; each entry receives its own
// revision and per-scope sequence number.
func (s *Store) WriteBatch(ctx context.Context, sc tenancy.Scope, ws []Write) ([]Entry, error) {
	return s.writeBatch(ctx, sc, ws)
}

func (s *Store) writeBatch(ctx context.Context, sc tenancy.Scope, ws []Write) ([]Entry, error) {
	for _, w := range ws {
		if err := validWrite(w); err != nil {
			return nil, err
		}
	}
	out := make([]Entry, 0, len(ws))
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		// Lock scopes in a stable order to avoid deadlocks between multi-scope batches.
		unique := make(map[Scope]struct{})
		scopes := make([]Scope, 0, len(ws))
		for _, w := range ws {
			if _, ok := unique[w.Scope]; !ok {
				unique[w.Scope] = struct{}{}
				scopes = append(scopes, w.Scope)
			}
		}
		sortScopes(scopes)
		for _, scope := range scopes {
			if err := lockScope(ctx, tx, sc.TenantID, scope); err != nil {
				return err
			}
		}
		for _, w := range ws {
			var seq int64
			if err := tx.QueryRowContext(ctx, `
				SELECT COALESCE(MAX(seq), 0) + 1 FROM memory_entries
				 WHERE scope_kind = $1 AND scope_ref = $2`, w.Scope.Kind, w.Scope.Ref).Scan(&seq); err != nil {
				return err
			}
			var revision int
			if err := tx.QueryRowContext(ctx, `
				SELECT COALESCE(MAX(revision), 0) + 1 FROM memory_entries
				 WHERE scope_kind = $1 AND scope_ref = $2 AND key = $3`,
				w.Scope.Kind, w.Scope.Ref, w.Key).Scan(&revision); err != nil {
				return err
			}
			var priorID string
			if revision > 1 {
				if err := tx.QueryRowContext(ctx, `
					SELECT id FROM memory_entries
					 WHERE scope_kind = $1 AND scope_ref = $2 AND key = $3 AND revision = $4`,
					w.Scope.Kind, w.Scope.Ref, w.Key, revision-1).Scan(&priorID); err != nil {
					return err
				}
			}
			id := newID()
			tags, err := json.Marshal(w.Tags)
			if err != nil {
				return err
			}
			created, err := scanEntry(tx.QueryRowContext(ctx, `
				INSERT INTO memory_entries (id, tenant_id, scope_kind, scope_ref, key, revision,
					content_md, content_json, source, source_run_id, source_step_run_id,
					source_user_id, tags_json, token_estimate, seq)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, nullif($10, ''),
					nullif($11, ''), nullif($12, ''), $13::jsonb, $14, $15)
				RETURNING `+entryColumns,
				id, sc.TenantID, w.Scope.Kind, w.Scope.Ref, w.Key, revision,
				w.ContentMD, nullableJSON(w.ContentJSON), w.Source, w.SourceRunID,
				w.SourceStepRunID, w.SourceUserID, tags, tokenEstimate(w.ContentMD), seq))
			if err != nil {
				return err
			}
			if priorID != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE memory_entries SET superseded_by = $2 WHERE id = $1`, priorID, id); err != nil {
					return err
				}
			}
			if err := recordActivity(ctx, tx, sc, created, w); err != nil {
				return err
			}
			out = append(out, created)
		}
		return nil
	})
	return out, err
}

func nullableJSON(v json.RawMessage) any {
	if len(v) == 0 {
		return nil
	}
	return []byte(v)
}

func sortScopes(scopes []Scope) {
	for i := 1; i < len(scopes); i++ {
		for j := i; j > 0 && (scopes[j].Kind < scopes[j-1].Kind ||
			(scopes[j].Kind == scopes[j-1].Kind && scopes[j].Ref < scopes[j-1].Ref)); j-- {
			scopes[j], scopes[j-1] = scopes[j-1], scopes[j]
		}
	}
}

func recordActivity(ctx context.Context, tx *sql.Tx, sc tenancy.Scope, e Entry, w Write) error {
	actorKind := "system"
	actorUser := ""
	if w.SourceUserID != "" {
		actorKind, actorUser = "user", w.SourceUserID
	}
	if w.Source == SourceAgentMarker && actorUser == "" {
		actorKind = "agent"
	}
	after, err := json.Marshal(map[string]any{
		"memory_entry_id": e.ID, "scope_kind": e.Scope.Kind, "scope_ref": e.Scope.Ref,
		"key": e.Key, "revision": e.Revision, "source": e.Source,
	})
	if err != nil {
		return err
	}
	itemID := ""
	if e.Scope.Kind == ScopeItem {
		itemID = e.Scope.Ref
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO activity_log (id, tenant_id, item_id, actor_kind, actor_user_id, action, after_json)
		VALUES ($1, $2, nullif($3, ''), $4, nullif($5, ''), 'memory.write', $6::jsonb)`,
		newID(), sc.TenantID, itemID, actorKind, actorUser, after)
	return err
}

func readRows(ctx context.Context, tx *sql.Tx, scope Scope, current bool, watermark *int64, key string) ([]Entry, error) {
	query := `SELECT ` + entryColumns + ` FROM memory_entries WHERE scope_kind = $1 AND scope_ref = $2`
	args := []any{scope.Kind, scope.Ref}
	if current {
		query += ` AND superseded_by IS NULL`
	}
	if watermark != nil {
		args = append(args, *watermark)
		query += fmt.Sprintf(" AND seq <= $%d", len(args))
	}
	if key != "" {
		args = append(args, key)
		query += fmt.Sprintf(" AND key = $%d", len(args))
	}
	query += ` ORDER BY seq ASC`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Read(ctx context.Context, sc tenancy.Scope, scope Scope) ([]Entry, int64, error) {
	if err := validScope(scope); err != nil {
		return nil, 0, err
	}
	var out []Entry
	var watermark int64
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM memory_entries WHERE scope_kind = $1 AND scope_ref = $2`, scope.Kind, scope.Ref).Scan(&watermark); err != nil {
			return err
		}
		var err error
		out, err = readRows(ctx, tx, scope, true, nil, "")
		return err
	})
	return out, watermark, err
}

func (s *Store) ReadAll(ctx context.Context, sc tenancy.Scope, scopes []Scope) (map[ScopeKind][]Entry, error) {
	out := make(map[ScopeKind][]Entry, len(scopes))
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		for _, scope := range scopes {
			if err := validScope(scope); err != nil {
				return err
			}
			entries, err := readRows(ctx, tx, scope, true, nil, "")
			if err != nil {
				return err
			}
			out[scope.Kind] = append(out[scope.Kind], entries...)
		}
		return nil
	})
	return out, err
}

func (s *Store) ReadAt(ctx context.Context, sc tenancy.Scope, scope Scope, watermark int64) ([]Entry, error) {
	if err := validScope(scope); err != nil {
		return nil, err
	}
	var out []Entry
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = readRows(ctx, tx, scope, false, &watermark, "")
		return err
	})
	return out, err
}

func (s *Store) History(ctx context.Context, sc tenancy.Scope, scope Scope, key string) ([]Entry, error) {
	if err := validScope(scope); err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, ErrInvalidKey
	}
	var out []Entry
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		entries, err := readRows(ctx, tx, scope, false, nil, key)
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
		out = entries
		return err
	})
	return out, err
}

func (s *Store) Get(ctx context.Context, sc tenancy.Scope, id string) (Entry, error) {
	var e Entry
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		e, err = scanEntry(tx.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM memory_entries WHERE id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return e, err
}

// DeleteScope is run cleanup. The latest current row is used as the terminal
// supersession target, leaving all rows available to history queries.
func (s *Store) DeleteScope(ctx context.Context, sc tenancy.Scope, scope Scope) error {
	if err := validScope(scope); err != nil {
		return err
	}
	if scope.Kind != ScopeRun {
		return fmt.Errorf("%w: cleanup is only allowed for run scope", ErrInvalidScope)
	}
	return s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if err := lockScope(ctx, tx, sc.TenantID, scope); err != nil {
			return err
		}
		var terminal string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM memory_entries WHERE scope_kind = $1 AND scope_ref = $2 AND superseded_by IS NULL ORDER BY seq DESC LIMIT 1`, scope.Kind, scope.Ref).Scan(&terminal); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE memory_entries SET superseded_by = $3 WHERE scope_kind = $1 AND scope_ref = $2 AND superseded_by IS NULL`, scope.Kind, scope.Ref, terminal)
		return err
	})
}
