package ctxbundle

import (
	"strings"
	"testing"
)

func TestAssembleOrdering(t *testing.T) {
	bundle, err := Assemble(AssembleInput{
		Soul:   "SOUL-FIRST",
		Skills: []Skill{{Slug: "go", Version: 4, Content: "SKILL-SECOND"}},
		Memory: []MemoryEntry{
			{Scope: "workspace", Key: "w", Content: "MEM-WORKSPACE"},
			{Scope: "agent", Key: "a", Content: "MEM-AGENT"},
			{Scope: "item", Key: "i", Content: "MEM-ITEM"},
			{Scope: "run", Key: "r", Content: "MEM-RUN"},
		},
		Task:     "TASK-TRUSTED",
		Sections: []Section{{Name: "issue", Trust: TrustUntrusted, Content: "TASK-UNTRUSTED"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ordered := []string{"SOUL-FIRST", "SKILL-SECOND", "MEM-RUN", "MEM-ITEM", "MEM-AGENT", "MEM-WORKSPACE", "TASK-TRUSTED", "TASK-UNTRUSTED"}
	last := -1
	for _, fragment := range ordered {
		position := strings.Index(bundle.Prompt, fragment)
		if position < 0 || position <= last {
			t.Fatalf("prompt ordering: %q at %d after %d\n%s", fragment, position, last, bundle.Prompt)
		}
		last = position
	}
}

func TestAssembleBudgetDropsWholeEntriesInPrecedence(t *testing.T) {
	bundle, err := Assemble(AssembleInput{
		Soul: "S", Budget: 1,
		Skills: []Skill{{Slug: "one", Version: 1, Content: "SKILL1"}, {Slug: "two", Version: 2, Content: "SKILL2"}},
		Memory: []MemoryEntry{
			{Scope: "run", Key: "run", Content: "MEMRUN"},
			{Scope: "item", Key: "item", Content: "MEMITEM"},
			{Scope: "agent", Key: "agent", Content: "MEMAGENT"},
			{Scope: "workspace", Key: "workspace", Content: "MEMWORK"},
		},
		Task:     "TRUSTEDTASK",
		Sections: []Section{{Name: "external", Trust: TrustUntrusted, Content: "UNTRUSTEDBLOCK"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bundle.Prompt, "S") || strings.Contains(bundle.Prompt, "SKILL1") || strings.Contains(bundle.Prompt, "SKILL2") ||
		strings.Contains(bundle.Prompt, "MEMRUN") || strings.Contains(bundle.Prompt, "MEMITEM") || strings.Contains(bundle.Prompt, "MEMAGENT") ||
		strings.Contains(bundle.Prompt, "MEMWORK") || strings.Contains(bundle.Prompt, "UNTRUSTEDBLOCK") || strings.Contains(bundle.Prompt, "TRUSTEDTASK") {
		t.Fatalf("expected soul only after trimming whole entries:\n%s", bundle.Prompt)
	}
	if len(bundle.Trimmed) != 8 {
		t.Fatalf("expected 8 trimmed entries, got %#v", bundle.Trimmed)
	}
	for _, note := range bundle.Trimmed {
		if note.Reason != "budget" {
			t.Errorf("trim reason = %q, want budget", note.Reason)
		}
	}
	if bundle.Trimmed[0].Kind != "skill" || bundle.Trimmed[1].Kind != "skill" || bundle.Trimmed[2].Scope != "workspace" ||
		bundle.Trimmed[6].Kind != "section" || bundle.Trimmed[7].Kind != "section" {
		t.Fatalf("unexpected trim precedence: %#v", bundle.Trimmed)
	}
}

func TestAssembleManifestAndReproducibility(t *testing.T) {
	in := AssembleInput{
		Soul: "soul", SoulRevision: 7,
		Skills: []Skill{{Slug: "go", Version: 4, Content: "skill"}},
		Memory: []MemoryEntry{{Scope: "item", Ref: "memory:1", Key: "fact", Revision: 3, Content: "memory"}},
		Task:   "task",
		Sections: []Section{
			{Name: "trusted-envelope", Trust: TrustTrusted, Ref: "item:1", Content: "envelope"},
			{Name: "external", Trust: TrustUntrusted, Ref: "github#1", Content: "body", Provenance: Provenance{System: "github", Ref: "acme/api#1", Author: "external", At: "2026-10-20T11:02:00Z"}},
		},
	}
	first, err := Assemble(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Assemble(in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Prompt != second.Prompt || first.TaskFileSHA != second.TaskFileSHA {
		t.Fatal("identical input produced different prompt or task-file hash")
	}
	if len(first.Manifest) != 6 || len(first.MemoryManifest) != 1 {
		t.Fatalf("manifest lengths = %d and %d", len(first.Manifest), len(first.MemoryManifest))
	}
	wantKinds := []string{"soul", "skill", "memory", "task_file", "task_file", "task_file"}
	for i, kind := range wantKinds {
		if first.Manifest[i].Kind != kind {
			t.Errorf("manifest[%d].Kind = %q, want %q", i, first.Manifest[i].Kind, kind)
		}
	}
	if first.Manifest[0].Revision != 7 || first.Manifest[3].Trust != TrustTrusted || first.Manifest[5].Trust != TrustUntrusted {
		t.Fatalf("manifest metadata missing: %#v", first.Manifest)
	}
	if first.Manifest[0].Tokens != estimateTokens(in.Soul) || first.Manifest[2].Tokens != estimateTokens("memory") {
		t.Fatalf("manifest token estimates incorrect: %#v", first.Manifest)
	}
	changed := in
	changed.Sections = append([]Section(nil), in.Sections...)
	changed.Sections[1].Content = "changed body"
	third, err := Assemble(changed)
	if err != nil {
		t.Fatal(err)
	}
	if first.TaskFileSHA == third.TaskFileSHA {
		t.Fatal("changing a section did not change task-file hash")
	}
}

func TestAssembleHonorsMemoryScopeCap(t *testing.T) {
	bundle, err := Assemble(AssembleInput{
		Soul: "s", Caps: map[string]int{"run": 1},
		Memory: []MemoryEntry{{Scope: "run", Key: "first", Content: "1234"}, {Scope: "run", Key: "second", Content: "5678"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.MemoryManifest) != 1 || bundle.MemoryManifest[0].Key != "first" {
		t.Fatalf("run cap not honored: %#v", bundle.MemoryManifest)
	}
	if len(bundle.Trimmed) != 1 || bundle.Trimmed[0].Key != "second" || bundle.Trimmed[0].Reason != "budget" {
		t.Fatalf("cap drop not recorded: %#v", bundle.Trimmed)
	}
}

func TestTaskFileJSONTrustOrderAndPreamble(t *testing.T) {
	bytes, err := TaskFileJSON([]Section{
		{Name: "external", Trust: TrustUntrusted, Content: "data"},
		{Name: "task", Trust: TrustTrusted, Content: "instruction"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(bytes)
	if strings.Index(text, `"name":"task"`) > strings.Index(text, `"name":"external"`) || !strings.Contains(text, taskFilePreamble) {
		t.Fatalf("task file order or preamble incorrect: %s", text)
	}
}
