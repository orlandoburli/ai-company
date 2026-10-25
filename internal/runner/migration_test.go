package runner_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
)

// The migration test applies the schema up to 0019, puts agents in it and
// then applies 0020 — the same order the migration runner uses, so the
// conversion is exercised against a database that really has agents.
//
// The files are read from disk rather than from the migrations package's
// embedded copy, because the test needs to stop at 0019: the package only
// offers "apply everything".

// migrationsDir is where the migration files live, relative to this package.
const migrationsDir = "../store/migrations"

// freshDatabase creates a disposable database and returns a connection to it.
func freshDatabase(t *testing.T) *sql.DB {
	t.Helper()
	base := pgtest.DSN(t)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	name := "aic_t_mig_" + identifierSafe(t.Name())
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`); err != nil {
		admin.Close()
		t.Fatalf("drop %s: %v", name, err)
	}
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatalf("create %s: %v", name, err)
	}
	db, err := sql.Open("pgx", pgtest.WithDatabase(t, base, name))
	if err != nil {
		admin.Close()
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`); err != nil {
			t.Logf("cleanup: drop %s: %v", name, err)
		}
		admin.Close()
	})
	return db
}

// identifierSafe reduces a test name to [a-z0-9] so it is a valid identifier.
func identifierSafe(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// applyMigrations applies every migration up to and including lastVersion that
// has not been applied yet, each in its own transaction — the discipline the
// migration runner uses. The versions are recorded in schema_migrations, so
// calling it twice continues where the last call stopped.
func applyMigrations(t *testing.T, db *sql.DB, lastVersion string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		name       TEXT        NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	done := map[string]bool{}
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		t.Fatalf("read applied migrations: %v", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			t.Fatalf("scan applied migrations: %v", err)
		}
		done[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read applied migrations: %v", err)
	}

	paths, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	sort.Strings(paths)
	applied := 0
	for _, path := range paths {
		base := filepath.Base(path)
		version := base[:strings.Index(base, "_")]
		if version > lastVersion || done[version] {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s: %v", version, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback() //nolint:errcheck // the test fails on the next line
			t.Fatalf("apply %s: %v", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
			version, strings.TrimSuffix(base, ".sql")); err != nil {
			tx.Rollback() //nolint:errcheck // the test fails on the next line
			t.Fatalf("record %s: %v", version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", version, err)
		}
		applied++
	}
	if applied == 0 {
		t.Fatalf("no migrations applied up to %s", lastVersion)
	}
}

// seedAgent inserts an agent the way the registry would: a name, a role, the
// legacy transport columns.
func seedAgent(t *testing.T, db *sql.DB, id, tenant, name, kind, config string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO agents (id, tenant_id, name, role, kind, config_json)
		VALUES ($1, $2, $3, 'dev', $4, $5::jsonb)`, id, tenant, name, kind, config)
	if err != nil {
		t.Fatalf("seed agent %s: %v", name, err)
	}
}

func TestMigrationConvertsAgentsIntoRunners(t *testing.T) {
	db := freshDatabase(t)
	applyMigrations(t, db, "0019")

	// A tenant with two agents, and a second tenant with one.
	for _, tenant := range []struct{ id, slug string }{{"T1", "t1"}, {"T2", "t2"}} {
		if _, err := db.Exec(`INSERT INTO tenants (id, name, slug) VALUES ($1, $2, $3)`,
			tenant.id, tenant.id, tenant.slug); err != nil {
			t.Fatalf("seed tenant %s: %v", tenant.id, err)
		}
	}
	seedAgent(t, db, "A1", "T1", "dev", "cli", `{"bin":"claude","args":["-p"],"output_format":"stream-json"}`)
	seedAgent(t, db, "A2", "T1", "pm", "api", `{"provider":"openai","model":"gpt-4o-mini","key_env":"OPENAI_API_KEY"}`)
	seedAgent(t, db, "A3", "T2", "dev", "cli", `{"bin":"codex"}`)

	applyMigrations(t, db, "0020")

	// One runner per agent, named after it, carrying its kind and config.
	rows, err := db.Query(`SELECT r.tenant_id, r.name, r.kind, r.config_json, ar.agent_id, ar.priority
		FROM runners r
		JOIN agent_runners ar ON ar.runner_id = r.id
		ORDER BY r.tenant_id, r.name`)
	if err != nil {
		t.Fatalf("read runners: %v", err)
	}
	defer rows.Close()
	type converted struct {
		tenant, name, kind, config, agent string
		priority                          int
	}
	var got []converted
	for rows.Next() {
		var c converted
		if err := rows.Scan(&c.tenant, &c.name, &c.kind, &c.config, &c.agent, &c.priority); err != nil {
			t.Fatalf("scan runner: %v", err)
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read runners: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("converted %d runners, want 3", len(got))
	}
	byAgent := map[string]converted{}
	for _, c := range got {
		byAgent[c.agent] = c
		if c.priority != 1 {
			t.Errorf("agent %s bound with priority %d, want 1", c.agent, c.priority)
		}
	}
	dev, ok := byAgent["A1"]
	if !ok {
		t.Fatal("agent A1 has no runner")
	}
	if dev.name != "dev" || dev.kind != "cli" {
		t.Errorf("A1 runner = %s/%s, want dev/cli", dev.name, dev.kind)
	}
	if bin := configField(t, dev.config, "bin"); bin != "claude" {
		t.Errorf("A1 runner config = %s, want the agent's old config (bin=claude)", dev.config)
	}
	pm, ok := byAgent["A2"]
	if !ok {
		t.Fatal("agent A2 has no runner")
	}
	if pm.name != "pm" || pm.kind != "api" {
		t.Errorf("A2 runner = %s/%s, want pm/api", pm.name, pm.kind)
	}
	if provider := configField(t, pm.config, "provider"); provider != "openai" {
		t.Errorf("A2 runner config = %s, want the agent's old config (provider=openai)", pm.config)
	}
	other, ok := byAgent["A3"]
	if !ok {
		t.Fatal("agent A3 has no runner")
	}
	if other.tenant != "T2" || other.name != "dev" {
		t.Errorf("A3 runner = %s/%s, want T2/dev", other.tenant, other.name)
	}

	// The agents' legacy columns are untouched: nothing reads them any more,
	// and a later migration drops them.
	var legacyKind, legacyConfig string
	if err := db.QueryRow(`SELECT kind, config_json FROM agents WHERE id = 'A1'`).
		Scan(&legacyKind, &legacyConfig); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if legacyKind != "cli" || !strings.Contains(legacyConfig, "claude") {
		t.Errorf("agent A1 legacy columns = %s/%s, want them preserved", legacyKind, legacyConfig)
	}
}

// configField reads one field out of a stored JSON config.
func configField(t *testing.T, config, field string) string {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(config), &obj); err != nil {
		t.Fatalf("config %q is not JSON: %v", config, err)
	}
	v, _ := obj[field].(string)
	return v
}

func TestMigrationReusesARunnerWithTheSameName(t *testing.T) {
	db := freshDatabase(t)
	applyMigrations(t, db, "0019")

	if _, err := db.Exec(`INSERT INTO tenants (id, name, slug) VALUES ('T1', 'T1', 't1')`); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	applyMigrations(t, db, "0020")

	// A runner that already carries the name an agent is about to take. The
	// conversion reuses it and updates its transport instead of failing on the
	// name conflict.
	if _, err := db.Exec(`INSERT INTO runners (id, tenant_id, name, kind, config_json)
		VALUES ('R1', 'T1', 'dev', 'api', '{"provider":"openai","model":"old"}')`); err != nil {
		t.Fatalf("seed runner: %v", err)
	}
	seedAgent(t, db, "A1", "T1", "dev", "cli", `{"bin":"claude"}`)

	if _, err := db.Exec(`SELECT public.convert_agents_to_runners()`); err != nil {
		t.Fatalf("convert: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM runners WHERE tenant_id = 'T1'`).Scan(&count); err != nil {
		t.Fatalf("count runners: %v", err)
	}
	if count != 1 {
		t.Fatalf("the tenant has %d runners, want 1 (the existing one, reused)", count)
	}
	var id, kind, config string
	if err := db.QueryRow(`SELECT id, kind, config_json FROM runners WHERE tenant_id = 'T1'`).
		Scan(&id, &kind, &config); err != nil {
		t.Fatalf("read runner: %v", err)
	}
	if id != "R1" {
		t.Errorf("the runner %s was replaced instead of reused", id)
	}
	if kind != "cli" || !strings.Contains(config, "claude") {
		t.Errorf("the reused runner = %s/%s, want the agent's kind and config", kind, config)
	}

	// And it is bound to the agent.
	var agentID string
	var priority int
	if err := db.QueryRow(`SELECT agent_id, priority FROM agent_runners WHERE runner_id = 'R1'`).
		Scan(&agentID, &priority); err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if agentID != "A1" || priority != 1 {
		t.Errorf("binding = %s/%d, want A1/1", agentID, priority)
	}
}

func TestMigrationOnAnEmptyDatabase(t *testing.T) {
	db := freshDatabase(t)
	applyMigrations(t, db, "0019")
	if _, err := db.Exec(`INSERT INTO tenants (id, name, slug) VALUES ('T1', 'T1', 't1')`); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	// No agents at all: the migration applies and converts nothing.
	applyMigrations(t, db, "0020")

	var runners, bindings int
	if err := db.QueryRow(`SELECT count(*) FROM runners`).Scan(&runners); err != nil {
		t.Fatalf("count runners: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM agent_runners`).Scan(&bindings); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if runners != 0 || bindings != 0 {
		t.Fatalf("the conversion wrote %d runners and %d bindings on an empty database", runners, bindings)
	}

	// The new columns exist with their defaults.
	var kind string
	var config []byte
	if err := db.QueryRow(`SELECT runner_kind, runner_config_json FROM agent_jobs LIMIT 1`).
		Scan(&kind, &config); err != nil && err != sql.ErrNoRows {
		t.Fatalf("read agent_jobs: %v", err)
	}
	var variables []byte
	if err := db.QueryRow(`SELECT variables_json FROM workflow_runs LIMIT 1`).
		Scan(&variables); err != nil && err != sql.ErrNoRows {
		t.Fatalf("read workflow_runs: %v", err)
	}
}

// TestMigrationAddsTheJobSnapshotColumns checks the columns a queued job
// carries from here on: the runner it was queued with.
func TestMigrationAddsTheJobSnapshotColumns(t *testing.T) {
	db := freshDatabase(t)
	applyMigrations(t, db, "0020")

	for _, col := range []string{"runner_id", "runner_kind", "runner_config_json"} {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_name = 'agent_jobs' AND column_name = $1)`, col).Scan(&exists); err != nil {
			t.Fatalf("read columns: %v", err)
		}
		if !exists {
			t.Errorf("agent_jobs.%s is missing", col)
		}
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		 WHERE table_name = 'workflow_runs' AND column_name = 'variables_json')`).Scan(&exists); err != nil {
		t.Fatalf("read columns: %v", err)
	}
	if !exists {
		t.Error("workflow_runs.variables_json is missing")
	}
}

func TestMigrationRestrictsNewRunnerKindsToCLIAndPreservesLegacyRows(t *testing.T) {
	db := freshDatabase(t)
	applyMigrations(t, db, "0020")
	if _, err := db.Exec(`INSERT INTO tenants (id, name, slug) VALUES ('T1', 'T1', 't1')`); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO runners (id, tenant_id, name, kind, config_json)
		VALUES ('R-LEGACY', 'T1', 'legacy-api', 'api', '{"provider":"anthropic","model":"legacy"}')`); err != nil {
		t.Fatalf("seed legacy API runner: %v", err)
	}
	migration, err := os.ReadFile(filepath.Join(migrationsDir, "0027_runner_kind_cli_only.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	var kind string
	if err := db.QueryRow(`SELECT kind FROM runners WHERE id='R-LEGACY'`).Scan(&kind); err != nil || kind != "api" {
		t.Fatalf("legacy runner kind = %q, err = %v; want preserved api row", kind, err)
	}
	if _, err := db.Exec(`INSERT INTO runners (id, tenant_id, name, kind, config_json)
		VALUES ('R-API', 'T1', 'new-api', 'api', '{}')`); err == nil {
		t.Fatal("migration accepted a newly created API runner")
	}
	if _, err := db.Exec(`INSERT INTO runners (id, tenant_id, name, kind, config_json)
		VALUES ('R-CLI', 'T1', 'new-cli', 'cli', '{"bin":"claude"}')`); err != nil {
		t.Fatalf("migration rejected a CLI runner: %v", err)
	}
	var rlsEnabled, selectGrant bool
	if err := db.QueryRow(`SELECT c.relrowsecurity, has_table_privilege('aic_app', 'runners', 'SELECT')
		FROM pg_class c WHERE c.oid='runners'::regclass`).Scan(&rlsEnabled, &selectGrant); err != nil {
		t.Fatalf("check registry protections: %v", err)
	}
	if !rlsEnabled || !selectGrant {
		t.Fatalf("migration changed RLS/grants: RLS=%v SELECT grant=%v", rlsEnabled, selectGrant)
	}
}
