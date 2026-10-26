"use client";

import { FormEvent, useMemo, useState } from "react";
import { api, type Agent, type AgentInput, type Runner } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useConfirm } from "@/lib/confirm";
import { useSession } from "@/lib/session";
import { formatDate } from "@/lib/format";
import { SettingsNav } from "@/components/settings-nav";
import { Dialog, EmptyState, ErrorBlock, Skeleton, Toasts, apiErrorMessage } from "@/components/primitives";
import { useT } from "@/lib/i18n";

const ROLES = [
  { value: "pm", key: "settings.agents.productManager" }, { value: "dev", key: "settings.agents.developer" },
  { value: "reviewer", key: "settings.agents.reviewer" }, { value: "qa", key: "settings.agents.qa" },
  { value: "researcher", key: "settings.agents.researcher" },
];
const DEFAULT_SKILLS = { auto: true, max_attach: 3 };

export default function AgentsSettingsPage() {
  const t = useT();
  const { me } = useSession();
  const { toasts, push } = useToasts();
  const confirm = useConfirm();
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
    if (!await confirm({ title: t("settings.agents.deleteTitle"), message: t("settings.agents.deleteMessage", { name: agent.name }), confirmLabel: t("settings.agents.delete"), destructive: true })) return;
    try { await api.deleteAgent(agent.id); await agentsQ.reload(); push("info", t("settings.agents.deleted", { name: agent.name })); }
    catch (error) { push("error", errorMessage(error)); }
  }

  return <div style={{ maxWidth: 1000 }}>
    <div className="pagehead"><h1>{t("settings.agents.title")}</h1><span className="pagehead__sub">{t("settings.agents.subtitle")}</span></div>
    <SettingsNav />
    <section className="section">
      <header className="section__head"><div><h2>{t("settings.agents.registry")}</h2><div className="tiny muted" style={{ marginTop: 3 }}>{t("settings.agents.registryHint")}</div></div>
        {isManager ? <button type="button" className="btn btn--sm btn--primary" style={{ marginLeft: "auto" }} onClick={() => setEditing("new")}>{t("settings.agents.new")}</button> : <span className="tiny muted" style={{ marginLeft: "auto" }}>{t("settings.agents.readonly")}</span>}
      </header>
      <div className="section__body">
        {agentsQ.error ? <ErrorBlock title={t("settings.agents.loadError")} message={errorMessage(agentsQ.error)} retry={() => void agentsQ.reload()} /> : !agentsQ.data ? <Skeleton h={110} /> : sorted.length === 0 ? <EmptyState title={t("settings.agents.emptyTitle")} body={t("settings.agents.emptyBody")} action={isManager ? <button className="btn btn--primary" onClick={() => setEditing("new")}>{t("settings.agents.first")}</button> : undefined} /> :
          <div className="table-wrap"><table className="data"><thead><tr><th>{t("settings.agents.agent")}</th><th>{t("settings.agents.role")}</th><th>{t("settings.agents.runnerChain")}</th><th>{t("settings.agents.enabled")}</th><th>{t("settings.agents.concurrency")}</th><th>{t("settings.agents.updated")}</th><th><span className="visually-hidden">{t("settings.agents.actions")}</span></th></tr></thead><tbody>
            {sorted.map((agent) => <tr key={agent.id}>
              <td><span className="mono agent-name">@{agent.name}</span>{agent.soul_md ? <span className="tiny muted" style={{ marginLeft: 8 }} title={agent.soul_md}>{t("settings.agents.soul")}</span> : null}</td>
              <td className="tiny">{agent.role || "—"}</td>
              <td>{agent.runners?.length ? <span className="runner-chain-inline">{agent.runners.map((runner, index) => { const credentialRef = runnersQ.data?.find((entry) => entry.id === runner.id)?.credential_ref; return <span key={runner.id}><span className={index === 0 ? "runner-first" : "tiny muted"}>{runner.name}</span>{credentialRef ? <small className="runner-chain-credential">{t("settings.agents.uses", { reference: credentialRef })}</small> : null}{index < agent.runners.length - 1 ? <span className="muted"> → </span> : null}</span>; })}</span> : <span className="runner-missing">{t("settings.agents.noRunner")}</span>}</td>
              <td>{isManager ? <input type="checkbox" className="row-check" checked={agent.enabled} aria-label={t("settings.agents.enableToggle", { name: agent.name })} onChange={() => void toggle(agent)} /> : <span className="badge-outline">{agent.enabled ? t("settings.agents.on") : t("settings.agents.off")}</span>}</td>
              <td className="tiny mono">{agent.concurrency_limit}</td><td className="tiny muted">{formatDate(agent.updated_at || agent.created_at)}</td>
              <td>{isManager ? <span className="stack" style={{ gap: 6 }}><button className="btn btn--sm" onClick={() => setEditing(agent)}>{t("settings.agents.edit")}</button><button className="btn btn--sm btn--ghost btn--danger" onClick={() => void remove(agent)}>{t("settings.agents.delete")}</button></span> : null}</td>
            </tr>)}
          </tbody></table></div>}
      </div>
    </section>
    {editing ? <AgentEditor agent={editing === "new" ? null : editing} runners={runnersQ.data ?? []} runnersError={runnersQ.error} onClose={() => setEditing(null)} onSaved={async () => { setEditing(null); await agentsQ.reload(); push("info", t("settings.agents.saved")); }} /> : null}
    <Toasts toasts={toasts} />
  </div>;
}

type FormState = { name: string; role: string; concurrency: string; enabled: boolean; soul: string; chain: string[]; skills: string };
function agentForm(agent: Agent | null): FormState {
  return { name: agent?.name ?? "", role: agent?.role ?? "dev", concurrency: String(agent?.concurrency_limit ?? 2), enabled: agent?.enabled ?? true, soul: agent?.soul_md ?? "", chain: agent?.runners?.map((runner) => runner.id) ?? [], skills: JSON.stringify(agent?.skills_policy ?? DEFAULT_SKILLS, null, 2) };
}
function AgentEditor({ agent, runners, runnersError, onClose, onSaved }: { agent: Agent | null; runners: Runner[]; runnersError: Error | null; onClose: () => void; onSaved: () => Promise<void> }) {
  const t = useT();
  const [form, setForm] = useState(() => agentForm(agent));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm((current) => ({ ...current, [key]: value }));
  function move(index: number, direction: -1 | 1) { const next = [...form.chain]; const target = index + direction; if (target < 0 || target >= next.length) return; [next[index], next[target]] = [next[target], next[index]]; set("chain", next); }
  async function submit(event: FormEvent) {
    event.preventDefault(); if (busy) return;
    let skills: Record<string, unknown>;
    try { const parsed: unknown = JSON.parse(form.skills || "{}"); if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(); skills = parsed as Record<string, unknown>; }
    catch { setError(t("settings.agents.invalidSkills")); return; }
    const body: AgentInput = { name: form.name.trim().toLowerCase(), role: form.role, runners: form.chain, concurrency_limit: Math.max(1, Number(form.concurrency) || 1), enabled: form.enabled, soul_md: form.soul, skills_policy: skills, capabilities: agent?.capabilities ?? { code: true, shell: true, streaming: true, usage: true } };
    setBusy(true); setError("");
    try { if (agent) await api.updateAgent(agent.id, body); else await api.createAgent(body); await onSaved(); }
    catch (err) { setError(errorMessage(err)); setBusy(false); }
  }
  const chainOptions = runners.filter((runner) => !form.chain.includes(runner.id));
  return <Dialog title={agent ? t("settings.agents.editTitle", { name: agent.name }) : t("settings.agents.newTitle")} description={t("settings.agents.dialogDescription")} onClose={onClose} width={680}>
    <form onSubmit={submit} style={{ display: "grid", gap: 14 }}>
      {error ? <div className="error-note" role="alert">{error}</div> : null}
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <div className="field"><label htmlFor="agent-name">{t("settings.agents.nameMention")}</label><input id="agent-name" className="input mono" required value={form.name} onChange={(e) => set("name", e.target.value.toLowerCase().replace(/[^a-z0-9_-]+/g, "-"))} placeholder={t("settings.agents.namePlaceholder")} /><span className="tiny muted">{t("settings.agents.nameHint")}</span></div>
        <div className="field"><label htmlFor="agent-role">{t("settings.agents.role")}</label><select id="agent-role" className="input" value={form.role} onChange={(e) => set("role", e.target.value)}>{ROLES.map((role) => <option key={role.value} value={role.value}>{t(role.key as "settings.agents.productManager")}</option>)}</select><span className="tiny muted">{t("settings.agents.roleHint")}</span></div>
      </div>
      <div className="runner-chain-editor">
        <div className="spread"><div><strong>{t("settings.agents.runnerChain")}</strong><p className="tiny muted">{t("settings.agents.orderHint")}</p></div><span className="tiny muted">{t("settings.agents.selected", { count: form.chain.length })}</span></div>
        {form.chain.length ? <ol className="runner-chain-list">{form.chain.map((id, index) => { const runner = runners.find((entry) => entry.id === id); const runnerName = runner?.name ?? "runner"; return <li key={id}><span className="runner-chain-index">{index + 1}</span><span className="runner-chain-name">{runner?.name ?? id}<small>{index === 0 ? t("settings.agents.defaultRunner") : t("settings.agents.fallback")}{runner?.enabled === false ? ` · ${t("settings.agents.disabled")}` : ""}</small></span><div className="stack" style={{ gap: 4 }}><button type="button" className="btn btn--sm" disabled={index === 0} aria-label={t("settings.agents.moveEarlier", { name: runnerName })} onClick={() => move(index, -1)}>↑</button><button type="button" className="btn btn--sm" disabled={index === form.chain.length - 1} aria-label={t("settings.agents.moveLater", { name: runnerName })} onClick={() => move(index, 1)}>↓</button><button type="button" className="btn btn--sm btn--ghost btn--danger" aria-label={t("settings.agents.removeRunner", { name: runnerName })} onClick={() => set("chain", form.chain.filter((entry) => entry !== id))}>×</button></div></li>; })}</ol> : <div className="runner-chain-empty">{t("settings.agents.noRunnerSelected")}</div>}
        {runnersError ? <p className="tiny muted">{t("settings.agents.runnerListUnavailable", { message: errorMessage(runnersError) })}</p> : <div className="stack runner-add-control"><select className="input" aria-label={t("settings.agents.addRunner")} value="" disabled={!chainOptions.length} onChange={(e) => { if (e.target.value) set("chain", [...form.chain, e.target.value]); }}><option value="">{chainOptions.length ? t("settings.agents.addRunner") : runners.length ? t("settings.agents.allRunnersAdded") : t("settings.agents.noRunnersAvailable")}</option>{chainOptions.map((runner) => <option key={runner.id} value={runner.id}>{runner.name} · {runner.kind}{runner.enabled ? "" : ` · ${t("settings.agents.disabled")}`}</option>)}</select><a className="tiny" href="/settings/runners">{t("settings.agents.manageRunners")}</a></div>}
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 140px 160px", gap: 12 }}>
        <div className="field"><label htmlFor="agent-concurrency">{t("settings.agents.concurrencyLimit")}</label><input id="agent-concurrency" className="input" type="number" min={1} max={16} value={form.concurrency} onChange={(e) => set("concurrency", e.target.value)} /><span className="tiny muted">{t("settings.agents.concurrencyHint")}</span></div>
        <div className="field"><label>{t("settings.agents.enabled")}</label><label className="stack tiny" style={{ padding: "8px 0", cursor: "pointer" }}><input type="checkbox" className="row-check" checked={form.enabled} onChange={(e) => set("enabled", e.target.checked)} />{form.enabled ? t("settings.agents.available") : t("settings.agents.disabled")}</label></div>
      </div>
      <div className="field"><label htmlFor="agent-soul">{t("settings.agents.soul")}</label><textarea id="agent-soul" className="input mono" rows={7} value={form.soul} onChange={(e) => set("soul", e.target.value)} placeholder={t("settings.agents.soulPlaceholder")} /><span className="tiny muted">{t("settings.agents.soulHint")}</span></div>
      <details><summary className="tiny muted" style={{ cursor: "pointer" }}>{t("settings.agents.advancedSkills")}</summary><div className="field" style={{ marginTop: 10 }}><label htmlFor="agent-skills">{t("settings.agents.skillsPolicy")}</label><textarea id="agent-skills" className="input mono" rows={4} value={form.skills} onChange={(e) => set("skills", e.target.value)} /><span className="tiny muted">{t("settings.agents.default", { value: JSON.stringify(DEFAULT_SKILLS) })}</span></div></details>
      <div className="dialog__row"><button type="button" className="btn" onClick={onClose}>{t("settings.agents.cancel")}</button><button type="submit" className="btn btn--primary" disabled={busy}>{busy ? t("settings.agents.saving") : agent ? t("settings.agents.saveChanges") : t("settings.agents.create")}</button></div>
    </form>
  </Dialog>;
}

function errorMessage(error: unknown) {
  return apiErrorMessage(error);
}
