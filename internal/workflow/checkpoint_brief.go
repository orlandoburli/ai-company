package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// The brief is what a human reads before deciding a checkpoint. It is short on
// purpose: the full detail stays in the run, the card carries the decision.
const (
	// briefMaxBytes bounds the whole brief.
	briefMaxBytes = 8 << 10
	// briefQuestionLimit bounds the question.
	briefQuestionLimit = 400
	// briefStepSummaryLimit bounds the excerpt of one step.
	briefStepSummaryLimit = 200
	// briefAgentOutputLimit bounds the agent's own words.
	briefAgentOutputLimit = 1200
	// briefGitTimeout bounds one git read.
	briefGitTimeout = 15 * time.Second
)

// truncationMarker marks a cut text: the reader knows there is more.
const truncationMarker = "… (truncated)"

// questionPrefix is the line the brief carries the question on. A client that
// wants the question on its own reads it back with BriefQuestion.
const questionPrefix = "**The question:**"

// defaultBaseBranch is the branch a change is measured against when the
// product's repository does not name another one.
const defaultBaseBranch = "main"

// ArtifactSummary is the structured evidence a checkpoint card shows next to
// the brief: what kind of thing it is, where it points and its metadata.
type ArtifactSummary struct {
	Kind string          `json:"kind"`
	URI  string          `json:"uri"`
	Meta json.RawMessage `json:"meta"`
}

// ArtifactSummaries converts stored artifacts into the summaries a card shows.
func ArtifactSummaries(artifacts []Artifact) []ArtifactSummary {
	out := make([]ArtifactSummary, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, ArtifactSummary{Kind: artifact.Kind, URI: artifact.URI, Meta: artifact.Meta})
	}
	return out
}

// BriefCommit is a commit of the run's worktree.
type BriefCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// BriefStep is one step of the run as the brief shows it.
type BriefStep struct {
	Name    string
	Kind    string
	State   string
	Summary string
	Output  string
}

// BriefInput is everything a checkpoint brief is built from.
//
// Only the fields below ever reach the brief: the item, the question, the
// steps and the worktree facts. Nothing else — no environment, no runner
// configuration, no context manifest — is read into a brief, so a brief
// cannot carry a secret that was not put there on purpose.
type BriefInput struct {
	// ItemNumber and ItemTitle name the work the run is about.
	ItemNumber int64
	ItemTitle  string
	// Question is the checkpoint node's question.
	Question string
	// Steps are the run's steps that already produced a result, in graph
	// order.
	Steps []BriefStep
	// Artifacts are the artifacts the run already knows about.
	Artifacts []ArtifactSummary
	// Workdir is the worktree the run works in. An empty workdir means the
	// run has no worktree, and the brief then has no change section.
	Workdir string
	// Branch is the branch the worktree is on.
	Branch string
	// BaseBranch is the branch the change is measured against.
	BaseBranch string
	// Commit is the commit the change is attributed to. When it is empty the
	// brief reads the worktree's HEAD.
	Commit BriefCommit
}

// DefaultCheckpointQuestion is the question a checkpoint asks when its node
// does not name one. The seed templates do not set a question yet, so the
// default has to read well on its own.
func DefaultCheckpointQuestion(nodeName string) string {
	name := strings.TrimSpace(nodeName)
	if name == "" {
		return "Approve this step?"
	}
	// A node named after the action it asks for ("approve spec") should not
	// become "Approve approve spec?" — the verb is already there.
	verb, rest, found := strings.Cut(name, " ")
	if found {
		switch strings.ToLower(verb) {
		case "approve", "accept", "review", "check", "confirm", "decide", "sign", "verify":
			return strings.ToUpper(verb[:1]) + verb[1:] + " " + rest + "?"
		}
	}
	return "Approve " + name + "?"
}

// BriefQuestion reads the question back out of a brief.
//
// The brief carries the question on its second line, so a client that wants
// the question on its own — a headline, a notification — reads it here
// instead of the schema carrying a second copy.
func BriefQuestion(brief string) string {
	for _, line := range strings.Split(brief, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, questionPrefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// NodeQuestion reads the question of a checkpoint node out of a run's graph
// snapshot.
//
// The node's config names the question. A node without one asks the default
// question built from the node's name — never the node id, which is a machine
// name a human cannot decide on.
//
// The second result is false when the snapshot does not carry the node at all.
func NodeQuestion(snapshot json.RawMessage, nodeID string) (string, bool) {
	if len(snapshot) == 0 || nodeID == "" {
		return "", false
	}
	// The snapshot is read leniently: a run must keep its question even when
	// the graph it executes no longer validates.
	var graph Template
	if err := json.Unmarshal(snapshot, &graph); err != nil {
		return "", false
	}
	for _, node := range graph.Nodes {
		if node.ID != nodeID {
			continue
		}
		question := strings.TrimSpace(stringConfig(decodeConfig(node), "question"))
		if question == "" {
			question = DefaultCheckpointQuestion(node.Name)
		}
		return question, true
	}
	return "", false
}

// CheckpointQuestion returns the question a checkpoint asks.
//
// The node's config names the question; a node without one asks the default
// question built from the node's name. When the run's snapshot does not carry
// the node, the question is read back out of the brief, and the step's name
// is the last resort. The answer is never empty.
func CheckpointQuestion(snapshot json.RawMessage, nodeID, stepName, brief string) string {
	if question, ok := NodeQuestion(snapshot, nodeID); ok {
		return question
	}
	if question := BriefQuestion(brief); question != "" {
		return question
	}
	return DefaultCheckpointQuestion(stepName)
}

// BuildCheckpointBrief assembles the human-readable brief of a checkpoint.
//
// The brief is Markdown: the question, what happened so far, the agent's own
// words and the change. Everything is truncated so the brief stays readable on
// a card, and every truncation is marked.
//
// The error is reserved for a broken input. Missing evidence — no worktree, no
// git, a failing git — never fails the brief: the change section is simply
// left out, because a checkpoint must still be decidable.
func BuildCheckpointBrief(input BriefInput) (string, []ArtifactSummary, error) {
	question := strings.TrimSpace(input.Question)
	if question == "" {
		question = DefaultCheckpointQuestion("")
	}
	commit, diffStat, derived := readWorktreeChange(context.Background(), input.Workdir, input.Branch, input.BaseBranch, input.Commit)

	var b strings.Builder
	title := strings.TrimSpace(input.ItemTitle)
	if title == "" {
		title = "Untitled work"
	}
	if input.ItemNumber > 0 {
		fmt.Fprintf(&b, "## %s (%d)\n", title, input.ItemNumber)
	} else {
		fmt.Fprintf(&b, "## %s\n", title)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s %s\n\n", questionPrefix, truncateText(question, briefQuestionLimit))

	if steps := briefStepLines(input.Steps); steps != "" {
		b.WriteString("**What happened so far**\n")
		b.WriteString(steps)
		b.WriteString("\n")
	}
	if words := briefAgentWords(input.Steps); words != "" {
		b.WriteString("**The agent says**\n")
		b.WriteString(words)
		b.WriteString("\n")
	}
	if strings.TrimSpace(input.Workdir) != "" {
		b.WriteString("**The change**\n")
		if branch := strings.TrimSpace(input.Branch); branch != "" {
			fmt.Fprintf(&b, "- branch: `%s`\n", branch)
		}
		if commit.SHA != "" {
			if commit.Subject != "" {
				fmt.Fprintf(&b, "- commit: `%s %s`\n", commit.SHA, commit.Subject)
			} else {
				fmt.Fprintf(&b, "- commit: `%s`\n", commit.SHA)
			}
		}
		if diffStat != "" {
			fmt.Fprintf(&b, "- %s\n", diffStat)
		}
		b.WriteString("\n")
	}
	return boundBrief(b.String()), mergeArtifacts(input.Artifacts, derived), nil
}

// briefStepLines renders the steps that already ran, one line each.
//
// A checkpoint without a summary is a question, not work: it is left out, so
// the card does not report the question it asks as something that happened.
func briefStepLines(steps []BriefStep) string {
	var b strings.Builder
	for _, step := range steps {
		if step.Kind == string(NodeCheckpoint) && strings.TrimSpace(step.Summary) == "" {
			continue
		}
		name := strings.TrimSpace(step.Name)
		if name == "" {
			name = "(unnamed step)"
		}
		b.WriteString("- " + name)
		if state := strings.TrimSpace(step.State); state != "" {
			b.WriteString(" — " + state)
		}
		if excerpt := oneLine(step.Summary); excerpt != "" {
			b.WriteString(" — " + truncateText(excerpt, briefStepSummaryLimit))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// briefAgentWords renders the last thing the agent said.
//
// The agent's own words come first: the output of the last agent step that
// produced one, and its summary when there is no output.
func briefAgentWords(steps []BriefStep) string {
	for _, step := range reverseSteps(steps) {
		if step.Kind != string(NodeAgent) {
			continue
		}
		if output := strings.TrimSpace(step.Output); output != "" {
			return truncateText(output, briefAgentOutputLimit) + "\n"
		}
	}
	for _, step := range reverseSteps(steps) {
		if output := strings.TrimSpace(step.Output); output != "" {
			return truncateText(output, briefAgentOutputLimit) + "\n"
		}
	}
	for _, step := range reverseSteps(steps) {
		if summary := strings.TrimSpace(step.Summary); summary != "" {
			return truncateText(summary, briefAgentOutputLimit) + "\n"
		}
	}
	return ""
}

// reverseSteps returns the steps from the last to the first.
func reverseSteps(steps []BriefStep) []BriefStep {
	out := make([]BriefStep, 0, len(steps))
	for i := len(steps) - 1; i >= 0; i-- {
		out = append(out, steps[i])
	}
	return out
}

// mergeArtifacts merges the artifacts the run already had with the ones the
// worktree produced. The worktree's facts win: they are the current truth.
func mergeArtifacts(existing, derived []ArtifactSummary) []ArtifactSummary {
	out := make([]ArtifactSummary, 0, len(existing)+len(derived))
	index := map[string]int{}
	for _, artifact := range append(append([]ArtifactSummary{}, existing...), derived...) {
		if artifact.Kind == "" {
			continue
		}
		if i, ok := index[artifact.Kind]; ok {
			out[i] = artifact
			continue
		}
		index[artifact.Kind] = len(out)
		out = append(out, artifact)
	}
	return out
}

// boundBrief keeps the brief inside its size limit and says so.
func boundBrief(brief string) string {
	if len(brief) <= briefMaxBytes {
		return brief
	}
	cut := brief[:briefMaxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(cut, "\n") + "\n\n" + truncationMarker + "\n"
}

// truncateText cuts a text to a limit and marks the cut.
func truncateText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := text[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(cut, " \t\n") + " " + truncationMarker
}

// oneLine turns a text into a single line.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// readWorktreeChange reads the commit and the diff stat of a worktree.
//
// The commit is the worktree's HEAD unless the caller already knows it. The
// diff stat is the difference between the base branch and the worktree. A
// missing worktree, a missing git or a failing git is not an error here: the
// brief simply has no change section.
func readWorktreeChange(ctx context.Context, workdir, branch, baseBranch string, known BriefCommit) (BriefCommit, string, []ArtifactSummary) {
	derived := []ArtifactSummary{}
	if strings.TrimSpace(workdir) == "" {
		return known, "", derived
	}
	home, err := os.MkdirTemp("", "aic-brief-home-*")
	if err != nil {
		return known, "", derived
	}
	defer os.RemoveAll(home) //nolint:errcheck // the empty home dies with the directory

	commit := known
	if commit.SHA == "" {
		commit = briefHeadCommit(ctx, workdir, home)
	}
	base := resolveBaseBranch(ctx, workdir, home, baseBranch)
	diffStat := briefDiffStat(ctx, workdir, home, base)

	if branch != "" {
		derived = append(derived, ArtifactSummary{Kind: "branch", URI: branch, Meta: rawJSON(map[string]any{"branch": branch})})
	}
	if commit.SHA != "" {
		derived = append(derived, ArtifactSummary{Kind: "commit", URI: commit.SHA, Meta: rawJSON(map[string]any{"sha": commit.SHA, "subject": commit.Subject})})
	}
	if diffStat != "" {
		derived = append(derived, ArtifactSummary{Kind: "diff_summary", URI: diffStat, Meta: rawJSON(map[string]any{"stat": diffStat, "base_branch": base})})
	}
	return commit, diffStat, derived
}

// resolveBaseBranch finds the ref the change is measured against.
//
// The product's repository does not name its default branch yet, so the
// conventional names are tried in order: the caller's base branch first, then
// the alternatives a repository may use.
func resolveBaseBranch(ctx context.Context, workdir, home, base string) string {
	candidates := []string{}
	if base = strings.TrimSpace(base); base != "" {
		candidates = append(candidates, base, "origin/"+base)
	}
	candidates = append(candidates, defaultBaseBranch, "master", "origin/master")
	for _, candidate := range candidates {
		if _, err := briefGit(ctx, workdir, home, "rev-parse", "--verify", "--quiet", candidate+"^{commit}"); err == nil {
			return candidate
		}
	}
	return ""
}

// briefHeadCommit reads the HEAD commit of a worktree.
func briefHeadCommit(ctx context.Context, workdir, home string) BriefCommit {
	out, err := briefGit(ctx, workdir, home, "log", "-1", "--format=%H%x00%s")
	if err != nil {
		return BriefCommit{}
	}
	parts := strings.SplitN(strings.TrimSpace(out), "\x00", 2)
	if len(parts) != 2 || parts[0] == "" {
		return BriefCommit{}
	}
	return BriefCommit{SHA: parts[0], Subject: strings.TrimSpace(parts[1])}
}

// briefDiffStat reads the summary line of the diff against the base branch.
func briefDiffStat(ctx context.Context, workdir, home, base string) string {
	if base == "" {
		return ""
	}
	out, err := briefGit(ctx, workdir, home, "diff", "--stat", base)
	if err != nil {
		return ""
	}
	return lastLine(out)
}

// lastLine returns the last non-empty line of a text.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// briefGit runs git in a worktree with a scrubbed environment.
//
// The command is an argument list, never a shell string; the environment
// carries no credential and no operator configuration; the call is bounded by
// a timeout. This is the discipline the server-side git uses.
func briefGit(ctx context.Context, dir, home string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, briefGitTimeout)
	defer cancel()
	full := append([]string{
		"-c", "credential.helper=",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "gc.auto=0",
		"-c", "protocol.ext.allow=never",
		"-c", "core.sshCommand=/usr/bin/false",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = briefGitEnvironment(home)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// briefGitEnvironment is the environment a brief's git runs in: no system
// configuration, no global configuration, no credential helper, an empty home.
func briefGitEnvironment(home string) []string {
	env := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"HOME=" + home,
	}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TMPDIR", "USER"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
