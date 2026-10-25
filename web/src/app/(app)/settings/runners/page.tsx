"use client";

import { FormEvent, useMemo, useState } from "react";
import { api, type Runner, type RunnerInput, type RunnerKind } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useConfirm } from "@/lib/confirm";
import { useSession } from "@/lib/session";
import { formatDate } from "@/lib/format";
import { SettingsNav } from "@/components/settings-nav";
import { Dialog, EmptyState, ErrorBlock, Field, Skeleton, Toasts, apiErrorMessage } from "@/components/primitives";

export default function RunnersSettingsPage() {
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
    if (!await confirm({ title: "Delete runner?", message: `Delete runner “${runner.name}”? Remove it from every agent chain first if it is in use.`, confirmLabel: "Delete", destructive: true })) return;
    try { await api.deleteRunner(runner.id); await runnersQ.reload(); push("info", `Runner “${runner.name}” deleted.`); }
    catch (error) { push("error", errorMessage(error)); }
  }

  return <div style={{ maxWidth: 1120 }}>
    <div className="pagehead"><h1>Runners</h1><span className="pagehead__sub">Runners are the how: a CLI binary or an API model. Agents choose from their ordered runner chain.</span></div>
    <SettingsNav />
    <section className="section">
      <header className="section__head"><div><h2>Runner registry</h2><div className="tiny muted" style={{ marginTop: 3 }}>Credential references point to OS keychain entries. Secret values never belong here.</div></div>
        {manager ? <button className="btn btn--sm btn--primary" style={{ marginLeft: "auto" }} onClick={() => setEditing("new")}>New runner</button> : <span className="tiny muted" style={{ marginLeft: "auto" }}>Read-only · only an owner or admin can change runners.</span>}
      </header>
      <div className="section__body">
        {runnersQ.error ? <ErrorBlock title="Could not load runners" message={errorMessage(runnersQ.error)} retry={() => void runnersQ.reload()} /> : !runnersQ.data ? <Skeleton h={125} /> : !runners.length ? <EmptyState title="No runners yet" body="A runner is the transport an agent uses. Add a CLI binary or an API model, then attach it to an agent’s runner chain." action={manager ? <button className="btn btn--primary" onClick={() => setEditing("new")}>Create your first runner</button> : undefined} /> :
          <div className="table-wrap"><table className="data"><thead><tr><th>Runner</th><th>Transport</th><th>Credential reference</th><th>Tenant budget</th><th>Enabled</th><th>Updated</th><th><span className="visually-hidden">Actions</span></th></tr></thead><tbody>
            {runners.map((runner) => <tr key={runner.id}>
              <td><strong className="mono">{runner.name}</strong><div className="tiny muted"><span className="badge-outline">{runner.kind}</span></div></td>
              <td className="runner-transport">{transportSummary(runner)}</td>
              <td><code className="runner-credential">{runner.credential_ref || "Not linked"}</code>{runner.credential_ref ? <><div className="tiny muted">{githubAccount(runner.credential_ref) ? `GitHub account · ${githubAccount(runner.credential_ref)}` : "Resolved from the OS keychain when used"}</div></> : <div className="tiny muted">Secret lives in the OS keychain</div>}</td>
              <td className="tiny runner-budget">{runner.budget_tokens ? `${runner.budget_tokens.toLocaleString()} tokens` : "Unlimited tokens"}<br />{runner.budget_cost_usd ? `$${runner.budget_cost_usd.toFixed(2)} cost` : "Unlimited cost"}</td>
              <td>{manager ? <input type="checkbox" className="row-check" checked={runner.enabled} aria-label={`Enable or disable ${runner.name}`} onChange={() => void toggle(runner)} /> : <span className="badge-outline">{runner.enabled ? "on" : "off"}</span>}</td>
              <td className="tiny muted">{formatDate(runner.updated_at || runner.created_at)}</td>
              <td>{manager ? <span className="stack" style={{ gap: 6 }}><button className="btn btn--sm" onClick={() => setEditing(runner)}>Edit</button><button className="btn btn--sm btn--ghost btn--danger" onClick={() => void remove(runner)}>Delete</button></span> : null}</td>
            </tr>)}
          </tbody></table></div>}
        <p className="runner-budget-note">Budget means cumulative spend for this tenant; when it is reached, the engine takes the next runner in the agent’s chain. A value of 0 means unlimited.</p>
      </div>
    </section>
    <TenantVariables variables={variablesQ.data} loading={variablesQ.loading} error={variablesQ.error} reload={variablesQ.reload} canEdit={Boolean(manager)} editing={editingVariables} setEditing={setEditingVariables} push={push} />
    {editing ? <RunnerEditor runner={editing === "new" ? null : editing} onClose={() => setEditing(null)} onSaved={async () => { setEditing(null); await runnersQ.reload(); push("info", "Runner saved."); }} /> : null}
    <Toasts toasts={toasts} />
  </div>;
}

function transportSummary(runner: Runner) {
  const config = runner.config ?? {};
  if (runner.kind === "cli") {
    const args = Array.isArray(config.args) ? config.args.map(String) : [];
    return <><strong className="mono">{String(config.bin ?? "(binary not set)")}</strong><div className="tiny muted runner-args">{args.length ? args.join(" · ") : "No arguments"}</div></>;
  }
  return <><strong>{String(config.bin ?? "binary not set")}</strong><div className="tiny muted mono">{Array.isArray(config.args) ? config.args.map(String).join(" ") : ""}</div></>;
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
  const [f, setF] = useState(() => runnerForm(runner));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const set = <K extends keyof RunnerForm>(key: K, value: RunnerForm[K]) => setF((current) => ({ ...current, [key]: value }));
  async function submit(event: FormEvent) {
    event.preventDefault(); if (busy) return;
    let config: Record<string, unknown>;
    {
      let env: unknown;
      try { env = JSON.parse(f.env || "{}"); } catch { setError("CLI env must be a JSON object."); return; }
      if (!env || typeof env !== "object" || Array.isArray(env) || Object.values(env).some((value) => typeof value !== "string")) { setError("CLI env must be a JSON object with string values."); return; }
      config = { bin: f.bin.trim(), args: f.args.split("\n").map((value) => value.trim()).filter(Boolean), env, output_format: f.outputFormat, ...(f.timeout.trim() ? { timeout_sec: Number(f.timeout) } : {}) };
    }
    const body: RunnerInput = { name: f.name.trim().toLowerCase(), kind: f.kind, config, credential_ref: f.credentialRef.trim(), budget_tokens: Number(f.budgetTokens) || 0, budget_cost_usd: Number(f.budgetCost) || 0, enabled: f.enabled };
    setBusy(true); setError("");
    try { if (runner) await api.updateRunner(runner.id, body); else await api.createRunner(body); await onSaved(); }
    catch (err) { setError(errorMessage(err)); setBusy(false); }
  }
  return <Dialog title={runner ? `Edit ${runner.name}` : "New runner"} description="Set up a transport and its tenant-wide spend limits. The secret itself is never stored here." onClose={onClose} width={660}>
    <form onSubmit={submit} style={{ display: "grid", gap: 13 }}>
      {error ? <div className="error-note" role="alert">{error}</div> : null}
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <Field label="Runner name"><input className="input mono" required pattern="[a-z0-9][a-z0-9_-]{0,63}" value={f.name} onChange={(e) => set("name", e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ""))} placeholder="claude" /></Field>
        <Field label="Kind"><select className="input" value={f.kind} onChange={(e) => set("kind", e.target.value as RunnerKind)}><option value="cli">CLI · local binary</option></select></Field>
      </div>
      <fieldset className="runner-config-fieldset"><legend>CLI transport</legend>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}><Field label="Binary (bin)"><input className="input mono" required value={f.bin} onChange={(e) => set("bin", e.target.value)} placeholder="claude" /></Field><Field label="Output format"><select className="input" value={f.outputFormat} onChange={(e) => set("outputFormat", e.target.value)}><option value="stream-json">stream-json</option><option value="text">text</option></select></Field></div>
        <Field label="Arguments (one per line)"><textarea className="input mono" rows={3} value={f.args} onChange={(e) => set("args", e.target.value)} placeholder="--output-format\nstream-json" /></Field>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}><Field label="Environment (JSON object)"><textarea className="input mono" rows={3} value={f.env} onChange={(e) => set("env", e.target.value)} placeholder={'{"TERM":"dumb"}'} /></Field><Field label="Timeout (seconds)"><input className="input" type="number" min={0} max={3600} value={f.timeout} onChange={(e) => set("timeout", e.target.value)} placeholder="0 · default" /></Field></div>
      </fieldset>
      <Field label="Credential reference" hint="the name of a secret held outside the system — a reference, never the secret. Format: `<service>/<account>`, e.g. `ai-company/github:apiary-reviewer`. The value is resolved from the OS keychain at the moment the runner is used.">
        <input className="input mono" aria-label="Credential reference" value={f.credentialRef} onChange={(e) => set("credentialRef", e.target.value)} placeholder="<service>/<account>" />
      </Field>
      <p className="tiny muted" style={{ margin: "-7px 0 0" }}>The secret itself is never stored here. Leave this blank if the runner uses no credential.</p>
      <div className="runner-form-budget"><div className="runner-form-budget__intro"><strong>Tenant budget</strong><span>The cumulative spend for this tenant. When it is reached, the engine takes the next runner in the agent’s chain. 0 means unlimited.</span></div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}><Field label="Token budget · 0 = unlimited"><input className="input" type="number" min={0} value={f.budgetTokens} onChange={(e) => set("budgetTokens", e.target.value)} /></Field><Field label="Cost budget (USD) · 0 = unlimited"><input className="input" type="number" min={0} step="0.01" value={f.budgetCost} onChange={(e) => set("budgetCost", e.target.value)} /></Field></div>
      </div>
      <label className="stack tiny"><input type="checkbox" className="row-check" checked={f.enabled} onChange={(e) => set("enabled", e.target.checked)} />Runner is enabled</label>
      <div className="dialog__row"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn btn--primary" type="submit" disabled={busy}>{busy ? "Saving…" : runner ? "Save changes" : "Create runner"}</button></div>
    </form>
  </Dialog>;
}

function TenantVariables({ variables, loading, error, reload, canEdit, editing, setEditing, push }: { variables: Record<string, string> | null; loading: boolean; error: Error | null; reload: () => Promise<void>; canEdit: boolean; editing: boolean; setEditing: (value: boolean) => void; push: (kind: "error" | "info", text: string) => void }) {
  const [rows, setRows] = useState<[string, string][]>([]);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");
  function begin() { setRows(Object.entries(variables ?? {})); setFormError(""); setEditing(true); }
  async function save(event: FormEvent) {
    event.preventDefault(); const result: Record<string, string> = {};
    for (const [rawName, value] of rows) { const name = rawName.trim(); if (!name && !value) continue; if (!/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name)) { setFormError("Variable names must start with a letter or underscore and contain only letters, digits and underscores (64 characters max)."); return; } result[name] = value; }
    setBusy(true); setFormError("");
    try { await api.setTenantVariables(result); await reload(); setEditing(false); push("info", "Tenant variables saved."); }
    catch (err) { setFormError(errorMessage(err)); } finally { setBusy(false); }
  }
  return <section className="section tenant-variables-section">
    <header className="section__head"><div><h2>Tenant variables</h2><div className="tiny muted" style={{ marginTop: 3 }}>Defaults available to every new run. A run’s own variables win.</div></div>{canEdit && !editing ? <button className="btn btn--sm" style={{ marginLeft: "auto" }} onClick={begin}>Edit variables</button> : !canEdit ? <span className="tiny muted" style={{ marginLeft: "auto" }}>Read-only</span> : null}</header>
    <div className="section__body">
      {error ? <ErrorBlock title="Could not load tenant variables" message={errorMessage(error)} retry={() => void reload()} /> : loading && !variables ? <Skeleton h={75} /> : editing ? <form onSubmit={save} className="tenant-variable-editor">
        {rows.map(([name, value], index) => <div className="tenant-variable-row" key={`${index}-${name}`}><input className="input mono" aria-label="Variable name" value={name} onChange={(e) => setRows((current) => current.map((row, i) => i === index ? [e.target.value, row[1]] : row))} placeholder="RUNNER" /><input className="input" aria-label={`Value for ${name || "new variable"}`} value={value} onChange={(e) => setRows((current) => current.map((row, i) => i === index ? [row[0], e.target.value] : row))} placeholder="claude" /><button type="button" className="btn btn--sm btn--ghost btn--danger" aria-label={`Remove variable ${name}`} onClick={() => setRows((current) => current.filter((_, i) => i !== index))}>×</button></div>)}
        <div className="stack"><button className="btn btn--sm" type="button" onClick={() => setRows((current) => [...current, ["", ""]])}>＋ Add variable</button><span className="tiny muted">Use a variable in a node as $NAME.</span></div>{formError ? <div className="error-note" role="alert">{formError}</div> : null}<div className="stack"><button type="submit" className="btn btn--sm btn--primary" disabled={busy}>{busy ? "Saving…" : "Save defaults"}</button><button type="button" className="btn btn--sm" onClick={() => setEditing(false)}>Cancel</button></div>
      </form> : Object.keys(variables ?? {}).length ? <div className="tenant-variable-list">{Object.entries(variables ?? {}).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => <div key={name}><code>${name}</code><span>{value}</span></div>)}</div> : <p className="tiny muted">No tenant defaults yet. A run may still provide its own variables.</p>}
      <p className="tiny muted tenant-vars-note">These are the default for every new run; a run’s own variables win.</p>
    </div>
  </section>;
}

function errorMessage(error: unknown) {
  const message = apiErrorMessage(error);
  return error && typeof error === "object" && "code" in error ? `${String((error as { code: unknown }).code)} — ${message}` : message;
}
