# Requirements — AI Company Super-Orchestrator

Status: draft v1 · Date: 2026-10-21 · Owner: Orlando

---

## 1. Vision

A single control plane from which one person runs an entire software company:
strategy, backlog, implementation, review, release and support — with AI agents
doing the bulk of the execution and the human acting as orchestrator,
reviewer and decision-maker through explicit checkpoints.

The system is **not** another chat wrapper. It is an orchestrator: work is
structured (hierarchy), execution is structured (workflows), and human
intervention is structured (checkpoints) — everything auditable.

### 1.1 Inspiration: Apiary

This product is inspired by [Apiary](https://github.com/orlandoburli/apiary),
the owner's open-source task-driven agent orchestration harness (sources →
router → workflows → runners/agents, with approval gates, failover and agent
memory). The two are complementary:

- **Apiary** is a headless, YAML-configured daemon routing tasks from external
  trackers to agents. It owns no backlog and has no collaborative UI.
- **AI Company** is a work-ownership control plane: it *is* the backlog
  (multi-hierarchy items), adds board/chats/checkpoint inbox, and treats
  agents as first-class assignees.

Where Apiary has already solved a hard execution problem (approval durability,
failover, resume, memory, spawn markers), AI Company borrows the pattern rather
than reinventing it. Borrowed requirements are tagged `[apiary]`. A future
option is embedding Apiary as the execution engine — see mvp-design.md §13.

### 1.2 Multi-tenancy (top-level requirement)

AI Company is a **multi-tenant product**: every entity — products, projects,
items, runs, chats, agents, credentials, activity — belongs to exactly one
tenant, and no query may ever cross tenant boundaries. The owner is simply
**tenant #1** of the first deployment. A tenant has **members** with roles
(`owner`, `admin`, `member`; read/comment-only `guest` later) and is multi-user from day
one: several humans can watch, steer and decide on the same runs. MVP ships
membership, roles and invitations on a self-hosted deployment; the fine-grained
permission matrix and a hosted SaaS come later — but nothing in the domain model
may assume a single user per tenant.

## 2. Problem statement

A solo software-house owner maintains and evolves three commercial products
(FazGiro, Vendamatica, ProntaLoja). Today every task — triage, coding, review,
PRs, deploy coordination, Jira updates — is done manually. AI coding agents
help, but using them ad-hoc (one chat at a time) doesn't scale:

- no persistent structure linking agent output to the work item it belongs to;
- no way to compose multi-step processes (research → implement → review → PR);
- no safe way to let an agent run unsupervised with human gates;
- no single view of "what is happening right now" across products.

## 3. Personas

| Persona | Description |
| --- | --- |
| **Owner (primary, MVP)** | Solo operator. Defines strategy and backlog, approves checkpoints, reviews PRs, talks to agents. |
| **Agent (system actor)** | Registered AI connector with a role (dev, reviewer, pm, qa, researcher). Executes workflow steps, replies in chats, produces artifacts. |
| Future: **Client/Stakeholder** | Read-only visibility of progress per product. Out of MVP scope. |

## 4. First use case (driving scenario)

> The owner opens the app on Monday. The board shows the three products.
> Under **FazGiro → "NF-e compliance" project**, a bug item "Wrong ICMS
> calculation for interstate sales" is moved to *In Progress* and a
> **Bugfix Workflow** is started: (1) *pm* agent refines the item and pulls
> context from the linked GitHub issue; (2) *dev* agent implements the fix in
> a branch; (3) **checkpoint**: owner reviews the diff summary in chat and
> approves; (4) *reviewer* agent opens a PR and runs checks; (5)
> **checkpoint**: owner merges; (6) the item moves to *Done*, Jira is synced
> and the activity log records everything.

## 5. Domain concepts

| Concept | Meaning |
| --- | --- |
| **Tenant** | The isolation boundary. Every entity belongs to exactly one tenant; tenant #1 is the owner's software house. |
| **Product** | Top-level commercial product (FazGiro, Vendamatica, ProntaLoja). Links to a GitHub repo and optionally a Jira project. |
| **Project** | A goal-oriented grouping inside a product — **recursive**: projects nest to arbitrary depth (e.g., "NF-e compliance → Emissor → MDF-e"). |
| **Work Item** | Recursive unit of work with arbitrary nesting depth. Its **kind is user-named** — there is no fixed taxonomy (the owner may call kinds "bug", "epic", "chapter", anything); hierarchy comes purely from parent/child nesting. Has status, assignee (human or agent), priority, links. |
| **Workflow Template** | Versioned **graph** of nodes and edges (branching, joins, bounded cycles). Node kinds: `agent`, `checkpoint`, `integration`, `merge`. Can filter which user-named item kinds it applies to. |
| **Workflow Graph** | The rendered form of a template version: nodes + directed edges, including back-edges for cycles. Execution follows edges — never list order or canvas position. |
| **Node** | One unit of execution in a graph (`agent`, `checkpoint`, `integration`, `merge`). Carries its own error policy and workspace policy. |
| **Edge** | Directed dependency between nodes: fan-out creates concurrent branches, fan-in feeds a join. |
| **Merge Node** | First-class join that reconciles two or more branch outputs into one result (default: all inputs must succeed). |
| **Workflow Run** | One execution of a template version against a target (usually a work item). Has state, node runs, logs and artifacts; independent nodes run concurrently. |
| **Agent** | A configured AI connector instance: kind (CLI/API), model, role, capabilities, working-directory and command policy. |
| **Checkpoint** | A step that suspends the run and requests a human decision: approve, approve-with-edits, reject-with-feedback, or abort. |
| **Intervention** | A mid-run human action on a live agent job: inject instructions (steering), redo a step, or stop. Recorded and auditable. |
| **Chat Thread** | Conversation between human and agent(s) bound to a context: item, run, checkpoint or free-standing. |
| **Activity** | (a) board-card view of work items; (b) immutable audit event in the activity log. |
| **Artifact** | Anything produced by a step: branch, commit, PR URL, diff summary, file, report, log. |
| **Integration** | Connection to an external system: GitHub, Jira, or an AI provider. |
| **Soul file** `[apiary]` | Markdown persona/system prompt of an agent, versioned with the repo. |
| **Agent memory** | Tiered notes/facts written during execution and recalled into later work: **run-scoped** (validated outputs, later nodes of the same run), **agent-scoped** (an agent's notes across runs), **workspace-scoped** (facts shared across runs in a product/workspace). See FR-MEM-*. |
| **Team / Membership** | A user's link to a tenant with exactly one role (`owner`, `admin`, `member`; `guest` post-MVP). A tenant has exactly one owner and any number of members. |
| **Trigger** `[apiary]` | Rule that decides whether an item **starts a workflow automatically** (labels/type/product). Independent of priority ranking, which decides only *which* task is worked first (FR-WF-10). MVP. |
| **Ready** | An item eligible to compete for a worker: no open `blocked_by` dependency, and not an epic with open sub-issues. Only ready items are ranked and dispatched (FR-DEP-5). |
| **Dependency** | A first-class `blocked_by` edge between work items. A transitively-blocking chain determines which items are *ready* to rank, and how much an issue unblocks when it clears (FR-DEP-1..6). |
| **Failure backoff** | An item's persisted consecutive-failure count and the wait it must serve before it is dispatchable again. Backoff is **eligibility**, never rank: it gates dispatch only, and on expiry the item re-enters the queue and is scored from current inputs like any other candidate (FR-RET-1..8). |
| **Paused** | An item removed from eligibility by an explicit human action. Paused is **not** a failure and **not** a backoff: no failure counter moves, no wait is scheduled, and the item keeps its labels and score. It is dispatchable again only when a human unpauses it (FR-QUE-2..3). |
| **Pinned** | An item moved to rank 1 by an explicit human action, above every scored item regardless of score. The pin is authoritative over the ranking, persists across ranking runs and restarts, and is cleared only by an explicit unpin or by the item closing (FR-QUE-4). |
| **Queue override** | One of the three human queue actions — **pause**, **pin to top**, **start now** — that acts on the queue rather than on scores. Overrides are authoritative over order (and, for start-now, over the wait), never inputs to the ranker, and never bypass hard limits (FR-QUE-1..7). |
| **Skill file** | A named, versioned **procedural** document — steps, commands, pitfalls, done criteria — stored in Postgres and loaded on demand when relevant. A skill changes *how* an agent works; memory recalls *facts*. Authoring or approving one is a capability grant restricted to `owner`/`admin`. See FR-AG-11..13. |
| **Task file** | The single explicit artifact handed to an agent for one node execution: the task statement plus the context envelope, with every section labelled `trusted` or `untrusted`. See FR-CTX-2. |
| **Trust level** | Per-section label on task-file content: `trusted` (operator/author-authored) or `untrusted` (external — fenced, provenance-labelled, sanitized, passed as data). Untrusted content never grants a capability. See FR-CTX-3..4. |
| **Item-scoped memory** | The fourth memory scope: facts tied to one work item and persisting across that item's runs — unlike run scope, which dies with the run. Extends the three scopes in **Agent memory**; see FR-MEM-8. |
| **Context bundle** | The assembled prompt for one node execution: soul → selected skills → memory → task file, untrusted sections always last, all under one token budget. See FR-CTX-1..6. |

## 6. Functional requirements

IDs are used for traceability into MVP milestones (see mvp-design.md §11).

### 6.1 Project management (PM)

- **FR-PM-1** CRUD for products, pre-seeded with FazGiro, Vendamatica, ProntaLoja.
- **FR-PM-2** CRUD for projects within a product, with **arbitrary nesting** (sub-projects inside projects).
- **FR-PM-3** CRUD for work items with **arbitrary nesting** (parent/child). The item **kind is free-form and user-defined** — no built-in type list; the user names kinds whatever they want (optionally reused as per-product kind presets). Plus title, description (markdown), **manually settable priority** with recorded provenance (FR-PRI-1..2), labels, due date, and first-class `blocked_by` dependency edges (FR-DEP-1..6).
- **FR-PM-4** Item status workflow: `backlog → todo → in_progress → waiting_human → in_review → done` (+ `cancelled`). Transitions by human, agent or workflow run, all logged.
- **FR-PM-5** Assignment of an item to the owner **or to an agent**.
- **FR-PM-6** Tree navigation (product → nested projects → nested items) with expand/collapse and per-subtree progress rollup (% done).
- **FR-PM-7** Link an item to external refs: GitHub issue/PR, Jira issue, URLs.

### 6.2 Activities (Jira-like) (ACT)

- **FR-ACT-1** Board view grouped by status with drag-and-drop, filterable by product/project/label/assignee.
- **FR-ACT-2** List/backlog view with ordering and bulk status change.
- **FR-ACT-3** Item detail page with tabs: Details, Activity, Chat, Runs, Links.
- **FR-ACT-4** Immutable **activity log**: every mutation (status, assignment, run event, checkpoint decision, integration sync) recorded with actor, timestamp and before/after.
- **FR-ACT-5** "Happening now" strip: live runs, pending checkpoints, failed steps — one click attaches to the live run monitor (FR-MON-1).

### 6.3 Workflows (WF)

- **FR-WF-1** CRUD + versioning of workflow templates as **graphs** (nodes, edges, join semantics), executed concurrently from day one. A linear workflow is just a graph with a single path — there is no separate "linear mode".
- **FR-WF-2** Node kinds:
  - `agent` — run a prompt/task on a chosen agent role with item context;
  - `checkpoint` — pause for human decision;
  - `integration` — deterministic action (create branch, open PR, sync Jira, comment on issue);
  - `merge` — reconcile the outputs of two or more branches (first-class node, not a special case of `integration`).
- **FR-WF-3** Start a run on a work item (or project). Run states: `queued → running → waiting_checkpoint → running → completed | failed | cancelled`.
- **FR-WF-4** Node execution passes forward a **context envelope**: item data, outputs of satisfied inbound nodes, artifacts, chat refs, injected memory (FR-MEM-6).
- **FR-WF-5** Run view: node timeline with concurrently running nodes shown side by side, per-node status, live log stream, produced artifacts, token/cost usage when available.
- **FR-WF-6** Retry a failed node; resume an interrupted run from its last completed node set; abort a run. (For redoing *any* node mid-run, including succeeded ones, see FR-MON-4.)
- **FR-WF-7** Built-in templates seeded at install: *Bugfix*, *Feature*, *Code Review*, *Research/Spike*.
- **FR-WF-8** `[apiary]` Agent steps produce **structured output** (JSON-schema validated); declared fields persist as **workflow memory** injected into later steps' context (FR-MEM-2). (MVP)
- **FR-WF-9** `[apiary]` Runs are crash-safe: parked checkpoints rehydrate after a server restart; interrupted runs are marked and resumable from the last completed node set (completed nodes replay from cache, side effects not re-fired). (MVP)
- **FR-WF-10** `[apiary]` **Auto-dispatch and triggers**: the scheduler starts a workflow for items that **match a trigger rule** (labels/kind/product), a bounded number per cycle, skipping items that already have an active run. `once` (a rule fires only on its first match) and `exclusive` (no concurrent run for the same item) semantics apply. Trigger eligibility is **independent of the priority ranking** — the ranking decides which task to work on first, never whether a task starts (FR-PRI-15). (MVP)
- **FR-WF-11** `[apiary]` Agents can **spawn child items** (decomposition) via output markers with idempotency keys; spawned items are first-class and run their own workflows. (post-MVP)
- **FR-WF-12** `[apiary]` **Graph cycles** with explicit bounds: a back-edge declares `max_iterations` (and optionally `restart_from`); reviewer feedback is injected into the retry prompt and the audit trail shows every lap. A cycle without `max_iterations` is a template **validation error** — it cannot be saved or started. (MVP)
- **FR-WF-13** **Concurrent branch execution**: nodes whose inbound edges are satisfied are scheduled immediately and run in parallel; a run's progress is the state of its node set, not a cursor. Each code-touching branch executes in its **own workspace** (git worktree/branch scoped to node execution), never a shared mutable directory. (MVP)
- **FR-WF-14** **Merge as a first-class node**: a `merge` node declares its inbound edge set and a reconcile policy (`union` by default, `pick`/`prefer` by declared key); it becomes ready only when every inbound edge is satisfied, then produces one output for downstream nodes. A merge with a missing inbound output fails the node, not the whole run, per its error policy. (MVP)
- **FR-WF-15** **Lease-based claiming**: every node execution is claimed by a worker with a renewable lease and heartbeat; an expired lease is reclaimable by another worker, and reclaim is logged as an event (never silent duplication). Claiming is atomic per node execution. (MVP)
- **FR-WF-16** **Per-node error policy**: each node declares `on_error` ∈ `stop_workflow | continue_regular_output | continue_error_output`, plus `max_attempts` and backoff. Policy is evaluated per node execution so one failing branch can either halt the run, continue with an empty output, or route to an error edge handled by downstream nodes. (MVP)
- **FR-WF-17** **Structured-output repair**: when a step's output fails schema validation, the engine issues one repair turn (schema + validation errors + the agent's raw text) before failing the node. The raw text is always persisted on the attempt — repair never discards work. (MVP)

### 6.4 Checkpoints (CP)

- **FR-CP-1** A checkpoint step renders a decision card: summary of work so far, artifacts (diff, PR, plan), and the agent's self-assessment.
- **FR-CP-2** Decisions: **Approve** (continue), **Approve with edits** (human modifies output/params, continue), **Reject with feedback** (feedback goes to the agent; step or run re-executes per policy), **Abort run**.
- **FR-CP-3** Pending checkpoints are first-class: badge in nav, entry in "Happening now", optional desktop notification.
- **FR-CP-4** Every decision is recorded in the activity log and appended to the run's chat thread.
- **FR-CP-5** Timeout policy per checkpoint: remind after N hours; never auto-approve by default.
- **FR-CP-6** `[apiary]` Each pass through a checkpoint inside a rework loop is its own durable decision request (lap-scoped IDs); the audit trail shows every lap's answer. (MVP)
- **FR-CP-7** `[apiary]` Checkpoints may declare **structured fields** (string/text/boolean/number/choice) the human fills in on approval; values enter workflow memory and select the outgoing edge at a branch node. (MVP — required for human steering of a concurrent graph.)

### 6.5 Chats (CH)

- **FR-CH-1** Threads bound to context: item, run, checkpoint, or free-standing per product.
- **FR-CH-2** Messages from human, agents and system events, rendered in one timeline with streaming agent replies.
- **FR-CH-3** Mention an agent (`@dev`, `@reviewer`) to route the message to that agent with the thread + item context.
- **FR-CH-4** Agent replies may attach artifacts and propose actions ("open PR", "start run") rendered as actionable buttons.
- **FR-CH-5** Full history persisted and searchable.

### 6.6 Agents & AI connectors (AG)

- **FR-AG-1** Agent registry: name, role (`pm`, `dev`, `reviewer`, `qa`, `researcher`), kind, model, capabilities, cost limits.
- **FR-AG-2** **CLI connectors**: spawn local CLI agents (Claude Code, Codex, Aider…) as supervised subprocesses in a sandboxed working directory (git worktree), streaming structured output where the CLI supports it.
- **FR-AG-3** **API connectors**: direct calls to Anthropic/OpenAI APIs with tool use, for roles that don't need repo write access (pm, researcher, reviewer summaries).
- **FR-AG-4** Unified **job/event model** regardless of kind: `queued → running → (stdout|tool_call|artifact|usage)* → succeeded | failed | cancelled`, with cancellation.
- **FR-AG-5** Concurrency control: global worker pool + per-agent semaphore (MVP default: 2 concurrent jobs).
- **FR-AG-6** Command/filesystem policy per agent: allowed working roots, denylist of destructive commands, secrets never exposed to agent environments.
- **FR-AG-7** Usage tracking (tokens, wall time, estimated cost) per job and aggregated per product.
- **FR-AG-8** `[apiary]` Agent persona defined by **soul markdown** (`agents.soul_md`, a Postgres column) layered under the step prompt. Versioned with the agent row rather than the repo, so personas are tenant-scoped and editable in the UI. (MVP)
- **FR-AG-9** `[apiary]` **Fallback chains**: job failures classified (rate-limited / credit-exhausted / aborted); the failing connector class pauses with a per-kind cooldown and the job fails over to the agent's next runner/model. (post-MVP)
- **FR-AG-10** `[apiary]` **Agent memory** tiers: run-scoped, agent-scoped, workspace-scoped (see §6.12). (MVP)
- **FR-AG-11** **Skill files are versioned procedures, not memory**: a skill is a named, versioned markdown/procedural document (`skills` / `skill_versions`, mvp-design.md §3) with a one-paragraph `description` stating when it applies — the trigger summary used for matching. It is stored in Postgres like every other durable artifact (FR-TN-4), scoped to a tenant and optionally a product (tenant-wide when `product_id IS NULL`), and is **distinct from memory**: memory is recalled **facts** about a run, item, agent or workspace; a skill is a **procedure** that changes *how* the agent works. A run never writes a skill and skills are never summarized into memory. (MVP)
- **FR-AG-12** **Skills are trusted operator content and a capability grant**: skill text loads as **trusted** context and can authorise command and tool patterns the agent would not otherwise know, so authoring, editing, enabling and installing a skill requires `owner` or `admin` (FR-TN-8). An agent may draft a skill only as an inert `agent_draft` revision that loads for nobody until a human approves it (`approved_by`); approved versions are immutable and an edit creates a new `version`. Skill content is never loaded from an untrusted section of the task file (FR-CTX-4). (MVP)
- **FR-AG-13** **Scoping, matching and attachment bound**: skill visibility follows the run's tenant and product — product skills first, then tenant-wide, never another tenant's or another product's (FR-TN-1). Matching uses only structured, trusted inputs: the node's declared skill selectors, the agent's `skills_policy_json`, the task statement, and each skill's own `description`. Untrusted text is never a selector. At most `max_attach` skills (default 3) are attached per node execution, and auto-selection is skipped entirely when the budget cannot fit them (FR-CTX-5); the bound is a context guard, not a ranking. An agent may also request a skill explicitly by name through a tool/signal; that request is checked against visibility and grants like any other. (MVP)

### 6.7 GitHub integration (GH)

- **FR-GH-1** Connect via token (GitHub App later); link one repo per product.
- **FR-GH-2** From an item: create/switch to a branch (`<type>/<item-id>-<slug>`), used as the agent's worktree.
- **FR-GH-3** Open a PR from a run, with body generated from item + run summary; link PR as artifact.
- **FR-GH-4** Sync PR status (open/checks/reviews/merged) into the run and item; merged PR can auto-complete the item.
- **FR-GH-5** Import/link GitHub issues as items; keep title/state synced (poll or webhook).
- **FR-GH-6** Fallback: if API token absent but `gh` CLI is authenticated, use `gh` for the same operations.

### 6.8 Jira integration (JR)

- **FR-JR-1** Connect a Jira Cloud site (email + API token); map one Jira project per product.
- **FR-JR-2** Import Jira issues as items and link existing items to issues.
- **FR-JR-3** Two-way status sync with an explicit mapping table (e.g., Jira "In Progress" ↔ `in_progress`).
- **FR-JR-4** Push key events as Jira comments (run started, checkpoint pending, PR opened). Nice-to-have for MVP.

### 6.9 Notifications (NT)

- **FR-NT-1** In-app notification center (pending checkpoints, failed runs, sync errors).
- **FR-NT-2** Desktop/browser push for pending checkpoints. (Email/webhook later.)
- **FR-NT-3** `[apiary]` Command-channel notifications: fire a user-defined shell command (ntfy, osascript, Slack webhook via curl) on watched events (checkpoint pending, run failed). (MVP)

### 6.10 Tenancy, teams & access (TN)

- **FR-TN-1** Every entity is tenant-scoped (`tenant_id`); all reads/writes are filtered by the caller's tenant — cross-tenant access is impossible at the repository layer and enforced by isolation tests.
- **FR-TN-2** Accounts and sessions: registration creates a tenant with the registrant as `owner`; further users join only through a membership in that tenant (FR-TN-6..7). No code path assumes one user per tenant. (MVP)
- **FR-TN-3** All credentials (GitHub, Jira, AI providers) are stored per tenant.
- **FR-TN-4** Agent execution resources are tenant-separated: worktrees, job queues, memory rows and logs. Postgres is the single durable store — domain rows, activity log, agent output logs, memories and soul content all live there, and the filesystem holds nothing the product reads back except git worktrees (an agent process's working directory only).
- **FR-TN-5** Activity log, notifications, chats and checkpoints are tenant-scoped; there are no shared/global feeds.
- **FR-TN-6** **Tenant membership**: users belong to a tenant through a `tenant_memberships` row carrying exactly one role — `owner` (1 per tenant), `admin`, `member`, `guest`. Roles are resolved per request; removing a membership revokes access immediately. (MVP)
- **FR-TN-7** **Invitations**: owner/admin invites by email; a single-use signed invite link with expiry creates the membership on acceptance. Pending invites are listable and revocable; acceptance and revocation are audit events. (MVP)
- **FR-TN-8** **Per-role permissions** enforced server-side (never in the UI only): `owner` = tenant settings, billing, role changes, ownership transfer; `admin` = products/projects/agents/integrations + all member actions; `member` = create/edit items, start runs, act on checkpoints, intervene in runs; `guest` = read + comment (post-MVP). Fine-grained per-resource grants are post-MVP. (MVP = owner/admin/member)
- **FR-TN-9** **Multi-user concurrency** on shared surfaces: checkpoint decisions are atomic (first decision wins; later attempts return `already_decided` with the deciding user); the checkpoint inbox is per-user with read/unread state over one shared queue; live runs support several concurrently attached viewers, and interventions are serialized and attributed per node execution so two users' instructions cannot interleave into one prompt. (MVP)
- **FR-TN-10** **Membership lifecycle**: deactivate/remove a member (their past activity stays attributed), transfer ownership, and record every membership/role change in the activity log. (MVP; transfer ownership may slip one milestone)

### 6.11 Live monitoring & intervention (MON)

- **FR-MON-1** **Run monitor**: real-time panel for any active run/job — live event stream (agent output chunks, tool calls, files touched, current step, tokens/elapsed), attachable from dashboard, board, item or run view with one click. (MVP)
- **FR-MON-2** Raw job logs accessible in the same panel (full stdout/stderr tail, downloadable). Logs live in Postgres (`job_logs`, append-only, partitioned monthly with a configurable retention window — default 90 days); artifacts, step results and activity log entries are records and are never dropped with log partitions. (MVP)
- **FR-MON-3** **Intervene (steering)**: inject a natural-language instruction into a running agent job without losing its work — delivered into the agent's live input when the connector supports it, otherwise the job is stopped and resumed with the instruction appended to its context (worktree preserved). The agent's acknowledgement is visible in the stream. (MVP)
- **FR-MON-4** **Redo node**: re-execute any node of a run — even a succeeded one — optionally with new instructions; the prior attempt's output is archived (never deleted), downstream nodes reset, and the run resumes from the redone node. Unaffected branches keep running. (MVP)
- **FR-MON-5** Stop a running job or the whole run from the monitor. (MVP)
- **FR-MON-6** Every intervention (inject/redo/stop) is recorded with actor, timestamp and content in the activity log, and mirrored as a system message in the run's chat thread. (MVP)

### 6.12 Memory (MEM)

Tiered memory, written by execution and read back into prompts under an explicit budget. Everything is tenant-scoped and every injected entry is attributable to its source.

- **FR-MEM-1** **Three tiers with fixed boundaries**: **run-scoped** (lifetime = one run; derived from that run's validated outputs), **agent-scoped** (lifetime = many runs; owned by one agent), **workspace-scoped** (lifetime = the product/workspace, across runs; shared by all agents working there). A higher tier is never silently mutated by a run. (MVP)
- **FR-MEM-2** **Run-scoped memory** is built from **validated** node outputs (`AIC_OUTPUT` / output schema): declared keys are written on node success only, and injected into every later node of the same run (including nodes on other branches once the join is satisfied). Failed or unvalidated output never writes memory. (MVP)
- **FR-MEM-3** **Agent-scoped memory** persists across runs in Postgres (`memory_entries` with `scope_kind=agent`), keyed `tenant/agent`. Content stays plain markdown so it remains human-readable and human-editable, but it is a revisioned, tenant-scoped row rather than a file on disk — no path to lose, no index to reconcile, and identical behaviour in dev and production. Every agent memory write creates a revision (FR-MEM-5); prior revisions stay inspectable in the UI. Content that genuinely belongs in version control (handbooks, long-lived personas) lives in the repo, not in memory rows. (MVP)
- **FR-MEM-4** **Workspace-scoped memory** holds facts for a product/repo (conventions, gotchas, decisions) shared by every agent and item in scope; writes come from human edits, a `memory.write` integration node, or an agent marker restricted to the current workspace. (MVP: explicit writes + human edits; automatic promotion from lower tiers post-MVP)
- **FR-MEM-5** **Write mechanism** is explicit, never implicit: either a deterministic `integration` node with `type: memory.write` (no agent turn) or the `AIC_MEMORIZE` plain-text marker parsed from agent output. Every entry stores provenance (run, node, item, author) and a dedupe/revision key; writes are append-only — a rewrite creates a revision, never destroys the prior one. (MVP)
- **FR-MEM-6** **Retrieval & injection respect the context budget**: each node declares memory selectors (`full` / `summary` / `none` per tier, or an explicit key list) plus caps on entries and tokens; over budget, entries are summarized oldest-first and truncation is recorded. Reads use a per-scope watermark so concurrent writes cannot change what an already-ready node sees, and the context envelope lists exactly which entries were injected. (MVP)
- **FR-MEM-7** **Memory inspector**: per scope, list entries with provenance and size, preview what a given node would inject, edit/forget an entry with an audit event; forgotten entries are excluded from future injections but remain in history. (MVP)
- **FR-MEM-8** **Fourth scope — item-scoped memory**: `memory_entries.scope_kind` gains **`item`** (`scope_ref` = `items.id`), extending — not replacing — FR-MEM-1's three tiers. Lifetimes, stated plainly: **run** = one run, dies with it; **item** = one work item, **across runs** (a multi-run bugfix keeps what earlier runs learned about that item); **agent** = one agent, across items and runs; **workspace** = the product, across everything in it. Run scope does not survive the run; item scope does not survive the item (entries stay readable after it closes, for audit). Item scope is written only through FR-MEM-5's explicit paths, is attributed, and a run writing run memory never mutates item, agent or workspace rows — FR-MEM-1's no-silent-promotion rule holds in every direction. (MVP)
- **FR-MEM-9** **Four tiers, one injection order**: injection runs narrowest to widest — **run → item → agent → workspace** — each tier under its own FR-MEM-6 selector and cap, all inside the single budget of FR-CTX-5 (there is no second budget system). Watermarks stay per scope, so a late item or workspace write cannot change what an already-ready node sees. (MVP)

### 6.13 Priority scoring & ranking (PRI)

Ranking answers one question: **in what order are ready items picked up for work?** It has two layers — a deterministic base score (FR-PRI-3) that is both the LLM's input and the fallback, and an LLM ranking pass that is the authority over the final order (FR-PRI-5). Only the order is advisory; item state is not: priority stays a human-set label (FR-PRI-1..2), and a ranking run is a separate, auditable record (FR-PRI-6..7).

- **FR-PRI-1** **Manual priority labels**: priority is a label, not a separate numeric field — `priority:p0`, `priority:p1`, `priority:p2` (aliases `high`, `medium`, `low`, normalised on write), or none. Any user with item-write permission (FR-TN-8: `owner`, `admin`, `member`) may set or clear it from item detail or the board; the value, actor and timestamp are recorded and every change is an `activity_log` entry. A sub-item with no priority label **inherits the nearest ancestor epic's label** for scoring and display; an explicit label on the sub-item wins. Manual labels are authoritative. (MVP)
- **FR-PRI-2** **The ranker orders, it does not edit**: no scoring or ranking path writes `items.priority`, status, assignee or any other item field. A ranking run influences only the dispatch order (FR-PRI-15) and what the UI displays as the queue; every item change still requires an explicit human or workflow action. (MVP)
- **FR-PRI-3** **Deterministic base score (Step 1)**: the reproducible integer score for a ready item is the sum of:
  - **priority label** — `priority:p0`/`high` **+100**, `priority:p1`/`medium` **+50**, `priority:p2`/`low` **+10**, none **0** (after inheritance, FR-PRI-1);
  - **`bug` label** — **+30**;
  - **unblocking power** — **+25 for every open item that waits on it**, directly or transitively (walk the `blocked_by` graph, FR-DEP-2);
  - **open PR** — **+20** when the item already has an open PR ("finish what was started");
  - **age** — **+1 per day since `created_at`, capped at +30**.

  The weights are one named, versioned set (`weights_version`, e.g. `pri-base-1`); the same inputs under the same version always produce the same score. Scores are not clamped — they are inputs and a fallback ordering, not the 0–1000 display score. (MVP)
- **FR-PRI-4** **The factor breakdown is persisted**: each ranked item stores its base score, `weights_version`, and each factor's input, weight and contribution, so the UI can explain the number without recomputing it. A factor absent from the breakdown was not scored — there are no hidden inputs. (MVP)
- **FR-PRI-5** **LLM ranking pass (Step 2 — authority)**: a configured model reorders the ready set. It **overrides** the base-score order; the base score is its input and fallback (FR-PRI-13). Criteria, most to least important:

  1. **Broken production / revenue** — production bugs, rejected fiscal documents, a blocked customer and security issues (SSRF, CSRF, data leaks) first. `p0`/`high` and `bug` labels reinforce this.
  2. **Blocking roots** — an item that unblocks others rises in proportion to how much it unblocks.
  3. **Finish before starting** — open PR, already in implementation or QA, ahead of items still waiting for design.
  4. **Pipeline health** — broken CI or infrastructure (stuck pipeline, staging down, full disk) blocks everyone; treat it as a blocking root of everything.
  5. **Epic order** — sub-items of one parent follow the epic's logical sequence (baseline → CI step → batches); QA of a feature follows its implementation.
  6. **Tie-breaker** — oldest first.

  The pass may investigate only as much as needed to decide, at most **~15 lookups**, and runs **hourly** (FR-PRI-8). Output contract: every ready item, most to least urgent, each with a `score` **0–1000 consistent with the emitted order** and a **one-sentence `reason`**; the **stalled blocking roots** (FR-PRI-14); and a **2–3 sentence summary** naming what goes first and why, plus any pipeline risk. Shape (ids shown as issue numbers; stored ids are ULIDs — mvp-design.md §3):

  ```json
  {"ranking":[{"number":6987,"score":950,"reason":"root: unblocks 8 test batches"}],"roots":[{"number":6987,"unblocks":8,"status":"PR #7007 with CI pending for 2h"}],"summary":"Ranking starts with 6987 because it unblocks eight test batches; the pipeline risk is a CI queue stuck for 2h. No other ready item blocks more than one dependent."}
  ```

  (MVP)
- **FR-PRI-6** **Ranking run lifecycle**: one run at a time per tenant, `running → ready | failed`, superseded by the next run and never deleted. A `ready` run is immutable; its entries are the queue's order of record until a newer run becomes ready or it expires (FR-PRI-13). (MVP)
- **FR-PRI-7** **Provenance & explainability**: every queue position is attributable — rank, 0–1000 score, one-sentence reason, base score + factor breakdown, `weights_version`, model and `computed_at`. Every item label is attributable as `manual` (actor + timestamp) or `inherited` (ancestor item id). A run past its expiry renders as stale. "Why this rank?" shows the base factors and the LLM reason side by side. (MVP)
- **FR-PRI-8** **Budgeted, cached, out-of-band**: ranking runs on an hourly schedule and on demand after a material change (ready set, label, dependency, open-PR state); never inside a page render or board interaction. A run is reused while its `input_fingerprint` is unchanged. Per-tenant budgets and the ~15-lookup cap bound LLM calls; an over-budget run degrades rather than waiting. (MVP)
- **FR-PRI-9** **Untrusted input / prompt-injection safety**: the ranker reads item titles, descriptions, comments and imported GitHub/Jira text — untrusted content an attacker may have written. It is passed as **data** in a delimited payload, never as instructions; readiness, labels, dependencies and PR state are database facts the text cannot change. The ranker has **no tool access and no write path**: it returns an ordering and nothing else, and it is advisory over *order*, not over item state (FR-PRI-2). Same threat model and containment as the agent runner — see mvp-design.md §10, not restated here. (MVP)
- **FR-PRI-10** **Scoping**: a run ranks the ready items of one tenant across that tenant's products — one queue, one order — so a single owner never has two competing "next" items. Base weights may be overridden per product and the override is recorded in `weights_version`; ranks and scores never cross tenant boundaries (FR-TN-1). (MVP)
- **FR-PRI-11** **Ready set**: an item is ranked iff it is (a) not `done` or `cancelled`, (b) **not blocked** — no open `blocked_by` dependency (FR-DEP-4), and (c) **not an epic** — no open sub-items. Epics are excluded because the work lives in their sub-items; blocked items are excluded because no rank position can start them. The ready set is recomputed from persisted state on every run, never from a cached rank. (MVP)
- **FR-PRI-12** **Output validation**: a run is accepted only if every ready item appears exactly once, scores are integers in 0–1000, and **scores are non-increasing as rank goes from most to least urgent**. A score that contradicts the emitted order — or a missing, invented or duplicated item — makes the output invalid: the run is marked `failed` and the queue uses the previous fresh run or the base-score order (FR-PRI-13). (MVP)
- **FR-PRI-13** **Degradation — never an unordered queue**: a ranking older than **3 hours** is ignored, and so is a failed, invalid or over-budget run. In every such case the queue falls back to the deterministic base score, ordered by `base_score` desc, then age desc, then id. A failed run is recorded with its failure kind and does not block the next hourly attempt; the queue is never empty and never unordered. (MVP)
- **FR-PRI-14** **Stalled blocking roots**: each run reports the ready items that block others but are not moving — **no run in progress**, or **a PR whose CI is stuck or red** — with the number of open dependents and a one-line status (e.g. `PR #7007 with CI pending for 2h`). They are shown in the queue view and named first in the summary. (MVP)
- **FR-PRI-15** **Which task gets the next worker**: workers and agents are a **scarce, bounded pool** (per-run `max_concurrency`, per-agent `concurrency_limit`, global worker pool §6.6), so the ranking exists to allocate that scarcity. It answers exactly one question: of the tasks currently competing for a worker, **which is the highest-scoring ready one**. Among *ready* items — unblocked and not an epic (FR-DEP-5, FR-PRI-11) — pure descending score order decides, and work is dispatched in that order, so an item is never started while a higher-scoring ready item is still waiting. An item whose dependencies are unmet, or that has no matching trigger rule (FR-WF-10), is **not** competing for a worker at all: it is excluded from the ready set and simply waits, and its high score never justifies starting work that cannot proceed. The ranking **never causes work to start** — it orders the queue the owner works through and the order in which already-triggered, ready items are dispatched; eligibility to start is decided solely by FR-WF-10. Dispatch stays bounded per cycle (`dispatch_batch_size`, default 1) and obeys the lease/claim rules (FR-WF-15). A stale or absent ranking is ignored and the deterministic base-score order is used instead (FR-PRI-13). (MVP)

### 6.14 Dependencies (DEP)

Dependencies are first-class edges between items, not labels and not a derived count: readiness uses them directly (FR-PRI-11) and ranking uses their transitive shape (FR-PRI-3, FR-PRI-5).

- **FR-DEP-1** **`blocked_by` edges (CRUD)**: a directed edge `A blocked_by B` may connect any two items of one tenant, across projects and products. Edges are tenant-scoped, attributed and recorded in `activity_log`; self-edges are rejected. (MVP)
- **FR-DEP-2** **Transitive resolution**: dependents (items that wait on X, directly or transitively) and dependencies (items X waits on) are resolved by walking the edge graph with a recursive query, not read from a stored count. The same walk backs unblocking power (FR-PRI-3), readiness (FR-PRI-11) and stalled-root counts (FR-PRI-14). (MVP)
- **FR-DEP-3** **Cycle detection**: creating an edge that would close a cycle (`A → … → A`) fails with `409 dependency_cycle` before it is stored. Bulk import and re-parenting run the same check per edge; cycles are never repaired by silently dropping an edge. (MVP)
- **FR-DEP-4** **Open vs closed**: an edge is open while the blocking item is not `done` or `cancelled`. Closing the blocker — status change, cancel or deletion — removes its open incident edges and re-evaluates dependents' readiness without a page interaction. Deleting an item deletes its incident edges. (MVP)
- **FR-DEP-5** **Epics are not ranked**: an item with at least one open sub-item is an epic. It is excluded from ranking (FR-PRI-11); its edges gate the epic, not its sub-items, which are ranked individually and inherit an absent priority label from it (FR-PRI-1). (MVP)
- **FR-DEP-6** **Edits invalidate the queue input**: any dependency edit changes the readiness and unblocking inputs, so it invalidates the cached ranking fingerprint (FR-PRI-8) and the affected item is re-ranked on the next run (or falls back to base-score order). (MVP)

### 6.15 Failure backoff and re-queue (RET)

Three levels handle failure and they never overlap. **Node retry** operates *within* one run: a node execution fails and `max_attempts` + `on_error` decide whether it is attempted again in that run (FR-WF-16, `transient` only). **Item backoff** operates *across* runs: the item's run failed and the item must wait before it may be dispatched again (this section). **Ranking** is allocation among what is currently eligible: which item gets the next worker (FR-PRI-15). Backoff is *eligibility*; ranking is *order*. A run that fails after its node retries are spent starts the item's backoff clock; backoff never reorders the queue, and the ranker never resets a counter.

- **FR-RET-1** **Failure counter, persisted across runs**: the unit is `(item, workflow)` — the pair NFR-9 caps — so a second template never inherits another's failures and a broken item cannot loop forever. The count increments **once per failed run**, not per node attempt (node attempts are FR-WF-16's business), and lives in Postgres (`item_failures`, mvp-design.md §3), not in scheduler memory, so it survives restarts, redeploys and dispatch-cycle boundaries. It resets to zero only on a successful run, on a human `retry now`, or on closing the item. (MVP)
- **FR-RET-2** **Parametrized curve, two named forms**: let `n` be the consecutive-failure count, `X` the base delay, `max_backoff` the ceiling. Two curves, each named in config:
  - **exponential** (default) — `delay = min(X * Y^(n-1), max_backoff)`, `Y > 1`;
  - **linear** — `delay = min(X + (n-1) * step, max_backoff)`.

  One failure therefore waits `X` seconds under both forms. A failure computes `next_eligible_at = last_failed_at + delay`. Defaults: `X = 60s`, `Y = 2`, `step = 300s`, `max_backoff = 6h`, `max_failures = 5`. Tradeoff: linear is predictable and recovers fastest for a long tail of unrelated one-off failures; exponential protects a shared dependency (CI runner, staging box, provider API) from a retry storm, which is the failure mode that actually hurts. **Recommendation: exponential.** (MVP)
- **FR-RET-3** **Parameters are a versioned set**: curve + `X`/`Y`/`step`/`max_backoff`/`max_failures` form one named, versioned set (`backoff_version`, e.g. `bo-exp-1`), defaulted per tenant and overridable per product, versioned the same way as `weights_version` (FR-PRI-3). The version and the parameters in force are snapshotted onto the failure row, so editing config never retimes a wait already in flight — new parameters apply to new failures only. (MVP)
- **FR-RET-4** **Eligibility gate, applied after ranking**: while `next_eligible_at > now` the item is **not claimable**: dispatch skips it (FR-PRI-15), a trigger that matches it does not start it (FR-WF-10), and no worker lease may be issued for it (FR-WF-15). It keeps its rank and its label and is displayed as `backing_off` with the remaining time. Being rank #1 does not exempt an item from its wait — the next eligible item is dispatched instead. The reverse also holds: a ranked item is not dispatched *because* it is ranked; eligibility decides, order only selects among the eligible. (MVP)
- **FR-RET-5** **Re-entry is re-scored, not remembered**: when the backoff expires the item returns to the competing set and is scored from **current** inputs, exactly like any other candidate — no penalty applied, no previous position restored, no bonus for having failed. Its inputs may legitimately have changed while it waited (more age points, a dependency resolved or newly opened, a new dependent it now unblocks, an open PR), and the new score reflects that state. It stays in the ranking's input set, so it re-enters at whatever rank its current score earns; only dispatch is gated (FR-RET-4). (MVP)
- **FR-RET-6** **Ceiling and terminal state**: `max_backoff` bounds a single wait and `max_failures` bounds the count. A failure that would take `n` past `max_failures` moves the item to **`exhausted`** instead of scheduling another wait: it leaves the dispatchable set, is never retried automatically, and stays terminal until a **human** acts — `retry now` (clears the counter and re-arms at `n = 0`) or closing/cancelling the item. `exhausted` is not `failed`: the run failed, the item is quarantined for a decision. This is NFR-9's "failure cap per (item, workflow)" made explicit — no item retries forever. (MVP)
- **FR-RET-7** **A shared failure backs off as one, not as N**: several items failing on one broken dependency (dead CI runner, staging box gone, provider outage) must not produce N independent retry trains that come due together. Two rules. (a) Dependents of an open `blocked_by` edge are not ready and are never dispatched (FR-DEP-5, FR-PRI-11); a failing blocker is still open, so its dependents simply wait. (b) The root's backoff **propagates**: a dependent's earliest dispatch is `max(own next_eligible_at, every blocking root's next_eligible_at)`, so nothing re-enters while the thing it needs is still backing off. A root that is `exhausted` holds its dependents until a human acts. The failing root is surfaced as a stalled blocking root and as pipeline health in the ranking (FR-PRI-5, FR-PRI-14) — it is one problem to fix, and the queue says so. (MVP)
- **FR-RET-8** **Every failure and retry is auditable**: each failure records failure count, `failure_kind`, curve + `backoff_version`, the computed delay, `next_eligible_at`, and the run that failed; each expiry/re-entry, human reset and exhaustion is an `activity_log` entry with actor and timestamp. A wait the UI shows must be reconstructible from stored rows — a scheduler-held timer is not an explanation (NFR-2). (MVP)

### 6.16 Manual queue control (QUE)

The ranker orders the queue (FR-PRI-5); the human owns it. Three explicit actions override that order — **pause**, **move to top (pin)** and **start now** — and none of them is a scoring input: no override writes a score, a rank row or an item field the ranker reads, and no ranking pass may "correct" one (FR-PRI-2). Precedence, applied **inside** the eligible set: `pinned > manual priority label > ranker score > deterministic base score`. Eligibility is decided before order and is never overturned by an override: a paused, backing-off, `exhausted`, blocked, epic or already-running item does not become dispatchable because it was pinned (FR-RET-4, FR-RET-6, FR-DEP-5, FR-PRI-11). A manual label is authoritative *through* the score and the ranker's criteria (FR-PRI-1, FR-PRI-3, FR-PRI-5); a pin is the only direct override of the emitted order.

- **FR-QUE-1** **Three actions, one precedence**: (a) **pause/unpause** — remove an item from eligibility without failing it (FR-QUE-2..3); (b) **move to top/unpin** — place an item at rank 1 above every scored item until unpinned (FR-QUE-4); (c) **start now** — dispatch a specific item ahead of rank order on the next available worker (FR-QUE-5). Each action is a queue operation, never a score edit; each is recorded with actor and timestamp (FR-QUE-7). Precedence is `pinned > manual priority label > ranker score`, with eligibility gates (paused, `backing_off`, `exhausted`, blocked, epic, active run) applied *before* any ordering (FR-QUE-6). (MVP)
- **FR-QUE-2** **Pause is not a failure and never starts a backoff** *(the rule that makes pause safe)*: pausing removes the item from dispatch **without it having failed**. It does **not** increment `item_failures.consecutive_failures`, does **not** set or move `next_eligible_at`, does **not** change `item_failures.state`, and does **not** count toward `max_failures` or `exhausted` (FR-RET-1, FR-RET-6). Only a run that actually fails touches those rows. Consequence, stated because it is the point: a task a human pauses ten times still has the failure count it had before, and can never be quarantined by being paused. Pause is also not a stop: an item with an in-flight run keeps that run (use FR-MON-5 to end it); pause gates future dispatch only. (MVP)
- **FR-QUE-3** **Paused means not ready, not eligible — durably**: while a pause is in force the item is excluded from dispatch (FR-PRI-15), from auto-start triggers (FR-WF-10) and from any worker lease (FR-WF-15) — the same *effect* as `backing_off` (FR-RET-4) but a different mechanism and different state: a backoff expires by itself, a pause never does. The pause is persisted (`items.paused_at`, `paused_by`, optional `pause_reason`) and survives restart, redeploy and every dispatch cycle; it is visible in the UI with actor and reason. It ends only on an explicit human unpause — never on a timer, a ranking run, a workflow or a scheduler restart. (MVP)
- **FR-QUE-4** **Move to top (pin)**: pins an item to rank 1, above every item the ranker scored, so the emitted ranking is read as a pinned block followed by the scored order. The ranker keeps scoring the full ready set, including the pinned item — the pin changes position, never score, labels or `priority_ranking_entries` rows, and a ranking pass that "demotes" a pin is ignored rather than obeyed. Multiple pins keep an explicit, stable order: **most recently pinned first** (`pinned_at` desc, then item id), so pinning a second item puts it at rank 1 and pushes the previous pin to rank 2. An unpinned item returns to whatever position its current score earns on the next run — no previous position is remembered. A pin is cleared by unpin, or automatically when the item reaches `done`/`cancelled`. (MVP)
- **FR-QUE-5** **Start now**: bypasses the queue and the ranking and dispatches the item's workflow ahead of rank order on the next available worker. It jumps the *queue*, not the *limits*: it still obeys `max_concurrency` per run (FR-WF-13), per-agent `concurrency_limit` (§6.6) and the global worker pool, the lease/claim rules (FR-WF-15), readiness (a blocked item or an epic cannot be started, FR-DEP-5, FR-PRI-11), and `exhausted`/circuit-breaker state (FR-RET-6 — an exhausted item requires `retry now` or closure, not start-now). With no free worker, the item becomes the **first** item dispatched when one frees, never an over-subscription. Start-now on a `backing_off` item is an explicit human choice to skip *that wait*; it is logged as an override and does not reset the failure count (only a successful run or `retry now` does, FR-RET-1). (MVP)
- **FR-QUE-6** **Conflicts resolve toward eligibility**: pause and backoff and the circuit breaker sit *above* ordering, so no combination of overrides defeats them. A **pinned `backing_off`** item stays backing-off and shows its countdown; the next eligible item is dispatched instead — pinning does not defeat a backoff timer (FR-RET-4). A **pinned `exhausted`** item is still not dispatchable (FR-RET-6). A **pinned paused** item is still paused: pause outranks pin. A **pinned blocked or epic** item is still not ready (FR-DEP-5). Unpausing restores eligibility, not position: the item is re-scored from current inputs like any re-entering candidate (FR-RET-5). (MVP)
- **FR-QUE-7** **Every override is attributed and visible**: pause, unpause, pin, unpin and start-now are `activity_log` entries with actor, timestamp, before/after and the optional reason (NFR-2), and each records who overrode what — an override with no actor is a defect, not a default. The queue and board show the override state next to the rank: 📌 pinned (with the pin order and actor), ⏸ paused (actor, reason, since), `backing_off` with its countdown, `exhausted` quarantined. "Why is this first?" must answer with the override (who, when) or the ranking reason (FR-PRI-7) — never a silent reordering. Ranking runs and dispatch logs keep their own history, so an override is reconstructible from stored rows without replaying a scheduler's memory. (MVP)

### 6.17 Agent context composition (CTX)

Every node execution hands the agent **one** assembled artifact — the **context bundle** — built from four attachments in a fixed order. The order is a security boundary, not a formatting choice: trusted material is composed first, untrusted material is appended last, and nothing external can be mistaken for the closing instruction.

- **FR-CTX-1** **Four attachments, fixed order**: (1) **soul file** — `agents.soul_md` (FR-AG-8), always present, the outermost persona layer; (2) **skills** — zero or more selected `skill_versions` (FR-AG-11..13), not all of them; (3) **memory** — the four scopes of FR-MEM-8, injected by tier (FR-MEM-9); (4) **task file** — the explicit artifact for this node execution (FR-CTX-2), whose **untrusted section is always last**. The adapter receives the composed bundle, never a bag of fragments assembled at each call site. (MVP)
- **FR-CTX-2** **Task file contents**: the task statement (the node's instruction, rendered from the template) plus the context envelope of FR-WF-4 — item, ancestor chain, inbound port values, artifacts, injected memory refs, chat refs — with every section carrying its trust level (FR-CTX-3). It is persisted per step execution (hash + manifest, FR-CTX-6) so the exact input to a run is reproducible from rows. (MVP)
- **FR-CTX-3** **Trust levels, declared per section**: `trusted` = operator/author-authored: the task instruction, the agent's soul, validated agent output, project conventions, approved skill text. `untrusted` = external: GitHub issue bodies and comments, Jira ticket text, PR descriptions and comments, web/scraped content, and repository file contents when the repo is not the operator's own. Untrusted sections are (a) **fenced** with explicit delimiters the content cannot forge; (b) **labelled as data with provenance** — source system, canonical ref/url, author, timestamp; (c) **passed as content, never instruction**; and (d) **sanitized** on ingest — ANSI/OSC control sequences stripped (OSC 52 clipboard writes and OSC 8 hyperlinks are a real vector), NUL bytes and bidi-override characters removed, instruction-shaped markdown/HTML neutralised. The bundle opens with a preamble stating that untrusted sections are data to reason about, never instructions to follow. Threat model: mvp-design.md §10, not restated here. (MVP)
- **FR-CTX-4** **Untrusted content cannot grant a capability**: no text inside an untrusted section may load a skill, widen a scope, change a trust label or make a marker act. Markers parsed out of **untrusted** content — `AIC_PUBLISH`, `AIC_MEMORIZE`, `AIC_OUTPUT`, `AIC_SPAWN` — are ignored and logged. Skills load only from trusted selectors (FR-AG-13); memory is written only through FR-MEM-5's explicit paths. This is the same trust-levelling discipline the ranker/scorer already applies (FR-PRI-9): one trust model for every component that reads external text, not two. (MVP)
- **FR-CTX-5** **One budget, defined trim precedence**: the bundle is capped by a single total token budget. It introduces no second budget system — memory selection, summarization, per-scope caps and watermarks are FR-MEM-6's, and the manifest is the same one. When over budget, trimming proceeds in this order: **skills → memory (widest tier first: workspace, agent, item, run) → untrusted task-file sections → trusted task statement**. The **soul is never trimmed**. Within a tier, entries summarize oldest-first per FR-MEM-6, never mid-entry, and every trim is recorded. (MVP)
- **FR-CTX-6** **The bundle is auditable**: each step execution records what was actually attached — soul revision, each skill id/version, each memory entry (scope, key, revision) and the task file's section list with trust levels and token counts — by **extending the existing injection manifest** (`step_runs.memory_manifest_json`, FR-MEM-6), not by adding a parallel one. Any run can therefore be audited for exactly the context the agent saw, including which sections were trimmed. (MVP)

## 7. Non-functional requirements

- **NFR-1 Local-first execution**: the control plane is self-hostable and the MVP deployment runs on the owner's machine (tenant #1); agent execution stays local/hybrid. Works offline for browsing.
- **NFR-2 Auditability**: every state change and agent action is in the activity log with actor and timestamp; run logs retained per run.
- **NFR-3 Idempotency & safety**: workflow nodes are retryable; integration nodes check before acting (no duplicate branches/PRs); agent filesystem access confined to managed worktrees. **Concurrent branches never share a mutable workspace** — each code-touching node execution gets its own worktree, and only a `merge` node may combine branch outputs.
- **NFR-4 Secrets**: tokens stored in OS keychain or encrypted local store; never written to logs or agent env beyond what's required.
- **NFR-5 Extensibility**: connectors (AI, GitHub, Jira) behind Go interfaces; adding a provider = new adapter, no core changes.
- **NFR-6 Observability**: structured logs, per-run event stream, token/cost accounting.
- **NFR-7 Performance**: UI feels instant on a tree of ~10k items; board renders < 100 ms interactions locally.
- **NFR-8 Multi-tenancy**: tenant isolation is a top-level, non-negotiable requirement (see FR-TN-*). Tenants are multi-user from M0 — membership and role checks are enforced at the repository/service layer, not just the UI; don't build a generalized per-resource permission matrix yet, but every table and every query is tenant-scoped from day one.
- **NFR-9 Resilience safeguards** `[apiary]`: re-dispatch failure cap per (item, workflow) so a broken item can't loop forever; **every cycle declares `max_iterations`** (unbounded cycles are rejected at validation); node executions run under a **lease with heartbeat, reclaimed on expiry** by another worker with a logged event; non-blocking dispatch (a busy agent never stalls others); separate total vs stall timeouts (kill silent runs, not slow ones); orphan reconciliation on startup.
- **NFR-10 Prompt-injection guard** `[apiary]`: content imported from external systems (GitHub/Jira issues) is untrusted; items not created by the owner require explicit triage before agents act on them.

## 8. Out of scope (MVP)

- Fine-grained RBAC beyond the built-in roles, SSO/SAML, billing, hosted multi-user SaaS (teams themselves are in scope; see §6.10).
- Hosted SaaS deployment (MVP is self-hosted; the model must not preclude SaaS later).
- Cron/scheduled runs beyond the hourly dispatch cycle (FR-WF-10); remote workers/clusters.
- Post-MVP `[apiary]` borrowings: agent-spawned items (FR-WF-11), structured checkpoint fields (FR-CP-7), fallback chains (FR-AG-9), automatic memory promotion across tiers.
- Mobile app; email digests.
- Deploy/CI orchestration (beyond reading PR check status).
- Marketplace of templates/agents.

## 9. Success metrics (dogfooding)

- ≥ 70% of FazGiro/Vendamatica/ProntaLoja bugfixes executed as workflow runs within the first month.
- Median human touch time per bugfix item < 15 min (two checkpoints).
- Every merged PR traceable to an item and a run (100% artifact linkage).
- Zero out-of-band agent actions: everything visible in the activity log.
