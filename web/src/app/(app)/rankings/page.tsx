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

export default function RankingsPage() {
  return <Suspense fallback={<div style={{ display: "grid", gap: 12 }}><Skeleton h={22} w={180} /><Skeleton h={300} /></div>}><RankingsView /></Suspense>;
}

function RankingsView() {
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
      push("info", "Ranking run completed.");
    } catch (error) {
      push("error", apiIssue(error));
    } finally {
      setRunning(false);
    }
  }

  const run = detail.data?.ranking ?? null;
  const degraded = Boolean(run?.degraded_reason);

  return (
    <>
      <div className="pagehead">
        <h1>Rankings</h1>
        <span className="pagehead__sub">A ranked pass explains order; eligibility still comes first.</span>
        <span style={{ marginLeft: "auto" }}>
          {canWrite ? <button type="button" className="btn btn--sm btn--primary" disabled={running} onClick={() => void runRanking()}>{running ? "Running…" : "Run the ranking"}</button> : <span className="tiny muted">Read only</span>}
        </span>
      </div>

      {runs.error ? (
        <ErrorBlock title="Ranking runs could not load." message={apiIssue(runs.error)} retry={() => void runs.reload()} />
      ) : runs.loading ? (
        <div style={{ display: "grid", gap: 10 }}><Skeleton h={90} /><Skeleton h={220} /></div>
      ) : runs.data?.rankings.length === 0 && !requestedId ? (
        <div className="empty panel" style={{ padding: "42px 20px" }}>
          <div className="mono tiny muted" style={{ marginBottom: 9 }}>RANKING RUNS / NONE</div>
          <h3>No ranking run yet</h3>
          <p>The queue is still usable with pinned order and the deterministic base score. A ranking pass adds an explanation for how eligible items compare.</p>
          {canWrite ? <button className="btn btn--primary" type="button" disabled={running} onClick={() => void runRanking()}>{running ? "Running…" : "Run the ranking"}</button> : null}
        </div>
      ) : detail.error ? (
        <ErrorBlock title="This ranking run could not load." message={apiIssue(detail.error)} retry={() => void detail.reload()} />
      ) : detail.loading ? (
        <div style={{ display: "grid", gap: 10 }}><Skeleton h={100} /><Skeleton h={240} /></div>
      ) : run && detail.data ? (
        <>
          <section className="panel" style={{ marginBottom: 14 }}>
            <div className="section__head" style={{ flexWrap: "wrap" }}>
              <div>
                <div className="mono tiny muted">RUN · {run.id.slice(0, 8)}</div>
                <h2 style={{ marginTop: 4 }}>{run.state === "ready" ? "Dispatch order explained" : `Ranking ${run.state}`}</h2>
              </div>
              <span className={`badge-outline ranking-state ranking-state--${run.state}`}>{run.state}</span>
              {degraded ? <span className="badge-outline" style={{ color: "var(--warn)", borderColor: "rgb(240 180 41 / .4)" }}>Degraded · {run.degraded_reason}</span> : null}
            </div>
            <div className="section__body" style={{ display: "grid", gap: 12 }}>
              {degraded ? <div className="error-note" style={{ color: "var(--warn)", borderColor: "rgb(240 180 41 / .3)" }}>
                The model pass did not decide the order ({run.degraded_reason}). The deterministic base score decided instead.
              </div> : null}
              <p style={{ margin: 0, maxWidth: 900, whiteSpace: "pre-wrap" }}>{run.summary_md || "This run has no summary."}</p>
              <div className="stack tiny muted" style={{ flexWrap: "wrap", gap: "6px 18px" }}>
                <span>Weights <span className="mono">{run.weights_version}</span></span>
                <span>{run.llm_model ? `Model ${run.llm_model}` : "No model recorded"}</span>
                <span>Computed {formatDateTime(run.computed_at)}</span>
                <span>Expires {formatDateTime(run.expires_at)}</span>
              </div>
              <div>
                <div className="tiny muted" style={{ marginBottom: 6 }}><strong>Stalled roots</strong> · items that unblock the most waiting work</div>
                {roots.length ? <ul style={{ margin: 0, paddingLeft: 18, display: "grid", gap: 4 }}>
                  {roots.map((root) => {
                    const item = itemById.get(root.item_id);
                    return <li key={root.item_id} className="tiny"><Link className="rowlink" href={`/items/${root.item_id}`}>{item ? `#${item.number} — ${item.title}` : `Item ${root.item_id.slice(0, 8)}`}</Link><span className="muted"> · unblocks {root.unblocks} · {root.status}</span></li>;
                  })}
                </ul> : <p className="tiny muted" style={{ margin: 0 }}>No stalled roots were recorded for this run.</p>}
              </div>
            </div>
          </section>

          <section className="panel">
            <div className="section__head"><h2>Ranked items</h2><span className="tiny muted" style={{ marginLeft: "auto" }}>{detail.data.entries.length} entries</span></div>
            {detail.data.entries.length ? <div className="table-wrap">
              <table className="data">
                <thead><tr><th>Rank</th><th>Item</th><th>Score</th><th>Reason</th><th>Base score</th></tr></thead>
                <tbody>{detail.data.entries.map((entry) => {
                  const item = itemById.get(entry.item_id);
                  return <tr key={entry.id}>
                    <td className="mono">{entry.rank}</td>
                    <td>{item ? <Link className="rowlink" href={`/items/${item.id}`}><span className="mono tiny muted">#{item.number}</span> {item.title}</Link> : <span className="mono tiny muted">{entry.item_id}</span>}</td>
                    <td className="mono">{entry.score}</td>
                    <td style={{ minWidth: 260, whiteSpace: "pre-wrap" }}>{entry.reason_md || "No reason was recorded."}</td>
                    <td className="mono">{entry.base_score}</td>
                  </tr>;
                })}</tbody>
              </table>
            </div> : <p className="tiny muted" style={{ padding: "12px 16px" }}>This run has no entries.</p>}
          </section>
          {items.error ? <p className="tiny muted" role="status" style={{ marginTop: 10 }}>Item names could not be resolved: {apiIssue(items.error)}</p> : null}

          {runs.data?.rankings.length && runs.data.rankings.length > 1 ? <div style={{ marginTop: 18 }}>
            <div className="tiny muted" style={{ marginBottom: 6 }}>Earlier runs</div>
            <div className="stack" style={{ flexWrap: "wrap" }}>{runs.data.rankings.filter((r) => r.id !== run.id).map((r) => <Link key={r.id} className="btn btn--sm" href={`/rankings?id=${encodeURIComponent(r.id)}`}>{r.state} · {formatDateTime(r.computed_at)}</Link>)}</div>
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

function apiIssue(error: unknown): string {
  if (error instanceof Error && "code" in error) {
    const apiError = error as ApiError;
    return `${apiError.code}: ${apiError.message}`;
  }
  return error instanceof Error ? error.message : "Something went wrong.";
}
