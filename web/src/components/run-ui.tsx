"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { ApiError, api, openRunEvents, type Checkpoint, type CheckpointArtifact, type CheckpointDecision, type CheckpointFieldSpec, type Intervention, type LogEntry, type Run, type StepRun, type WorkflowEdge, type WorkflowGraph, type WorkflowNode } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { apiErrorMessage, EmptyState, ErrorBlock, Field, Skeleton, Toasts } from "@/components/primitives";
import { renderMarkdown } from "@/components/chat";

const STEP_COLOR: Record<string, string> = {
  pending: "#596579", ready: "#58a6ff", running: "#58a6ff", waiting: "#f0b429",
  succeeded: "#3ecf6e", failed: "#f4756b", skipped: "#8590a4", blocked: "#f4756b",
  cancelled: "#8590a4", superseded: "#a78bfa",
};
const EDGE_COLOR = { delivered: "#3ecf6e", skipped: "#8590a4", dead: "#f4756b" };

export function formatElapsed(start?: string | null, end?: string | null): string {
  if (!start) return "—";
  const seconds = Math.max(0, Math.floor(((end ? new Date(end) : new Date()).getTime() - new Date(start).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
}

export function StateChip({ state }: { state: string }) {
  const cls = state === "running" || state === "ready" ? "jobchip--run" : ["completed", "succeeded"].includes(state) ? "jobchip--ok" : ["failed", "blocked"].includes(state) ? "jobchip--fail" : ["waiting_human", "waiting"].includes(state) ? "jobchip--warn" : "jobchip--queued";
  return <span className={`jobchip ${cls}`}><span className="jobchip__dot" />{state.replaceAll("_", " ")}</span>;
}

interface RunGraphProps {
  graph?: WorkflowGraph | null;
  steps: StepRun[];
  selected?: string;
  onSelect: (step: StepRun) => void;
}

function graphFromRun(run?: Run | null): WorkflowGraph | null {
  const raw = run?.definition_snapshot_json;
  if (!raw) return null;
  try {
    const value = typeof raw === "string" ? JSON.parse(raw) as unknown : raw;
    if (value && typeof value === "object" && "nodes" in value) return value as WorkflowGraph;
    if (value && typeof value === "object" && "graph" in value) return (value as { graph: WorkflowGraph }).graph;
  } catch { /* malformed snapshots stay in the honest no-graph state */ }
  return null;
}

function layoutNodes(nodes: WorkflowNode[], edges: WorkflowEdge[]) {
  const incoming = new Map(nodes.map((node) => [node.id, 0]));
  const outgoing = new Map(nodes.map((node) => [node.id, [] as string[]]));
  edges.forEach((edge) => {
    incoming.set(edge.to.node, (incoming.get(edge.to.node) ?? 0) + 1);
    outgoing.get(edge.from.node)?.push(edge.to.node);
  });
  const depth = new Map<string, number>();
  const queue = nodes.filter((node) => !incoming.get(node.id)).map((node) => node.id);
  queue.forEach((id) => depth.set(id, 0));
  for (let i = 0; i < queue.length; i += 1) {
    const id = queue[i];
    for (const child of outgoing.get(id) ?? []) {
      depth.set(child, Math.max(depth.get(child) ?? 0, (depth.get(id) ?? 0) + 1));
      incoming.set(child, (incoming.get(child) ?? 1) - 1);
      if (incoming.get(child) === 0) queue.push(child);
    }
  }
  // Invalid/cyclic leftovers are still visible rather than disappearing.
  nodes.forEach((node) => { if (!depth.has(node.id)) depth.set(node.id, 0); });
  const columns = new Map<number, WorkflowNode[]>();
  nodes.forEach((node) => {
    const d = depth.get(node.id) ?? 0;
    columns.set(d, [...(columns.get(d) ?? []), node]);
  });
  const hasPositions = nodes.some((node) => node.position && Number.isFinite(node.position.x) && Number.isFinite(node.position.y));
  const points = new Map<string, { x: number; y: number }>();
  if (hasPositions) {
    const positioned = nodes.filter((node) => node.position);
    const minX = Math.min(...positioned.map((node) => node.position!.x));
    const minY = Math.min(...positioned.map((node) => node.position!.y));
    nodes.forEach((node, index) => points.set(node.id, node.position
      ? { x: 150 + (node.position.x - minX) * 1.5, y: 80 + (node.position.y - minY) * 1.25 }
      : { x: 150 + (depth.get(node.id) ?? 0) * 240, y: 80 + index * 110 }));
  } else {
    [...columns.entries()].sort(([a], [b]) => a - b).forEach(([d, col]) => col.forEach((node, row) => points.set(node.id, { x: 150 + d * 245, y: 80 + row * 118 })));
  }
  const width = Math.max(460, ...Array.from(points.values()).map((point) => point.x + 190));
  const height = Math.max(220, ...Array.from(points.values()).map((point) => point.y + 78));
  return { points, width, height };
}

export function RunGraph({ graph, steps, selected, onSelect }: RunGraphProps) {
  const nodes = graph?.nodes ?? [];
  const edges = graph?.edges ?? [];
  const layout = useMemo(() => layoutNodes(nodes, edges), [nodes, edges]);
  const stepByNode = useMemo(() => new Map(steps.map((step) => [step.node_id, step])), [steps]);
  if (!nodes.length) return <div className="run-graph-empty">The run snapshot does not include a graph yet.</div>;
  return <div className="run-graph-scroll" aria-label="Run workflow graph">
    <div className="run-graph" style={{ width: layout.width, height: layout.height }}>
      <svg className="run-graph__edges" width={layout.width} height={layout.height} aria-hidden="true">
        {edges.map((edge) => {
          const a = layout.points.get(edge.from.node); const b = layout.points.get(edge.to.node);
          if (!a || !b) return null;
          const receipt = stepByNode.get(edge.to.node)?.input_edges?.[edge.id];
          const color = receipt ? EDGE_COLOR[receipt.state] : "#344052";
          return <g key={edge.id}><path d={`M ${a.x + 165} ${a.y + 30} C ${a.x + 205} ${a.y + 30}, ${b.x - 42} ${b.y + 30}, ${b.x} ${b.y + 30}`} fill="none" stroke={color} strokeWidth="2" />
            <circle cx={b.x} cy={b.y + 30} r="3" fill={color} /></g>;
        })}
      </svg>
      {nodes.map((node) => {
        const step = stepByNode.get(node.id);
        const state = step?.state ?? "pending";
        return <button type="button" key={node.id} className={`run-node ${selected === step?.id ? "run-node--selected" : ""}`} style={{ left: layout.points.get(node.id)?.x, top: layout.points.get(node.id)?.y, borderLeftColor: STEP_COLOR[state] }} onClick={() => step && onSelect(step)} aria-pressed={selected === step?.id}>
          <span className="run-node__kind">{node.kind}</span><strong>{node.name || node.id}</strong>{step?.result?.runner ? <span className="run-node__runner">↳ {step.result.runner}</span> : null}<span className="run-node__foot"><StateChip state={state} />{step?.run_index ? <span className="mono">lap {step.run_index + 1}</span> : null}</span>
        </button>;
      })}
    </div>
  </div>;
}

export function CheckpointActions({ checkpoint, onDecided, compact = false }: { checkpoint: Checkpoint; onDecided?: (cp: Checkpoint) => void; compact?: boolean }) {
  const { canWrite } = useSession();
  const { toasts, push } = useToasts();
  const [decisionText, setDecisionText] = useState("");
  const [fields, setFields] = useState<Record<string, unknown>>({});
  const [busy, setBusy] = useState(false);
  const [won, setWon] = useState<Checkpoint | null>(null);
  const [wonText, setWonText] = useState("");
  const specs = checkpoint.fields_spec ?? [];
  const pending = checkpoint.state === "pending" && !won;
  const missingFields = specs.filter((spec) => spec.required && !fieldHasValue(spec, fields[spec.key]));
  const missingRequired = missingFields.length > 0;
  async function decide(decision: CheckpointDecision) {
    setBusy(true);
    try {
      const response = await api.decideCheckpoint(checkpoint.id, {
        decision,
        ...(decisionText ? { decision_md: decisionText } : {}),
        ...(specs.length ? { fields: Object.fromEntries(specs.map((spec) => [spec.key, fields[spec.key]]).filter(([, value]) => value !== undefined && value !== "")) } : {}),
      });
      setWon(response.checkpoint); onDecided?.(response.checkpoint); push("info", `Checkpoint ${decision.replaceAll("_", " ")} recorded.`);
    } catch (error) {
      if (error instanceof ApiError && error.code === "already_decided") {
        const payload = error.data as Record<string, unknown> | undefined;
        const envelope = payload?.error && typeof payload.error === "object" ? payload.error as Record<string, unknown> : {};
        const winnerValue = payload?.checkpoint ?? payload?.winning_decision ?? payload?.decision ?? envelope.checkpoint ?? envelope.winning_decision ?? envelope.decision;
        const winner = winnerValue && typeof winnerValue === "object" && "id" in winnerValue ? winnerValue as Checkpoint : null;
        const winningText = typeof winnerValue === "string" ? winnerValue : winner?.decision_md ?? (winnerValue && typeof winnerValue === "object" ? JSON.stringify(winnerValue) : "");
        if (winner) { setWon(winner); onDecided?.(winner); }
        if (winningText) setWonText(winningText);
        push("error", `already_decided — ${error.message}${winningText ? ` · winning decision: ${winningText}` : " · the server reports another decision won; refresh to load its details."}`);
      } else push("error", `${error instanceof ApiError ? error.code + " — " : ""}${apiErrorMessage(error)}`);
    } finally { setBusy(false); }
  }
  return <div className={`checkpoint-actions ${compact ? "checkpoint-actions--compact" : ""}`}>
    {specs.length ? <div className="checkpoint-fields-wrap"><div className="checkpoint-form-heading"><strong>Structured direction</strong><span className="tiny muted">Values are saved with this lap</span></div><div className="checkpoint-fields">{specs.map((spec) => <CheckpointField key={spec.key} spec={spec} value={fields[spec.key]} onChange={(v) => setFields((all) => ({ ...all, [spec.key]: v }))} disabled={!canWrite || !pending} />)}</div></div> : null}
    <label className="field"><span>Decision notes / feedback</span><textarea className="input" value={decisionText} disabled={!canWrite || !pending} onChange={(event) => setDecisionText(event.target.value)} placeholder="Optional for approval; required for edits or rejection" rows={compact ? 2 : 3} /></label>
    {won || wonText || checkpoint.state !== "pending" ? <div className="run-notice">{won || checkpoint.state !== "pending" ? "Decision recorded" : "already_decided · winning decision"}{wonText || (won ?? checkpoint).decision_md ? `: ${wonText || (won ?? checkpoint).decision_md}` : "."} {((won ?? checkpoint).decided_at) ? <span className="muted"> · {new Date((won ?? checkpoint).decided_at!).toLocaleString()}</span> : null}</div> : null}
    {canWrite ? <>
      {pending && missingRequired ? <p className="checkpoint-validity" role="status">Complete the required field{missingFields.length === 1 ? "" : "s"}: {missingFields.map((spec) => spec.label).join(", ")}.</p> : null}
      {pending && !decisionText.trim() ? <p className="checkpoint-validity" role="status">Add notes to enable <strong>Approve with edits</strong> or <strong>Reject with feedback</strong>.</p> : null}
      <div className="run-actions">
        <button className="btn btn--primary btn--sm" disabled={!pending || busy || missingRequired} onClick={() => void decide("approve")} title={missingRequired ? "Complete the required structured fields first." : undefined}>Approve</button>
        <button className="btn btn--sm" disabled={!pending || busy || missingRequired || !decisionText.trim()} onClick={() => void decide("approve_with_edits")} title={!decisionText.trim() ? "Decision notes are required." : missingRequired ? "Complete the required structured fields first." : undefined}>Approve with edits</button>
        <button className="btn btn--danger btn--sm" disabled={!pending || busy || missingRequired || !decisionText.trim()} onClick={() => void decide("reject")} title={!decisionText.trim() ? "Feedback is required." : missingRequired ? "Complete the required structured fields first." : undefined}>Reject with feedback</button>
        <button className="btn btn--danger btn--sm" disabled={!pending || busy || missingRequired} onClick={() => void decide("abort")} title={missingRequired ? "Complete the required structured fields first." : undefined}>Abort run</button>
      </div>
    </> : <p className="tiny muted">Guest access is read-only.</p>}
    <Toasts toasts={toasts} />
  </div>;
}

function CheckpointField({ spec, value, onChange, disabled }: { spec: CheckpointFieldSpec; value: unknown; onChange: (v: unknown) => void; disabled: boolean }) {
  if (spec.type === "boolean") return <label className="checkpoint-field"><input type="checkbox" checked={Boolean(value)} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />{spec.label}{spec.required ? " *" : ""}</label>;
  if (spec.type === "enum") return <Field label={`${spec.label}${spec.required ? " *" : ""}`}><select className="input" disabled={disabled} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)}><option value="">Choose…</option>{(spec.options ?? []).map((option) => <option key={option}>{option}</option>)}</select></Field>;
  if (spec.type === "text") return <Field label={`${spec.label}${spec.required ? " *" : ""}`}><textarea className="input" required={spec.required} disabled={disabled} value={value == null ? "" : String(value)} rows={3} onChange={(e) => onChange(e.target.value)} /></Field>;
  return <Field label={`${spec.label}${spec.required ? " *" : ""}`}><input className="input" type={spec.type === "number" ? "number" : "text"} required={spec.required} disabled={disabled} value={value == null ? "" : String(value)} onChange={(e) => onChange(spec.type === "number" ? (e.target.value === "" ? "" : Number(e.target.value)) : e.target.value)} /></Field>;
}

function fieldHasValue(spec: CheckpointFieldSpec, value: unknown): boolean {
  if (value == null) return false;
  if (typeof value === "string") return value.trim().length > 0;
  if (spec.type === "number") return typeof value === "number" && Number.isFinite(value);
  return true;
}

function CheckpointCard({ checkpoint, step, itemTitle, itemId, itemNumber, decidedByName, onDecided, compact = false }: { checkpoint: Checkpoint; step?: StepRun; itemTitle?: string; itemId?: string; itemNumber?: number; decidedByName?: string; onDecided?: (cp: Checkpoint) => void; compact?: boolean }) {
  const item = checkpoint.item ?? (itemId ? { id: itemId, number: itemNumber ?? 0, title: itemTitle ?? "Work item" } : null);
  const question = checkpoint.question?.trim() || step?.name || "Decision requested";
  const brief = checkpoint.summary_md?.trim() || step?.result?.summary?.trim() || "";
  const artifacts = checkpoint.artifacts?.length ? checkpoint.artifacts : step?.result?.artifacts ?? [];
  return <article className={`checkpoint-card panel ${compact ? "checkpoint-card--monitor" : ""}`}>
    <header className="checkpoint-card__head">
      <div className="checkpoint-card__title">
        <div className="checkpoint-auditline"><span>{step?.name ?? "Checkpoint"}</span><code>{checkpoint.node_id}</code><span>lap {checkpoint.lap}</span></div>
        <h3>{item ? <Link href={`/items/${item.id}`} className="checkpoint-item-link">{item.number ? `#${item.number} · ` : ""}{item.title}</Link> : <span>Work item details unavailable</span>}</h3>
        <p className="checkpoint-question">{question}</p>
        {checkpoint.remind_after ? <p className="checkpoint-reminder">Reminder set for {new Date(checkpoint.remind_after).toLocaleString()} · this will never approve the work automatically.</p> : null}
      </div>
      <StateChip state={checkpoint.state} />
    </header>
    <section className="checkpoint-brief" aria-label="Checkpoint brief">
      <div className="checkpoint-section-label"><span>Review brief</span><span className="tiny muted">Work so far · agent assessment · proposed change</span></div>
      {brief ? <div className="checkpoint-summary msg__body">{renderMarkdown(brief)}</div> : <p className="checkpoint-unavailable">The brief is not available yet.</p>}
    </section>
    {artifacts.length ? <Artifacts artifacts={artifacts} /> : null}
    {checkpoint.state === "pending" ? <CheckpointActions checkpoint={checkpoint} onDecided={onDecided} compact={compact} /> : <>
      {checkpoint.fields_spec?.length ? <div className="checkpoint-field-values"><h4>Submitted fields</h4><dl>{checkpoint.fields_spec.map((spec) => <div key={spec.key}><dt>{spec.label}</dt><dd>{formatFieldValue(checkpoint.fields?.[spec.key])}</dd></div>)}</dl></div> : null}
      <div className="checkpoint-decision-audit"><strong>{checkpoint.state.replaceAll("_", " ")}</strong><span>{checkpoint.decided_by_kind === "human" ? `Decided by ${decidedByName ?? "human"}` : checkpoint.decided_by_kind ? `Decided by ${checkpoint.decided_by_kind}` : "Decision recorded"}{checkpoint.decided_by && !decidedByName ? ` · ${checkpoint.decided_by.slice(0, 12)}` : ""}</span><time>{checkpoint.decided_at ? new Date(checkpoint.decided_at).toLocaleString() : "Time unavailable"}</time>{checkpoint.decision_md ? <p>{checkpoint.decision_md}</p> : null}</div>
    </>}
  </article>;
}

function formatFieldValue(value: unknown): string {
  if (value == null || value === "") return "Not provided";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  return String(value);
}

function Artifacts({ artifacts }: { artifacts: unknown[] }) {
  const rows = artifacts.map((artifact, index) => normalizeArtifact(artifact, index)).filter((row): row is NormalizedArtifact => Boolean(row));
  if (!rows.length) return null;
  return <section className="checkpoint-artifacts" aria-label="Checkpoint artifacts"><div className="checkpoint-section-label"><span>Artifacts</span><span className="tiny muted">Evidence to review</span></div><ul>{rows.map((artifact) => <li key={artifact.key}>
    <span className="artifact-kind">{artifact.kind.replaceAll("_", " ")}</span>
    <div className="artifact-detail">{artifact.uri ? <a href={artifact.uri} target="_blank" rel="noreferrer noopener">{artifact.linkLabel || artifact.uri}<span aria-hidden> ↗</span></a> : artifact.headline ? <strong>{artifact.headline}</strong> : null}{artifact.details.map(([label, value]) => <span key={label}><b>{label}</b> {value}</span>)}</div>
  </li>)}</ul></section>;
}

interface NormalizedArtifact { key: string; kind: string; uri?: string; linkLabel?: string; headline?: string; details: [string, string][]; }
function normalizeArtifact(value: unknown, index: number): NormalizedArtifact | null {
  if (typeof value === "string") return { key: `legacy-${index}`, kind: "artifact", headline: value, details: [] };
  if (!value || typeof value !== "object") return null;
  const artifact = value as Partial<CheckpointArtifact> & Record<string, unknown>;
  const kind = typeof artifact.kind === "string" ? artifact.kind : "artifact";
  const meta = artifact.meta;
  const entries = typeof meta === "string" ? [["detail", meta] as [string, string]] : meta && typeof meta === "object" ? Object.entries(meta).flatMap(([key, entry]) => {
    if (entry == null || entry === "") return [];
    const text = typeof entry === "string" ? entry : typeof entry === "number" || typeof entry === "boolean" ? String(entry) : JSON.stringify(entry);
    return [[key.replaceAll("_", " "), text] as [string, string]];
  }) : [];
  const headlineEntry = entries.find(([key]) => ["subject", "title", "name", "branch", "sha"].includes(key.toLowerCase()));
  const uri = typeof artifact.uri === "string" && artifact.uri.trim() ? artifact.uri : undefined;
  const details = entries.filter(([key]) => key !== headlineEntry?.[0]);
  return { key: `${kind}-${index}`, kind, ...(uri ? { uri } : {}), ...(kind === "pr" && uri ? { linkLabel: headlineEntry?.[1] || "Open pull request" } : {}), ...(headlineEntry ? { headline: headlineEntry[1] } : {}), details };
}

export function CheckpointInbox() {
  const pending = useAsync(() => api.listCheckpoints("pending", 50), []);
  const decided = useAsync(() => api.listCheckpoints("decided", 100), []);
  const members = useAsync(() => api.listMembers(), []);
  const [details, setDetails] = useState<Record<string, { step?: StepRun; itemTitle?: string; itemId?: string; itemNumber?: number }>>({});
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  useEffect(() => {
    const checkpoints = [...(pending.data?.checkpoints ?? []), ...(decided.data?.checkpoints ?? [])];
    let live = true;
    void Promise.all(checkpoints.map(async (cp) => {
      try {
        const runDetail = await api.getRun(cp.run_id);
        const item = cp.item ? null : await api.getItem(runDetail.run.item_id).catch(() => null);
        const detail = { step: runDetail.steps.find((s) => s.id === cp.step_run_id), ...(item ? { itemTitle: item.item.title, itemId: item.item.id, itemNumber: item.item.number } : {}) };
        if (live) setDetails((all) => ({ ...all, [cp.id]: detail }));
      } catch { /* cards remain usable with checkpoint's own summary */ }
    }));
    return () => { live = false; };
  }, [pending.data, decided.data]);
  const pendingSorted = [...(pending.data?.checkpoints ?? [])].sort((a, b) => a.created_at && b.created_at ? Date.parse(a.created_at) - Date.parse(b.created_at) : 0);
  const decidedByRun = useMemo(() => {
    const map = new Map<string, Checkpoint[]>();
    (decided.data?.checkpoints ?? []).forEach((cp) => map.set(cp.run_id, [...(map.get(cp.run_id) ?? []), cp]));
    return map;
  }, [decided.data]);
  if (pending.loading && !pending.data) return <div className="run-stack"><Skeleton h={160} /><Skeleton h={160} /></div>;
  if (pending.error && !pending.data) return <ErrorBlock title="Checkpoint inbox is not available yet" message={pending.error.message} retry={() => void pending.reload()} />;
  return <div className="run-stack">
    <div className="pagehead"><div><h1>Checkpoint inbox</h1><div className="pagehead__sub">Human decisions, oldest first. Every lap stays in the audit trail.</div></div><button className="btn btn--sm" onClick={() => { void pending.reload(); void decided.reload(); }}>Refresh</button></div>
    {pendingSorted.length ? pendingSorted.map((cp) => <CheckpointCard key={cp.id} checkpoint={cp} {...details[cp.id]} onDecided={() => { void pending.reload(); void decided.reload(); }} />) : <EmptyState title="No checkpoints waiting" body="When a run needs a human decision, it will appear here." />}
    <section className="section"><header className="section__head"><h2>Decision history</h2><span className="tiny muted">Grouped by run · each lap preserved</span></header><div className="section__body run-stack">
      {[...decidedByRun.entries()].map(([runId, rows]) => <details key={runId} className="audit-run" open={expanded[runId]} onToggle={(e) => {
        const el = (e.currentTarget ?? e.target) as HTMLDetailsElement | null;
        const isOpen = el ? el.open : false;
        setExpanded((all) => ({ ...all, [runId]: isOpen }));
      }}>
        <summary><Link href={`/runs/${runId}`} className="mono" onClick={(e) => e.stopPropagation()}>{runId.slice(0, 12)}</Link><span>{rows.length} decision{rows.length === 1 ? "" : "s"}</span><span className="muted">{rows.map((cp) => `lap ${cp.lap}`).join(" · ")}</span></summary>
        {[...rows].sort((a, b) => a.lap - b.lap).map((cp) => <CheckpointCard key={cp.id} checkpoint={cp} {...details[cp.id]} decidedByName={members.data?.members.find((member) => member.user_id === cp.decided_by)?.name} />)}
      </details>)}
      {!decided.loading && !decided.error && !decidedByRun.size ? <p className="muted tiny">No decisions recorded yet.</p> : null}
      {decided.error ? <ErrorBlock title="Decision history is unavailable" message={decided.error.message} retry={() => void decided.reload()} /> : null}
    </div></section>
  </div>;
}

export function RunMonitor({ runId }: { runId: string }) {
  const { canWrite } = useSession();
  const { toasts, push } = useToasts();
  const detail = useAsync(() => api.getRun(runId), [runId]);
  const agents = useAsync(() => api.listAgents().then((response) => response.agents), []);
  const members = useAsync(() => api.listMembers(), []);
  const tenantVariables = useAsync(() => api.getTenantVariables().then((response) => response.variables), []);
  const item = useAsync(async () => {
    if (!detail.data) return null;
    return (await api.getItem(detail.data.run.item_id)).item;
  }, [detail.data?.run.item_id]);
  const templates = useAsync(() => api.listWorkflowTemplates(), []);
  const [run, setRun] = useState<Run | null>(null);
  const [steps, setSteps] = useState<StepRun[]>([]);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [activity, setActivity] = useState<{ id: string; at: string; text: string; kind: "step" | "log" | "checkpoint" }[]>([]);
  const [checkpoints, setCheckpoints] = useState<Checkpoint[]>([]);
  const [eventsError, setEventsError] = useState("");
  const [selected, setSelected] = useState("");
  const [interventions, setInterventions] = useState<Intervention[]>([]);
  const [instruction, setInstruction] = useState("");
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);
  const stepStates = useRef(new Map<string, string>());
  const seenStepEvents = useRef(new Set<string>());
  const addActivity = (entry: { id: string; at: string; text: string; kind: "step" | "log" | "checkpoint" }) => setActivity((all) => [entry, ...all.filter((row) => row.id !== entry.id)].slice(0, 120));
  useEffect(() => {
    if (!detail.data) return;
    setRun(detail.data.run); setSteps(detail.data.steps);
    stepStates.current = new Map(detail.data.steps.map((step) => [step.id, step.state]));
    if (!selected && detail.data.steps[0]) setSelected((detail.data.steps.find((step) => step.state === "waiting") ?? detail.data.steps[0]).id);
  }, [detail.data]);
  useEffect(() => {
    void api.listInterventions(runId).then((r) => setInterventions(r.interventions)).catch(() => undefined);
  }, [runId]);
  useEffect(() => {
    let live = true;
    void Promise.all([api.listCheckpoints("pending", 100), api.listCheckpoints("decided", 100)]).then(([pending, decided]) => {
      if (live) setCheckpoints((current) => {
        const rows = [...pending.checkpoints, ...decided.checkpoints].filter((cp) => cp.run_id === runId);
        const merged = new Map(current.filter((cp) => cp.run_id === runId).map((cp) => [cp.id, cp]));
        rows.forEach((cp) => merged.set(cp.id, cp));
        return [...merged.values()];
      });
    }).catch(() => undefined);
    return () => { live = false; };
  }, [runId]);
  useEffect(() => {
    if (!run || ["completed", "failed", "cancelled"].includes(run.state)) return;
    let closed = false; let timer: ReturnType<typeof setTimeout> | undefined; let failures = 0;
    const startPoll = () => {
      const poll = async () => {
        if (closed) return;
        try {
          const [logResult, runResult] = await Promise.all([api.runLogs(runId, seq.current), api.getRun(runId)]);
          logResult.logs.forEach((log) => { seq.current = Math.max(seq.current, log.seq); addActivity({ id: `log-${log.seq}`, at: log.ts ?? new Date().toISOString(), text: log.message, kind: "log" }); });
          runResult.steps.forEach((step) => {
            const prior = stepStates.current.get(step.id);
            if (prior && prior !== step.state) {
              const eventKey = `${step.id}:${step.state}`;
              if (!seenStepEvents.current.has(eventKey)) addActivity({ id: `step-${eventKey}`, at: new Date().toISOString(), text: `${step.name} · ${prior} → ${step.state}`, kind: "step" });
              seenStepEvents.current.add(eventKey);
            }
            stepStates.current.set(step.id, step.state);
          });
          setLogs((all) => mergeLogs(all, logResult.logs)); setRun(runResult.run); setSteps(runResult.steps); failures = 0;
          if (!["completed", "failed", "cancelled"].includes(runResult.run.state)) timer = setTimeout(() => void poll(), 1500);
        } catch (error) {
          failures += 1;
          if (failures >= 4) setEventsError(`${error instanceof ApiError ? error.code + " — " : ""}${apiErrorMessage(error)}`);
          else timer = setTimeout(() => void poll(), 1500);
        }
      };
      void poll();
    };
    const close = openRunEvents(runId, seq.current, {
      onStep: (step) => {
        const prior = stepStates.current.get(step.id);
        if (prior !== step.state) {
          const eventKey = `${step.id}:${step.state}`;
          if (!seenStepEvents.current.has(eventKey)) addActivity({ id: `step-${eventKey}`, at: step.finished_at ?? step.started_at ?? new Date().toISOString(), text: `${step.name} · ${prior ?? "created"} → ${step.state}`, kind: "step" });
          seenStepEvents.current.add(eventKey);
        }
        stepStates.current.set(step.id, step.state);
        setSteps((current) => [...current.filter((s) => s.id !== step.id), step]);
      },
      onLog: (entry) => { seq.current = Math.max(seq.current, entry.seq); addActivity({ id: `log-${entry.seq}`, at: entry.ts ?? new Date().toISOString(), text: entry.message, kind: "log" }); setLogs((current) => mergeLogs(current, [entry])); },
      onCheckpoint: (event) => { addActivity({ id: `checkpoint-${event.id}-${event.state}`, at: event.decided_at ?? new Date().toISOString(), text: `Checkpoint ${event.node_id} · ${event.state}${event.decision_md ? ` — ${event.decision_md}` : ""}`, kind: "checkpoint" }); setCheckpoints((current) => [event, ...current.filter((cp) => cp.id !== event.id)]); },
      onRun: setRun,
      onDone: () => { void api.getRun(runId).then((r) => { setRun(r.run); setSteps(r.steps); }).catch(() => undefined); },
      onError: () => { setEventsError("Live updates disconnected; reconnecting by polling run logs."); startPoll(); },
    });
    return () => { closed = true; close(); if (timer) clearTimeout(timer); };
  }, [runId, run?.state]);

  const graph = useMemo(() => graphFromRun(run ?? detail.data?.run), [run, detail.data]);
  const activeStep = steps.find((step) => step.id === selected) ?? null;
  const selectedNode = graph?.nodes.find((node) => node.id === activeStep?.node_id);
  const configuredRunner = String(selectedNode?.config?.runner ?? "");
  const selectedAgent = agents.data?.find((agent) => agent.id === String(selectedNode?.config?.agent_id ?? ""));
  const chainDefault = selectedAgent?.runners?.[0]?.name ?? "";
  const configuredRunnerName = configuredRunner.startsWith("$")
    ? (tenantVariables.data?.[configuredRunner.slice(1)] && tenantVariables.data[configuredRunner.slice(1)] !== activeStep?.result?.runner
      ? tenantVariables.data[configuredRunner.slice(1)] : chainDefault)
    : configuredRunner || chainDefault;
  const switchedForBudget = Boolean(activeStep?.result?.runner && configuredRunnerName && configuredRunnerName !== activeStep.result.runner && activeStep.result.runners_tried?.includes(activeStep.result.runner));
  const checkpoint = checkpoints.find((cp) => cp.step_run_id === activeStep?.id && cp.state === "pending");
  const canRunIntervention = Boolean(canWrite && activeStep && activeStep.state !== "cancelled" && activeStep.state !== "superseded" && !["succeeded", "failed", "skipped", "blocked"].includes(activeStep.state));
  const terminal = Boolean(run && ["completed", "failed", "cancelled"].includes(run.state));
  async function intervention(kind: Intervention["kind"]) {
    if (!run || !activeStep) return;
    setBusy(true);
    try {
      const result = await api.intervene(run.id, { kind, node_id: activeStep.node_id, step_run_id: activeStep.id, ...(instruction.trim() ? { body_md: instruction.trim() } : {}) });
      setInterventions((all) => [...all, result.intervention].sort((a, b) => a.seq - b.seq)); setInstruction("");
      push("info", `Intervention #${result.intervention.seq} recorded. Terminal executions are never resurrected.`);
    } catch (error) { push("error", `${error instanceof ApiError ? error.code + " — " : ""}${apiErrorMessage(error)}`); }
    finally { setBusy(false); }
  }
  async function cancelRun() {
    if (!run) return;
    setBusy(true);
    try { const response = await api.cancelRun(run.id); setRun(response.run); push("info", "Run cancellation requested."); }
    catch (error) { push("error", `${error instanceof ApiError ? error.code + " — " : ""}${apiErrorMessage(error)}`); }
    finally { setBusy(false); }
  }
  if (detail.loading && !detail.data) return <div className="run-stack"><Skeleton h={34} w="45%" /><Skeleton h={360} /></div>;
  if (detail.error && !detail.data) return <ErrorBlock title="Run monitor is not available yet" message={`${detail.error instanceof ApiError ? detail.error.code + " — " : ""}${detail.error.message}`} retry={() => void detail.reload()} />;
  if (!run) return null;
  return <div className="run-stack">
    <div className="pagehead run-pagehead"><div><Link href="/runs" className="tiny muted">← All runs</Link><h1>{item.data?.title ?? `Run ${run.id.slice(0, 10)}`}</h1><div className="pagehead__sub"><span className="mono">{run.id}</span> · {templates.data?.templates.find((t) => t.id === run.template_id)?.name ?? `Template ${run.template_id.slice(0, 8)}`} v{run.template_version} · started {run.started_at ? new Date(run.started_at).toLocaleString() : "—"}</div></div><div className="run-head-actions"><StateChip state={run.state} /><span className="mono">{formatElapsed(run.started_at, run.finished_at)}</span><button className="btn btn--danger btn--sm" disabled={!canWrite || terminal || busy} onClick={() => void cancelRun()}>Cancel run</button></div></div>
     {item.error ? <p className="tiny muted">Item details are unavailable: {item.error.message}</p> : null}
     {templates.error ? <p className="tiny muted">Template metadata unavailable; using the run snapshot where possible.</p> : null}
     <section className="section run-variables-card"><header className="section__head"><h2>Run variables</h2><span className="tiny muted">Tenant defaults plus run overrides</span></header><div className="section__body">{Object.keys(run.variables ?? {}).length ? <div className="tenant-variable-list">{Object.entries(run.variables ?? {}).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => <div key={name}><code>${name}</code><span>{value}</span></div>)}</div> : <p className="tiny muted">This run has no variables. Tenant defaults may be empty.</p>}<p className="tiny muted tenant-vars-note">When a runner is over budget, the engine updates the run’s variable to the next runner in that agent’s chain.</p></div></section>
    <div className="monitor-grid">
      <section className="section graph-section"><header className="section__head"><h2>Execution graph</h2><span className="tiny muted">Select a node to inspect receipts, output and context</span></header><div className="section__body"><RunGraph graph={graph} steps={steps} selected={activeStep?.id} onSelect={(step) => setSelected(step.id)} /><div className="graph-legend"><span><i style={{ background: EDGE_COLOR.delivered }} />Delivered</span><span><i style={{ background: EDGE_COLOR.skipped }} />Skipped</span><span><i style={{ background: EDGE_COLOR.dead }} />Dead</span></div></div></section>
      <section className="section stream-section"><header className="section__head"><h2>Live event stream</h2><span className="tiny muted">{eventsError ? "Polling fallback" : "Live"}</span></header>{eventsError ? <div className="stream-warning">{eventsError}</div> : null}<ol className="timeline run-timeline">{activity.map((event) => <li key={event.id}><div className="tiny muted mono">{new Date(event.at).toLocaleTimeString()} · {event.kind}</div>{event.kind === "step" ? <button className="link-button" onClick={() => { const step = steps.find((s) => event.text.startsWith(s.name)); if (step) setSelected(step.id); }}>{event.text}</button> : <div className="run-log-message">{event.text}</div>}</li>)}{!activity.length ? <li className="muted tiny">Waiting for the first run event…</li> : null}</ol></section>
    </div>
    <div className="monitor-grid monitor-grid--lower">
      <section className="section inspector-section"><header className="section__head"><h2>Step inspector</h2>{activeStep ? <StateChip state={activeStep.state} /> : null}</header><div className="section__body">{activeStep ? <>
        <div className="inspector-title"><div><span className="tiny muted">{activeStep.kind} · {activeStep.node_id}</span><h3>{activeStep.name}</h3></div><span className="tiny muted">Execution {activeStep.run_index ?? 0}</span></div>
         {checkpoint ? <CheckpointCard checkpoint={checkpoint} step={activeStep} itemTitle={item.data?.title} itemId={item.data?.id} itemNumber={item.data?.number} decidedByName={members.data?.members.find((member) => member.user_id === checkpoint.decided_by)?.name} onDecided={(next) => setCheckpoints((all) => all.map((cp) => cp.id === next.id ? next : cp))} compact /> : null}
         <div className="step-runner-info"><div><span>Runner</span><strong>{activeStep.result?.runner ?? "Not reported"}</strong></div>{activeStep.result?.runners_tried?.length ? <div><span>Runners tried</span><strong>{activeStep.result.runners_tried.join(" → ")}</strong></div> : null}</div>
         {switchedForBudget ? <p className="runner-switch-note">Switched to {activeStep.result?.runner} — the {configuredRunnerName} runner’s budget was spent.</p> : null}
        <h4>Incoming receipts</h4>{activeStep.input_edges && Object.keys(activeStep.input_edges).length ? <div className="receipt-list">{Object.entries(activeStep.input_edges).map(([edge, receipt]) => <div className="receipt-row" key={edge}><code>{edge}</code><StateChip state={receipt.state} /><code>{receipt.port ?? "—"}</code><pre>{JSON.stringify(receipt.value ?? null, null, 2)}</pre></div>)}</div> : <p className="tiny muted">No incoming receipts.</p>}
        <div className="result-grid"><div><span>Status</span><strong>{activeStep.result?.status ?? activeStep.state}</strong></div><div><span>Exit reason</span><strong>{activeStep.result?.exit_reason ?? "—"}</strong></div><div><span>Tokens</span><strong>{activeStep.result?.tokens ? `${activeStep.result.tokens.in ?? 0} in / ${activeStep.result.tokens.out ?? 0} out` : "—"}</strong></div><div><span>Cost</span><strong>{activeStep.result?.cost_usd == null ? "—" : `$${activeStep.result.cost_usd.toFixed(4)}`}</strong></div><div><span>Failure kind</span><strong>{activeStep.failure_kind ?? activeStep.result?.failure_kind ?? "—"}</strong></div></div>
        {activeStep.result?.summary ? <div className="inspector-summary"><h4>Summary</h4><p>{activeStep.result.summary}</p></div> : null}
         {!checkpoint && activeStep.result?.artifacts?.length ? <Artifacts artifacts={activeStep.result.artifacts} /> : null}
        <Manifest title="Memory manifest" entries={activeStep.memory_manifest ?? []} /><Manifest title="Context manifest" entries={activeStep.context_manifest ?? []} />
        <details className="raw-tail"><summary>Raw log tail ({logs.filter((log) => log.step_run_id === activeStep.id).length})</summary><pre>{logs.filter((log) => log.step_run_id === activeStep.id).slice(-80).map((log) => `[${log.seq}] ${log.message}`).join("\n") || "No log lines for this step."}</pre></details>
        {canWrite ? <div className="intervention-box"><h4>Interventions</h4><p className="tiny muted">Ordered per execution. A terminal step stays terminal; interventions never bring it back.</p><label className="field"><span>Instruction (optional for redo)</span><textarea className="input" rows={2} value={instruction} onChange={(e) => setInstruction(e.target.value)} disabled={!canRunIntervention} placeholder={activeStep.state === "running" ? "Give the running step new direction…" : "Add context before redoing this node…"} /></label><div className="run-actions"><button className="btn btn--sm" disabled={!canWrite || !activeStep || activeStep.state !== "running" || busy || terminal} onClick={() => void intervention("inject")}>Inject instruction</button><button className="btn btn--sm" disabled={!canRunIntervention || busy || terminal} onClick={() => void intervention("redo")}>Redo node</button><button className="btn btn--danger btn--sm" disabled={!canRunIntervention || busy || terminal} onClick={() => void intervention("stop")}>Stop step</button></div></div> : <p className="tiny muted">Guest access is read-only.</p>}
      </> : <EmptyState title="Select a step" body="Choose a node in the graph to inspect its receipts and result." />}</div></section>
      <aside className="section intervention-history"><header className="section__head"><h2>Run activity</h2><span className="tiny muted">{checkpoints.length} checkpoint events</span></header><div className="section__body run-stack"><h4>Intervention order</h4>{interventions.length ? [...interventions].sort((a, b) => a.seq - b.seq).map((action) => <div className="intervention-row" key={action.id}><span className="mono">#{action.seq}</span><strong>{action.kind}</strong><span className="muted">{action.body_md || action.node_id || "run"}</span><span className="tiny muted">{action.delivered_at ? "delivered" : "queued"}</span></div>) : <p className="tiny muted">No interventions recorded.</p>}
        <h4>Checkpoint events</h4>{checkpoints.map((cp) => <div className="intervention-row" key={cp.id}><span className="mono">lap {cp.lap}</span><StateChip state={cp.state} /><span>{cp.decision_md || cp.summary_md || cp.node_id}</span></div>)}<Link href="/checkpoints" className="rowlink tiny">Open checkpoint inbox →</Link>
      </div></aside>
    </div><Toasts toasts={toasts} />
  </div>;
}

function Manifest({ title, entries }: { title: string; entries: unknown[] }) {
  return <details className="manifest"><summary>{title} <span className="muted">{entries.length}</span></summary>{entries.length ? <pre>{JSON.stringify(entries, null, 2)}</pre> : <p className="tiny muted">No captured entries.</p>}</details>;
}
function mergeLogs(existing: LogEntry[], incoming: LogEntry[]) {
  const bySeq = new Map(existing.map((entry) => [entry.seq, entry])); incoming.forEach((entry) => bySeq.set(entry.seq, entry));
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq).slice(-500);
}
