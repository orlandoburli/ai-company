"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type Agent, type Runner, type WorkflowEdge, type WorkflowGraph, type WorkflowNode, type WorkflowProblem, type WorkflowTemplate, type WorkflowTemplateInput, type WorkflowTemplateSummary } from "@/lib/api";
import { useConfirm } from "@/lib/confirm";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import type { Item, Product, Project } from "@/lib/types";
import { apiErrorMessage, EmptyState, ErrorBlock, Field, Skeleton, Toasts } from "@/components/primitives";

/** A kind's palette entry: how it is named, what it does, and its ports. */
type KindMeta = {
  title: string;
  caption: string;
  input: string[];
  output: string[];
  glyph: string;
  /** "ends" are the flow's boundaries; "work" are the steps that do the work. */
  group: "ends" | "work";
  defaultName: string;
  note?: string;
  hint?: string;
};

// The flow's boundaries come first, then the work. A start has no input and a
// single "main" output; an end has a single "in" input and no output.
const KIND_META: Record<string, KindMeta> = {
  start: { title: "Start", caption: "Where the flow begins", input: [], output: ["main"], glyph: "▶", group: "ends", defaultName: "Start", note: "The flow begins here.", hint: "The engine passes straight through this node; it is never dispatched as work." },
  end: { title: "End", caption: "Where it finishes", input: ["in"], output: [], glyph: "■", group: "ends", defaultName: "End", note: "Reaching this ends the run.", hint: "This node has no outputs, so nothing follows it." },
  agent: { title: "Agent", caption: "Delegated work", input: ["in"], output: ["main", "error"], glyph: "✦", group: "work", defaultName: "agent" },
  integration: { title: "Integration", caption: "A connected service", input: ["in"], output: ["main", "error"], glyph: "⌁", group: "work", defaultName: "integration" },
  checkpoint: { title: "Checkpoint", caption: "Human decision", input: ["in"], output: ["approved", "rejected"], glyph: "◇", group: "work", defaultName: "checkpoint" },
  loop: { title: "Loop", caption: "Bounded repetition", input: ["in"], output: ["body", "done"], glyph: "↻", group: "work", defaultName: "loop" },
  merge: { title: "Merge", caption: "Join branches", input: ["in"], output: ["main", "error"], glyph: "⋈", group: "work", defaultName: "merge" },
};
const PALETTE_GROUPS: { key: "ends" | "work"; label: string }[] = [
  { key: "ends", label: "Flow ends" },
  { key: "work", label: "Work" },
];

/** One integration type: the name the flow carries, the glyph that marks it,
 *  and the form the inspector shows for it. Each type is its own entry in the
 *  palette and its own form — the node it creates is an integration node whose
 *  type is the contract the server reads. */
type IntegrationMeta = {
  title: string;
  caption: string;
  glyph: string;
  form: "open_pr" | "wait_ci" | "comment" | "fetch_issue";
};

const INTEGRATION_META: Record<string, IntegrationMeta> = {
  "github.open_pr": { title: "Open pull request", caption: "raises a pull request", glyph: "⇧", form: "open_pr" },
  "github.wait_ci": { title: "Wait for CI", caption: "waits for the checks to finish", glyph: "◷", form: "wait_ci" },
  "github.comment": { title: "Comment on a pull request", caption: "leaves a review comment", glyph: "✎", form: "comment" },
  "github.fetch_issue": { title: "Fetch an issue", caption: "reads an issue from GitHub", glyph: "☰", form: "fetch_issue" },
};

/** The integrations the engine knows that take no settings of their own. */
const BUILTIN_INTEGRATIONS: Record<string, string> = {
  "item.mark_done": "Close the item",
};

/** The friendly name of an integration node, whatever its type. */
function integrationName(type: string | undefined): string {
  if (!type) return "Integration";
  return INTEGRATION_META[type]?.title ?? BUILTIN_INTEGRATIONS[type] ?? type;
}

/** A palette entry: what the flow is made of. */
type PaletteEntry = {
  key: string;
  kind: string;
  /** Set for the integration entries: the type of the node they add. */
  type?: string;
  title: string;
  caption: string;
  glyph: string;
  group: "ends" | "work";
};

function paletteEntry(kind: string, type?: string): PaletteEntry {
  if (kind === "integration" && type) {
    const meta = INTEGRATION_META[type];
    return { key: `integration:${type}`, kind, type, title: meta.title, caption: meta.caption, glyph: meta.glyph, group: "work" };
  }
  const meta = KIND_META[kind];
  return { key: kind, kind, title: meta.title, caption: meta.caption, glyph: meta.glyph, group: meta.group };
}

/** The palette reads as the things a flow is made of: where it begins, where
 *  it ends, and the steps in between. */
const PALETTE_ENTRIES: PaletteEntry[] = [
  paletteEntry("start"),
  paletteEntry("end"),
  paletteEntry("agent"),
  paletteEntry("integration", "github.open_pr"),
  paletteEntry("integration", "github.wait_ci"),
  paletteEntry("integration", "github.comment"),
  paletteEntry("integration", "github.fetch_issue"),
  paletteEntry("checkpoint"),
  paletteEntry("loop"),
  paletteEntry("merge"),
];

const PORT_ROW = 24;
const CARD_W = 252;
const NODE_H = 118;
/** A terminator is a pill, not a card: its width and height are fixed so its
 *  ports can be anchored to the canvas geometry. */
const TERMINATOR_W = 148;
const TERMINATOR_H = 40;
const GAP_X = 120;
const GAP_Y = 48;
const BASE_X = 3200;
const BASE_Y = 2500;
const OUTPUT_TYPES = ["string", "number", "integer", "boolean", "object", "array"] as const;
const ERROR_POLICIES = ["stop_workflow", "continue_regular_output", "continue_error_output"];
const WORKSPACES = ["scratch", "worktree", "dir"];

/** Lay nodes out by their incoming graph edges without changing placed nodes. */
export function layoutGraph(graph: WorkflowGraph, ignorePositions = false): Record<string, { x: number; y: number }> {
  const nodes = graph.nodes;
  const indexById = new Map(nodes.map((node, index) => [node.id, index]));
  const predecessors = nodes.map(() => new Set<number>());
  const connected = new Set<number>();
  for (const edge of graph.edges) {
    const from = indexById.get(edge.from.node);
    const to = indexById.get(edge.to.node);
    if (from === undefined || to === undefined) continue;
    predecessors[to].add(from);
    connected.add(from);
    connected.add(to);
  }
  const incoming = predecessors.map((set) => [...set]);

  // Break cycles at the latest node in each discovered cycle. It becomes the
  // start of that loop; its incoming loop edges no longer hold up ranking.
  for (;;) {
    const state = nodes.map(() => 0);
    const pathIndex = nodes.map(() => -1);
    let cycle: number[] | null = null;
    for (let start = 0; start < nodes.length && !cycle; start += 1) {
      if (state[start]) continue;
      const stack = [start];
      const nextPredecessor = [0];
      state[start] = 1;
      pathIndex[start] = 0;
      while (stack.length && !cycle) {
        const current = stack[stack.length - 1];
        const next = nextPredecessor[nextPredecessor.length - 1];
        if (next >= incoming[current].length) {
          state[current] = 2;
          pathIndex[current] = -1;
          stack.pop();
          nextPredecessor.pop();
          continue;
        }
        const predecessor = incoming[current][next];
        nextPredecessor[nextPredecessor.length - 1] += 1;
        if (state[predecessor] === 0) {
          state[predecessor] = 1;
          pathIndex[predecessor] = stack.length;
          stack.push(predecessor);
          nextPredecessor.push(0);
        } else if (state[predecessor] === 1) {
          cycle = stack.slice(pathIndex[predecessor]);
        }
      }
    }
    if (!cycle) break;
    const cycleNodes = new Set(cycle);
    const breakAt = cycle.reduce((latest, index) => Math.max(latest, index), -1);
    incoming[breakAt] = incoming[breakAt].filter((predecessor) => !cycleNodes.has(predecessor));
  }

  const outgoing = nodes.map(() => [] as number[]);
  const remainingIncoming = incoming.map((list) => list.length);
  incoming.forEach((list, nodeIndex) => list.forEach((predecessor) => outgoing[predecessor].push(nodeIndex)));
  const ranks = nodes.map(() => 0);
  const ready = nodes.map((_, index) => index).filter((index) => remainingIncoming[index] === 0);
  for (let cursor = 0; cursor < ready.length; cursor += 1) {
    const predecessor = ready[cursor];
    for (const next of outgoing[predecessor]) {
      ranks[next] = Math.max(ranks[next], ranks[predecessor] + 1);
      remainingIncoming[next] -= 1;
      if (remainingIncoming[next] === 0) ready.push(next);
    }
  }
  const connectedMaxRank = nodes.reduce((rank, _node, index) => connected.has(index) ? Math.max(rank, ranks[index]) : rank, -1);
  let isolatedOffset = 0;
  nodes.forEach((_node, index) => {
    if (!connected.has(index)) ranks[index] = connectedMaxRank + 1 + isolatedOffset++;
  });

  const rankCounts = new Map<number, number>();
  ranks.forEach((rank) => rankCounts.set(rank, (rankCounts.get(rank) ?? 0) + 1));
  const rankOffsets = new Map<number, number>();
  const stepY = NODE_H + GAP_Y;
  const arranged = nodes.map((node, index) => {
    const rank = ranks[index];
    const ordinal = rankOffsets.get(rank) ?? 0;
    rankOffsets.set(rank, ordinal + 1);
    const count = rankCounts.get(rank) ?? 1;
    // A terminator is narrower than a card, so it is centred in its column
    // and the flow still reads as one straight line through the columns.
    const width = nodeWidth(node);
    return { x: rank * (CARD_W + GAP_X) + (CARD_W - width) / 2, y: (ordinal - (count - 1) / 2) * stepY };
  });

  const positions: Record<string, { x: number; y: number }> = {};
  const occupied = new Set<string>();
  const movable: number[] = [];
  const pointKey = (point: { x: number; y: number }) => `${point.x},${point.y}`;
  const usable = (position: WorkflowNode["position"] | null | undefined): position is { x: number; y: number } => Boolean(position && Number.isFinite(position.x) && Number.isFinite(position.y));

  // Reserve the first occurrence of each usable, non-origin user position.
  // Later duplicates are the only explicit positions that may be corrected.
  for (let index = 0; index < nodes.length; index += 1) {
    const position = nodes[index].position as WorkflowNode["position"] | null | undefined;
    if (ignorePositions || !usable(position) || (position.x === 0 && position.y === 0)) { movable.push(index); continue; }
    const key = pointKey(position);
    if (occupied.has(key)) movable.push(index);
    else occupied.add(key);
  }

  for (const index of movable) {
    const original = nodes[index].position as WorkflowNode["position"] | null | undefined;
    const originalKey = usable(original) ? pointKey(original) : "";
    const isDuplicate = !ignorePositions && usable(original) && occupied.has(originalKey) && !(original.x === 0 && original.y === 0);
    let point = isDuplicate ? { ...original } : arranged[index];
    while ((point.x === 0 && point.y === 0) || occupied.has(pointKey(point))) point = { x: point.x, y: point.y + stepY };
    occupied.add(pointKey(point));
    positions[nodes[index].id] = point;
  }
  return positions;
}

function applyLayout(graph: WorkflowGraph, positions: Record<string, { x: number; y: number }>): WorkflowGraph {
  if (!Object.keys(positions).length) return graph;
  return { ...graph, nodes: graph.nodes.map((node) => positions[node.id] ? { ...node, position: positions[node.id] } : node) };
}

function asConfig(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}
function makeId(): string {
  const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  let time = Date.now(); let timePart = "";
  for (let i = 0; i < 10; i += 1) { timePart = alphabet[time % 32] + timePart; time = Math.floor(time / 32); }
  let random = "";
  for (let i = 0; i < 16; i += 1) random += alphabet[Math.floor(Math.random() * 32)];
  return timePart + random;
}
function newNode(kind: string, position = { x: 160, y: 120 }, type?: string): WorkflowNode {
  const meta = KIND_META[kind];
  const common = { id: makeId(), kind, name: meta?.defaultName ?? kind, type_version: 1, config: {}, position, on_error: "stop_workflow", always_output_data: false, workspace: "scratch" };
  if (kind === "start") return { ...common, type: "flow.start" };
  if (kind === "end") return { ...common, type: "flow.end" };
  if (kind === "agent") return { ...common, type: "agent.dev", config: { agent_id: "", prompt_tmpl: "", outputs: {} } };
  // An integration node carries the type of the action it performs: the
  // palette entry decides it, and the inspector shows that type's own form.
  if (kind === "integration") {
    const integrationType = type && INTEGRATION_META[type] ? type : "github.open_pr";
    return { ...common, name: INTEGRATION_META[integrationType].title.toLowerCase(), type: integrationType, config: {} };
  }
  if (kind === "checkpoint") return { ...common, type: "checkpoint.approval", config: { policy: "retry_step", max_cycles: 2 } };
  if (kind === "loop") return { ...common, type: "loop.bounded", config: { max_iterations: 3, until: true } };
  return { ...common, type: "merge.all", config: { mode: "all" }, always_output_data: true };
}
/** The repository a remote points at, in the owner/name form the GitHub API
 *  takes. It mirrors the server's own mapping, so the editor and the engine
 *  read the same repository from the same remote. */
function repositoryFromRemote(remote: string | undefined): string {
  const value = (remote ?? "").trim();
  if (!value) return "";
  let path = "";
  const at = value.lastIndexOf("@");
  if (at >= 0) {
    const colon = value.indexOf(":", at + 1);
    if (colon >= 0) path = value.slice(colon + 1);
  } else {
    const match = /^[a-z][a-z0-9+.-]*:\/\/[^/]+(\/.*)$/i.exec(value);
    if (match) path = match[1];
  }
  path = path.replace(/^\/+/, "").replace(/\/+$/, "").replace(/\.git$/, "");
  const parts = path.split("/");
  if (parts.length !== 2 || !parts[0] || !parts[1]) return "";
  return parts[0] + "/" + parts[1];
}
/** The repository a new integration node starts from: the product the
 *  template's items belong to, when the editor knows which one that is. When
 *  it cannot tell, the field stays empty and the server follows the item's
 *  own product at run time. */
function defaultRepository(items: Item[], projects: Project[], products: Product[], appliesTo: string): string {
  const kinds = appliesTo.split(",").map((kind) => kind.trim()).filter(Boolean);
  const eligible = kinds.length ? items.filter((item) => kinds.includes(item.kind)) : items;
  const projectById = new Map(projects.map((project) => [project.id, project]));
  const productById = new Map(products.map((product) => [product.id, product]));
  const found = new Set<string>();
  for (const item of eligible) {
    const project = projectById.get(item.project_id);
    const product = project ? productById.get(project.product_id) : undefined;
    const repo = repositoryFromRemote(product?.repo_url);
    if (repo) found.add(repo);
    if (found.size > 1) return "";
  }
  if (found.size === 1) return [...found][0];
  if (!found.size && products.length === 1) return repositoryFromRemote(products[0].repo_url);
  return "";
}
/** Start and end mark the flow's boundaries; every other kind does work. */
function isTerminator(kind: string): boolean {
  return kind === "start" || kind === "end";
}
function nodeWidth(node: WorkflowNode): number {
  return isTerminator(node.kind) ? TERMINATOR_W : CARD_W;
}
function starterGraph(agentId = "", agentRole = "dev"): WorkflowGraph {
  // The flow says where it begins and where it ends, the way the seeded
  // templates do: a start hands over to the first agent, the end takes the
  // last word.
  const start = newNode("start", { x: 52, y: 140 });
  const first = newNode("agent", { x: 372, y: 140 });
  const gate = newNode("checkpoint", { x: 744, y: 140 });
  const second = newNode("agent", { x: 1116, y: 140 });
  const end = newNode("end", { x: 1540, y: 140 });
  first.name = "review brief"; first.config = { agent_id: agentId, prompt_tmpl: "Review the item and summarize the work needed.", outputs: { findings_md: "string" } };
  gate.name = "approve plan";
  second.name = "implement"; second.config = { agent_id: agentId, prompt_tmpl: "Implement the approved plan.", outputs: {} }; second.workspace = "worktree";
  if (agentId) { first.type = `agent.${agentRole}`; second.type = `agent.${agentRole}`; }
  return { schema_version: 1, nodes: [start, first, gate, second, end], edges: [
    { id: makeId(), from: { node: start.id, port: "main" }, to: { node: first.id, port: "in" } },
    { id: makeId(), from: { node: first.id, port: "main" }, to: { node: gate.id, port: "in" } },
    { id: makeId(), from: { node: gate.id, port: "approved" }, to: { node: second.id, port: "in" } },
    { id: makeId(), from: { node: second.id, port: "main" }, to: { node: end.id, port: "in" } },
  ] };
}
function isPublished(template: WorkflowTemplate | WorkflowTemplateSummary): boolean {
  return Boolean(template.published_at || ("published" in template && template.published) || ("status" in template && template.status === "published"));
}
function errorText(error: unknown): string {
  return `${error instanceof ApiError ? `${error.code} — ` : ""}${apiErrorMessage(error)}`;
}

export function WorkflowsPage() {
  const { me } = useSession();
  const { toasts, push } = useToasts();
  const confirm = useConfirm();
  const router = useRouter();
  const templates = useAsync(() => api.listWorkflowTemplates(), []);
  const agents = useAsync(() => api.listAgents(), []);
  const isManager = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const rows = useMemo(() => [...(templates.data?.templates ?? [])].sort((a, b) => a.name.localeCompare(b.name) || (b.version ?? 0) - (a.version ?? 0)), [templates.data]);

  async function create() {
    try {
      const agent = agents.data?.agents.find((candidate) => candidate.enabled);
      const graph = starterGraph(agent?.id ?? "", agent?.role || "dev");
      const response = await api.createWorkflowTemplate({ name: "Untitled workflow", applies_to: "", max_concurrency: 4, graph });
      router.push(`/workflows/${response.template.id}`);
    } catch (error) { push("error", errorText(error)); }
  }
  async function clone(template: WorkflowTemplateSummary) {
    try {
      const full = await api.getWorkflowTemplate(template.id);
      const response = await api.createWorkflowTemplate({ name: `${template.name} copy`, applies_to: full.template.applies_to ?? "", max_concurrency: full.template.max_concurrency ?? 4, graph: structuredClone(full.template.graph ?? { schema_version: 1, nodes: [], edges: [] }) });
      await templates.reload();
      push("info", "Draft copy created.");
      router.push(`/workflows/${response.template.id}`);
    } catch (error) { push("error", errorText(error)); }
  }
  async function action(template: WorkflowTemplateSummary, kind: "publish" | "version" | "delete") {
    if (kind === "delete" && !await confirm({ title: "Delete this draft?", message: <>Delete the draft template “{template.name}”? This cannot be undone.</>, confirmLabel: "Delete", destructive: true })) return;
    try {
      if (kind === "publish") { await api.publishWorkflowTemplate(template.id); push("info", "Template published and frozen."); }
      if (kind === "version") { const result = await api.createWorkflowVersion(template.id); router.push(`/workflows/${result.template.id}`); return; }
      if (kind === "delete") { await api.deleteWorkflowTemplate(template.id); push("info", "Draft deleted."); }
      await templates.reload();
    } catch (error) { push("error", errorText(error)); }
  }

  return <div className="workflow-list-page">
    <header className="pagehead workflow-list-head"><div><div className="workflow-eyebrow">WORKFLOW STUDIO <span>／</span> TEMPLATE LIBRARY</div><h1>Workflows</h1><div className="pagehead__sub">Versioned graphs that turn an item into coordinated work.</div></div>
      {isManager ? <button className="btn btn--primary" onClick={() => void create()}>＋ New template</button> : <span className="workflow-readonly">Read-only · template changes need an owner or admin</span>}
    </header>
    {templates.loading && !templates.data ? <div className="workflow-list-grid"><Skeleton h={150} /><Skeleton h={150} /><Skeleton h={150} /></div> : templates.error && !templates.data ? <ErrorBlock title="Templates are not available" message={errorText(templates.error)} retry={() => void templates.reload()} /> : rows.length === 0 ? <EmptyState title="No workflow templates yet" body="Build a graph from agents, checkpoints and integrations. A new template starts with a review → approval → implementation path." action={isManager ? <button className="btn btn--primary" onClick={() => void create()}>Create a template</button> : undefined} /> : <div className="workflow-list-grid">{rows.map((template, index) => {
      const published = isPublished(template);
      return <article className="workflow-template-card" key={template.id} style={{ animationDelay: `${index * 45}ms` }}>
        <div className="workflow-template-top"><span className={`workflow-state ${published ? "workflow-state--published" : ""}`}><i />{published ? "Published" : "Draft"}</span><span className="mono tiny">v{template.version ?? 1}</span></div>
        <Link className="workflow-template-name" href={`/workflows/${template.id}`}>{template.name}</Link>
        <div className="workflow-template-meta"><span>{template.applies_to?.trim() ? template.applies_to.split(",").map((part) => part.trim()).filter(Boolean).join(" · ") : "Any item kind"}</span><span>{template.created_at ? new Date(template.created_at).toLocaleDateString() : "Workflow graph"}</span></div>
        <div className="workflow-template-foot"><Link className="btn btn--sm" href={`/workflows/${template.id}`}>{published ? "View graph" : "Open editor"} →</Link>
          <div className="stack workflow-template-actions">
            {isManager ? <>
              {!published ? <><button className="btn btn--sm" onClick={() => router.push(`/workflows/${template.id}`)}>Edit</button><button className="btn btn--sm btn--primary" onClick={() => void action(template, "publish")}>Publish</button><button className="btn btn--sm btn--ghost btn--danger" onClick={() => void action(template, "delete")}>Delete</button></> : <button className="btn btn--sm" title="Published versions are frozen. Create a new version to make changes." onClick={() => void action(template, "version")}>＋ New version</button>}
              <button className="btn btn--sm btn--ghost" onClick={() => void clone(template)}>Duplicate</button>
            </> : <span className="tiny muted">Read-only</span>}
          </div>
        </div>
        {published ? <p className="workflow-frozen-note">Published versions are frozen. Create a new version to make changes.</p> : null}
      </article>;
    })}</div>}
    {templates.error && templates.data ? <ErrorBlock title="Could not refresh templates" message={errorText(templates.error)} retry={() => void templates.reload()} /> : null}
    {agents.error ? <p className="tiny muted">Agent list is unavailable; new agent nodes will need an agent selected after creation.</p> : null}
    <Toasts toasts={toasts} />
  </div>;
}

type Selection = { type: "node" | "edge"; id: string } | null;
type View = { x: number; y: number; scale: number };
type PortDrag = { type: "node"; id: string; startX: number; startY: number; originX: number; originY: number } | { type: "pan"; startX: number; startY: number; originX: number; originY: number } | { type: "connect"; node: string; port: string; point: { x: number; y: number } };

export function WorkflowEditor({ templateId }: { templateId: string }) {
  const { me, canWrite } = useSession();
  const { toasts, push } = useToasts();
  const confirm = useConfirm();
  const router = useRouter();
  const manager = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const query = useAsync(() => api.getWorkflowTemplate(templateId), [templateId]);
  const agentsQ = useAsync(() => api.listAgents(), []);
  const runnersQ = useAsync(() => api.listRunners(), []);
  const variablesQ = useAsync(() => api.getTenantVariables(), []);
  const itemsQ = useAsync(() => api.listItems({ limit: 200 }), []);
  const productsQ = useAsync(() => api.listProducts(), []);
  const projectsQ = useAsync(() => api.listProjects(), []);
  const [template, setTemplate] = useState<WorkflowTemplate | null>(null);
  const [graph, setGraph] = useState<WorkflowGraph>({ schema_version: 1, nodes: [], edges: [] });
  const [saved, setSaved] = useState<WorkflowTemplateInput | null>(null);
  const [selection, setSelection] = useState<Selection>(null);
  const [problems, setProblems] = useState<WorkflowProblem[]>([]);
  const [validation, setValidation] = useState<"loading" | "ready" | "offline">("loading");
  const [validationMessage, setValidationMessage] = useState("");
  const [meta, setMeta] = useState({ name: "", applies_to: "", max_concurrency: 4 });
  const [busy, setBusy] = useState(false);
  const [pageError, setPageError] = useState("");
  const [runPicker, setRunPicker] = useState(false);
  const [runItem, setRunItem] = useState("");
  const [runResult, setRunResult] = useState("");
  const [runVariables, setRunVariables] = useState<[string, string][]>([]);
  const [fitVersion, setFitVersion] = useState(0);
  const dirty = Boolean(saved && (JSON.stringify({ ...meta, graph }) !== JSON.stringify(saved)));
  const published = Boolean(template && isPublished(template));
  const editable = Boolean(manager && !published);
  const agentMap = useMemo(() => new Map((agentsQ.data?.agents ?? []).map((agent) => [agent.id, agent])), [agentsQ.data]);
  const repository = useMemo(() => defaultRepository(itemsQ.data?.items ?? [], projectsQ.data?.projects ?? [], productsQ.data?.products ?? [], meta.applies_to), [itemsQ.data, projectsQ.data, productsQ.data, meta.applies_to]);
  const renderedGraph = useMemo(() => applyLayout(graph, layoutGraph(graph)), [graph]);

  useEffect(() => {
    if (!query.data?.template) return;
    const incoming = query.data.template;
    const nextMeta = { name: incoming.name ?? "", applies_to: incoming.applies_to ?? "", max_concurrency: incoming.max_concurrency ?? 4 };
    const nextGraph = incoming.graph ?? { schema_version: 1, nodes: [], edges: [] };
    const laidOutGraph = applyLayout(nextGraph, layoutGraph(nextGraph));
    setTemplate(incoming); setMeta(nextMeta); setGraph(laidOutGraph);
    setSaved({ ...nextMeta, graph: isPublished(incoming) ? laidOutGraph : nextGraph });
    setFitVersion((version) => version + 1);
    setSelection(null);
  }, [query.data]);

  useEffect(() => {
    if (!dirty) return;
    const beforeUnload = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    // Leaving with unsaved changes asks first, in the app's own dialog: the
    // click is held, and the navigation happens only when the answer is yes.
    const guardLink = (event: MouseEvent) => {
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      const target = event.target as HTMLElement | null;
      const link = target?.closest("a[href]") as HTMLAnchorElement | null;
      if (!link || link.target === "_blank" || link.hasAttribute("download")) return;
      if (link.origin !== window.location.origin || link.pathname === window.location.pathname) return;
      event.preventDefault();
      event.stopPropagation();
      void confirm({
        title: "Leave without saving?",
        message: <>This workflow has unsaved changes. Leaving now discards them.</>,
        confirmLabel: "Leave",
        destructive: true,
      }).then((leave) => {
        if (!leave) return;
        const href = link.getAttribute("href") ?? "";
        if (href.startsWith("/")) router.push(href);
        else window.location.assign(link.href);
      });
    };
    window.addEventListener("beforeunload", beforeUnload);
    document.addEventListener("click", guardLink, true);
    return () => { window.removeEventListener("beforeunload", beforeUnload); document.removeEventListener("click", guardLink, true); };
  }, [dirty, confirm, router]);

  useEffect(() => {
    if (!template || !graph) return;
    setValidation("loading");
    const timer = window.setTimeout(() => {
      void api.validateWorkflowTemplate(graph).then((result) => { setProblems(result.problems ?? []); setValidationMessage(""); setValidation("ready"); }).catch((error: unknown) => { setProblems([]); setValidationMessage(error instanceof ApiError && error.code !== "network" ? `The server returned ${errorText(error)}.` : "The server could not be reached. Graph validation is unavailable, not assumed valid."); setValidation("offline"); });
    }, dirty ? 420 : 0);
    return () => window.clearTimeout(timer);
  }, [graph, template?.id, dirty]);

  const updateNode = useCallback((id: string, update: (node: WorkflowNode) => WorkflowNode) => {
    setGraph((current) => ({ ...current, nodes: current.nodes.map((node) => node.id === id ? update(node) : node) }));
  }, []);
  const updateConfig = useCallback((id: string, patch: Record<string, unknown>) => updateNode(id, (node) => ({ ...node, config: { ...asConfig(node.config), ...patch } })), [updateNode]);
  const addNode = useCallback((kind: string, position: { x: number; y: number }, type?: string) => {
    if (!editable) return;
    const node = newNode(kind, position, type);
    if (kind === "agent") {
      const first = agentsQ.data?.agents.find((agent) => agent.enabled);
      if (first) { node.config = { ...asConfig(node.config), agent_id: first.id }; node.type = `agent.${first.role || "dev"}`; }
    }
    if (kind === "integration" && repository) {
      // The repository the product of the template's items points at, when the
      // editor knows it. The other fields start empty.
      node.config = { ...asConfig(node.config), repo: repository };
    }
    setGraph((current) => ({ ...current, nodes: [...current.nodes, node] })); setSelection({ type: "node", id: node.id });
  }, [agentsQ.data, editable, repository]);
  const deleteSelection = useCallback(() => {
    if (!editable || !selection) return;
    if (selection.type === "node") setGraph((current) => ({ ...current, nodes: current.nodes.filter((node) => node.id !== selection.id), edges: current.edges.filter((edge) => edge.from.node !== selection.id && edge.to.node !== selection.id) }));
    else setGraph((current) => ({ ...current, edges: current.edges.filter((edge) => edge.id !== selection.id) }));
    setSelection(null);
  }, [editable, selection]);

  async function save() {
    if (!template || !editable || busy) return;
    setBusy(true); setPageError("");
    const body: WorkflowTemplateInput = { ...meta, graph };
    try {
      const result = await api.updateWorkflowTemplate(template.id, body);
      const normalized = { name: result.template.name, applies_to: result.template.applies_to ?? "", max_concurrency: result.template.max_concurrency ?? 4, graph: result.template.graph ?? graph };
      setTemplate(result.template); setMeta({ name: normalized.name, applies_to: normalized.applies_to, max_concurrency: normalized.max_concurrency }); setSaved(normalized); setGraph(normalized.graph); push("info", "Workflow saved.");
    } catch (error) { setPageError(errorText(error)); push("error", errorText(error)); }
    finally { setBusy(false); }
  }
  async function publish() {
    if (!template || !manager || published || busy) return;
    setBusy(true); setPageError("");
    try {
      if (dirty) {
        const body: WorkflowTemplateInput = { ...meta, graph };
        const savedResult = await api.updateWorkflowTemplate(template.id, body);
        const normalized = { name: savedResult.template.name, applies_to: savedResult.template.applies_to ?? "", max_concurrency: savedResult.template.max_concurrency ?? 4, graph: savedResult.template.graph ?? graph };
        setTemplate(savedResult.template); setMeta({ name: normalized.name, applies_to: normalized.applies_to, max_concurrency: normalized.max_concurrency }); setSaved(normalized); setGraph(normalized.graph);
      }
      const result = await api.publishWorkflowTemplate(template.id);
      setTemplate(result.template); push("info", "Workflow published and frozen.");
    } catch (error) { setPageError(errorText(error)); push("error", errorText(error)); }
    finally { setBusy(false); }
  }
  async function newVersion() {
    if (!template || !manager || busy) return;
    setBusy(true); setPageError("");
    try { const result = await api.createWorkflowVersion(template.id); router.push(`/workflows/${result.template.id}`); }
    catch (error) { setPageError(errorText(error)); push("error", errorText(error)); setBusy(false); }
  }
  async function deleteDraft() {
    if (!template || !manager || published || busy) return;
    if (!await confirm({ title: "Delete this draft?", message: <>Delete the draft template “{template.name}”? This cannot be undone.</>, confirmLabel: "Delete", destructive: true })) return;
    setBusy(true);
    try { await api.deleteWorkflowTemplate(template.id); router.push("/workflows"); }
    catch (error) { setPageError(errorText(error)); push("error", errorText(error)); setBusy(false); }
  }
  async function startRun() {
    if (!template || !runItem || !canWrite || busy) return;
    setBusy(true); setPageError("");
    const variables = Object.fromEntries(runVariables.map(([key, value]) => [key.trim(), value]).filter(([key]) => key));
    try { const result = await api.startItemRun(runItem, template.id, variables); setRunResult(result.run.id); push("info", "Run started. Real agents may incur usage costs."); }
    catch (error) { setPageError(errorText(error)); push("error", errorText(error)); }
    finally { setBusy(false); }
  }
  function focusProblem(problem: WorkflowProblem) {
    if (problem.node_id) { setSelection({ type: "node", id: problem.node_id }); window.dispatchEvent(new CustomEvent("workflow-focus", { detail: problem.node_id })); }
    if (problem.edge_id) { const edge = graph.edges.find((candidate) => candidate.id === problem.edge_id); setSelection({ type: "edge", id: problem.edge_id }); if (edge) window.dispatchEvent(new CustomEvent("workflow-focus", { detail: edge.to.node })); }
  }
  function arrangeGraph() {
    setGraph((current) => applyLayout(current, layoutGraph(current, true)));
    setFitVersion((version) => version + 1);
  }

  if (query.loading && !query.data) return <div className="run-stack"><Skeleton h={30} w="40%" /><Skeleton h={500} /></div>;
  if (query.error && !query.data) return <ErrorBlock title="Workflow could not be opened" message={errorText(query.error)} retry={() => void query.reload()} />;
  if (!template) return null;
  const selectedNode = selection?.type === "node" ? graph.nodes.find((node) => node.id === selection.id) : undefined;
  const selectedEdge = selection?.type === "edge" ? graph.edges.find((edge) => edge.id === selection.id) : undefined;
  const eligibleItems = (itemsQ.data?.items ?? []).filter((item) => !meta.applies_to.trim() || meta.applies_to.split(",").map((kind) => kind.trim()).filter(Boolean).includes(item.kind));

  return <div className="workflow-editor-page">
    <header className="workflow-editor-head">
      <div className="workflow-editor-title"><Link href="/workflows" className="tiny muted">← Workflows</Link><div className="workflow-title-line"><span className="workflow-mark">↗</span><div><div className="workflow-eyebrow">WORKFLOW STUDIO <span>／</span> GRAPH EDITOR</div><h1>{meta.name || "Untitled workflow"}</h1></div><span className={`workflow-state ${published ? "workflow-state--published" : ""}`}><i />{published ? `Published · v${template.version ?? 1}` : "Draft"}</span>{dirty && !published ? <span className="workflow-unsaved"><i />Unsaved</span> : null}</div>
        <p>Connect named ports to shape how work moves. Canvas position never changes execution order.</p>
      </div>
      <div className="workflow-header-actions">
        {!published && manager ? <button className="btn btn--primary" disabled={busy || !dirty} onClick={() => void save()}>{busy ? "Saving…" : "Save changes"}</button> : null}
        {published && manager ? <button className="btn btn--primary" disabled={busy} onClick={() => void newVersion()}>＋ New version</button> : null}
        {!published && manager ? <button className="btn" disabled={busy} onClick={() => void publish()}>Publish</button> : null}
        {manager && !published ? <button className="btn btn--ghost btn--danger" disabled={busy} onClick={() => void deleteDraft()}>Delete</button> : null}
        {published ? <span className="workflow-lock-note">Frozen · create a new version to edit</span> : null}
      </div>
    </header>
    {pageError ? <div className="error-note workflow-page-error" role="alert">{pageError}</div> : null}
    {!manager ? <div className="workflow-readonly-banner">You can inspect this graph and its validation, but only an owner or admin can edit templates.</div> : null}
    <div className="workflow-meta-bar">
      <Field label="Template name"><input className="input" value={meta.name} disabled={!editable} onChange={(event) => setMeta((current) => ({ ...current, name: event.target.value }))} /></Field>
      <Field label="Applies to"><input className="input" value={meta.applies_to} disabled={!editable} onChange={(event) => setMeta((current) => ({ ...current, applies_to: event.target.value }))} placeholder="Any item kind" /></Field>
      <Field label="Max concurrency"><input className="input" type="number" min={1} max={64} value={meta.max_concurrency} disabled={!editable} onChange={(event) => setMeta((current) => ({ ...current, max_concurrency: Number(event.target.value) }))} /></Field>
      <span className="workflow-meta-help">Separate item kinds with commas. Empty means any.</span>
    </div>

    <div className="workflow-workbench">
      <aside className="workflow-palette">
        <div className="workflow-panel-title"><span>01</span><strong>Node palette</strong></div>
        <p>Choose a step to add it.</p>
        <div className="workflow-palette-scroll">
          {PALETTE_GROUPS.map((group) => <div className="workflow-palette-group" key={group.key}>
            <span className="workflow-palette-group-label">{group.label}</span>
            {PALETTE_ENTRIES.filter((entry) => entry.group === group.key).map((entry) => <button key={entry.key} type="button" className="workflow-palette-item" disabled={!editable} onClick={() => addNode(entry.kind, { x: 80 + graph.nodes.length * 24, y: 100 + graph.nodes.length * 22 }, entry.type)}><span className={`workflow-kind-icon workflow-kind-icon--${entry.kind}`}>{entry.glyph}</span><span><strong>{entry.title}</strong><small>{entry.caption}</small></span><b>＋</b></button>)}
          </div>)}
        </div>
        <div className="workflow-palette-footer"><span className="mono">{graph.nodes.length} nodes</span><span className="mono">{graph.edges.length} edges</span></div>
      </aside>

      <main className="workflow-canvas-column">
      <div className="workflow-canvas-toolbar"><div><span className="workflow-live-dot" /><span className="workflow-canvas-label">Graph canvas<span className="tiny muted">· {editable ? "drag nodes · drag ports to connect" : "read-only view"}</span></span></div><div className="workflow-zoom-tools"><button className="btn btn--sm" aria-label="Zoom out" onClick={() => window.dispatchEvent(new CustomEvent("workflow-zoom", { detail: -0.1 }))}>−</button><button className="btn btn--sm" aria-label="Zoom in" onClick={() => window.dispatchEvent(new CustomEvent("workflow-zoom", { detail: 0.1 }))}>＋</button><button className="btn btn--sm" disabled={published} title={published ? "Published versions are frozen." : "Lay the graph out in reading order"} onClick={arrangeGraph}>Arrange</button><button className="btn btn--sm" onClick={() => window.dispatchEvent(new Event("workflow-fit"))}>Fit</button></div></div>
        <WorkflowCanvas graph={renderedGraph} fitVersion={fitVersion} agents={agentMap} editable={editable} selection={selection} problems={problems} onSelect={setSelection} onPosition={(id, position) => updateNode(id, (node) => ({ ...node, position }))} onAddEdge={(edge) => { setGraph((current) => ({ ...current, edges: [...current.edges, edge] })); setSelection({ type: "edge", id: edge.id }); }} onMessage={(text) => push("error", text)} onAddNode={addNode} />
        <section className="workflow-validation">
          <div className="workflow-validation-head"><div><span className="workflow-panel-index">02</span><strong>Validation</strong></div><span className={`workflow-validation-state workflow-validation-state--${validation}`}>{validation === "loading" ? "Checking…" : validation === "offline" ? "Unavailable" : problems.length ? `${problems.length} issue${problems.length === 1 ? "" : "s"}` : "Ready"}</span></div>
          {validation === "offline" ? <p className="workflow-validation-empty workflow-validation-error">{validationMessage}</p> : validation === "loading" ? <p className="workflow-validation-empty">Checking the graph with the server…</p> : problems.length ? <ul className="workflow-problem-list">{problems.map((problem, index) => <li key={`${problem.code}-${problem.node_id ?? problem.edge_id ?? index}`}><button type="button" onClick={() => focusProblem(problem)}><span className="workflow-problem-code">{problem.code}</span><span>{problem.message}</span><span className="workflow-problem-target">{problem.node_id ? `node ${problem.node_id.slice(0, 8)}` : problem.edge_id ? `edge ${problem.edge_id.slice(0, 8)}` : "graph"} ↗</span></button></li>)}</ul> : <p className="workflow-validation-empty workflow-valid">✓ The graph is valid</p>}
        </section>
      </main>

      <aside className="workflow-inspector">
        <div className="workflow-panel-title"><span>03</span><strong>Inspector</strong>{selection ? <button className="workflow-clear-selection" onClick={() => setSelection(null)}>Clear</button> : null}</div>
        <div className="workflow-inspector-body">
          {selectedNode ? <NodeInspector node={selectedNode} agents={agentsQ.data?.agents ?? []} runners={runnersQ.data?.runners ?? []} variables={variablesQ.data?.variables ?? {}} runnersError={runnersQ.error} variablesError={variablesQ.error} nodes={graph.nodes} editable={editable} onUpdate={(fn) => updateNode(selectedNode.id, fn)} onConfig={(patch) => updateConfig(selectedNode.id, patch)} onDelete={deleteSelection} /> : selectedEdge ? <EdgeInspector edge={selectedEdge} graph={graph} onDelete={deleteSelection} editable={editable} /> : <EmptyState title="Select a node or edge" body="Choose something on the canvas to inspect its settings." />}
          {agentsQ.error ? <p className="tiny muted">Agent picker unavailable: {errorText(agentsQ.error)}</p> : null}
          {runnersQ.error ? <p className="tiny muted">Runner picker unavailable: {errorText(runnersQ.error)}</p> : null}
        </div>
        <div className="workflow-runbox"><div className="workflow-runbox-head"><span className="workflow-kind-icon workflow-kind-icon--agent">▶</span><div><strong>Run on an item</strong><small>Starts this published workflow</small></div></div>
          <p className="workflow-cost-warning">This starts real agents and may incur model or service costs.</p>
          {runResult ? <div className="workflow-run-success">Run started · <Link href={`/runs/${runResult}`}>Open monitor →</Link></div> : null}
          {!runPicker ? <button className="btn btn--primary btn--sm workflow-run-button" disabled={!canWrite || !published} onClick={() => setRunPicker(true)}>{published ? "Choose an item" : "Publish to run"}</button> : <div className="workflow-run-picker"><select className="input" value={runItem} disabled={itemsQ.loading || !canWrite} onChange={(event) => setRunItem(event.target.value)}><option value="">{itemsQ.loading ? "Loading items…" : "Choose an item…"}</option>{eligibleItems.map((item) => <option key={item.id} value={item.id}>#{item.number} · {item.title}</option>)}</select>{itemsQ.error ? <span className="tiny muted">Items could not be loaded: {errorText(itemsQ.error)}</span> : null}{!itemsQ.loading && !itemsQ.error && !eligibleItems.length ? <span className="tiny muted">No matching items. Check the applies-to kinds.</span> : null}
            <div className="workflow-run-vars"><div className="spread"><strong>Run variables</strong><span className="tiny muted">Optional overrides</span></div>{runVariables.map(([key, value], index) => <div className="workflow-run-var-row" key={`${index}-${key}`}><input className="input mono" aria-label="Run variable name" value={key} placeholder="RUNNER" onChange={(event) => setRunVariables((current) => current.map((row, i) => i === index ? [event.target.value, row[1]] : row))} /><input className="input" aria-label={`Run variable value ${key}`} value={value} placeholder="claude" onChange={(event) => setRunVariables((current) => current.map((row, i) => i === index ? [row[0], event.target.value] : row))} /><button className="workflow-remove-row" aria-label={`Remove run variable ${key}`} onClick={() => setRunVariables((current) => current.filter((_, i) => i !== index))}>×</button></div>)}<button type="button" className="btn btn--sm" onClick={() => setRunVariables((current) => [...current, ["", ""]])}>＋ Add variable</button><span className="tiny muted">Tenant defaults are inherited; values entered here take precedence.</span>{variablesQ.error ? <span className="tiny muted">Tenant defaults unavailable: {errorText(variablesQ.error)}</span> : null}</div>
            <div className="stack"><button className="btn btn--primary btn--sm" disabled={!runItem || busy || !canWrite} onClick={() => void startRun()}>{busy ? "Starting…" : "Start run"}</button><button className="btn btn--sm" onClick={() => setRunPicker(false)}>Cancel</button></div></div>}
          {!canWrite ? <p className="tiny muted">Your role is read-only for starting runs.</p> : null}
        </div>
      </aside>
    </div>
    <Toasts toasts={toasts} />
  </div>;
}

function WorkflowCanvas({ graph, fitVersion, agents, editable, selection, problems, onSelect, onPosition, onAddEdge, onMessage, onAddNode }: {
  graph: WorkflowGraph; fitVersion: number; agents: Map<string, Agent>; editable: boolean; selection: Selection; problems: WorkflowProblem[];
  onSelect: (selection: Selection) => void; onPosition: (id: string, position: { x: number; y: number }) => void;
  onAddEdge: (edge: WorkflowEdge) => void; onMessage: (message: string) => void; onAddNode: (kind: string, position: { x: number; y: number }, type?: string) => void;
}) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const [view, setView] = useState<View>({ x: -BASE_X + 380, y: -BASE_Y + 250, scale: 1 });
  const [drag, setDrag] = useState<PortDrag | null>(null);
  const dragRef = useRef<PortDrag | null>(null);
  const [dropMessage, setDropMessage] = useState("");
  const fitRef = useRef<() => void>(() => {});
  const problemNodes = new Set(problems.map((problem) => problem.node_id).filter(Boolean));
  const problemEdges = new Set(problems.map((problem) => problem.edge_id).filter(Boolean));
  const setDragBoth = (next: PortDrag | null) => { dragRef.current = next; setDrag(next); };
  const pointInCanvas = (clientX: number, clientY: number) => {
    const rect = viewportRef.current?.getBoundingClientRect();
    if (!rect) return { x: 0, y: 0 };
    return { x: (clientX - rect.left - view.x) / view.scale, y: (clientY - rect.top - view.y) / view.scale };
  };
  const fit = useCallback(() => {
    const rect = viewportRef.current?.getBoundingClientRect();
    if (!rect || !graph.nodes.length) return;
    const points = graph.nodes.map((node) => ({ x: BASE_X + (node.position?.x ?? 0), y: BASE_Y + (node.position?.y ?? 0), size: nodeGeometry(node), width: nodeWidth(node) }));
    const left = Math.min(...points.map((p) => p.x)); const top = Math.min(...points.map((p) => p.y));
    const right = Math.max(...points.map((p) => p.x + p.width)); const bottom = Math.max(...points.map((p) => p.y + p.size.height));
    const scale = Math.max(0.42, Math.min(1.25, (rect.width - 100) / (right - left + 120), (rect.height - 90) / (bottom - top + 100)));
    const x = rect.width / 2 - ((left + right) / 2) * scale; const y = rect.height / 2 - ((top + bottom) / 2) * scale;
    setView({ x, y, scale });
  }, [graph.nodes]);
  fitRef.current = fit;
  useEffect(() => { if (graph.nodes.length) fit(); }, [graph.nodes.length]);
  useEffect(() => { if (fitVersion) fitRef.current(); }, [fitVersion]);
  useEffect(() => {
    const zoom = (event: Event) => { const delta = (event as CustomEvent<number>).detail; setView((current) => ({ ...current, scale: Math.max(0.35, Math.min(1.65, current.scale + delta)) })); };
    const onFit = () => fit();
    const onFocus = (event: Event) => {
      const id = (event as CustomEvent<string>).detail; const node = graph.nodes.find((candidate) => candidate.id === id); const rect = viewportRef.current?.getBoundingClientRect();
      if (!node || !rect) return;
      const pos = node.position ?? { x: 0, y: 0 };
      setView((current) => ({ ...current, x: rect.width / 2 - (BASE_X + pos.x + nodeWidth(node) / 2) * current.scale, y: rect.height / 2 - (BASE_Y + pos.y + nodeGeometry(node).height / 2) * current.scale }));
    };
    window.addEventListener("workflow-zoom", zoom); window.addEventListener("workflow-fit", onFit); window.addEventListener("workflow-focus", onFocus);
    return () => { window.removeEventListener("workflow-zoom", zoom); window.removeEventListener("workflow-fit", onFit); window.removeEventListener("workflow-focus", onFocus); };
  }, [fit, graph.nodes]);
  useEffect(() => {
    if (!drag) return;
    const move = (event: PointerEvent) => {
      const current = dragRef.current;
      if (!current) return;
      if (current.type === "node") {
        const dx = (event.clientX - current.startX) / view.scale; const dy = (event.clientY - current.startY) / view.scale;
        onPosition(current.id, { x: Math.round(current.originX + dx), y: Math.round(current.originY + dy) });
      } else if (current.type === "pan") setView((value) => ({ ...value, x: current.originX + event.clientX - current.startX, y: current.originY + event.clientY - current.startY }));
      else {
        const point = pointInCanvas(event.clientX, event.clientY);
        const next = { ...current, point }; dragRef.current = next; setDrag(next);
      }
    };
    const up = (event: PointerEvent) => {
      const current = dragRef.current;
      if (current?.type === "connect") {
        const target = document.elementFromPoint(event.clientX, event.clientY)?.closest("[data-input-node]") as HTMLElement | null;
        if (target) {
          const node = target.dataset.inputNode ?? ""; const port = target.dataset.inputPort ?? "";
          if (node === current.node) onMessage("Connect to a different node; self-edges are not supported by this editor.");
          else onAddEdge({ id: makeId(), from: { node: current.node, port: current.port }, to: { node, port } });
        } else { setDropMessage("Drop it on a port."); onMessage("Drop it on a port."); window.setTimeout(() => setDropMessage(""), 2200); }
      }
      setDragBoth(null);
    };
    window.addEventListener("pointermove", move); window.addEventListener("pointerup", up);
    return () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", up); };
  }, [drag?.type, onAddEdge, onMessage, onPosition, view.scale]);

  function beginNode(event: React.PointerEvent, node: WorkflowNode) {
    if (!editable || (event.target as HTMLElement).closest("button")) return;
    event.preventDefault(); onSelect({ type: "node", id: node.id });
    const pos = node.position ?? { x: 0, y: 0 };
    setDragBoth({ type: "node", id: node.id, startX: event.clientX, startY: event.clientY, originX: pos.x, originY: pos.y });
  }
  function beginConnect(event: React.PointerEvent, node: WorkflowNode, port: string) {
    event.preventDefault(); event.stopPropagation();
    const point = pointInCanvas(event.clientX, event.clientY);
    setDropMessage(""); setDragBoth({ type: "connect", node: node.id, port, point });
  }
  function beginPan(event: React.PointerEvent) {
    if ((event.target as HTMLElement).closest(".workflow-node, .workflow-terminator, .workflow-edge-hit, button")) return;
    event.preventDefault(); setDragBoth({ type: "pan", startX: event.clientX, startY: event.clientY, originX: view.x, originY: view.y });
  }
  function wheel(event: React.WheelEvent) {
    event.preventDefault();
    const rect = viewportRef.current?.getBoundingClientRect(); if (!rect) return;
    const factor = event.deltaY < 0 ? 1.08 : 0.92;
    const nextScale = Math.max(0.35, Math.min(1.65, view.scale * factor));
    const px = event.clientX - rect.left; const py = event.clientY - rect.top;
    const ratio = nextScale / view.scale;
    setView({ scale: nextScale, x: px - (px - view.x) * ratio, y: py - (py - view.y) * ratio });
  }
  /** The handles for a node, taken from the kind's own port list. A terminator
   *  carries a single port, so it sits centred on the pill rather than stacked. */
  function nodePorts(node: WorkflowNode, ports: KindMeta, centered: boolean) {
    const label = node.name || "Unnamed step";
    const offset = (index: number): React.CSSProperties => (centered ? { top: "50%", transform: "translateY(-50%)" } : { top: 58 + index * PORT_ROW });
    return <div className="workflow-node-ports">
      <div className="workflow-port-column workflow-port-column--input">{ports.input.map((port, index) => <button key={port} type="button" className="workflow-port workflow-port--input" data-input-node={node.id} data-input-port={port} style={offset(index)} aria-label={`${label} input ${port}`} onPointerDown={(event) => { event.stopPropagation(); onSelect({ type: "node", id: node.id }); }}><i /><span>{port}</span></button>)}</div>
      <div className="workflow-port-column workflow-port-column--output">{ports.output.map((port, index) => <button key={port} type="button" className="workflow-port workflow-port--output" style={offset(index)} aria-label={`${label} output ${port}`} disabled={!editable} onPointerDown={(event) => beginConnect(event, node, port)}><span>{port}</span><i /></button>)}</div>
    </div>;
  }
  const live = drag?.type === "connect" ? drag.point : null;
  return <div className="workflow-canvas" data-workflow-canvas ref={viewportRef} onPointerDown={beginPan} onWheel={wheel} onDragOver={(event) => event.preventDefault()} onDrop={(event) => {
    const kind = event.dataTransfer.getData("workflow-kind"); if (!kind || !editable) return;
    const p = pointInCanvas(event.clientX, event.clientY); onAddNode(kind, { x: Math.round(p.x - BASE_X), y: Math.round(p.y - BASE_Y) }, event.dataTransfer.getData("workflow-integration") || undefined);
  }}>
    <div className="workflow-canvas-grid" />
    <div className="workflow-world" style={{ width: 6400, height: 5000, transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}>
      <svg className="workflow-edge-layer" width={6400} height={5000} viewBox="0 0 6400 5000" aria-hidden="true">
        <defs><marker id="workflow-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="var(--theme-gray-600)" /></marker></defs>
        {graph.edges.map((edge) => {
          const from = graph.nodes.find((node) => node.id === edge.from.node); const to = graph.nodes.find((node) => node.id === edge.to.node);
          if (!from || !to) return null;
          const a = portPoint(from, edge.from.port, true); const b = portPoint(to, edge.to.port, false);
          const d = bezier(a.x, a.y, b.x, b.y); const selected = selection?.type === "edge" && selection.id === edge.id;
          return <g key={edge.id} data-edge-id={edge.id} className={`workflow-edge ${selected ? "workflow-edge--selected" : ""} ${problemEdges.has(edge.id) ? "workflow-edge--invalid" : ""}`} onPointerDown={(event) => { event.stopPropagation(); onSelect({ type: "edge", id: edge.id }); }}><path className="workflow-edge-shadow" d={d} /><path className="workflow-edge-line" d={d} markerEnd="url(#workflow-arrow)" /><path className="workflow-edge-hit" d={d} /></g>;
        })}
        {live ? <path className="workflow-edge-live" d={bezier(portPoint(graph.nodes.find((node) => node.id === (drag as Extract<PortDrag, { type: "connect" }>).node)!, (drag as Extract<PortDrag, { type: "connect" }>).port, true).x, portPoint(graph.nodes.find((node) => node.id === (drag as Extract<PortDrag, { type: "connect" }>).node)!, (drag as Extract<PortDrag, { type: "connect" }>).port, true).y, live.x, live.y)} /> : null}
      </svg>
      {graph.nodes.map((node) => {
        const pos = node.position ?? { x: 0, y: 0 }; const ports = KIND_META[node.kind] ?? KIND_META.agent; const agentId = String(asConfig(node.config).agent_id ?? ""); const agent = agents.get(agentId);
        // An integration node wears the glyph of the action it performs, so the
        // four kinds of GitHub work are told apart on the canvas as well.
        const glyph = node.kind === "integration" ? (INTEGRATION_META[node.type ?? ""]?.glyph ?? ports.glyph) : ports.glyph;
        const state = `${selection?.type === "node" && selection.id === node.id ? " workflow-node--selected" : ""}${problemNodes.has(node.id) ? " workflow-node--invalid" : ""}`;
        // Start and end are drawn as pills, not cards, so the flow's two
        // boundaries are unmistakable beside the work steps.
        if (isTerminator(node.kind)) return <article key={node.id} data-node-id={node.id} className={`workflow-terminator workflow-terminator--${node.kind}${state}`} style={{ left: BASE_X + pos.x, top: BASE_Y + pos.y, width: TERMINATOR_W, height: TERMINATOR_H }} onPointerDown={(event) => beginNode(event, node)} onClick={() => onSelect({ type: "node", id: node.id })}>
          <span className="workflow-terminator-glyph" aria-hidden="true">{ports.glyph}</span>
          <strong className="workflow-terminator-name">{node.name || ports.defaultName}</strong>
          {nodePorts(node, ports, true)}
        </article>;
        return <article key={node.id} data-node-id={node.id} className={`workflow-node${state}`} style={{ left: BASE_X + pos.x, top: BASE_Y + pos.y, width: CARD_W, minHeight: nodeGeometry(node).height }} onPointerDown={(event) => beginNode(event, node)} onClick={() => onSelect({ type: "node", id: node.id })}>
          <header className="workflow-node-head"><span className={`workflow-kind-icon workflow-kind-icon--${node.kind}`}>{glyph}</span><span className="workflow-node-kind">{node.kind}</span><span className="workflow-node-id mono">{node.id.slice(0, 6)}</span></header>
          <strong className="workflow-node-name">{node.name || "Unnamed step"}</strong>
          {node.kind === "agent" ? <span className="workflow-node-agent">{agent ? `✦ @${agent.name}` : agentId ? `Agent ${agentId.slice(0, 8)}` : "Choose an agent"}</span> : node.kind === "integration" ? <span className="workflow-node-agent">{integrationName(node.type)}</span> : <span className="workflow-node-agent">{node.type || ports.caption}</span>}
          {nodePorts(node, ports, false)}
        </article>;
      })}
    </div>
    {!graph.nodes.length ? <div className="workflow-canvas-empty"><span>◇</span><strong>An empty canvas is a fresh start.</strong><p>Choose a node from the palette, or add an agent to begin.</p></div> : null}
    <div className="workflow-canvas-coordinates"><span>{graph.nodes.length} steps</span><i />{Math.round(view.scale * 100)}%</div>
    {dropMessage ? <div className="workflow-canvas-message">{dropMessage}</div> : null}
  </div>;
}

function nodeGeometry(node: WorkflowNode) {
  if (isTerminator(node.kind)) return { height: TERMINATOR_H };
  const count = Math.max(KIND_META[node.kind]?.input.length ?? 1, KIND_META[node.kind]?.output.length ?? 1);
  return { height: 70 + count * PORT_ROW };
}
function portPoint(node: WorkflowNode, port: string, output: boolean) {
  const ports = output ? KIND_META[node.kind]?.output ?? [] : KIND_META[node.kind]?.input ?? [];
  const index = Math.max(0, ports.indexOf(port)); const position = node.position ?? { x: 0, y: 0 };
  if (isTerminator(node.kind)) return { x: BASE_X + position.x + (output ? TERMINATOR_W : 0), y: BASE_Y + position.y + TERMINATOR_H / 2 };
  return { x: BASE_X + position.x + (output ? CARD_W : 0), y: BASE_Y + position.y + 68 + index * PORT_ROW };
}
function bezier(ax: number, ay: number, bx: number, by: number) {
  const curve = Math.max(48, Math.abs(bx - ax) * 0.44);
  return `M ${ax} ${ay} C ${ax + curve} ${ay}, ${bx - curve} ${by}, ${bx} ${by}`;
}

function NodeInspector({ node, agents, runners, variables, runnersError, variablesError, nodes, editable, onUpdate, onConfig, onDelete }: { node: WorkflowNode; agents: Agent[]; runners: Runner[]; variables: Record<string, string>; runnersError: Error | null; variablesError: Error | null; nodes: WorkflowNode[]; editable: boolean; onUpdate: (fn: (node: WorkflowNode) => WorkflowNode) => void; onConfig: (patch: Record<string, unknown>) => void; onDelete: () => void }) {
  const config = asConfig(node.config);
  const meta = KIND_META[node.kind];
  const terminator = isTerminator(node.kind);
  const integration = node.kind === "integration" ? INTEGRATION_META[node.type ?? ""] : undefined;
  const selectedAgent = agents.find((agent) => agent.id === String(config.agent_id ?? ""));
  const runnerChoice = String(config.runner ?? "");
  const selectedVariable = runnerChoice.startsWith("$") ? runnerChoice.slice(1) : "";
  const select = (label: string, value: string, options: string[], change: (value: string) => void) => <Field label={label}><select className="input" value={value} disabled={!editable} onChange={(event) => change(event.target.value)}>{options.map((option) => <option key={option} value={option}>{option.replaceAll("_", " ")}</option>)}</select></Field>;
  const number = (label: string, value: unknown, change: (value: number | undefined) => void, min = 0) => <Field label={label}><input className="input" type="number" min={min} disabled={!editable} value={value == null ? "" : String(value)} onChange={(event) => change(event.target.value === "" ? undefined : Number(event.target.value))} /></Field>;
  const text = (label: string, value: unknown, change: (value: string) => void, placeholder = "") => <Field label={label}><input className="input" value={typeof value === "string" ? value : ""} disabled={!editable} placeholder={placeholder} onChange={(event) => change(event.target.value)} /></Field>;
  return <div className="workflow-form">
    <div className="workflow-inspector-entity"><span className={`workflow-kind-icon workflow-kind-icon--${node.kind}`}>{integration?.glyph ?? meta?.glyph ?? "✦"}</span><div><small>{terminator ? (node.type || node.kind) : node.kind === "integration" ? integrationName(node.type) : `${node.kind} node`}</small><strong>{node.name || "Unnamed step"}</strong></div></div>
    {terminator ? <p className="workflow-terminator-note">{meta?.note}</p> : null}
    {text("Display name", node.name, (name) => onUpdate((current) => ({ ...current, name })), terminator ? "e.g. intake" : "e.g. review changes")}
    {terminator ? <>
      <Field label="Description" hint={meta?.hint}><textarea className="input workflow-description" value={String(config.description ?? "")} disabled={!editable} placeholder="What this point means for the run…" onChange={(event) => onConfig({ description: event.target.value })} /></Field>
    </> : null}
    {node.kind === "agent" ? <>
      <Field label="Agent"><select className="input" value={String(config.agent_id ?? "")} disabled={!editable} onChange={(event) => { const agent = agents.find((candidate) => candidate.id === event.target.value); onConfig({ agent_id: event.target.value }); if (agent) onUpdate((current) => ({ ...current, type: `agent.${agent.role || "dev"}` })); }}><option value="">Choose an agent…</option>{agents.map((agent) => <option key={agent.id} value={agent.id}>{agent.enabled ? "" : "Disabled · "}@{agent.name} · {agent.role}</option>)}</select></Field>
      <Field label="Runner choice"><select className="input" value={runnerChoice} disabled={!editable} onChange={(event) => onConfig({ runner: event.target.value || undefined })}><option value="">Use agent default</option><optgroup label="Tenant runners">{runners.map((runner) => <option key={runner.id} value={runner.name}>{runner.enabled ? "" : "Disabled · "}{runner.name} · {runner.kind}</option>)}</optgroup>{Object.keys(variables).length ? <optgroup label="Use a tenant variable">{Object.entries(variables).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => <option key={name} value={`$${name}`}>${name} = {value}</option>)}</optgroup> : null}</select><span className="tiny muted">Choose a specific runner or use a variable from the run.</span></Field>
      {selectedVariable ? <div className="workflow-runner-variable"><strong>${selectedVariable}</strong><span>Current tenant default: <code>{variables[selectedVariable] ?? "not set"}</code></span><small>A run’s own value takes precedence. If it resolves to a runner that is over budget, the engine follows the agent’s chain.</small></div> : null}
      {runnersError ? <p className="tiny muted">Runners are unavailable: {errorText(runnersError)}</p> : null}{variablesError ? <p className="tiny muted">Tenant variables are unavailable: {errorText(variablesError)}</p> : null}
      <div className="workflow-agent-chain"><div className="workflow-form-subhead"><strong>Agent runner chain</strong><span>fallback order</span></div>{selectedAgent?.runners?.length ? <ol>{selectedAgent.runners.map((runner, index) => <li key={runner.id}><span className="workflow-chain-number">{index + 1}</span><span>{runner.name}</span>{index === 0 ? <small>default</small> : null}</li>)}</ol> : <p>{selectedAgent ? "This agent has no runner, so it cannot be dispatched." : "Choose an agent to see its default and fallback runners."}</p>}</div>
      <Field label="Prompt template"><textarea className="input workflow-prompt" value={String(config.prompt_tmpl ?? "")} disabled={!editable} placeholder="Describe the work for this agent…" onChange={(event) => onConfig({ prompt_tmpl: event.target.value })} /></Field>
      <div className="workflow-form-subhead"><strong>Declared outputs</strong><span>key → JSON type</span></div>
      <OutputRows value={asConfig(config.outputs)} disabled={!editable} onChange={(outputs) => onConfig({ outputs })} />
    </> : null}
    {node.kind === "checkpoint" ? <>
      {select("Rejection policy", String(config.policy ?? "retry_step"), ["retry_step", "abort", "goto_step"], (policy) => onConfig({ policy }))}
      {number("Maximum cycles", config.max_cycles, (max_cycles) => onConfig({ max_cycles }), 1)}
      {config.policy === "goto_step" ? <Field label="Target node"><select className="input" value={String(config.target ?? "")} disabled={!editable} onChange={(event) => onConfig({ target: event.target.value })}><option value="">Choose a node…</option>{nodes.filter((candidate) => candidate.id !== node.id).map((candidate) => <option key={candidate.id} value={candidate.id}>{candidate.name || candidate.id}</option>)}</select></Field> : null}
    </> : null}
    {node.kind === "loop" ? <>
      {number("Maximum iterations", config.max_iterations ?? 3, (max_iterations) => onConfig({ max_iterations }), 1)}
      <Field label="Stop when"><select className="input" value={typeof config.until === "boolean" ? String(config.until) : "output"} disabled={!editable} onChange={(event) => onConfig({ until: event.target.value === "true" ? true : event.target.value === "false" ? false : "" })}><option value="true">Condition is true</option><option value="false">Condition is false</option><option value="output">Output key</option></select></Field>
      {typeof config.until === "string" ? text("Output key", config.until, (until) => onConfig({ until }), "e.g. ready") : null}
    </> : null}
    {node.kind === "merge" ? <>
      {select("Join mode", String(config.mode ?? "all"), ["all", "any", "n_of_m"], (mode) => { onConfig({ mode }); onUpdate((current) => ({ ...current, type: `merge.${mode}` })); })}
      {config.mode === "n_of_m" ? number("Required inputs (n)", config.n, (n) => onConfig({ n }), 1) : null}
    </> : null}
    {node.kind === "integration" ? (integration
      ? <IntegrationForm type={node.type ?? ""} config={config} editable={editable} onConfig={onConfig} />
      : <p className="workflow-terminator-note">“{integrationName(node.type)}” is a built-in action. It has no settings of its own — the server runs it as it is.</p>) : null}
    <div className="workflow-form-divider" />
    {terminator ? null : <>
      {select("On error", String(node.on_error ?? "stop_workflow"), ERROR_POLICIES, (on_error) => onUpdate((current) => ({ ...current, on_error })))}
      {select("Workspace", String(node.workspace ?? "scratch"), WORKSPACES, (workspace) => onUpdate((current) => ({ ...current, workspace })))}
      <label className="workflow-check"><input type="checkbox" checked={Boolean(node.always_output_data)} disabled={!editable} onChange={(event) => onUpdate((current) => ({ ...current, always_output_data: event.target.checked }))} />Always pass output data</label>
    </>}
    {editable ? <button className="btn btn--danger btn--sm workflow-delete-node" onClick={onDelete}>Delete node</button> : null}
  </div>;
}

/* --- Integration forms ------------------------------------------------------
   Each integration type has its own form with named fields: the fields the
   server reads, and nothing else. The JSON box is gone — a flow is written in
   words, not in braces. */

type IntegrationFieldProps = {
  config: Record<string, unknown>;
  editable: boolean;
  onConfig: (patch: Record<string, unknown>) => void;
};

/** The placeholders the engine fills in when the run starts. */
const TEMPLATE_HINT = "Templated: {{item.title}}, {{item.description}} and {{item.id}} are filled in when the run starts.";

function configText(config: Record<string, unknown>, key: string): string {
  return typeof config[key] === "string" ? (config[key] as string) : "";
}
function configNumber(config: Record<string, unknown>, key: string): number | undefined {
  const value = config[key];
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function RepositoryField({ config, editable, onConfig }: IntegrationFieldProps) {
  return <Field label="Repository" hint="The repository as owner/name. Empty means the item's own product repository.">
    <input className="input" value={configText(config, "repo")} disabled={!editable} placeholder="owner/name" onChange={(event) => onConfig({ repo: event.target.value })} />
  </Field>;
}

function RemoteField({ config, editable, onConfig }: IntegrationFieldProps) {
  return <Field label="Remote" hint="The clone remote to push to. It usually stays empty: the product's repository is used.">
    <input className="input" value={configText(config, "remote")} disabled={!editable} placeholder="https://github.com/owner/name.git" onChange={(event) => onConfig({ remote: event.target.value })} />
  </Field>;
}

function AdvancedRemote({ config, editable, onConfig }: IntegrationFieldProps) {
  return <>
    <div className="workflow-form-subhead"><strong>Advanced</strong><span>usually empty</span></div>
    <RemoteField config={config} editable={editable} onConfig={onConfig} />
  </>;
}

/** github.open_pr — the server needs head, base and title. */
function OpenPullRequestForm({ config, editable, onConfig }: IntegrationFieldProps) {
  return <>
    <RepositoryField config={config} editable={editable} onConfig={onConfig} />
    <Field label="Base branch" hint="The branch the pull request merges into. The server requires it.">
      <input className="input" value={configText(config, "base")} disabled={!editable} placeholder="main" onChange={(event) => onConfig({ base: event.target.value })} />
    </Field>
    <Field label="Head branch" hint="The branch the worktree pushed. Empty means the run's branch: aic/<item>-<step>-<attempt>.">
      <input className="input" value={configText(config, "head")} disabled={!editable} placeholder="aic/…" onChange={(event) => onConfig({ head: event.target.value })} />
    </Field>
    <Field label="Title" hint={TEMPLATE_HINT}>
      <input className="input" value={configText(config, "title")} disabled={!editable} placeholder="{{item.title}}" onChange={(event) => onConfig({ title: event.target.value })} />
    </Field>
    <Field label="Body" hint={TEMPLATE_HINT}>
      <textarea className="input workflow-description" value={configText(config, "body")} disabled={!editable} placeholder="{{item.description}}" onChange={(event) => onConfig({ body: event.target.value })} />
    </Field>
    <label className="workflow-check"><input type="checkbox" checked={Boolean(config.draft)} disabled={!editable} onChange={(event) => onConfig({ draft: event.target.checked })} />Open as a draft</label>
    <AdvancedRemote config={config} editable={editable} onConfig={onConfig} />
  </>;
}

/** github.wait_ci — the server needs a pull request, and finds it by number
 *  or by the branch the run worked on. */
function WaitForCIForm({ config, editable, onConfig }: IntegrationFieldProps) {
  return <>
    <RepositoryField config={config} editable={editable} onConfig={onConfig} />
    <Field label="Pull request number" hint="The pull request to watch. Empty means the pull request the run opened.">
      <input className="input" type="number" min={1} value={configNumber(config, "pr") ?? ""} disabled={!editable} placeholder="42" onChange={(event) => onConfig({ pr: event.target.value === "" ? undefined : Number(event.target.value) })} />
    </Field>
    <Field label="Branch" hint="Empty waits on the pull request of the run's own branch: aic/<item>-<step>-<attempt>.">
      <input className="input" value={configText(config, "head")} disabled={!editable} placeholder="aic/…" onChange={(event) => onConfig({ head: event.target.value })} />
    </Field>
    <Field label="Timeout (seconds)" hint="How long the node waits before it gives up. Empty means 30 minutes.">
      <input className="input" type="number" min={1} value={configNumber(config, "timeout_s") ?? ""} disabled={!editable} placeholder="1800" onChange={(event) => onConfig({ timeout_s: event.target.value === "" ? undefined : Number(event.target.value) })} />
    </Field>
    <Field label="Poll interval (seconds)" hint="How often the checks are read. Empty means every 5 seconds.">
      <input className="input" type="number" min={1} value={configNumber(config, "poll_interval_s") ?? ""} disabled={!editable} placeholder="5" onChange={(event) => onConfig({ poll_interval_s: event.target.value === "" ? undefined : Number(event.target.value) })} />
    </Field>
    <AdvancedRemote config={config} editable={editable} onConfig={onConfig} />
  </>;
}

/** github.comment — the server needs a pull request and a body. */
function CommentForm({ config, editable, onConfig }: IntegrationFieldProps) {
  return <>
    <RepositoryField config={config} editable={editable} onConfig={onConfig} />
    <Field label="Pull request number" hint="The pull request to comment on. The server requires it.">
      <input className="input" type="number" min={1} value={configNumber(config, "pr") ?? ""} disabled={!editable} placeholder="42" onChange={(event) => onConfig({ pr: event.target.value === "" ? undefined : Number(event.target.value) })} />
    </Field>
    <Field label="Body" hint={TEMPLATE_HINT}>
      <textarea className="input workflow-description" value={configText(config, "body")} disabled={!editable} placeholder="{{item.description}}" onChange={(event) => onConfig({ body: event.target.value })} />
    </Field>
    <AdvancedRemote config={config} editable={editable} onConfig={onConfig} />
  </>;
}

/** github.fetch_issue — the server needs a positive issue number. */
function FetchIssueForm({ config, editable, onConfig }: IntegrationFieldProps) {
  return <>
    <RepositoryField config={config} editable={editable} onConfig={onConfig} />
    <Field label="Issue number" hint="The issue to read. The server requires a positive number.">
      <input className="input" type="number" min={1} value={configNumber(config, "issue") ?? ""} disabled={!editable} placeholder="42" onChange={(event) => onConfig({ issue: event.target.value === "" ? undefined : Number(event.target.value) })} />
    </Field>
    <AdvancedRemote config={config} editable={editable} onConfig={onConfig} />
  </>;
}

function IntegrationForm({ type, config, editable, onConfig }: IntegrationFieldProps & { type: string }) {
  const form = INTEGRATION_META[type]?.form;
  if (form === "open_pr") return <OpenPullRequestForm config={config} editable={editable} onConfig={onConfig} />;
  if (form === "wait_ci") return <WaitForCIForm config={config} editable={editable} onConfig={onConfig} />;
  if (form === "comment") return <CommentForm config={config} editable={editable} onConfig={onConfig} />;
  return <FetchIssueForm config={config} editable={editable} onConfig={onConfig} />;
}

function OutputRows({ value, disabled, onChange }: { value: Record<string, unknown>; disabled: boolean; onChange: (value: Record<string, unknown>) => void }) {
  const entries = Object.entries(value);
  return <div className="workflow-output-rows">{entries.map(([key, type], index) => <div className="workflow-output-row" key={`${key}-${index}`}><input className="input mono" aria-label="Output key" value={key} disabled={disabled} placeholder="finding" onChange={(event) => { const next = [...entries]; next[index] = [event.target.value, type]; onChange(Object.fromEntries(next.filter(([name]) => name.trim()))); }} /><select className="input" aria-label="Output JSON type" value={OUTPUT_TYPES.includes(type as typeof OUTPUT_TYPES[number]) ? String(type) : "string"} disabled={disabled} onChange={(event) => onChange({ ...value, [key]: event.target.value })}>{OUTPUT_TYPES.map((kind) => <option key={kind}>{kind}</option>)}</select><button className="workflow-remove-row" aria-label={`Remove output ${key}`} disabled={disabled} onClick={() => { const next = { ...value }; delete next[key]; onChange(next); }}>×</button></div>)}<button className="btn btn--sm workflow-add-output" disabled={disabled} onClick={() => { let key = "output"; let index = 2; while (key in value) key = `output_${index++}`; onChange({ ...value, [key]: "string" }); }}>＋ Add output</button></div>;
}

function EdgeInspector({ edge, graph, editable, onDelete }: { edge: WorkflowEdge; graph: WorkflowGraph; editable: boolean; onDelete: () => void }) {
  const from = graph.nodes.find((node) => node.id === edge.from.node); const to = graph.nodes.find((node) => node.id === edge.to.node);
  return <div className="workflow-edge-inspector"><span className="workflow-edge-glyph">↗</span><small>Connected edge</small><h3>{from?.name ?? edge.from.node} <span>→</span> {to?.name ?? edge.to.node}</h3><div className="workflow-edge-endpoint"><span>FROM</span><code>{from?.name ?? edge.from.node}.{edge.from.port}</code></div><div className="workflow-edge-endpoint"><span>TO</span><code>{to?.name ?? edge.to.node}.{edge.to.port}</code></div><p>Ports are fixed by node kind. To change this connection, delete it and draw a new one.</p>{editable ? <button className="btn btn--danger btn--sm" onClick={onDelete}>Delete edge</button> : null}</div>;
}
