package retention_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/store/migrations"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/store/retention"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

func TestMigrationAndPartitionSecurity(t *testing.T) {
	db := pgtest.FreshDB(t)
	assertPartitioned(t, db.DB)

	app, scopes, jobs := seedJobs(t, db, "migration")
	now := time.Now().UTC()
	past := monthStart(now.AddDate(0, -5, 0))
	boundary := monthStart(now)
	future := monthStart(now.AddDate(0, 2, 0))
	for i, createdAt := range []time.Time{past, boundary, future} {
		appendLog(t, app, scopes[0].TenantID, jobs[0], fmt.Sprintf("migration-%d", i), createdAt)
	}

	// Restore the historical heap shape with the rows above, then apply 0015
	// against real pre-existing data. This exercises its data-copy path rather
	// than only the empty-table path run by FreshDB.
	rebuildLegacyLogsAndReapply(t, db, scopes[0].TenantID)
	assertPartitioned(t, db.DB)

	// A row written into a previously discovered old month routes to that month.
	appendLog(t, app, scopes[0].TenantID, jobs[0], "past-month-append", past.Add(12*time.Hour))
	partitionName := "job_logs_" + past.Format("2006_01")
	var inPartition int
	if err := db.QueryRow(`SELECT count(*) FROM public.`+partitionName+` WHERE message = 'past-month-append'`).Scan(&inPartition); err != nil {
		t.Fatalf("read past partition as owner: %v", err)
	}
	if inPartition != 1 {
		t.Fatalf("past-month partition has %d matching rows, want 1", inPartition)
	}

	currentLog := appendLog(t, app, scopes[0].TenantID, jobs[0], "current-month", now)
	if got := countLog(t, app, scopes[0].TenantID, currentLog); got != 1 {
		t.Fatalf("parent-table row count=%d, want 1", got)
	}
	currentPartition := "job_logs_" + now.Format("2006_01")
	if err := db.QueryRow(`SELECT count(*) FROM public.`+currentPartition+` WHERE seq = $1`, currentLog).Scan(&inPartition); err != nil {
		t.Fatalf("read current partition as owner: %v", err)
	}
	if inPartition != 1 {
		t.Fatalf("current partition row count=%d, want 1", inPartition)
	}

	// Ensure the application role has no direct privilege on either an existing
	// child or a later child created by the migration owner.
	assertNoDirectPartitionPrivileges(t, db, partitionName)
	newPartition := "job_logs_future_test"
	if _, err := db.Exec(`CREATE TABLE public.` + newPartition + ` PARTITION OF public.job_logs
		FOR VALUES FROM ('2098-01-01 00:00:00+00') TO ('2098-02-01 00:00:00+00')`); err != nil {
		t.Fatalf("create future partition: %v", err)
	}
	assertNoDirectPartitionPrivileges(t, db, newPartition)

	// Tenant A's rows are not visible to tenant B through the parent relation.
	if got := countLog(t, app, scopes[1].TenantID, currentLog); got != 0 {
		t.Fatalf("tenant B sees %d tenant A log rows, want 0", got)
	}

	assertAppendOnly(t, db, currentLog)
}

func TestPruneDropsOnlyExpiredPartitions(t *testing.T) {
	db := pgtest.FreshDB(t)
	app, scopes, jobs := seedJobs(t, db, "prune")
	now := time.Now().UTC()
	old := monthStart(now.AddDate(0, -8, 0))
	recent := monthStart(now.AddDate(0, -1, 0))
	appendLog(t, app, scopes[0].TenantID, jobs[0], "old", old)
	appendLog(t, app, scopes[0].TenantID, jobs[0], "recent", recent)
	rebuildLegacyLogsAndReapply(t, db, scopes[0].TenantID)

	oldName := "job_logs_" + old.Format("2006_01")
	recentName := "job_logs_" + recent.Format("2006_01")
	result, err := retention.Prune(context.Background(), db.DB, retention.Config{
		KeepDays: 90,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(result.Dropped) != 1 || result.Dropped[0] != oldName {
		t.Fatalf("dropped partitions=%v, want [%s]", result.Dropped, oldName)
	}
	if result.Kept == 0 {
		t.Fatal("Prune kept no range partitions; expected recent partitions")
	}
	assertPartitionExists(t, db.DB, oldName, false)
	assertPartitionExists(t, db.DB, recentName, true)

	// The default retention behavior is safe on an empty fresh database.
	empty := pgtest.FreshDB(t)
	emptyResult, err := retention.Prune(context.Background(), empty.DB, retention.Config{})
	if err != nil {
		t.Fatalf("Prune on empty database: %v", err)
	}
	if len(emptyResult.Dropped) != 0 {
		t.Fatalf("empty database dropped partitions: %v", emptyResult.Dropped)
	}
}

func seedJobs(t *testing.T, db *pgtest.DB, prefix string) (*sql.DB, []tenancy.Scope, []string) {
	t.Helper()
	for i, tenantID := range []string{prefix + "-a", prefix + "-b"} {
		if _, err := db.Exec(`SELECT auth_register_tenant($1, $2, $3, $4, $5, $6, $7, $8)`,
			tenantID, "Tenant "+tenantID, tenantID, "user-"+tenantID,
			"user-"+tenantID+"@example.com", "x", "User", "member-"+tenantID); err != nil {
			t.Fatalf("register tenant %s: %v", tenantID, err)
		}
		_ = i
	}
	app := db.AsRole(t, "aic_app")
	store := agent.NewStore(app)
	scopes := []tenancy.Scope{{TenantID: prefix + "-a"}, {TenantID: prefix + "-b"}}
	jobs := make([]string, 0, 2)
	for i, scope := range scopes {
		agentRow, err := store.CreateAgent(context.Background(), scope, agent.Agent{
			Name: fmt.Sprintf("dev-%d", i), Role: agent.RoleDev, Kind: agent.KindCLI,
			SoulMD: "test", Config: []byte(`{"bin":"test"}`), Enabled: true,
		})
		if err != nil {
			t.Fatalf("create agent: %v", err)
		}
		job, err := store.CreateJob(context.Background(), scope, agent.NewJob{
			AgentID: agentRow.ID, Prompt: "test prompt",
		})
		if err != nil {
			t.Fatalf("create job: %v", err)
		}
		jobs = append(jobs, job.ID)
	}
	return app, scopes, jobs
}

func appendLog(t *testing.T, db *sql.DB, tenantID, jobID, message string, createdAt time.Time) int64 {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin log insert: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatalf("set log tenant: %v", err)
	}
	var seq int64
	if err := tx.QueryRow(`INSERT INTO job_logs (tenant_id, job_id, message, created_at)
		VALUES ($1, $2, $3, $4) RETURNING seq`, tenantID, jobID, message, createdAt).Scan(&seq); err != nil {
		t.Fatalf("insert log: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit log insert: %v", err)
	}
	return seq
}

func countLog(t *testing.T, db *sql.DB, tenantID string, seq int64) int {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin scoped read: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatalf("set scoped read tenant: %v", err)
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM job_logs WHERE seq = $1`, seq).Scan(&count); err != nil {
		t.Fatalf("count log via parent: %v", err)
	}
	return count
}

func rebuildLegacyLogsAndReapply(t *testing.T, db *pgtest.DB, tenantID string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin restore legacy heap: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatalf("set tenant for legacy copy: %v", err)
	}
	if _, err := tx.Exec(`CREATE TABLE public.job_logs_before_test (
		seq BIGINT GENERATED ALWAYS AS IDENTITY,
		tenant_id TEXT NOT NULL, job_id TEXT NOT NULL, stream TEXT, level TEXT, kind TEXT,
		message TEXT NOT NULL DEFAULT '', data_json JSONB, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		run_id TEXT, step_run_id TEXT)`); err != nil {
		t.Fatalf("create legacy heap: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO public.job_logs_before_test
		(seq, tenant_id, job_id, stream, level, kind, message, data_json, created_at, run_id, step_run_id)
		OVERRIDING SYSTEM VALUE SELECT seq, tenant_id, job_id, stream, level, kind, message, data_json,
		created_at, run_id, step_run_id FROM public.job_logs`); err != nil {
		t.Fatalf("copy partitioned rows into legacy heap: %v", err)
	}
	if _, err := tx.Exec(`DROP TABLE public.job_logs CASCADE`); err != nil {
		t.Fatalf("drop partitioned table for migration test: %v", err)
	}
	if _, err := tx.Exec(`ALTER TABLE public.job_logs_before_test RENAME TO job_logs`); err != nil {
		t.Fatalf("restore legacy table name: %v", err)
	}
	if _, err := tx.Exec(`DELETE FROM schema_migrations WHERE version = '0015'`); err != nil {
		t.Fatalf("clear migration version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit legacy heap restoration: %v", err)
	}
	if _, err := migrations.Up(context.Background(), db.DB); err != nil {
		t.Fatalf("apply migration to populated heap: %v", err)
	}
}

func assertPartitioned(t *testing.T, db *sql.DB) {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_partitioned_table p
		JOIN pg_class c ON c.oid = p.partrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = 'job_logs')`).Scan(&ok); err != nil {
		t.Fatalf("inspect partitioned job_logs: %v", err)
	}
	if !ok {
		t.Fatal("job_logs is not partitioned")
	}
}

func assertNoDirectPartitionPrivileges(t *testing.T, db *pgtest.DB, part string) {
	t.Helper()
	var hasPrivilege bool
	if err := db.QueryRow(`SELECT has_table_privilege('aic_app', 'public."`+part+`"', 'SELECT')`).Scan(&hasPrivilege); err != nil {
		t.Fatalf("check partition SELECT privilege: %v", err)
	}
	if hasPrivilege {
		t.Fatalf("aic_app has direct SELECT on partition %s", part)
	}
	app := db.AsRole(t, "aic_app")
	if _, err := app.Query(`SELECT 1 FROM public."` + part + `" LIMIT 1`); err == nil {
		t.Fatalf("aic_app directly selected partition %s without privileges", part)
	}
}

func assertAppendOnly(t *testing.T, db *pgtest.DB, seq int64) {
	t.Helper()
	for _, statement := range []string{
		`UPDATE public.job_logs SET message = 'changed' WHERE seq = $1`,
		`DELETE FROM public.job_logs WHERE seq = $1`,
	} {
		_, err := db.Exec(statement, seq)
		if err == nil || !strings.Contains(err.Error(), "AIC05") {
			t.Fatalf("append-only statement %q error=%v, want AIC05", statement, err)
		}
	}
}

func assertPartitionExists(t *testing.T, db *sql.DB, name string, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = $1)`, name).Scan(&exists); err != nil {
		t.Fatalf("check partition %s: %v", name, err)
	}
	if exists != want {
		t.Fatalf("partition %s exists=%v, want %v", name, exists, want)
	}
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.UTC().Year(), t.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
}
