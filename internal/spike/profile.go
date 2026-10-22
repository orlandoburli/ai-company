// Package spike contains throwaway verification harnesses used to validate
// assumptions about agent sandboxing on macOS before the engine is built.
//
// Nothing here is production code. It exists so each claim in mvp-design.md §10
// is backed by a recorded pass/fail rather than by observation.
package spike

import (
	"fmt"
	"strings"
)

// Paths holds the locations a single job is allowed to use. Each is passed to
// the sandbox profile as an explicit parameter.
type Paths struct {
	RunRoot   string // per-job scratch: home, config, caches, tmp
	Worktree  string // the agent's git worktree
	Mirror    string // app-owned bare mirror (.git + objects for this worktree)
	BinDir    string // staged, version-pinned CLI (directory, not file)
	ExecDir   string // compiled test binaries (go test writes here)
	ProxyPort string // loopback egress proxy port
}

// Profile renders the Seatbelt policy for a job.
//
// Shape rationale (see §10 "Profile"): deny-default with a *narrow* allow list
// is the shape we want, but a measured constraint on this platform is that a
// path-filtered `file-read*` aborts the process (SIGABRT, rc=134) — verified in
// spike_test.go TestPathFilteredFileReadAborts. A bare `file-read*` is
// therefore used for reads and containment is enforced by narrowing writes,
// mach services and network instead.
//
// That means: broad READ, narrow WRITE. The agent can read anything the
// operator's UID can read. It is accepted risk, recorded in §10, and the
// credential story depends on the other chokepoints (proxy-injected model key,
// --bare never reading OAuth/Keychain, server-held GitHub App tokens,
// deny mach-lookup).
func (p Paths) Profile() string {
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n\n")

	// --- process -----------------------------------------------------------
	b.WriteString(";; process\n")
	b.WriteString("(allow process-fork)\n")
	fmt.Fprintf(&b, "(allow process-exec\n  (subpath %q)\n  (subpath %q)\n", p.BinDir, p.ExecDir)
	b.WriteString("  (subpath \"/usr/bin\") (subpath \"/bin\")\n")
	b.WriteString("  (subpath \"/sbin\") (subpath \"/usr/lib\")\n")
	b.WriteString("  (subpath \"/opt/homebrew\") (subpath \"/usr/local\"))\n")
	b.WriteString("(allow signal (target same-sandbox))\n")
	b.WriteString("(allow process-info* (target self))\n")
	b.WriteString("(allow sysctl-read)\n\n")

	// --- reads -------------------------------------------------------------
	// Broad, per the note above. `/private/var` is required for TMPDIR
	// (which macOS resolves under /var/folders -> /private/var/folders).
	b.WriteString(";; reads: broad by necessity (see Profile doc comment)\n")
	b.WriteString(";; NB: `file-read-metadata*` is NOT a valid SBPL operation on this platform\n")
	b.WriteString(";; (it fails as an unbound variable, like `sandbox`) — metadata rides\n")
	b.WriteString(";; along with file-read*.\n")
	b.WriteString("(allow file-read*)\n\n")

	// --- writes ------------------------------------------------------------
	// This is the actual boundary: the agent cannot damage anything outside
	// its own disposable run + worktree.
	b.WriteString(";; writes: the real boundary\n")
	fmt.Fprintf(&b, "(allow file-write*\n  (subpath %q)\n  (subpath %q)\n", p.RunRoot, p.Worktree)
	b.WriteString("  (subpath \"/dev/null\") (subpath \"/dev/zero\")\n")
	b.WriteString("  (subpath \"/dev/random\") (subpath \"/dev/urandom\")\n")
	b.WriteString("  (subpath \"/private/var/folders\"))\n\n")

	// --- mach --------------------------------------------------------------
	// (deny mach-lookup) is what makes the login Keychain unreachable: the
	// keychain is a mach service, not a file.
	b.WriteString(";; mach: keychain lives behind a mach service\n")
	b.WriteString("(deny mach-lookup)\n")
	b.WriteString("(allow mach-lookup (global-name \"com.apple.system.logger\"))\n\n")

	// --- network -----------------------------------------------------------
	// Loopback only. This also kills DNS (mDNSResponder is a mach service and
	// is denied above), so hostname exfiltration cannot even be resolved.
	b.WriteString(";; network: loopback egress proxy only, no DNS\n")
	fmt.Fprintf(&b, "(allow network-outbound (remote ip \"localhost:%s\"))\n", p.ProxyPort)

	return b.String()
}

// Env returns the environment for a sandboxed job: a fixed allowlist with no
// platform credentials. The Anthropic key here is an opaque per-run token the
// proxy maps to the real key; the child never holds the real one.
func (p Paths) Env(runToken string) []string {
	return []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin",
		"HOME=" + p.RunRoot + "/home",
		"TMPDIR=" + p.RunRoot + "/tmp",
		"CLAUDE_CONFIG_DIR=" + p.RunRoot + "/claude",
		"XDG_CACHE_HOME=" + p.RunRoot + "/cache",
		"SHELL=/bin/bash",
		"TERM=dumb",
		"LANG=en_US.UTF-8",
		"TZ=UTC",
		"USER=aic",
		"LOGNAME=aic",
		"CI=1",
		"NO_COLOR=1",

		// model access: proxied, never direct
		"ANTHROPIC_BASE_URL=http://localhost:" + p.ProxyPort + "/anthropic",
		"ANTHROPIC_API_KEY=sk-ant-aic-" + runToken,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1",
		"DISABLE_AUTOUPDATER=1",

		// toolchain caches must not land in the real ~/Library/Caches
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"GOCACHE=" + p.RunRoot + "/cache/go",
		"GOMODCACHE=" + p.RunRoot + "/cache/gomod",
		"npm_config_cache=" + p.RunRoot + "/cache/npm",
	}
}
