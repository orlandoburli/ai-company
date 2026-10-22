package priority_test

import (
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/priority"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func p(s string) *string { return &s }

func TestBaseScoreIsTheSumOfItsFactors(t *testing.T) {
	open := true
	it := priority.Item{
		ID:        "I1",
		Number:    1,
		Priority:  p("priority:p0"),
		Labels:    []string{"bug"},
		Unblocks:  2,
		OpenPR:    &open,
		CreatedAt: now.AddDate(0, 0, -10),
	}

	got := priority.Compute(it, now)

	// 100 (p0) + 30 (bug) + 50 (2 open dependents x25) + 20 (open PR) + 10 (age)
	if got.Value != 210 {
		t.Fatalf("score = %d; want 210", got.Value)
	}
	if got.WeightsVersion != priority.WeightsVersion {
		t.Fatalf("weights version = %q", got.WeightsVersion)
	}

	sum := 0
	for _, f := range got.Factors {
		sum += f.Contribution
	}
	if sum != got.Value {
		t.Fatalf("factor contributions sum to %d, score is %d", sum, got.Value)
	}

	// Every factor is explainable: input, weight and contribution are present.
	for _, f := range got.Factors {
		if f.Key == "" || f.Input == "" {
			t.Fatalf("factor missing input: %+v", f)
		}
	}
}

func TestAgeContributionIsCapped(t *testing.T) {
	it := priority.Item{Priority: p("priority:p2"), CreatedAt: now.AddDate(0, 0, -400)}
	got := priority.Compute(it, now)

	var age int
	for _, f := range got.Factors {
		if f.Key == "age" {
			age = f.Contribution
		}
	}
	if age != priority.WeightAgeCap {
		t.Fatalf("age contribution = %d; want cap %d", age, priority.WeightAgeCap)
	}
}

func TestAbsentPRSignalIsNotScored(t *testing.T) {
	it := priority.Item{CreatedAt: now}
	got := priority.Compute(it, now)

	for _, f := range got.Factors {
		if f.Key == "open_pr" {
			t.Fatalf("open_pr factor present without a PR signal: %+v", f)
		}
	}
	if got.Value != 0 {
		t.Fatalf("score = %d; want 0", got.Value)
	}
}

func TestQueueOrdersPinsThenScoreAndExcludesIneligible(t *testing.T) {
	t1 := now.Add(-time.Hour)
	t2 := now.Add(-time.Minute)

	items := []priority.Item{
		{ID: "high", Number: 1, Priority: p("priority:p0"), CreatedAt: now},
		{ID: "low", Number: 2, Priority: p("priority:p2"), CreatedAt: now},
		{ID: "pinned-old", Number: 3, CreatedAt: now, PinnedAt: &t1},
		{ID: "pinned-new", Number: 4, CreatedAt: now, PinnedAt: &t2},
		{ID: "paused", Number: 5, Priority: p("priority:p0"), CreatedAt: now, Paused: true, PinnedAt: &t2},
		{ID: "blocked", Number: 6, Priority: p("priority:p0"), CreatedAt: now, Blocked: true, PinnedAt: &t2},
		{ID: "epic", Number: 7, Priority: p("priority:p0"), CreatedAt: now, IsEpic: true},
		{ID: "done", Number: 8, Priority: p("priority:p0"), CreatedAt: now, Status: "done", PinnedAt: &t2},
	}

	got := priority.Queue(items, now)
	order := make([]string, 0, len(got))
	for _, e := range got {
		order = append(order, e.ItemID)
	}

	want := []string{"pinned-new", "pinned-old", "high", "low"}
	if len(order) != len(want) {
		t.Fatalf("queue = %v; want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("queue = %v; want %v", order, want)
		}
	}
}

func TestPausedOutranksPinned(t *testing.T) {
	at := now.Add(-time.Minute)
	it := priority.Item{ID: "I", Status: "todo", Paused: true, PinnedAt: &at}
	if got := priority.IneligibleReason(it); got != "paused" {
		t.Fatalf("reason = %q; want paused", got)
	}
}
