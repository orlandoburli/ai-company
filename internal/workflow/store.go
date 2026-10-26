package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

var (
	ErrNotFound          = errors.New("workflow: not found")
	ErrAlreadyDecided    = errors.New("workflow: checkpoint already decided")
	ErrTemplatePublished = errors.New("workflow: template published")
	ErrTemplateDisabled  = errors.New("workflow: template disabled")
	ErrTemplateInUse     = errors.New("workflow: template in use")
	ErrTerminal          = errors.New("workflow: terminal")
	ErrClaimLost         = errors.New("workflow: claim lost")
	ErrInvalidTransition = errors.New("workflow: invalid transition")
	ErrDuplicateStep     = errors.New("workflow: duplicate step")
	ErrInvalidArtifact   = errors.New("workflow: invalid artifact")
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }
func newID() string              { return strings.ToUpper(uuid.NewString()) }

// withTenant keeps the tenant binding transaction-local, matching the agent store.
func (s *Store) withTenant(ctx context.Context, sc tenancy.Scope, fn func(context.Context, *sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		return fmt.Errorf("set tenant scope: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "AIC07":
			return ErrTemplatePublished
		case "23503":
			return fmt.Errorf("%w: referenced row does not exist in this tenant", ErrNotFound)
		case "23505":
			if strings.Contains(pg.ConstraintName, "step_runs_run_id_node_id_iteration_path_run_index") {
				return fmt.Errorf("%w: %s", ErrDuplicateStep, pg.ConstraintName)
			}
		}
	}
	return err
}

type rowScanner interface{ Scan(...any) error }

func nullableTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
func nullString(s sql.NullString) string {
	if s.Valid {
		return s.String
	}
	return ""
}

// Templates
type TemplateRow struct {
	ID, TenantID, Name string
	Version            int
	AppliesTo          string
	GraphJSON          json.RawMessage
	MaxConcurrency     int
	CreatedBy          string
	PublishedAt        *time.Time
	DisabledAt         *time.Time
	CreatedAt          time.Time
}

// TemplateDisabledError marks a retired template as an invalid run configuration.
type TemplateDisabledError struct{}

func (TemplateDisabledError) Error() string {
	return ErrTemplateDisabled.Error() + ": retired templates cannot start runs"
}
func (TemplateDisabledError) Unwrap() error            { return ErrTemplateDisabled }
func (TemplateDisabledError) FailureKind() FailureKind { return FailureCapability }

type NewTemplate struct {
	Name           string
	AppliesTo      string
	Graph          Template
	MaxConcurrency int
	CreatedBy      string
}
type TemplatePatch struct {
	Name           *string
	AppliesTo      *string
	Graph          *Template
	MaxConcurrency *int
}

const templateCols = `id, tenant_id, name, version, applies_to, graph_json, max_concurrency, created_by, published_at, disabled_at, created_at`

func scanTemplate(r rowScanner) (TemplateRow, error) {
	var x TemplateRow
	var raw []byte
	var by sql.NullString
	var pub, disabled sql.NullTime
	err := r.Scan(&x.ID, &x.TenantID, &x.Name, &x.Version, &x.AppliesTo, &raw, &x.MaxConcurrency, &by, &pub, &disabled, &x.CreatedAt)
	if err != nil {
		return x, err
	}
	x.GraphJSON = raw
	x.CreatedBy = nullString(by)
	x.PublishedAt = nullableTime(pub)
	x.DisabledAt = nullableTime(disabled)
	return x, nil
}
func (s *Store) CreateTemplate(ctx context.Context, sc tenancy.Scope, in NewTemplate) (TemplateRow, error) {
	if err := in.Graph.Validate(); err != nil {
		return TemplateRow{}, err
	}
	b, err := json.Marshal(in.Graph)
	if err != nil {
		return TemplateRow{}, err
	}
	if in.MaxConcurrency == 0 {
		in.MaxConcurrency = 4
	}
	var out TemplateRow
	err = s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanTemplate(tx.QueryRowContext(ctx, `INSERT INTO workflow_templates (id,tenant_id,name,applies_to,graph_json,max_concurrency,created_by) VALUES ($1,$2,$3,$4,$5::jsonb,$6,nullif($7,'')) RETURNING `+templateCols, newID(), sc.TenantID, in.Name, in.AppliesTo, b, in.MaxConcurrency, in.CreatedBy))
		return e
	})
	if err != nil {
		return TemplateRow{}, mapError(err)
	}
	return out, nil
}
func (s *Store) GetTemplate(ctx context.Context, sc tenancy.Scope, id string) (TemplateRow, error) {
	var out TemplateRow
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanTemplate(tx.QueryRowContext(ctx, `SELECT `+templateCols+` FROM workflow_templates WHERE id=$1`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	return out, err
}
func (s *Store) UpdateTemplate(ctx context.Context, sc tenancy.Scope, id string, p TemplatePatch) (TemplateRow, error) {
	var out TemplateRow
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		old, e := scanTemplate(tx.QueryRowContext(ctx, `SELECT `+templateCols+` FROM workflow_templates WHERE id=$1 FOR UPDATE`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if old.PublishedAt != nil {
			return ErrTemplatePublished
		}
		if old.DisabledAt != nil {
			return ErrTemplateDisabled
		}
		if p.Graph != nil {
			if e = p.Graph.Validate(); e != nil {
				return e
			}
		}
		name, applies, graph, conc := old.Name, old.AppliesTo, old.GraphJSON, old.MaxConcurrency
		if p.Name != nil {
			name = *p.Name
		}
		if p.AppliesTo != nil {
			applies = *p.AppliesTo
		}
		if p.Graph != nil {
			graph, e = json.Marshal(p.Graph)
			if e != nil {
				return e
			}
		}
		if p.MaxConcurrency != nil {
			conc = *p.MaxConcurrency
		}
		out, e = scanTemplate(tx.QueryRowContext(ctx, `UPDATE workflow_templates SET name=$2, applies_to=$3, graph_json=$4::jsonb, max_concurrency=$5 WHERE id=$1 RETURNING `+templateCols, id, name, applies, graph, conc))
		return e
	})
	if err != nil {
		return TemplateRow{}, mapError(err)
	}
	return out, nil
}
func (s *Store) PublishTemplate(ctx context.Context, sc tenancy.Scope, id string) (TemplateRow, error) {
	var out TemplateRow
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanTemplate(tx.QueryRowContext(ctx, `UPDATE workflow_templates SET published_at=now() WHERE id=$1 AND published_at IS NULL AND disabled_at IS NULL RETURNING `+templateCols, id))
		if errors.Is(e, sql.ErrNoRows) {
			out, e = scanTemplate(tx.QueryRowContext(ctx, `SELECT `+templateCols+` FROM workflow_templates WHERE id=$1`, id))
		}
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e == nil && out.DisabledAt != nil {
			return ErrTemplateDisabled
		}
		return e
	})
	if err != nil {
		return TemplateRow{}, mapError(err)
	}
	return out, nil
}
func (s *Store) NewTemplateVersion(ctx context.Context, sc tenancy.Scope, id string) (TemplateRow, error) {
	var out TemplateRow
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		src, e := scanTemplate(tx.QueryRowContext(ctx, `SELECT `+templateCols+` FROM workflow_templates WHERE id=$1`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		out, e = scanTemplate(tx.QueryRowContext(ctx, `INSERT INTO workflow_templates (id,tenant_id,name,version,applies_to,graph_json,max_concurrency,created_by) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,nullif($8,'')) RETURNING `+templateCols, newID(), sc.TenantID, src.Name, src.Version+1, src.AppliesTo, src.GraphJSON, src.MaxConcurrency, src.CreatedBy))
		return e
	})
	if err != nil {
		return TemplateRow{}, mapError(err)
	}
	return out, nil
}
func (s *Store) ListTemplates(ctx context.Context, sc tenancy.Scope, onlyPublished bool) ([]TemplateRow, error) {
	out := []TemplateRow{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+templateCols+` FROM workflow_templates WHERE (NOT $1 OR published_at IS NOT NULL) ORDER BY name,version`, onlyPublished)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			x, e := scanTemplate(rows)
			if e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// DisableTemplate retires a template without changing its immutable published definition.
func (s *Store) DisableTemplate(ctx context.Context, sc tenancy.Scope, id string) (TemplateRow, error) {
	return s.setTemplateDisabled(ctx, sc, id, true)
}

// EnableTemplate clears a template's retired marker. Published definitions remain immutable.
func (s *Store) EnableTemplate(ctx context.Context, sc tenancy.Scope, id string) (TemplateRow, error) {
	return s.setTemplateDisabled(ctx, sc, id, false)
}

func (s *Store) setTemplateDisabled(ctx context.Context, sc tenancy.Scope, id string, disabled bool) (TemplateRow, error) {
	var out TemplateRow
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		const update = `UPDATE workflow_templates SET disabled_at=CASE WHEN $2 THEN now() ELSE NULL END WHERE id=$1 AND (($2 AND disabled_at IS NULL) OR (NOT $2 AND disabled_at IS NOT NULL)) RETURNING `
		var e error
		out, e = scanTemplate(tx.QueryRowContext(ctx, update+templateCols, id, disabled))
		if errors.Is(e, sql.ErrNoRows) {
			out, e = scanTemplate(tx.QueryRowContext(ctx, `SELECT `+templateCols+` FROM workflow_templates WHERE id=$1`, id))
		}
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		return TemplateRow{}, mapError(err)
	}
	return out, nil
}
func (s *Store) GetTemplateVersion(ctx context.Context, sc tenancy.Scope, name string, version int) (TemplateRow, error) {
	var out TemplateRow
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanTemplate(tx.QueryRowContext(ctx, `SELECT `+templateCols+` FROM workflow_templates WHERE name=$1 AND version=$2`, name, version))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	return out, err
}
func (s *Store) DeleteTemplate(ctx context.Context, sc tenancy.Scope, id string) error {
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		x, e := scanTemplate(tx.QueryRowContext(ctx, `SELECT `+templateCols+` FROM workflow_templates WHERE id=$1 FOR UPDATE`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if x.PublishedAt != nil && x.DisabledAt == nil {
			return ErrTemplatePublished
		}
		var used bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_runs WHERE template_id=$1)`, id).Scan(&used); e != nil {
			return e
		}
		if used {
			return ErrTemplateInUse
		}
		_, e = tx.ExecContext(ctx, `DELETE FROM workflow_templates WHERE id=$1`, id)
		return e
	})
	return mapError(err)
}

// Runs
type RunState string

const (
	RunQueued       RunState = "queued"
	RunRunning      RunState = "running"
	RunWaitingHuman RunState = "waiting_human"
	RunCompleted    RunState = "completed"
	RunFailed       RunState = "failed"
	RunCancelled    RunState = "cancelled"
)

type Run struct {
	ID, TenantID, TemplateID string
	TemplateVersion          int
	ItemID                   string
	DefinitionSnapshot       json.RawMessage
	// Variables are the run's inputs: the values a template's placeholders
	// are filled with and the values a node's `config.runner` may resolve
	// ($NAME). They win over the tenant's defaults.
	Variables map[string]string
	// Language is the language this run works in. It is resolved when the run
	// starts — the request may name one, otherwise the tenant's language is
	// inherited — and it never changes afterwards, so the agents of a running
	// workflow all write in the same language.
	Language       tenancy.Language
	State          RunState
	MaxConcurrency int
	StartedBy      string
	WorktreePath   string
	Branch         string
	StartedAt      *time.Time
	FinishedAt     *time.Time
	ErrorText      string
	FailureKind    string
	CreatedAt      time.Time
}
type NewRun struct {
	TemplateID         string
	TemplateVersion    int
	ItemID             string
	DefinitionSnapshot json.RawMessage
	// Variables are the run's own inputs. Empty means the run inherits the
	// tenant's defaults.
	Variables map[string]string
	// Language is the language the run works in. Empty means English.
	Language       tenancy.Language
	MaxConcurrency int
	StartedBy      string
}
type RunFilter struct {
	ItemID string
	State  string
	Limit  int
}

const runCols = `id,tenant_id,template_id,template_version,item_id,definition_snapshot_json,variables_json,language,state,max_concurrency,started_by,worktree_path,branch,started_at,finished_at,error,failure_kind,created_at`

func scanRun(r rowScanner) (Run, error) {
	var x Run
	var item, by, worktree, branch, errText, failure sql.NullString
	var snap, variables []byte
	var language sql.NullString
	var started, finished sql.NullTime
	e := r.Scan(&x.ID, &x.TenantID, &x.TemplateID, &x.TemplateVersion, &item, &snap, &variables, &language, &x.State, &x.MaxConcurrency, &by, &worktree, &branch, &started, &finished, &errText, &failure, &x.CreatedAt)
	if e != nil {
		return x, e
	}
	x.ItemID = nullString(item)
	x.DefinitionSnapshot = snap
	x.Variables = decodeVariables(variables)
	x.Language = tenancy.Language(nullString(language))
	x.StartedBy = nullString(by)
	x.WorktreePath = nullString(worktree)
	x.Branch = nullString(branch)
	x.StartedAt = nullableTime(started)
	x.FinishedAt = nullableTime(finished)
	x.ErrorText = nullString(errText)
	x.FailureKind = nullString(failure)
	return x, nil
}

// decodeVariables reads a variables_json document into a map. A missing or
// malformed document is an empty map: the run still runs, it just has no
// variables of its own.
func decodeVariables(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]string{}
	}
	return out
}

// encodeVariables renders a variable map as the JSON document the columns
// carry.
func encodeVariables(vars map[string]string) (json.RawMessage, error) {
	if vars == nil {
		return json.RawMessage(`{}`), nil
	}
	b, err := json.Marshal(vars)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Store) CreateRun(ctx context.Context, sc tenancy.Scope, in NewRun) (Run, error) {
	if len(in.DefinitionSnapshot) == 0 {
		in.DefinitionSnapshot = json.RawMessage(`{}`)
	}
	if in.MaxConcurrency == 0 {
		in.MaxConcurrency = 4
	}
	// The language of a run is one of the two the product speaks. An unknown
	// one is refused here too, so a caller that skipped the resolution cannot
	// store a language no agent would be told.
	if in.Language == "" {
		in.Language = tenancy.DefaultLanguage
	}
	if !in.Language.Valid() {
		return Run{}, fmt.Errorf("%w: %q", ErrUnknownLanguage, in.Language)
	}
	variables, err := encodeVariables(in.Variables)
	if err != nil {
		return Run{}, err
	}
	var out Run
	err = s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanRun(tx.QueryRowContext(ctx, `INSERT INTO workflow_runs (id,tenant_id,template_id,template_version,item_id,definition_snapshot_json,variables_json,language,max_concurrency,started_by) VALUES ($1,$2,$3,$4,nullif($5,''),$6::jsonb,$7::jsonb,$8,$9,nullif($10,'')) RETURNING `+runCols, newID(), sc.TenantID, in.TemplateID, in.TemplateVersion, in.ItemID, in.DefinitionSnapshot, variables, string(in.Language), in.MaxConcurrency, in.StartedBy))
		return e
	})
	if err != nil {
		return Run{}, mapError(err)
	}
	return out, nil
}
func (s *Store) GetRun(ctx context.Context, sc tenancy.Scope, id string) (Run, error) {
	var out Run
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanRun(tx.QueryRowContext(ctx, `SELECT `+runCols+` FROM workflow_runs WHERE id=$1`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	return out, err
}
func (s *Store) ListRuns(ctx context.Context, sc tenancy.Scope, f RunFilter) ([]Run, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	out := []Run{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+runCols+` FROM workflow_runs WHERE ($1='' OR item_id=$1) AND ($2='' OR state=$2) ORDER BY created_at DESC LIMIT $3`, f.ItemID, f.State, f.Limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			x, e := scanRun(rows)
			if e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Store) SetRunState(ctx context.Context, sc tenancy.Scope, id string, state RunState, failureKind, errText string) (Run, error) {
	var out Run
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanRun(tx.QueryRowContext(ctx, `UPDATE workflow_runs SET state=$2, failure_kind=nullif($3,''), error=nullif($4,''), started_at=CASE WHEN $2='running' THEN COALESCE(started_at,now()) ELSE started_at END, finished_at=CASE WHEN $2 IN ('completed','failed','cancelled') THEN now() ELSE NULL END WHERE id=$1 RETURNING `+runCols, id, state, failureKind, errText))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		return Run{}, mapError(err)
	}
	return out, nil
}
func (s *Store) CancelRun(ctx context.Context, sc tenancy.Scope, id string) (Run, error) {
	var out Run
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		cur, e := scanRun(tx.QueryRowContext(ctx, `SELECT `+runCols+` FROM workflow_runs WHERE id=$1 FOR UPDATE`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if terminalRun(cur.State) {
			return ErrTerminal
		}
		out, e = scanRun(tx.QueryRowContext(ctx, `UPDATE workflow_runs SET state='cancelled',finished_at=now() WHERE id=$1 RETURNING `+runCols, id))
		return e
	})
	if err != nil {
		return Run{}, err
	}
	return out, nil
}
func terminalRun(s RunState) bool { return s == RunCompleted || s == RunFailed || s == RunCancelled }

// Step runs
type StepRun struct {
	ID, TenantID, RunID, NodeID     string
	IterationPath                   string
	RunIndex                        int
	Kind, Name                      string
	State                           StepState
	ActorKind, ActorID              string
	InputEdges                      map[string]Receipt
	MemoryManifest, ContextManifest json.RawMessage
	TaskFileSHA                     string
	OutputPorts                     map[string]json.RawMessage
	Result                          json.RawMessage
	FailureKind                     string
	WorkspaceKind, Workdir          string
	AgentJobID                      string
	ResumeToken                     string
	ClaimLock                       string
	ClaimExpiresAt, HeartbeatAt     *time.Time
	SupersededBy                    string
	StartedAt, FinishedAt           *time.Time
	CreatedAt                       time.Time
}
type NewStepRun struct {
	RunID, NodeID, IterationPath string
	RunIndex                     int
	Kind, Name                   string
	ActorKind, ActorID           string
	WorkspaceKind                string
}
type StepRunPatch struct {
	State           *StepState
	ActorKind       *string
	ActorID         *string
	InputEdges      *map[string]Receipt
	OutputPorts     *map[string]json.RawMessage
	Result          *json.RawMessage
	FailureKind     *string
	WorkspaceKind   *string
	Workdir         *string
	AgentJobID      *string
	ResumeToken     *string
	SupersededBy    *string
	StartedAt       *time.Time
	FinishedAt      *time.Time
	MemoryManifest  *json.RawMessage
	ContextManifest *json.RawMessage
	TaskFileSHA     *string
}

const stepCols = `id,tenant_id,run_id,node_id,iteration_path,run_index,kind,name,state,actor_kind,actor_id,input_edges_json,memory_manifest_json,context_manifest_json,task_file_sha,output_ports_json,result_json,failure_kind,workspace_kind,workdir,agent_job_id,resume_token,claim_lock,claim_expires_at,heartbeat_at,superseded_by,started_at,finished_at,created_at`

func scanStep(r rowScanner) (StepRun, error) {
	var x StepRun
	var actorKind, actorID, sha, failure, workdir, job, resume, lock, sup sql.NullString
	var edges, mem, ctxm, ports, result []byte
	var expiry, beat, start, finish sql.NullTime
	e := r.Scan(&x.ID, &x.TenantID, &x.RunID, &x.NodeID, &x.IterationPath, &x.RunIndex, &x.Kind, &x.Name, &x.State, &actorKind, &actorID, &edges, &mem, &ctxm, &sha, &ports, &result, &failure, &x.WorkspaceKind, &workdir, &job, &resume, &lock, &expiry, &beat, &sup, &start, &finish, &x.CreatedAt)
	if e != nil {
		return x, e
	}
	x.ActorKind = nullString(actorKind)
	x.ActorID = nullString(actorID)
	x.TaskFileSHA = nullString(sha)
	x.FailureKind = nullString(failure)
	x.Workdir = nullString(workdir)
	x.AgentJobID = nullString(job)
	x.ResumeToken = nullString(resume)
	x.ClaimLock = nullString(lock)
	x.SupersededBy = nullString(sup)
	x.InputEdges = map[string]Receipt{}
	if len(edges) > 0 {
		if e = json.Unmarshal(edges, &x.InputEdges); e != nil {
			return x, e
		}
	}
	x.MemoryManifest = mem
	x.ContextManifest = ctxm
	x.OutputPorts = map[string]json.RawMessage{}
	if len(ports) > 0 {
		if e = json.Unmarshal(ports, &x.OutputPorts); e != nil {
			return x, e
		}
	}
	x.Result = result
	x.ClaimExpiresAt = nullableTime(expiry)
	x.HeartbeatAt = nullableTime(beat)
	x.StartedAt = nullableTime(start)
	x.FinishedAt = nullableTime(finish)
	return x, nil
}
func (s *Store) CreateStepRun(ctx context.Context, sc tenancy.Scope, in NewStepRun) (StepRun, error) {
	if in.WorkspaceKind == "" {
		in.WorkspaceKind = "scratch"
	}
	var out StepRun
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanStep(tx.QueryRowContext(ctx, `INSERT INTO step_runs (id,tenant_id,run_id,node_id,iteration_path,run_index,kind,name,actor_kind,actor_id,workspace_kind) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,''),nullif($10,''),$11) RETURNING `+stepCols, newID(), sc.TenantID, in.RunID, in.NodeID, in.IterationPath, in.RunIndex, in.Kind, in.Name, in.ActorKind, in.ActorID, in.WorkspaceKind))
		return e
	})
	if err != nil {
		if pg, ok := err.(*pgconn.PgError); ok && pg.Code == "23505" {
			return StepRun{}, ErrDuplicateStep
		}
		return StepRun{}, mapError(err)
	}
	return out, nil
}
func (s *Store) GetStepRun(ctx context.Context, sc tenancy.Scope, id string) (StepRun, error) {
	var out StepRun
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanStep(tx.QueryRowContext(ctx, `SELECT `+stepCols+` FROM step_runs WHERE id=$1`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	return out, err
}
func (s *Store) ListStepRuns(ctx context.Context, sc tenancy.Scope, runID string) ([]StepRun, error) {
	out := []StepRun{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+stepCols+` FROM step_runs WHERE run_id=$1 ORDER BY created_at,node_id,iteration_path,run_index`, runID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			x, e := scanStep(rows)
			if e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Store) UpdateStepRun(ctx context.Context, sc tenancy.Scope, id string, p StepRunPatch) (StepRun, error) {
	var out StepRun
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		sets := []string{}
		args := []any{id}
		add := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf(col+`=$%d`, len(args)))
		}
		if p.State != nil {
			add("state", *p.State)
		}
		if p.ActorKind != nil {
			add("actor_kind", *p.ActorKind)
		}
		if p.ActorID != nil {
			add("actor_id", *p.ActorID)
		}
		if p.InputEdges != nil {
			edges := *p.InputEdges
			if edges == nil {
				edges = map[string]Receipt{}
			}
			b, e := json.Marshal(edges)
			if e != nil {
				return e
			}
			args = append(args, b)
			sets = append(sets, fmt.Sprintf("input_edges_json=$%d::jsonb", len(args)))
		}
		if p.OutputPorts != nil {
			ports := *p.OutputPorts
			if ports == nil {
				ports = map[string]json.RawMessage{}
			}
			b, e := json.Marshal(ports)
			if e != nil {
				return e
			}
			args = append(args, b)
			sets = append(sets, fmt.Sprintf("output_ports_json=$%d::jsonb", len(args)))
		}
		if p.Result != nil {
			add("result_json", raw(*p.Result))
		}
		if p.FailureKind != nil {
			add("failure_kind", *p.FailureKind)
		}
		if p.WorkspaceKind != nil {
			add("workspace_kind", *p.WorkspaceKind)
		}
		if p.Workdir != nil {
			add("workdir", *p.Workdir)
		}
		if p.AgentJobID != nil {
			add("agent_job_id", *p.AgentJobID)
		}
		if p.ResumeToken != nil {
			add("resume_token", *p.ResumeToken)
		}
		if p.SupersededBy != nil {
			add("superseded_by", *p.SupersededBy)
		}
		if p.StartedAt != nil {
			add("started_at", *p.StartedAt)
		}
		if p.FinishedAt != nil {
			add("finished_at", *p.FinishedAt)
		}
		if p.MemoryManifest != nil {
			add("memory_manifest_json", raw(*p.MemoryManifest))
		}
		if p.ContextManifest != nil {
			add("context_manifest_json", raw(*p.ContextManifest))
		}
		if p.TaskFileSHA != nil {
			add("task_file_sha", *p.TaskFileSHA)
		}
		if len(sets) > 0 {
			if _, e := tx.ExecContext(ctx, `UPDATE step_runs SET `+strings.Join(sets, ",")+` WHERE id=$1`, args...); e != nil {
				return e
			}
		}
		var e error
		out, e = scanStep(tx.QueryRowContext(ctx, `SELECT `+stepCols+` FROM step_runs WHERE id=$1`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		return StepRun{}, mapError(err)
	}
	return out, nil
}
func (s *Store) ClaimStepRun(ctx context.Context, sc tenancy.Scope, id, claimLock string, lease time.Duration) (StepRun, error) {
	var out StepRun
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanStep(tx.QueryRowContext(ctx, `UPDATE step_runs SET state='running',claim_lock=$2,claim_expires_at=now()+make_interval(secs=>$3::int),heartbeat_at=now(),started_at=COALESCE(started_at,now()) WHERE id=$1 AND state IN ('ready','pending') AND (claim_expires_at IS NULL OR claim_expires_at<=now()) RETURNING `+stepCols, id, claimLock, int(lease.Seconds())))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrClaimLost
		}
		return e
	})
	if err != nil {
		return StepRun{}, err
	}
	return out, nil
}
func (s *Store) HeartbeatStepRun(ctx context.Context, sc tenancy.Scope, id, claimLock string, lease time.Duration) error {
	return s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		tag, e := tx.ExecContext(ctx, `UPDATE step_runs SET heartbeat_at=now(),claim_expires_at=now()+make_interval(secs=>$3::int) WHERE id=$1 AND claim_lock=$2 AND state='running'`, id, claimLock, int(lease.Seconds()))
		if e != nil {
			return e
		}
		n, e := tag.RowsAffected()
		if e == nil && n == 0 {
			return ErrClaimLost
		}
		return e
	})
}
func (s *Store) SetStepTerminal(ctx context.Context, sc tenancy.Scope, id, claimLock string, state StepState, result json.RawMessage, outputPorts map[string]json.RawMessage, failureKind string) (StepRun, error) {
	return s.SetStepTerminalWithReceipts(ctx, sc, id, claimLock, state, result, outputPorts, failureKind, nil)
}

// DownstreamReceipt carries the receipts one downstream execution receives
// from a terminal upstream execution.
type DownstreamReceipt struct {
	// StepRunID is the downstream step run that receives the receipts.
	StepRunID string
	// Receipts is keyed by edge id.
	Receipts map[string]Receipt
}

// SetStepTerminalWithReceipts marks a step terminal AND writes the edge
// receipts into the downstream executions' join accumulators, in ONE
// transaction.
//
// This is the join correctness guarantee from the design: "each terminal
// upstream execution writes its edge receipt into the downstream execution's
// input_edges_json in the same transaction that marks it terminal". A side
// effect committed but the receipt lost is the one failure mode the diamond
// cannot tolerate, and a second transaction would leave exactly that window
// open. The downstream step runs must already exist (the engine creates them
// before it terminates the upstream node).
func (s *Store) SetStepTerminalWithReceipts(ctx context.Context, sc tenancy.Scope, id, claimLock string, state StepState, result json.RawMessage, outputPorts map[string]json.RawMessage, failureKind string, downstream []DownstreamReceipt) (StepRun, error) {
	if state != StepSucceeded && state != StepFailed && state != StepSkipped && state != StepBlocked && state != StepCancelled && state != StepSuperseded {
		return StepRun{}, ErrInvalidTransition
	}
	var out StepRun
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		if outputPorts == nil {
			outputPorts = map[string]json.RawMessage{}
		}
		out, e = scanStep(tx.QueryRowContext(ctx, `UPDATE step_runs SET state=$3,result_json=$4::jsonb,output_ports_json=$5::jsonb,failure_kind=nullif($6,''),finished_at=now(),claim_lock=NULL,claim_expires_at=NULL,heartbeat_at=NULL WHERE id=$1 AND claim_lock=$2 AND state='running' RETURNING `+stepCols, id, claimLock, state, raw(result), mustJSON(outputPorts), failureKind))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrClaimLost
		}
		if e != nil {
			return e
		}
		for _, d := range downstream {
			if d.StepRunID == "" || len(d.Receipts) == 0 {
				continue
			}
			// jsonb || merges the top level, so the downstream accumulator
			// keeps the receipts of the other incoming edges.
			if _, e := tx.ExecContext(ctx,
				`UPDATE step_runs SET input_edges_json = input_edges_json || $2::jsonb WHERE id = $1`,
				d.StepRunID, mustJSON(d.Receipts)); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return StepRun{}, mapError(err)
	}
	return out, nil
}

// MergeStepReceipts merges receipts into a step's join accumulator without
// changing its state. It is used when a receipt arrives outside a terminal
// transition — for example when a merge node collects one more branch.
func (s *Store) MergeStepReceipts(ctx context.Context, sc tenancy.Scope, stepRunID string, receipts map[string]Receipt) (StepRun, error) {
	if stepRunID == "" {
		return StepRun{}, ErrNotFound
	}
	if receipts == nil {
		receipts = map[string]Receipt{}
	}
	var out StepRun
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanStep(tx.QueryRowContext(ctx,
			`UPDATE step_runs SET input_edges_json = input_edges_json || $2::jsonb WHERE id = $1 RETURNING `+stepCols,
			stepRunID, mustJSON(receipts)))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		return StepRun{}, mapError(err)
	}
	return out, nil
}

// ResetStep puts a running step back to pending and clears its claim, so the
// scheduler dispatches it again. It is how a steered execution is resumed:
// the adapter was stopped, and the next dispatch carries the instruction.
func (s *Store) ResetStep(ctx context.Context, sc tenancy.Scope, id string) (StepRun, error) {
	var out StepRun
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanStep(tx.QueryRowContext(ctx, `
			UPDATE step_runs
			   SET state = 'pending',
			       claim_lock = NULL,
			       claim_expires_at = NULL,
			       heartbeat_at = NULL,
			       started_at = NULL
			 WHERE id = $1 AND state IN ('running', 'waiting')
			RETURNING `+stepCols, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	if err != nil {
		return StepRun{}, mapError(err)
	}
	return out, nil
}

func (s *Store) SupersedeSteps(ctx context.Context, sc tenancy.Scope, runID string, nodeIDs []string) error {
	return s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var snap []byte
		if e := tx.QueryRowContext(ctx, `SELECT definition_snapshot_json FROM workflow_runs WHERE id=$1`, runID).Scan(&snap); errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		} else if e != nil {
			return e
		}
		var graph Template
		if e := json.Unmarshal(snap, &graph); e != nil {
			return e
		}
		todo := map[string]bool{}
		for _, id := range nodeIDs {
			todo[id] = true
		}
		for changed := true; changed; {
			changed = false
			for _, edge := range graph.Edges {
				if todo[edge.From.Node] && !todo[edge.To.Node] {
					todo[edge.To.Node] = true
					changed = true
				}
			}
		}
		for node := range todo {
			rows, e := tx.QueryContext(ctx, `SELECT id,run_index FROM step_runs WHERE run_id=$1 AND node_id=$2 AND state<>'superseded' ORDER BY run_index DESC`, runID, node)
			if e != nil {
				return e
			}
			type prior struct {
				id  string
				idx int
			}
			var matches []prior
			for rows.Next() {
				var p prior
				if e = rows.Scan(&p.id, &p.idx); e != nil {
					rows.Close()
					return e
				}
				matches = append(matches, p)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			if len(matches) == 0 {
				continue
			}
			replacement := matches[0].id
			for _, p := range matches {
				rep := replacement
				if p.id == replacement {
					rep = ""
				}
				if _, e = tx.ExecContext(ctx, `UPDATE step_runs SET state='superseded',superseded_by=nullif($2,''),finished_at=COALESCE(finished_at,now()),claim_lock=NULL,claim_expires_at=NULL,heartbeat_at=NULL WHERE id=$1`, p.id, rep); e != nil {
					return e
				}
			}
		}
		return nil
	})
}

// Checkpoints
type CheckpointState string

const (
	CheckpointPending          CheckpointState = "pending"
	CheckpointApproved         CheckpointState = "approved"
	CheckpointApprovedWithEdit CheckpointState = "approved_with_edits"
	CheckpointRejected         CheckpointState = "rejected"
	CheckpointAborted          CheckpointState = "aborted"
	CheckpointSuperseded       CheckpointState = "superseded"
)

type Checkpoint struct {
	ID, TenantID, RunID, StepRunID, NodeID string
	Lap                                    int
	State                                  CheckpointState
	SummaryMD, DecisionMD                  string
	Fields, FieldsSpec                     json.RawMessage
	DecidedByKind, DecidedBy               string
	DecidedAt, RemindAfter                 *time.Time
	CreatedAt                              time.Time
}
type NewCheckpoint struct {
	RunID, StepRunID, NodeID string
	Lap                      int
	SummaryMD                string
	FieldsSpec               json.RawMessage
	RemindAfter              *time.Time
}
type Decision struct {
	Kind       CheckpointState
	DecisionMD string
	Fields     json.RawMessage
	DecidedBy  string
}

const checkpointCols = `id,tenant_id,run_id,step_run_id,node_id,lap,state,summary_md,decision_md,fields_json,fields_spec_json,decided_by_kind,decided_by,decided_at,remind_after,created_at`

func scanCheckpoint(r rowScanner) (Checkpoint, error) {
	var x Checkpoint
	var decision, byKind, by sql.NullString
	var fields []byte
	var decided, remind sql.NullTime
	var fieldsSpec []byte
	e := r.Scan(&x.ID, &x.TenantID, &x.RunID, &x.StepRunID, &x.NodeID, &x.Lap, &x.State, &x.SummaryMD, &decision, &fields, &fieldsSpec, &byKind, &by, &decided, &remind, &x.CreatedAt)
	if e != nil {
		return x, e
	}
	x.DecisionMD = nullString(decision)
	x.Fields = fields
	x.FieldsSpec = fieldsSpec
	x.DecidedByKind = nullString(byKind)
	x.DecidedBy = nullString(by)
	x.DecidedAt = nullableTime(decided)
	x.RemindAfter = nullableTime(remind)
	return x, nil
}
func (s *Store) CreateCheckpoint(ctx context.Context, sc tenancy.Scope, in NewCheckpoint) (Checkpoint, error) {
	var out Checkpoint
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var parentsExist bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_runs WHERE id=$1) AND EXISTS(SELECT 1 FROM step_runs WHERE id=$2)`, in.RunID, in.StepRunID).Scan(&parentsExist); e != nil {
			return e
		}
		if !parentsExist {
			return ErrNotFound
		}
		if in.Lap <= 0 {
			if _, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, in.StepRunID); e != nil {
				return e
			}
			if e := tx.QueryRowContext(ctx, `SELECT COALESCE(max(lap),0)+1 FROM checkpoints WHERE step_run_id=$1`, in.StepRunID).Scan(&in.Lap); e != nil {
				return e
			}
		}
		var e error
		out, e = scanCheckpoint(tx.QueryRowContext(ctx, `INSERT INTO checkpoints (id,tenant_id,run_id,step_run_id,node_id,lap,summary_md,fields_spec_json,remind_after) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9) RETURNING `+checkpointCols, newID(), sc.TenantID, in.RunID, in.StepRunID, in.NodeID, in.Lap, in.SummaryMD, raw(in.FieldsSpec), in.RemindAfter))
		return e
	})
	if err != nil {
		return Checkpoint{}, mapError(err)
	}
	return out, nil
}
func (s *Store) GetCheckpoint(ctx context.Context, sc tenancy.Scope, id string) (Checkpoint, error) {
	var out Checkpoint
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanCheckpoint(tx.QueryRowContext(ctx, `SELECT `+checkpointCols+` FROM checkpoints WHERE id=$1`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	return out, err
}
func (s *Store) ListCheckpoints(ctx context.Context, sc tenancy.Scope, state string, limit int) ([]Checkpoint, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	out := []Checkpoint{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+checkpointCols+` FROM checkpoints WHERE ($1='' OR state=$1) ORDER BY created_at DESC LIMIT $2`, state, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			x, e := scanCheckpoint(rows)
			if e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// ListRunCheckpoints returns the checkpoints of one run, oldest first. The
// monitor needs them next to the steps: the checkpoint is a separate row with
// its own id, and the decision endpoint takes that id.
func (s *Store) ListRunCheckpoints(ctx context.Context, sc tenancy.Scope, runID string) ([]Checkpoint, error) {
	if runID == "" {
		return nil, ErrNotFound
	}
	out := []Checkpoint{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+checkpointCols+` FROM checkpoints WHERE run_id=$1 ORDER BY created_at ASC, id ASC`, runID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			x, e := scanCheckpoint(rows)
			if e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) DecideCheckpoint(ctx context.Context, sc tenancy.Scope, id string, lap int, d Decision) (Checkpoint, error) {
	if d.Kind != CheckpointApproved && d.Kind != CheckpointApprovedWithEdit && d.Kind != CheckpointRejected && d.Kind != CheckpointAborted {
		return Checkpoint{}, ErrInvalidTransition
	}
	if (d.Kind == CheckpointApprovedWithEdit || d.Kind == CheckpointRejected) && strings.TrimSpace(d.DecisionMD) == "" {
		return Checkpoint{}, ErrInvalidTransition
	}
	var out Checkpoint
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		updated, e := scanCheckpoint(tx.QueryRowContext(ctx, `UPDATE checkpoints SET state=$3,decided_by_kind='human',decided_by=nullif($4,''),decided_at=now(),decision_md=nullif($5,''),fields_json=$6::jsonb WHERE id=$1 AND lap=$2 AND state='pending' RETURNING `+checkpointCols, id, lap, d.Kind, d.DecidedBy, d.DecisionMD, raw(d.Fields)))
		if e != nil {
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			winner, e := scanCheckpoint(tx.QueryRowContext(ctx, `SELECT `+checkpointCols+` FROM checkpoints WHERE id=$1 AND lap=$2`, id, lap))
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			out = winner
			return errors.Join(ErrAlreadyDecided, e)
		}
		out = updated
		actorKind := "system"
		if d.DecidedBy != "" {
			actorKind = "user"
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO activity_log (id,tenant_id,item_id,actor_kind,actor_user_id,action,after_json) VALUES ($1,$2,NULL,$3,nullif($4,''),'checkpoint.decided',$5::jsonb)`, newID(), sc.TenantID, actorKind, d.DecidedBy, mustJSON(updated))
		return e
	})
	if err != nil {
		var joined interface{ Unwrap() []error }
		if errors.As(err, &joined) {
			return out, err
		}
		return Checkpoint{}, mapError(err)
	}
	return out, nil
}
func (s *Store) SetCheckpointRemindAfter(ctx context.Context, sc tenancy.Scope, id string, at time.Time) (Checkpoint, error) {
	var out Checkpoint
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanCheckpoint(tx.QueryRowContext(ctx, `UPDATE checkpoints SET remind_after=$2 WHERE id=$1 AND state='pending' RETURNING `+checkpointCols, id, at))
		if errors.Is(e, sql.ErrNoRows) {
			var exists bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checkpoints WHERE id=$1)`, id).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return ErrNotFound
			}
			return ErrTerminal
		}
		return e
	})
	if err != nil {
		return Checkpoint{}, mapError(err)
	}
	return out, nil
}

// Interventions
type InterventionKind string

const (
	InterventionInject InterventionKind = "inject"
	InterventionRedo   InterventionKind = "redo"
	InterventionStop   InterventionKind = "stop"
)

type Intervention struct {
	ID, TenantID, RunID, NodeID, StepRunID, AgentJobID string
	Kind                                               InterventionKind
	BodyMD                                             string
	ActorKind, ActorID                                 string
	Seq                                                int
	DeliveredAt                                        *time.Time
	CreatedAt                                          time.Time
}
type NewIntervention struct {
	RunID, NodeID, StepRunID, AgentJobID string
	Kind                                 InterventionKind
	BodyMD                               string
	ActorKind, ActorID                   string
}

const interventionCols = `id,tenant_id,run_id,node_id,step_run_id,agent_job_id,kind,body_md,actor_kind,actor_id,seq,delivered_at,created_at`

func scanIntervention(r rowScanner) (Intervention, error) {
	var x Intervention
	var node, step, job, body, actor sql.NullString
	var delivered sql.NullTime
	e := r.Scan(&x.ID, &x.TenantID, &x.RunID, &node, &step, &job, &x.Kind, &body, &x.ActorKind, &actor, &x.Seq, &delivered, &x.CreatedAt)
	if e != nil {
		return x, e
	}
	x.NodeID = nullString(node)
	x.StepRunID = nullString(step)
	x.AgentJobID = nullString(job)
	x.BodyMD = nullString(body)
	x.ActorID = nullString(actor)
	x.DeliveredAt = nullableTime(delivered)
	return x, nil
}
func (s *Store) CreateIntervention(ctx context.Context, sc tenancy.Scope, in NewIntervention) (Intervention, error) {
	var out Intervention
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var exists bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_runs WHERE id=$1) AND ($2='' OR EXISTS(SELECT 1 FROM step_runs WHERE id=$2)) AND ($3='' OR EXISTS(SELECT 1 FROM agent_jobs WHERE id=$3))`, in.RunID, in.StepRunID, in.AgentJobID).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return ErrNotFound
		}
		key := in.StepRunID
		if key == "" {
			key = in.RunID
		}
		if _, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, key); e != nil {
			return e
		}
		var seq int
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE(max(seq),0)+1 FROM interventions WHERE step_run_id IS NOT DISTINCT FROM nullif($1,'') AND run_id=$2`, in.StepRunID, in.RunID).Scan(&seq); e != nil {
			return e
		}
		var e error
		out, e = scanIntervention(tx.QueryRowContext(ctx, `INSERT INTO interventions (id,tenant_id,run_id,node_id,step_run_id,agent_job_id,kind,body_md,actor_kind,actor_id,seq) VALUES ($1,$2,$3,nullif($4,''),nullif($5,''),nullif($6,''),$7,nullif($8,''),$9,nullif($10,''),$11) RETURNING `+interventionCols, newID(), sc.TenantID, in.RunID, in.NodeID, in.StepRunID, in.AgentJobID, in.Kind, in.BodyMD, in.ActorKind, in.ActorID, seq))
		return e
	})
	if err != nil {
		return Intervention{}, mapError(err)
	}
	return out, nil
}
func (s *Store) ListInterventions(ctx context.Context, sc tenancy.Scope, runID string) ([]Intervention, error) {
	out := []Intervention{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+interventionCols+` FROM interventions WHERE run_id=$1 ORDER BY seq`, runID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			x, e := scanIntervention(rows)
			if e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Store) PendingInterventions(ctx context.Context, sc tenancy.Scope, stepRunID string) ([]Intervention, error) {
	out := []Intervention{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+interventionCols+` FROM interventions WHERE step_run_id=$1 AND delivered_at IS NULL ORDER BY seq`, stepRunID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			x, e := scanIntervention(rows)
			if e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Store) MarkInterventionDelivered(ctx context.Context, sc tenancy.Scope, id string) (Intervention, error) {
	var out Intervention
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanIntervention(tx.QueryRowContext(ctx, `UPDATE interventions SET delivered_at=now() WHERE id=$1 AND delivered_at IS NULL RETURNING `+interventionCols, id))
		if errors.Is(e, sql.ErrNoRows) {
			var exists bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM interventions WHERE id=$1)`, id).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return ErrNotFound
			}
			return ErrTerminal
		}
		return e
	})
	if err != nil {
		return Intervention{}, mapError(err)
	}
	return out, nil
}

// Artifacts
type Artifact struct {
	ID, TenantID, RunID, StepRunID, ItemID string
	Kind, URI                              string
	Meta                                   json.RawMessage
	CreatedAt                              time.Time
}
type NewArtifact struct {
	RunID, StepRunID, ItemID, Kind, URI string
	Meta                                json.RawMessage
}

const artifactCols = `id,tenant_id,run_id,step_run_id,item_id,kind,uri,meta_json,created_at`

func scanArtifact(r rowScanner) (Artifact, error) {
	var x Artifact
	var run, step, item sql.NullString
	var meta []byte
	e := r.Scan(&x.ID, &x.TenantID, &run, &step, &item, &x.Kind, &x.URI, &meta, &x.CreatedAt)
	if e != nil {
		return x, e
	}
	x.RunID = nullString(run)
	x.StepRunID = nullString(step)
	x.ItemID = nullString(item)
	x.Meta = meta
	return x, nil
}
func (s *Store) CreateArtifact(ctx context.Context, sc tenancy.Scope, in NewArtifact) (Artifact, error) {
	if len(in.Meta) == 0 {
		in.Meta = json.RawMessage(`{}`)
	}
	var out Artifact
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		out, e = scanArtifact(tx.QueryRowContext(ctx, `INSERT INTO artifacts (id,tenant_id,run_id,step_run_id,item_id,kind,uri,meta_json) VALUES ($1,$2,nullif($3,''),nullif($4,''),nullif($5,''),$6,$7,$8::jsonb) RETURNING `+artifactCols, newID(), sc.TenantID, in.RunID, in.StepRunID, in.ItemID, in.Kind, in.URI, in.Meta))
		return e
	})
	if err != nil {
		return Artifact{}, mapError(err)
	}
	return out, nil
}
func (s *Store) ListArtifacts(ctx context.Context, sc tenancy.Scope, runID string) ([]Artifact, error) {
	out := []Artifact{}
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+artifactCols+` FROM artifacts WHERE run_id=$1 ORDER BY created_at,id`, runID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			x, e := scanArtifact(rows)
			if e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// UpsertRunArtifact records an artifact of a run.
//
// The artifacts of a worktree — the branch, the commit, the diff stat — are
// facts about the run, not about one step: the row is written once and a
// later execution updates it instead of adding a second one. The step and the
// item of an existing row are kept unless the caller names new ones.
func (s *Store) UpsertRunArtifact(ctx context.Context, sc tenancy.Scope, in NewArtifact) (Artifact, error) {
	if len(in.Meta) == 0 {
		in.Meta = json.RawMessage(`{}`)
	}
	if in.RunID == "" {
		return Artifact{}, ErrInvalidArtifact
	}
	var out Artifact
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var id string
		e := tx.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE run_id=$1 AND kind=$2 ORDER BY created_at,id LIMIT 1 FOR UPDATE`, in.RunID, in.Kind).Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			out, e = scanArtifact(tx.QueryRowContext(ctx, `INSERT INTO artifacts (id,tenant_id,run_id,step_run_id,item_id,kind,uri,meta_json) VALUES ($1,$2,$3,nullif($4,''),nullif($5,''),$6,$7,$8::jsonb) RETURNING `+artifactCols, newID(), sc.TenantID, in.RunID, in.StepRunID, in.ItemID, in.Kind, in.URI, in.Meta))
			return e
		}
		if e != nil {
			return e
		}
		out, e = scanArtifact(tx.QueryRowContext(ctx, `UPDATE artifacts SET uri=$2, meta_json=$3::jsonb, step_run_id=coalesce(nullif($4,''),step_run_id), item_id=coalesce(nullif($5,''),item_id) WHERE id=$1 RETURNING `+artifactCols, id, in.URI, in.Meta, in.StepRunID, in.ItemID))
		return e
	})
	if err != nil {
		return Artifact{}, mapError(err)
	}
	return out, nil
}

func raw(v json.RawMessage) []byte {
	if len(v) == 0 {
		return nil
	}
	return v
}
func mustJSON(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic("workflow: marshal: " + e.Error())
	}
	return b
}
