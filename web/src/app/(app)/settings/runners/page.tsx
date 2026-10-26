"use client";

import { FormEvent, useMemo, useState } from "react";
import { api, type Runner, type RunnerInput, type RunnerKind } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useConfirm } from "@/lib/confirm";
import { useSession } from "@/lib/session";
import { formatDate } from "@/lib/format";
import { SettingsNav } from "@/components/settings-nav";
import { Dialog, EmptyState, ErrorBlock, Field, Skeleton, Toasts, apiErrorMessage } from "@/components/primitives";
import { useT } from "@/lib/i18n";

export default function RunnersSettingsPage() {
  const t = useT();
  const { me } = useSession();
  const { toasts, push } = useToasts();
  const confirm = useConfirm();
  const manager = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const runnersQ = useAsync(() => api.listRunners().then((result) => result.runners), []);
  const variablesQ = useAsync(() => api.getTenantVariables().then((result) => result.variables), []);
  const [editing, setEditing] = useState<Runner | "new" | null>(null);
  const [editingVariables, setEditingVariables] = useState(false);
  const runners = useMemo(() => [...(runnersQ.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [runnersQ.data]);

  async function toggle(runner: Runner) {
    try { await api.updateRunner(runner.id, { enabled: !runner.enabled }); await runnersQ.reload(); }
    catch (error) { push("error", errorMessage(error)); }
  }
  async function remove(runner: Runner) {
    if (!await confirm({ title: t("settings.runners.deleteTitle"), message: t("settings.runners.deleteMessage", { name: runner.name }), confirmLabel: t("settings.runners.delete"), destructive: true })) return;
    try { await api.deleteRunner(runner.id); await runnersQ.reload(); push("info", t("settings.runners.deleted", { name: runner.name })); }
    catch (error) { push("error", errorMessage(error)); }
  }

  return <div style={{ maxWidth: 1120 }}>
    <div className="pagehead"><h1>{t("settings.runners.title")}</h1><span className="pagehead__sub">{t("settings.runners.subtitle")}</span></div>
    <SettingsNav />
    <section className="section">
      <header className="section__head"><div><h2>{t("settings.runners.registry")}</h2><div className="tiny muted" style={{ marginTop: 3 }}>{t("settings.runners.registryHint")}</div></div>
        {manager ? <button className="btn btn--sm btn--primary" style={{ marginLeft: "auto" }} onClick={() => setEditing("new")}>{t("settings.runners.new")}</button> : <span className="tiny muted" style={{ marginLeft: "auto" }}>{t("settings.runners.readonly")}</span>}
      </header>
      <div className="section__body">
        {runnersQ.error ? <ErrorBlock title={t("settings.runners.loadError")} message={errorMessage(runnersQ.error)} retry={() => void runnersQ.reload()} /> : !runnersQ.data ? <Skeleton h={125} /> : !runners.length ? <EmptyState title={t("settings.runners.emptyTitle")} body={t("settings.runners.emptyBody")} action={manager ? <button className="btn btn--primary" onClick={() => setEditing("new")}>{t("settings.runners.first")}</button> : undefined} /> :
          <div className="table-wrap"><table className="data"><thead><tr><th>{t("settings.runners.runner")}</th><th>{t("settings.runners.transport")}</th><th>{t("settings.runners.credentialReference")}</th><th>{t("settings.runners.tenantBudget")}</th><th>{t("settings.runners.enabled")}</th><th>{t("settings.runners.updated")}</th><th><span className="visually-hidden">{t("settings.runners.actions")}</span></th></tr></thead><tbody>
            {runners.map((runner) => <tr key={runner.id}>
              <td><strong className="mono">{runner.name}</strong><div className="tiny muted"><span className="badge-outline">{runner.kind}</span></div></td>
              <td className="runner-transport">{transportSummary(runner, t)}</td>
              <td><code className="runner-credential">{runner.credential_ref || t("settings.runners.notLinked")}</code>{runner.credential_ref ? <><div className="tiny muted">{githubAccount(runner.credential_ref) ? t("settings.runners.githubAccount", { account: githubAccount(runner.credential_ref) }) : t("settings.runners.keychainResolved")}</div></> : <div className="tiny muted">{t("settings.runners.keychainSecret")}</div>}</td>
              <td className="tiny runner-budget">{runner.budget_tokens ? `${runner.budget_tokens.toLocaleString()} ${t("settings.runners.tokens")}` : t("settings.runners.unlimitedTokens")}<br />{runner.budget_cost_usd ? `$${runner.budget_cost_usd.toFixed(2)} ${t("settings.runners.cost")}` : t("settings.runners.unlimitedCost")}</td>
              <td>{manager ? <input type="checkbox" className="row-check" checked={runner.enabled} aria-label={t("settings.runners.enableToggle", { name: runner.name })} onChange={() => void toggle(runner)} /> : <span className="badge-outline">{runner.enabled ? t("settings.runners.on") : t("settings.runners.off")}</span>}</td>
              <td className="tiny muted">{formatDate(runner.updated_at || runner.created_at)}</td>
              <td>{manager ? <span className="stack" style={{ gap: 6 }}><button className="btn btn--sm" onClick={() => setEditing(runner)}>{t("settings.runners.edit")}</button><button className="btn btn--sm btn--ghost btn--danger" onClick={() => void remove(runner)}>{t("settings.runners.delete")}</button></span> : null}</td>
            </tr>)}
          </tbody></table></div>}
        <p className="runner-budget-note">{t("settings.runners.budgetNote")}</p>
      </div>
    </section>
    <TenantVariables variables={variablesQ.data} loading={variablesQ.loading} error={variablesQ.error} reload={variablesQ.reload} canEdit={Boolean(manager)} editing={editingVariables} setEditing={setEditingVariables} push={push} />
    {editing ? <RunnerEditor runner={editing === "new" ? null : editing} onClose={() => setEditing(null)} onSaved={async () => { setEditing(null); await runnersQ.reload(); push("info", t("settings.runners.saved")); }} /> : null}
    <Toasts toasts={toasts} />
  </div>;
}

function transportSummary(runner: Runner, t: ReturnType<typeof useT>) {
  const config = runner.config ?? {};
  if (runner.kind === "cli") {
    const args = Array.isArray(config.args) ? config.args.map(String) : [];
    return <><strong className="mono">{String(config.bin ?? t("settings.runners.binaryNotSet"))}</strong><div className="tiny muted runner-args">{args.length ? args.join(" · ") : t("settings.runners.noArguments")}</div></>;
  }
  return <><strong>{String(config.bin ?? t("settings.runners.binaryNotSetShort"))}</strong><div className="tiny muted mono">{Array.isArray(config.args) ? config.args.map(String).join(" ") : ""}</div></>;
}

function githubAccount(reference: string) {
  return reference.match(/(?:^|\/)github:([^/]+)$/i)?.[1] ?? "";
}

type RunnerForm = { name: string; kind: RunnerKind; bin: string; args: string; env: string; outputFormat: string; timeout: string; credentialRef: string; budgetTokens: string; budgetCost: string; enabled: boolean };
function runnerForm(runner: Runner | null): RunnerForm {
  const c = runner?.config ?? {};
  return { name: runner?.name ?? "", kind: runner?.kind ?? "cli", bin: String(c.bin ?? ""), args: Array.isArray(c.args) ? c.args.map(String).join("\n") : "", env: JSON.stringify(c.env ?? {}, null, 2), outputFormat: String(c.output_format ?? "stream-json"), timeout: String(c.timeout_sec ?? ""), credentialRef: runner?.credential_ref ?? "", budgetTokens: String(runner?.budget_tokens ?? 0), budgetCost: String(runner?.budget_cost_usd ?? 0), enabled: runner?.enabled ?? true };
}
function RunnerEditor({ runner, onClose, onSaved }: { runner: Runner | null; onClose: () => void; onSaved: () => Promise<void> }) {
  const t = useT();
  const [f, setF] = useState(() => runnerForm(runner));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const set = <K extends keyof RunnerForm>(key: K, value: RunnerForm[K]) => setF((current) => ({ ...current, [key]: value }));
  async function submit(event: FormEvent) {
    event.preventDefault(); if (busy) return;
    let config: Record<string, unknown>;
    {
      let env: unknown;
      try { env = JSON.parse(f.env || "{}"); } catch { setError(t("settings.runners.invalidEnv")); return; }
      if (!env || typeof env !== "object" || Array.isArray(env) || Object.values(env).some((value) => typeof value !== "string")) { setError(t("settings.runners.invalidEnvValues")); return; }
      config = { bin: f.bin.trim(), args: f.args.split("\n").map((value) => value.trim()).filter(Boolean), env, output_format: f.outputFormat, ...(f.timeout.trim() ? { timeout_sec: Number(f.timeout) } : {}) };
    }
    const body: RunnerInput = { name: f.name.trim().toLowerCase(), kind: f.kind, config, credential_ref: f.credentialRef.trim(), budget_tokens: Number(f.budgetTokens) || 0, budget_cost_usd: Number(f.budgetCost) || 0, enabled: f.enabled };
    setBusy(true); setError("");
    try { if (runner) await api.updateRunner(runner.id, body); else await api.createRunner(body); await onSaved(); }
    catch (err) { setError(errorMessage(err)); setBusy(false); }
  }
  return <Dialog title={runner ? t("settings.runners.editTitle", { name: runner.name }) : t("settings.runners.newTitle")} description={t("settings.runners.dialogDescription")} onClose={onClose} width={660}>
    <form onSubmit={submit} style={{ display: "grid", gap: 13 }}>
      {error ? <div className="error-note" role="alert">{error}</div> : null}
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <Field label={t("settings.runners.runnerName")}><input className="input mono" required pattern="[a-z0-9][a-z0-9_-]{0,63}" value={f.name} onChange={(e) => set("name", e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ""))} placeholder="claude" /></Field>
        <Field label={t("settings.runners.kind")}><select className="input" value={f.kind} onChange={(e) => set("kind", e.target.value as RunnerKind)}><option value="cli">{t("settings.runners.cliLocal")}</option></select></Field>
      </div>
      <fieldset className="runner-config-fieldset"><legend>{t("settings.runners.cliTransport")}</legend>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}><Field label={t("settings.runners.binary")}><input className="input mono" required value={f.bin} onChange={(e) => set("bin", e.target.value)} placeholder="claude" /></Field><Field label={t("settings.runners.outputFormat")}><select className="input" value={f.outputFormat} onChange={(e) => set("outputFormat", e.target.value)}><option value="stream-json">stream-json</option><option value="text">text</option></select></Field></div>
        <Field label={t("settings.runners.arguments")}><textarea className="input mono" rows={3} value={f.args} onChange={(e) => set("args", e.target.value)} placeholder="--output-format\nstream-json" /></Field>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}><Field label={t("settings.runners.environment")}><textarea className="input mono" rows={3} value={f.env} onChange={(e) => set("env", e.target.value)} placeholder={'{"TERM":"dumb"}'} /></Field><Field label={t("settings.runners.timeout")}><input className="input" type="number" min={0} max={3600} value={f.timeout} onChange={(e) => set("timeout", e.target.value)} placeholder={t("settings.runners.timeoutDefault")} /></Field></div>
      </fieldset>
      <Field label={t("settings.runners.credentialReference")} hint={t("settings.runners.credentialHint")}>
        <input className="input mono" aria-label={t("settings.runners.credentialAria")} value={f.credentialRef} onChange={(e) => set("credentialRef", e.target.value)} placeholder="<service>/<account>" />
      </Field>
      <p className="tiny muted" style={{ margin: "-7px 0 0" }}>{t("settings.runners.secretNotStored")}</p>
      <div className="runner-form-budget"><div className="runner-form-budget__intro"><strong>{t("settings.runners.tenantBudget")}</strong><span>{t("settings.runners.budgetIntro")}</span></div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}><Field label={t("settings.runners.tokenBudget")}><input className="input" type="number" min={0} value={f.budgetTokens} onChange={(e) => set("budgetTokens", e.target.value)} /></Field><Field label={t("settings.runners.costBudget")}><input className="input" type="number" min={0} step="0.01" value={f.budgetCost} onChange={(e) => set("budgetCost", e.target.value)} /></Field></div>
      </div>
      <label className="stack tiny"><input type="checkbox" className="row-check" checked={f.enabled} onChange={(e) => set("enabled", e.target.checked)} />{t("settings.runners.enabledToggle")}</label>
      <div className="dialog__row"><button type="button" className="btn" onClick={onClose}>{t("settings.runners.cancel")}</button><button className="btn btn--primary" type="submit" disabled={busy}>{busy ? t("settings.runners.saving") : runner ? t("settings.runners.saveChanges") : t("settings.runners.create")}</button></div>
    </form>
  </Dialog>;
}

function TenantVariables({ variables, loading, error, reload, canEdit, editing, setEditing, push }: { variables: Record<string, string> | null; loading: boolean; error: Error | null; reload: () => Promise<void>; canEdit: boolean; editing: boolean; setEditing: (value: boolean) => void; push: (kind: "error" | "info", text: string) => void }) {
  const t = useT();
  const [rows, setRows] = useState<[string, string][]>([]);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");
  function begin() { setRows(Object.entries(variables ?? {})); setFormError(""); setEditing(true); }
  async function save(event: FormEvent) {
    event.preventDefault(); const result: Record<string, string> = {};
    for (const [rawName, value] of rows) { const name = rawName.trim(); if (!name && !value) continue; if (!/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name)) { setFormError(t("settings.runners.variableNameHint")); return; } result[name] = value; }
    setBusy(true); setFormError("");
    try { await api.setTenantVariables(result); await reload(); setEditing(false); push("info", t("settings.runners.variablesSaved")); }
    catch (err) { setFormError(errorMessage(err)); } finally { setBusy(false); }
  }
  return <section className="section tenant-variables-section">
    <header className="section__head"><div><h2>{t("settings.runners.variables")}</h2><div className="tiny muted" style={{ marginTop: 3 }}>{t("settings.runners.variablesHint")}</div></div>{canEdit && !editing ? <button className="btn btn--sm" style={{ marginLeft: "auto" }} onClick={begin}>{t("settings.runners.editVariables")}</button> : !canEdit ? <span className="tiny muted" style={{ marginLeft: "auto" }}>{t("settings.runners.readonlyShort")}</span> : null}</header>
    <div className="section__body">
      {error ? <ErrorBlock title={t("settings.runners.loadVariablesError")} message={errorMessage(error)} retry={() => void reload()} /> : loading && !variables ? <Skeleton h={75} /> : editing ? <form onSubmit={save} className="tenant-variable-editor">
        {rows.map(([name, value], index) => <div className="tenant-variable-row" key={`${index}-${name}`}><input className="input mono" aria-label={t("settings.runners.variableName")} value={name} onChange={(e) => setRows((current) => current.map((row, i) => i === index ? [e.target.value, row[1]] : row))} placeholder="RUNNER" /><input className="input" aria-label={t("settings.runners.variableValue", { name: name || t("settings.runners.newVariable") })} value={value} onChange={(e) => setRows((current) => current.map((row, i) => i === index ? [row[0], e.target.value] : row))} placeholder="claude" /><button type="button" className="btn btn--sm btn--ghost btn--danger" aria-label={t("settings.runners.removeVariable", { name })} onClick={() => setRows((current) => current.filter((_, i) => i !== index))}>×</button></div>)}
        <div className="stack"><button className="btn btn--sm" type="button" onClick={() => setRows((current) => [...current, ["", ""]])}>{t("settings.runners.addVariable")}</button><span className="tiny muted">{t("settings.runners.variableUsage")}</span></div>{formError ? <div className="error-note" role="alert">{formError}</div> : null}<div className="stack"><button type="submit" className="btn btn--sm btn--primary" disabled={busy}>{busy ? t("settings.runners.saving") : t("settings.runners.saveDefaults")}</button><button type="button" className="btn btn--sm" onClick={() => setEditing(false)}>{t("settings.runners.cancel")}</button></div>
      </form> : Object.keys(variables ?? {}).length ? <div className="tenant-variable-list">{Object.entries(variables ?? {}).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => <div key={name}><code>${name}</code><span>{value}</span></div>)}</div> : <p className="tiny muted">{t("settings.runners.noVariables")}</p>}
      <p className="tiny muted tenant-vars-note">{t("settings.runners.variablesNote")}</p>
    </div>
  </section>;
}

function errorMessage(error: unknown) {
  return apiErrorMessage(error);
}
