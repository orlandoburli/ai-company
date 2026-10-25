package runner_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// These tests run against the application role, never the owner: a superuser
// bypasses row-level security even with FORCE, which would make every
// isolation assertion here vacuously true.

type fixture struct {
	t   *testing.T
	app *sql.DB
	db  *pgtest.DB
	st  *runner.Store
	ast *agent.Store
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
		t:   t,
		app: app,
		db:  db,
		st:  runner.NewStore(app),
		ast: agent.NewStore(app),
		t1:  tenancy.Scope{TenantID: "T1", UserID: "U-T1", Role: tenancy.RoleOwner},
		t2:  tenancy.Scope{TenantID: "T2", UserID: "U-T2", Role: tenancy.RoleOwner},
	}
}

func (f *fixture) cliRunner(sc tenancy.Scope, name string) runner.Runner {
	f.t.Helper()
	r, err := f.st.Create(context.Background(), sc, runner.NewRunner{
		Name:   name,
		Kind:   runner.KindCLI,
		Config: json.RawMessage(`{"bin":"claude","args":["-p"],"output_format":"stream-json","timeout_sec":300}`),
	})
	if err != nil {
		f.t.Fatalf("create runner %q: %v", name, err)
	}
	return r
}

func (f *fixture) agent(sc tenancy.Scope, name string) agent.Agent {
	f.t.Helper()
	a, err := f.ast.CreateAgent(context.Background(), sc, agent.Agent{
		Name:      name,
		Role:      agent.RoleDev,
		SoulMD:    "You are " + name + ".",
		Enabled:   true,
		CreatedBy: &sc.UserID,
	})
	if err != nil {
		f.t.Fatalf("create agent %q: %v", name, err)
	}
	return a
}

func TestRunnerCRUD(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	created := f.cliRunner(f.t1, "dev")
	if created.ID == "" || created.TenantID != "T1" || created.Name != "dev" {
		t.Fatalf("unexpected runner: %+v", created)
	}
	if created.Kind != runner.KindCLI || !created.Enabled {
		t.Fatalf("unexpected runner: %+v", created)
	}
	if created.BudgetTokens != 0 || created.BudgetCostUSD != 0 {
		t.Errorf("budgets default to 0, got %d and %v", created.BudgetTokens, created.BudgetCostUSD)
	}
	var cfg map[string]any
	if err := json.Unmarshal(created.Config, &cfg); err != nil {
		t.Fatalf("config is not JSON: %v", err)
	}
	if cfg["bin"] != "claude" {
		t.Errorf("config = %s, want bin=claude", created.Config)
	}

	got, err := f.st.Get(ctx, f.t1, created.ID)
	if err != nil {
		t.Fatalf("get runner: %v", err)
	}
	if got.ID != created.ID || got.Name != "dev" {
		t.Fatalf("get returned %+v", got)
	}

	byName, found, err := f.st.GetByName(ctx, f.t1, "DEV")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if !found || byName.ID != created.ID {
		t.Fatalf("get by name = %+v (found %v)", byName, found)
	}
	if _, found, err := f.st.GetByName(ctx, f.t1, "ghost"); err != nil || found {
		t.Fatalf("get by name (missing) found = %v, err = %v", found, err)
	}

	list, err := f.st.List(ctx, f.t1)
	if err != nil {
		t.Fatalf("list runners: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}

	// A patch touches only what it names.
	name := "dev-cli"
	newCfg := json.RawMessage(`{"bin":"codex"}`)
	disabled := false
	budget := int64(5000)
	cost := 1.5
	updated, err := f.st.Update(ctx, f.t1, created.ID, runner.Patch{
		Name:          &name,
		Config:        &newCfg,
		Enabled:       &disabled,
		BudgetTokens:  &budget,
		BudgetCostUSD: &cost,
	})
	if err != nil {
		t.Fatalf("update runner: %v", err)
	}
	if updated.Name != "dev-cli" || updated.Enabled || updated.BudgetTokens != 5000 {
		t.Fatalf("unexpected update: %+v", updated)
	}
	if updated.BudgetCostUSD != cost {
		t.Errorf("budget_cost_usd = %v, want %v", updated.BudgetCostUSD, cost)
	}
	if !strings.Contains(string(updated.Config), "codex") {
		t.Errorf("config = %s, want bin=codex", updated.Config)
	}
	if updated.Kind != runner.KindCLI {
		t.Errorf("kind changed to %q by a patch that did not name it", updated.Kind)
	}

	toggled, err := f.st.SetEnabled(ctx, f.t1, created.ID, true)
	if err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if !toggled.Enabled {
		t.Error("set enabled did not enable the runner")
	}

	if err := f.st.Delete(ctx, f.t1, created.ID); err != nil {
		t.Fatalf("delete runner: %v", err)
	}
	if _, err := f.st.Get(ctx, f.t1, created.ID); !errors.Is(err, runner.ErrNotFound) {
		t.Fatalf("get after delete: %v, want ErrNotFound", err)
	}
	if err := f.st.Delete(ctx, f.t1, created.ID); !errors.Is(err, runner.ErrNotFound) {
		t.Fatalf("delete twice: %v, want ErrNotFound", err)
	}
}

func TestRunnerTenantIsolation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	created := f.cliRunner(f.t1, "dev")

	if _, err := f.st.Get(ctx, f.t2, created.ID); !errors.Is(err, runner.ErrNotFound) {
		t.Fatalf("cross-tenant get: %v, want ErrNotFound", err)
	}
	if _, found, err := f.st.GetByName(ctx, f.t2, "dev"); err != nil || found {
		t.Fatalf("cross-tenant get by name: found = %v, err = %v", found, err)
	}
	list, err := f.st.List(ctx, f.t2)
	if err != nil {
		t.Fatalf("list T2: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("T2 sees %d runners of T1", len(list))
	}
	if err := f.st.Delete(ctx, f.t2, created.ID); !errors.Is(err, runner.ErrNotFound) {
		t.Fatalf("cross-tenant delete: %v, want ErrNotFound", err)
	}
	if err := f.st.SetChain(ctx, f.t2, "A1", []string{created.ID}); !errors.Is(err, runner.ErrNotFound) {
		t.Fatalf("cross-tenant chain: %v, want ErrNotFound", err)
	}
}

func TestRunnerNameIsUniquePerTenant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.cliRunner(f.t1, "dev")
	if _, err := f.st.Create(ctx, f.t1, runner.NewRunner{
		Name:   "dev",
		Kind:   runner.KindCLI,
		Config: json.RawMessage(`{"bin":"codex"}`),
	}); !errors.Is(err, runner.ErrConflict) {
		t.Fatalf("duplicate name: %v, want ErrConflict", err)
	}

	// The same name in another tenant is a different runner.
	other := f.cliRunner(f.t2, "dev")
	if other.ID == "" {
		t.Fatal("the second tenant could not use the name")
	}
}

func TestRunnerConfigValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		kind   runner.Kind
		config string
	}{
		{"cli without a binary", runner.KindCLI, `{}`},
		{"cli with an unknown field", runner.KindCLI, `{"bin":"claude","nope":1}`},
		{"cli with a bad timeout", runner.KindCLI, `{"bin":"claude","timeout_sec":9999}`},
		{"cli with a bad output format", runner.KindCLI, `{"bin":"claude","output_format":"yaml"}`},
		{"cli with a bad env name", runner.KindCLI, `{"bin":"claude","env":{"lower":1}}`},
		{"api runner kind is no longer supported", runner.KindAPI, `{"provider":"openai","model":"gpt","key_env":"OPENAI_API_KEY"}`},
		{"a config that is not an object", runner.KindCLI, `["claude"]`},
		{"a config that is not JSON", runner.KindCLI, `{`},
	}
	for _, tc := range cases {
		_, err := f.st.Create(ctx, f.t1, runner.NewRunner{
			Name:   "r-" + strings.ReplaceAll(tc.name, " ", "-"),
			Kind:   tc.kind,
			Config: json.RawMessage(tc.config),
		})
		if !errors.Is(err, runner.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
		}
	}

	// An unknown kind is refused before anything is written.
	if _, err := f.st.Create(ctx, f.t1, runner.NewRunner{
		Name: "carrier-pigeon", Kind: runner.Kind("pigeon"), Config: json.RawMessage(`{}`),
	}); !errors.Is(err, runner.ErrInvalid) {
		t.Errorf("unknown kind: %v, want ErrInvalid", err)
	}

	// A name that is not mentionable-shaped is refused.
	if _, err := f.st.Create(ctx, f.t1, runner.NewRunner{
		Name: "Not Valid", Kind: runner.KindCLI, Config: json.RawMessage(`{"bin":"claude"}`),
	}); !errors.Is(err, runner.ErrInvalid) {
		t.Errorf("bad name: %v, want ErrInvalid", err)
	}

	// API is retained as a legacy stored value, but cannot be set in the registry.
	r := f.cliRunner(f.t1, "dev")
	kind := runner.KindAPI
	if runner.KindAPI.Valid() {
		t.Error("legacy API kind is valid for registry writes")
	}
	if _, err := f.st.Update(ctx, f.t1, r.ID, runner.Patch{Kind: &kind}); !errors.Is(err, runner.ErrInvalid) {
		t.Errorf("API kind patch: %v, want ErrInvalid", err)
	}
}

func TestRunnerChain(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	first := f.cliRunner(f.t1, "dev")
	second := f.cliRunner(f.t1, "pm")
	a := f.agent(f.t1, "dev-agent")

	// An agent starts with no chain.
	chain, err := f.st.Chain(ctx, f.t1, a.ID)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 0 {
		t.Fatalf("chain = %+v, want empty", chain)
	}

	// The chain is the fallback order, exactly as given.
	if err := f.st.SetChain(ctx, f.t1, a.ID, []string{second.ID, first.ID}); err != nil {
		t.Fatalf("set chain: %v", err)
	}
	chain, err = f.st.Chain(ctx, f.t1, a.ID)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain has %d runners, want 2", len(chain))
	}
	if chain[0].ID != second.ID || chain[1].ID != first.ID {
		t.Fatalf("chain order = %s, %s", chain[0].Name, chain[1].Name)
	}

	// Setting the chain again replaces it.
	if err := f.st.SetChain(ctx, f.t1, a.ID, []string{first.ID}); err != nil {
		t.Fatalf("replace chain: %v", err)
	}
	chain, err = f.st.Chain(ctx, f.t1, a.ID)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 1 || chain[0].ID != first.ID {
		t.Fatalf("chain after replace = %+v", chain)
	}

	// An empty chain clears it.
	if err := f.st.SetChain(ctx, f.t1, a.ID, nil); err != nil {
		t.Fatalf("clear chain: %v", err)
	}
	chain, err = f.st.Chain(ctx, f.t1, a.ID)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 0 {
		t.Fatalf("chain after clear = %+v, want empty", chain)
	}

	// A runner that does not exist is refused, and nothing is written.
	if err := f.st.SetChain(ctx, f.t1, a.ID, []string{first.ID, "NOPE"}); !errors.Is(err, runner.ErrNotFound) {
		t.Fatalf("chain with an unknown runner: %v, want ErrNotFound", err)
	}
	chain, err = f.st.Chain(ctx, f.t1, a.ID)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 0 {
		t.Fatalf("a refused chain was written anyway: %+v", chain)
	}

	// The same runner may not appear twice in one chain.
	if err := f.st.SetChain(ctx, f.t1, a.ID, []string{first.ID, first.ID}); !errors.Is(err, runner.ErrInvalid) {
		t.Fatalf("chain with a duplicate runner: %v, want ErrInvalid", err)
	}

	// A chain for an agent that does not exist is refused.
	if err := f.st.SetChain(ctx, f.t1, "ghost", []string{first.ID}); !errors.Is(err, runner.ErrNotFound) {
		t.Fatalf("chain for an unknown agent: %v, want ErrNotFound", err)
	}
}

func TestRunnerDeleteIsRefusedWhileBound(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	r := f.cliRunner(f.t1, "dev")
	a := f.agent(f.t1, "dev-agent")
	if err := f.st.SetChain(ctx, f.t1, a.ID, []string{r.ID}); err != nil {
		t.Fatalf("set chain: %v", err)
	}

	if err := f.st.Delete(ctx, f.t1, r.ID); !errors.Is(err, runner.ErrRunnerInUse) {
		t.Fatalf("delete a bound runner: %v, want ErrRunnerInUse", err)
	}
	if _, err := f.st.Get(ctx, f.t1, r.ID); err != nil {
		t.Fatalf("the runner was deleted anyway: %v", err)
	}

	// Unbind it and the delete goes through.
	if err := f.st.SetChain(ctx, f.t1, a.ID, nil); err != nil {
		t.Fatalf("clear chain: %v", err)
	}
	if err := f.st.Delete(ctx, f.t1, r.ID); err != nil {
		t.Fatalf("delete after unbinding: %v", err)
	}
}
