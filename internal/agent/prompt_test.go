package agent

import (
	"strings"
	"testing"
)

func TestParseMentions(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"@dev please look", []string{"dev"}},
		{"hey @dev, and @Reviewer too", []string{"dev", "reviewer"}},
		{"@dev @dev @dev", []string{"dev"}},
		{"mail dev@example.com", nil},
		{"user.name+tag@example.com", nil},
		{"(@qa) check this", []string{"qa"}},
		{"no mention here", nil},
		{"@dev-run_2", []string{"dev-run_2"}},
		{"text@dev", nil},
	}
	for _, c := range cases {
		got := ParseMentions(c.body)
		if len(got) != len(c.want) {
			t.Errorf("ParseMentions(%q) = %v, want %v", c.body, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseMentions(%q) = %v, want %v", c.body, got, c.want)
				break
			}
		}
	}
}

func TestBuildChatPromptOrdering(t *testing.T) {
	soul := "You are terse and precise."
	ctx := PromptContext{
		AgentName:       "dev",
		AgentRole:       RoleDev,
		ItemID:          "I-1",
		ItemTitle:       "Fix   the\nlogin redirect",
		ItemStatus:      "in_progress",
		ItemKind:        "task",
		ItemPriority:    "p1",
		ItemDescription: "Users land on a blank page.",
	}
	history := []Message{
		{AuthorKind: AuthorSystem, BodyMD: "Thread opened."},
		{AuthorKind: AuthorHuman, BodyMD: "@dev why?"},
	}
	out := BuildChatPrompt(soul, ctx, history)

	for _, want := range []string{
		"# Persona",
		soul,
		"# Context",
		"Item: I-1",
		"Title: Fix the login redirect",
		"Status: in_progress",
		"Priority: p1",
		"Users land on a blank page.",
		"# Conversation",
		"## System",
		"## Human",
		"@dev why?",
		"# Instruction",
		"Reply as dev",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q:\n%s", want, out)
		}
	}

	// The instruction is last: nothing after it can be read as a new task.
	instruction := strings.Index(out, "# Instruction")
	if instruction < strings.Index(out, "# Conversation") {
		t.Error("instruction must come after the conversation")
	}
	if strings.TrimSpace(out) == "" {
		t.Error("prompt should not be empty")
	}
}

func TestBuildChatPromptEmptySoulFallback(t *testing.T) {
	out := BuildChatPrompt("   ", PromptContext{AgentName: "pm", AgentRole: RolePM}, nil)
	if !strings.Contains(out, "You are pm, a pm agent") {
		t.Errorf("expected role fallback persona, got:\n%s", out)
	}
	if !strings.Contains(out, "(no earlier messages)") {
		t.Errorf("expected empty-history marker, got:\n%s", out)
	}
}

func TestBuildChatPromptTrimsOldHistory(t *testing.T) {
	long := strings.Repeat("x", maxMessageChars)
	history := []Message{
		{AuthorKind: AuthorHuman, BodyMD: "oldest " + long},
		{AuthorKind: AuthorHuman, BodyMD: "newest"},
	}
	out := BuildChatPrompt("soul", PromptContext{AgentName: "dev", AgentRole: RoleDev}, history)
	if !strings.Contains(out, "newest") {
		t.Error("latest message must survive trimming")
	}
	if strings.Contains(out, strings.Repeat("x", maxMessageChars)) {
		t.Error("oversized message should be truncated")
	}
}
