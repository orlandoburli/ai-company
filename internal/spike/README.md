# M2 spike — throwaway verification harness

**Nothing in this directory is production code.** It exists to turn the claims in
`docs/mvp-design.md` §10 into recorded pass/fail results before the engine is
built, on the principle that a security design nobody has executed is a
hypothesis.

## Why it is separate

This directory is disposable. It may be deleted at any time without affecting the
server, and **nothing here should be promoted into `internal/runtime` or
`internal/agent`**. If a piece of it turns out to be needed for real (the loopback
egress proxy is the obvious candidate), it gets rewritten properly in its own
package with its own tests — not moved in here. Keeping the boundary explicit is
the point; a half-promoted spike is worse than no spike.

## What it has already established

Run with:

```sh
cd internal/spike && go test -v -count=1 ./...
```

| Claim | Result |
| --- | --- |
| Deny-default profile applies; basic exec works | pass |
| Agent cannot write to `$HOME` | `Operation not permitted` |
| External network unreachable | `Could not resolve host` (DNS dead too) |
| Loopback reachable for the egress proxy | reaches socket layer |
| Nested `sandbox-exec` cannot widen its own sandbox | `sandbox_apply: Operation not permitted` |
| `RLIMIT_NPROC=512` is fatal on a normal desktop | 545 processes already owned by this UID |

## Platform facts it discovered (do not re-derive)

These cost real time to find and are easy to reintroduce:

- `sandbox` and `file-read-metadata*` are **not valid SBPL operations** here.
  They fail as unbound variables rather than being ignored, so a profile
  containing them is silently invalid.
- A **path-filtered** `file-read*` aborts the process (SIGABRT); only a bare
  `(allow file-read*)` works. Hence the accepted broad-read/narrow-write risk
  documented in §10.
- `(remote ip ...)` takes `localhost`, not `127.0.0.1`.
- `/dev/null` must be allowed literally or half the toolchain fails.
- `RLIMIT_NPROC` is per-UID and counts every process the operator owns.
- `sandbox-exec` is deprecated-but-present; re-run this suite on any macOS or
  Claude Code upgrade, since the profile is the boundary.

## Still to prove

- A real coding task completes under `--restricted --permission-mode dontAsk`
  (this decides the minimum viable flag set).
- `--bare` authenticates through the loopback proxy with a placeholder key.
- Credential non-exposure end to end (Keychain, env, argv, inherited fds).
- Process lifecycle: SIGTERM to the group, rlimits, orphan sweep after SIGKILL.