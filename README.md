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

## Development

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
AIC_TEST_DSN="postgres://aic_owner@/aic_test?host=/tmp&port=55432" go test ./...
```

`internal/spike` is a throwaway verification harness for the macOS sandbox; see
its README. It is not production code and must not be promoted.
