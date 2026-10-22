"use client";

import { FormEvent, useMemo, useState } from "react";
import { api, type Agent, type AgentInput, type AgentKind } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { formatDate } from "@/lib/format";
import { SettingsNav } from "@/components/settings-nav";
import {
  Dialog,
  EmptyState,
  ErrorBlock,
  Skeleton,
  Toasts,
  apiErrorMessage,
} from "@/components/primitives";

/**
 * Agent registry (Settings → Agents). Owner/admin manage; other roles see a
 * read-only list. No API key value is ever requested or displayed — API-kind
 * agents reference the server-side environment variable via `key_env`.
 */
export default function AgentsSettingsPage() {
  const { me } = useSession();
  const { toasts, push } = useToasts();
  const isManager =
    me?.tenant.role === "owner" || me?.tenant.role === "admin";

  const agentsQ = useAsync(() => api.listAgents().then((r) => r.agents), []);
  const [editing, setEditing] = useState<Agent | "new" | null>(null);

  const sorted = useMemo(
    () =>
      [...(agentsQ.data ?? [])].sort((a, b) =>
        a.name.localeCompare(b.name),
      ),
    [agentsQ.data],
  );

  async function toggle(a: Agent) {
    try {
      await api.updateAgent(a.id, { enabled: !a.enabled });
      await agentsQ.reload();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  async function remove(a: Agent) {
    const ok = window.confirm(
      `Delete @${a.name}? Agents with past job history cannot be deleted (409 agent_in_use) — disable the agent instead if you want to keep its history.`,
    );
    if (!ok) return;
    try {
      await api.deleteAgent(a.id);
      await agentsQ.reload();
      push("info", `Agent @${a.name} deleted.`);
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  return (
    <div style={{ maxWidth: 920 }}>
      <div className="pagehead">
        <h1>Agents</h1>
        <span className="pagehead__sub">
          The registry of workers that mentions and runs dispatch to. Members
          mention them in item chat as @name.
        </span>
      </div>
      <SettingsNav />

      <div className="section">
        <div className="section__head">
          <h2>Registry</h2>
          {isManager ? (
            <button
              type="button"
              className="btn btn--sm"
              style={{ marginLeft: "auto" }}
              onClick={() => setEditing("new")}
            >
              New agent
            </button>
          ) : (
            <span className="tiny muted" style={{ marginLeft: "auto" }}>
              Only the owner or an admin can change agents.
            </span>
          )}
        </div>
        <div className="section__body">
          {agentsQ.error ? (
            <ErrorBlock
              message={agentsQ.error.message}
              retry={() => void agentsQ.reload()}
            />
          ) : !agentsQ.data ? (
            <Skeleton h={110} />
          ) : sorted.length === 0 ? (
            <EmptyState
              title="No agents yet"
              body="Agents do the work: a dev agent implements items, a review agent checks them, a pm agent drafts specs. Register your first agent — most teams start with one CLI runner (like claude or codex) for the dev role. Mentioned in item chat as @name, or launched with the Run agent button."
            />
          ) : (
            <div className="table-wrap">
              <table className="data">
                <thead>
                  <tr>
                    <th scope="col">Agent</th>
                    <th scope="col">Role</th>
                    <th scope="col">Kind</th>
                    <th scope="col">Enabled</th>
                    <th scope="col">Concurrency</th>
                    <th scope="col">Updated</th>
                    <th scope="col">
                      <span className="visually-hidden">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {sorted.map((a) => (
                    <tr key={a.id}>
                      <td>
                        <span className="mono agent-name">@{a.name}</span>
                        {a.soul_md ? (
                          <span
                            className="tiny muted"
                            style={{ marginLeft: 8 }}
                            title={a.soul_md}
                          >
                            soul
                          </span>
                        ) : null}
                      </td>
                      <td className="tiny">{a.role || "—"}</td>
                      <td>
                        <span className="badge-outline">{a.kind}</span>
                      </td>
                      <td>
                        {isManager ? (
                          <input
                            type="checkbox"
                            className="row-check"
                            checked={a.enabled}
                            aria-label={`Enable or disable @${a.name}`}
                            onChange={() => void toggle(a)}
                          />
                        ) : (
                          <span className="badge-outline">
                            {a.enabled ? "on" : "off"}
                          </span>
                        )}
                      </td>
                      <td className="tiny mono">{a.concurrency_limit}</td>
                      <td className="tiny muted">
                        {formatDate(a.updated_at || a.created_at)}
                      </td>
                      <td>
                        {isManager ? (
                          <span className="stack" style={{ gap: 6, flexWrap: "wrap" }}>
                            <button
                              type="button"
                              className="btn btn--sm"
                              onClick={() => setEditing(a)}
                            >
                              Edit
                            </button>
                            <button
                              type="button"
                              className="btn btn--sm btn--ghost btn--danger"
                              aria-label={`Delete agent @${a.name}`}
                              onClick={() => void remove(a)}
                            >
                              Delete
                            </button>
                          </span>
                        ) : null}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>

      {editing ? (
        <AgentEditorDialog
          agent={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await agentsQ.reload();
            push("info", "Agent saved.");
          }}
        />
      ) : null}

      <Toasts toasts={toasts} />
    </div>
  );
}

/* --- create / edit dialog ------------------------------------------------------ */

interface AgentFormState {
  name: string;
  role: string;
  kind: AgentKind;
  concurrency: string;
  enabled: boolean;
  soul: string;
  // cli
  bin: string;
  args: string;
  outputFormat: "stream-json" | "text";
  timeoutSec: string;
  // api
  provider: string;
  model: string;
  baseUrl: string;
  keyEnv: string;
  maxTokens: string;
  // JSON fields
  capabilities: string;
  skillsPolicy: string;
}

const DEFAULT_CAPS: Record<AgentKind, Record<string, unknown>> = {
  cli: { code: true, shell: true, streaming: true, usage: true },
  api: { code: true, streaming: true, usage: true },
};

const DEFAULT_SKILLS = { auto: true, max_attach: 3 };

/** Server-accepted role enum with readable labels. */
export const AGENT_ROLES: { value: string; label: string }[] = [
  { value: "pm", label: "Product manager" },
  { value: "dev", label: "Developer" },
  { value: "reviewer", label: "Reviewer" },
  { value: "qa", label: "QA" },
  { value: "researcher", label: "Researcher" },
];

function formFromAgent(a: Agent | null): AgentFormState {
  const cfg = (a?.config ?? {}) as Record<string, unknown>;
  const str = (v: unknown): string =>
    typeof v === "string" ? v : typeof v === "number" ? String(v) : "";
  const args = Array.isArray(cfg.args)
    ? (cfg.args as unknown[]).every((x) => typeof x === "string")
      ? (cfg.args as string[]).join("\n")
      : ""
    : "";
  return {
    name: a?.name ?? "",
    role: a?.role ?? "dev",
    kind: a?.kind ?? "cli",
    concurrency: a ? String(a.concurrency_limit ?? 2) : "2",
    enabled: a?.enabled ?? true,
    soul: a?.soul_md ?? "",
    bin: str(cfg.bin),
    args,
    outputFormat: cfg.output_format === "text" ? "text" : "stream-json",
    timeoutSec: str(cfg.timeout_sec),
    provider: str(cfg.provider),
    model: str(cfg.model),
    baseUrl: str(cfg.base_url),
    keyEnv: str(cfg.key_env),
    maxTokens: str(cfg.max_tokens),
    capabilities: JSON.stringify(
      a?.capabilities ?? DEFAULT_CAPS[a?.kind ?? "cli"],
      null,
      2,
    ),
    skillsPolicy: JSON.stringify(a?.skills_policy ?? DEFAULT_SKILLS, null, 2),
  };
}

function AgentEditorDialog(props: {
  agent: Agent | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const isNew = props.agent === null;
  const [f, setF] = useState<AgentFormState>(() => formFromAgent(props.agent));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = <K extends keyof AgentFormState>(k: K, v: AgentFormState[K]) =>
    setF((x) => ({ ...x, [k]: v }));

  function switchKind(kind: AgentKind) {
    setF((x) => {
      // Swap in the kind's capability defaults only while the textarea still
      // holds the other kind's untouched defaults or is empty.
      const cur = x.capabilities.trim();
      const other = JSON.stringify(DEFAULT_CAPS[x.kind], null, 2);
      return {
        ...x,
        kind,
        capabilities:
          cur === "" || cur === other
            ? JSON.stringify(DEFAULT_CAPS[kind], null, 2)
            : x.capabilities,
      };
    });
  }

  function parseObject(
    text: string,
    label: string,
  ): { ok: true; value: Record<string, unknown> } | { ok: false; error: string } {
    if (text.trim() === "") return { ok: true, value: {} };
    try {
      const v: unknown = JSON.parse(text);
      if (v === null || typeof v !== "object" || Array.isArray(v)) {
        return { ok: false, error: `${label} must be a JSON object.` };
      }
      return { ok: true, value: v as Record<string, unknown> };
    } catch {
      return { ok: false, error: `${label} is not valid JSON.` };
    }
  }

  function parseArgs(text: string):
    | { ok: true; value: string[] }
    | { ok: false; error: string } {
    const t = text.trim();
    if (!t) return { ok: true, value: [] };
    if (t.startsWith("[")) {
      try {
        const v: unknown = JSON.parse(t);
        if (Array.isArray(v) && v.every((x) => typeof x === "string")) {
          return { ok: true, value: v as string[] };
        }
        return { ok: false, error: "Args must be a JSON array of strings." };
      } catch {
        return { ok: false, error: "Args is not valid JSON." };
      }
    }
    // One argument per line.
    return {
      ok: true,
      value: t
        .split("\n")
        .map((s) => s.trim())
        .filter((s) => s.length > 0),
    };
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;

    const name = f.name.trim().toLowerCase();
    if (!name) {
      setError("Give the agent a name.");
      return;
    }
    const argsRes = f.kind === "cli" ? parseArgs(f.args) : { ok: true as const, value: [] };
    if (!argsRes.ok) {
      setError(argsRes.error);
      return;
    }
    const capsRes = parseObject(f.capabilities, "Capabilities");
    if (!capsRes.ok) {
      setError(capsRes.error);
      return;
    }
    const skillsRes = parseObject(f.skillsPolicy, "Skills policy");
    if (!skillsRes.ok) {
      setError(skillsRes.error);
      return;
    }
    const conc = Math.max(1, Math.floor(Number(f.concurrency)) || 1);

    const config: Record<string, unknown> =
      f.kind === "cli"
        ? {
            bin: f.bin.trim(),
            args: argsRes.value,
            output_format: f.outputFormat,
            ...(f.timeoutSec.trim() !== ""
              ? { timeout_sec: Number(f.timeoutSec) }
              : {}),
          }
        : {
            provider: f.provider.trim(),
            model: f.model.trim(),
            ...(f.baseUrl.trim() !== "" ? { base_url: f.baseUrl.trim() } : {}),
            key_env: f.keyEnv.trim(),
            ...(f.maxTokens.trim() !== ""
              ? { max_tokens: Number(f.maxTokens) }
              : {}),
          };

    const body: AgentInput = {
      name,
      role: f.role.trim(),
      kind: f.kind,
      config,
      capabilities: capsRes.value,
      concurrency_limit: conc,
      enabled: f.enabled,
      soul_md: f.soul,
      skills_policy: skillsRes.value,
    };
    if (isNew) body.fallbacks = [];

    setBusy(true);
    setError(null);
    try {
      if (current === null) await api.createAgent(body);
      else await api.updateAgent(current.id, body);
      await props.onSaved();
    } catch (err) {
      setError(errMessage(err));
      setBusy(false);
    }
  }

  const current = props.agent;
  const currentName = current === null ? "" : current.name;

  return (
    <Dialog
      title={isNew ? "New agent" : `Edit @${currentName}`}
      description={
        isNew
          ? "An agent is a runner the mixer dispatches to — a CLI binary or an API model with a role."
          : `Registry entry for @${currentName}. Unknown fields keep their server-side value.`
      }
      onClose={props.onClose}
      width={640}
    >
      <form onSubmit={submit} style={{ display: "grid", gap: 12 }}>
        {error ? (
          <div className="error-note" role="alert">
            {error}
          </div>
        ) : null}

        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
          <div className="field">
            <label htmlFor="ag-name">Name (the @mention)</label>
            <input
              id="ag-name"
              className="input mono"
              value={f.name}
              onChange={(e) =>
                set("name", e.target.value.toLowerCase().replace(/[^a-z0-9_-]+/g, "-"))
              }
              placeholder="dev"
              required
            />
            <span className="tiny muted">
              Lowercase letters, digits, hyphens. Mentioned as @{f.name || "name"}.
            </span>
          </div>
          <div className="field">
            <label htmlFor="ag-role">Role</label>
            <select
              id="ag-role"
              className="input"
              value={f.role}
              onChange={(e) => set("role", e.target.value)}
            >
              {AGENT_ROLES.map((r) => (
                <option key={r.value} value={r.value}>
                  {r.label}
                </option>
              ))}
            </select>
            <span className="tiny muted">
              Which seat the agent fills; the mixer picks roles per workflow step.
            </span>
          </div>
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 140px", gap: 12 }}>
          <div className="field">
            <label htmlFor="ag-kind">Kind</label>
            <select
              id="ag-kind"
              className="input"
              value={f.kind}
              onChange={(e) => switchKind(e.target.value as AgentKind)}
            >
              <option value="cli">CLI (spawns a binary)</option>
              <option value="api">API (model provider)</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor="ag-conc">Concurrency limit</label>
            <input
              id="ag-conc"
              className="input"
              type="number"
              min={1}
              value={f.concurrency}
              onChange={(e) => set("concurrency", e.target.value)}
            />
            <span className="tiny muted">Parallel runs allowed for this agent.</span>
          </div>
          <div className="field">
            <label htmlFor="ag-enabled">Enabled</label>
            <label
              className="stack tiny"
              style={{ padding: "7px 0", cursor: "pointer" }}
            >
              <input
                id="ag-enabled"
                type="checkbox"
                className="row-check"
                checked={f.enabled}
                onChange={(e) => set("enabled", e.target.checked)}
              />
              {f.enabled ? "Mentionable and runnable" : "Disabled"}
            </label>
          </div>
        </div>

        {f.kind === "cli" ? (
          <fieldset
            style={{
              border: "1px solid var(--border)",
              borderRadius: "var(--radius-sm)",
              padding: "12px",
              display: "grid",
              gap: 12,
              margin: 0,
            }}
          >
            <legend className="tiny muted" style={{ padding: "0 4px" }}>
              CLI config
            </legend>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
              <div className="field">
                <label htmlFor="ag-bin">Binary (bin)</label>
                <input
                  id="ag-bin"
                  className="input mono"
                  value={f.bin}
                  onChange={(e) => set("bin", e.target.value)}
                  placeholder="claude"
                />
              </div>
              <div className="field">
                <label htmlFor="ag-outfmt">Output format</label>
                <select
                  id="ag-outfmt"
                  className="input"
                  value={f.outputFormat}
                  onChange={(e) =>
                    set("outputFormat", e.target.value as "stream-json" | "text")
                  }
                >
                  <option value="stream-json">stream-json</option>
                  <option value="text">text</option>
                </select>
                <span className="tiny muted">
                  stream-json parses structured events; text is a plain log tail.
                </span>
              </div>
            </div>
            <div className="field">
              <label htmlFor="ag-args">Args</label>
              <textarea
                id="ag-args"
                className="input mono"
                rows={3}
                value={f.args}
                onChange={(e) => set("args", e.target.value)}
                placeholder={"-p\n--output-format stream-json\n--verbose"}
              />
              <span className="tiny muted">
                One argument per line — or paste a JSON array of strings.
              </span>
            </div>
            <div className="field">
              <label htmlFor="ag-timeout">Timeout (seconds, optional)</label>
              <input
                id="ag-timeout"
                className="input"
                type="number"
                min={1}
                value={f.timeoutSec}
                onChange={(e) => set("timeoutSec", e.target.value)}
                placeholder="300"
              />
            </div>
          </fieldset>
        ) : (
          <fieldset
            style={{
              border: "1px solid var(--border)",
              borderRadius: "var(--radius-sm)",
              padding: "12px",
              display: "grid",
              gap: 12,
              margin: 0,
            }}
          >
            <legend className="tiny muted" style={{ padding: "0 4px" }}>
              API config
            </legend>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
              <div className="field">
                <label htmlFor="ag-provider">Provider</label>
                <input
                  id="ag-provider"
                  className="input"
                  value={f.provider}
                  onChange={(e) => set("provider", e.target.value)}
                  placeholder="anthropic"
                />
              </div>
              <div className="field">
                <label htmlFor="ag-model">Model</label>
                <input
                  id="ag-model"
                  className="input mono"
                  value={f.model}
                  onChange={(e) => set("model", e.target.value)}
                  placeholder="claude-sonnet-example"
                />
              </div>
            </div>
            <div className="field">
              <label htmlFor="ag-baseurl">Base URL (optional)</label>
              <input
                id="ag-baseurl"
                className="input mono"
                value={f.baseUrl}
                onChange={(e) => set("baseUrl", e.target.value)}
                placeholder="https://api.example.com"
              />
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 160px", gap: 12 }}>
              <div className="field">
                <label htmlFor="ag-keyenv">Key environment variable (key_env)</label>
                <input
                  id="ag-keyenv"
                  className="input mono"
                  value={f.keyEnv}
                  onChange={(e) => set("keyEnv", e.target.value)}
                  placeholder="ANTHROPIC_API_KEY"
                />
                <span className="tiny muted">
                  The name of the env var set on the server — never the key
                  itself. Secrets are never stored in the registry or sent by
                  this page.
                </span>
              </div>
              <div className="field">
                <label htmlFor="ag-maxtokens">Max tokens (optional)</label>
                <input
                  id="ag-maxtokens"
                  className="input"
                  type="number"
                  min={1}
                  value={f.maxTokens}
                  onChange={(e) => set("maxTokens", e.target.value)}
                />
              </div>
            </div>
          </fieldset>
        )}

        <div className="field">
          <div className="spread">
            <label htmlFor="ag-soul">Soul (markdown)</label>
            <span className="tiny muted" aria-live="off">
              {f.soul.length.toLocaleString()} chars
            </span>
          </div>
          <textarea
            id="ag-soul"
            className="input mono"
            rows={7}
            value={f.soul}
            onChange={(e) => set("soul", e.target.value)}
            placeholder={"You are the implementation agent…"}
          />
          <span className="tiny muted">
            Persona layered under every prompt for this agent. Markdown, never
            trimmed away.
          </span>
        </div>

        <details>
          <summary className="tiny muted" style={{ cursor: "pointer" }}>
            Advanced: capabilities and skills policy (JSON)
          </summary>
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "1fr 1fr",
              gap: 12,
              marginTop: 10,
            }}
          >
            <div className="field">
              <label htmlFor="ag-caps">Capabilities</label>
              <textarea
                id="ag-caps"
                className="input mono"
                rows={5}
                value={f.capabilities}
                onChange={(e) => set("capabilities", e.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="ag-skills">Skills policy</label>
              <textarea
                id="ag-skills"
                className="input mono"
                rows={5}
                value={f.skillsPolicy}
                onChange={(e) => set("skillsPolicy", e.target.value)}
              />
              <span className="tiny muted">
                {"Default " + JSON.stringify(DEFAULT_SKILLS)}
              </span>
            </div>
          </div>
        </details>

        <div className="dialog__row">
          <button type="button" className="btn" onClick={props.onClose}>
            Cancel
          </button>
          <button type="submit" className="btn btn--primary" disabled={busy}>
            {busy ? "Saving…" : isNew ? "Create agent" : "Save changes"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

/* --- error copy ---------------------------------------------------------------- */

function errMessage(err: unknown): string {
  const msg = apiErrorMessage(err);
  if (err instanceof Error && "code" in err) {
    const code = (err as { code: string }).code;
    if (code === "invalid_config")
      return `The server rejected the config: ${msg}`;
    if (code === "invalid_soul")
      return `The server rejected the soul file: ${msg}`;
    if (code === "conflict")
      return `That agent name is already taken: ${msg}`;
  }
  return msg;
}
