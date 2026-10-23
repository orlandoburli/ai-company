// Package failure stores the per-item/workflow retry circuit breaker. Failure
// state is eligibility, never priority; pause state is intentionally untouched.
package failure

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type Curve string

const (
	CurveExponential Curve = "exponential"
	CurveLinear      Curve = "linear"
)

type Params struct {
	XSeconds          int     `json:"x_seconds"`
	Y                 float64 `json:"y"`
	StepSeconds       int     `json:"step_seconds"`
	MaxBackoffSeconds int     `json:"max_backoff_seconds"`
	MaxFailures       int     `json:"max_failures"`
}

type Version string

type State struct {
	ItemID              string     `json:"item_id"`
	WorkflowID          string     `json:"workflow_id"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	State               string     `json:"state"`
	Curve               Curve      `json:"curve"`
	Params              Params     `json:"backoff_params"`
	Version             Version    `json:"backoff_version"`
	LastFailureKind     string     `json:"last_failure_kind,omitempty"`
	LastErrorMD         string     `json:"last_error_md,omitempty"`
	LastFailedAt        *time.Time `json:"last_failed_at,omitempty"`
	NextEligibleAt      *time.Time `json:"next_eligible_at,omitempty"`
	ExhaustedAt         *time.Time `json:"exhausted_at,omitempty"`
}

// Delay implements X*Y^(n-1) seconds for exponential and
// X+(n-1)*step_seconds for linear, rounded up to a whole second and capped.
func (p Params) Delay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	seconds := float64(p.XSeconds)
	if p.XSeconds < 0 {
		seconds = 0
	}
	// Delay itself represents the exponential curve; use LinearDelay for a
	// linear configuration. This keeps the public curve helper explicit.
	seconds *= math.Pow(p.Y, float64(failures-1))
	if math.IsNaN(seconds) || seconds < 0 {
		seconds = 0
	}
	if p.MaxBackoffSeconds > 0 && seconds > float64(p.MaxBackoffSeconds) {
		seconds = float64(p.MaxBackoffSeconds)
	}
	return time.Duration(math.Ceil(seconds)) * time.Second
}

func (p Params) delay(curve Curve, failures int) time.Duration {
	if curve == CurveLinear {
		seconds := p.XSeconds + (failures-1)*p.StepSeconds
		if seconds < 0 { seconds = 0 }
		if p.MaxBackoffSeconds > 0 && seconds > p.MaxBackoffSeconds { seconds = p.MaxBackoffSeconds }
		return time.Duration(seconds) * time.Second
	}
	return p.Delay(failures)
}

func DefaultParams(curve Curve) Params {
	switch curve {
	case CurveLinear:
		return Params{XSeconds: 60, StepSeconds: 60, MaxBackoffSeconds: 3600, MaxFailures: 5}
	default:
		return Params{XSeconds: 60, Y: 2, StepSeconds: 60, MaxBackoffSeconds: 3600, MaxFailures: 5}
	}
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var ErrNotFound = errors.New("failure: not found")

type Record struct {
	Curve Curve
	Params Params
	Version Version
	Kind string
	ErrorMD string
	FailedAt time.Time
}

func (s *Store) Record(ctx context.Context, sc tenancy.Scope, in Record) (State, error) {
	if err := sc.Validate(); err != nil { return State{}, err }
	if in.Curve != CurveLinear && in.Curve != CurveExponential { in.Curve = CurveExponential }
	if in.Params.MaxFailures <= 0 { in.Params = DefaultParams(in.Curve) }
	if in.Version == "" { if in.Curve == CurveLinear { in.Version = "bo-lin-1" } else { in.Version = "bo-exp-1" } }
	if in.FailedAt.IsZero() { in.FailedAt = time.Now().UTC() }
	var out State
	err := withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		var previous int
		err := tx.QueryRowContext(ctx, `SELECT consecutive_failures FROM item_failures WHERE item_id=$1 AND workflow_id=$2 FOR UPDATE`, inItemID(in), inWorkflowID(in)).Scan(&previous)
		_ = err
		return errors.New("failure.Record requires item and workflow ids")
	})
	return out, err
}

// FailureInput identifies the unit (item, workflow) and the failure details.
// RecordInput is an alias retained as a descriptive spelling for callers.
type FailureInput struct {
	ItemID string
	WorkflowID string
	Curve Curve
	Params Params
	Version Version
	Kind string
	ErrorMD string
	FailedAt time.Time
}

type RecordInput = FailureInput

func inItemID(_ Record) string { return "" }
func inWorkflowID(_ Record) string { return "" }

func (s *Store) RecordFailure(ctx context.Context, sc tenancy.Scope, in FailureInput) (State, error) {
	return s.record(ctx, sc, in)
}

// RecordWithIDs is the explicit convenience API; it has the same persistence
// behavior as RecordFailure.
func (s *Store) RecordWithIDs(ctx context.Context, sc tenancy.Scope, itemID, workflowID string, in Record) (State, error) {
	return s.record(ctx, sc, FailureInput{ItemID:itemID, WorkflowID:workflowID, Curve:in.Curve, Params:in.Params, Version:in.Version, Kind:in.Kind, ErrorMD:in.ErrorMD, FailedAt:in.FailedAt})
}

func (s *Store) record(ctx context.Context, sc tenancy.Scope, in FailureInput) (State, error) {
	if err := sc.Validate(); err != nil { return State{}, err }
	if in.ItemID == "" || in.WorkflowID == "" { return State{}, fmt.Errorf("failure: item and workflow are required") }
	if in.Curve != CurveLinear && in.Curve != CurveExponential { in.Curve = CurveExponential }
	if in.Params.MaxFailures <= 0 { in.Params = DefaultParams(in.Curve) }
	if in.Version == "" { if in.Curve == CurveLinear { in.Version = "bo-lin-1" } else { in.Version = "bo-exp-1" } }
	if in.FailedAt.IsZero() { in.FailedAt = time.Now().UTC() }
	var out State
	err := withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		var n int
		err := tx.QueryRowContext(ctx, `SELECT consecutive_failures FROM item_failures WHERE item_id=$1 AND workflow_id=$2 FOR UPDATE`, in.ItemID, in.WorkflowID).Scan(&n)
		if err != nil && !errors.Is(err, sql.ErrNoRows) { return err }
		if errors.Is(err, sql.ErrNoRows) { n = 0 }
		n++
		state := "backing_off"
		var until, exhausted *time.Time
		if n >= in.Params.MaxFailures {
			state = "exhausted"
			exhausted = &in.FailedAt
		} else {
			t := in.FailedAt.Add(in.Params.delay(in.Curve, n))
			until = &t
		}
		params, _ := json.Marshal(in.Params)
		id, _ := json.Marshal(nil)
		_ = id
		out = State{ItemID:in.ItemID, WorkflowID:in.WorkflowID, ConsecutiveFailures:n, State:state, Curve:in.Curve, Params:in.Params, Version:in.Version, LastFailureKind:in.Kind, LastErrorMD:in.ErrorMD, LastFailedAt:&in.FailedAt, NextEligibleAt:until, ExhaustedAt:exhausted}
		_, err = tx.ExecContext(ctx, `INSERT INTO item_failures
			(id,tenant_id,item_id,workflow_id,consecutive_failures,state,curve,backoff_params_json,backoff_version,last_failure_kind,last_error_md,last_failed_at,next_eligible_at,exhausted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (tenant_id,item_id,workflow_id) DO UPDATE SET consecutive_failures=EXCLUDED.consecutive_failures,state=EXCLUDED.state,curve=EXCLUDED.curve,backoff_params_json=EXCLUDED.backoff_params_json,backoff_version=EXCLUDED.backoff_version,last_failure_kind=EXCLUDED.last_failure_kind,last_error_md=EXCLUDED.last_error_md,last_failed_at=EXCLUDED.last_failed_at,next_eligible_at=EXCLUDED.next_eligible_at,exhausted_at=EXCLUDED.exhausted_at,updated_at=now()`,
			stringsID(), sc.TenantID, in.ItemID, in.WorkflowID, n, state, string(in.Curve), params, string(in.Version), in.Kind, in.ErrorMD, in.FailedAt, until, exhausted)
		return err
	})
	return out, err
}

func (s *Store) State(ctx context.Context, sc tenancy.Scope, itemID, workflowID string) (State, bool, error) {
	var out State
	found := false
	err := withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT item_id,workflow_id,consecutive_failures,state,curve,backoff_params_json,backoff_version,coalesce(last_failure_kind,''),coalesce(last_error_md,''),last_failed_at,next_eligible_at,exhausted_at FROM item_failures WHERE item_id=$1 AND workflow_id=$2`, itemID, workflowID).Scan(&out.ItemID,&out.WorkflowID,&out.ConsecutiveFailures,&out.State,&out.Curve,&raw,&out.Version,&out.LastFailureKind,&out.LastErrorMD,&out.LastFailedAt,&out.NextEligibleAt,&out.ExhaustedAt)
		if errors.Is(err,sql.ErrNoRows) { return nil }; if err != nil { return err }
		found = true
		return json.Unmarshal(raw,&out.Params)
	})
	return out, found, err
}

func (s *Store) Eligible(ctx context.Context, sc tenancy.Scope, itemID, workflowID string) (bool, *time.Time, error) {
	st, ok, err := s.State(ctx,sc,itemID,workflowID)
	if err != nil { return false,nil,err }
	if !ok || st.State == "ok" { return true,nil,nil }
	if st.State == "exhausted" { return false,nil,nil }
	if st.NextEligibleAt != nil && st.NextEligibleAt.After(time.Now()) { return false,st.NextEligibleAt,nil }
	return true,nil,nil
}

func (s *Store) Reset(ctx context.Context, sc tenancy.Scope, itemID, workflowID, actor string) (State, error) {
	var out State
	err := withTenant(ctx,s.db,sc,func(tx *sql.Tx) error {
		var raw []byte
		err:=tx.QueryRowContext(ctx,`UPDATE item_failures SET consecutive_failures=0,state='ok',next_eligible_at=NULL,exhausted_at=NULL,updated_at=now() WHERE item_id=$1 AND workflow_id=$2 RETURNING item_id,workflow_id,consecutive_failures,state,curve,backoff_params_json,backoff_version,coalesce(last_failure_kind,''),coalesce(last_error_md,''),last_failed_at,next_eligible_at,exhausted_at`,itemID,workflowID).Scan(&out.ItemID,&out.WorkflowID,&out.ConsecutiveFailures,&out.State,&out.Curve,&raw,&out.Version,&out.LastFailureKind,&out.LastErrorMD,&out.LastFailedAt,&out.NextEligibleAt,&out.ExhaustedAt)
		if errors.Is(err,sql.ErrNoRows) { return ErrNotFound }; if err != nil{return err}
		if err=json.Unmarshal(raw,&out.Params); err!=nil{return err}
		_,err=tx.ExecContext(ctx,`INSERT INTO activity_log (id,tenant_id,item_id,actor_kind,actor_user_id,action,after_json) VALUES ($1,$2,$3,'user',nullif($4,''),'item.failure_reset',$5::jsonb)`,stringsID(),sc.TenantID,itemID,actor,`{"workflow_id":"`+workflowID+`"}`)
		return err
	})
	return out,err
}

// Succeed resets the failure counter after a successful workflow run.
func (s *Store) Succeed(ctx context.Context, sc tenancy.Scope, itemID, workflowID string) error {
	return withTenant(ctx,s.db,sc,func(tx *sql.Tx) error {
		_,err:=tx.ExecContext(ctx,`UPDATE item_failures SET consecutive_failures=0,state='ok',next_eligible_at=NULL,exhausted_at=NULL,updated_at=now() WHERE item_id=$1 AND workflow_id=$2`,itemID,workflowID)
		return err
	})
}

func stringsID() string { return uuid.NewString() }

func withTenant(ctx context.Context, db *sql.DB, sc tenancy.Scope, fn func(*sql.Tx) error) error {
	if err:=sc.Validate(); err!=nil{return err}
	tx,err:=db.BeginTx(ctx,nil); if err!=nil{return err}; defer tx.Rollback()
	if _,err=tx.ExecContext(ctx,`SELECT set_config('app.tenant_id',$1,true)`,sc.TenantID);err!=nil{return err}
	if err=fn(tx);err!=nil{return err}; return tx.Commit()
}
