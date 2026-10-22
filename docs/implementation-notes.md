# Implementation notes

What is actually built, and where it deliberately differs from or defers the
design in `mvp-design.md`. The design stays the target; this file records the
current edge of it so a later session does not have to rediscover it.

## Milestone state

| Milestone | Backend | Interface |
| --- | --- | --- |
| M0 — scaffold, tenancy, auth, tenant admin | done | done |
| M1 — work structure, queue overrides, base score, invitations | done | done |
| M2 — first agent | done | done |
| M3 — graph engine and checkpoints | not started | — |
| M4 — GitHub, memory, LLM ranking, backoff | not started | — |

## Dev harness

`make dev` runs migrations (owner role), seeds, and starts the API and Next.js
together. `cmd/seed` is a development tool, not a fixture loader: it uses the
ordinary stores and the runtime connection, so what it writes has the same RLS
behaviour, per-tenant numbering and activity entries as the API. Re-running is a
no-op once the tenant slug exists; a second tenant needs a different `-slug` and
`-email`.

The dataset is three products (FazGiro, Vendamatica, ProntaLoja), four projects
and 23 items across every status and kind, with four blocked-by edges, one
paused item and one pinned item. That is what exercises the board grouping, the
tree rollup and the queue's eligibility-before-order rule without hand-clicking
fixtures.

## Schema

Migrations are plain SQL, embedded and applied by `cmd/migrate`:

- `0001_tenancy.sql` — tenants, users, memberships, sessions.
- `0002_identity_force_rls.sql` — FORCE RLS everywhere; the identity plane is
  reachable only through `SECURITY DEFINER` functions owned by `aic_auth`.
- `0003_tenant_admin.sql` — tenant settings, member roles, role invariants.
- `0004_content.sql` — products, projects, items, dependencies, activity log,
  per-tenant counters.
- `0005_invites.sql` — single-use invitations.
- `0006_tree_cycles.sql` — parent-cycle triggers for items and projects.
- `0007_agents.sql` — agents, threads, messages, `agent_jobs`, append-only
  `job_logs`, plus the narrowly scoped `worker_claim_job` / `worker_reap_jobs`
  functions.

Rules enforced by the database rather than by the application:

- Every tenant-scoped table is `ENABLE` + `FORCE` row-level security.
- Parent links use composite foreign keys on `(id, tenant_id)`, so a cross-tenant
  reference cannot be inserted even with a valid id.
- A dependency edge that closes a cycle raises SQLSTATE `AIC01`; a self-edge
  `AIC02`. The API maps these to `409 dependency_cycle` / `409 self_dependency`.
- The activity log has no `UPDATE`/`DELETE` for the app role, and a trigger
  raises `AIC03` for anyone else.
- `job_logs` is append-only in the same way (`AIC05`), and the app role has only
  `SELECT`/`INSERT` on it — a job's output cannot be rewritten after the fact.
- Agent tables use the same `(id, tenant_id)` composite foreign keys, so a job
  cannot point at another tenant's agent, thread, message or item. A partial
  unique index allows at most one item thread per item.
- Deletes refuse non-empty products and projects instead of cascading a
  mis-click. Items delete normally; their edges cascade.

## Queue semantics as implemented

- `paused_at/paused_by/pause_reason` and `pinned_at/pinned_by` are the only
  queue state. Both are written by explicit, attributed actions and survive
  restarts because they are columns, not scheduler memory.
- Eligibility is evaluated before order: `closed → paused → blocked → epic`.
  A pinned paused item is still paused; a pinned blocked item is still blocked.
- Order inside the eligible set: most recent pin first, then base score
  descending, then item number, then id.
- Pausing is not a failure and does not touch any retry state. There is no
  `item_failures` table yet (M3), so the "pause leaves the failure counter
  untouched" criterion is currently satisfied by construction: pause writes only
  pause columns.
- Closing an item (`done`/`cancelled`) clears its pin, including in bulk status
  changes.
- Unpausing restores eligibility, not position: the item is re-scored from
  current inputs.

## Base score

`internal/priority` is pure and versioned (`pri-base-1`):

| Factor | Weight |
| --- | --- |
| `priority:p0` / `p1` / `p2` | 100 / 50 / 10 |
| `bug` label | 30 |
| unblocking power | 25 per open transitive dependent |
| open PR | 20 (omitted when there is no PR signal) |
| age | 1/day, capped at 30 |

`GET /api/items/{id}/score` returns the score and the factor breakdown, whose
contributions sum to the score. Sub-items inherit the nearest ancestor's label
for scoring without the label ever being written to the item (FR-PRI-2).

Scores are computed on request. There is no `priority_rankings` table until M4,
so nothing persists a ranking run yet and no LLM pass exists.

## Invitations

- Creation returns the single-use token exactly once; only its SHA-256 digest is
  stored. There is no mailer in the MVP, so the inviter passes the link on.
- Invitations expire after seven days, are revocable, and are unique per
  `(tenant, lower(email))` while pending.
- An invitation can grant `admin`, `member` or `guest` — never `owner`.
- Acceptance is keyed on the token, creates the user if the email is new, and
  never changes an existing account's password.
- Unknown, expired, revoked and already-used tokens are indistinguishable
  (`400 invalid_invite`).

## Rate limiting

The abuse-prone endpoints are rate limited in memory (`internal/api/ratelimit.go`):

| Bucket | Endpoint | Key | Default |
| --- | --- | --- | --- |
| `login` | `POST /api/auth/login` | client IP + lowercased email | 10 / 5 min |
| `accept-invite` | `POST /api/auth/accept-invite` | client IP | 20 / hour |
| `create-invite` | `POST /api/invites` | acting user id | 30 / hour |

Behaviour:

- A fixed-window counter per key; exhausting one answers
  `429 rate_limited` ("Too many attempts. Try again later.") with a
  `Retry-After` header in seconds.
- The login counter is charged only on a failed verification and cleared on
  success, so a burst of wrong passwords exhausts the bucket but a legitimate
  attempt that follows a typo storm is never locked out.
- Keys are `r.RemoteAddr`, not `X-Forwarded-For`: without a trusted proxy that
  header is client-supplied and would hand an attacker unlimited keys. Wire
  trusted-proxy parsing in at the edge if that changes.
- Limits are **per-process** and **reset on restart**: counters live in the
  server's memory. That is deliberate for the MVP, which runs exactly one
  process (`docs/mvp-design.md` §10); horizontal scaling needs a shared counter
  store instead.
- Memory is bounded: unauthenticated keys are attacker-controlled, so the
  counter map prunes expired entries lazily and holds at most 8192 keys, ever,
  with no goroutine behind it.

## Item tree depth

The design requires depth ≤ 6 for the item tree. It is enforced at the API:
`POST /api/items` with a `parent_id` and `PATCH /api/items/{id}` with a
non-empty `parent_id` answer `422 tree_too_deep` when the placement would put
any item past level six, with the root counted as one. On a move the rule
applies to the deepest descendant of the moving subtree, not just the moved
item, so a deep branch cannot be smuggled under a low parent. The recursive
walks in `internal/work` keep their depth-32 loop guards as a backstop, and
there is still no database constraint — the depth lives in queries, not DDL.

Parent cycles are closed by the database instead (`0006_tree_cycles.sql`): a
trigger on `items` and `projects` walks the new parent's ancestor chain and
raises SQLSTATE `AIC04` when it reaches the row's own id, so a move under one's
own descendant is rejected for every caller, not just the API's depth check.
`internal/work` maps `AIC04` to `ErrParentCycle`, which the API surfaces as
`422 parent_cycle` ("That move would make the tree circular.") for both items
and projects. The trigger is not `SECURITY DEFINER` and walks only the row's
tenant, so the check runs through row-level security like every other access.


## Agent runtime (M2)

The registry (`agents`) stores one row per mentionable worker: unique `name`,
`role` (`pm|dev|reviewer|qa|researcher`), `kind` (`cli|api`), an opaque `config`
validated per kind, `soul_md`, `capabilities`/`skills_policy`/`fallbacks`, a
`concurrency_limit` (default 2) and `enabled`. Owner/admin manage it; every role
can read it. `cmd/seed` ensures `dev` (Claude CLI) and `pm` (OpenAI API) without
overwriting an edited row, and also runs on an already-seeded tenant.

Chat is deliberately a thin M2 slice of the future context pipeline:

- Each item has at most one thread (`context_kind = 'item'`); M2 exposes no
  product or free threads yet.
- Messages are ordered by a per-thread `seq`, with `human`, `agent` and `system`
  authors. `@name` mentions are resolved at post time: every enabled mentioned
  agent gets exactly one queued job, and an unknown or disabled mention is a
  `422` that writes nothing.
- The manual run action queues a job directly and records its optional
  instructions as a human message.
- The prompt is the agent's soul (never trimmed), trusted item context (title,
  status, kind, priority, description), the last 20 messages bounded by
  characters, and the instruction last. Skills, memory and task files are M3.

The worker lives in the API process and claims from `agent_jobs` in Postgres:

- `worker_claim_job` picks the oldest queued job whose agent is enabled and
  whose per-agent running count is below `concurrency_limit`, under a row lock;
  `worker_reap_jobs` returns jobs whose lease expired to the queue. Both are
  `SECURITY DEFINER` functions owned by `aic_auth` because a claim necessarily
  crosses tenants; there is still no privileged runtime connection.
- A claimed job gets a two-minute lease that the worker heartbeats. Two workers
  run globally, the queue is capped at 100 jobs per tenant, and the poll
  interval is 500 ms.
- Cancellation is terminal: the API marks the job cancelled, the worker cancels
  the adapter context, and the terminal check after a run prevents a late
  success from resurrecting it. Worker shutdown requeues without counting an
  attempt.
- Transient failures requeue with `10s · 2^(attempt-1)` (capped at 5m) up to
  three attempts; permanent failures finish the job and post a system message to
  the thread. Adapter errors are classified, never string-matched.

Adapters:

- CLI: the prompt is the final argv element, the child gets its own process
  group (cancel kills the group), stdout is parsed as `stream-json` or shown as
  a text tail, and the environment is an allowlist that drops `*KEY*`,
  `*TOKEN*`, `*SECRET*` and `*PASSWORD*`. Without a job workdir it runs in a
  throwaway temp directory and removes it afterwards.
- API: OpenAI-compatible chat completions and Anthropic messages, one call per
  job, key read from the environment variable named by `config.key_env`. It
  does not stream tokens or call tools yet; the UI still gets progress from the
  job state and log rows.

The SSE hub is in-process: `GET /api/jobs/{id}/events?after_seq=` replays log
rows and then streams `log`, `state` and a final `done`; the browser falls back
to polling `/logs` if the stream drops. Because the hub is memory, replay after
a restart still works from `job_logs`, but live frames are per-process — the
same single-process constraint as the rate limiter.

## Agent security notes

- The runtime role is a member of `aic_app`, not the schema owner and not
  `BYPASSRLS`. `worker_claim_job` / `worker_reap_jobs` are the only
  cross-tenant door, they take no caller-supplied tenant id, and `EXECUTE` is
  revoked from `PUBLIC` before being granted to `aic_app`.
- API keys are never stored: `config.key_env` names an environment variable,
  and the adapter reports only the variable name when it is missing.
- `job_logs` is append-only (`AIC05`). It is **not partitioned in M2**: the
  blanket `ALTER DEFAULT PRIVILEGES` in `0001` would give `aic_app` direct
  access to child partitions, bypassing the parent's RLS. Partitioning waits
  until per-partition RLS/revokes exist.
- CLI isolation is the process boundary plus the narrowed environment and
  process group. The macOS sandbox profile and worktree provisioning arrive
  with the workflow engine; chat runs in a temp directory and cannot touch the
  operator's checkout through a `..` cwd.

## M2 interface

`web/` adds the item **Chat** tab, a full-page `/chat/[threadId]`, and
`/settings/agents`. The chat timeline merges human/agent/system messages with
job chips and live logs; the composer has an `@` mention menu and a manual run
dialog. The registry editor manages role/kind/config/soul/concurrency without
ever accepting a secret. Streaming uses one `EventSource` per non-terminal job
with polling fallback, and guests get a read-only chat.

## Known gaps

- `item_failures`, backoff and `exhausted` at item level (M3/M4). Agent jobs have
  their own attempt counter, but items have none yet.
- `priority_rankings` / ranking runs, LLM ranking, stale-run fallback (M4).
- Start-now (M4) and agent assignees (M3).
- Graph workflows, checkpoints and the merge node (M3) — chat jobs are the only
  runs that exist.
- Skills, memory (`memory_entries`) and task files (M3/M4) are designed but not
  built; the agent prompt in M2 is soul + item context + thread history.
- Worktrees, the macOS sandbox profile and the per-branch workspace isolation
  arrive with the workflow engine. M2 CLI runs use a temp directory.
- The API adapter does not stream tokens and cannot call tools yet; the CLI
  adapter has no tool-permission gate.
- `job_logs` is append-only but not partitioned (see above).
- Cost is recorded per job (`cost_usd`) but nothing budgets or caps it.
- No email delivery; no password reset.
- Rate limiting charges the login bucket after Argon2 verification, so it stops
  credential stuffing but does not shield CPU from a flood of login attempts.
- Project nesting has no depth limit (items are capped at 6); cycles are
  rejected by `0006_tree_cycles.sql`.
- The SSE hub and the rate limiter are per-process, so running more than one
  API process would split live job frames and counters.
