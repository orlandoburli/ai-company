// Package ctxbundle assembles deterministic, budgeted prompts for workflow
// node executions. Untrusted sections are sanitized, provenance-labelled and
// framed as data. Their frame is a line of the form
// "<<<UNTRUSTED:<safe-name>:<sha256-prefix>>>" followed by provenance and a
// text code block, then "<<<END UNTRUSTED:<safe-name>>>>". Sanitization escapes
// angle-bracket delimiter runs and neutralizes markdown fences, so section
// content cannot emit either framing line or close its code block.
package ctxbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// DefaultBudget is the total prompt budget when AssembleInput.Budget is zero.
	DefaultBudget = 6000
	// DefaultMaxSkills is the maximum number of selected skills to attach.
	DefaultMaxSkills = 3
)

// DefaultCaps contains the default memory token cap for each scope.
var DefaultCaps = map[string]int{
	"run":       2000,
	"item":      2000,
	"agent":     3000,
	"workspace": 2000,
}

// Trust identifies whether a task-file section is trusted instruction or
// untrusted data.
type Trust string

const (
	// TrustTrusted marks content supplied by an authorized operator or engine.
	TrustTrusted Trust = "trusted"
	// TrustUntrusted marks external content that must be treated as data.
	TrustUntrusted Trust = "untrusted"
)

// Provenance identifies the origin of a task-file section.
type Provenance struct {
	System string `json:"system,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Author string `json:"author,omitempty"`
	At     string `json:"at,omitempty"`
}

// Section is a trust-labelled part of a task file.
type Section struct {
	Name       string     `json:"name"`
	Trust      Trust      `json:"trust"`
	Content    string     `json:"content"`
	Ref        string     `json:"ref,omitempty"`
	Provenance Provenance `json:"provenance,omitempty"`
}

// Skill is a selected, trusted skill revision.
type Skill struct {
	Slug    string
	Version int
	Content string
	Source  string
}

// MemoryEntry is a source-annotated memory record selected for injection.
type MemoryEntry struct {
	Scope    string
	Ref      string
	Key      string
	Revision int
	Content  string
	Tokens   int
	Source   string
}

// AssembleInput contains the trusted selectors and content for one node.
type AssembleInput struct {
	Soul         string
	SoulRevision int
	// SoulFingerprint is the content hash of the soul, for the personas that
	// are a column rather than a versioned row.
	SoulFingerprint string
	Skills          []Skill
	Memory          []MemoryEntry
	Task            string
	Sections        []Section
	Budget          int
	Caps            map[string]int
	MaxSkills       int
}

// ManifestEntry records one attached context component.
type ManifestEntry struct {
	Kind     string `json:"kind"`
	Ref      string `json:"ref,omitempty"`
	Revision int    `json:"revision,omitempty"`
	// Fingerprint identifies a piece of content that has no version of its
	// own — the agent's soul is a column, so the manifest carries its content
	// hash instead of a version number.
	Fingerprint string `json:"fingerprint,omitempty"`
	Trust       Trust  `json:"trust,omitempty"`
	Key         string `json:"key,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Tokens      int    `json:"tokens"`
}

// TrimNote explains why a whole component was omitted from the bundle.
type TrimNote struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Key    string `json:"key,omitempty"`
	Reason string `json:"reason"`
}

// Bundle is the exact prompt and audit data produced by Assemble.
type Bundle struct {
	Prompt         string
	Manifest       []ManifestEntry
	MemoryManifest []ManifestEntry
	TaskFileSHA    string
	Trimmed        []TrimNote
	DroppedMarkers []string
}

type skillPart struct {
	skill Skill
	token int
	keep  bool
}

type memoryPart struct {
	entry MemoryEntry
	token int
	keep  bool
}

type sectionPart struct {
	section  Section
	token    int
	keep     bool
	implicit bool
}

// Assemble builds one deterministic prompt. Content is retained or dropped
// whole; the soul is always included even when it exceeds the total budget.
func Assemble(in AssembleInput) (Bundle, error) {
	var out Bundle
	for _, section := range in.Sections {
		if section.Trust != TrustTrusted && section.Trust != TrustUntrusted {
			return out, fmt.Errorf("section %q has invalid trust %q", section.Name, section.Trust)
		}
	}

	budget := in.Budget
	if budget == 0 {
		budget = DefaultBudget
	}
	if budget < 0 {
		return out, fmt.Errorf("budget must not be negative")
	}
	maxSkills := in.MaxSkills
	if maxSkills == 0 {
		maxSkills = DefaultMaxSkills
	}
	if maxSkills < 0 {
		return out, fmt.Errorf("max skills must not be negative")
	}
	caps := make(map[string]int, len(DefaultCaps))
	for scope, cap := range DefaultCaps {
		caps[scope] = cap
	}
	for scope, cap := range in.Caps {
		if _, ok := caps[scope]; !ok {
			return out, fmt.Errorf("unknown memory scope %q", scope)
		}
		if cap < 0 {
			return out, fmt.Errorf("cap for %s must not be negative", scope)
		}
		caps[scope] = cap
	}

	soulTokens := estimateTokens(in.Soul)
	skills := make([]skillPart, 0, minInt(len(in.Skills), maxSkills))
	for i, skill := range in.Skills {
		if i >= maxSkills {
			break
		}
		skills = append(skills, skillPart{skill: skill, token: estimateTokens(skill.Content), keep: true})
	}
	memory := make([]memoryPart, len(in.Memory))
	for i, entry := range in.Memory {
		if _, ok := caps[entry.Scope]; !ok {
			return out, fmt.Errorf("memory entry %q has unknown scope %q", entry.Key, entry.Scope)
		}
		memory[i] = memoryPart{entry: entry, token: estimateTokens(entry.Content), keep: true}
	}

	sections := make([]sectionPart, 0, len(in.Sections)+1)
	// External sections are sanitized at the assembly boundary, never at the
	// selector boundary. Preserve trusted section bytes verbatim.
	for _, section := range in.Sections {
		if section.Trust == TrustUntrusted {
			section.Content, _ = SanitizeUntrusted(section.Content)
			out.DroppedMarkers = appendMarkers(out.DroppedMarkers, section.Name, section.Content)
		}
		sections = append(sections, sectionPart{section: section, token: estimateTokens(section.Content), keep: true})
	}
	sections = append(sections, sectionPart{
		section: Section{Name: "task", Trust: TrustTrusted, Content: in.Task},
		token:   estimateTokens(in.Task), keep: true, implicit: true,
	})

	// Apply per-scope caps in the injection order, retaining earlier input
	// entries and dropping a whole entry when the next one does not fit.
	usedByScope := map[string]int{}
	for i := range memory {
		m := &memory[i]
		if usedByScope[m.entry.Scope]+m.token > caps[m.entry.Scope] {
			m.keep = false
			out.Trimmed = append(out.Trimmed, memoryTrim(m.entry))
			continue
		}
		usedByScope[m.entry.Scope] += m.token
	}

	if totalTokens(soulTokens, skills, memory, sections) > budget {
		for i := len(skills) - 1; i >= 0 && totalTokens(soulTokens, skills, memory, sections) > budget; i-- {
			if skills[i].keep {
				skills[i].keep = false
				out.Trimmed = append(out.Trimmed, TrimNote{Kind: "skill", Ref: skillRef(skills[i].skill), Reason: "budget"})
			}
		}
		for _, scope := range []string{"workspace", "agent", "item", "run"} {
			for i := len(memory) - 1; i >= 0 && totalTokens(soulTokens, skills, memory, sections) > budget; i-- {
				if memory[i].keep && memory[i].entry.Scope == scope {
					memory[i].keep = false
					out.Trimmed = append(out.Trimmed, memoryTrim(memory[i].entry))
				}
			}
		}
		for i := len(sections) - 1; i >= 0 && totalTokens(soulTokens, skills, memory, sections) > budget; i-- {
			if sections[i].keep && sections[i].section.Trust == TrustUntrusted {
				sections[i].keep = false
				out.Trimmed = append(out.Trimmed, sectionTrim(sections[i].section))
			}
		}
		// Other trusted envelope sections precede the task statement; the
		// statement itself is deliberately the final trimmable component.
		for i := len(sections) - 1; i >= 0 && totalTokens(soulTokens, skills, memory, sections) > budget; i-- {
			if sections[i].keep && !sections[i].implicit {
				sections[i].keep = false
				out.Trimmed = append(out.Trimmed, sectionTrim(sections[i].section))
			}
		}
		for i := range sections {
			if sections[i].keep && sections[i].implicit && totalTokens(soulTokens, skills, memory, sections) > budget {
				sections[i].keep = false
				out.Trimmed = append(out.Trimmed, sectionTrim(sections[i].section))
				break
			}
		}
	}

	manifest := make([]ManifestEntry, 0, 1+len(skills)+len(memory)+len(sections))
	manifest = append(manifest, ManifestEntry{Kind: "soul", Ref: "soul", Revision: in.SoulRevision, Fingerprint: in.SoulFingerprint, Trust: TrustTrusted, Tokens: soulTokens})
	var prompt strings.Builder
	prompt.WriteString("# Soul\n\n")
	prompt.WriteString(in.Soul)
	if in.Soul != "" && !strings.HasSuffix(in.Soul, "\n") {
		prompt.WriteByte('\n')
	}
	for _, part := range skills {
		if !part.keep {
			continue
		}
		ref := skillRef(part.skill)
		manifest = append(manifest, ManifestEntry{Kind: "skill", Ref: ref, Revision: part.skill.Version, Trust: TrustTrusted, Tokens: part.token})
		fmt.Fprintf(&prompt, "\n# Skill %s\n\n%s", part.skill.Slug, part.skill.Content)
		if part.skill.Source != "" {
			fmt.Fprintf(&prompt, "\nSource: %s", part.skill.Source)
		}
		if part.skill.Content != "" && !strings.HasSuffix(part.skill.Content, "\n") {
			prompt.WriteByte('\n')
		}
	}
	for _, scope := range []string{"run", "item", "agent", "workspace"} {
		for _, part := range memory {
			if !part.keep || part.entry.Scope != scope {
				continue
			}
			entry := part.entry
			entryManifest := ManifestEntry{Kind: "memory", Ref: entry.Ref, Revision: entry.Revision, Trust: TrustTrusted, Key: entry.Key, Scope: entry.Scope, Tokens: part.token}
			manifest = append(manifest, entryManifest)
			out.MemoryManifest = append(out.MemoryManifest, entryManifest)
			fmt.Fprintf(&prompt, "\n# Memory (%s)\n\nSource: %s\nKey: %s\n\n%s", scope, entry.Source, entry.Key, entry.Content)
			if entry.Content != "" && !strings.HasSuffix(entry.Content, "\n") {
				prompt.WriteByte('\n')
			}
		}
	}

	attachedSections := make([]Section, 0, len(sections))
	for _, part := range sections {
		if part.keep && part.section.Trust == TrustTrusted && !part.implicit {
			attachedSections = append(attachedSections, part.section)
		}
	}
	for _, part := range sections {
		if part.keep && part.implicit {
			attachedSections = append(attachedSections, part.section)
		}
	}
	for _, part := range sections {
		if part.keep && part.section.Trust == TrustUntrusted {
			attachedSections = append(attachedSections, part.section)
		}
	}

	prompt.WriteString("\n# Task file\n\n")
	prompt.WriteString(taskFilePreamble)
	prompt.WriteString("\n\n")
	for _, section := range attachedSections {
		tokens := estimateTokens(section.Content)
		manifest = append(manifest, ManifestEntry{Kind: "task_file", Ref: section.Ref, Trust: section.Trust, Tokens: tokens})
		if section.Trust == TrustUntrusted {
			writeUntrusted(&prompt, section)
		} else {
			fmt.Fprintf(&prompt, "## %s (trusted)\n\n%s", section.Name, section.Content)
			if section.Content != "" && !strings.HasSuffix(section.Content, "\n") {
				prompt.WriteByte('\n')
			}
		}
	}

	out.Manifest = manifest
	taskBytes, err := TaskFileJSON(attachedSections)
	if err != nil {
		return Bundle{}, fmt.Errorf("encode task file: %w", err)
	}
	sum := sha256.Sum256(taskBytes)
	out.TaskFileSHA = hex.EncodeToString(sum[:])
	out.Prompt = prompt.String()
	return out, nil
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	n := (len([]rune(s)) + 3) / 4
	if n < 1 {
		return 1
	}
	return n
}

func totalTokens(soul int, skills []skillPart, memory []memoryPart, sections []sectionPart) int {
	total := soul
	for _, p := range skills {
		if p.keep {
			total += p.token
		}
	}
	for _, p := range memory {
		if p.keep {
			total += p.token
		}
	}
	for _, p := range sections {
		if p.keep {
			total += p.token
		}
	}
	return total
}

func skillRef(skill Skill) string { return fmt.Sprintf("skill:%s@%d", skill.Slug, skill.Version) }

func memoryTrim(entry MemoryEntry) TrimNote {
	return TrimNote{Kind: "memory", Ref: entry.Ref, Scope: entry.Scope, Key: entry.Key, Reason: "budget"}
}

func sectionTrim(section Section) TrimNote {
	return TrimNote{Kind: "section", Ref: section.Ref, Reason: "budget"}
}

func appendMarkers(markers []string, name, content string) []string {
	for _, marker := range []string{"AIC_PUBLISH", "AIC_MEMORIZE", "AIC_OUTPUT", "AIC_SPAWN"} {
		if strings.Contains(content, marker) {
			value := safeName(name) + ":" + marker
			found := false
			for _, previous := range markers {
				if previous == value {
					found = true
					break
				}
			}
			if !found {
				markers = append(markers, value)
			}
		}
	}
	return markers
}

func safeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.-", r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "section"
	}
	return b.String()
}

func writeUntrusted(b *strings.Builder, section Section) {
	name := safeName(section.Name)
	sum := sha256.Sum256([]byte(section.Content))
	fmt.Fprintf(b, "<<<UNTRUSTED:%s:%s>>>\n", name, hex.EncodeToString(sum[:])[:8])
	b.WriteString("Provenance:")
	if section.Provenance.System != "" {
		fmt.Fprintf(b, " system=%q", oneLine(section.Provenance.System))
	}
	if section.Provenance.Ref != "" {
		fmt.Fprintf(b, " ref=%q", oneLine(section.Provenance.Ref))
	}
	if section.Provenance.Author != "" {
		fmt.Fprintf(b, " author=%q", oneLine(section.Provenance.Author))
	}
	if section.Provenance.At != "" {
		fmt.Fprintf(b, " at=%q", oneLine(section.Provenance.At))
	}
	b.WriteString("\n```text\n")
	b.WriteString(section.Content)
	if !strings.HasSuffix(section.Content, "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(b, "```\n<<<END UNTRUSTED:%s>>>>\n", name)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
