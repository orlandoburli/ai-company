package failure_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/failure"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

func TestFailureStateMachinePauseIsolationAndTenantScope(t *testing.T) {
	db := pgtest.FreshDB(t)
	seedFailureFixture(t, db.DB)
	app := db.AsRole(t, "aic_app")
	store := failure.NewStore(app)
	sc := tenancy.Scope{TenantID: "t-failure", UserID: "u-failure", Role: tenancy.RoleMember}
	ctx := context.Background()

	var before time.Time
	if err := db.QueryRow(`SELECT paused_at FROM items WHERE id='i-failure'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	params := failure.Params{XSeconds: 1, Y: 2, StepSeconds: 1, MaxBackoffSeconds: 10, MaxFailures: 2}
	first, err := store.Record(ctx, sc, failure.Record{ItemID: "i-failure", WorkflowID: "wf-failure", Curve: failure.CurveExponential, Params: params, Version: "test-1", Kind: "transient"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ConsecutiveFailures != 1 || first.State != "backing_off" || first.NextEligibleAt == nil {
		t.Fatalf("first failure state = %+v", first)
	}
	var after time.Time
	if err := db.QueryRow(`SELECT paused_at FROM items WHERE id='i-failure'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) {
		t.Fatalf("failure changed pause timestamp: %v -> %v", before, after)
	}

	if err := store.Succeed(ctx, sc, "i-failure", "wf-failure"); err != nil {
		t.Fatal(err)
	}
	reset, found, err := store.State(ctx, sc, "i-failure", "wf-failure")
	if err != nil || !found || reset.State != "ok" || reset.ConsecutiveFailures != 0 {
		t.Fatalf("success reset = %+v, found=%v err=%v", reset, found, err)
	}
	_, err = store.Record(ctx, sc, failure.Record{ItemID: "i-failure", WorkflowID: "wf-failure", Curve: failure.CurveExponential, Params: params, Version: "test-1", Kind: "transient"})
	if err != nil {
		t.Fatal(err)
	}
	exhausted, err := store.Record(ctx, sc, failure.Record{ItemID: "i-failure", WorkflowID: "wf-failure", Curve: failure.CurveExponential, Params: params, Version: "test-1", Kind: "transient"})
	if err != nil {
		t.Fatal(err)
	}
	if exhausted.State != "exhausted" || exhausted.ExhaustedAt == nil || exhausted.NextEligibleAt != nil {
		t.Fatalf("terminal state = %+v", exhausted)
	}
	if eligible, _, err := store.Eligible(ctx, sc, "i-failure", "wf-failure"); err != nil || eligible {
		t.Fatalf("exhausted eligible=%v err=%v", eligible, err)
	}
	rearmed, err := store.Reset(ctx, sc, "i-failure", "wf-failure", "u-failure")
	if err != nil {
		t.Fatal(err)
	}
	if rearmed.State != "ok" || rearmed.ConsecutiveFailures != 0 || rearmed.ExhaustedAt != nil {
		t.Fatalf("reset state = %+v", rearmed)
	}

	other := tenancy.Scope{TenantID: "t-other", Role: tenancy.RoleOwner}
	if _, found, err := store.State(ctx, other, "i-failure", "wf-failure"); err != nil || found {
		t.Fatalf("cross-tenant state found=%v err=%v", found, err)
	}
}

func seedFailureFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	// Seed with raw SQL through the owner connection; application operations below
	// still run under aic_app with FORCE RLS and a transaction-local scope.
	queries := []string{
		`INSERT INTO tenants(id,name,slug) VALUES('t-failure','Failure test','failure-test'),('t-other','Other tenant','other-test')`,
		`INSERT INTO users(id,email,password_hash,name) VALUES('u-failure','failure@example.test','unused','Failure Test')`,
		`INSERT INTO products(id,tenant_id,name,slug) VALUES('p-failure','t-failure','Product','product')`,
		`INSERT INTO projects(id,tenant_id,product_id,name) VALUES('pr-failure','t-failure','p-failure','Project')`,
		`INSERT INTO items(id,tenant_id,project_id,number,kind,title,paused_at,paused_by) VALUES('i-failure','t-failure','pr-failure',1,'bug','Paused item',now(),'u-failure')`,
		`INSERT INTO workflow_templates(id,tenant_id,name,graph_json) VALUES('wf-failure','t-failure','Workflow','{}'::jsonb)`,
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}
}
