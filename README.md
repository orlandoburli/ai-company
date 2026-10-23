# AI Company — Super-Orchestrator of AI Agents

A control plane for running a one-person software company with AI agents.
It manages products, projects and work items in a multi-level hierarchy,
executes workflows composed of AI-agent steps and human checkpoints,
and keeps every action visible through a Jira-like activity layer.

**First use case:** a solo software-house owner automating implementation and
maintenance across three products:

| Product | Domain |
| --- | --- |
| **FazGiro** | Brazilian ERP for small business |
| **Vendamatica** | AI-driven automatic sales |
| **ProntaLoja** | E-commerce solution |

## Core capabilities

- **Multi-tenant by design** — every entity is tenant-isolated; teams live *inside* a tenant with owner/admin/member/guest roles
- **Project management** — products → nested projects → nested work items (arbitrary depth everywhere; item kinds are user-named, no fixed taxonomy)
- **Workflows** — versioned graph templates (agent, integration, checkpoint, merge, loop nodes) executed concurrently as durable runs
- **Memory** — tiered across a run, an agent and a workspace, so knowledge carries between steps and between runs
- **Chats** — contextual threads between you and agents, attached to items, runs or checkpoints
- **Activities** — Jira-like board, statuses, assignment (human *or* agent), full audit log
- **Checkpoints** — human-in-the-loop gates that pause a workflow until you approve, edit or reject, with structured fields that steer the branch
- **Live run monitor** — watch agents work in real time (logs, tool calls, current action), inject instructions mid-run, or redo any node

## External integrations

- **GitHub** — repos, branches, PRs, issues (API first, `gh` CLI as fallback)
- **Jira** — bidirectional task sync
- **AI connectors** — CLI agents (Claude Code, Codex, Aider…) and API agents (Anthropic, OpenAI…)

## Inspiration

- **[n8n](https://github.com/n8n-io/n8n)** — the graph/connection model: nodes with typed
  parameters, named output ports, per-node error policy, and immutable per-execution
  snapshots of the definition.
- **[Apiary](https://github.com/orlandoburli/apiary)** — the owner's open-source task-driven
  agent orchestration harness. AI Company borrows its battle-tested execution semantics
  (durable approval gates, workflow memory, output markers, fallback chains, resilience
  safeguards).
- **[hermes-agent](https://github.com/nousresearch/hermes-agent)** — lease-based job claiming,
  the failure taxonomy that distinguishes *blocked* from *failed*, and workspace isolation as
  a node property.
- **[Claude Code](https://claude.com/claude-code)** — the agent adapter and layered persona
  (soul) files that shape how each role approaches a step.

See `docs/mvp-design.md` §13 for the full concept mapping.

## Stack (MVP)

- **Backend:** Go (REST API + orchestrator + agent runtime)
- **Frontend:** Next.js (App Router, TypeScript)
- **Storage:** Postgres only — tenant-isolated from day one (`tenant_id` on every row); one store for dev, test and production, so there is no second persistence dialect. Agent output logs live in a partitioned `job_logs` table with retention; the filesystem holds only git worktrees, which exist solely as an agent process's working directory
- **Agents:** hybrid local-first — CLI wrappers + direct API connectors

## Docs

- [docs/requirements.md](docs/requirements.md) — full product requirements
- [docs/mvp-design.md](docs/mvp-design.md) — MVP architecture, data model, screens and milestones
- [docs/m2-contract.md](docs/m2-contract.md) — agreed M2 agent/chat endpoints and payloads
- [docs/implementation-notes.md](docs/implementation-notes.md) — what is built, current deviations and known gaps

## Development

### Quick start

`make` wraps the two roles and both processes. Postgres must be running and the
`aic_owner` / `aic_app` roles must exist (see [Running](#running) and
`docs/mvp-design.md` §10).

```sh
make web-install   # once
make dev           # migrate + seed + API on :8080 + web on :3000
```

`make dev` is safe to re-run: migrations are recorded, and the seed exits
cleanly when its tenant already exists. It creates a development tenant with the
three example products, four projects and 23 work items — epics with children,
mixed statuses, dependency edges, one paused item and one pinned item — so the
board and the queue have something real to render.

Default sign-in: `owner@acme.dev` / `correct-horse-battery-staple`.

### Agents and chat (M2)

The seed also ensures two mentionable agents: `@dev` runs the local Claude CLI
(`claude -p --output-format stream-json --verbose`) and `@pm` calls an
OpenAI-compatible API (`gpt-4o-mini`). Seeding is idempotent and never overwrites
an agent you edited. API agents name the server environment variable that holds
the key (`key_env`, default `OPENAI_API_KEY`); the key itself is never stored,
returned or sent to the browser. Export it before `make dev` if you want `@pm`
to answer — without it the run fails cleanly and the thread gets a system
message naming the missing variable.

Every item has one chat thread. Mention an enabled agent (`@dev …`) to queue one
run per mention, or use the manual "Run agent" action beside the thread. The API
process owns the worker: it claims jobs from Postgres, enforces per-agent
concurrency and streams output over Server-Sent Events
(`GET /api/jobs/{id}/events`, replayable with `?after_seq=`), falling back to
`GET /api/jobs/{id}/logs` when the stream drops. CLI adapters receive a narrowed
environment (no `*KEY*`, `*TOKEN*`, `*SECRET*`, `*PASSWORD*`) and are killed as a
process group on cancel. Limits: 100 queued jobs per tenant, two workers, two
concurrent runs per agent by default, a two-minute lease and three attempts
(10s·2^(n-1) backoff capped at 5m). See `docs/m2-contract.md` for the payloads.

### Workflows and checkpoints (M3)

A workflow template is a real graph: nodes (`agent`, `integration`, `checkpoint`,
`loop`, `merge`) joined by edges that name their ports, validated on write and
again on run start (unique ids, known endpoints, no unreachable node, every
cycle through a `loop` with `max_iterations`). Starting a run snapshots the
published graph, so editing a template never changes a run in flight.

The scheduler is a stateless tick: it recomputes the ready set from the
persisted state, claims ready nodes with a lease, and dispatches them. Two
branches under one node run concurrently and are joined by a `merge` node,
which waits for the edge receipts (`delivered` / `skipped` / `dead`) — never
for a completion order. A `checkpoint` node parks its branch and waits for a
human: approve, approve with edits, reject with feedback or abort. The
decision is a conditional update, so a second person deciding the same lap
gets `409 already_decided` with the winning decision attached.

The seed publishes three templates: **Bugfix** (spec → checkpoint → implement →
review ∥ check → merge → checkpoint), **Review** (review → checkpoint) and
**Repair** (a bounded cycle with `max_iterations: 3`). The interface adds
`/runs`, `/runs/{id}` (the monitor: graph, timeline, step inspector, decision
and intervention controls) and `/checkpoints` (the inbox). The contract is in
`docs/m3-contract.md`.

### Smoke check

`make smoke` end-to-end checks a **running, seeded** server: `/healthz`,
login (session cookie in a temporary jar), `/api/queue` with a pinned item
first and no paused item, a non-empty `/api/tree` and at least one product in
`/api/products`. Assertions use `python3` (no `jq` dependency) and the cookie
jar is removed on exit.

```sh
make dev                # or: make api   (run in one terminal, then:)
make smoke              # hits http://localhost:8080 by default
AIC_URL=http://localhost:18081 make smoke   # any other instance
```

| Variable | Default |
| --- | --- |
| `AIC_URL` | `http://localhost:8080` |
| `AIC_EMAIL` | `owner@acme.dev` (the seeded owner) |
| `AIC_PASSWORD` | `correct-horse-battery-staple` |

### Rate limits

`POST /api/auth/login`, `POST /api/auth/accept-invite` and `POST /api/invites`
are rate limited in memory, per process:

| Endpoint | Key | Default limit |
| --- | --- | --- |
| `POST /api/auth/login` | client IP + lowercased email | 10 failed attempts / 5 min |
| `POST /api/auth/accept-invite` | client IP | 20 attempts / hour |
| `POST /api/invites` | acting user id | 30 invites / hour |

Exhausting a bucket answers `429 rate_limited` with a `Retry-After` header in
seconds; a successful login clears its own counter so a typo storm does not
lock the legitimate user out. Keys use the TCP peer address (`r.RemoteAddr`),
never `X-Forwarded-For` — that header is client-controlled without a trusted
proxy. Limits are per-process and reset on restart; the MVP runs one process
(see `docs/mvp-design.md` §10 and `docs/implementation-notes.md`).

| Target | What it does |
| --- | --- |
| `make dev` | `migrate`, `seed`, then API and web together |
| `make migrate` | apply schema as `MIGRATE_URL` (owner role) |
| `make seed` | development tenant + backlog as `DATABASE_URL` |
| `make api` | API only |
| `make web-install` / `make web` | web dependencies / Next.js dev server |
| `make test` | `go test -count=1` over `cmd` and `internal` against `AIC_TEST_DSN` |
| `make smoke` | end-to-end check against a running, seeded server |
| `make build`, `make fmt`, `make vet` | build, format, vet |

| Variable | Default |
| --- | --- |
| `DATABASE_URL` | `postgres://aic@localhost/aic` — runtime role |
| `MIGRATE_URL` | `postgres://aic_owner@localhost/aic` — schema owner |
| `ADDR` | `:8080` |
| `SEED_FLAGS` | extra flags for `cmd/seed` |

To seed a different tenant without dropping the database:

```sh
make seed SEED_FLAGS="-slug studio-x -email me@studio-x.dev -tenant 'Studio X'"
```

`cmd/seed` flags: `-dsn`, `-email`, `-password`, `-name`, `-tenant`, `-slug`,
`-timeout`. It refuses to reuse an email that belongs to another tenant.

### Schema

Migrations are plain SQL files in `internal/store/migrations`, embedded into the
binary and applied in version order. Each runs in its own transaction and is
recorded in `schema_migrations`, so re-running is always safe.

```sh
DATABASE_URL=postgres://aic_owner@localhost/aic go run ./cmd/migrate
```

### Tests

The isolation suite creates and drops its own databases, so it only runs against
an explicitly disposable Postgres (never a shared one):

```sh
createdb aic_test
AIC_TEST_DSN="postgres://aic_owner@/aic_test?host=/tmp&port=55432" go test -count=1 ./cmd/... ./internal/...
```

Package patterns are explicit rather than `./...` because `web/node_modules`
ships a vendored Go package that has no business in this suite.

`internal/spike` is a throwaway verification harness for the macOS sandbox; see
its README. It is not production code and must not be promoted.

### Running

Schema and runtime are separate roles on purpose: the server must not be able to
create tables, and a superuser bypasses row-level security regardless of
`FORCE`.

```sh
# 1. schema — owner role
DATABASE_URL="postgres://aic_owner@localhost/aic" go run ./cmd/migrate

# 2. runtime — non-owner, non-BYPASSRLS role granted aic_app
DATABASE_URL="postgres://aic@localhost/aic" go run ./cmd/server
```

The runtime role is `aic_app` itself or a login role that is a member of it
(`GRANT aic_app TO aic`). See `docs/mvp-design.md` §10 for why the identity
plane is reachable only through `SECURITY DEFINER` functions.

```sh
curl -c cookies.txt -X POST localhost:8080/api/auth/register -H 'Content-Type: application/json' \
  -d '{"email":"me@acme.dev","password":"correct-horse-battery-staple","name":"Orlando","tenant_name":"Acme","tenant_slug":"acme"}'
curl -b cookies.txt localhost:8080/api/auth/me
```

| Route | Purpose |
| --- | --- |
| `POST /api/auth/register` | create tenant + owner, returns a session |
| `POST /api/auth/login` | `tenant_id` optional when the user has one membership; rate limited on repeated failures (`429 rate_limited`) |
| `POST /api/auth/logout` | revokes the session server-side |
| `GET /api/auth/me` | current user, active tenant, available tenants |
| `POST /api/auth/switch-tenant` | rotates the session into another membership |
| `GET /api/auth/invite?token=` | public: tenant, email and role behind an invite link |
| `POST /api/auth/accept-invite` | public: consumes the token, joins the tenant, starts a session |
| `GET/POST /api/invites` | pending invitations (owner/admin creates) |
| `DELETE /api/invites/{id}` | revoke a pending invitation |
| `GET/POST /api/products`, `PATCH/DELETE /api/products/{id}` | product lines |
| `GET/POST /api/projects`, `PATCH/DELETE /api/projects/{id}` | nested projects; deletes refuse non-empty rows |
| `GET/POST /api/items`, `GET/PATCH/DELETE /api/items/{id}` | work items (`?project_id=&status=&label=&assignee_user_id=`) |
| `POST /api/items/bulk-status` | one status across up to 100 items, logged per item |
| `GET/POST /api/items/{id}/dependencies` | `blocked_by` edges; cycles answer `409 dependency_cycle` |
| `DELETE /api/items/{id}/dependencies/{depID}` | remove an edge |
| `GET /api/items/{id}/activity` | immutable, attributed history |
| `POST /api/items/{id}/pause`, `/unpause`, `/pin`, `/unpin` | attributed queue overrides; pause outranks pin |
| `GET /api/items/{id}/score` | reproducible base score with its factor breakdown |
| `GET /api/queue` | dispatch order: pinned first, then base score |
| `GET /api/tree` | nested projects/items with subtree progress rollup |
| `GET /api/board` | items grouped into the seven status columns |
| `GET/POST /api/agents`, `GET/PATCH/DELETE /api/agents/{id}` | agent registry; writes are owner/admin only |
| `POST /api/items/{id}/thread` | get-or-create the item's chat thread |
| `GET /api/threads/{id}`, `/messages`, `/jobs` | thread, message history and run history |
| `POST /api/threads/{id}/messages` | post a message; every enabled `@mention` queues a run |
| `POST /api/threads/{id}/jobs` | manual run, with optional instructions recorded as a message |
| `GET /api/jobs/{id}`, `/logs`, `/events` | job state, append-only logs and an SSE stream |
| `POST /api/jobs/{id}/cancel` | cancel a queued or running job |
| `GET/POST /api/workflow-templates`, `GET/PATCH/DELETE /api/workflow-templates/{id}` | graph templates; writes are owner/admin only |
| `POST /api/workflow-templates/{id}/publish`, `/versions` | publish a draft, cut a new version |
| `POST /api/items/{id}/runs` | start a run of a published template on an item |
| `GET /api/runs`, `GET /api/runs/{id}` | runs of the tenant, and one run with its steps |
| `POST /api/runs/{id}/cancel`, `/intervene` | cancel a run, inject into it, redo a node, stop a step |
| `GET /api/runs/{id}/events`, `/logs`, `/interventions` | the monitor stream, the run's log tail, the steering log |
| `GET /api/checkpoints`, `GET /api/checkpoints/{id}` | the pending inbox and the decided laps |
| `POST /api/checkpoints/{id}/decision`, `/remind` | approve / approve with edits / reject / abort; push the reminder out |
| `GET/POST /api/memory`, `GET /api/memory/manifest` | the four memory scopes, the human writes, the injection manifest |
| `GET /api/notifications`, `POST /api/notifications/{id}/read` | the notification centre |

Invitations have no mailer in the MVP: creating one returns the single-use token
(and an `accept_url`) exactly once, to be passed to the invitee out of band. Only
its SHA-256 digest is stored, and it expires after seven days.
