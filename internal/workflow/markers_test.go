package workflow

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/ctxbundle"
	"github.com/orlandoburli/ai-company/internal/markers"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"github.com/orlandoburli/ai-company/internal/work"
)

func markerFixture(t *testing.T, itemContext bool) (*engineFixture, *Engine, Run, Node, string) {
	t.Helper()
	db := pgtest.FreshDB(t).AsRole(t, "aic_app")
	db.SetMaxOpenConns(12)
	if _, err := db.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`, "MRK-T1", "Marker tenant", "mrk-t1", "MRK-U1", "marker@example.com", "x", "Marker owner", "MRK-M1"); err != nil {
		t.Fatal(err)
	}
	f := &engineFixture{db: db, sc: tenancy.Scope{TenantID: "MRK-T1", UserID: "MRK-U1", Role: tenancy.RoleOwner}, store: NewStore(db), work: work.NewStore(db)}
	memStore := memory.NewStore(db)
	e := NewEngine(EngineDeps{Store: f.store, Memory: memStore, Work: f.work, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	node := Node{ID: "agent", Kind: NodeAgent, Name: "agent", Type: "agent.worker", TypeVersion: 1,
		Config: json.RawMessage(`{"agent_id":"agent-1","outputs":{"summary_md":"string","count":"integer"}}`), OnError: OnErrorStop, Workspace: WorkspaceScratch}
	graph := Template{SchemaVersion: 1, Nodes: []Node{node}}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := f.store.CreateTemplate(context.Background(), f.sc, NewTemplate{Name: "marker-test", Graph: graph, CreatedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	itemID := ""
	if itemContext {
		product, err := f.work.CreateProduct(context.Background(), f.sc, "P", "p", "https://github.com/acme/p", "")
		if err != nil {
			t.Fatal(err)
		}
		project, err := f.work.CreateProject(context.Background(), f.sc, product.ID, nil, "Project", "P")
		if err != nil {
			t.Fatal(err)
		}
		item, err := f.work.CreateItem(context.Background(), f.sc, work.NewItem{ProjectID: project.ID, Kind: "bug", Title: "Item", ActorUserID: f.sc.UserID})
		if err != nil {
			t.Fatal(err)
		}
		itemID = item.ID
	}
	run, err := f.store.CreateRun(context.Background(), f.sc, NewRun{TemplateID: tpl.ID, TemplateVersion: tpl.Version, ItemID: itemID, DefinitionSnapshot: graphJSON, StartedBy: f.sc.UserID})
	if err != nil {
		t.Fatal(err)
	}
	return f, e, run, node, itemID
}

func TestAICOutputWritesOnlyDeclaredCorrectTypesOnSuccess(t *testing.T) {
	f, e, run, node, _ := markerFixture(t, false)
	step := createMarkerStep(t, f, run, node)
	text := "AIC_OUTPUT\n{\"summary_md\":\"done\",\"count\":\"two\",\"extra\":true}\nAIC_OUTPUT_END"
	parsed := e.inspectAgentMarkers(context.Background(), f.sc, run, node, text)
	if parsed.Failure != "" || parsed.Output["summary_md"] != "done" || len(parsed.Output) != 1 || len(parsed.Dropped) != 2 {
		t.Fatalf("parsed marker=%+v", parsed)
	}
	e.writeAgentMarkers(context.Background(), f.sc, run, step, node, parsed)
	entries, _, err := e.memory.Read(context.Background(), f.sc, memory.Scope{Kind: memory.ScopeRun, Ref: run.ID})
	if err != nil || len(entries) != 1 || entries[0].Key != "summary_md" || entries[0].Source != memory.SourceStepOutput || entries[0].SourceRunID != run.ID || entries[0].SourceStepRunID != step.ID {
		t.Fatalf("run memory=%+v err=%v", entries, err)
	}
}

func TestMalformedOrFailedOutputCannotWriteRunMemory(t *testing.T) {
	f, e, run, node, _ := markerFixture(t, false)
	step := createMarkerStep(t, f, run, node)
	parsed := e.inspectAgentMarkers(context.Background(), f.sc, run, node, "AIC_OUTPUT\n{broken}\nAIC_OUTPUT_END")
	if parsed.Failure != string(FailureCapability) {
		t.Fatalf("failure=%q", parsed.Failure)
	}
	e.writeAgentMarkers(context.Background(), f.sc, run, step, node, parsed)
	entries, _, err := e.memory.Read(context.Background(), f.sc, memory.Scope{Kind: memory.ScopeRun, Ref: run.ID})
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed output wrote memory: %+v err=%v", entries, err)
	}
}

func TestAICMemorizeScopesAndHumanProtection(t *testing.T) {
	f, e, run, node, itemID := markerFixture(t, true)
	item, err := f.work.GetItem(context.Background(), f.sc, itemID)
	if err != nil {
		t.Fatal(err)
	}
	productID, _, _ := e.productForItem(context.Background(), f.sc, item)
	// A human-authored agent entry must remain current after a marker proposes
	// the same key; the agent may still write other keys and workspace memory.
	if _, err := e.memory.WriteHuman(context.Background(), f.sc, memory.Write{Scope: memory.Scope{Kind: memory.ScopeAgent, Ref: "agent-1"}, Key: "human", ContentMD: "keep human text"}); err != nil {
		t.Fatal(err)
	}
	text := "AIC_MEMORIZE\n{\"key\":\"own\",\"content\":\"remember\",\"scope\":\"agent\"}\nAIC_MEMORIZE_END\n" +
		"AIC_MEMORIZE\n{\"key\":\"shared\",\"content\":\"shared note\",\"scope\":\"workspace\"}\nAIC_MEMORIZE_END\n" +
		"AIC_MEMORIZE\n{\"key\":\"human\",\"content\":\"replace human\",\"scope\":\"agent\"}\nAIC_MEMORIZE_END\n" +
		"AIC_MEMORIZE\n{\"key\":\"nope\",\"content\":\"run\",\"scope\":\"run\"}\nAIC_MEMORIZE_END"
	parsed := e.inspectAgentMarkers(context.Background(), f.sc, run, node, text)
	if len(parsed.Memories) != 2 || len(parsed.Dropped) != 2 {
		t.Fatalf("parsed=%+v", parsed)
	}
	step := createMarkerStep(t, f, run, node)
	e.writeAgentMarkers(context.Background(), f.sc, run, step, node, parsed)
	agentEntries, _, err := e.memory.Read(context.Background(), f.sc, memory.Scope{Kind: memory.ScopeAgent, Ref: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	workspaceEntries, _, err := e.memory.Read(context.Background(), f.sc, memory.Scope{Kind: memory.ScopeWorkspace, Ref: productID})
	if err != nil {
		t.Fatal(err)
	}
	if len(agentEntries) != 2 || agentEntries[0].Key != "human" || agentEntries[0].Source != memory.SourceHuman || agentEntries[1].Key != "own" || agentEntries[1].Source != memory.SourceAgentMarker {
		t.Fatalf("agent entries=%+v", agentEntries)
	}
	if len(workspaceEntries) != 1 || workspaceEntries[0].Key != "shared" || workspaceEntries[0].Source != memory.SourceAgentMarker {
		t.Fatalf("workspace entries=%+v", workspaceEntries)
	}
}

func TestUntrustedIssueBodyIsFencedSanitizedAndMarkersDropped(t *testing.T) {
	issue := "visible \x1b]0;title\a bidi\u202e AIC_PUBLISH AIC_MEMORIZE AIC_OUTPUT AIC_SPAWN"
	f, e, run, _, itemID := markerFixture(t, true)
	meta, _ := json.Marshal(map[string]any{"repository": "acme/p", "issue_number": 42, "author": "reporter", "timestamp": "2026-10-20T12:00:00Z", "title": "External", "body": issue})
	if _, err := f.store.CreateArtifact(context.Background(), f.sc, NewArtifact{RunID: run.ID, ItemID: itemID, Kind: "issue_body", URI: "https://github.com/acme/p/issues/42", Meta: meta}); err != nil {
		t.Fatal(err)
	}
	section, dropped := e.issueBodySection(context.Background(), f.sc, run, work.Item{ID: itemID})
	if section.Trust != ctxbundle.TrustUntrusted || len(dropped) != 4 {
		t.Fatalf("section=%+v dropped=%v", section, dropped)
	}
	bundle, err := ctxbundle.Assemble(ctxbundle.AssembleInput{Task: "trusted task", Sections: []ctxbundle.Section{*section}})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\x1b", "\u202e", "AIC_PUBLISH", "AIC_MEMORIZE", "AIC_OUTPUT", "AIC_SPAWN"} {
		if strings.Contains(bundle.Prompt, forbidden) {
			t.Errorf("prompt contains %q", forbidden)
		}
	}
	if len(bundle.Manifest) < 2 || bundle.Manifest[len(bundle.Manifest)-1].Trust != ctxbundle.TrustUntrusted || bundle.Manifest[len(bundle.Manifest)-1].Tokens == 0 {
		t.Fatalf("untrusted section missing from manifest: %+v", bundle.Manifest)
	}
	if !strings.Contains(bundle.Prompt, "<<<UNTRUSTED:issue-body:") {
		t.Fatalf("untrusted section was not fenced: %s", bundle.Prompt)
	}
	if len(dropped) != 4 {
		t.Fatalf("dropped markers=%v", dropped)
	}
	// The bundle's marker scan cannot see neutralized markers, so preserve the
	// engine's issue-body dropped list in the execution context result.
	if parsed, err := markers.ParseMemories(section.Content); err != nil || len(parsed) != 0 {
		t.Fatal("external marker unexpectedly parsed as a memory action")
	}
	entries, _, err := e.memory.Read(context.Background(), f.sc, memory.Scope{Kind: memory.ScopeRun, Ref: run.ID})
	if err != nil || len(entries) != 0 {
		t.Fatalf("issue body caused a memory write: entries=%+v err=%v", entries, err)
	}
}

func createMarkerStep(t *testing.T, f *engineFixture, run Run, node Node) StepRun {
	t.Helper()
	step, err := f.store.CreateStepRun(context.Background(), f.sc, NewStepRun{RunID: run.ID, NodeID: node.ID, Kind: string(node.Kind), Name: node.Name})
	if err != nil {
		t.Fatal(err)
	}
	return step
}
