package ranking_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/ranking"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type nonsenseRanker struct{}

func (nonsenseRanker) Rank(_ context.Context, items []ranking.Candidate) (ranking.LLMResult, error) {
	entries := make([]ranking.LLMEntry, 0, len(items))
	for _, it := range items {
		entries = append(entries, ranking.LLMEntry{ItemID: it.ItemID, Score: 1001, Reason: "Invalid score."})
	}
	return ranking.LLMResult{Model: "test-model", Entries: entries}, nil
}

func TestRunnerDegradationSupersedeAndStalledRoots(t *testing.T) {
	db := pgtest.FreshDB(t)
	seedRankingFixture(t, db.DB)
	app := db.AsRole(t, "aic_app")
	sc := tenancy.Scope{TenantID: "t-ranking", Role: tenancy.RoleOwner}
	store := ranking.NewStore(app)
	first, err := ranking.NewRunner(store, nil, nil).Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "ready" || first.DegradedReason != "llm_failed" || first.LLMModel != "" {
		t.Fatalf("absent-model run = %+v", first)
	}
	if first.WeightsVersion != "pri-base-1" || first.InputFingerprint == "" || first.ExpiresAt.Sub(first.ComputedAt) != 3*time.Hour {
		t.Fatalf("run metadata = %+v", first)
	}
	entries, err := store.Entries(context.Background(), sc, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ItemID != "i-root" || entries[0].Rank != 1 || entries[0].ReasonMD == "" {
		t.Fatalf("base entries = %+v", entries)
	}
	var roots []map[string]any
	if err := json.Unmarshal(first.RootsJSON, &roots); err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0]["item_id"] != "i-root" || roots[0]["unblocks"] != float64(1) {
		t.Fatalf("stalled roots = %s", first.RootsJSON)
	}

	second, err := ranking.NewRunner(store, nil, nonsenseRanker{}).Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != "ready" || second.DegradedReason != "invalid_output" || second.LLMModel != "" {
		t.Fatalf("invalid-model run = %+v", second)
	}
	old, found, err := store.Get(context.Background(), sc, first.ID)
	if err != nil || !found {
		t.Fatalf("get superseded run: found=%v err=%v", found, err)
	}
	if old.State != "superseded" || old.SupersededBy == nil || *old.SupersededBy != second.ID {
		t.Fatalf("prior run not superseded: %+v", old)
	}
	current, found, err := store.Ready(context.Background(), sc)
	if err != nil || !found || current.ID != second.ID {
		t.Fatalf("ready run=%+v found=%v err=%v", current, found, err)
	}

	// Expiring a ready run removes it from the dispatch lookup and retains it for
	// diagnosis as a superseded row.
	if _, err := db.Exec(`UPDATE priority_rankings SET expires_at=now()-interval '1 second' WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Ready(context.Background(), sc); err != nil || found {
		t.Fatalf("expired run usable: found=%v err=%v", found, err)
	}
}

func seedRankingFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	queries := []string{
		`INSERT INTO tenants(id,name,slug) VALUES('t-ranking','Ranking test','ranking-test')`,
		`INSERT INTO products(id,tenant_id,name,slug) VALUES('p-ranking','t-ranking','Product','product')`,
		`INSERT INTO projects(id,tenant_id,product_id,name) VALUES('pr-ranking','t-ranking','p-ranking','Project')`,
		`INSERT INTO items(id,tenant_id,project_id,number,kind,title,created_at) VALUES('i-root','t-ranking','pr-ranking',1,'bug','Root',now()-interval '1 day'),('i-dependent','t-ranking','pr-ranking',2,'bug','Dependent',now()-interval '1 day')`,
		`INSERT INTO item_dependencies(id,tenant_id,from_item_id,to_item_id,kind) VALUES('dep-ranking','t-ranking','i-dependent','i-root','blocked_by')`,
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed ranking fixture: %v", err)
		}
	}
}
