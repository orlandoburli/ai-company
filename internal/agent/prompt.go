package agent

import (
	"fmt"
	"strings"
)

// Chat prompt assembly for M2.
//
// This is the small end of the context discipline in docs/mvp-design.md §6:
// the soul is always present and never trimmed, item context is trusted
// (it is the tenant's own data), thread history is bounded by characters, and
// the latest human message is last so nothing after it can act as an
// instruction. The full soul → skills → memory → task-file bundle with trust
// labels and a context manifest arrives with the workflow engine (M3); this is
// intentionally not a second budget system, just the chat case of it.
const (
	maxPromptChars    = 24000
	maxMessageChars   = 4000
	maxHistoryChars   = 16000
	maxDescriptionLen = 8000
	maxHistoryRows    = 20
)

// PromptContext is the trusted, structured context for a chat job.
type PromptContext struct {
	AgentName       string
	AgentRole       Role
	ItemID          string
	ItemTitle       string
	ItemStatus      string
	ItemKind        string
	ItemPriority    string
	ItemDescription string
	ThreadTitle     string
}

// BuildChatPrompt composes the single string handed to an adapter. history is
// oldest-first and should include the message being replied to as its last
// human entry. The result is deterministic for tests.
func BuildChatPrompt(soul string, ctx PromptContext, history []Message) string {
	var b strings.Builder

	b.WriteString("# Persona\n\n")
	if strings.TrimSpace(soul) == "" {
		fmt.Fprintf(&b, "You are %s, a %s agent working inside a software company's control plane.\n", ctx.AgentName, ctx.AgentRole)
	} else {
		b.WriteString(strings.TrimSpace(soul))
		b.WriteString("\n")
	}

	b.WriteString("\n# Context\n\n")
	if ctx.ItemID != "" {
		fmt.Fprintf(&b, "Item: %s\n", ctx.ItemID)
	}
	if ctx.ItemTitle != "" {
		fmt.Fprintf(&b, "Title: %s\n", oneLine(ctx.ItemTitle))
	}
	if ctx.ItemStatus != "" {
		fmt.Fprintf(&b, "Status: %s\n", ctx.ItemStatus)
	}
	if ctx.ItemKind != "" {
		fmt.Fprintf(&b, "Kind: %s\n", ctx.ItemKind)
	}
	if ctx.ItemPriority != "" {
		fmt.Fprintf(&b, "Priority: %s\n", ctx.ItemPriority)
	}
	if ctx.ThreadTitle != "" {
		fmt.Fprintf(&b, "Thread: %s\n", oneLine(ctx.ThreadTitle))
	}
	if desc := strings.TrimSpace(ctx.ItemDescription); desc != "" {
		if len(desc) > maxDescriptionLen {
			desc = desc[:maxDescriptionLen] + "\n… (description truncated)"
		}
		b.WriteString("\n## Description\n\n")
		b.WriteString(desc)
		b.WriteString("\n")
	}

	b.WriteString("\n# Conversation\n\n")
	b.WriteString(renderHistory(ctx.AgentName, history))

	b.WriteString("\n# Instruction\n\n")
	fmt.Fprintf(&b, "Reply as %s to the latest human message, in Markdown, in the thread's voice.\n", ctx.AgentName)
	b.WriteString("You are answering inside the control plane, not executing a workflow: do not claim to have run commands, changed files, opened pull requests or started runs. ")
	b.WriteString("If the request needs code changes, say what you would do and what you need. Be concrete and concise.\n")

	out := b.String()
	if len(out) > maxPromptChars {
		// The soul is never trimmed; when the rest does not fit, keep the tail
		// (latest history and the instruction) and accept that a very large
		// soul consumed the budget.
		out = out[:maxPromptChars]
	}
	return out
}

func renderHistory(agentName string, history []Message) string {
	if len(history) == 0 {
		return "(no earlier messages)\n"
	}
	if len(history) > maxHistoryRows {
		history = history[len(history)-maxHistoryRows:]
	}
	// Reserve room for the latest human message first, then walk backwards.
	rendered := make([]string, 0, len(history))
	used := 0
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		body := strings.TrimSpace(m.BodyMD)
		if body == "" {
			continue
		}
		if len(body) > maxMessageChars {
			body = body[:maxMessageChars] + "\n… (message truncated)"
		}
		label := "System"
		switch m.AuthorKind {
		case AuthorHuman:
			label = "Human"
		case AuthorAgent:
			label = "Agent"
		}
		block := fmt.Sprintf("## %s\n\n%s\n", label, body)
		if used+len(block) > maxHistoryChars && len(rendered) > 0 {
			break
		}
		rendered = append(rendered, block)
		used += len(block)
	}
	var b strings.Builder
	if len(rendered) == 0 {
		return "(no earlier messages)\n"
	}
	for i := len(rendered) - 1; i >= 0; i-- {
		b.WriteString(rendered[i])
		if i > 0 {
			b.WriteString("\n")
		}
	}
	_ = agentName
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
