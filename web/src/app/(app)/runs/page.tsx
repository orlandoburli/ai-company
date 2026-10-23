"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { api, type RunState } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { EmptyState, ErrorBlock, Skeleton } from "@/components/primitives";
import { formatElapsed, StateChip } from "@/components/run-ui";

const STATES: RunState[] = ["queued", "running", "waiting_human", "completed", "failed", "cancelled"];
const ACTIVE = new Set<RunState>(["queued", "running", "waiting_human"]);

export default function RunsPage() {
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
    <header className="pagehead"><div><h1>Runs</h1><div className="pagehead__sub">Execution history across the workspace. Active runs rise to the top.</div></div><button className="btn btn--sm" onClick={() => { void runs.reload(); void pendingCheckpoints.reload(); }}>Refresh</button></header>
    <div className="filters run-filters"><label className="field"><span>State</span><select className="input" value={state} onChange={(e) => setState(e.target.value)}><option value="">All states</option>{STATES.map((s) => <option key={s} value={s}>{s.replaceAll("_", " ")}</option>)}</select></label><label className="field"><span>Item</span><select className="input" value={itemId} onChange={(e) => setItemId(e.target.value)}><option value="">All items</option>{(items.data?.items ?? []).map((item) => <option key={item.id} value={item.id}>#{item.number} {item.title}</option>)}</select></label><span className="tiny muted run-filter-count">{rows.length} {rows.length === 1 ? "run" : "runs"}</span></div>
    {runs.error && !runs.data ? <ErrorBlock title="Runs are not available yet" message={runs.error.message} retry={() => void runs.reload()} /> : rows.length === 0 ? <EmptyState title="No runs yet" body="Runs will appear here once a workflow is started. Reads are available to every role." /> : <div className="panel run-table-wrap"><table className="data run-table"><thead><tr><th>Run</th><th>Item</th><th>Template</th><th>State</th><th>Elapsed</th><th>Started</th><th /></tr></thead><tbody>{rows.map((run, index) => <tr key={run.id} className={index === 0 && ACTIVE.has(run.state) ? "run-row--now" : undefined}><td><Link className="rowlink mono" href={`/runs/${run.id}`}>{run.id.slice(0, 12)}</Link>{index === 0 && ACTIVE.has(run.state) ? <span className="now-label">Happening now</span> : null}{waitingRunIds.has(run.id) ? <span className="run-waiting-marker"><i />Waiting for you</span> : null}</td><td>{itemMap.get(run.item_id) ? <><span className="tiny muted">#{itemMap.get(run.item_id)?.number}</span> {itemMap.get(run.item_id)?.title}</> : <span className="mono tiny">{run.item_id.slice(0, 12)}</span>}</td><td>{templateMap.get(run.template_id)?.name ?? <span className="mono tiny">{run.template_id.slice(0, 10)}</span>}{run.template_version ? <span className="muted tiny"> · v{run.template_version}</span> : null}</td><td><StateChip state={run.state} /></td><td className="mono">{formatElapsed(run.started_at, run.finished_at)}</td><td>{run.started_at ? new Date(run.started_at).toLocaleString() : "—"}</td><td><Link href={`/runs/${run.id}`} className="rowlink">Monitor →</Link></td></tr>)}</tbody></table></div>}
    {runs.error && runs.data ? <ErrorBlock title="Refresh failed" message={runs.error.message} retry={() => void runs.reload()} /> : null}
    {(items.error || templates.error || pendingCheckpoints.error) ? <p className="tiny muted">Some item, template or checkpoint details are unavailable while the M3 API is being brought online.</p> : null}
  </div>;
}
