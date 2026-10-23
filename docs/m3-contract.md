# M3 API contract — templates, runs, checkpoints, interventions

Working note for the M3 lanes. The source of truth is `docs/mvp-design.md` §4
and §5; this file pins the shapes the backend and the web client agree on while
M3 is in flight, the way `docs/m2-contract.md` did for M2.

All endpoints require a session cookie and are tenant-scoped. Reads are open to
every role (guests included). Writing follows the role matrix below. The
house error envelope is `{"error": {"code": "…", "message": "…"}}`.

## Roles

| Action | owner | admin | member | guest |
| --- | --- | --- | --- | --- |
| Read templates, runs, checkpoints, memory, notifications | yes | yes | yes | yes |
| Create/edit/publish/delete a template | yes | yes | no | no |
| Start a run, decide a checkpoint, intervene, write memory | yes | yes | yes | no |

Runs belong to the tenant, not the user: `run.started_by` records who started
it, and any member can decide a checkpoint on any run of the tenant.

## Templates

```
GET    /api/workflow-templates                     -> {templates: TemplateSummary[]}
POST   /api/workflow-templates                     -> 201 {template: Template}
GET    /api/workflow-templates/{id}                -> {template: Template}
PATCH  /api/workflow-templates/{id}                -> {template: Template}
DELETE /api/workflow-templates/{id}                -> 204
POST   /api/workflow-templates/{id}/publish        -> {template: Template}
POST   /api/workflow-templates/{id}/versions       -> 201 {template: Template}
```

A template is a draft until it is published. `PATCH` may only touch a draft:
editing a published row is `409 template_published`, because a published
`(name, version)` is immutable — a change creates a new version row instead.
`POST /versions` clones the current graph into the next version.

```json
{
  "name": "Bugfix",
  "applies_to": "bug",
  "max_concurrency": 4,
  "graph": {
    "schema_version": 1,
    "nodes": [
      {"id": "n_pm", "kind": "agent", "name": "spec", "type": "agent.pm", "type_version": 2,
       "config": {"agent_id": "…", "outputs": {"spec_md": "string"}},
       "position": {"x": 0, "y": 0}, "on_error": "stop_workflow",
       "always_output_data": false, "workspace": "scratch"}
    ],
    "edges": [{"id": "e1", "from": {"node": "n_pm", "port": "main"}, "to": {"node": "n_gate", "port": "in"}}]
  }
}
```

The graph is validated on write and again on run start; a rejected graph is
`422 template_invalid` with a `code` naming the rule
(`duplicate_node_id`, `unknown_node`, `invalid_port`, `no_entry_node`,
`unreachable_node`, `cycle_without_budget`, `merge_budget`,
`invalid_output_type`, `invalid_config`, `invalid_schema_version`).

## Runs

```
POST   /api/items/{id}/runs  {"template_id": "…", "template_version": 3} -> 202 {run: Run}
GET    /api/runs?item_id=&state=&limit=50          -> {runs: Run[]}
GET    /api/runs/{id}                              -> {run: Run, steps: StepRun[]}
POST   /api/runs/{id}/cancel                       -> 202 {run: Run}
GET    /api/runs/{id}/events?after_seq=0           -> text/event-stream
GET    /api/runs/{id}/logs?after_seq=0&limit=500   -> {logs: LogEntry[]}
```

Starting a run snapshots the published graph into
`run.definition_snapshot_json`; the run never reads the template again.
`template_version` is optional — the latest published version is used.

Run states: `queued → running → waiting_human → completed|failed|cancelled`.
`waiting_human` means nothing can progress except a pending checkpoint; a run
with one parked branch and another running is still `running`.

Step states: `pending|ready|running|waiting|succeeded|failed|skipped|blocked|
cancelled|superseded`.

```json
{
  "id": "…", "item_id": "…", "template_id": "…", "template_version": 3,
  "state": "running", "max_concurrency": 4,
  "started_by": "…", "started_at": "…", "finished_at": null,
  "error": null, "failure_kind": null, "branch": "aic/<item>-<run>",
  "worktree_path": null
}
```

A step carries the receipts of its incoming edges, the result contract and the
manifests:

```json
{
  "id": "…", "run_id": "…", "node_id": "n_code", "iteration_path": "", "run_index": 0,
  "kind": "agent", "name": "implement", "state": "succeeded",
  "input_edges": {"e3": {"state": "delivered", "port": "main", "value": null}},
  "output_ports": {"main": {"value": {"pr_url": "…"}, "summary": "opened PR"}},
  "result": {"status": "succeeded", "exit_reason": "ok", "summary": "…",
             "tokens": {"in": 1200, "out": 480}, "cost_usd": 0.02,
             "failure_kind": null, "artifacts": []},
  "failure_kind": null, "workspace_kind": "worktree", "workdir": "…",
  "agent_job_id": "…", "memory_manifest": [], "context_manifest": [],
  "claim_expires_at": null, "started_at": "…", "finished_at": "…"
}
```

Receipt states are `delivered|skipped|dead` — never silence. A `dead` receipt
on an incoming edge blocks the downstream node instead of retrying it.

Run SSE frames:

```
event: step
data: {"step":{…}}

event: log
data: {"seq":12,"job_id":"…","run_id":"…","step_run_id":"…","stream":"stdout","level":"info","kind":"output_chunk","message":"…","ts":"…"}

event: checkpoint
data: {"checkpoint":{…}}

event: run
data: {"run":{…}}

event: done
data: {}
```

The server replays rows with `seq > after_seq`, then tails. Clients fall back
to polling `/logs` if the `EventSource` errors.

## Checkpoints

```
GET    /api/checkpoints?state=pending|decided&limit=50 -> {checkpoints: Checkpoint[]}
GET    /api/checkpoints/{id}                     -> {checkpoint: Checkpoint}
POST   /api/checkpoints/{id}/decision            -> 200 {checkpoint: Checkpoint}
POST   /api/checkpoints/{id}/remind              -> {checkpoint: Checkpoint}
```

`state` is `pending` (the inbox), `decided` (every answered lap, for the audit
trail) or absent (everything).

```json
{
  "id": "…", "run_id": "…", "step_run_id": "…", "node_id": "n_gate", "lap": 1,
  "state": "pending", "summary_md": "…", "decision_md": null, "fields": null,
  "fields_spec": [{"key": "target", "type": "string", "label": "Target branch"}],
  "decided_by_kind": null, "decided_by": null, "decided_at": null,
  "remind_after": "…"
}
```

The decision body:

```json
{"decision": "approve", "decision_md": "", "fields": {"target": "main"}}
```

`decision` is one of `approve | approve_with_edits | reject | abort`.
The decision is a conditional update on `(id, lap, state = 'pending')`: the
first writer wins and a later decision is `409 already_decided` with the
winning decision attached. `approve_with_edits` requires `decision_md`;
`reject` requires `decision_md` (the feedback goes back to the agent);
`abort` fails the run. Structured `fields` are validated against
`fields_spec` and land in run memory. Every decision writes an activity-log
entry and a system message into the run thread.

`fields_spec` is the typed form the checkpoint asks for — the node's declared
fields, copied into the checkpoint row when it is created:

```json
[{"key": "target", "type": "string", "label": "Target branch", "required": true},
 {"key": "release", "type": "enum", "label": "Release", "options": ["canary", "stable"]}]
```

`type` is `string | text | boolean | number | enum`.

A rejected checkpoint follows its node's policy: `retry_step` re-runs the same
node (`run_index+1`, the old attempt marked `superseded`, capped by
`max_cycles`), `goto_step` jumps to the configured target node, `abort` fails
the run. Every re-entry opens a new `lap`; earlier laps stay in the audit
trail.

## Interventions

```
POST   /api/runs/{id}/intervene -> 202 {intervention: Intervention}
GET    /api/runs/{id}/interventions -> {interventions: Intervention[]}
```

```json
{"kind": "inject", "node_id": "n_code", "step_run_id": "…", "body_md": "use the new API"}
```

`kind` is `inject | redo | stop`. `inject` steers the running execution
(delivered to the adapter when it supports live input, otherwise stop-and-
resume with the instruction prepended to the context). `redo` supersedes the
node execution and every descendant and re-creates them as `pending`;
siblings outside the descendant set keep their results. `stop` cancels the
execution (or the whole run when `step_run_id` is omitted). An intervention
that arrives after the execution is terminal is recorded and never resurrects
it.

Interventions are ordered per step execution by an atomically allocated `seq`;
only the lease holder consumes them, and `delivered_at` is CAS-written.

## Memory

```
GET    /api/memory?scope_kind=&scope_ref=&limit=100 -> {entries: MemoryEntry[]}
GET    /api/memory/manifest?step_run_id=…           -> {manifest: MemoryManifestEntry[]}
POST   /api/memory                                   -> 201 {entry: MemoryEntry}
```

`scope_kind` is `run | item | agent | workspace`; `scope_ref` is the run, item,
agent or product id. Memory is append-only: a write with an existing
`(scope_kind, scope_ref, key)` creates revision *n+1* and supersedes the prior
row; the prior row stays readable for executions that captured an earlier
watermark. The response carries `revision`, `seq` and `superseded_by`.

Run memory is engine-only: an agent cannot write it, and the API refuses
`scope_kind=run` writes with `403 run_memory_readonly`.

## Notifications

```
GET    /api/notifications?unread=true&limit=50 -> {notifications: Notification[]}
POST   /api/notifications/{id}/read           -> {notification: Notification}
```

Kinds: `checkpoint_pending | run_failed | run_completed | sync_error`. A
notification is visible only to the user it names. The nav badge is the count
of unread rows.

## Errors

New codes: `template_invalid`, `template_published`, `template_not_found`,
`run_not_found`, `run_terminal`, `step_not_found`, `checkpoint_not_found`,
`already_decided`, `decision_invalid`, `intervention_invalid`,
`node_not_found`, `memory_scope_invalid`, `run_memory_readonly`,
`notification_not_found`, `max_concurrency_exceeded`.
