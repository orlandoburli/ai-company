"use client";

import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import Link from "next/link";
import {
  api,
  isTerminalJobState,
  type Agent,
  type Job,
  type LogEntry,
  type Message,
} from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { useJobStreams, type JobStreamCallbacks } from "@/lib/use-job-streams";
import { firstName, formatDateTime } from "@/lib/format";
import {
  Dialog,
  EmptyState,
  ErrorBlock,
  Skeleton,
  Toasts,
  apiErrorMessage,
} from "@/components/primitives";

/**
 * Item chat: message timeline, @agent mention composer, live streaming agent
 * output, and the manual "run agent" button. Shared by the item detail tab
 * (compact) and the full-page /chat/[threadId] route.
 */

const JOB_HISTORY = 30;
const MAX_LOG_TAIL = 60;

export function ChatPanel(props: {
  threadId: string;
  canWrite: boolean;
  /** Full-page rendering uses viewport height instead of a fixed box. */
  full?: boolean;
  /** Link to the full-page version, shown when embedded in the item tab. */
  fullHref?: string;
}) {
  const { toasts, push } = useToasts();
  const { me } = useSession();
  const threadId = props.threadId;

  const agentsQ = useAsync(() => api.listAgents().then((r) => r.agents), []);
  const membersQ = useAsync(
    () => api.listMembers().then((r) => r.members),
    [],
  );

  const [messages, setMessages] = useState<Message[] | null>(null);
  const [messagesErr, setMessagesErr] = useState<string | null>(null);
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [jobsErr, setJobsErr] = useState<string | null>(null);
  const [streams, setStreams] = useState<Record<string, LogEntry[]>>({});
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const [runOpen, setRunOpen] = useState(false);

  const messagesRef = useRef<Message[]>([]);
  useEffect(() => {
    messagesRef.current = messages ?? [];
  }, [messages]);

  const mergeMessages = useCallback((incoming: Message[]) => {
    const byId = new Map(messagesRef.current.map((m) => [m.id, m]));
    for (const m of incoming) byId.set(m.id, m);
    const merged = [...byId.values()].sort((a, b) => a.seq - b.seq);
    messagesRef.current = merged;
    setMessages(merged);
  }, []);

  const fetchNewMessages = useCallback(async () => {
    const after = messagesRef.current.reduce((m, x) => Math.max(m, x.seq), 0);
    const r = await api.threadMessages(threadId, after, 100);
    if (r.messages.length > 0) mergeMessages(r.messages);
    return r.messages.length > 0;
  }, [threadId, mergeMessages]);

  const refreshAfterJobDone = useCallback(async () => {
    // The worker inserts the agent reply right after the job finishes; it can
    // lag a moment behind the state transition, so try once more after a delay.
    try {
      const got = await fetchNewMessages();
      if (got) return;
    } catch {
      return;
    }
    window.setTimeout(() => {
      void fetchNewMessages();
    }, 2500);
  }, [fetchNewMessages]);

  const load = useCallback(async () => {
    setMessagesErr(null);
    setJobsErr(null);
    let msgOk = false;
    try {
      const r = await api.threadMessages(threadId, 0, 100);
      mergeMessages(r.messages);
      msgOk = true;
    } catch (err) {
      setMessagesErr(err instanceof Error ? err.message : "Load failed.");
    }
    try {
      const r = await api.threadJobs(threadId, JOB_HISTORY);
      setJobs(r.jobs);
    } catch (err) {
      setJobsErr(err instanceof Error ? err.message : "Load failed.");
    }
    if (msgOk) void refreshAfterJobDone();
  }, [threadId, mergeMessages, refreshAfterJobDone]);

  useEffect(() => {
    setMessages(null);
    setJobs(null);
    setStreams({});
    messagesRef.current = [];
    void load();
  }, [load]);

  const streamCb = useMemo<JobStreamCallbacks>(
    () => ({
      onJobUpdated: (job) => {
        let becameTerminal = false;
        setJobs((prev) => {
          const list = prev ? [...prev] : [];
          const idx = list.findIndex((j) => j.id === job.id);
          if (idx === -1) {
            list.push(job);
            becameTerminal = isTerminalJobState(job.state);
          } else {
            becameTerminal =
              !isTerminalJobState(list[idx].state) &&
              isTerminalJobState(job.state);
            list[idx] = job;
          }
          return list.slice(-JOB_HISTORY);
        });
        if (becameTerminal) void refreshAfterJobDone();
      },
      onLogChunk: (jobId, entry) => {
        setStreams((prev) => {
          const cur = prev[jobId] ?? [];
          return { ...prev, [jobId]: [...cur, entry].slice(-MAX_LOG_TAIL) };
        });
      },
      onDone: () => {
        void refreshAfterJobDone();
      },
      onStreamError: (_jobId, message) => push("error", message),
    }),
    [refreshAfterJobDone, push],
  );
  useJobStreams(jobs, streamCb);

  // Keep an "elapsed" clock ticking while anything is queued or running.
  const anyActive = (jobs ?? []).some((j) => !isTerminalJobState(j.state));
  const [now, setNow] = useState<number>(() => Date.now());
  useEffect(() => {
    if (!anyActive) return;
    setNow(Date.now());
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [anyActive]);

  const names = useNames(agentsQ.data, membersQ.data);

  const rows = useMemo(() => {
    const msgRows: Row[] = (messages ?? []).map((m) => ({
      kind: "msg" as const,
      msg: m,
      t: Date.parse(m.created_at) || 0,
    }));
    const jobRows: Row[] = (jobs ?? []).map((j) => ({
      kind: "job" as const,
      job: j,
      t: (Date.parse(j.created_at) || 0) + 0.6,
    }));
    return [...msgRows, ...jobRows].sort((a, b) => a.t - b.t);
  }, [messages, jobs]);

  const cancelJob = useCallback(
    async (job: Job) => {
      setCancelling((c) => new Set(c).add(job.id));
      try {
        const r = await api.cancelJob(job.id);
        streamCb.onJobUpdated(r.job);
        push("info", "Cancel requested for the run.");
      } catch (err) {
        push("error", apiErrorMessage(err));
      } finally {
        setCancelling((c) => {
          const next = new Set(c);
          next.delete(job.id);
          return next;
        });
      }
    },
    [streamCb, push],
  );

  const canWrite = props.canWrite;

  return (
    <div className={`chat ${props.full ? "chat--full" : ""}`}>
      <div className="chat__tools spread">
        <span className="tiny muted">
          {canWrite
            ? "Mention an agent with @name to route work."
            : "Read-only view — guests can read the chat but not write."}
        </span>
        <span className="stack" style={{ gap: 6 }}>
          {props.fullHref ? (
            <Link
              className="btn btn--sm btn--ghost"
              href={props.fullHref}
              aria-label="Open chat as a full page"
            >
              Full page
            </Link>
          ) : null}
          {canWrite ? (
            <button
              type="button"
              className="btn btn--sm"
              onClick={() => setRunOpen(true)}
            >
              Run agent
            </button>
          ) : null}
        </span>
      </div>

      <div className="chat__scroll" role="log" aria-live="polite">
        {messages === null && !messagesErr ? (
          <div style={{ display: "grid", gap: 10, padding: "4px 2px" }}>
            <Skeleton h={14} w="55%" />
            <Skeleton h={14} w="72%" />
            <Skeleton h={14} w="40%" />
          </div>
        ) : messagesErr ? (
          <ErrorBlock
            title="The conversation could not load."
            message={messagesErr}
            retry={() => void load()}
          />
        ) : rows.length === 0 ? (
          <EmptyState
            title="No messages yet"
            body="Write the first message and mention an agent with @name — every enabled, mentioned agent gets its own run. The reply streams back here."
          />
        ) : (
          <ul className="chat__list">
            {rows.map((row) =>
              row.kind === "msg" ? (
                <MessageRow
                  key={row.msg.id}
                  msg={row.msg}
                  youId={me?.user.id}
                  userName={names.userName}
                  agentName={names.agentName}
                />
              ) : (
                <JobRow
                  key={row.job.id}
                  job={row.job}
                  agentName={names.agentName(row.job.agent_id ?? "")}
                  tail={tailText(streams[row.job.id])}
                  now={now}
                  canCancel={canWrite}
                  cancelling={cancelling.has(row.job.id)}
                  onCancel={() => void cancelJob(row.job)}
                />
              ),
            )}
          </ul>
        )}
        {jobsErr ? (
          <ErrorBlock
            title="Run history could not load."
            message={jobsErr}
            retry={() => void load()}
          />
        ) : null}
      </div>

      {canWrite ? (
        <Composer
          threadId={props.threadId}
          enabledAgents={(agentsQ.data ?? []).filter((a) => a.enabled)}
          onMessage={(m) => mergeMessages([m])}
          onJobs={(created) =>
            setJobs((prev) => [...(prev ?? []), ...created].slice(-JOB_HISTORY))
          }
        />
      ) : (
        <p className="tiny muted" style={{ margin: "6px 2px 0" }}>
          Members can post messages and run agents. Resize the panel or open the
          full page to read comfortably.
        </p>
      )}

      {runOpen ? (
        <RunAgentDialog
          threadId={props.threadId}
          enabledAgents={(agentsQ.data ?? []).filter((a) => a.enabled)}
          onClose={() => setRunOpen(false)}
          onStarted={(job, message) => {
            setRunOpen(false);
            if (message) mergeMessages([message]);
            setJobs((prev) => [...(prev ?? []), job].slice(-JOB_HISTORY));
          }}
          push={push}
        />
      ) : null}

      <Toasts toasts={toasts} />
    </div>
  );
}

/* --- timeline rows --------------------------------------------------------- */

type Row =
  | { kind: "msg"; msg: Message; t: number }
  | { kind: "job"; job: Job; t: number };

const MessageRow = memo(function MessageRow(props: {
  msg: Message;
  youId?: string;
  userName: (id: string) => string;
  agentName: (id: string) => string;
}) {
  const m = props.msg;

  if (m.author_kind === "system") {
    return (
      <li className="msg msg--system">
        <span className="msg__glyph" aria-hidden>
          ⌘
        </span>
        <div className="msg__main">
          <div className="msg__head">
            <span className="role role--system">system</span>
            <span className="tiny muted">{formatDateTime(m.created_at)}</span>
          </div>
          <div className="msg__body muted">{renderMarkdown(m.body_md)}</div>
        </div>
      </li>
    );
  }

  if (m.author_kind === "agent") {
    const name = props.agentName(m.author_agent_id ?? "");
    return (
      <li className="msg msg--agent">
        <span className="msg__glyph msg__glyph--agent" aria-hidden>
          ✦
        </span>
        <div className="msg__main">
          <div className="msg__head">
            <span className="role role--agent">
              {name ? `@${name}` : "agent"}
            </span>
            <span className="role role--kind">agent</span>
            <span className="tiny muted">{formatDateTime(m.created_at)}</span>
          </div>
          <div className="msg__body">{renderMarkdown(m.body_md)}</div>
        </div>
      </li>
    );
  }

  const name = props.userName(m.author_user_id ?? "");
  const isYou = props.youId && m.author_user_id === props.youId;
  return (
    <li className="msg msg--human">
      <span className="msg__glyph msg__glyph--human" aria-hidden>
        {firstName(name).slice(0, 1).toUpperCase() || "?"}
      </span>
      <div className="msg__main">
        <div className="msg__head">
          <span className="role role--human">{name || "someone"}</span>
          {isYou ? <span className="tiny muted">(you)</span> : null}
          <span className="role role--kind">member</span>
          <span className="tiny muted">{formatDateTime(m.created_at)}</span>
        </div>
        <div className="msg__body">{renderMarkdown(m.body_md)}</div>
      </div>
    </li>
  );
});

const JobRow = memo(function JobRow(props: {
  job: Job;
  agentName: string;
  tail: string | null;
  now: number;
  canCancel: boolean;
  cancelling: boolean;
  onCancel: () => void;
}) {
  const { job } = props;
  const open = job.state === "queued" || job.state === "running";
  const cls =
    job.state === "succeeded"
      ? "jobchip--ok"
      : job.state === "failed"
        ? "jobchip--fail"
        : job.state === "cancelled"
          ? "jobchip--warn"
          : job.state === "running"
            ? "jobchip--run"
            : "jobchip--queued";

  return (
    <li className="msg msg--job">
      <span className="msg__glyph msg__glyph--agent" aria-hidden>
        ✦
      </span>
      <div className="msg__main">
        <div className="msg__head">
          <span className="role role--agent">
            {props.agentName ? `@${props.agentName}` : "agent"}
          </span>
          <span className={`jobchip ${cls}`}>
            <span className="jobchip__dot" aria-hidden />
            {job.state}
          </span>
          <span className="tiny muted elapsed">{elapsedText(job, props.now)}</span>
        </div>
        {job.error ? (
          <div className="msg__body jobrow__error" role="alert">
            {job.error}
          </div>
        ) : null}
        {job.state === "running" && props.tail !== null ? (
          <div className="msg__body jsontail">
            <span className="mono">{props.tail}</span>
            <span className="caret" aria-hidden />
          </div>
        ) : null}
        {open && props.canCancel ? (
          <div className="jobrow__actions">
            <button
              type="button"
              className="btn btn--sm btn--ghost btn--danger"
              disabled={props.cancelling}
              aria-label={`Cancel the run by @${props.agentName}`}
              onClick={props.onCancel}
            >
              {props.cancelling ? "Cancelling…" : "Cancel"}
            </button>
          </div>
        ) : null}
      </div>
    </li>
  );
});

/* --- composer --------------------------------------------------------------- */

function Composer(props: {
  threadId: string;
  enabledAgents: Agent[];
  onMessage: (m: Message) => void;
  onJobs: (jobs: Job[]) => void;
}) {
  const [draft, setDraft] = useState("");
  const [posting, setPosting] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const [mention, setMention] = useState<{
    start: number;
    query: string;
  } | null>(null);
  const [mentionIdx, setMentionIdx] = useState(0);
  const taRef = useRef<HTMLTextAreaElement>(null);

  const candidates = useMemo(() => {
    if (!mention) return [];
    const q = mention.query.toLowerCase();
    return props.enabledAgents
      .filter((a) => a.name.toLowerCase().startsWith(q))
      .slice(0, 6);
  }, [mention, props.enabledAgents]);

  useEffect(() => {
    setMentionIdx(0);
  }, [mention]);

  function updateMention(value: string, caret: number) {
    const m = /(^|\s)@([A-Za-z0-9_-]*)$/.exec(value.slice(0, caret));
    setMention(m ? { start: m.index + m[1].length + 1, query: m[2] } : null);
  }

  function choose(name: string) {
    const ta = taRef.current;
    if (!ta || !mention) return;
    const caret = ta.selectionStart ?? draft.length;
    const before = draft.slice(0, mention.start) + name + " ";
    const after = draft.slice(caret);
    const next = before + after;
    setDraft(next);
    setMention(null);
    window.requestAnimationFrame(() => {
      ta.focus();
      ta.setSelectionRange(before.length, before.length);
    });
  }

  async function send() {
    const body = draft.trim();
    if (!body || posting) return;
    setPosting(true);
    setSendError(null);
    try {
      const r = await api.postThreadMessage(props.threadId, body);
      setDraft("");
      setMention(null);
      if (r.message) props.onMessage(r.message);
      if (r.jobs.length > 0) props.onJobs(r.jobs);
    } catch (err) {
      // Server envelopes (unknown_agent, agent_disabled, …) surface verbatim.
      setSendError(apiErrorMessage(err));
    } finally {
      setPosting(false);
    }
  }

  function onKeyDown(e: ReactKeyboardEvent<HTMLTextAreaElement>) {
    if (mention && candidates.length > 0) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setMentionIdx((i) => (i + 1) % candidates.length);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setMentionIdx(
          (i) => (i - 1 + candidates.length) % candidates.length,
        );
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        choose(candidates[mentionIdx].name);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setMention(null);
        return;
      }
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  }

  const canSend = draft.trim().length > 0 && !posting;
  const noAgents =
    mention !== null && candidates.length === 0 && props.enabledAgents.length === 0;

  return (
    <div className="composer">
      {sendError ? (
        <div className="error-note" role="alert">
          {sendError}
        </div>
      ) : null}
      <div className="composer__anchor">
        {mention && candidates.length > 0 ? (
          <div
            className="mention"
            role="listbox"
            aria-label="Mention an agent"
          >
            <div className="tiny muted" style={{ padding: "4px 10px 2px" }}>
              Mention an agent
            </div>
            {candidates.map((a, i) => (
              <button
                key={a.id}
                type="button"
                role="option"
                aria-selected={i === mentionIdx}
                className={`menu__item ${i === mentionIdx ? "menu__item--active" : ""}`}
                onMouseDown={(e) => {
                  e.preventDefault();
                  choose(a.name);
                }}
                onMouseEnter={() => setMentionIdx(i)}
              >
                <span className="mono">@{a.name}</span>
                <span className="tiny muted">{a.role}</span>
                <span
                  className="tiny muted"
                  style={{ marginLeft: "auto", flex: "none" }}
                >
                  {a.runners?.[0]?.name ?? "no runner"}
                </span>
              </button>
            ))}
            <div className="tiny muted" style={{ padding: "2px 10px 4px" }}>
              ↑↓ to move · Enter to pick · Esc to dismiss
            </div>
          </div>
        ) : noAgents ? (
          <div className="mention" role="status">
            <span
              className="tiny muted"
              style={{ padding: "6px 10px", display: "block" }}
            >
              No enabled agents yet. Add and enable one in Settings → Agents.
            </span>
          </div>
        ) : null}
        <textarea
          ref={taRef}
          className="input composer__input"
          placeholder="Write to the thread… type @ to mention an agent"
          rows={2}
          value={draft}
          aria-label="Message"
          onChange={(e) => {
            setDraft(e.target.value);
            updateMention(e.target.value, e.target.selectionStart ?? 0);
          }}
          onKeyDown={onKeyDown}
          disabled={posting}
        />
      </div>
      <div className="composer__foot spread">
        <span className="tiny muted">
          Enter to send · Shift+Enter for a new line · @name routes the message
          to an agent
        </span>
        <button
          type="button"
          className="btn btn--primary btn--sm"
          disabled={!canSend}
          onClick={() => void send()}
        >
          {posting ? "Sending…" : "Send"}
        </button>
      </div>
    </div>
  );
}

/* --- run agent ---------------------------------------------------------------- */

function RunAgentDialog(props: {
  threadId: string;
  enabledAgents: Agent[];
  onClose: () => void;
  onStarted: (job: Job, message: Message | null) => void;
  push: (kind: "error" | "info", text: string) => void;
}) {
  const [agentId, setAgentId] = useState(props.enabledAgents[0]?.id ?? "");
  const [instructions, setInstructions] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!agentId || busy) return;
    setBusy(true);
    setError(null);
    try {
      const r = await api.createThreadJob(props.threadId, {
        agent_id: agentId,
        instructions: instructions.trim() ? instructions.trim() : undefined,
      });
      props.onStarted(r.job, r.message);
      props.push("info", "Run queued.");
    } catch (err) {
      // queue_full & friends arrive verbatim from the server envelope.
      setError(apiErrorMessage(err));
      setBusy(false);
    }
  }

  const agent = props.enabledAgents.find((a) => a.id === agentId);

  return (
    <Dialog
      title="Run an agent"
      description="The agent gets the thread context and its soul file. Optional instructions are recorded as your message on the thread."
      onClose={props.onClose}
      width={480}
    >
      {props.enabledAgents.length === 0 ? (
        <>
          <p className="tiny muted" style={{ marginTop: 0 }}>
            No enabled agents yet. Add and enable one in Settings → Agents
            first.
          </p>
          <div className="dialog__row">
            <button type="button" className="btn" onClick={props.onClose}>
              Close
            </button>
          </div>
        </>
      ) : (
        <form onSubmit={submit} style={{ display: "grid", gap: 12 }}>
          {error ? (
            <div className="error-note" role="alert">
              {error}
            </div>
          ) : null}
          <div className="field">
            <label htmlFor="run-agent">Agent</label>
            <select
              id="run-agent"
              className="input"
              value={agentId}
              onChange={(e) => setAgentId(e.target.value)}
            >
              {props.enabledAgents.map((a) => (
                <option key={a.id} value={a.id}>
                  @{a.name} — {a.role} ({a.runners?.map((runner) => runner.name).join(" → ") || "no runner"})
                </option>
              ))}
            </select>
            {agent ? (
              <span className="tiny muted">
                Role {agent.role} · runs at most {agent.concurrency_limit} at a
                time.
              </span>
            ) : null}
          </div>
          <div className="field">
            <label htmlFor="run-instructions">Instructions (optional)</label>
            <textarea
              id="run-instructions"
              className="input"
              rows={4}
              value={instructions}
              onChange={(e) => setInstructions(e.target.value)}
              placeholder="What should the agent focus on this run?"
            />
          </div>
          <div className="dialog__row">
            <button type="button" className="btn" onClick={props.onClose}>
              Cancel
            </button>
            <button type="submit" className="btn btn--primary" disabled={busy}>
              {busy ? "Queuing…" : "Run agent"}
            </button>
          </div>
        </form>
      )}
    </Dialog>
  );
}

/* --- helpers ------------------------------------------------------------------ */

function useNames(
  agents: Agent[] | null,
  members: { user_id: string; name: string }[] | null,
) {
  const userName = useCallback(
    (id: string) => {
      const m = (members ?? []).find((x) => x.user_id === id);
      return m?.name ?? "";
    },
    [members],
  );
  const agentName = useCallback(
    (id: string) => (agents ?? []).find((a) => a.id === id)?.name ?? "",
    [agents],
  );
  return useMemo(() => ({ userName, agentName }), [userName, agentName]);
}

function elapsedText(job: Job, now: number): string {
  const from = job.started_at
    ? Date.parse(job.started_at)
    : Date.parse(job.created_at);
  const to = job.finished_at ? Date.parse(job.finished_at) : now;
  if (Number.isNaN(from) || Number.isNaN(to)) return "";
  const s = Math.max(0, Math.round((to - from) / 1000));
  const mm = Math.floor(s / 60);
  const ss = s % 60;
  if (mm >= 60) {
    const h = Math.floor(mm / 60);
    return `${h}h${mm % 60}m elapsed`;
  }
  return `${mm}:${String(ss).padStart(2, "0")} elapsed`;
}

/** Last few output lines of a running job, for the live-stream bubble. */
function tailText(entries: LogEntry[] | undefined): string | null {
  if (!entries || entries.length === 0) return null;
  const chunks = entries.filter(
    (e) => !e.kind || e.kind === "output_chunk",
  );
  const src = chunks.length > 0 ? chunks : entries;
  const text = src
    .slice(-8)
    .map((e) => e.message)
    .filter((s) => s.length > 0)
    .join("\n");
  if (!text) return null;
  return text.length > 2000 ? "…" + text.slice(-2000) : text;
}

/**
 * Markdown-lite renderer: headings, paragraphs, lists, fenced code blocks,
 * inline code, bold, italic and bare links. Builds React nodes only — raw HTML is never
 * interpreted, so agent output stays safe without a markdown dependency.
 */
export function renderMarkdown(md: string): ReactNode {
  const lines = md.replace(/\r\n/g, "\n").split("\n");
  const out: ReactNode[] = [];
  let para: string[] = [];
  let code: string[] | null = null;
  let list: { ordered: boolean; items: string[] } | null = null;

  const flushPara = () => {
    if (para.length === 0) return;
    out.push(
      <p className="msg__p" key={`p${out.length}`}>
        {inline(para.join("\n"))}
      </p>,
    );
    para = [];
  };
  const flushList = () => {
    if (!list) return;
    const Tag = list.ordered ? "ol" : "ul";
    out.push(
      <Tag className="msg__list" key={`l${out.length}`}>
        {list.items.map((item, index) => <li key={index}>{inline(item)}</li>)}
      </Tag>,
    );
    list = null;
  };

  for (const line of lines) {
    if (line.trimStart().startsWith("```")) {
      if (code === null) {
        flushPara();
        flushList();
        code = [];
      } else {
        out.push(
          <pre className="msg-code mono" key={`c${out.length}`}>
            {code.join("\n")}
          </pre>,
        );
        code = null;
      }
      continue;
    }
    if (code !== null) {
      code.push(line);
      continue;
    }
    if (line.trim() === "") {
      flushPara();
      flushList();
      continue;
    }
    const heading = /^(#{1,6})\s+(.+?)\s*#*\s*$/.exec(line.trim());
    if (heading) {
      flushPara();
      flushList();
      const level = heading[1].length;
      const Tag = `h${level}` as "h1" | "h2" | "h3" | "h4" | "h5" | "h6";
      out.push(<Tag className="msg__heading" key={`h${out.length}`}>{inline(heading[2])}</Tag>);
      continue;
    }
    const listItem = /^\s*(?:([-*+])|(\d+)\.)\s+(.+)$/.exec(line);
    if (listItem) {
      flushPara();
      const ordered = Boolean(listItem[2]);
      if (list && list.ordered !== ordered) flushList();
      if (!list) list = { ordered, items: [] };
      list.items.push(listItem[3]);
      continue;
    }
    flushList();
    para.push(line);
  }
  flushPara();
  flushList();
  if (code !== null && code.length > 0) {
    out.push(
      <pre className="msg-code mono" key={`c${out.length}`}>
        {code.join("\n")}
      </pre>,
    );
  }
  return out.length === 0 ? null : out;
}

const INLINE =
  /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(\*[^*\n]+\*)|(https?:\/\/[^\s<>"')\]]+)/g;

function inline(text: string): ReactNode[] {
  const nodes: ReactNode[] = [];
  let last = 0;
  let key = 0;
  let m: RegExpExecArray | null;
  INLINE.lastIndex = 0;
  while ((m = INLINE.exec(text))) {
    if (m.index > last) nodes.push(text.slice(last, m.index));
    const t = m[0];
    if (t.startsWith("`")) {
      nodes.push(<code key={key++}>{t.slice(1, -1)}</code>);
    } else if (t.startsWith("**")) {
      nodes.push(<strong key={key++}>{t.slice(2, -2)}</strong>);
    } else if (t.startsWith("*")) {
      nodes.push(<em key={key++}>{t.slice(1, -1)}</em>);
    } else {
      nodes.push(
        <a key={key++} href={t} target="_blank" rel="noreferrer noopener">
          {t}
        </a>,
      );
    }
    last = m.index + t.length;
  }
  if (last < text.length) nodes.push(text.slice(last));
  return nodes;
}
