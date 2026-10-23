package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// MaxQueuedPerTenant caps how many queued jobs one tenant may hold at once
// (docs/mvp-contract.md: beyond it the API answers 422 queue_full). Enforcing
// it inside the tenant transaction — after pg_advisory_xact_lock — makes the
// count-then-insert race-safe: without the advisory lock two concurrent
// requests could both count 99 and both insert, ending above the cap. The
// advisory lock is per tenant ("tenant:agent_jobs"), so tenants never
// serialise against each other.
const MaxQueuedPerTenant = 100

// Input and size limits enforced in Go (the schema enforces what it can).
const (
	maxSoulBytes       = 200 * 1024
	maxBodyBytes       = 64 * 1024
	minConcurrency     = 1
	maxConcurrency     = 16
	defaultConcurrency = 2
)

// SQLSTATEs raised by the schema for agent rules.
const codeLogAppendOnly = "AIC05"

// namePattern keeps agent names parseable as @mentions: lowercase alphanumerics
// with dashes or inner underscores, at most 64 characters.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Store is the agent-runtime store over the application connection.
type Store struct {
	db *sql.DB
}

// NewStore builds an agent store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func newID() string { return strings.ToUpper(uuid.NewString()) }

// withTenant runs fn in a transaction scoped to the tenant. Same discipline as
// internal/work: the setting is transaction-local, so a pooled connection
// cannot carry a tenant into the next request.
func (s *Store) withTenant(ctx context.Context, sc tenancy.Scope, fn func(context.Context, *sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx,
		`SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		return fmt.Errorf("set tenant scope: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// mapPGError turns schema errors into typed agent errors.
//   - 23505 → ErrConflict (duplicate name, duplicate thread)
//   - 23503 → ErrNotFound, because the only unmet FKs reference a missing
//     parent in this tenant
//   - 23514 → ErrInvalid (shape/state CHECKs)
//   - AIC05 → the job_logs append-only rule
func mapPGError(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case codeLogAppendOnly:
			return fmt.Errorf("agent: job log is append-only")
		case "23505":
			return fmt.Errorf("%w: %s", ErrConflict, pg.ConstraintName)
		case "23503":
			return fmt.Errorf("%w: referenced row does not exist in this tenant", ErrNotFound)
		case "23514":
			return fmt.Errorf("%w: %s", ErrInvalid, pg.ConstraintName)
		}
	}
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strptr(s string) *string { return &s }

func raw(v json.RawMessage) []byte {
	if len(v) == 0 {
		return nil
	}
	return v
}

// mustJSON marshals a value that cannot fail to marshal.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("agent: marshal: " + err.Error())
	}
	return b
}

// orJSON returns raw when non-empty, otherwise the schema default.
func orJSON(v json.RawMessage, dflt string) []byte {
	if len(v) > 0 {
		return v
	}
	return []byte(dflt)
}

// requireJSONObject keeps opaque JSON opaque: it must parse, and it must be an
// object. The store never interprets what is inside — that is the connector's
// job — but it refuses malformed input at the boundary rather than letting a
// bad row surface later as a corrupt agent.
func requireJSONObject(field string, v json.RawMessage) error {
	if len(v) == 0 {
		return nil
	}
	if !json.Valid(v) {
		return fmt.Errorf("%w: %s is not valid JSON", ErrInvalid, field)
	}
	var obj map[string]any
	if err := json.Unmarshal(v, &obj); err != nil {
		return fmt.Errorf("%w: %s must be a JSON object", ErrInvalid, field)
	}
	return nil
}

func requireJSON(field string, v json.RawMessage) error {
	if len(v) == 0 {
		return nil
	}
	if !json.Valid(v) {
		return fmt.Errorf("%w: %s is not valid JSON", ErrInvalid, field)
	}
	return nil
}

func validName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: name %q must match a-z0-9 with - or _ (max 64)", ErrInvalid, name)
	}
	return nil
}

func validConcurrency(n int) error {
	if n < minConcurrency || n > maxConcurrency {
		return fmt.Errorf("%w: concurrency_limit must be between %d and %d", ErrInvalid, minConcurrency, maxConcurrency)
	}
	return nil
}

func validAgentFields(name string, role Role, kind Kind, soul string, config, capabilities, skillsPolicy, fallbacks json.RawMessage) error {
	if err := validName(name); err != nil {
		return err
	}
	if !role.Valid() {
		return fmt.Errorf("%w: unknown role %q", ErrInvalid, role)
	}
	if !kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, kind)
	}
	if len(soul) > maxSoulBytes {
		return fmt.Errorf("%w: soul_md is over 200 KB", ErrInvalid)
	}
	if err := requireJSONObject("config", config); err != nil {
		return err
	}
	if err := requireJSONObject("capabilities", capabilities); err != nil {
		return err
	}
	if err := requireJSONObject("skills_policy", skillsPolicy); err != nil {
		return err
	}
	if err := requireJSON("fallbacks", fallbacks); err != nil {
		return err
	}
	return nil
}

// --- agents ----------------------------------------------------------------

const agentColumns = `
	a.id, a.tenant_id, a.name, a.role, a.kind, a.config_json, a.capabilities_json,
	a.concurrency_limit, a.enabled, a.soul_md, a.skills_policy_json, a.fallbacks_json,
	a.created_by, a.created_at, a.updated_at`

func scanAgent(r rowScanner) (Agent, error) {
	var (
		a                              Agent
		config, caps, policy, fallback []byte
		createdBy                      sql.NullString
	)
	err := r.Scan(&a.ID, &a.TenantID, &a.Name, &a.Role, &a.Kind, &config, &caps,
		&a.ConcurrencyLimit, &a.Enabled, &a.SoulMD, &policy, &fallback,
		&createdBy, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return Agent{}, err
	}
	a.Config = json.RawMessage(config)
	a.Capabilities = json.RawMessage(caps)
	a.SkillsPolicy = json.RawMessage(policy)
	a.Fallbacks = json.RawMessage(fallback)
	if createdBy.Valid {
		a.CreatedBy = &createdBy.String
	}
	return a, nil
}

// CreateAgent adds an agent to the tenant. ConcurrencyLimit defaults to 2 when
// unset; Config/Capabilities/SoulMD/Enabled are honoured as given.
func (s *Store) CreateAgent(ctx context.Context, sc tenancy.Scope, in Agent) (Agent, error) {
	if err := validAgentFields(in.Name, in.Role, in.Kind, in.SoulMD, in.Config, in.Capabilities, in.SkillsPolicy, in.Fallbacks); err != nil {
		return Agent{}, err
	}
	limit := in.ConcurrencyLimit
	if limit == 0 {
		limit = defaultConcurrency
	}
	if err := validConcurrency(limit); err != nil {
		return Agent{}, err
	}

	var a Agent
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		created, err := scanAgent(tx.QueryRowContext(ctx, `
			INSERT INTO agents AS a (id, tenant_id, name, role, kind, config_json,
			                capabilities_json, concurrency_limit, enabled, soul_md,
			                skills_policy_json, fallbacks_json, created_by)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10,
			        $11::jsonb, $12::jsonb, nullif($13, ''))
			RETURNING`+agentColumns,
			newID(), sc.TenantID, in.Name, in.Role, in.Kind,
			orJSON(in.Config, "{}"), orJSON(in.Capabilities, "{}"),
			limit, in.Enabled, in.SoulMD,
			orJSON(in.SkillsPolicy, "{}"), orJSON(in.Fallbacks, "[]"),
			deref(in.CreatedBy)))
		if err != nil {
			return err
		}
		a = created
		return nil
	})
	if err != nil {
		return Agent{}, mapPGError(err)
	}
	return a, nil
}

// ListAgents returns the tenant's agents, ordered by name.
func (s *Store) ListAgents(ctx context.Context, sc tenancy.Scope) ([]Agent, error) {
	out := []Agent{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT`+agentColumns+`
			  FROM agents a
			 ORDER BY a.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAgent(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// GetAgent returns one agent by id.
func (s *Store) GetAgent(ctx context.Context, sc tenancy.Scope, id string) (Agent, error) {
	var a Agent
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		a, err = scanAgent(tx.QueryRowContext(ctx, `
			SELECT`+agentColumns+`
			  FROM agents a
			 WHERE a.id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return a, err
}

// GetAgentByName resolves an @mention; the lookup is case-insensitive.
func (s *Store) GetAgentByName(ctx context.Context, sc tenancy.Scope, name string) (Agent, error) {
	var a Agent
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		a, err = scanAgent(tx.QueryRowContext(ctx, `
			SELECT`+agentColumns+`
			  FROM agents a
			 WHERE lower(a.name) = lower($1)`, name))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return a, err
}

// AgentPatch is a partial update; a nil field is unchanged. SoulMD is a text
// field like ItemPatch's strings: an empty string clears the soul. The JSON
// fields are pointers so "not sent" and "{}" stay distinct.
type AgentPatch struct {
	Name             *string
	Role             *Role
	Kind             *Kind
	Config           *json.RawMessage
	Capabilities     *json.RawMessage
	ConcurrencyLimit *int
	Enabled          *bool
	SoulMD           *string
	SkillsPolicy     *json.RawMessage
	Fallbacks        *json.RawMessage
}

func validAgentJSONPatch(name string, v *json.RawMessage, object bool) error {
	if v == nil {
		return nil
	}
	if object {
		return requireJSONObject(name, *v)
	}
	return requireJSON(name, *v)
}

// UpdateAgent applies a patch to one agent and returns the updated row.
func (s *Store) UpdateAgent(ctx context.Context, sc tenancy.Scope, id string, p AgentPatch, actor string) (Agent, error) {
	if err := validName(deref(p.Name)); p.Name != nil && err != nil {
		return Agent{}, err
	}
	if p.Role != nil && !p.Role.Valid() {
		return Agent{}, fmt.Errorf("%w: unknown role %q", ErrInvalid, *p.Role)
	}
	if p.Kind != nil && !p.Kind.Valid() {
		return Agent{}, fmt.Errorf("%w: unknown kind %q", ErrInvalid, *p.Kind)
	}
	if p.SoulMD != nil && len(*p.SoulMD) > maxSoulBytes {
		return Agent{}, fmt.Errorf("%w: soul_md is over 200 KB", ErrInvalid)
	}
	if p.ConcurrencyLimit != nil {
		if err := validConcurrency(*p.ConcurrencyLimit); err != nil {
			return Agent{}, err
		}
	}
	if err := validAgentJSONPatch("config", p.Config, true); err != nil {
		return Agent{}, err
	}
	if err := validAgentJSONPatch("capabilities", p.Capabilities, true); err != nil {
		return Agent{}, err
	}
	if err := validAgentJSONPatch("skills_policy", p.SkillsPolicy, true); err != nil {
		return Agent{}, err
	}
	if err := validAgentJSONPatch("fallbacks", p.Fallbacks, false); err != nil {
		return Agent{}, err
	}

	var out Agent
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		sets := []string{"updated_at = now()"}
		args := []any{id}
		add := func(expr string, val any) {
			args = append(args, val)
			sets = append(sets, fmt.Sprintf(expr, len(args)))
		}
		if p.Name != nil {
			add("name = $%d", *p.Name)
		}
		if p.Role != nil {
			add("role = $%d", *p.Role)
		}
		if p.Kind != nil {
			add("kind = $%d", *p.Kind)
		}
		if p.Config != nil {
			add("config_json = COALESCE($%d::jsonb, '{}'::jsonb)", raw(*p.Config))
		}
		if p.Capabilities != nil {
			add("capabilities_json = COALESCE($%d::jsonb, '{}'::jsonb)", raw(*p.Capabilities))
		}
		if p.ConcurrencyLimit != nil {
			add("concurrency_limit = $%d", *p.ConcurrencyLimit)
		}
		if p.Enabled != nil {
			add("enabled = $%d", *p.Enabled)
		}
		if p.SoulMD != nil {
			add("soul_md = $%d", *p.SoulMD) // empty string clears the soul
		}
		if p.SkillsPolicy != nil {
			add("skills_policy_json = COALESCE($%d::jsonb, '{}'::jsonb)", raw(*p.SkillsPolicy))
		}
		if p.Fallbacks != nil {
			add("fallbacks_json = COALESCE($%d::jsonb, '[]'::jsonb)", raw(*p.Fallbacks))
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE agents SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		updated, err := scanAgent(tx.QueryRowContext(ctx, `
			SELECT`+agentColumns+`
			  FROM agents a
			 WHERE a.id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return Agent{}, mapPGError(err)
	}
	return out, nil
}

// DeleteAgent removes an agent. Jobs RESTRICT the delete, so an agent with
// execution history cannot be removed — mapped to ErrConflict with a clear
// reason instead of the raw FK error.
func (s *Store) DeleteAgent(ctx context.Context, sc tenancy.Scope, id string) error {
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var jobs, messages int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM agent_jobs WHERE agent_id = $1`, id).Scan(&jobs); err != nil {
			return err
		}
		if jobs > 0 {
			return fmt.Errorf("%w: agent still has %d job(s)", ErrConflict, jobs)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM messages WHERE author_agent_id = $1 AND author_kind = 'agent'`, id).Scan(&messages); err != nil {
			return err
		}
		if messages > 0 {
			return fmt.Errorf("%w: agent still has %d message(s) in chat", ErrConflict, messages)
		}
		tag, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id = $1`, id)
		if err != nil {
			return err
		}
		n, err := tag.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	return mapPGError(err)
}

// --- threads ---------------------------------------------------------------

const threadColumns = `
	t.id, t.tenant_id, t.context_kind, t.item_id, t.product_id, t.run_id, t.title,
	t.created_by, t.created_at`

func scanThread(r rowScanner) (Thread, error) {
	var (
		t         Thread
		createdBy sql.NullString
	)
	err := r.Scan(&t.ID, &t.TenantID, &t.ContextKind, &t.ItemID, &t.ProductID, &t.RunID, &t.Title,
		&createdBy, &t.CreatedAt)
	if err != nil {
		return Thread{}, err
	}
	if createdBy.Valid {
		t.CreatedBy = &createdBy.String
	}
	return t, nil
}

// CreateThread adds a thread bound to an item, a product, or nothing (free).
func (s *Store) CreateThread(ctx context.Context, sc tenancy.Scope, kind, itemID, productID, title, createdBy string) (Thread, error) {
	switch kind {
	case ContextItem:
		if itemID == "" || productID != "" {
			return Thread{}, fmt.Errorf("%w: item thread needs an item and no product", ErrInvalid)
		}
	case ContextProduct:
		if productID == "" || itemID != "" {
			return Thread{}, fmt.Errorf("%w: product thread needs a product and no item", ErrInvalid)
		}
	case ContextFree:
		if itemID != "" || productID != "" {
			return Thread{}, fmt.Errorf("%w: free thread needs no context", ErrInvalid)
		}
	default:
		return Thread{}, fmt.Errorf("%w: context kind %q", ErrInvalid, kind)
	}

	var th Thread
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		created, err := scanThread(tx.QueryRowContext(ctx, `
			INSERT INTO threads AS t (id, tenant_id, context_kind, item_id, product_id, title, created_by)
			VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, nullif($7, ''))
			RETURNING`+threadColumns,
			newID(), sc.TenantID, kind, itemID, productID, title, createdBy))
		if err != nil {
			return err
		}
		th = created
		return nil
	})
	if err != nil {
		return Thread{}, mapPGError(err)
	}
	return th, nil
}

// EnsureItemThread returns the item's one thread, creating it on first use.
// The partial unique index on (tenant_id, item_id) makes this get-or-create
// idempotent; the item is verified first so a missing item is ErrNotFound
// rather than an FK surprise.
func (s *Store) EnsureItemThread(ctx context.Context, sc tenancy.Scope, itemID, createdBy string) (Thread, error) {
	var th Thread
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM items WHERE id = $1)`, itemID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}

		if err := tx.QueryRowContext(ctx, `
			SELECT`+threadColumns+`
			  FROM threads t
			 WHERE t.context_kind = 'item' AND t.item_id = $1`, itemID).
			Scan(&th.ID, &th.TenantID, &th.ContextKind, &th.ItemID, &th.ProductID, &th.RunID, &th.Title,
				&th.CreatedBy, &th.CreatedAt); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		// Get-or-create. A concurrent EnsureItemThread may win the insert; the
		// partial unique index turns that into a no-op and the re-select below
		// returns the winner's row.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO threads (id, tenant_id, context_kind, item_id, title, created_by)
			VALUES ($1, $2, 'item', $3, '', nullif($4, ''))
			ON CONFLICT (tenant_id, item_id) WHERE context_kind = 'item'
			DO NOTHING`, newID(), sc.TenantID, itemID, createdBy); err != nil {
			return err
		}
		t, err := scanThread(tx.QueryRowContext(ctx, `
			SELECT`+threadColumns+`
			  FROM threads t
			 WHERE t.context_kind = 'item' AND t.item_id = $1`, itemID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		th = t
		return nil
	})
	if err != nil {
		return Thread{}, mapPGError(err)
	}
	return th, nil
}

// EnsureRunThread returns the run's one thread, creating it on first use.
// The partial unique index on (tenant_id, run_id) makes the get-or-create
// idempotent: a concurrent creation loses the race and the re-select below
// returns the winner's row.
func (s *Store) EnsureRunThread(ctx context.Context, sc tenancy.Scope, runID, title, createdBy string) (Thread, error) {
	if runID == "" {
		return Thread{}, fmt.Errorf("%w: run thread needs a run", ErrInvalid)
	}
	var th Thread
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`SELECT`+threadColumns+`
			  FROM threads t
			 WHERE t.context_kind = 'run' AND t.run_id = $1`, runID).
			Scan(&th.ID, &th.TenantID, &th.ContextKind, &th.ItemID, &th.ProductID, &th.RunID, &th.Title,
				&th.CreatedBy, &th.CreatedAt); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO threads (id, tenant_id, context_kind, run_id, title, created_by)
			VALUES ($1, $2, 'run', $3, $4, nullif($5, ''))
			ON CONFLICT (tenant_id, run_id) WHERE context_kind = 'run'
			DO NOTHING`, newID(), sc.TenantID, runID, title, createdBy); err != nil {
			return err
		}
		t, err := scanThread(tx.QueryRowContext(ctx, `
			SELECT`+threadColumns+`
			  FROM threads t
			 WHERE t.context_kind = 'run' AND t.run_id = $1`, runID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		th = t
		return nil
	})
	if err != nil {
		return Thread{}, mapPGError(err)
	}
	return th, nil
}

// GetThread returns one thread by id.
func (s *Store) GetThread(ctx context.Context, sc tenancy.Scope, id string) (Thread, error) {
	var th Thread
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		th, err = scanThread(tx.QueryRowContext(ctx, `
			SELECT`+threadColumns+`
			  FROM threads t
			 WHERE t.id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return th, err
}

// --- messages --------------------------------------------------------------

const messageColumns = `
	m.id, m.seq, m.tenant_id, m.thread_id, m.author_kind, m.author_user_id,
	m.author_agent_id, m.body_md, m.artifacts_json, m.created_at`

func scanMessage(r rowScanner) (Message, error) {
	var (
		m         Message
		artifacts []byte
		bodyUser  sql.NullString
		bodyAgent sql.NullString
	)
	err := r.Scan(&m.ID, &m.Seq, &m.TenantID, &m.ThreadID, &m.AuthorKind, &bodyUser,
		&bodyAgent, &m.BodyMD, &artifacts, &m.CreatedAt)
	if err != nil {
		return Message{}, err
	}
	m.Artifacts = json.RawMessage(artifacts)
	if bodyUser.Valid {
		m.AuthorUserID = &bodyUser.String
	}
	if bodyAgent.Valid {
		m.AuthorAgentID = &bodyAgent.String
	}
	return m, nil
}

// NewMessage is the input for AddMessage. Exactly one author identity matches
// AuthorKind: human needs AuthorUserID, agent needs AuthorAgentID, system
// needs neither.
type NewMessage struct {
	ThreadID      string
	AuthorKind    string
	AuthorUserID  string
	AuthorAgentID string
	BodyMD        string
	Artifacts     json.RawMessage
}

func validAuthor(kind, userID, agentID string) error {
	switch kind {
	case AuthorHuman:
		if userID == "" || agentID != "" {
			return fmt.Errorf("%w: human message needs author_user_id and no agent", ErrInvalid)
		}
	case AuthorAgent:
		if agentID == "" || userID != "" {
			return fmt.Errorf("%w: agent message needs author_agent_id and no user", ErrInvalid)
		}
	case AuthorSystem:
		if userID != "" || agentID != "" {
			return fmt.Errorf("%w: system message has no author", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: author kind %q", ErrInvalid, kind)
	}
	return nil
}

// AddMessage appends one message to a thread and returns the stored row,
// including the global seq cursor.
func (s *Store) AddMessage(ctx context.Context, sc tenancy.Scope, in NewMessage) (Message, error) {
	if err := validAuthor(in.AuthorKind, in.AuthorUserID, in.AuthorAgentID); err != nil {
		return Message{}, err
	}
	n := len(in.BodyMD)
	if n == 0 {
		return Message{}, fmt.Errorf("%w: body_md is empty", ErrInvalid)
	}
	if n > maxBodyBytes {
		return Message{}, fmt.Errorf("%w: body_md is over 64 KB", ErrInvalid)
	}

	var m Message
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		created, err := insertMessage(ctx, tx, sc, in)
		if err != nil {
			return err
		}
		m = created
		return nil
	})
	if err != nil {
		return Message{}, mapPGError(err)
	}
	return m, nil
}

// insertMessage writes one message inside an existing transaction.
func insertMessage(ctx context.Context, tx *sql.Tx, sc tenancy.Scope, in NewMessage) (Message, error) {
	return scanMessage(tx.QueryRowContext(ctx, `
		INSERT INTO messages AS m (id, tenant_id, thread_id, author_kind,
		                          author_user_id, author_agent_id, body_md, artifacts_json)
		VALUES ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), $7, $8::jsonb)
		RETURNING`+messageColumns,
		newID(), sc.TenantID, in.ThreadID, in.AuthorKind,
		in.AuthorUserID, in.AuthorAgentID, in.BodyMD,
		orJSON(in.Artifacts, "[]")))
}

// ListMessages returns a thread's timeline ascending by seq, starting after
// afterSeq (0 = the beginning). limit defaults to 100 and is capped at 500.
func (s *Store) ListMessages(ctx context.Context, sc tenancy.Scope, threadID string, afterSeq int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	out := []Message{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT`+messageColumns+`
			  FROM messages m
			 WHERE m.thread_id = $1 AND m.seq > $2
			 ORDER BY m.seq
			 LIMIT $3`, threadID, afterSeq, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMessage(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// --- jobs ------------------------------------------------------------------

const jobColumns = `
	j.id, j.tenant_id, j.agent_id, j.thread_id, j.message_id, j.item_id,
	j.step_run_id, j.idempotency_key,
	j.state, j.attempt, j.max_attempts, j.prompt, j.context_json, j.workdir,
	j.tokens_in, j.tokens_out, j.cost_usd, j.result_json, j.failure_kind,
	j.error_text, j.created_by, j.created_at, j.started_at, j.finished_at`

func scanJob(r rowScanner) (Job, error) {
	var (
		j              Job
		context        []byte
		result         []byte
		threadID       sql.NullString
		messageID      sql.NullString
		itemID         sql.NullString
		stepRunID      sql.NullString
		idempotencyKey sql.NullString
		workdir        sql.NullString
		cost           sql.NullFloat64
		failureKind    sql.NullString
		errText        sql.NullString
		createdBy      sql.NullString
		startedAt      sql.NullTime
		finishedAt     sql.NullTime
	)
	err := r.Scan(&j.ID, &j.TenantID, &j.AgentID, &threadID, &messageID, &itemID, &stepRunID, &idempotencyKey,
		&j.State, &j.Attempt, &j.MaxAttempts, &j.Prompt, &context, &workdir,
		&j.TokensIn, &j.TokensOut, &cost, &result, &failureKind, &errText,
		&createdBy, &j.CreatedAt, &startedAt, &finishedAt)
	if err != nil {
		return Job{}, err
	}
	j.Context = json.RawMessage(context)
	j.Result = json.RawMessage(result)
	if threadID.Valid {
		j.ThreadID = &threadID.String
	}
	if messageID.Valid {
		j.MessageID = &messageID.String
	}
	if itemID.Valid {
		j.ItemID = &itemID.String
	}
	if stepRunID.Valid {
		j.StepRunID = &stepRunID.String
	}
	if idempotencyKey.Valid {
		j.IdempotencyKey = &idempotencyKey.String
	}
	if workdir.Valid {
		j.Workdir = &workdir.String
	}
	if cost.Valid {
		c := cost.Float64
		j.CostUSD = &c
	}
	if failureKind.Valid {
		j.FailureKind = &failureKind.String
	}
	if errText.Valid {
		j.ErrorText = &errText.String
	}
	if createdBy.Valid {
		j.CreatedBy = &createdBy.String
	}
	if startedAt.Valid {
		t := startedAt.Time
		j.StartedAt = &t
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		j.FinishedAt = &t
	}
	return j, nil
}

// NewJob is the input for CreateJob. ThreadID/MessageID/ItemID are all
// optional: a job may belong to a chat thread, an item, or neither (a bare
// run,FR-AG-4).
type NewJob struct {
	AgentID        string
	ThreadID       string
	MessageID      string
	ItemID         string
	StepRunID      string
	IdempotencyKey string
	Prompt         string
	Context        json.RawMessage
	// Workdir is the directory the adapter runs in — the node's worktree
	// when the node has one. Empty means the adapter picks a throwaway
	// directory of its own.
	Workdir   string
	CreatedBy string
}

// CreateJob queues one adapter execution.
//
// The queue cap (MaxQueuedPerTenant) is enforced under
// pg_advisory_xact_lock(hashtext(tenant || ':agent_jobs')): a count in one
// transaction is not otherwise protected against a concurrent insert in
// another, and the per-tenant advisory lock makes count+insert serialise
// without blocking anyone outside the tenant. Jobs leaving the queue (claims,
// finishes) need no protection — removals can only lower the count.
func (s *Store) CreateJob(ctx context.Context, sc tenancy.Scope, in NewJob) (Job, error) {
	if in.AgentID == "" {
		return Job{}, fmt.Errorf("%w: agent_id is required", ErrInvalid)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return Job{}, fmt.Errorf("%w: prompt is empty", ErrInvalid)
	}
	if err := requireJSONObject("context", in.Context); err != nil {
		return Job{}, err
	}

	var j Job
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if in.IdempotencyKey != "" {
			existing, err := scanJob(tx.QueryRowContext(ctx, `SELECT`+jobColumns+` FROM agent_jobs j WHERE j.idempotency_key=$1`, in.IdempotencyKey))
			if err == nil {
				j = existing
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var enabled bool
		err := tx.QueryRowContext(ctx,
			`SELECT enabled FROM agents WHERE id = $1`, in.AgentID).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: agent not found", ErrNotFound)
		}
		if err != nil {
			return err
		}
		if !enabled {
			return ErrDisabled
		}

		if _, err := tx.ExecContext(ctx,
			`SELECT pg_advisory_xact_lock(hashtext($1 || ':agent_jobs'))`, sc.TenantID); err != nil {
			return err
		}
		var queued int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM agent_jobs WHERE state = 'queued'`).Scan(&queued); err != nil {
			return err
		}
		if queued >= MaxQueuedPerTenant {
			return fmt.Errorf("%w: %d queued jobs", ErrQueueFull, queued)
		}

		created, err := scanJob(tx.QueryRowContext(ctx, `
			INSERT INTO agent_jobs AS j (id, tenant_id, agent_id, thread_id, message_id,
			                    item_id, step_run_id, idempotency_key, prompt, context_json, workdir, created_by)
			VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), nullif($6, ''), nullif($7, ''), nullif($8, ''),
			        $9, $10::jsonb, nullif($11, ''), nullif($12, ''))
			ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
			RETURNING`+jobColumns,
			newID(), sc.TenantID, in.AgentID, in.ThreadID, in.MessageID, in.ItemID, in.StepRunID, in.IdempotencyKey,
			in.Prompt, orJSON(in.Context, "{}"), in.Workdir, in.CreatedBy))
		if errors.Is(err, sql.ErrNoRows) && in.IdempotencyKey != "" {
			// A replay attaches to the same job row instead of enqueuing a
			// second one. When that job is still queued it has not run yet,
			// so the fresh prompt and context replace the old ones — that is
			// how a steered execution carries its instruction.
			if _, updErr := tx.ExecContext(ctx, `
				UPDATE agent_jobs
				   SET prompt = $2,
				       context_json = $3::jsonb,
				       state = CASE WHEN state = 'cancelled' THEN 'queued' ELSE state END,
				       next_attempt_at = CASE WHEN state = 'cancelled' THEN now() ELSE next_attempt_at END,
				       started_at = CASE WHEN state = 'cancelled' THEN NULL ELSE started_at END,
				       finished_at = CASE WHEN state = 'cancelled' THEN NULL ELSE finished_at END,
				       error_text = CASE WHEN state = 'cancelled' THEN NULL ELSE error_text END,
				       failure_kind = CASE WHEN state = 'cancelled' THEN NULL ELSE failure_kind END
				 WHERE idempotency_key = $1 AND state IN ('queued', 'cancelled')`,
				in.IdempotencyKey, in.Prompt, orJSON(in.Context, "{}")); updErr != nil {
				return updErr
			}
			created, err = scanJob(tx.QueryRowContext(ctx, `SELECT`+jobColumns+` FROM agent_jobs j WHERE j.idempotency_key=$1`, in.IdempotencyKey))
		}
		if err != nil {
			return err
		}
		j = created
		return nil
	})
	if err != nil {
		return Job{}, mapPGError(err)
	}
	return j, nil
}

// GetJobByIdempotencyKey returns the existing tenant-owned job for a replay key.
func (s *Store) GetJobByIdempotencyKey(ctx context.Context, sc tenancy.Scope, key string) (Job, error) {
	var j Job
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		j, err = scanJob(tx.QueryRowContext(ctx, `SELECT`+jobColumns+` FROM agent_jobs j WHERE j.idempotency_key=$1`, key))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return j, err
}

// RunningAgentJobs counts the jobs an agent is running right now in this
// tenant. The dispatcher uses it to respect the agent's concurrency limit
// before it starts a run.
func (s *Store) RunningAgentJobs(ctx context.Context, sc tenancy.Scope, agentID string) (int, error) {
	if agentID == "" {
		return 0, nil
	}
	var count int
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT count(*) FROM agent_jobs WHERE agent_id = $1 AND state = 'running'`, agentID).Scan(&count)
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetJob returns one job by id.
func (s *Store) GetJob(ctx context.Context, sc tenancy.Scope, id string) (Job, error) {
	var j Job
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		j, err = scanJob(tx.QueryRowContext(ctx, `
			SELECT`+jobColumns+`
			  FROM agent_jobs j
			 WHERE j.id = $1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return j, err
}

// ListJobsByThread returns a thread's jobs, newest first.
func (s *Store) ListJobsByThread(ctx context.Context, sc tenancy.Scope, threadID string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	out := []Job{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT`+jobColumns+`
			  FROM agent_jobs j
			 WHERE j.thread_id = $1
			 ORDER BY j.created_at DESC, j.id DESC
			 LIMIT $2`, threadID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

// ClaimNextJob crosses tenants: the worker has no session, so it cannot be
// scoped. It calls worker_claim_job on the pool connection — the SECURITY
// DEFINER function is the only surface that selects past RLS, and it returns
// job coordinates only. ErrNotFound when nothing is claimable.
func (s *Store) ClaimNextJob(ctx context.Context, workerID string, lease time.Duration, skipAgents []string) (tenantID, jobID string, err error) {
	if strings.TrimSpace(workerID) == "" {
		return "", "", fmt.Errorf("%w: worker id is empty", ErrInvalid)
	}
	seconds := int(math.Ceil(lease.Seconds()))
	if seconds <= 0 {
		return "", "", fmt.Errorf("%w: lease must be positive", ErrInvalid)
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT tenant_id, job_id FROM worker_claim_job($1, $2, $3)`,
		workerID, seconds, skipAgents).Scan(&tenantID, &jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return tenantID, jobID, err
}

// HeartbeatJob extends the lease of a job this worker still holds. Zero rows
// means the job is not running or was claimed by someone else (after a
// requeue): ErrNotRunning.
func (s *Store) HeartbeatJob(ctx context.Context, sc tenancy.Scope, jobID, workerID string, lease time.Duration) error {
	seconds := int(math.Ceil(lease.Seconds()))
	if seconds <= 0 {
		return fmt.Errorf("%w: lease must be positive", ErrInvalid)
	}
	return s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		tag, err := tx.ExecContext(ctx, `
			UPDATE agent_jobs
			   SET heartbeat_at = now(),
			       claim_expires_at = now() + make_interval(secs => $2::int)
			 WHERE id = $1 AND state = 'running' AND claim_lock = $3`,
			jobID, seconds, workerID)
		if err != nil {
			return err
		}
		n, err := tag.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotRunning
		}
		return nil
	})
}

// ResultJSON is the shape stored when an adapter returned no structured data
// itself: the output text is preserved, the tokens are already columns.
func ResultJSON(res *Result) []byte {
	if res == nil {
		return nil
	}
	if len(res.Data) > 0 {
		return res.Data
	}
	return mustJSON(res)
}

func tokenCount(res *Result, in bool) int {
	if res == nil {
		return 0
	}
	if in {
		return res.TokensIn
	}
	return res.TokensOut
}

func costPtr(res *Result) *float64 {
	if res == nil {
		return nil
	}
	return res.CostUSD
}

// FinishJob moves a claimed running job to a terminal state and records the
// result, then clears the claim fields. Only the worker holding the claim may
// finish: zero rows (expired lease → requeued, or cancelled) is ErrNotRunning,
// so a cancelled job is never resurrected and a requeued one cannot be
// finished by the stale worker.
func (s *Store) FinishJob(ctx context.Context, sc tenancy.Scope, jobID, workerID string, state JobState, res *Result, failureKind, errText string) error {
	if !state.Terminal() {
		return fmt.Errorf("%w: finish requires a terminal state, got %q", ErrInvalid, state)
	}
	return s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		tag, err := tx.ExecContext(ctx, `
			UPDATE agent_jobs
			   SET state = $2,
			       tokens_in = $3,
			       tokens_out = $4,
			       cost_usd = $5,
			       result_json = $6::jsonb,
			       failure_kind = nullif($7, ''),
			       error_text = nullif($8, ''),
			       finished_at = now(),
			       next_attempt_at = NULL,
			       claim_lock = NULL,
			       claim_expires_at = NULL,
			       heartbeat_at = NULL
			 WHERE id = $1 AND state = 'running' AND claim_lock = $9`,
			jobID, state, tokenCount(res, true), tokenCount(res, false),
			costPtr(res), raw(ResultJSON(res)), failureKind, errText, workerID)
		if err != nil {
			return err
		}
		n, err := tag.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotRunning
		}
		return nil
	})
}

// FinishJobWithMessage transitions the job to a terminal state and appends the
// thread message in the SAME transaction.
//
// The worker posts the agent's reply (or the failure notice) together with the
// terminal state, so an observer that sees "succeeded" or "failed" always
// finds the explanation next to it, and a job that was cancelled meanwhile
// never gets a reply: the message insert is rolled back with the job update.
func (s *Store) FinishJobWithMessage(ctx context.Context, sc tenancy.Scope, jobID, workerID string, state JobState, res *Result, msg *NewMessage, failureKind, errText string) error {
	if !state.Terminal() {
		return fmt.Errorf("%w: finish requires a terminal state, got %q", ErrInvalid, state)
	}
	if msg != nil {
		if err := validAuthor(msg.AuthorKind, msg.AuthorUserID, msg.AuthorAgentID); err != nil {
			return err
		}
		if len(msg.BodyMD) == 0 {
			return fmt.Errorf("%w: body_md is empty", ErrInvalid)
		}
		if len(msg.BodyMD) > maxBodyBytes {
			return fmt.Errorf("%w: body_md is over 64 KB", ErrInvalid)
		}
	}
	return s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		if msg != nil {
			if _, err := insertMessage(ctx, tx, sc, *msg); err != nil {
				return err
			}
		}
		tag, err := tx.ExecContext(ctx, `
			UPDATE agent_jobs
			   SET state = $2,
			       tokens_in = $3,
			       tokens_out = $4,
			       cost_usd = $5,
			       result_json = $6::jsonb,
			       failure_kind = nullif($7, ''),
			       error_text = nullif($8, ''),
			       finished_at = now(),
			       next_attempt_at = NULL,
			       claim_lock = NULL,
			       claim_expires_at = NULL,
			       heartbeat_at = NULL
			 WHERE id = $1 AND state = 'running' AND claim_lock = $9`,
			jobID, state, tokenCount(res, true), tokenCount(res, false),
			costPtr(res), raw(ResultJSON(res)), failureKind, errText, workerID)
		if err != nil {
			return err
		}
		n, err := tag.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotRunning
		}
		return nil
	})
}

// RestartCancelledJob puts a cancelled job back in the queue so it runs
// again. It is the stop-and-resume half of steering a running execution: the
// adapter has no live input, so the run is stopped and the job is re-queued
// with the same idempotency key. The attempt counter is kept — the history of
// what happened stays visible.
func (s *Store) RestartCancelledJob(ctx context.Context, sc tenancy.Scope, jobID string) (Job, error) {
	var out Job
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanJob(tx.QueryRowContext(ctx, `
			UPDATE agent_jobs AS j
			   SET state = 'queued',
			       next_attempt_at = now(),
			       started_at = NULL,
			       finished_at = NULL,
			       error_text = NULL,
			       failure_kind = NULL,
			       claim_lock = NULL,
			       claim_expires_at = NULL,
			       heartbeat_at = NULL
			 WHERE id = $1 AND state = 'cancelled'
			RETURNING `+jobColumns, jobID))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotRunning
		}
		return e
	})
	if err != nil {
		return Job{}, mapPGError(err)
	}
	return out, nil
}

// RequeueJob returns a claimed job to the queue after a future delay. If the
// job has spent its attempts the worker calls FinishJob(failed) instead; this
// method does not police that, it only performs the requeue of a job the
// worker actually holds.
func (s *Store) RequeueJob(ctx context.Context, sc tenancy.Scope, jobID, workerID string, delay time.Duration, failureKind, errText string) error {
	seconds := int(math.Ceil(delay.Seconds()))
	if seconds < 0 {
		seconds = 0
	}
	return s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		tag, err := tx.ExecContext(ctx, `
			UPDATE agent_jobs
			   SET state = 'queued',
			       next_attempt_at = now() + make_interval(secs => $2::int),
			       started_at = NULL,
			       claim_lock = NULL,
			       claim_expires_at = NULL,
			       heartbeat_at = NULL
			 WHERE id = $1 AND state = 'running' AND claim_lock = $3`,
			jobID, seconds, workerID)
		if err != nil {
			return err
		}
		n, err := tag.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotRunning
		}
		return nil
	})
}

// CancelJob cancels a queued or running job. Already-terminal jobs refuse with
// ErrNotRunning. A running job keeps its worker's tick race-safe because the
// transition narrows on the previous state, so FinishJob from the worker finds
// no running row and gives up.
func (s *Store) CancelJob(ctx context.Context, sc tenancy.Scope, id string) (Job, error) {
	var j Job
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		updated, err := scanJob(tx.QueryRowContext(ctx, `
			UPDATE agent_jobs AS j
			   SET state = 'cancelled',
			       finished_at = now(),
			       next_attempt_at = NULL,
			       claim_lock = NULL,
			       claim_expires_at = NULL,
			       heartbeat_at = NULL
			 WHERE j.id = $1 AND j.state IN ('queued', 'running')
			RETURNING`+jobColumns, id))
		if err == nil {
			j = updated
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := scanJob(tx.QueryRowContext(ctx, `
			SELECT`+jobColumns+`
			  FROM agent_jobs j
			 WHERE j.id = $1`, id)); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return ErrNotRunning
	})
	if err != nil {
		return Job{}, mapPGError(err)
	}
	return j, nil
}

// ReapJobs requeues or fails every running job whose claim lease expired,
// across tenants (same definer surface as ClaimNextJob). It returns the number
// of jobs reaped.
func (s *Store) ReapJobs(ctx context.Context, maxAttempts int) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tenant_id, job_id, state FROM worker_reap_jobs($1)`, maxAttempts)
	if err != nil {
		return 0, mapPGError(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(new(string), new(string), new(string)); err != nil {
			return count, err
		}
	}
	return count, rows.Err()
}

// --- job logs --------------------------------------------------------------

func scanLog(r rowScanner) (LogEntry, error) {
	var (
		e        LogEntry
		data     []byte
		tenantID string
	)
	err := r.Scan(&e.Seq, &tenantID, &e.JobID, &e.Stream, &e.Level, &e.Kind,
		&e.Message, &data, &e.CreatedAt)
	if err != nil {
		return LogEntry{}, err
	}
	e.TenantID = tenantID
	e.Data = json.RawMessage(data)
	return e, nil
}

// AppendLog persists one adapter event. Empty stream/level/kind default to
// engine/info/output_chunk.
func (s *Store) AppendLog(ctx context.Context, sc tenancy.Scope, jobID string, ev Event) (LogEntry, error) {
	stream := ev.Stream
	if stream == "" {
		stream = "engine"
	}
	level := ev.Level
	if level == "" {
		level = "info"
	}
	kind := ev.Kind
	if kind == "" {
		kind = "output_chunk"
	}

	var e LogEntry
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		inserted, err := scanLog(tx.QueryRowContext(ctx, `
			INSERT INTO job_logs (tenant_id, job_id, stream, level, kind, message, data_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
			RETURNING seq, tenant_id, job_id, stream, level, kind, message, data_json, created_at`,
			sc.TenantID, jobID, stream, level, kind, ev.Message, raw(ev.Data)))
		if err != nil {
			return err
		}
		e = inserted
		return nil
	})
	if err != nil {
		return LogEntry{}, mapPGError(err)
	}
	return e, nil
}

// ListLogs returns a job's log ascending by seq, starting after afterSeq
// (0 = the beginning). limit defaults to 100 and is capped at 500.
func (s *Store) ListLogs(ctx context.Context, sc tenancy.Scope, jobID string, afterSeq int64, limit int) ([]LogEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	out := []LogEntry{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT seq, tenant_id, job_id, stream, level, kind, message, data_json, created_at
			  FROM job_logs
			 WHERE job_id = $1 AND seq > $2
			 ORDER BY seq
			 LIMIT $3`, jobID, afterSeq, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanLog(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}
