// Package priority implements the deterministic base score (FR-PRI-3) and the
// queue ordering that sits on top of it (FR-QUE-1..7).
//
// It is deliberately pure: no database, no clock of its own, no LLM. The same
// inputs under the same weights version always produce the same score, which is
// what makes the number reproducible and the breakdown auditable. The ranker is
// the authority over order at higher milestones; this package is both its input
// and its fallback, so it must never depend on the ranker.
package priority

import (
	"sort"
	"time"
)

// WeightsVersion names the weight set. It is persisted with every score so an
// old number can be explained by the weights that produced it, not today's.
const WeightsVersion = "pri-base-1"

// The base-score weights (FR-PRI-3). They are plain constants rather than
// configuration because changing them is a version bump, not a setting.
const (
	WeightP0      = 100
	WeightP1      = 50
	WeightP2      = 10
	WeightBug     = 30
	WeightUnblock = 25
	WeightOpenPR  = 20
	WeightAgeDay  = 1
	WeightAgeCap  = 30
)

// Item is the scoring input. Everything here is a database fact; untrusted text
// (title, description) is intentionally absent, because the base score must not
// be steerable by what an item says about itself.
type Item struct {
	ID       string
	Number   int64
	Status   string
	Priority *string // effective priority, after inheritance
	Labels   []string
	Unblocks int  // open items that wait on this one, transitively
	OpenPR   *bool // nil when there is no PR signal at all
	CreatedAt time.Time
	Paused    bool
	Blocked   bool
	IsEpic    bool
	PinnedAt  *time.Time
	PauseReason string
}

// Factor is one line of the breakdown: the input as scored, the weight applied,
// and the contribution. Contribution is always input-derived, never invented.
type Factor struct {
	Key          string `json:"key"`
	Input        string `json:"input"`
	Weight       int    `json:"weight"`
	Contribution int    `json:"contribution"`
}

// Score is a reproducible base score and its full breakdown.
type Score struct {
	Value          int      `json:"base_score"`
	WeightsVersion string   `json:"weights_version"`
	Factors        []Factor `json:"factors"`
}

// Compute returns the base score for one item. Factors that have no signal are
// omitted rather than recorded as zero, so "absent from the breakdown" keeps its
// documented meaning: it was not scored (FR-PRI-4).
func Compute(it Item, now time.Time) Score {
	factors := make([]Factor, 0, 5)
	total := 0

	add := func(key string, input string, weight, contribution int) {
		factors = append(factors, Factor{Key: key, Input: input, Weight: weight, Contribution: contribution})
		total += contribution
	}

	switch {
	case it.Priority != nil && *it.Priority == "priority:p0":
		add("priority", "priority:p0", WeightP0, WeightP0)
	case it.Priority != nil && *it.Priority == "priority:p1":
		add("priority", "priority:p1", WeightP1, WeightP1)
	case it.Priority != nil && *it.Priority == "priority:p2":
		add("priority", "priority:p2", WeightP2, WeightP2)
	default:
		add("priority", "none", 0, 0)
	}

	hasBug := false
	for _, l := range it.Labels {
		if l == "bug" {
			hasBug = true
			break
		}
	}
	if hasBug {
		add("bug", "bug", WeightBug, WeightBug)
	} else {
		add("bug", "no bug label", 0, 0)
	}

	add("unblocks", itoa(it.Unblocks), WeightUnblock, it.Unblocks*WeightUnblock)

	if it.OpenPR != nil && *it.OpenPR {
		add("open_pr", "open", WeightOpenPR, WeightOpenPR)
	} else if it.OpenPR != nil {
		add("open_pr", "none", 0, 0)
	}

	days := int(now.Sub(it.CreatedAt).Hours() / 24)
	if days < 0 {
		days = 0
	}
	contribution := days * WeightAgeDay
	if contribution > WeightAgeCap {
		contribution = WeightAgeCap
	}
	add("age", itoa(days)+" days", WeightAgeDay, contribution)

	return Score{Value: total, WeightsVersion: WeightsVersion, Factors: factors}
}

// IneligibleReason explains why an item cannot be dispatched. Empty means ready.
// Eligibility is checked before ordering and is never overridden by a pin or a
// score (FR-QUE-6).
func IneligibleReason(it Item) string {
	switch {
	case it.Status == "done" || it.Status == "cancelled":
		return "closed"
	case it.Paused:
		return "paused"
	case it.Blocked:
		return "blocked"
	case it.IsEpic:
		return "epic"
	}
	return ""
}

// Entry is one line of the queue: score, breakdown, override state and whether
// it is actually eligible.
type Entry struct {
	ItemID           string     `json:"item_id"`
	Number           int64      `json:"number"`
	Score            Score      `json:"score"`
	Pinned           bool       `json:"pinned"`
	PinnedAt         *time.Time `json:"pinned_at,omitempty"`
	Paused           bool       `json:"paused"`
	IneligibleReason string     `json:"ineligible_reason,omitempty"`
}

// Queue orders the eligible items. Precedence is pinned first (most recently
// pinned first), then base score descending, then item number as a stable
// tiebreak. Ineligible items are excluded entirely: pinning a paused or blocked
// item changes nothing about dispatch, so it must not appear as if it had.
func Queue(items []Item, now time.Time) []Entry {
	out := make([]Entry, 0, len(items))
	for _, it := range items {
		if IneligibleReason(it) != "" {
			continue
		}
		out = append(out, Entry{
			ItemID:   it.ID,
			Number:   it.Number,
			Score:    Compute(it, now),
			Pinned:   it.PinnedAt != nil,
			PinnedAt: it.PinnedAt,
			Paused:   it.Paused,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.PinnedAt != nil) != (b.PinnedAt != nil) {
			return a.PinnedAt != nil
		}
		if a.PinnedAt != nil && !a.PinnedAt.Equal(*b.PinnedAt) {
			return a.PinnedAt.After(*b.PinnedAt)
		}
		if a.Score.Value != b.Score.Value {
			return a.Score.Value > b.Score.Value
		}
		if a.Number != b.Number {
			return a.Number < b.Number
		}
		return a.ItemID < b.ItemID
	})
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
