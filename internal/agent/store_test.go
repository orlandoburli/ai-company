package agent_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

// These tests run against the application role, never the owner: a superuser
// bypasses row-level security even with FORCE, which would make every isolation
// assertion here vacuously true. The owner is opened separately below where the
// append-only trigger is exercised directly.

type fixture struct {
	t   *testing.T
	app *sql.DB
	db  *pgtest.DB
	st  *agent.Store
	wst *work.Store
	t1  tenancy.Scope
	t2  tenancy.Scope
	pr1 work.Project
	pr2 work.Project
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	// Keep every pool involved at/below 8 connections: the admin pool is capped
	// by pgtest, so this application pool stays at 6.
	app.SetMaxOpenConns(6)

	for _, id := range []string{"T1", "T2"} {
		if _, err := app.Exec(`SELECT auth_register_tenant($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, "Tenant "+id, strings.ToLower(id), "U-"+id, strings.ToLower(id)+"@example.com",
			"x", "User "+id, "M-"+id); err != nil {
			t.Fatalf("seed tenant %s: %v", id, err)
		}
	}

	f := &fixture{
		t:   t,
		app: app,
		db:  db,
		st:  agent.NewStore(app),
		wst: work.NewStore(app),
		t1:  tenancy.Scope{TenantID: "T1", UserID: "U-T1", Role: tenancy.RoleOwner},
		t2:  tenancy.Scope{TenantID: "T2", UserID: "U-T2", Role: tenancy.RoleOwner},
	}
	ctx := context.Background()

	var err error
	p1, err := f.wst.CreateProduct(ctx, f.t1, "Acme", "acme", "", "")
	if err != nil {
		t.Fatalf("create product T1: %v", err)
	}
	if f.pr1, err = f.wst.CreateProject(ctx, f.t1, p1.ID, nil, "Core", "CORE"); err != nil {
		t.Fatalf("create project T1: %v", err)
	}
	p2, err := f.wst.CreateProduct(ctx, f.t2, "Globex", "globex", "", "")
	if err != nil {
		t.Fatalf("create product T2: %v", err)
	}
	if f.pr2, err = f.wst.CreateProject(ctx, f.t2, p2.ID, nil, "Core", "CORE"); err != nil {
		t.Fatalf("create project T2: %v", err)
	}
	return f
}

func (f *fixture) itemFor(sc tenancy.Scope, projectID, title string) work.Item {
	f.t.Helper()
	it, err := f.wst.CreateItem(context.Background(), sc, work.NewItem{
		ProjectID: projectID, Kind: "task", Title: title, ActorUserID: sc.UserID,
	})
	if err != nil {
		f.t.Fatalf("create item %q: %v", title, err)
	}
	return it
}

func (f *fixture) agent(sc tenancy.Scope, name string) agent.Agent {
	f.t.Helper()
	a, err := f.st.CreateAgent(context.Background(), sc, agent.Agent{
		Name:      name,
		Role:      agent.RoleDev,
		Kind:      agent.KindCLI,
		SoulMD:    "You are " + name + ".",
		Config:    json.RawMessage(`{"bin":"claude","args":["-p"]}`),
		Enabled:   true,
		CreatedBy: strptr(sc.UserID),
	})
	if err != nil {
		f.t.Fatalf("create agent %q: %v", name, err)
	}
	return a
}

func (f *fixture) queue(sc tenancy.Scope, agentID, prompt string) agent.Job {
	f.t.Helper()
	j, err := f.st.CreateJob(context.Background(), sc, agent.NewJob{
		AgentID: agentID, Prompt: prompt, CreatedBy: sc.UserID,
	})
	if err != nil {
		f.t.Fatalf("create job for %s: %v", agentID, err)
	}
	return j
}

func strptr(s string) *string { return &s }

func rawptr(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

func TestAgentIsolation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	item1 := f.itemFor(f.t1, f.pr1.ID, "tenant one item")
	a1 := f.agent(f.t1, "dev")

	agentsT2, err := f.st.ListAgents(ctx, f.t2)
	if err != nil {
		t.Fatalf("list agents as T2: %v", err)
	}
	if len(agentsT2) != 0 {
		t.Fatalf("T2 sees %d T1 agents; want 0", len(agentsT2))
	}
	if _, err := f.st.GetAgent(ctx, f.t2, a1.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("T2 get T1 agent: err = %v; want ErrNotFound", err)
	}
	if _, err := f.st.GetAgentByName(ctx, f.t2, "dev"); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("T2 get T1 agent by name: err = %v; want ErrNotFound", err)
	}

	// Threads and messages are invisible too.
	th1, err := f.st.EnsureItemThread(ctx, f.t1, item1.ID, f.t1.UserID)
	if err != nil {
		t.Fatalf("ensure thread: %v", err)
	}
	if _, err := f.st.AddMessage(ctx, f.t1, agent.NewMessage{
		ThreadID: th1.ID, AuthorKind: agent.AuthorHuman,
		AuthorUserID: f.t1.UserID, BodyMD: "hello @dev",
	}); err != nil {
		t.Fatalf("add message: %v", err)
	}
	if _, err := f.st.GetThread(ctx, f.t2, th1.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("T2 get T1 thread: err = %v; want ErrNotFound", err)
	}
	msgsT2, err := f.st.ListMessages(ctx, f.t2, th1.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages as T2: %v", err)
	}
	if len(msgsT2) != 0 {
		t.Fatalf("T2 sees %d T1 messages; want 0", len(msgsT2))
	}

	// Jobs and logs are invisible too.
	j1 := f.queue(f.t1, a1.ID, "say hello")
	if _, err := f.st.AppendLog(ctx, f.t1, j1.ID, agent.Event{Message: "chunk"}); err != nil {
		t.Fatalf("append log: %v", err)
	}
	if _, err := f.st.GetJob(ctx, f.t2, j1.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("T2 get T1 job: err = %v; want ErrNotFound", err)
	}
	logsT2, err := f.st.ListLogs(ctx, f.t2, j1.ID, 0, 10)
	if err != nil {
		t.Fatalf("list logs as T2: %v", err)
	}
	if len(logsT2) != 0 {
		t.Fatalf("T2 sees %d T1 log rows; want 0", len(logsT2))
	}

	// Cross-tenant composite FKs are refused by the database even when both ids
	// are real: a T1 thread on a T2 item, a T1 job on a T2 agent.
	item2 := f.itemFor(f.t2, f.pr2.ID, "tenant two item")
	if _, err := f.st.CreateThread(ctx, f.t1, agent.ContextItem, item2.ID, "", "", f.t1.UserID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("T1 thread on T2 item: err = %v; want ErrNotFound", err)
	}
	a2 := f.agent(f.t2, "glob")
	if _, err := f.st.CreateJob(ctx, f.t1, agent.NewJob{AgentID: a2.ID, Prompt: "cross"}); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("T1 job on T2 agent: err = %v; want ErrNotFound", err)
	}
}

func TestAgentCreationValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	bad := []struct {
		name  string
		scene func() (agent.Agent, error)
	}{
		{"invalid name", func() (agent.Agent, error) {
			return f.st.CreateAgent(ctx, f.t1, agent.Agent{Role: agent.RoleDev, Kind: agent.KindCLI, Name: "Big Dev"})
		}},
		{"uppercase name", func() (agent.Agent, error) {
			return f.st.CreateAgent(ctx, f.t1, agent.Agent{Role: agent.RoleDev, Kind: agent.KindCLI, Name: "Dev"})
		}},
		{"unknown role", func() (agent.Agent, error) {
			return f.st.CreateAgent(ctx, f.t1, agent.Agent{Name: "x", Role: agent.Role("boss"), Kind: agent.KindCLI})
		}},
		{"unknown kind", func() (agent.Agent, error) {
			return f.st.CreateAgent(ctx, f.t1, agent.Agent{Name: "x", Role: agent.RoleDev, Kind: agent.Kind("carrier-pigeon")})
		}},
		{"soul too big", func() (agent.Agent, error) {
			return f.st.CreateAgent(ctx, f.t1, agent.Agent{
				Name: "x", Role: agent.RoleDev, Kind: agent.KindCLI, SoulMD: strings.Repeat("a", 200*1024+1),
			})
		}},
		{"config not an object", func() (agent.Agent, error) {
			return f.st.CreateAgent(ctx, f.t1, agent.Agent{
				Name: "x", Role: agent.RoleDev, Kind: agent.KindCLI, Config: json.RawMessage(`["bin"]`),
			})
		}},
		{"config not JSON", func() (agent.Agent, error) {
			return f.st.CreateAgent(ctx, f.t1, agent.Agent{
				Name: "x", Role: agent.RoleDev, Kind: agent.KindCLI, Config: json.RawMessage(`{`),
			})
		}},
		{"concurrency out of range", func() (agent.Agent, error) {
			return f.st.CreateAgent(ctx, f.t1, agent.Agent{
				Name: "x", Role: agent.RoleDev, Kind: agent.KindCLI, ConcurrencyLimit: 17,
			})
		}},
	}

	for _, scene := range bad {
		if _, err := scene.scene(); !errors.Is(err, agent.ErrInvalid) {
			t.Fatalf("%s: err = %v; want ErrInvalid", scene.name, err)
		}
	}

	// Duplicate (tenant, name) is a conflict; the same name in another tenant is
	// fine.
	f.agent(f.t1, "review")
	if _, err := f.st.CreateAgent(ctx, f.t1, agent.Agent{Name: "review", Role: agent.RoleReviewer, Kind: agent.KindCLI}); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("duplicate name: err = %v; want ErrConflict", err)
	}
	if _, err := f.st.CreateAgent(ctx, f.t2, agent.Agent{Name: "review", Role: agent.RoleReviewer, Kind: agent.KindCLI}); err != nil {
		t.Fatalf("same name in T2: %v", err)
	}
}

func TestAgentUpdateSemanticsAndDelete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a := f.agent(f.t1, "dev")

	// Case-insensitive mention lookup.
	got, err := f.st.GetAgentByName(ctx, f.t1, "DEV")
	if err != nil {
		t.Fatalf("get by upper name: %v", err)
	}
	if got.ID != a.ID {
		t.Fatalf("get by name returned %s; want %s", got.ID, a.ID)
	}

	// Everything nil: nothing changes but updated_at.
	updated, err := f.st.UpdateAgent(ctx, f.t1, a.ID, agent.AgentPatch{}, f.t1.UserID)
	if err != nil {
		t.Fatalf("empty patch: %v", err)
	}
	if updated.Name != a.Name || updated.SoulMD != a.SoulMD || !updated.Enabled || updated.Role != a.Role {
		t.Fatalf("empty patch changed fields: %+v -> %+v", a, updated)
	}
	if !updated.UpdatedAt.After(a.UpdatedAt) && updated.UpdatedAt.Equal(a.UpdatedAt) {
		t.Fatalf("empty patch did not bump updated_at")
	}

	// A real patch: rename, disable, swap config, clear the soul.
	capabilities := rawptr(`{"code":true,"shell":false,"streaming":false,"usage":false}`)
	updated, err = f.st.UpdateAgent(ctx, f.t1, a.ID, agent.AgentPatch{
		Name:         strptr("implementer"),
		Enabled:      boolptr(false),
		Config:       rawptr(`{"bin":"codex"}`),
		SoulMD:       strptr(""),
		Capabilities: capabilities,
	}, f.t1.UserID)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if updated.Name != "implementer" || updated.Enabled || updated.SoulMD != "" {
		t.Fatalf("patch not applied: %+v", updated)
	}
	var cfg map[string]any
	if err := json.Unmarshal(updated.Config, &cfg); err != nil {
		t.Fatalf("stored config: %v", err)
	}
	if cfg["bin"] != "codex" {
		t.Fatalf("config = %s; want bin codex", updated.Config)
	}

	// Update with patch validation errors and misses.
	if _, err := f.st.UpdateAgent(ctx, f.t1, a.ID, agent.AgentPatch{Name: strptr("Nope")}, f.t1.UserID); !errors.Is(err, agent.ErrInvalid) {
		t.Fatalf("bad rename: err = %v; want ErrInvalid", err)
	}
	if _, err := f.st.UpdateAgent(ctx, f.t1, "A-NONE", agent.AgentPatch{}, f.t1.UserID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("miss: err = %v; want ErrNotFound", err)
	}

	// An agent with jobs cannot be deleted: the FK is RESTRICT and the store
	// answers with a clear conflict.
	if _, err := f.st.UpdateAgent(ctx, f.t1, a.ID, agent.AgentPatch{Enabled: boolptr(true)}, f.t1.UserID); err != nil {
		t.Fatalf("re-enable agent: %v", err)
	}
	j := f.queue(f.t1, a.ID, "must not outlive its agent")
	if err := f.st.DeleteAgent(ctx, f.t1, a.ID); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("delete agent with jobs: err = %v; want ErrConflict", err)
	}

	// ...and an agent that authored chat messages is still in use.
	th, err := f.st.EnsureItemThread(ctx, f.t1, f.itemFor(f.t1, f.pr1.ID, "chat item").ID, f.t1.UserID)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if _, err := f.st.AddMessage(ctx, f.t1, agent.NewMessage{
		ThreadID: th.ID, AuthorKind: agent.AuthorAgent, AuthorAgentID: a.ID, BodyMD: "on it",
	}); err != nil {
		t.Fatalf("agent message: %v", err)
	}
	if err := f.st.DeleteAgent(ctx, f.t1, a.ID); !errors.Is(err, agent.ErrConflict) {
		t.Fatalf("delete agent with messages: err = %v; want ErrConflict", err)
	}

	// A stranger's agent is invisible to T2, and deleting a real one works.
	if err := f.st.DeleteAgent(ctx, f.t2, a.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("T2 delete T1 agent: err = %v; want ErrNotFound", err)
	}

	// Requeue the queue so delete succeeds: cancel every remaining job, finish
	// none — cancelling moves queued/running out of the FK's way? No: RESTRICT
	// counts rows, not states, so terminal jobs still block. Test the clean path
	// instead: a fresh agent with no history deletes fine.
	clean := f.agent(f.t1, "temp")
	if err := f.st.DeleteAgent(ctx, f.t1, clean.ID); err != nil {
		t.Fatalf("delete clean agent: %v", err)
	}
	if _, err := f.st.GetAgent(ctx, f.t1, clean.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("deleted agent still readable: %v", err)
	}
	_ = j
}

func boolptr(b bool) *bool { return &b }

// scopedExec runs one statement inside a transaction with the tenant set: a
// bare Exec on the app role would match zero rows, because FORCE RLS hides
// every row without app.tenant_id.
func (f *fixture) scopedExec(sc tenancy.Scope, query string, args ...any) sql.Result {
	f.t.Helper()
	tx, err := f.app.BeginTx(context.Background(), nil)
	if err != nil {
		f.t.Fatalf("begin scoped exec: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, sc.TenantID); err != nil {
		f.t.Fatalf("set tenant scope: %v", err)
	}
	res, err := tx.Exec(query, args...)
	if err != nil {
		f.t.Fatalf("scoped exec: %v", err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatalf("commit scoped exec: %v", err)
	}
	return res
}

func TestEnsureItemThreadIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	it := f.itemFor(f.t1, f.pr1.ID, "discussed")
	first, err := f.st.EnsureItemThread(ctx, f.t1, it.ID, f.t1.UserID)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if first.ContextKind != agent.ContextItem || first.ItemID == nil || *first.ItemID != it.ID || first.ProductID != nil {
		t.Fatalf("thread shape: %+v", first)
	}

	for i := 0; i < 5; i++ {
		again, err := f.st.EnsureItemThread(ctx, f.t1, it.ID, f.t1.UserID)
		if err != nil {
			t.Fatalf("ensure #%d: %v", i, err)
		}
		if again.ID != first.ID {
			t.Fatalf("ensure #%d created a second thread: %s vs %s", i, again.ID, first.ID)
		}
	}

	if _, err := f.st.EnsureItemThread(ctx, f.t1, "A-NONE", f.t1.UserID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("ensure on missing item: err = %v; want ErrNotFound", err)
	}

	// CreateThread validates the context shape.
	if _, err := f.st.CreateThread(ctx, f.t1, agent.ContextItem, "", "", "", f.t1.UserID); !errors.Is(err, agent.ErrInvalid) {
		t.Fatalf("item thread without item: err = %v; want ErrInvalid", err)
	}
	if _, err := f.st.CreateThread(ctx, f.t1, agent.ContextFree, it.ID, "", "", f.t1.UserID); !errors.Is(err, agent.ErrInvalid) {
		t.Fatalf("free thread with item: err = %v; want ErrInvalid", err)
	}
	free, err := f.st.CreateThread(ctx, f.t1, agent.ContextFree, "", "", "scratch", f.t1.UserID)
	if err != nil {
		t.Fatalf("free thread: %v", err)
	}
	if free.Title != "scratch" || free.ItemID != nil || free.ProductID != nil {
		t.Fatalf("free thread shape: %+v", free)
	}
}

func TestMessagesAuthorShapeAndOrdering(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	th, err := f.st.CreateThread(ctx, f.t1, agent.ContextFree, "", "", "", f.t1.UserID)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	a := f.agent(f.t1, "dev")

	for _, bad := range []agent.NewMessage{
		{ThreadID: th.ID, AuthorKind: agent.AuthorHuman, BodyMD: "no user"},                                              // human without user
		{ThreadID: th.ID, AuthorKind: agent.AuthorHuman, AuthorUserID: f.t1.UserID, AuthorAgentID: a.ID, BodyMD: "both"}, // human with agent
		{ThreadID: th.ID, AuthorKind: agent.AuthorAgent, BodyMD: "no agent"},                                             // agent without agent
		{ThreadID: th.ID, AuthorKind: agent.AuthorSystem, AuthorUserID: f.t1.UserID, BodyMD: "system with user"},         // system with user
		{ThreadID: th.ID, AuthorKind: agent.AuthorHuman, AuthorUserID: f.t1.UserID, BodyMD: ""},                          // empty body
		{ThreadID: th.ID, AuthorKind: "robot", BodyMD: "x"},                                                              // unknown kind
	} {
		if _, err := f.st.AddMessage(ctx, f.t1, bad); !errors.Is(err, agent.ErrInvalid) {
			t.Fatalf("invalid message %+v: err = %v; want ErrInvalid", bad, err)
		}
	}

	first, err := f.st.AddMessage(ctx, f.t1, agent.NewMessage{
		ThreadID: th.ID, AuthorKind: agent.AuthorHuman, AuthorUserID: f.t1.UserID, BodyMD: "@dev, take this",
	})
	if err != nil {
		t.Fatalf("human message: %v", err)
	}
	second, err := f.st.AddMessage(ctx, f.t1, agent.NewMessage{
		ThreadID: th.ID, AuthorKind: agent.AuthorAgent, AuthorAgentID: a.ID, BodyMD: "working",
	})
	if err != nil {
		t.Fatalf("agent message: %v", err)
	}
	third, err := f.st.AddMessage(ctx, f.t1, agent.NewMessage{
		ThreadID: th.ID, AuthorKind: agent.AuthorSystem, BodyMD: "job queued",
	})
	if err != nil {
		t.Fatalf("system message: %v", err)
	}
	if !(first.Seq < second.Seq && second.Seq < third.Seq) {
		t.Fatalf("seqs not ordered: %d %d %d", first.Seq, second.Seq, third.Seq)
	}

	msgs, err := f.st.ListMessages(ctx, f.t1, th.ID, 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 3 || msgs[0].ID != first.ID || msgs[2].ID != third.ID {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[1].AuthorAgentID == nil || *msgs[1].AuthorAgentID != a.ID || msgs[1].AuthorUserID != nil {
		t.Fatalf("agent message author shape: %+v", msgs[1])
	}

	tail, err := f.st.ListMessages(ctx, f.t1, th.ID, second.Seq, 10)
	if err != nil {
		t.Fatalf("list after seq: %v", err)
	}
	if len(tail) != 1 || tail[0].ID != third.ID {
		t.Fatalf("after_seq returned %+v; want only the third message", tail)
	}
}

func TestJobLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a := f.agent(f.t1, "dev")
	th, err := f.st.CreateThread(ctx, f.t1, agent.ContextFree, "", "", "", f.t1.UserID)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	msg, err := f.st.AddMessage(ctx, f.t1, agent.NewMessage{
		ThreadID: th.ID, AuthorKind: agent.AuthorHuman, AuthorUserID: f.t1.UserID, BodyMD: "@dev build it",
	})
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	j, err := f.st.CreateJob(ctx, f.t1, agent.NewJob{
		AgentID: a.ID, ThreadID: th.ID, MessageID: msg.ID, Prompt: "build it",
		Context: json.RawMessage(`{"branch":"main"}`), CreatedBy: f.t1.UserID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if j.State != agent.JobQueued || j.Attempt != 0 {
		t.Fatalf("fresh job state: %+v", j)
	}

	// Unknown agent and disabled agent are rejected before anything is queued.
	if _, err := f.st.CreateJob(ctx, f.t1, agent.NewJob{AgentID: "A-NONE", Prompt: "x"}); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("unknown agent: err = %v; want ErrNotFound", err)
	}
	if _, err := f.st.UpdateAgent(ctx, f.t1, a.ID, agent.AgentPatch{Enabled: boolptr(false)}, f.t1.UserID); err != nil {
		t.Fatalf("disable agent: %v", err)
	}
	if _, err := f.st.CreateJob(ctx, f.t1, agent.NewJob{AgentID: a.ID, Prompt: "x"}); !errors.Is(err, agent.ErrDisabled) {
		t.Fatalf("disabled agent: err = %v; want ErrDisabled", err)
	}
	if _, err := f.st.UpdateAgent(ctx, f.t1, a.ID, agent.AgentPatch{Enabled: boolptr(true)}, f.t1.UserID); err != nil {
		t.Fatalf("re-enable agent: %v", err)
	}

	// Claim and heartbeat.
	tenantID, jobID, err := f.st.ClaimNextJob(ctx, "W-1", 5*time.Minute, nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if tenantID != f.t1.TenantID || jobID != j.ID {
		t.Fatalf("claim = %s/%s; want %s/%s", tenantID, jobID, f.t1.TenantID, j.ID)
	}
	if err := f.st.HeartbeatJob(ctx, f.t1, j.ID, "W-1", 10*time.Minute); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, _, err := f.st.ClaimNextJob(ctx, "W-1", 5*time.Minute, nil); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("claimed job is claimable again: err = %v; want ErrNotFound", err)
	}

	claimed, err := f.st.GetJob(ctx, f.t1, j.ID)
	if err != nil {
		t.Fatalf("get claimed: %v", err)
	}
	if claimed.State != agent.JobRunning || claimed.Attempt != 1 || claimed.StartedAt == nil {
		t.Fatalf("claimed job: %+v", claimed)
	}

	// A different worker cannot heartbeat or finish someone else's claim.
	if err := f.st.HeartbeatJob(ctx, f.t1, j.ID, "W-2", time.Minute); !errors.Is(err, agent.ErrNotRunning) {
		t.Fatalf("foreign heartbeat: err = %v; want ErrNotRunning", err)
	}
	if err := f.st.FinishJob(ctx, f.t1, j.ID, "W-2", agent.JobSucceeded, nil, "", ""); !errors.Is(err, agent.ErrNotRunning) {
		t.Fatalf("foreign finish: err = %v; want ErrNotRunning", err)
	}

	// Logs while running.
	first, err := f.st.AppendLog(ctx, f.t1, j.ID, agent.Event{
		Stream: "stdout", Level: "info", Kind: "output_chunk", Message: "compiling…",
	})
	if err != nil {
		t.Fatalf("append log 1: %v", err)
	}
	second, err := f.st.AppendLog(ctx, f.t1, j.ID, agent.Event{
		Stream: "engine", Level: "error", Kind: "tool_call",
		Data: json.RawMessage(`{"tool":"go build"}`),
	})
	if err != nil {
		t.Fatalf("append log 2: %v", err)
	}
	if !(first.Seq < second.Seq) {
		t.Fatalf("log seqs not ordered: %d %d", first.Seq, second.Seq)
	}

	logs, err := f.st.ListLogs(ctx, f.t1, j.ID, 0, 10)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) != 2 || logs[0].Kind != "output_chunk" || logs[1].Kind != "tool_call" {
		t.Fatalf("logs = %+v", logs)
	}
	tail, err := f.st.ListLogs(ctx, f.t1, j.ID, first.Seq, 10)
	if err != nil {
		t.Fatalf("list logs after seq: %v", err)
	}
	if len(tail) != 1 || tail[0].Seq != second.Seq {
		t.Fatalf("logs after seq = %+v", tail)
	}

	// Finish (with usage) and read the stored result back.
	cost := 0.012345
	if err := f.st.FinishJob(ctx, f.t1, j.ID, "W-1", agent.JobSucceeded, &agent.Result{
		Output: "done", TokensIn: 100, TokensOut: 42, CostUSD: &cost,
	}, "", ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	finished, err := f.st.GetJob(ctx, f.t1, j.ID)
	if err != nil {
		t.Fatalf("get finished: %v", err)
	}
	if finished.State != agent.JobSucceeded || finished.FinishedAt == nil {
		t.Fatalf("finished job: %+v", finished)
	}
	if finished.TokensIn != 100 || finished.TokensOut != 42 {
		t.Fatalf("tokens not stored: %+v", finished)
	}
	if finished.CostUSD == nil || math.Abs(*finished.CostUSD-cost) > 1e-9 {
		t.Fatalf("cost = %v; want %v", finished.CostUSD, cost)
	}
	var result map[string]any
	if err := json.Unmarshal(finished.Result, &result); err != nil {
		t.Fatalf("stored result: %v", err)
	}
	if result["output"] != "done" {
		t.Fatalf("result output = %v", result["output"])
	}

	// Terminal is terminal: cancel refuses, and finishing again is refused even
	// by the worker that did it.
	if _, err := f.st.CancelJob(ctx, f.t1, j.ID); !errors.Is(err, agent.ErrNotRunning) {
		t.Fatalf("cancel terminal: err = %v; want ErrNotRunning", err)
	}
	if err := f.st.FinishJob(ctx, f.t1, j.ID, "W-1", agent.JobSucceeded, nil, "", ""); !errors.Is(err, agent.ErrNotRunning) {
		t.Fatalf("re-finish: err = %v; want ErrNotRunning", err)
	}
	if len(logs) != 2 {
		t.Fatalf("logs changed after finish")
	}
}

func TestCancelBeatTheWorkerClaim(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a := f.agent(f.t1, "dev")
	j := f.queue(f.t1, a.ID, "cancel me")
	if _, _, err := f.st.ClaimNextJob(ctx, "W-1", 5*time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	cancelled, err := f.st.CancelJob(ctx, f.t1, j.ID)
	if err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	if cancelled.State != agent.JobCancelled || cancelled.FinishedAt == nil {
		t.Fatalf("cancelled job: %+v", cancelled)
	}

	// The stale worker's finish must find nothing: a cancelled job is never
	// resurrected.
	if err := f.st.FinishJob(ctx, f.t1, j.ID, "W-1", agent.JobSucceeded, &agent.Result{Output: "late"}, "", ""); !errors.Is(err, agent.ErrNotRunning) {
		t.Fatalf("finish after cancel: err = %v; want ErrNotRunning", err)
	}
	after, err := f.st.GetJob(ctx, f.t1, j.ID)
	if err != nil {
		t.Fatalf("get after cancel: %v", err)
	}
	if after.State != agent.JobCancelled {
		t.Fatalf("cancel was resurrected: %+v", after)
	}

	// Cancelling an unknown job is a miss, not a crash.
	if _, err := f.st.CancelJob(ctx, f.t1, "A-NONE"); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("cancel missing: err = %v; want ErrNotFound", err)
	}
}

func TestClaimCrossesTenants(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a1 := f.agent(f.t1, "dev")
	a2 := f.agent(f.t2, "dev")
	j1 := f.queue(f.t1, a1.ID, "t1 job")
	j2 := f.queue(f.t2, a2.ID, "t2 job")

	// The worker claims the oldest eligible job first — the T1 row, then the T2
	// row — through the definer surface, with no session of its own.
	t1, id1, err := f.st.ClaimNextJob(ctx, "W-1", 5*time.Minute, nil)
	if err != nil {
		t.Fatalf("claim 1: %v", err)
	}
	t2, id2, err := f.st.ClaimNextJob(ctx, "W-1", 5*time.Minute, nil)
	if err != nil {
		t.Fatalf("claim 2: %v", err)
	}
	if t1 == t2 {
		t.Fatalf("claims landed in the same tenant: %s, %s", t1, t2)
	}
	tenants := map[string]string{t1: id1, t2: id2}
	if tenants[f.t1.TenantID] != j1.ID || tenants[f.t2.TenantID] != j2.ID {
		t.Fatalf("claims = %s/%s, %s/%s; want %s and %s", t1, id1, t2, id2, j1.ID, j2.ID)
	}

	// Both are running exactly once.
	for _, sc := range []tenancy.Scope{f.t1, f.t2} {
		id := tenants[sc.TenantID]
		j, err := f.st.GetJob(ctx, sc, id)
		if err != nil {
			t.Fatalf("get claimed job in %s: %v", sc.TenantID, err)
		}
		if j.State != agent.JobRunning || j.Attempt != 1 {
			t.Fatalf("job %s: state %s attempt %d", id, j.State, j.Attempt)
		}
		if _, _, err := f.st.ClaimNextJob(ctx, "W-1", 5*time.Minute, nil); !errors.Is(err, agent.ErrNotFound) {
			t.Fatalf("queue should be drained: err = %v; want ErrNotFound", err)
		}
		break
	}
}

func TestSkipAgentsAndLeaseReaping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a := f.agent(f.t1, "dev")
	f.queue(f.t1, a.ID, "skipped")

	// skipAgents keeps the worker away from a saturated agent.
	if _, _, err := f.st.ClaimNextJob(ctx, "W-1", 5*time.Minute, []string{a.ID}); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("claim with skip: err = %v; want ErrNotFound", err)
	}
	_, jobID, err := f.st.ClaimNextJob(ctx, "W-1", 5*time.Minute, []string{"A-OTHER"})
	if err != nil {
		t.Fatalf("claim without skip: %v", err)
	}

	// Expire the lease by hand instead of sleeping a real second.
	f.scopedExec(f.t1, `UPDATE agent_jobs SET claim_expires_at = now() - interval '1 millisecond' WHERE id = $1`, jobID)

	reaped, err := f.st.ReapJobs(ctx, 3)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped %d jobs; want 1", reaped)
	}
	requeued, err := f.st.GetJob(ctx, f.t1, jobID)
	if err != nil {
		t.Fatalf("get requeued: %v", err)
	}
	if requeued.State != agent.JobQueued || requeued.Attempt != 1 {
		t.Fatalf("requeued job: %+v", requeued)
	}

	// The expired job is claimable again, and the attempt counter advanced.
	tenantID, jobID2, err := f.st.ClaimNextJob(ctx, "W-2", 5*time.Minute, nil)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if tenantID != f.t1.TenantID || jobID2 != jobID {
		t.Fatalf("re-claim = %s/%s", tenantID, jobID2)
	}

	// Attempt 2 of max 1: expiry is now terminal failure with the taxonomy
	// prescribed for lease loss.
	f.scopedExec(f.t1, `UPDATE agent_jobs SET claim_expires_at = now() - interval '1 millisecond' WHERE id = $1`, jobID)
	reaped, err = f.st.ReapJobs(ctx, 1)
	if err != nil {
		t.Fatalf("reap 2: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped %d jobs on second reap; want 1", reaped)
	}
	failed, err := f.st.GetJob(ctx, f.t1, jobID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if failed.State != agent.JobFailed || failed.FailureKind == nil || *failed.FailureKind != "transient" {
		t.Fatalf("failed job: %+v", failed)
	}
	if failed.ErrorText == nil || *failed.ErrorText != "lease expired" || failed.FinishedAt == nil {
		t.Fatalf("failure not recorded: %+v", failed)
	}
	if _, _, err := f.st.ClaimNextJob(ctx, "W-3", 5*time.Minute, nil); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("failed job claimable: err = %v; want ErrNotFound", err)
	}
}

func TestQueueCapPerTenant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a := f.agent(f.t1, "dev")
	for i := 0; i < agent.MaxQueuedPerTenant; i++ {
		if _, err := f.st.CreateJob(ctx, f.t1, agent.NewJob{AgentID: a.ID, Prompt: "job"}); err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
	}
	if _, err := f.st.CreateJob(ctx, f.t1, agent.NewJob{AgentID: a.ID, Prompt: "one too many"}); !errors.Is(err, agent.ErrQueueFull) {
		t.Fatalf("over cap: err = %v; want ErrQueueFull", err)
	}

	// Another tenant is unaffected: the cap is per tenant.
	a2 := f.agent(f.t2, "dev")
	if _, err := f.st.CreateJob(ctx, f.t2, agent.NewJob{AgentID: a2.ID, Prompt: "t2 is fine"}); err != nil {
		t.Fatalf("T2 job under T1 cap: %v", err)
	}

	// Draining the queue opens a slot again.
	if _, _, err := f.st.ClaimNextJob(ctx, "W-1", 5*time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := f.st.CreateJob(ctx, f.t1, agent.NewJob{AgentID: a.ID, Prompt: "slot opened"}); err != nil {
		t.Fatalf("after claim: %v", err)
	}
}

func TestJobLogsAreAppendOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a := f.agent(f.t1, "dev")
	j := f.queue(f.t1, a.ID, "logged")
	if _, err := f.st.AppendLog(ctx, f.t1, j.ID, agent.Event{Message: "hello"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// The app role holds no UPDATE/DELETE privilege at all.
	if _, err := f.app.Exec(`UPDATE job_logs SET message = 'tampered'`); err == nil {
		t.Fatal("app role could update job_logs")
	}
	if _, err := f.app.Exec(`DELETE FROM job_logs`); err == nil {
		t.Fatal("app role could delete from job_logs")
	}

	// Even through the owner — who does hold UPDATE — the append-only trigger
	// raises AIC05. The owner is bound by FORCE RLS, so the mutation happens in
	// a scoped transaction.
	db := f.db
	for _, stmt := range []string{
		`UPDATE job_logs SET message = 'tampered'`,
		`DELETE FROM job_logs`,
	} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(`SELECT set_config('app.tenant_id', 'T1', true)`); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if _, err := tx.Exec(stmt); err == nil {
			t.Fatalf("owner could %q", stmt)
		} else if !strings.Contains(err.Error(), "job_logs_append_only") {
			t.Fatalf("%q: err = %v; want job_logs_append_only", stmt, err)
		}
		tx.Rollback() //nolint:errcheck // the mutation must not land
	}

	// T2 still sees nothing of T1's logs.
	logs, err := f.st.ListLogs(ctx, f.t2, j.ID, 0, 10)
	if err != nil {
		t.Fatalf("list logs as T2: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("T2 sees %d T1 log rows", len(logs))
	}

	// Defaults: empty stream/level/kind become engine/info/output_chunk.
	e, err := f.st.AppendLog(ctx, f.t1, j.ID, agent.Event{Message: "bare"})
	if err != nil {
		t.Fatalf("append bare: %v", err)
	}
	if e.Stream != "engine" || e.Level != "info" || e.Kind != "output_chunk" {
		t.Fatalf("defaults: %+v", e)
	}
}

func TestThreadJobListing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	a := f.agent(f.t1, "dev")
	th, err := f.st.CreateThread(ctx, f.t1, agent.ContextFree, "", "", "", f.t1.UserID)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	j1, err := f.st.CreateJob(ctx, f.t1, agent.NewJob{AgentID: a.ID, ThreadID: th.ID, Prompt: "first"})
	if err != nil {
		t.Fatalf("job 1: %v", err)
	}
	j2, err := f.st.CreateJob(ctx, f.t1, agent.NewJob{AgentID: a.ID, ThreadID: th.ID, Prompt: "second"})
	if err != nil {
		t.Fatalf("job 2: %v", err)
	}

	jobs, err := f.st.ListJobsByThread(ctx, f.t1, th.ID, 10)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != j2.ID || jobs[1].ID != j1.ID {
		t.Fatalf("jobs newest-first: %+v", jobs)
	}

	// Listing an unrelated thread is empty, and T2 sees nothing.
	other, err := f.st.CreateThread(ctx, f.t1, agent.ContextFree, "", "", "", f.t1.UserID)
	if err != nil {
		t.Fatalf("thread 2: %v", err)
	}
	jobs, err = f.st.ListJobsByThread(ctx, f.t1, other.ID, 10)
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("unrelated thread has jobs: %+v", jobs)
	}
	jobs, err = f.st.ListJobsByThread(ctx, f.t2, th.ID, 10)
	if err != nil {
		t.Fatalf("list as T2: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("T2 sees T1 jobs: %+v", jobs)
	}
}
