"use client";

import { Suspense, useMemo, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { api, type ApiError } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import type { RankingRun } from "@/lib/types";
import { ErrorBlock, Skeleton, Toasts } from "@/components/primitives";
import { formatDateTime } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { STATUS_LABELS } from "@/lib/format";
import type { Status } from "@/lib/types";

export default function RankingsPage() {
  return <Suspense fallback={<div style={{ display: "grid", gap: 12 }}><Skeleton h={22} w={180} /><Skeleton h={300} /></div>}><RankingsView /></Suspense>;
}

function RankingsView() {
  const t = useT();
  const params = useSearchParams();
  const router = useRouter();
  const { canWrite } = useSession();
  const { toasts, push } = useToasts();
  const runs = useAsync(() => api.listRankings(), []);
  const items = useAsync(() => api.listItems().then((r) => r.items), []);
  const requestedId = params.get("id") ?? "";
  const latest = runs.data?.rankings[0] ?? null;
  const selectedId = requestedId || latest?.id || "";
  const detail = useAsync(
    () => selectedId ? api.getRanking(selectedId) : Promise.resolve(null),
    [selectedId],
  );
  const [running, setRunning] = useState(false);

  const itemById = useMemo(() => new Map((items.data ?? []).map((item) => [item.id, item])), [items.data]);
  const roots = useMemo(() => parseRoots(detail.data?.ranking.roots_json), [detail.data]);

  async function runRanking() {
    setRunning(true);
    try {
      const result = await api.runRanking();
      await runs.reload();
      router.replace(`/rankings?id=${encodeURIComponent(result.ranking.id)}`, { scroll: false });
      push("info", t("rankings.completedToast"));
    } catch (error) {
      push("error", apiIssue(error, t));
    } finally {
      setRunning(false);
    }
  }

  const run = detail.data?.ranking ?? null;
  const degraded = Boolean(run?.degraded_reason);

  return (
    <>
      <div className="pagehead">
        <h1>{t("rankings.title")}</h1>
        <span className="pagehead__sub">{t("rankings.subtitle")}</span>
        <span style={{ marginLeft: "auto" }}>
          {canWrite ? <button type="button" className="btn btn--sm btn--primary" disabled={running} onClick={() => void runRanking()}>{running ? t("rankings.running") : t("rankings.run")}</button> : <span className="tiny muted">{t("rankings.readOnly")}</span>}
        </span>
      </div>

      {runs.error ? (
        <ErrorBlock title={t("rankings.loadError")} message={apiIssue(runs.error, t)} retry={() => void runs.reload()} />
      ) : runs.loading ? (
        <div style={{ display: "grid", gap: 10 }}><Skeleton h={90} /><Skeleton h={220} /></div>
      ) : runs.data?.rankings.length === 0 && !requestedId ? (
        <div className="empty panel" style={{ padding: "42px 20px" }}>
          <div className="mono tiny muted" style={{ marginBottom: 9 }}>{t("rankings.empty.code")}</div>
          <h3>{t("rankings.empty.title")}</h3>
          <p>{t("rankings.empty.body")}</p>
          {canWrite ? <button className="btn btn--primary" type="button" disabled={running} onClick={() => void runRanking()}>{running ? t("rankings.running") : t("rankings.run")}</button> : null}
        </div>
      ) : detail.error ? (
        <ErrorBlock title={t("rankings.detailError")} message={apiIssue(detail.error, t)} retry={() => void detail.reload()} />
      ) : detail.loading ? (
        <div style={{ display: "grid", gap: 10 }}><Skeleton h={100} /><Skeleton h={240} /></div>
      ) : run && detail.data ? (
        <>
          <section className="panel" style={{ marginBottom: 14 }}>
            <div className="section__head" style={{ flexWrap: "wrap" }}>
              <div>
                <div className="mono tiny muted">{t("rankings.runLabel", { id: run.id.slice(0, 8) })}</div>
                <h2 style={{ marginTop: 4 }}>{run.state === "ready" ? t("rankings.explainedOrder") : t("rankings.state", { state: rankingState(run.state, t) })}</h2>
              </div>
              <span className={`badge-outline ranking-state ranking-state--${run.state}`}>{rankingState(run.state, t)}</span>
              {degraded ? <span className="badge-outline" style={{ color: "var(--theme-warning-text-emphasis)", borderColor: "var(--theme-warning-border-subtle)", background: "var(--theme-warning-bg-subtle)" }}>{t("rankings.degraded", { reason: degradedReason(run.degraded_reason ?? "", t) })}</span> : null}
            </div>
            <div className="section__body" style={{ display: "grid", gap: 12 }}>
              {degraded ? <div className="error-note" style={{ color: "var(--theme-warning-text-emphasis)", borderColor: "var(--theme-warning-border-subtle)", background: "var(--theme-warning-bg-subtle)" }}>
                {t("rankings.degradedExplanation", { reason: degradedReason(run.degraded_reason ?? "", t) })}
              </div> : null}
              <p style={{ margin: 0, maxWidth: 900, whiteSpace: "pre-wrap" }}>{run.summary_md || t("rankings.noSummary")}</p>
              <div className="stack tiny muted" style={{ flexWrap: "wrap", gap: "6px 18px" }}>
                <span>{t("rankings.weights")} <span className="mono">{run.weights_version}</span></span>
                <span>{run.llm_model ? t("rankings.model", { model: run.llm_model }) : t("rankings.noModel")}</span>
                <span>{t("rankings.computed", { date: formatDateTime(run.computed_at) })}</span>
                <span>{t("rankings.expires", { date: formatDateTime(run.expires_at) })}</span>
              </div>
              <div>
                <div className="tiny muted" style={{ marginBottom: 6 }}><strong>{t("rankings.stalledRoots")}</strong> · {t("rankings.stalledHint")}</div>
                {roots.length ? <ul style={{ margin: 0, paddingLeft: 18, display: "grid", gap: 4 }}>
                  {roots.map((root) => {
                    const item = itemById.get(root.item_id);
                    return <li key={root.item_id} className="tiny"><Link className="rowlink" href={`/items/${root.item_id}`}>{item ? `#${item.number} — ${item.title}` : t("rankings.itemFallback", { id: root.item_id.slice(0, 8) })}</Link><span className="muted"> · {t("rankings.rootUnblocks", { count: root.unblocks })} · {rankingState(root.status, t)}</span></li>;
                  })}
                </ul> : <p className="tiny muted" style={{ margin: 0 }}>{t("rankings.noStalledRoots")}</p>}
              </div>
            </div>
          </section>

          <section className="panel">
            <div className="section__head"><h2>{t("rankings.items")}</h2><span className="tiny muted" style={{ marginLeft: "auto" }}>{t("rankings.entries", { count: detail.data.entries.length })}</span></div>
            {detail.data.entries.length ? <div className="table-wrap">
              <table className="data">
                <thead><tr><th>{t("rankings.column.rank")}</th><th>{t("rankings.column.item")}</th><th>{t("rankings.column.score")}</th><th>{t("rankings.column.reason")}</th><th>{t("rankings.column.baseScore")}</th></tr></thead>
                <tbody>{detail.data.entries.map((entry) => {
                  const item = itemById.get(entry.item_id);
                  return <tr key={entry.id}>
                    <td className="mono">{entry.rank}</td>
                    <td>{item ? <Link className="rowlink" href={`/items/${item.id}`}><span className="mono tiny muted">#{item.number}</span> {item.title}</Link> : <span className="mono tiny muted">{entry.item_id}</span>}</td>
                    <td className="mono">{entry.score}</td>
                    <td style={{ minWidth: 260, whiteSpace: "pre-wrap" }}>{entry.reason_md || t("rankings.noReason")}</td>
                    <td className="mono">{entry.base_score}</td>
                  </tr>;
                })}</tbody>
              </table>
            </div> : <p className="tiny muted" style={{ padding: "12px 16px" }}>{t("rankings.noEntries")}</p>}
          </section>
          {items.error ? <p className="tiny muted" role="status" style={{ marginTop: 10 }}>{t("rankings.itemNamesError", { error: apiIssue(items.error, t) })}</p> : null}

          {runs.data?.rankings.length && runs.data.rankings.length > 1 ? <div style={{ marginTop: 18 }}>
            <div className="tiny muted" style={{ marginBottom: 6 }}>{t("rankings.earlierRuns")}</div>
            <div className="stack" style={{ flexWrap: "wrap" }}>{runs.data.rankings.filter((r) => r.id !== run.id).map((r) => <Link key={r.id} className="btn btn--sm" href={`/rankings?id=${encodeURIComponent(r.id)}`}>{rankingState(r.state, t)} · {formatDateTime(r.computed_at)}</Link>)}</div>
          </div> : null}
        </>
      ) : null}
      <Toasts toasts={toasts} />
    </>
  );
}

function parseRoots(value: RankingRun["roots_json"] | undefined): { item_id: string; unblocks: number; status: string }[] {
  if (!value) return [];
  if (Array.isArray(value)) return value;
  try {
    const parsed: unknown = JSON.parse(value);
    return Array.isArray(parsed) ? parsed.filter(isRoot) : [];
  } catch {
    return [];
  }
}

function isRoot(value: unknown): value is { item_id: string; unblocks: number; status: string } {
  if (!value || typeof value !== "object") return false;
  const root = value as Record<string, unknown>;
  return typeof root.item_id === "string" && typeof root.unblocks === "number" && typeof root.status === "string";
}

function rankingState(state: string, t: ReturnType<typeof useT>): string {
  const key = `rankings.state.${state}`;
  if (["ready", "running", "failed", "superseded"].includes(state)) return t(key as "rankings.state.ready" | "rankings.state.running" | "rankings.state.failed" | "rankings.state.superseded");
  if (["backlog", "todo", "in_progress", "waiting_human", "in_review", "done", "cancelled"].includes(state)) return STATUS_LABELS[state as Status];
  return state.replaceAll("_", " ");
}

function degradedReason(reason: string, t: ReturnType<typeof useT>): string {
  if (["llm_failed", "invalid_output", "over_budget"].includes(reason)) {
    return t(`rankings.degradedReason.${reason}` as "rankings.degradedReason.llm_failed" | "rankings.degradedReason.invalid_output" | "rankings.degradedReason.over_budget");
  }
  return reason;
}

function apiIssue(error: unknown, t: ReturnType<typeof useT>): string {
  if (error instanceof Error && "code" in error) {
    const apiError = error as ApiError;
    return apiError.code === "network" ? t("rankings.error.network") : apiError.message || t("rankings.error.generic");
  }
  return error instanceof Error ? error.message : t("rankings.error.generic");
}
