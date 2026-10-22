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

## Known gaps

- `item_failures`, backoff and `exhausted` (M3/M4). No failure counter exists
  yet.
- `priority_rankings` / ranking runs, LLM ranking, stale-run fallback (M4).
- Start-now (M4) and agent assignees (`agents` arrives in M2).
- No rate limiting on login, invite creation or invite acceptance.
- No email delivery; no password reset.
- Item tree depth is bounded in code (32) but not enforced as a database
  constraint; the design asks for depth ≤ 6 at the API level.
