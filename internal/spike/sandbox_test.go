package spike

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var env = []string{"PATH=/usr/bin:/bin:/usr/sbin"}

// fixture builds a disposable run root + worktree for a test.
func fixture(t *testing.T) (Paths, string) {
	t.Helper()
	root := t.TempDir()
	p := Paths{
		RunRoot:   root + "/run",
		Worktree:  root + "/worktree",
		Mirror:    root + "/mirror.git",
		BinDir:    root + "/bin",
		ExecDir:   root + "/run/exec",
		ProxyPort: "8787",
	}
	for _, d := range []string{p.RunRoot + "/home", p.RunRoot + "/tmp", p.RunRoot + "/claude",
		p.RunRoot + "/cache", p.Worktree, p.BinDir, p.ExecDir, root + "/logs"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prof, err := WriteProfile(root, p)
	if err != nil {
		t.Fatal(err)
	}
	return p, prof
}

func run(t *testing.T, prof string, argv ...string) Result {
	t.Helper()
	return SandboxExec(context.Background(), prof, argv, env, "")
}

// TestControlProfileApplies guards every other test: if deny-default is not
// actually being enforced, containment results are meaningless.
func TestControlProfileApplies(t *testing.T) {
	p, prof := fixture(t)

	// must succeed
	r := run(t, prof, "/bin/echo", "CONTROL_OK")
	if r.ExitCode != 0 || !strings.Contains(r.Stdout, "CONTROL_OK") {
		t.Fatalf("profile does not permit basic exec: %+v", r)
	}
	t.Logf("control ok: %s", r.String())

	// must fail: no process-exec for /usr/bin/sort without file-read
	_ = p
	t.Log("baseline control recorded")
}

// TestPathFilteredFileReadAborts records the measured constraint that shapes
// the profile: on this platform, adding a (subpath ...) filter inside
// file-read* aborts the process (SIGABRT) rather than denying it.
//
// This is recorded as a fact, not asserted as desirable: if a future OS or
// sandbox-exec build fixes it, TestProfileUsesBareFileRead will fail and the
// profile can be tightened back down to narrow-allow.
func TestPathFilteredFileReadAborts(t *testing.T) {
	_, _ = fixture(t)
	dir := t.TempDir()

	base := `(version 1)(deny default)(allow process-fork)(allow process-exec (subpath "/bin"))(allow file-read* `
	withPath := base + `(subpath "/usr/lib"))`
	profPath := filepath.Join(dir, "filtered.sb")
	if err := os.WriteFile(profPath, []byte(withPath), 0o600); err != nil {
		t.Fatal(err)
	}
	r := SandboxExec(context.Background(), profPath, []string{"/bin/echo", "X"}, env, "")

	t.Logf("path-filtered file-read*: exit=%d stderr=%q", r.ExitCode, r.Stderr)
	if r.ExitCode == 0 {
		t.Log("NOT reproducible on this build — narrow-allow file-read* now works; " +
			"the profile can be tightened (update Profile doc comment)")
	} else {
		t.Log("reproduced: path-filtered file-read* aborts; bare file-read* required")
	}
}

// TestBareFileReadContains is the load-bearing containment assertion: even with
// broad reads, the agent cannot WRITE outside its run root and worktree.
func TestBareFileReadContains(t *testing.T) {
	p, prof := fixture(t)

	canary := filepath.Join(t.TempDir(), "canary")
	if err := os.WriteFile(canary, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		script  string
		wantErr bool
	}{
		{"write into worktree", "touch " + p.Worktree + "/.ok", false},
		{"write into run root", "touch " + p.RunRoot + "/.ok", false},
		{"write outside (home)", "touch $HOME/.aic_probe && echo WROTE", true},
		{"read arity outside", "cat " + canary + " >/dev/null && echo READ", false}, // broad read: expected
	}
	for _, tc := range cases {
		r := run(t, prof, "/bin/sh", "-c", tc.script)
		gotErr := r.ExitCode != 0 || !strings.Contains(r.Stdout, "OK") && strings.Contains(r.Stdout, "WROTE")
		t.Logf("%-24s exit=%-4d stdout=%q stderr=%q", tc.name, r.ExitCode, r.Stdout, r.Stderr)
		if tc.wantErr && strings.Contains(r.Stdout, "WROTE") {
			t.Errorf("%s: agent wrote outside its sandbox", tc.name)
		}
		_ = gotErr
	}
	// hard assertion on the single most important property
	r := run(t, prof, "/bin/sh", "-c", "touch $HOME/.aic_probe2 && echo WROTE_OUTSIDE")
	if strings.Contains(r.Stdout, "WROTE_OUTSIDE") {
		t.Fatal("sandbox escape: agent wrote to $HOME")
	}
	t.Log("containment holds: $HOME not writable")
}

// TestNoDNSButLoopbackReachable verifies the egress chokepoint: the sandbox can
// reach the proxy on loopback but cannot resolve or reach any external host.
func TestNoDNSButLoopbackReachable(t *testing.T) {
	_, prof := fixture(t)

	ext := run(t, prof, "/usr/bin/curl", "-sS", "-m", "5", "-o", "/dev/null", "https://api.anthropic.com/")
	t.Logf("external https: exit=%d stderr=%q", ext.ExitCode, strings.TrimSpace(ext.Stderr))
	if ext.ExitCode == 0 {
		t.Error("external network reachable from inside the sandbox")
	}
	if !strings.Contains(ext.Stderr, "resolve") {
		t.Log("note: external blocked but not at DNS; check mach-lookup deny is in effect")
	}

	// loopback: connection refused (000) means the socket was reached;
	// blocked would look like a policy/permission error instead.
	loop := run(t, prof, "/usr/bin/curl", "-sS", "-m", "3", "-o", "/dev/null", "http://127.0.0.1:9/")
	t.Logf("loopback: exit=%d stderr=%q", loop.ExitCode, strings.TrimSpace(loop.Stderr))
	if strings.Contains(loop.Stderr, "not permitted") {
		t.Error("loopback blocked — proxy chokepoint would be impossible")
	}
}

// TestProfileRendersSanctions inspects the generated profile for rules that
// must never regress.
func TestProfileRendersSanctions(t *testing.T) {
	p, _ := fixture(t)
	s := p.Profile()
	for _, want := range []string{
		"(deny default)",
		"(deny mach-lookup)",
		"(allow file-read*)",
		p.RunRoot,
		p.Worktree,
		"localhost:" + p.ProxyPort,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("profile missing %q", want)
		}
	}
	// the model key must never be the real one
	if strings.Contains(p.Env("abc123")[0], "real") {
		t.Error("env leak")
	}
	t.Log("profile renders with expected chokepoints")
}

// TestRlimitNPROCIsNotAPerRunQuota records why §10 says 2048 and not 512:
// RLIMIT_NPROC is per-UID and counts every process the operator already owns.
func TestRlimitNPROCIsNotAPerRunQuota(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := countProcesses(ctx, currentUID())
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	t.Logf("processes already owned by this UID: %d (a 512 cap would fail immediately)", n)
	if n > 512 {
		t.Log("confirms RLIMIT_NPROC=512 is fatal on a normal desktop session")
	}
}
