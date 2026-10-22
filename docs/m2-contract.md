# M2 API contract — agents, chat, jobs

Internal working note for the M2 lanes. The source of truth is
`docs/mvp-design.md` §6; this file pins the shapes the backend and the web
client agree on while M2 is in flight.

All endpoints require a session cookie and are tenant-scoped. Reads are open to
every role (guests included); agent CRUD requires `owner`/`admin`; posting
messages and running agents requires `member` or better.

## Agents

```
GET    /api/agents                 -> {agents: Agent[]}
POST   /api/agents                 -> 201 {agent: Agent}        owner/admin
GET    /api/agents/{id}            -> {agent: Agent}
PATCH  /api/agents/{id}            -> {agent: Agent}            owner/admin
DELETE /api/agents/{id}            -> 204                        owner/admin, 409 if it has jobs
```

```json
{
  "name": "dev",
  "role": "dev",
  "kind": "cli",
  "config": {"bin": "claude", "args": ["-p", "--output-format", "stream-json", "--verbose"], "output_format": "stream-json", "timeout_sec": 300},
  "capabilities": {"code": true, "shell": true, "streaming": true, "usage": true},
  "concurrency_limit": 2,
  "enabled": true,
  "soul_md": "You are the implementation agent…",
  "skills_policy": {"auto": true, "max_attach": 3},
  "fallbacks": []
}
```

`PATCH` uses the house convention: absent field = unchanged, `""` clears a
nullable string. `config` and `capabilities` are opaque JSON objects validated
by the server per `kind`. Secrets never live here: `config.key_env` names an
environment variable.

## Threads and messages

```
POST   /api/items/{id}/thread      -> 200 {thread: Thread}   get-or-create the item's thread
GET    /api/threads/{id}           -> {thread: Thread}
GET    /api/threads/{id}/messages?after_seq=0&limit=100
                                   -> {messages: Message[]}
POST   /api/threads/{id}/messages  {"body_md": "…"} -> 201 {message: Message, jobs: Job[]}
GET    /api/threads/{id}/jobs?limit=20 -> {jobs: Job[]}
```

`POST /messages` parses `@name` mentions. Every distinct, enabled, mentioned
agent gets a queued job; an unknown or disabled mention fails the request with
`422 unknown_agent` and nothing is written. The created human message and the
jobs are returned together.

`Message.author_kind` is `human|agent|system`. Agent replies are inserted by the
worker when a job finishes.

## Jobs

```
POST   /api/threads/{id}/jobs      {"agent_id": "…", "instructions": "…"} -> 202 {job: Job, message: Message|null}
GET    /api/jobs/{id}              -> {job: Job}
GET    /api/jobs/{id}/logs?after_seq=0&limit=500 -> {logs: LogEntry[], job: Job}
GET    /api/jobs/{id}/events?after_seq=0         -> text/event-stream
POST   /api/jobs/{id}/cancel       -> 202 {job: Job}
```

`POST /threads/{id}/jobs` is the manual "run agent" button: it optionally
records `instructions` as a human message, then queues the job with the same
context assembly a mention uses.

Job states: `queued → running → succeeded|failed|cancelled`. A tenant may have
at most 100 queued jobs; beyond that `422 queue_full`.

SSE frames:

```
event: log
data: {"seq":12,"job_id":"…","stream":"stdout","level":"info","kind":"output_chunk","message":"…","ts":"…"}

event: state
data: {"job":{…}}

event: done
data: {}
```

The server replays rows with `seq > after_seq`, then tails; `state` is sent on
every transition and `done` closes the stream. Clients should fall back to
polling `/logs` if `EventSource` errors.

## Errors

The usual envelope, with these codes added: `unknown_agent`, `agent_disabled`,
`queue_full`, `job_terminal`, `agent_in_use`, `invalid_config`, `invalid_soul`.
