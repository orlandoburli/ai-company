"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, type ApiError } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import type { QueueItem, RankingRun } from "@/lib/types";
import { ErrorBlock, Skeleton, Toasts } from "@/components/primitives";

export default function QueuePage() {
  return (
    <Suspense fallback={<div style={{ display: "grid", gap: 12 }}><Skeleton h={22} w={140} /><Skeleton h={300} /></div>}>
      <QueueView />
    </Suspense>
  );
}

function QueueView() {
  const { canWrite } = useSession();
  const { toasts, push } = useToasts();
  const queue = useAsync(() => api.queue(), []);
  const rankings = useAsync(() => api.listRankings(), []);
  const [now, setNow] = useState(() => Date.now());
  const [starting, setStarting] = useState<string | null>(null);

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const activeRun = (rankings.data?.rankings ?? []).find(
    (run) => run.state === "ready" && Date.parse(run.expires_at) > now,
  );
  const ranking = useAsync(
    () => activeRun ? api.getRanking(activeRun.id) : Promise.resolve(null),
    [activeRun?.id ?? ""],
  );
  const reasonByItem = useMemo(() => {
    const map = new Map<string, { rank: number; reason: string }>();
    for (const entry of ranking.data?.entries ?? []) {
      map.set(entry.item_id, { rank: entry.rank, reason: entry.reason_md });
    }
    return map;
  }, [ranking.data]);

  async function startNow(item: QueueItem) {
    setStarting(item.item.id);
    try {
      await api.startNow(item.item.id);
      await queue.reload();
      push("info", `#${item.item.number} moved to the front of the queue.`);
    } catch (error) {
      push("error", apiIssue(error));
    } finally {
      setStarting(null);
    }
  }

  const entries = queue.data?.queue ?? [];
  return (
    <>
      <div className="pagehead">
        <h1>Dispatch queue</h1>
        <span className="pagehead__sub">Eligible work, in the order it can be dispatched.</span>
        <span style={{ marginLeft: "auto", display: "flex", gap: 8 }}>
          <Link className="btn btn--sm" href="/rankings">Rankings</Link>
        </span>
      </div>

      <div className="panel" style={{ marginBottom: 14, padding: "10px 14px", borderColor: "rgb(62 207 110 / .28)", background: "linear-gradient(105deg, rgb(62 207 110 / .07), var(--panel) 48%)" }}>
        <div className="stack" style={{ alignItems: "baseline", flexWrap: "wrap", gap: 6 }}>
          <strong className="tiny" style={{ color: "var(--accent)" }}>ORDER IS NOT ELIGIBILITY</strong>
          <span className="tiny muted">Start now moves an eligible item to the front. It never bypasses a pause, dependency, backoff, or exhausted state.</span>
        </div>
      </div>

      {queue.error ? (
        <ErrorBlock title="The queue could not load." message={apiIssue(queue.error)} retry={() => void queue.reload()} />
      ) : queue.loading ? (
        <div style={{ display: "grid", gap: 8 }}>{Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} h={64} />)}</div>
      ) : entries.length === 0 ? (
        <div className="empty panel" style={{ padding: "42px 20px" }}>
          <div className="mono tiny muted" style={{ marginBottom: 9 }}>QUEUE / 0 DISPATCHABLE</div>
          <h3>Nothing is dispatchable right now</h3>
          <p>Paused, blocked, exhausted, and still-backing-off items are left out of this list. Check the backlog or an item’s failure panel for the reason it is waiting.</p>
          <Link className="btn" href="/backlog">Open backlog</Link>
        </div>
      ) : (
        <section className="panel" aria-label="Dispatch order">
          <div className="section__head">
            <h2>Next up</h2>
            <span className="tiny muted" style={{ marginLeft: "auto" }}>{entries.length} eligible {entries.length === 1 ? "item" : "items"}</span>
          </div>
          <ol style={{ listStyle: "none", padding: 0, margin: 0 }}>
            {entries.map((entry, index) => (
              <QueueRow
                key={entry.item.id}
                entry={entry}
                position={index + 1}
                count={ranking.data?.entries.length ?? entries.length}
                reason={reasonByItem.get(entry.item.id)?.reason}
                rank={reasonByItem.get(entry.item.id)?.rank ?? entry.rank}
                canWrite={canWrite}
                busy={starting === entry.item.id}
                now={now}
                onStart={() => void startNow(entry)}
              />
            ))}
          </ol>
          {activeRun ? <div className="section__body tiny muted" style={{ borderTop: "1px solid var(--border)" }}>Order informed by ranking run <Link className="rowlink mono" href={`/rankings?id=${encodeURIComponent(activeRun.id)}`}>{activeRun.id.slice(0, 8)}</Link> · weights {activeRun.weights_version}.</div> : null}
        </section>
      )}

      {rankings.error ? <p className="tiny muted" role="status" style={{ marginTop: 12 }}>Ranking context could not be loaded: {apiIssue(rankings.error)}</p> : null}
      {ranking.error ? <p className="tiny muted" role="status" style={{ marginTop: 12 }}>Ranking explanations could not be loaded: {apiIssue(ranking.error)}</p> : null}
      <Toasts toasts={toasts} />
    </>
  );
}

function QueueRow(props: {
  entry: QueueItem;
  position: number;
  count: number;
  rank?: number;
  reason?: string;
  canWrite: boolean;
  busy: boolean;
  now: number;
  onStart: () => void;
}) {
  const { item } = props.entry;
  const when = props.entry.next_eligible_at ? Date.parse(props.entry.next_eligible_at) : NaN;
  const failure = props.entry.failure_state;
  const explanation = props.entry.start_now_at
    ? "Start now — moved to the front by a human; eligibility is unchanged."
    : props.entry.pinned
    ? "Pinned — this item is first by a human ordering override."
    : props.rank && props.reason
      ? `Rank ${props.rank} of ${props.count} — ${props.reason}`
      : props.rank
        ? `Rank ${props.rank} in the latest ranking run.`
        : `Base score ${props.entry.score.base_score} — deterministic score decides this position.`;

  return (
    <li className="queue-row" style={{ gridTemplateColumns: "42px minmax(0, 1fr) auto", gap: 12, padding: "13px 15px", borderBottom: "1px solid var(--border)", borderLeft: props.position === 1 ? "2px solid var(--accent)" : "2px solid transparent" }}>
      <span className="mono" style={{ color: props.position === 1 ? "var(--accent)" : "var(--muted)", fontSize: 17, textAlign: "center" }}>{String(props.position).padStart(2, "0")}</span>
      <div style={{ minWidth: 0 }}>
        <div className="stack" style={{ alignItems: "baseline", flexWrap: "wrap", gap: 7 }}>
          <Link className="rowlink" href={`/items/${item.id}`} style={{ fontWeight: 650 }}><span className="mono tiny muted">#{item.number}</span> {item.title}</Link>
          <span className="chip chip--kind">{item.kind}</span>
          {item.priority ? <span className="tiny muted">{item.priority.replace("priority:", "")}</span> : null}
        </div>
        <p className="tiny muted" style={{ margin: "4px 0 0" }}>{explanation}</p>
        {failure === "backing_off" ? (
          <p className="tiny" style={{ margin: "5px 0 0", color: Number.isFinite(when) && when > props.now ? "var(--warn)" : "var(--accent)" }}>
            {Number.isFinite(when) && when > props.now ? `Backing off · ${remaining(when - props.now)} remaining · not before ${clock(when)}` : Number.isFinite(when) ? "Backoff has passed — eligible again." : "Backing off · the server did not provide a next eligible time."}
          </p>
        ) : failure === "exhausted" ? (
          <p className="tiny" style={{ margin: "5px 0 0", color: "var(--danger)" }}>Exhausted · a human must reset this item before it can run again.</p>
        ) : null}
      </div>
      <div className="stack" style={{ alignItems: "center", justifyContent: "flex-end" }}>
        <span className="mono tiny" title="Base score">{props.entry.score.base_score}</span>
        {props.canWrite ? <button type="button" className="btn btn--sm btn--primary" disabled={props.busy} onClick={props.onStart}>{props.busy ? "Moving…" : "Start now"}</button> : <span className="tiny muted">Read only</span>}
      </div>
    </li>
  );
}

function clock(time: number): string {
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(time);
}

function remaining(ms: number): string {
  const seconds = Math.max(1, Math.ceil(ms / 1000));
  if (seconds < 60) return `${seconds} ${seconds === 1 ? "second" : "seconds"}`;
  const minutes = Math.ceil(seconds / 60);
  if (minutes < 60) return `${minutes} ${minutes === 1 ? "minute" : "minutes"}`;
  const hours = Math.floor(minutes / 60);
  const remainder = minutes % 60;
  return remainder ? `${hours} ${hours === 1 ? "hour" : "hours"} ${remainder} min` : `${hours} ${hours === 1 ? "hour" : "hours"}`;
}

function apiIssue(error: unknown): string {
  if (error instanceof Error && "code" in error) {
    const apiError = error as ApiError;
    return `${apiError.code}: ${apiError.message}`;
  }
  return error instanceof Error ? error.message : "Something went wrong.";
}
