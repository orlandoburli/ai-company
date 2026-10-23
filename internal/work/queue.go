package work

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/orlandoburli/ai-company/internal/priority"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// Queue overrides and the deterministic score.
//
// Pause and pin are queue state, not item content: they are written only by an
// explicit human action, always attributed, and they never touch a score, a label
// or a failure counter. Eligibility is evaluated before order, so a pinned
// paused item is still paused (FR-QUE-6).

func getItemTx(ctx context.Context, tx *sql.Tx, id string) (Item, error) {
	it, err := scanItem(tx.QueryRowContext(ctx, `
		SELECT `+itemColumns+itemComputed+` FROM items i WHERE i.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	return it, err
}

// override applies a queue-state change, records it, and is idempotent: acting
// twice on the same state returns the item unchanged rather than logging a
// phantom override.
func (s *Store) override(
	ctx context.Context,
	sc tenancy.Scope,
	id, actor, action, sets string,
	args []any,
	alreadySet func(Item) bool,
) (Item, error) {
	var out Item
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		before, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if alreadySet(before) {
			out = before
			return nil
		}

		stmt := `UPDATE items SET ` + sets + `, updated_at = now() WHERE id = $1`
		if _, err := tx.ExecContext(ctx, stmt, append([]any{id}, args...)...); err != nil {
			return err
		}
		after, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		out = after
		return recordActivity(ctx, tx, sc, id, actor, action, before, after)
	})
	if err != nil {
		return Item{}, mapPGError(err)
	}
	return out, nil
}

// PauseItem removes the item from dispatch. It is not a failure: no failure
// counter moves and no wait is scheduled (FR-QUE-2). The pause never expires by
// itself.
func (s *Store) PauseItem(ctx context.Context, sc tenancy.Scope, id, reason, actor string) (Item, error) {
	return s.override(ctx, sc, id, actor, "item.paused",
		`paused_at = now(), paused_by = $2, pause_reason = nullif($3, '')`,
		[]any{actor, reason},
		func(it Item) bool { return it.PausedAt != nil })
}

// UnpauseItem restores eligibility. It does not restore a previous position: the
// item is re-scored from current inputs (FR-QUE-6).
func (s *Store) UnpauseItem(ctx context.Context, sc tenancy.Scope, id, actor string) (Item, error) {
	return s.override(ctx, sc, id, actor, "item.unpaused",
		`paused_at = NULL, paused_by = NULL, pause_reason = NULL`, nil,
		func(it Item) bool { return it.PausedAt == nil })
}

// PinItem moves the item to rank 1. Multiple pins order most-recently-pinned
// first; the pin is ordering only and never defeats eligibility.
func (s *Store) PinItem(ctx context.Context, sc tenancy.Scope, id, actor string) (Item, error) {
	return s.override(ctx, sc, id, actor, "item.pinned",
		`pinned_at = now(), pinned_by = $2`, []any{actor},
		func(it Item) bool { return it.PinnedAt != nil })
}

// UnpinItem clears the override; the item returns to whatever position its score
// earns on the next ranking run.
func (s *Store) UnpinItem(ctx context.Context, sc tenancy.Scope, id, actor string) (Item, error) {
	return s.override(ctx, sc, id, actor, "item.unpinned",
		`pinned_at = NULL, pinned_by = NULL`, nil,
		func(it Item) bool { return it.PinnedAt == nil })
}

// QueueEntry is one scored, eligible queue item with the item attached.
type QueueEntry struct {
	Item           Item           `json:"item"`
	Score          priority.Score `json:"score"`
	Pinned         bool           `json:"pinned"`
	PinnedAt       *time.Time     `json:"pinned_at,omitempty"`
	Paused         bool           `json:"paused"`
	Rank           *int           `json:"rank,omitempty"`
	FailureState   string         `json:"failure_state,omitempty"`
	NextEligibleAt *time.Time     `json:"next_eligible_at,omitempty"`
	StartNowAt     *time.Time     `json:"start_now_at,omitempty"`
}

// Queue returns eligible items in dispatch order: pinned first (most recent
// first), then base score descending. Ineligible items are excluded, because
// including them would suggest a pin or a score could start them.
func (s *Store) Queue(ctx context.Context, sc tenancy.Scope, f ItemFilter) ([]QueueEntry, error) {
	var out []QueueEntry
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		items, err := listItemsTx(ctx, tx, f)
		if err != nil {
			return err
		}
		unblocks, err := unblockCounts(ctx, tx, "")
		if err != nil {
			return err
		}

		byID := make(map[string]Item, len(items))
		for _, it := range items {
			byID[it.ID] = it
		}

		inputs := make([]priority.Item, 0, len(items))
		for _, it := range items {
			inputs = append(inputs, priority.Item{
				ID:        it.ID,
				Number:    it.Number,
				Status:    it.Status,
				Priority:  effectivePriority(it, byID),
				Labels:    labelsOf(it),
				Unblocks:  unblocks[it.ID],
				CreatedAt: it.CreatedAt,
				Paused:    it.Paused,
				Blocked:   it.Blocked,
				IsEpic:    it.IsEpic,
				PinnedAt:  it.PinnedAt,
			})
		}

		entries := priority.Queue(inputs, time.Now())
		ranks := map[string]int{}
		var runID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM priority_rankings WHERE state='ready' AND expires_at>now() ORDER BY computed_at DESC LIMIT 1`).Scan(&runID); err == nil {
			rows, err := tx.QueryContext(ctx, `SELECT item_id,rank FROM priority_ranking_entries WHERE ranking_id=$1`, runID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				var rank int
				if err := rows.Scan(&id, &rank); err != nil {
					rows.Close()
					return err
				}
				ranks[id] = rank
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		fails := map[string]struct {
			state string
			until *time.Time
		}{}
		rows, err := tx.QueryContext(ctx, `SELECT item_id, CASE WHEN bool_or(state='exhausted') THEN 'exhausted' WHEN bool_or(state='backing_off') THEN 'backing_off' ELSE 'ok' END, max(next_eligible_at) FILTER (WHERE state='backing_off') FROM item_failures GROUP BY item_id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, state string
			var until *time.Time
			if err := rows.Scan(&id, &state, &until); err != nil {
				rows.Close()
				return err
			}
			fails[id] = struct {
				state string
				until *time.Time
			}{state, until}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		startNow := map[string]*time.Time{}
		rows, err = tx.QueryContext(ctx, `SELECT DISTINCT ON (item_id) item_id,created_at FROM activity_log WHERE action='item.start_now' ORDER BY item_id,created_at DESC`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var at time.Time
			if err := rows.Scan(&id, &at); err != nil {
				rows.Close()
				return err
			}
			startNow[id] = &at
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		out = make([]QueueEntry, 0, len(entries))
		for _, e := range entries {
			f := fails[e.ItemID]
			if f.state == "exhausted" || (f.state == "backing_off" && f.until != nil && f.until.After(time.Now())) {
				continue
			}
			entry := QueueEntry{Item: byID[e.ItemID], Score: e.Score, Pinned: e.Pinned, PinnedAt: e.PinnedAt, Paused: e.Paused, StartNowAt: startNow[e.ItemID]}
			if rank, ok := ranks[e.ItemID]; ok {
				entry.Rank = &rank
			}
			if f.state != "" {
				entry.FailureState = f.state
			}
			entry.NextEligibleAt = f.until
			out = append(out, entry)
		}
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if (a.StartNowAt != nil) != (b.StartNowAt != nil) {
				return a.StartNowAt != nil
			}
			if a.StartNowAt != nil && !a.StartNowAt.Equal(*b.StartNowAt) {
				return a.StartNowAt.After(*b.StartNowAt)
			}
			if (a.PinnedAt != nil) != (b.PinnedAt != nil) {
				return a.PinnedAt != nil
			}
			if a.PinnedAt != nil && !a.PinnedAt.Equal(*b.PinnedAt) {
				return a.PinnedAt.After(*b.PinnedAt)
			}
			if a.Rank != nil && b.Rank != nil && *a.Rank != *b.Rank {
				return *a.Rank < *b.Rank
			}
			if (a.Rank != nil) != (b.Rank != nil) {
				return a.Rank != nil
			}
			if a.Score.Value != b.Score.Value {
				return a.Score.Value > b.Score.Value
			}
			if a.Item.Number != b.Item.Number {
				return a.Item.Number < b.Item.Number
			}
			return a.Item.ID < b.Item.ID
		})
		return nil
	})
	return out, err
}

var (
	ErrNotDispatchable = errors.New("work: item is not dispatchable")
	ErrExhausted       = errors.New("work: item is exhausted")
)

// StartNow records a human ordering override after checking the same eligibility
// gate as the normal queue. It never changes the failure counter or item fields.
func (s *Store) StartNow(ctx context.Context, sc tenancy.Scope, id, actor string) (Item, error) {
	var item Item
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		item, err = scanItem(tx.QueryRowContext(ctx, `SELECT `+itemColumns+itemComputed+` FROM items i WHERE i.id=$1`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if item.Status == "done" || item.Status == "cancelled" || item.Paused || item.Blocked || item.IsEpic {
			return ErrNotDispatchable
		}
		var state string
		err = tx.QueryRowContext(ctx, `SELECT CASE WHEN bool_or(state='exhausted') THEN 'exhausted' WHEN bool_or(state='backing_off') THEN 'backing_off' ELSE 'ok' END FROM item_failures WHERE item_id=$1`, id).Scan(&state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state == "exhausted" {
			return ErrExhausted
		}
		var next sql.NullTime
		if state == "backing_off" {
			if err := tx.QueryRowContext(ctx, `SELECT max(next_eligible_at) FROM item_failures WHERE item_id=$1 AND state='backing_off'`, id).Scan(&next); err != nil {
				return err
			}
		}
		if state == "backing_off" && next.Valid && next.Time.After(time.Now()) {
			return ErrNotDispatchable
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO activity_log(id,tenant_id,item_id,actor_kind,actor_user_id,action,after_json) VALUES($1,$2,$3,'user',nullif($4,''),'item.start_now','{}'::jsonb)`, newID(), sc.TenantID, id, actor)
		return err
	})
	return item, mapPGError(err)
}

// ScoreItem computes an item's base score and breakdown without requiring it to
// be eligible. The breakdown is the audit trail: the UI explains the number from
// these factors rather than recomputing it (FR-PRI-4).
func (s *Store) ScoreItem(ctx context.Context, sc tenancy.Scope, id string) (priority.Score, Item, error) {
	var (
		score priority.Score
		item  Item
	)
	err := s.withTenant(ctx, sc, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		item, err = getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}

		// The inheritance walk needs the ancestors; a bounded recursive query
		// keeps it to one round trip instead of climbing in Go.
		inherited := item.Priority
		if inherited == nil {
			err = tx.QueryRowContext(ctx, `
				WITH RECURSIVE chain AS (
					SELECT i.id, i.parent_id, i.priority, 1 AS depth
					  FROM items i WHERE i.id = $1
					UNION ALL
					SELECT p.id, p.parent_id, p.priority, c.depth + 1
					  FROM items p JOIN chain c ON p.id = c.parent_id
					 WHERE c.depth < 32
				)
				SELECT priority FROM chain
				 WHERE priority IS NOT NULL AND depth > 1
				 ORDER BY depth LIMIT 1`, id).Scan(&inherited)
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
				inherited = nil
			}
			if err != nil {
				return err
			}
		}

		unblocks, err := unblockCounts(ctx, tx, id)
		if err != nil {
			return err
		}

		score = priority.Compute(priority.Item{
			ID:        item.ID,
			Number:    item.Number,
			Status:    item.Status,
			Priority:  inherited,
			Labels:    labelsOf(item),
			Unblocks:  unblocks[item.ID],
			CreatedAt: item.CreatedAt,
			Paused:    item.Paused,
			Blocked:   item.Blocked,
			IsEpic:    item.IsEpic,
			PinnedAt:  item.PinnedAt,
		}, time.Now())
		return nil
	})
	return score, item, err
}

// effectivePriority walks the ancestor chain within an already-loaded set,
// returning the nearest inherited label when the item has none. An explicit
// label always wins (FR-PRI-1).
func effectivePriority(it Item, byID map[string]Item) *string {
	if it.Priority != nil {
		return it.Priority
	}
	seen := map[string]bool{it.ID: true}
	cur := it
	for depth := 0; depth < 32; depth++ {
		if cur.ParentID == nil {
			return nil
		}
		parent, ok := byID[*cur.ParentID]
		if !ok || seen[parent.ID] {
			return nil
		}
		if parent.Priority != nil {
			return parent.Priority
		}
		seen[parent.ID] = true
		cur = parent
	}
	return nil
}

// unblockCounts returns, per item, how many open items wait on it transitively.
// The walk is over persisted edges with UNION (not UNION ALL), so it terminates
// even if a cycle exists in data written outside the API.
func unblockCounts(ctx context.Context, tx *sql.Tx, rootID string) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE reach(root_id, dependent_id) AS (
			SELECT d.to_item_id, d.from_item_id
			  FROM item_dependencies d
			 WHERE ($1 = '' OR d.to_item_id = $1)
			UNION
			SELECT r.root_id, d.from_item_id
			  FROM reach r
			  JOIN item_dependencies d ON d.to_item_id = r.dependent_id
		)
		SELECT r.root_id, count(DISTINCT r.dependent_id)
		  FROM reach r
		  JOIN items dep ON dep.id = r.dependent_id
		 WHERE dep.status NOT IN ('done', 'cancelled')
		 GROUP BY r.root_id`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var (
			id    string
			count int
		)
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		out[id] = count
	}
	return out, rows.Err()
}

func labelsOf(it Item) []string {
	return it.Labels
}
