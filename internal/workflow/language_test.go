package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The resolution is a chain: the request may name a language, otherwise the
// company's own language is the run's. An unknown language is refused rather
// than ignored, because a run that quietly fell back to English would answer
// in a language nobody asked for.
func TestResolveLanguagePrefersTheRequestThenTheTenant(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		tenant    string
		want      tenancy.Language
	}{
		{"the request wins", "pt-BR", "en", tenancy.LanguagePortugueseBrazil},
		{"the tenant is the default", "", "pt-BR", tenancy.LanguagePortugueseBrazil},
		{"english is the last resort", "", "", tenancy.LanguageEnglish},
		{"surrounding space is ignored", "  pt-BR  ", "en", tenancy.LanguagePortugueseBrazil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveLanguage(tc.requested, tc.tenant)
			if err != nil {
				t.Fatalf("resolve %q/%q: %v", tc.requested, tc.tenant, err)
			}
			if got != tc.want {
				t.Errorf("resolved %q, want %q", got, tc.want)
			}
		})
	}

	for _, tc := range []struct{ requested, tenant string }{
		{"fr", "en"},
		{"", "fr"},
		{"pt", "en"},
	} {
		if _, err := ResolveLanguage(tc.requested, tc.tenant); !errors.Is(err, ErrUnknownLanguage) {
			t.Errorf("resolve %q/%q = %v, want ErrUnknownLanguage", tc.requested, tc.tenant, err)
		}
	}
}

// The directive names the language and says what it covers. An agent that
// writes its summary in one language and its commit messages in another has
// not been told anything useful.
func TestLanguageInstructionNamesTheLanguageAndEveryOutput(t *testing.T) {
	instruction := LanguageInstruction(tenancy.LanguagePortugueseBrazil)
	if !strings.Contains(instruction, "Portuguese (Brazil)") {
		t.Errorf("the instruction does not name the language: %s", instruction)
	}
	for _, want := range []string{
		"your answers",
		"the documents you write",
		"the pull-request title and body",
		"the comments you leave",
		"your commit messages",
	} {
		if !strings.Contains(instruction, want) {
			t.Errorf("the instruction does not cover %q: %s", want, instruction)
		}
	}

	english := LanguageInstruction(tenancy.LanguageEnglish)
	if !strings.Contains(english, "English") || strings.Contains(english, "Portuguese") {
		t.Errorf("the English instruction names the wrong language: %s", english)
	}

	// An unknown language falls back to the default rather than naming a
	// language nobody asked for.
	if !strings.Contains(LanguageInstruction(tenancy.Language("fr")), "English") {
		t.Error("an unknown language did not fall back to the default")
	}
}

// The instruction lives in the soul — the trusted part of the context — so the
// work item's text cannot forge it, and the agent's own voice is kept.
func TestSoulWithLanguageKeepsThePersonaAndTheDirective(t *testing.T) {
	soul := soulWithLanguage("Always be helpful.", tenancy.LanguagePortugueseBrazil)
	if !strings.Contains(soul, "Always be helpful.") {
		t.Errorf("the agent's own soul was lost: %s", soul)
	}
	if !strings.Contains(soul, "Your work language is Portuguese (Brazil).") {
		t.Errorf("the language directive is missing: %s", soul)
	}
	if !strings.Contains(soul, "your commit messages") {
		t.Errorf("the directive does not cover the commit messages: %s", soul)
	}
}

// A run remembers the language it started in: a company that changes its
// language later does not change the language of a run already under way.
func TestRunKeepsTheLanguageItStartedIn(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()

	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "language", Graph: agentGraph(agent.Agent{ID: "A"}), CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}

	// The company's language, and the run that starts in it.
	tenancyStore := tenancy.NewStore(f.db)
	if _, err := tenancyStore.CreateSession(ctx, "ENG-U1", "ENG-T1", "tok-eng", time.Hour); err != nil {
		t.Fatalf("session: %v", err)
	}
	if err := tenancyStore.SetTenantLanguage(ctx, "tok-eng", tenancy.LanguagePortugueseBrazil); err != nil {
		t.Fatalf("set the company language: %v", err)
	}
	companyLanguage, err := tenancyStore.TenantLanguage(ctx, "tok-eng")
	if err != nil {
		t.Fatalf("read the company language: %v", err)
	}

	resolved, err := ResolveLanguage("", string(companyLanguage))
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.store.CreateRun(ctx, f.sc, NewRun{TemplateID: tpl.ID, TemplateVersion: tpl.Version,
		DefinitionSnapshot: tpl.GraphJSON, Language: resolved, StartedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if run.Language != tenancy.LanguagePortugueseBrazil {
		t.Fatalf("the run was created in %q, want pt-BR", run.Language)
	}

	// The company changes its language while the run is under way.
	if err := tenancyStore.SetTenantLanguage(ctx, "tok-eng", tenancy.LanguageEnglish); err != nil {
		t.Fatalf("change the company language: %v", err)
	}
	after, err := f.store.GetRun(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Language != tenancy.LanguagePortugueseBrazil {
		t.Errorf("the run's language changed to %q; a run keeps the language it started in", after.Language)
	}
}

// An unknown language never reaches the row.
func TestRunRefusesAnUnknownLanguage(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "language", Graph: agentGraph(agent.Agent{ID: "A"}), CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateRun(ctx, f.sc, NewRun{TemplateID: tpl.ID, TemplateVersion: tpl.Version,
		DefinitionSnapshot: tpl.GraphJSON, Language: tenancy.Language("fr")}); !errors.Is(err, ErrUnknownLanguage) {
		t.Errorf("an unknown language was stored: %v", err)
	}
	// A run without a language speaks the default.
	run, err := f.store.CreateRun(ctx, f.sc, NewRun{TemplateID: tpl.ID, TemplateVersion: tpl.Version, DefinitionSnapshot: tpl.GraphJSON})
	if err != nil {
		t.Fatal(err)
	}
	if run.Language != tenancy.LanguageEnglish {
		t.Errorf("the default language is %q, want en", run.Language)
	}
}

// The agents are told. The directive is in the soul — the trusted part of the
// context — so the work item's text cannot forge it, and the budget cannot
// trim it away.
func TestEngineTellsEveryAgentTheRunsLanguage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		language tenancy.Language
		want     string
	}{
		{"the company's language", tenancy.LanguagePortugueseBrazil, "Your work language is Portuguese (Brazil)."},
		{"the request's override", tenancy.LanguageEnglish, "Your work language is English."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEngineFixture(t)
			ctx := context.Background()
			a := f.createAgent(t)
			run := f.createRunWithLanguage(t, agentGraph(a), tc.language)
			e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			if err := e.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			first, ok := latestStep(steps, "first", "")
			if !ok || first.AgentJobID == "" {
				t.Fatalf("the agent did not run: %+v", first)
			}
			job, err := f.agents.GetJob(ctx, f.sc, first.AgentJobID)
			if err != nil {
				t.Fatal(err)
			}

			// The directive is in the prompt.
			if !strings.Contains(job.Prompt, tc.want) {
				t.Fatalf("the prompt does not carry the language directive %q:\n%s", tc.want, job.Prompt)
			}
			// It is in the soul, which is the trusted part: it comes before the
			// task file, where the untrusted sections live.
			directiveAt := strings.Index(job.Prompt, tc.want)
			taskFileAt := strings.Index(job.Prompt, "# Task file")
			if taskFileAt < 0 || directiveAt < 0 || directiveAt > taskFileAt {
				t.Fatalf("the directive is not in the trusted soul:\n%s", job.Prompt)
			}
			// The agent's own voice is still there.
			if !strings.Contains(job.Prompt, "Always be helpful.") {
				t.Errorf("the agent's own soul was lost:\n%s", job.Prompt)
			}
			// And the step records the language the context was built in.
			var manifest struct {
				Language tenancy.Language `json:"language"`
			}
			if err := json.Unmarshal(first.ContextManifest, &manifest); err != nil {
				t.Fatalf("decode the context manifest: %v", err)
			}
			if manifest.Language != tc.language {
				t.Errorf("the context manifest says %q, want %q", manifest.Language, tc.language)
			}
		})
	}
}

// The instruction is the run's, not one node's: every agent of the run is told
// the same thing.
func TestEngineTellsEveryNodeTheSameLanguage(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	a := f.createAgent(t)
	graph := Template{SchemaVersion: 1, Nodes: []Node{
		{ID: "first", Kind: NodeAgent, Name: "first step", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"first"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
		{ID: "second", Kind: NodeAgent, Name: "second step", Type: "agent.worker", TypeVersion: 1, Config: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"prompt_tmpl":"second"}`, a.ID)), OnError: OnErrorStop, Workspace: WorkspaceScratch},
	}, Edges: []Edge{{ID: "first-second", From: PortRef{Node: "first", Port: "main"}, To: PortRef{Node: "second", Port: "in"}}}}
	run := f.createRunWithLanguage(t, graph, tenancy.LanguagePortugueseBrazil)
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, err := f.store.ListStepRuns(ctx, f.sc, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := latestStep(steps, "first", "")
	job, err := f.agents.GetJob(ctx, f.sc, first.AgentJobID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(job.Prompt, "Your work language is Portuguese (Brazil).") {
		t.Fatalf("the first node's agent was not told the language:\n%s", job.Prompt)
	}
	// The second node is dispatched from the same run, so it is told the same
	// thing: the language is a property of the run.
	second, ok := latestStep(steps, "second", "")
	if ok && second.AgentJobID != "" {
		other, err := f.agents.GetJob(ctx, f.sc, second.AgentJobID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(other.Prompt, "Your work language is Portuguese (Brazil).") {
			t.Fatalf("the second node's agent was not told the language:\n%s", other.Prompt)
		}
	}
}

// createRunWithLanguage starts a run in a given language, the way a request
// that names one does.
func (f *engineFixture) createRunWithLanguage(t *testing.T, graph Template, language tenancy.Language) Run {
	t.Helper()
	tpl, err := f.store.CreateTemplate(context.Background(), f.sc, NewTemplate{Name: "engine-language", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	run, err := e.StartRun(context.Background(), f.sc, tpl.ID, "", f.sc.UserID, WithRunLanguage(language))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// The engine refuses to start a run in a language the product does not speak.
func TestEngineRefusesAnUnknownRunLanguage(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	tpl, err := f.store.CreateTemplate(ctx, f.sc, NewTemplate{Name: "engine-language", Graph: agentGraph(agent.Agent{ID: "A"}), CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work})
	if _, err := e.StartRun(ctx, f.sc, tpl.ID, "", f.sc.UserID, WithRunLanguage(tenancy.Language("fr"))); !errors.Is(err, ErrUnknownLanguage) {
		t.Errorf("an unknown language was accepted: %v", err)
	}
}

// The agent job's prompt is the single source of truth: the engine's context
// is the only place the language is stated.
func TestAgentJobCarriesTheLanguageInItsContext(t *testing.T) {
	f := newEngineFixture(t)
	ctx := context.Background()
	a := f.createAgent(t)
	run := f.createRunWithLanguage(t, agentGraph(a), tenancy.LanguagePortugueseBrazil)
	e := NewEngine(EngineDeps{Store: f.store, Agents: f.agents, Runners: f.runners, Memory: memory.NewStore(f.db), Work: f.work})
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	steps, _ := f.store.ListStepRuns(ctx, f.sc, run.ID)
	first, _ := latestStep(steps, "first", "")
	job, err := f.agents.GetJob(ctx, f.sc, first.AgentJobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Prompt == "" {
		t.Fatal("the job carries no prompt")
	}
	if !strings.Contains(job.Prompt, "commit messages") {
		t.Errorf("the job's prompt does not state what the language covers:\n%s", job.Prompt)
	}
}
