package memory_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type fixture struct {
	app *sql.DB
	st  *memory.Store
	t1  tenancy.Scope
	t2  tenancy.Scope
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	app.SetMaxOpenConns(6)
	for _, id := range []string{"T1", "T2"} {
		if _, err := app.Exec(`SELECT auth_register_tenant($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, "Tenant "+id, strings.ToLower(id), "U-"+id, strings.ToLower(id)+"@example.com",
			"x", "User "+id, "M-"+id); err != nil {
			t.Fatalf("seed tenant %s: %v", id, err)
		}
	}
	return &fixture{
		app: app,
		st:  memory.NewStore(app),
		t1:  tenancy.Scope{TenantID: "T1", UserID: "U-T1", Role: tenancy.RoleOwner},
		t2:  tenancy.Scope{TenantID: "T2", UserID: "U-T2", Role: tenancy.RoleOwner},
	}
}

func testWrite(scope memory.Scope, key, text string) memory.Write {
	return memory.Write{Scope: scope, Key: key, ContentMD: text, Source: memory.SourceIntegration, Tags: []string{"test"}, ContentJSON: json.RawMessage(`{"ok":true}`)}
}

func TestWriteReadEachScopeAndActivity(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	scopes := []memory.Scope{
		{Kind: memory.ScopeRun, Ref: "run-1"},
		{Kind: memory.ScopeItem, Ref: "item-1"},
		{Kind: memory.ScopeAgent, Ref: "agent-1"},
		{Kind: memory.ScopeWorkspace, Ref: "product-1"},
	}
	for i, scope := range scopes {
		written, err := f.st.Write(ctx, f.t1, testWrite(scope, fmt.Sprintf("key-%d", i), "hello memory"))
		if err != nil {
			t.Fatalf("write %s: %v", scope.Kind, err)
		}
		if written.TokenEstimate != 3 {
			t.Errorf("token estimate = %d, want 3", written.TokenEstimate)
		}
		entries, watermark, err := f.st.Read(ctx, f.t1, scope)
		if err != nil || len(entries) != 1 || entries[0].ID != written.ID || watermark != written.Seq {
			t.Fatalf("read %s = (%v, %d, %v), want written row and watermark %d", scope.Kind, entries, watermark, err, written.Seq)
		}
	}
	var activityCount int
	tx, err := f.app.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, f.t1.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM activity_log WHERE action = 'memory.write'`).Scan(&activityCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if activityCount != len(scopes) {
		t.Fatalf("activity rows = %d, want %d", activityCount, len(scopes))
	}
}

func TestRevisionWatermarkHistoryAndSequence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeItem, Ref: "item-revisions"}
	first, err := f.st.Write(ctx, f.t1, testWrite(scope, "decision", "first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.st.Write(ctx, f.t1, testWrite(scope, "decision", "second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || second.Revision != 2 || second.Seq <= first.Seq || first.SupersededBy != "" {
		t.Fatalf("bad returned revisions: first=%+v second=%+v", first, second)
	}
	current, watermark, err := f.st.Read(ctx, f.t1, scope)
	if err != nil || len(current) != 1 || current[0].Revision != 2 || watermark != second.Seq {
		t.Fatalf("current read: entries=%+v watermark=%d err=%v", current, watermark, err)
	}
	history, err := f.st.History(ctx, f.t1, scope, "decision")
	if err != nil || len(history) != 2 || history[0].Revision != 2 || history[1].Revision != 1 || history[1].SupersededBy != second.ID {
		t.Fatalf("history: %+v err=%v", history, err)
	}
	at, err := f.st.ReadAt(ctx, f.t1, scope, first.Seq)
	if err != nil || len(at) != 1 || at[0].ID != first.ID {
		t.Fatalf("read at first watermark: %+v err=%v", at, err)
	}
	third, err := f.st.Write(ctx, f.t1, testWrite(scope, "another", "third"))
	if err != nil || third.Seq <= second.Seq {
		t.Fatalf("seq not increasing: third=%+v err=%v", third, err)
	}
	entries, watermark, err := f.st.Read(ctx, f.t1, scope)
	if err != nil || watermark != third.Seq || entries[len(entries)-1].Seq != watermark {
		t.Fatalf("watermark=%d entries=%+v err=%v", watermark, entries, err)
	}
}

func TestConcurrentWritesAllocateUniqueRevisions(t *testing.T) {
	f := newFixture(t)
	const writers = 10
	scope := memory.Scope{Kind: memory.ScopeAgent, Ref: "agent-concurrent"}
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.st.Write(context.Background(), f.t1, testWrite(scope, "shared", fmt.Sprintf("write-%d", i)))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	history, err := f.st.History(context.Background(), f.t1, scope, "shared")
	if err != nil || len(history) != writers {
		t.Fatalf("history length=%d err=%v", len(history), err)
	}
	seen := map[int]bool{}
	for _, e := range history {
		if seen[e.Revision] {
			t.Fatalf("duplicate revision %d", e.Revision)
		}
		seen[e.Revision] = true
	}
	for rev := 1; rev <= writers; rev++ {
		if !seen[rev] {
			t.Fatalf("missing revision %d", rev)
		}
	}
}

func TestWriteBatchIsAtomicAndReadAllKeepsCallerScopeOrder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	firstScope := memory.Scope{Kind: memory.ScopeItem, Ref: "batch-a"}
	secondScope := memory.Scope{Kind: memory.ScopeItem, Ref: "batch-b"}
	valid := testWrite(firstScope, "first", "first")
	invalid := testWrite(secondScope, "second", "second")
	invalid.SourceRunID = "missing-run"
	if _, err := f.st.WriteBatch(ctx, f.t1, []memory.Write{valid, invalid}); err == nil {
		t.Fatal("batch with an invalid FK unexpectedly succeeded")
	}
	for _, scope := range []memory.Scope{firstScope, secondScope} {
		entries, _, err := f.st.Read(ctx, f.t1, scope)
		if err != nil || len(entries) != 0 {
			t.Fatalf("partial batch persisted in %+v: entries=%+v err=%v", scope, entries, err)
		}
	}
	if _, err := f.st.WriteBatch(ctx, f.t1, []memory.Write{valid, testWrite(secondScope, "second", "second")}); err != nil {
		t.Fatal(err)
	}
	all, err := f.st.ReadAll(ctx, f.t1, []memory.Scope{secondScope, firstScope})
	if err != nil || len(all[memory.ScopeItem]) != 2 || all[memory.ScopeItem][0].Key != "second" || all[memory.ScopeItem][1].Key != "first" {
		t.Fatalf("ReadAll did not retain supplied scope order: %+v err=%v", all, err)
	}
}

func TestTenantIsolation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeWorkspace, Ref: "shared-ref"}
	entry, err := f.st.Write(ctx, f.t1, testWrite(scope, "secret", "tenant one"))
	if err != nil {
		t.Fatal(err)
	}
	entries, _, err := f.st.Read(ctx, f.t2, scope)
	if err != nil || len(entries) != 0 {
		t.Fatalf("tenant two read entries=%+v err=%v", entries, err)
	}
	if _, err := f.st.Get(ctx, f.t2, entry.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("tenant two get err=%v, want ErrNotFound", err)
	}
	// Composite provenance foreign keys prevent a tenant from attaching another
	// tenant's run to an otherwise tenant-local memory write.
	tx, err := f.app.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, f.t1.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workflow_templates (id, tenant_id, name, graph_json) VALUES ('TEMPLATE-T1', 'T1', 'test', '{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workflow_runs (id, tenant_id, template_id, template_version, definition_snapshot_json) VALUES ('RUN-T1', 'T1', 'TEMPLATE-T1', 1, '{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	w := testWrite(scope, "cross-run", "blocked")
	w.SourceRunID = "RUN-T1"
	if _, err := f.st.Write(ctx, f.t2, w); err == nil {
		t.Fatal("cross-tenant source run write unexpectedly succeeded")
	}
}

func TestHumanRunScopeRefused(t *testing.T) {
	f := newFixture(t)
	_, err := f.st.WriteHuman(context.Background(), f.t1, testWrite(memory.Scope{Kind: memory.ScopeRun, Ref: "run"}, "k", "v"))
	if !errors.Is(err, memory.ErrRunScopeReadonly) {
		t.Fatalf("WriteHuman error = %v, want ErrRunScopeReadonly", err)
	}
}

func TestInvalidScopeAndKey(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, scope := range []memory.Scope{{Kind: "unknown", Ref: "x"}, {Kind: memory.ScopeItem}, {Kind: "", Ref: "x"}} {
		if _, err := f.st.Write(ctx, f.t1, testWrite(scope, "key", "value")); !errors.Is(err, memory.ErrInvalidScope) {
			t.Errorf("scope %+v error=%v, want ErrInvalidScope", scope, err)
		}
	}
	if _, err := f.st.Write(ctx, f.t1, testWrite(memory.Scope{Kind: memory.ScopeItem, Ref: "x"}, "", "value")); !errors.Is(err, memory.ErrInvalidKey) {
		t.Fatalf("empty key error=%v, want ErrInvalidKey", err)
	}
	entries, _, err := f.st.Read(ctx, f.t1, memory.Scope{Kind: memory.ScopeItem, Ref: "x"})
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid writes persisted: entries=%+v err=%v", entries, err)
	}
}

func TestDeleteRunScopeRetainsHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeRun, Ref: "run-cleanup"}
	if _, err := f.st.Write(ctx, f.t1, testWrite(scope, "one", "1")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Write(ctx, f.t1, testWrite(scope, "two", "2")); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteScope(ctx, f.t1, scope); err != nil {
		t.Fatal(err)
	}
	current, _, err := f.st.Read(ctx, f.t1, scope)
	if err != nil || len(current) != 0 {
		t.Fatalf("current after cleanup=%+v err=%v", current, err)
	}
	history, err := f.st.History(ctx, f.t1, scope, "one")
	if err != nil || len(history) != 1 || history[0].SupersededBy == "" {
		t.Fatalf("history after cleanup=%+v err=%v", history, err)
	}
}
