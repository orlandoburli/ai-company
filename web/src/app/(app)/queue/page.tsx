"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, type ApiError } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import type { QueueItem, RankingRun } from "@/lib/types";
import { ErrorBlock, Skeleton, Toasts } from "@/components/primitives";
import { useI18n, useT } from "@/lib/i18n";

export default function QueuePage() {
  return (
    <Suspense fallback={<div style={{ display: "grid", gap: 12 }}><Skeleton h={22} w={140} /><Skeleton h={300} /></div>}>
      <QueueView />
    </Suspense>
  );
}

function QueueView() {
  const t = useT();
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
      push("info", t("queue.startNowSuccess", { number: item.item.number }));
    } catch (error) {
      push("error", apiIssue(error, t));
    } finally {
      setStarting(null);
    }
  }

  const entries = queue.data?.queue ?? [];
  return (
    <>
      <div className="pagehead">
        <h1>{t("queue.title")}</h1>
        <span className="pagehead__sub">{t("queue.subtitle")}</span>
        <span style={{ marginLeft: "auto", display: "flex", gap: 8 }}>
          <Link className="btn btn--sm" href="/rankings">{t("queue.rankings")}</Link>
        </span>
      </div>

      <div className="panel" style={{ marginBottom: 14, padding: "10px 14px", borderColor: "var(--theme-success-border-subtle)", background: "var(--theme-success-bg-subtle)" }}>
        <div className="stack" style={{ alignItems: "baseline", flexWrap: "wrap", gap: 6 }}>
          <strong className="tiny" style={{ color: "var(--accent)" }}>{t("queue.orderNotice.title")}</strong>
          <span className="tiny muted">{t("queue.orderNotice.body")}</span>
        </div>
      </div>

      {queue.error ? (
        <ErrorBlock title={t("queue.loadError")} message={apiIssue(queue.error, t)} retry={() => void queue.reload()} />
      ) : queue.loading ? (
        <div style={{ display: "grid", gap: 8 }}>{Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} h={64} />)}</div>
      ) : entries.length === 0 ? (
        <div className="empty panel" style={{ padding: "42px 20px" }}>
          <div className="mono tiny muted" style={{ marginBottom: 9 }}>{t("queue.empty.code")}</div>
          <h3>{t("queue.empty.title")}</h3>
          <p>{t("queue.empty.body")}</p>
          <Link className="btn" href="/backlog">{t("queue.openBacklog")}</Link>
        </div>
      ) : (
        <section className="panel" aria-label={t("queue.dispatchOrder")}>
          <div className="section__head">
            <h2>{t("queue.nextUp")}</h2>
            <span className="tiny muted" style={{ marginLeft: "auto" }}>{t("queue.eligibleCount", { count: entries.length })}</span>
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
                t={t}
              />
            ))}
          </ol>
          {activeRun ? <div className="section__body tiny muted" style={{ borderTop: "1px solid var(--border)" }}>{t("queue.rankingOrder")} <Link className="rowlink mono" href={`/rankings?id=${encodeURIComponent(activeRun.id)}`}>{activeRun.id.slice(0, 8)}</Link> · {t("queue.weights", { version: activeRun.weights_version })}</div> : null}
        </section>
      )}

      {rankings.error ? <p className="tiny muted" role="status" style={{ marginTop: 12 }}>{t("queue.rankingContext", { error: apiIssue(rankings.error, t) })}</p> : null}
      {ranking.error ? <p className="tiny muted" role="status" style={{ marginTop: 12 }}>{t("queue.rankingExplanations", { error: apiIssue(ranking.error, t) })}</p> : null}
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
  t: ReturnType<typeof useT>;
}) {
  const { lang } = useI18n();
  const { t } = props;
  const { item } = props.entry;
  const when = props.entry.next_eligible_at ? Date.parse(props.entry.next_eligible_at) : NaN;
  const failure = props.entry.failure_state;
  const explanation = props.entry.start_now_at
    ? t("queue.explanation.startNow")
    : props.entry.pinned
    ? t("queue.explanation.pinned")
    : props.rank && props.reason
      ? t("queue.explanation.rank", { rank: props.rank, count: props.count, reason: props.reason })
      : props.rank
        ? t("queue.explanation.latestRank", { rank: props.rank })
        : t("queue.explanation.baseScore", { score: props.entry.score.base_score });

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
            {Number.isFinite(when) && when > props.now ? t("queue.backoff.remaining", { remaining: remaining(when - props.now, t), time: clock(when, lang) }) : Number.isFinite(when) ? t("queue.backoff.passed") : t("queue.backoff.noTime")}
          </p>
        ) : failure === "exhausted" ? (
          <p className="tiny" style={{ margin: "5px 0 0", color: "var(--danger)" }}>{t("queue.exhausted")}</p>
        ) : null}
      </div>
      <div className="stack" style={{ alignItems: "center", justifyContent: "flex-end" }}>
        <span className="mono tiny" title={t("queue.baseScore")}>{props.entry.score.base_score}</span>
        {props.canWrite ? <button type="button" className="btn btn--sm btn--primary" disabled={props.busy} onClick={props.onStart}>{props.busy ? t("queue.moving") : t("queue.startNow")}</button> : <span className="tiny muted">{t("queue.readOnly")}</span>}
      </div>
    </li>
  );
}

function clock(time: number, lang: string): string {
  return new Intl.DateTimeFormat(lang, { hour: "2-digit", minute: "2-digit" }).format(time);
}

function remaining(ms: number, t: ReturnType<typeof useT>): string {
  const seconds = Math.max(1, Math.ceil(ms / 1000));
  if (seconds < 60) return t("queue.seconds", { count: seconds });
  const minutes = Math.ceil(seconds / 60);
  if (minutes < 60) return t("queue.minutes", { count: minutes });
  const hours = Math.floor(minutes / 60);
  const remainder = minutes % 60;
  return remainder ? `${t("queue.hours", { count: hours })} ${t("queue.minutesShort", { count: remainder })}` : t("queue.hours", { count: hours });
}

function apiIssue(error: unknown, t: ReturnType<typeof useT>): string {
  if (error instanceof Error && "code" in error) {
    const apiError = error as ApiError;
    return apiError.code === "network" ? t("queue.error.network") : apiError.message || t("error.title");
  }
  return error instanceof Error ? error.message : t("error.title");
}
