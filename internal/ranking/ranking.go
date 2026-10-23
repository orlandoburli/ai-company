// Package ranking persists and validates tenant-scoped dispatch ranking runs.
package ranking

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/orlandoburli/ai-company/internal/priority"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type Run struct {
	ID               string          `json:"id"`
	TenantID         string          `json:"tenant_id"`
	State            string          `json:"state"`
	WeightsVersion   string          `json:"weights_version"`
	LLMModel         string          `json:"llm_model,omitempty"`
	DegradedReason   string          `json:"degraded_reason,omitempty"`
	InputFingerprint string          `json:"input_fingerprint"`
	SummaryMD        string          `json:"summary_md"`
	RootsJSON        json.RawMessage `json:"roots_json"`
	ComputedAt       time.Time       `json:"computed_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	SupersededBy     *string         `json:"superseded_by,omitempty"`
}

type Entry struct {
	ID          string          `json:"id"`
	RankingID   string          `json:"ranking_id"`
	ItemID      string          `json:"item_id"`
	Rank        int             `json:"rank"`
	Score       float64         `json:"score"`
	ReasonMD    string          `json:"reason_md"`
	BaseScore   float64         `json:"base_score"`
	FactorsJSON json.RawMessage `json:"factors_json"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var ErrNotFound = errors.New("ranking: not found")

func (s *Store) Create(ctx context.Context, sc tenancy.Scope, in Run) (Run, error) {
	if err := sc.Validate(); err != nil {
		return Run{}, err
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	if in.State == "" {
		in.State = "running"
	}
	if in.WeightsVersion == "" {
		in.WeightsVersion = priority.WeightsVersion
	}
	if in.ComputedAt.IsZero() {
		in.ComputedAt = time.Now().UTC()
	}
	if in.ExpiresAt.IsZero() {
		in.ExpiresAt = in.ComputedAt.Add(3 * time.Hour)
	}
	if len(in.RootsJSON) == 0 {
		in.RootsJSON = json.RawMessage(`[]`)
	}
	err := withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `INSERT INTO priority_rankings(id,tenant_id,state,weights_version,llm_model,degraded_reason,input_fingerprint,summary_md,roots_json,computed_at,expires_at) VALUES($1,$2,$3,$4,nullif($5,''),nullif($6,''),$7,$8,$9::jsonb,$10,$11) RETURNING tenant_id,created_at`, in.ID, sc.TenantID, in.State, in.WeightsVersion, in.LLMModel, in.DegradedReason, in.InputFingerprint, in.SummaryMD, []byte(in.RootsJSON), in.ComputedAt, in.ExpiresAt).Scan(&in.TenantID, new(time.Time))
	})
	return in, err
}

func (s *Store) AddEntries(ctx context.Context, sc tenancy.Scope, runID string, entries []Entry) error {
	return withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		for _, e := range entries {
			if e.ID == "" {
				e.ID = uuid.NewString()
			}
			if len(e.FactorsJSON) == 0 {
				e.FactorsJSON = json.RawMessage(`[]`)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO priority_ranking_entries(id,tenant_id,ranking_id,item_id,rank,score,reason_md,base_score,factors_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)`, e.ID, sc.TenantID, runID, e.ItemID, e.Rank, e.Score, e.ReasonMD, e.BaseScore, []byte(e.FactorsJSON)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Ready(ctx context.Context, sc tenancy.Scope) (Run, bool, error) {
	return s.findRun(ctx, sc, `SELECT `+runColumns+` FROM priority_rankings WHERE state='ready' AND expires_at>now() ORDER BY computed_at DESC LIMIT 1`)
}
func (s *Store) Latest(ctx context.Context, sc tenancy.Scope) (Run, bool, error) {
	return s.findRun(ctx, sc, `SELECT `+runColumns+` FROM priority_rankings ORDER BY computed_at DESC,created_at DESC LIMIT 1`)
}
func (s *Store) Get(ctx context.Context, sc tenancy.Scope, id string) (Run, bool, error) {
	var out Run
	found := false
	err := withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM priority_rankings WHERE id=$1`, id).Scan(&out.ID, &out.TenantID, &out.State, &out.WeightsVersion, &out.LLMModel, &out.DegradedReason, &out.InputFingerprint, &out.SummaryMD, &out.RootsJSON, &out.ComputedAt, &out.ExpiresAt, &out.SupersededBy)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return out, found, err
}
func (s *Store) List(ctx context.Context, sc tenancy.Scope, limit int) ([]Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out := []Run{}
	err := withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+runColumns+` FROM priority_rankings ORDER BY computed_at DESC,created_at DESC LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Run
			if err := rows.Scan(&r.ID, &r.TenantID, &r.State, &r.WeightsVersion, &r.LLMModel, &r.DegradedReason, &r.InputFingerprint, &r.SummaryMD, &r.RootsJSON, &r.ComputedAt, &r.ExpiresAt, &r.SupersededBy); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

const runColumns = `id,tenant_id,state,weights_version,coalesce(llm_model,''),coalesce(degraded_reason,''),input_fingerprint,summary_md,roots_json,computed_at,expires_at,superseded_by`

func (s *Store) findRun(ctx context.Context, sc tenancy.Scope, q string) (Run, bool, error) {
	var out Run
	found := false
	err := withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, q).Scan(&out.ID, &out.TenantID, &out.State, &out.WeightsVersion, &out.LLMModel, &out.DegradedReason, &out.InputFingerprint, &out.SummaryMD, &out.RootsJSON, &out.ComputedAt, &out.ExpiresAt, &out.SupersededBy)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return out, found, err
}
func (s *Store) Entries(ctx context.Context, sc tenancy.Scope, runID string) ([]Entry, error) {
	out := []Entry{}
	err := withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT e.id,e.ranking_id,e.item_id,e.rank,e.score,e.reason_md,e.base_score,e.factors_json FROM priority_ranking_entries e JOIN priority_rankings r ON r.id=e.ranking_id AND r.tenant_id=e.tenant_id WHERE e.ranking_id=$1 ORDER BY e.rank`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Entry
			if err := rows.Scan(&e.ID, &e.RankingID, &e.ItemID, &e.Rank, &e.Score, &e.ReasonMD, &e.BaseScore, &e.FactorsJSON); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) Supersede(ctx context.Context, sc tenancy.Scope, previousID string) error {
	return withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE priority_rankings SET state='superseded',superseded_by=(SELECT id FROM priority_rankings WHERE tenant_id=$1 AND state='running' ORDER BY computed_at DESC LIMIT 1) WHERE id=$2 AND state='ready'`, sc.TenantID, previousID)
		return err
	})
}
func (s *Store) Expire(ctx context.Context, sc tenancy.Scope) error {
	// Expiry is represented by expires_at; preserving state keeps the run useful
	// for diagnosis while Ready explicitly filters it from dispatch.
	return withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE priority_rankings SET state='superseded' WHERE state='ready' AND expires_at<=now()`)
		return err
	})
}

type Candidate struct {
	ItemID      string          `json:"item_id"`
	Number      int64           `json:"number"`
	BaseScore   int             `json:"base_score"`
	FactorsJSON json.RawMessage `json:"factors_json"`
	Reason      string          `json:"reason"`
	Input       json.RawMessage `json:"input"`
}

// BaseScorer returns the recomputed ready set, ordered or unordered. It must be
// based on persisted tenant facts, never the previous ranking.
type BaseScorer interface {
	Score(context.Context, tenancy.Scope) ([]Candidate, error)
}

type LLMResult struct {
	Model   string
	Summary string
	Entries []LLMEntry
}
type LLMEntry struct {
	ItemID string
	Score  int
	Reason string
}
type LLMRanker interface {
	Rank(context.Context, []Candidate) (LLMResult, error)
}

type RunnerOptions struct {
	OverBudget bool
	Now        func() time.Time
	MaxItems   int
}
type Runner struct {
	store *Store
	base  BaseScorer
	llm   LLMRanker
	opts  RunnerOptions
}

func NewRunner(store *Store, base BaseScorer, llm LLMRanker, opts ...RunnerOptions) *Runner {
	r := &Runner{store: store, base: base, llm: llm, opts: RunnerOptions{Now: time.Now, MaxItems: 500}}
	if len(opts) > 0 {
		r.opts = opts[0]
		if r.opts.Now == nil {
			r.opts.Now = time.Now
		}
		if r.opts.MaxItems <= 0 {
			r.opts.MaxItems = 500
		}
	}
	if r.base == nil {
		r.base = &databaseBase{db: store.db, now: r.opts.Now}
	}
	return r
}

func (r *Runner) Run(ctx context.Context, sc tenancy.Scope) (Run, error) {
	items, err := r.base.Score(ctx, sc)
	if err != nil {
		return Run{}, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].BaseScore != items[j].BaseScore {
			return items[i].BaseScore > items[j].BaseScore
		}
		if items[i].Number != items[j].Number {
			return items[i].Number < items[j].Number
		}
		return items[i].ItemID < items[j].ItemID
	})
	fingerprint := fingerprint(items)
	now := r.opts.Now().UTC()
	roots, err := r.stalledRoots(ctx, sc)
	if err != nil {
		return Run{}, err
	}
	rootsJSON, _ := json.Marshal(roots)
	run := Run{ID: uuid.NewString(), TenantID: sc.TenantID, State: "running", WeightsVersion: priority.WeightsVersion, InputFingerprint: fingerprint, RootsJSON: rootsJSON, ComputedAt: now, ExpiresAt: now.Add(3 * time.Hour)}
	result := LLMResult{}
	degraded := ""
	switch {
	case r.opts.OverBudget:
		degraded = "over_budget"
	case r.llm == nil:
		degraded = "llm_failed"
	default:
		result, err = r.llm.Rank(ctx, items)
		if err != nil {
			degraded = "llm_failed"
		} else if !validOutput(items, result.Entries) {
			degraded = "invalid_output"
		}
	}
	entries := make([]Entry, 0, len(items))
	if degraded == "" {
		run.LLMModel = result.Model
		run.SummaryMD = result.Summary
		byID := map[string]Candidate{}
		for _, it := range items {
			byID[it.ItemID] = it
		}
		for idx, e := range result.Entries {
			it := byID[e.ItemID]
			entries = append(entries, Entry{ID: uuid.NewString(), RankingID: run.ID, ItemID: e.ItemID, Rank: idx + 1, Score: float64(e.Score), ReasonMD: e.Reason, BaseScore: float64(it.BaseScore), FactorsJSON: it.FactorsJSON})
		}
	} else {
		run.DegradedReason = degraded
		for idx, it := range items {
			score := it.BaseScore
			if score < 0 {
				score = 0
			}
			if score > 1000 {
				score = 1000
			}
			entries = append(entries, Entry{ID: uuid.NewString(), RankingID: run.ID, ItemID: it.ItemID, Rank: idx + 1, Score: float64(score), ReasonMD: it.Reason, BaseScore: float64(it.BaseScore), FactorsJSON: it.FactorsJSON})
		}
		run.SummaryMD = baseSummary(items, roots)
	}
	created, err := r.store.Create(ctx, sc, run)
	if err != nil {
		return Run{}, err
	}
	if err = r.store.AddEntries(ctx, sc, created.ID, entries); err != nil {
		_ = r.store.markFailed(ctx, sc, created.ID)
		return created, err
	}
	if err = r.store.validatePersisted(ctx, sc, created.ID, items); err != nil {
		_ = r.store.markFailed(ctx, sc, created.ID)
		created.State = "failed"
		return created, err
	}
	if err = r.store.markReady(ctx, sc, created.ID); err != nil {
		return created, err
	}
	created.State = "ready"
	if created.DegradedReason == "" {
		created.LLMModel = result.Model
	}
	created.SummaryMD = run.SummaryMD
	return created, nil
}

func validOutput(items []Candidate, entries []LLMEntry) bool {
	if len(items) != len(entries) {
		return false
	}
	valid := map[string]bool{}
	for _, it := range items {
		valid[it.ItemID] = true
	}
	seen := map[string]bool{}
	last := 1001
	for _, e := range entries {
		if !valid[e.ItemID] || seen[e.ItemID] || e.Score < 0 || e.Score > 1000 || e.Score > last || strings.TrimSpace(e.Reason) == "" {
			return false
		}
		seen[e.ItemID] = true
		last = e.Score
	}
	return len(seen) == len(valid)
}
func fingerprint(items []Candidate) string {
	copyItems := append([]Candidate(nil), items...)
	sort.Slice(copyItems, func(i, j int) bool { return copyItems[i].ItemID < copyItems[j].ItemID })
	b, _ := json.Marshal(copyItems)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func baseSummary(items []Candidate, roots []map[string]any) string {
	if len(items) == 0 {
		return "No ready items are currently competing for dispatch. The queue is clear."
	}
	first := items[0]
	return fmt.Sprintf("Item %d goes first with base score %d. The queue follows the deterministic base score; %d stalled roots need attention.", first.Number, first.BaseScore, len(roots))
}

func (s *Store) validatePersisted(ctx context.Context, sc tenancy.Scope, runID string, items []Candidate) error {
	entries, err := s.Entries(ctx, sc, runID)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, i := range items {
		want[i.ItemID] = true
	}
	if len(entries) != len(want) {
		return errors.New("ranking: validation failed: ready-set cardinality mismatch")
	}
	last := 1001
	seen := map[string]bool{}
	for n, e := range entries {
		score := int(e.Score)
		if e.Rank != n+1 || score > last || !want[e.ItemID] || seen[e.ItemID] {
			return errors.New("ranking: validation failed: invalid order or item set")
		}
		seen[e.ItemID] = true
		last = score
	}
	return nil
}
func (s *Store) markFailed(ctx context.Context, sc tenancy.Scope, id string) error {
	return withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE priority_rankings SET state='failed' WHERE id=$1 AND state='running'`, id)
		return err
	})
}
func (s *Store) markReady(ctx context.Context, sc tenancy.Scope, id string) error {
	return withTenant(ctx, s.db, sc, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE priority_rankings SET state='superseded',superseded_by=$2 WHERE tenant_id=$1 AND state='ready'`, sc.TenantID, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE priority_rankings SET state='ready' WHERE id=$1 AND state='running'`, id)
		return err
	})
}

type databaseBase struct {
	db  *sql.DB
	now func() time.Time
}

func (b *databaseBase) Score(ctx context.Context, sc tenancy.Scope) ([]Candidate, error) {
	out := []Candidate{}
	err := withTenant(ctx, b.db, sc, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `WITH RECURSIVE reach(root_id,dependent_id) AS (
			SELECT to_item_id,from_item_id FROM item_dependencies
			UNION SELECT r.root_id,d.from_item_id FROM reach r JOIN item_dependencies d ON d.to_item_id=r.dependent_id
		)
		SELECT i.id,i.number,i.status,i.priority,i.labels_json,i.created_at,
		 EXISTS(SELECT 1 FROM item_dependencies d JOIN items blocker ON blocker.id=d.to_item_id WHERE d.from_item_id=i.id AND blocker.status NOT IN ('done','cancelled')) blocked,
	 EXISTS(SELECT 1 FROM items child WHERE child.parent_id=i.id AND child.status NOT IN ('done','cancelled')) epic,
	 (SELECT count(DISTINCT dependent_id) FROM reach WHERE root_id=i.id AND dependent_id IN (SELECT id FROM items WHERE status NOT IN ('done','cancelled'))) unblocks
		FROM items i WHERE i.status NOT IN ('done','cancelled') AND i.paused_at IS NULL
		ORDER BY i.number`)
		if err != nil {
			return err
		}
		defer rows.Close()
		type row struct {
			id            string
			number        int64
			status        string
			priority      sql.NullString
			labels        []byte
			created       time.Time
			blocked, epic bool
			unblocks      int
		}
		var all []row
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.id, &x.number, &x.status, &x.priority, &x.labels, &x.created, &x.blocked, &x.epic, &x.unblocks); err != nil {
				return err
			}
			all = append(all, x)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, x := range all {
			if x.blocked || x.epic {
				continue
			}
			var labels []string
			_ = json.Unmarshal(x.labels, &labels)
			var p *string
			if x.priority.Valid {
				p = &x.priority.String
			}
			score := priority.Compute(priority.Item{ID: x.id, Number: x.number, Status: x.status, Priority: p, Labels: labels, Unblocks: x.unblocks, CreatedAt: x.created}, b.now())
			factors, _ := json.Marshal(score.Factors)
			input, _ := json.Marshal(struct {
				Priority        sql.NullString
				Labels          []string
				Blocked, IsEpic bool
				Unblocks        int
				Created         time.Time
			}{x.priority, labels, x.blocked, x.epic, x.unblocks, x.created})
			keys := make([]string, 0, len(score.Factors))
			for _, f := range score.Factors {
				if f.Contribution > 0 {
					keys = append(keys, f.Key)
				}
			}
			reason := "No positive base-score factors distinguished this item."
			if len(keys) > 0 {
				reason = "Base score favors this item for " + strings.Join(keys, ", ") + "."
			}
			out = append(out, Candidate{ItemID: x.id, Number: x.number, BaseScore: score.Value, FactorsJSON: factors, Reason: reason, Input: input})
		}
		return nil
	})
	return out, err
}

func (r *Runner) stalledRoots(ctx context.Context, sc tenancy.Scope) ([]map[string]any, error) {
	out := []map[string]any{}
	err := withTenant(ctx, r.store.db, sc, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `WITH RECURSIVE reach(root_id,dependent_id) AS (
		SELECT to_item_id,from_item_id FROM item_dependencies
		UNION SELECT r.root_id,d.from_item_id FROM reach r JOIN item_dependencies d ON d.to_item_id=r.dependent_id
	) SELECT i.id,count(DISTINCT r.dependent_id) AS unblocks,i.status FROM items i JOIN reach r ON r.root_id=i.id
	WHERE i.status NOT IN ('done','cancelled') AND r.dependent_id IN (SELECT id FROM items WHERE status NOT IN ('done','cancelled'))
	GROUP BY i.id,i.status ORDER BY unblocks DESC,i.number LIMIT 5`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, status string
			var n int
			if err := rows.Scan(&id, &n, &status); err != nil {
				return err
			}
			out = append(out, map[string]any{"item_id": id, "unblocks": n, "status": status})
		}
		return rows.Err()
	})
	return out, err
}

func withTenant(ctx context.Context, db *sql.DB, sc tenancy.Scope, fn func(*sql.Tx) error) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, sc.TenantID); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
