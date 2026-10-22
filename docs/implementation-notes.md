# Implementation notes

What is actually built, and where it deliberately differs from or defers the
design in `mvp-design.md`. The design stays the target; this file records the
current edge of it so a later session does not have to rediscover it.

## Milestone state

| Milestone | Backend | Interface |
| --- | --- | --- |
| M0 — scaffold, tenancy, auth, tenant admin | done | in progress (`web/`) |
| M1 — work structure, queue overrides, base score, invitations | done | in progress (`web/`) |
| M2 — first agent | not started | — |
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

Rules enforced by the database rather than by the application:

- Every tenant-scoped table is `ENABLE` + `FORCE` row-level security.
- Parent links use composite foreign keys on `(id, tenant_id)`, so a cross-tenant
  reference cannot be inserted even with a valid id.
- A dependency edge that closes a cycle raises SQLSTATE `AIC01`; a self-edge
  `AIC02`. The API maps these to `409 dependency_cycle` / `409 self_dependency`.
- The activity log has no `UPDATE`/`DELETE` for the app role, and a trigger
  raises `AIC03` for anyone else.
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


## Known gaps

- `item_failures`, backoff and `exhausted` (M3/M4). No failure counter exists
  yet.
- `priority_rankings` / ranking runs, LLM ranking, stale-run fallback (M4).
- Start-now (M4) and agent assignees (`agents` arrives in M2).
- No email delivery; no password reset.
- Rate limiting charges the login bucket after Argon2 verification, so it stops
  credential stuffing but does not shield CPU from a flood of login attempts.
- Project nesting has no depth limit (items are capped at 6); cycles are
  rejected by `0006_tree_cycles.sql`.
