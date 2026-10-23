"use client";

import { FormEvent, useMemo, useState } from "react";
import { api, type Agent, type AgentInput, type Runner } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { formatDate } from "@/lib/format";
import { SettingsNav } from "@/components/settings-nav";
import { Dialog, EmptyState, ErrorBlock, Skeleton, Toasts, apiErrorMessage } from "@/components/primitives";

const ROLES = [
  { value: "pm", label: "Product manager" }, { value: "dev", label: "Developer" },
  { value: "reviewer", label: "Reviewer" }, { value: "qa", label: "QA" },
  { value: "researcher", label: "Researcher" },
];
const DEFAULT_SKILLS = { auto: true, max_attach: 3 };

export default function AgentsSettingsPage() {
  const { me } = useSession();
  const { toasts, push } = useToasts();
  const isManager = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const agentsQ = useAsync(() => api.listAgents().then((r) => r.agents), []);
  const runnersQ = useAsync(() => api.listRunners().then((r) => r.runners), []);
  const [editing, setEditing] = useState<Agent | "new" | null>(null);
  const sorted = useMemo(() => [...(agentsQ.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [agentsQ.data]);

  async function toggle(agent: Agent) {
    try { await api.updateAgent(agent.id, { enabled: !agent.enabled }); await agentsQ.reload(); }
    catch (error) { push("error", errorMessage(error)); }
  }
  async function remove(agent: Agent) {
    if (!window.confirm(`Delete @${agent.name}? Agents with past job history cannot be deleted — disable it instead.`)) return;
    try { await api.deleteAgent(agent.id); await agentsQ.reload(); push("info", `Agent @${agent.name} deleted.`); }
    catch (error) { push("error", errorMessage(error)); }
  }

  return <div style={{ maxWidth: 1000 }}>
    <div className="pagehead"><h1>Agents</h1><span className="pagehead__sub">Agents are the work itself — their persona, role and skills. Runners are the tools they use.</span></div>
    <SettingsNav />
    <section className="section">
      <header className="section__head"><div><h2>Agent registry</h2><div className="tiny muted" style={{ marginTop: 3 }}>The first runner is the default; later runners are tried in fallback order.</div></div>
        {isManager ? <button type="button" className="btn btn--sm btn--primary" style={{ marginLeft: "auto" }} onClick={() => setEditing("new")}>New agent</button> : <span className="tiny muted" style={{ marginLeft: "auto" }}>Read-only · only an owner or admin can change agents.</span>}
      </header>
      <div className="section__body">
        {agentsQ.error ? <ErrorBlock title="Could not load agents" message={errorMessage(agentsQ.error)} retry={() => void agentsQ.reload()} /> : !agentsQ.data ? <Skeleton h={110} /> : sorted.length === 0 ? <EmptyState title="No agents yet" body="Create an agent to define what work should be done. Add one or more runners to define how it gets done." action={isManager ? <button className="btn btn--primary" onClick={() => setEditing("new")}>Create your first agent</button> : undefined} /> :
          <div className="table-wrap"><table className="data"><thead><tr><th>Agent</th><th>Role</th><th>Runner chain</th><th>Enabled</th><th>Concurrency</th><th>Updated</th><th><span className="visually-hidden">Actions</span></th></tr></thead><tbody>
            {sorted.map((agent) => <tr key={agent.id}>
              <td><span className="mono agent-name">@{agent.name}</span>{agent.soul_md ? <span className="tiny muted" style={{ marginLeft: 8 }} title={agent.soul_md}>soul</span> : null}</td>
              <td className="tiny">{agent.role || "—"}</td>
              <td>{agent.runners?.length ? <span className="runner-chain-inline">{agent.runners.map((runner, index) => <span key={runner.id}><span className={index === 0 ? "runner-first" : "tiny muted"}>{runner.name}</span>{index < agent.runners.length - 1 ? <span className="muted"> → </span> : null}</span>)}</span> : <span className="runner-missing">No runner · cannot be dispatched</span>}</td>
              <td>{isManager ? <input type="checkbox" className="row-check" checked={agent.enabled} aria-label={`Enable or disable @${agent.name}`} onChange={() => void toggle(agent)} /> : <span className="badge-outline">{agent.enabled ? "on" : "off"}</span>}</td>
              <td className="tiny mono">{agent.concurrency_limit}</td><td className="tiny muted">{formatDate(agent.updated_at || agent.created_at)}</td>
              <td>{isManager ? <span className="stack" style={{ gap: 6 }}><button className="btn btn--sm" onClick={() => setEditing(agent)}>Edit</button><button className="btn btn--sm btn--ghost btn--danger" onClick={() => void remove(agent)}>Delete</button></span> : null}</td>
            </tr>)}
          </tbody></table></div>}
      </div>
    </section>
    {editing ? <AgentEditor agent={editing === "new" ? null : editing} runners={runnersQ.data ?? []} runnersError={runnersQ.error} onClose={() => setEditing(null)} onSaved={async () => { setEditing(null); await agentsQ.reload(); push("info", "Agent saved."); }} /> : null}
    <Toasts toasts={toasts} />
  </div>;
}

type FormState = { name: string; role: string; concurrency: string; enabled: boolean; soul: string; chain: string[]; skills: string };
function agentForm(agent: Agent | null): FormState {
  return { name: agent?.name ?? "", role: agent?.role ?? "dev", concurrency: String(agent?.concurrency_limit ?? 2), enabled: agent?.enabled ?? true, soul: agent?.soul_md ?? "", chain: agent?.runners?.map((runner) => runner.id) ?? [], skills: JSON.stringify(agent?.skills_policy ?? DEFAULT_SKILLS, null, 2) };
}
function AgentEditor({ agent, runners, runnersError, onClose, onSaved }: { agent: Agent | null; runners: Runner[]; runnersError: Error | null; onClose: () => void; onSaved: () => Promise<void> }) {
  const [form, setForm] = useState(() => agentForm(agent));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm((current) => ({ ...current, [key]: value }));
  function move(index: number, direction: -1 | 1) { const next = [...form.chain]; const target = index + direction; if (target < 0 || target >= next.length) return; [next[index], next[target]] = [next[target], next[index]]; set("chain", next); }
  async function submit(event: FormEvent) {
    event.preventDefault(); if (busy) return;
    let skills: Record<string, unknown>;
    try { const parsed: unknown = JSON.parse(form.skills || "{}"); if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(); skills = parsed as Record<string, unknown>; }
    catch { setError("Skills policy must be a JSON object."); return; }
    const body: AgentInput = { name: form.name.trim().toLowerCase(), role: form.role, runners: form.chain, concurrency_limit: Math.max(1, Number(form.concurrency) || 1), enabled: form.enabled, soul_md: form.soul, skills_policy: skills, capabilities: agent?.capabilities ?? { code: true, shell: true, streaming: true, usage: true } };
    setBusy(true); setError("");
    try { if (agent) await api.updateAgent(agent.id, body); else await api.createAgent(body); await onSaved(); }
    catch (err) { setError(errorMessage(err)); setBusy(false); }
  }
  const chainOptions = runners.filter((runner) => !form.chain.includes(runner.id));
  return <Dialog title={agent ? `Edit @${agent.name}` : "New agent"} description="An agent defines what to do. Its ordered runner chain defines how to do it." onClose={onClose} width={680}>
    <form onSubmit={submit} style={{ display: "grid", gap: 14 }}>
      {error ? <div className="error-note" role="alert">{error}</div> : null}
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <div className="field"><label htmlFor="agent-name">Name (@mention)</label><input id="agent-name" className="input mono" required value={form.name} onChange={(e) => set("name", e.target.value.toLowerCase().replace(/[^a-z0-9_-]+/g, "-"))} placeholder="dev" /><span className="tiny muted">Lowercase letters, digits, hyphens or underscores.</span></div>
        <div className="field"><label htmlFor="agent-role">Role</label><select id="agent-role" className="input" value={form.role} onChange={(e) => set("role", e.target.value)}>{ROLES.map((role) => <option key={role.value} value={role.value}>{role.label}</option>)}</select><span className="tiny muted">The kind of work this persona is responsible for.</span></div>
      </div>
      <div className="runner-chain-editor">
        <div className="spread"><div><strong>Runner chain</strong><p className="tiny muted">Order is fallback order. The first runner is the default.</p></div><span className="tiny muted">{form.chain.length} selected</span></div>
        {form.chain.length ? <ol className="runner-chain-list">{form.chain.map((id, index) => { const runner = runners.find((entry) => entry.id === id); return <li key={id}><span className="runner-chain-index">{index + 1}</span><span className="runner-chain-name">{runner?.name ?? id}<small>{index === 0 ? "Default runner" : "Fallback"}{runner?.enabled === false ? " · disabled" : ""}</small></span><div className="stack" style={{ gap: 4 }}><button type="button" className="btn btn--sm" disabled={index === 0} aria-label={`Move ${runner?.name ?? "runner"} earlier`} onClick={() => move(index, -1)}>↑</button><button type="button" className="btn btn--sm" disabled={index === form.chain.length - 1} aria-label={`Move ${runner?.name ?? "runner"} later`} onClick={() => move(index, 1)}>↓</button><button type="button" className="btn btn--sm btn--ghost btn--danger" aria-label={`Remove ${runner?.name ?? "runner"}`} onClick={() => set("chain", form.chain.filter((entry) => entry !== id))}>×</button></div></li>; })}</ol> : <div className="runner-chain-empty">No runner selected. This agent will not be dispatched until a runner is added.</div>}
        {runnersError ? <p className="tiny muted">Runner list unavailable: {errorMessage(runnersError)}</p> : <div className="stack runner-add-control"><select className="input" aria-label="Choose runner to add" value="" disabled={!chainOptions.length} onChange={(e) => { if (e.target.value) set("chain", [...form.chain, e.target.value]); }}><option value="">{chainOptions.length ? "Add a runner…" : runners.length ? "All runners added" : "No runners available"}</option>{chainOptions.map((runner) => <option key={runner.id} value={runner.id}>{runner.name} · {runner.kind}{runner.enabled ? "" : " · disabled"}</option>)}</select><a className="tiny" href="/settings/runners">Manage runners →</a></div>}
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 140px 160px", gap: 12 }}>
        <div className="field"><label htmlFor="agent-concurrency">Concurrency limit</label><input id="agent-concurrency" className="input" type="number" min={1} max={16} value={form.concurrency} onChange={(e) => set("concurrency", e.target.value)} /><span className="tiny muted">Maximum parallel jobs for this agent.</span></div>
        <div className="field"><label>Enabled</label><label className="stack tiny" style={{ padding: "8px 0", cursor: "pointer" }}><input type="checkbox" className="row-check" checked={form.enabled} onChange={(e) => set("enabled", e.target.checked)} />{form.enabled ? "Available" : "Disabled"}</label></div>
      </div>
      <div className="field"><label htmlFor="agent-soul">Soul (markdown)</label><textarea id="agent-soul" className="input mono" rows={7} value={form.soul} onChange={(e) => set("soul", e.target.value)} placeholder="You are the implementation agent…" /><span className="tiny muted">Persona layered under every prompt for this agent.</span></div>
      <details><summary className="tiny muted" style={{ cursor: "pointer" }}>Advanced: skills policy (JSON)</summary><div className="field" style={{ marginTop: 10 }}><label htmlFor="agent-skills">Skills policy</label><textarea id="agent-skills" className="input mono" rows={4} value={form.skills} onChange={(e) => set("skills", e.target.value)} /><span className="tiny muted">Default {JSON.stringify(DEFAULT_SKILLS)}</span></div></details>
      <div className="dialog__row"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn--primary" disabled={busy}>{busy ? "Saving…" : agent ? "Save changes" : "Create agent"}</button></div>
    </form>
  </Dialog>;
}

function errorMessage(error: unknown) {
  const message = apiErrorMessage(error);
  return error && typeof error === "object" && "code" in error ? `${String((error as { code: unknown }).code)} — ${message}` : message;
}
