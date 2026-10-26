"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { api, ApiError, type RunState } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { EmptyState, ErrorBlock, Skeleton } from "@/components/primitives";
import { formatElapsed } from "@/components/run-ui";
import { useI18n, useT } from "@/lib/i18n";

const STATES: RunState[] = ["queued", "running", "waiting_human", "completed", "failed", "cancelled"];
const ACTIVE = new Set<RunState>(["queued", "running", "waiting_human"]);

export default function RunsPage() {
  const t = useT();
  const { lang } = useI18n();
  const [state, setState] = useState("");
  const [itemId, setItemId] = useState("");
  const [clock, setClock] = useState(Date.now());
  const runs = useAsync(() => api.listRuns({ state: state ? state as RunState : undefined, item_id: itemId || undefined, limit: 100 }), [state, itemId]);
  const items = useAsync(() => api.listItems(), []);
  const templates = useAsync(() => api.listWorkflowTemplates(), []);
  const pendingCheckpoints = useAsync(() => api.listCheckpoints("pending", 100), []);
  useEffect(() => { const timer = window.setInterval(() => setClock(Date.now()), 15000); return () => window.clearInterval(timer); }, []);
  const itemMap = useMemo(() => new Map((items.data?.items ?? []).map((item) => [item.id, item])), [items.data]);
  const templateMap = useMemo(() => new Map((templates.data?.templates ?? []).map((template) => [template.id, template])), [templates.data]);
  const waitingRunIds = useMemo(() => new Set((pendingCheckpoints.data?.checkpoints ?? []).map((checkpoint) => checkpoint.run_id)), [pendingCheckpoints.data]);
  const rows = useMemo(() => [...(runs.data?.runs ?? [])].sort((a, b) => {
    const aActive = ACTIVE.has(a.state); const bActive = ACTIVE.has(b.state);
    return aActive === bActive ? Date.parse(b.started_at) - Date.parse(a.started_at) : aActive ? -1 : 1;
  }), [runs.data, clock]);
  if (runs.loading && !runs.data) return <div className="run-stack"><Skeleton h={34} w="35%" /><Skeleton h={56} /><Skeleton h={56} /><Skeleton h={56} /></div>;
  return <div className="run-stack">
    <header className="pagehead"><div><h1>{t("runs.title")}</h1><div className="pagehead__sub">{t("runs.subtitle")}</div></div><button className="btn btn--sm" onClick={() => { void runs.reload(); void pendingCheckpoints.reload(); }}>{t("runs.refresh")}</button></header>
    <div className="filters run-filters"><label className="field"><span>{t("runs.filter.state")}</span><select className="input" value={state} onChange={(e) => setState(e.target.value)}><option value="">{t("runs.filter.allStates")}</option>{STATES.map((s) => <option key={s} value={s}>{runState(s, t)}</option>)}</select></label><label className="field"><span>{t("runs.filter.item")}</span><select className="input" value={itemId} onChange={(e) => setItemId(e.target.value)}><option value="">{t("runs.filter.allItems")}</option>{(items.data?.items ?? []).map((item) => <option key={item.id} value={item.id}>#{item.number} {item.title}</option>)}</select></label><span className="tiny muted run-filter-count">{t("runs.count", { count: rows.length })}</span></div>
    {runs.error && !runs.data ? <ErrorBlock title={t("runs.error.title")} message={runsError(runs.error, t)} retry={() => void runs.reload()} /> : rows.length === 0 ? <EmptyState title={t("runs.empty.title")} body={t("runs.empty.body")} /> : <div className="panel run-table-wrap"><table className="data run-table"><thead><tr><th>{t("runs.column.run")}</th><th>{t("runs.column.item")}</th><th>{t("runs.column.template")}</th><th>{t("runs.column.state")}</th><th>{t("runs.column.elapsed")}</th><th>{t("runs.column.started")}</th><th /></tr></thead><tbody>{rows.map((run, index) => <tr key={run.id} className={index === 0 && ACTIVE.has(run.state) ? "run-row--now" : undefined}><td><Link className="rowlink mono" href={`/runs/${run.id}`}>{run.id.slice(0, 12)}</Link>{index === 0 && ACTIVE.has(run.state) ? <span className="now-label">{t("runs.now")}</span> : null}{waitingRunIds.has(run.id) ? <span className="run-waiting-marker"><i />{t("runs.waiting")}</span> : null}</td><td>{itemMap.get(run.item_id) ? <><span className="tiny muted">#{itemMap.get(run.item_id)?.number}</span> {itemMap.get(run.item_id)?.title}</> : <span className="mono tiny">{run.item_id.slice(0, 12)}</span>}</td><td>{templateMap.get(run.template_id)?.name ?? <span className="mono tiny">{run.template_id.slice(0, 10)}</span>}{run.template_version ? <span className="muted tiny"> · v{run.template_version}</span> : null}</td><td><RunStateChip state={run.state} t={t} /></td><td className="mono">{formatElapsed(run.started_at, run.finished_at)}</td><td>{run.started_at ? new Date(run.started_at).toLocaleString(lang) : "—"}</td><td><Link href={`/runs/${run.id}`} className="rowlink">{t("runs.monitor")}</Link></td></tr>)}</tbody></table></div>}
    {runs.error && runs.data ? <ErrorBlock title={t("runs.refreshFailed")} message={runsError(runs.error, t)} retry={() => void runs.reload()} /> : null}
    {(items.error || templates.error || pendingCheckpoints.error) ? <p className="tiny muted">{t("runs.detailsUnavailable")}</p> : null}
  </div>;
}

function runState(state: RunState, t: ReturnType<typeof useT>): string {
  return t(`runs.state.${state}` as "runs.state.queued" | "runs.state.running" | "runs.state.waiting_human" | "runs.state.completed" | "runs.state.failed" | "runs.state.cancelled");
}

function runsError(error: unknown, t: ReturnType<typeof useT>): string {
  if (error instanceof ApiError && error.code === "network") return t("runs.error.network");
  return error instanceof Error ? error.message || t("error.title") : t("error.title");
}

function RunStateChip({ state, t }: { state: RunState; t: ReturnType<typeof useT> }) {
  const cls = state === "running" ? "jobchip--warn" : state === "queued" ? "jobchip--queued" : state === "completed" ? "jobchip--ok" : state === "failed" || state === "cancelled" ? "jobchip--fail" : "jobchip--waiting";
  return <span className={`jobchip ${cls}`}><span className="jobchip__dot" />{runState(state, t)}</span>;
}
