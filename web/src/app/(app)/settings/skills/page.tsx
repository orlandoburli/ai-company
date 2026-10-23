"use client";

import { FormEvent, useMemo, useState } from "react";
import {
  api,
  type Skill,
  type SkillInput,
  type SkillVersion,
} from "@/lib/api";
import type { Product } from "@/lib/types";
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

export default function SkillsSettingsPage() {
  const { me } = useSession();
  const { toasts, push } = useToasts();
  const isManager = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const skillsQ = useAsync(() => api.listSkills().then((r) => r.skills), []);
  const productsQ = useAsync(() => api.listProducts().then((r) => r.products), []);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [editing, setEditing] = useState<Skill | "new" | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  const skills = useMemo(
    () => [...(skillsQ.data ?? [])].sort((a, b) => a.name.localeCompare(b.name)),
    [skillsQ.data],
  );
  const selectedSkill = skills.find((skill) => skill.id === selectedId) ?? null;
  const detailQ = useAsync(
    () => (selectedId ? api.getSkill(selectedId) : Promise.resolve(null)),
    [selectedId],
  );
  const productNames = useMemo(
    () => new Map((productsQ.data ?? []).map((product) => [product.id, product.name])),
    [productsQ.data],
  );

  async function refreshSelected() {
    await Promise.all([skillsQ.reload(), detailQ.reload()]);
  }

  async function toggle(skill: Skill) {
    setBusyId(skill.id);
    try {
      if (skill.enabled) await api.updateSkill(skill.id, { enabled: false });
      else await api.enableSkill(skill.id);
      await skillsQ.reload();
      if (selectedId === skill.id) await detailQ.reload();
      push("info", `Skill ${skill.enabled ? "disabled" : "enabled"}.`);
    } catch (err) {
      push("error", skillErrorMessage(err));
    } finally {
      setBusyId(null);
    }
  }

  async function remove(skill: Skill) {
    if (!window.confirm(`Delete the skill “${skill.name}” and its version history?`)) return;
    setBusyId(skill.id);
    try {
      await api.deleteSkill(skill.id);
      if (selectedId === skill.id) setSelectedId(null);
      await skillsQ.reload();
      push("info", `Skill “${skill.name}” deleted.`);
    } catch (err) {
      push("error", skillErrorMessage(err));
    } finally {
      setBusyId(null);
    }
  }

  return (
    <div style={{ maxWidth: 1120 }}>
      <div className="pagehead">
        <h1>Skills</h1>
        <span className="pagehead__sub">
          Trusted procedures that can be attached to an agent’s context.
        </span>
      </div>
      <SettingsNav />

      <div className="section">
        <div className="section__head">
          <div>
            <h2>Skill registry</h2>
            <div className="tiny muted" style={{ marginTop: 3 }}>
              The description is what a skill is matched on when deciding whether it applies.
            </div>
          </div>
          {isManager ? (
            <button
              type="button"
              className="btn btn--sm btn--primary"
              style={{ marginLeft: "auto" }}
              onClick={() => setEditing("new")}
            >
              New skill
            </button>
          ) : (
            <span className="tiny muted" style={{ marginLeft: "auto" }}>
              Read-only · only the owner or an admin can change skills.
            </span>
          )}
        </div>
        <div className="section__body">
          {skillsQ.error ? (
            <ErrorBlock
              title="Could not load the skill registry."
              message={skillErrorMessage(skillsQ.error)}
              retry={() => void skillsQ.reload()}
            />
          ) : !skillsQ.data ? (
            <Skeleton h={112} />
          ) : skills.length === 0 ? (
            <EmptyState
              title="No skills yet"
              body="A skill is a named, versioned procedure written as trusted operator text. An owner or admin can create one, describe when it applies, then add and approve a version before enabling it. Agent-written drafts stay inert until a human approves them."
              action={
                isManager ? (
                  <button type="button" className="btn btn--primary" onClick={() => setEditing("new")}>
                    Create the first skill
                  </button>
                ) : (
                  <span className="tiny muted">Only the owner or an admin can create a skill.</span>
                )
              }
            />
          ) : (
            <div className="table-wrap">
              <table className="data skills-table">
                <thead>
                  <tr>
                    <th scope="col">Skill</th>
                    <th scope="col">Scope</th>
                    <th scope="col">Description · match text</th>
                    <th scope="col">Current</th>
                    <th scope="col">Enabled</th>
                    <th scope="col"><span className="visually-hidden">Actions</span></th>
                  </tr>
                </thead>
                <tbody>
                  {skills.map((skill) => (
                    <tr key={skill.id} className={selectedId === skill.id ? "is-selected" : undefined}>
                      <td>
                        <button
                          type="button"
                          className="link-button skills-name"
                          onClick={() => setSelectedId(skill.id)}
                          aria-pressed={selectedId === skill.id}
                        >
                          <strong>{skill.name}</strong>
                          <span className="mono tiny muted">{skill.slug}</span>
                        </button>
                      </td>
                      <td className="tiny">
                        {skill.product_id
                          ? productNames.get(skill.product_id) ?? skill.product_id
                          : "Tenant-wide"}
                      </td>
                      <td className="skills-description">{skill.description || <span className="muted">—</span>}</td>
                      <td className="mono tiny">
                        {skill.current_version == null ? "—" : `v${skill.current_version}`}
                      </td>
                      <td>
                        <span className={`jobchip ${skill.enabled ? "jobchip--ok" : "jobchip--queued"}`}>
                          <span className="jobchip__dot" />{skill.enabled ? "Enabled" : "Disabled"}
                        </span>
                      </td>
                      <td>
                        {isManager ? (
                          <div className="stack skills-actions">
                            <button type="button" className="btn btn--sm" onClick={() => setEditing(skill)}>
                              Edit
                            </button>
                            <button
                              type="button"
                              className="btn btn--sm"
                              disabled={busyId === skill.id}
                              onClick={() => void toggle(skill)}
                            >
                              {skill.enabled ? "Disable" : "Enable"}
                            </button>
                            <button
                              type="button"
                              className="btn btn--sm"
                              onClick={() => {
                                setSelectedId(skill.id);
                                window.setTimeout(() => document.getElementById("skill-new-version")?.focus(), 0);
                              }}
                            >
                              New version
                            </button>
                            <button
                              type="button"
                              className="btn btn--sm btn--ghost btn--danger"
                              disabled={busyId === skill.id}
                              onClick={() => void remove(skill)}
                            >
                              Delete
                            </button>
                          </div>
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

      {selectedId ? (
        <SkillDetail
          skill={selectedSkill}
          data={detailQ.data}
          error={detailQ.error}
          loading={detailQ.loading}
          reload={refreshSelected}
          onRetry={() => void detailQ.reload()}
          isManager={isManager}
          busyId={busyId}
          setBusyId={setBusyId}
          push={push}
        />
      ) : null}

      {editing ? (
        <SkillEditorDialog
          skill={editing === "new" ? null : editing}
          products={productsQ.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={async (skill) => {
            setEditing(null);
            setSelectedId(skill.id);
            await skillsQ.reload();
            push("info", editing === "new" ? "Skill created." : "Skill updated.");
          }}
        />
      ) : null}
      <Toasts toasts={toasts} />
    </div>
  );
}

function SkillDetail(props: {
  skill: Skill | null;
  data: { skill: Skill; versions: SkillVersion[] } | null;
  error: Error | null;
  loading: boolean;
  reload: () => Promise<void>;
  onRetry: () => void;
  isManager: boolean;
  busyId: string | null;
  setBusyId: (id: string | null) => void;
  push: (kind: "error" | "info", text: string) => void;
}) {
  const [versionChoice, setVersionChoice] = useState<number | null>(null);
  const [newContent, setNewContent] = useState("");
  const [saving, setSaving] = useState(false);
  const skill = props.data?.skill ?? props.skill;
  const versions = useMemo(
    () => [...(props.data?.versions ?? [])].sort((a, b) => b.version - a.version),
    [props.data?.versions],
  );
  const currentVersion = versions.find((v) => v.version === skill?.current_version) ?? versions[0] ?? null;
  const shownVersion = versions.find((v) => v.version === versionChoice) ?? currentVersion;
  const pending = shownVersion?.author_kind === "agent_draft" && !shownVersion.approved_at;

  async function createVersion(e: FormEvent) {
    e.preventDefault();
    if (!skill || !newContent.trim() || saving) return;
    setSaving(true);
    props.setBusyId(skill.id);
    try {
      const result = await api.createSkillVersion(skill.id, newContent);
      setNewContent("");
      setVersionChoice(result.version.version);
      await props.reload();
      props.push("info", `Version ${result.version.version} created. It is not current until approved.`);
    } catch (err) {
      props.push("error", skillErrorMessage(err));
    } finally {
      setSaving(false);
      props.setBusyId(null);
    }
  }

  async function approve(version: SkillVersion) {
    if (!skill || !props.isManager) return;
    props.setBusyId(skill.id);
    try {
      await api.approveSkillVersion(skill.id, version.version);
      await props.reload();
      props.push("info", `Version ${version.version} approved.`);
    } catch (err) {
      props.push("error", skillErrorMessage(err));
    } finally {
      props.setBusyId(null);
    }
  }

  return (
    <div className="section skills-detail" aria-label="Skill details">
      <div className="section__head">
        <div>
          <h2>{skill ? skill.name : "Skill details"}</h2>
          <div className="tiny muted" style={{ marginTop: 3 }}>
            {skill ? `/${skill.slug} · ${versions.length} version${versions.length === 1 ? "" : "s"}` : "Loading the version record…"}
          </div>
        </div>
        <span className="tiny muted" style={{ marginLeft: "auto" }}>Version history &amp; trusted content</span>
      </div>
      <div className="section__body">
        {props.error ? (
          <ErrorBlock
            title="Could not load skill details."
            message={skillErrorMessage(props.error)}
            retry={props.onRetry}
          />
        ) : props.loading && !props.data ? (
          <Skeleton h={180} />
        ) : !props.data ? (
          <p className="tiny muted">No detail record is available yet.</p>
        ) : (
          <div className="skills-detail-grid">
            <div className="skills-history">
              <div className="spread">
                <h3 className="skills-subhead">Version history</h3>
                {pending ? <span className="jobchip jobchip--warn">Awaiting human approval</span> : null}
              </div>
              {versions.length === 0 ? (
                <p className="tiny muted">No versions yet. Add the first procedure below.</p>
              ) : (
                <div className="table-wrap">
                  <table className="data skills-version-table">
                    <thead><tr><th>Version</th><th>Author</th><th>Approved by</th><th>Fingerprint</th></tr></thead>
                    <tbody>
                      {versions.map((version) => {
                        const isCurrent = version.version === currentVersion?.version;
                        const isPending = version.author_kind === "agent_draft" && !version.approved_at;
                        return (
                          <tr key={version.id} className={version.version === shownVersion?.version ? "is-selected" : undefined}>
                            <td>
                              <button type="button" className="link-button mono tiny" onClick={() => setVersionChoice(version.version)}>
                                v{version.version}{isCurrent ? <span className="skills-current">current</span> : null}
                                {isPending ? <span className="skills-pending"> · draft</span> : null}
                              </button>
                            </td>
                            <td className="tiny">{version.author_kind}</td>
                            <td className="tiny muted">
                              {version.approved_at
                                ? `${version.approved_by ?? version.approved_by_user_id ?? "Human"} · ${formatDate(version.approved_at)}`
                                : "Not approved"}
                            </td>
                            <td>
                              <code className="skills-sha" title={version.content_sha || "No content fingerprint returned"}>
                                {version.content_sha ? shortSha(version.content_sha) : "—"}
                              </code>
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}

              {shownVersion ? (
                <div className="skills-version-content">
                  <div className="spread">
                    <h3 className="skills-subhead">
                      {shownVersion.version === currentVersion?.version ? "Current content" : `Version ${shownVersion.version} content`}
                    </h3>
                    <span className={`jobchip ${pending ? "jobchip--warn" : "jobchip--ok"}`}>
                      <span className="jobchip__dot" />{pending ? "Inert draft" : "Approved"}
                    </span>
                  </div>
                  {pending ? (
                    <div className="skills-warning" role="note">
                      This agent-written version is inert until a human approves it. A human approves this version, and from then on it can be attached to an agent’s context.
                    </div>
                  ) : null}
                  <pre className="skills-content" aria-label={`Read-only content for version ${shownVersion.version}`}>
                    {shownVersion.content_md || "(empty version content)"}
                  </pre>
                  {pending && props.isManager ? (
                    <button
                      type="button"
                      className="btn btn--primary"
                      disabled={props.busyId === skill?.id}
                      onClick={() => void approve(shownVersion)}
                    >
                      Approve v{shownVersion.version}
                    </button>
                  ) : pending ? (
                    <p className="tiny muted">Only the owner or an admin can approve this draft.</p>
                  ) : null}
                </div>
              ) : null}
            </div>

            <form className="skills-new-version" onSubmit={createVersion}>
              <div className="spread">
                <h3 className="skills-subhead">New version</h3>
                <span className="tiny muted" aria-live="off">{newContent.length.toLocaleString()} characters</span>
              </div>
              {props.isManager ? (
                <>
                  <label className="visually-hidden" htmlFor="skill-new-version">New skill version content</label>
                  <textarea
                    id="skill-new-version"
                    className="input mono skills-editor"
                    value={newContent}
                    onChange={(e) => setNewContent(e.target.value)}
                    placeholder="Write the procedure, step by step…"
                    disabled={saving}
                  />
                  <div className="skills-warning" role="note">
                    A new version does not replace the current one until it is approved. Approved versions are immutable; create another version to make changes.
                  </div>
                  <button type="submit" className="btn btn--primary" disabled={saving || !newContent.trim()}>
                    {saving ? "Saving…" : "Save new version"}
                  </button>
                </>
              ) : (
                <div className="skills-readonly-note">
                  Members and guests can review the registry, but only the owner or an admin can add versions or approve drafts.
                </div>
              )}
            </form>
          </div>
        )}
      </div>
    </div>
  );
}

function SkillEditorDialog(props: {
  skill: Skill | null;
  products: Product[];
  onClose: () => void;
  onSaved: (skill: Skill) => Promise<void>;
}) {
  const isNew = props.skill === null;
  const [name, setName] = useState(props.skill?.name ?? "");
  const [slug, setSlug] = useState(props.skill?.slug ?? "");
  const [description, setDescription] = useState(props.skill?.description ?? "");
  const [productId, setProductId] = useState(props.skill?.product_id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    const next = { name: name.trim(), slug: slug.trim(), description, product_id: productId };
    if (!next.name || !next.slug) {
      setError("Name and slug are required.");
      return;
    }
    const body: SkillInput = isNew
      ? next
      : changedSkillFields(props.skill!, next);
    if (!isNew && Object.keys(body).length === 0) {
      await props.onSaved(props.skill!);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const result = isNew
        ? await api.createSkill(body)
        : await api.updateSkill(props.skill!.id, body);
      await props.onSaved(result.skill);
    } catch (err) {
      setError(skillErrorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Dialog
      title={isNew ? "New skill" : `Edit ${props.skill?.name ?? "skill"}`}
      description="Skills are trusted operator procedures. Keep the description specific: it is the text used to match a skill to work."
      onClose={props.onClose}
      width={560}
    >
      <form onSubmit={submit} className="skills-form">
        {error ? <div className="error-note" role="alert">{error}</div> : null}
        <div className="skills-form-row">
          <div className="field">
            <label htmlFor="skill-name">Name</label>
            <input id="skill-name" className="input" value={name} onChange={(e) => setName(e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor="skill-slug">Slug</label>
            <input
              id="skill-slug"
              className="input mono"
              value={slug}
              onChange={(e) => setSlug(e.target.value.toLowerCase().replace(/[^a-z0-9_-]+/g, "-"))}
              required
            />
          </div>
        </div>
        <div className="field">
          <label htmlFor="skill-scope">Scope</label>
          <select id="skill-scope" className="input" value={productId} onChange={(e) => setProductId(e.target.value)}>
            <option value="">Tenant-wide</option>
            {props.products.map((product) => <option key={product.id} value={product.id}>{product.name}</option>)}
          </select>
          <span className="tiny muted">Choose a product to limit where this skill can match. Leave tenant-wide for shared procedures.</span>
        </div>
        <div className="field">
          <label htmlFor="skill-description">Description · used for matching</label>
          <textarea
            id="skill-description"
            className="input"
            rows={3}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="When should an agent use this procedure?"
          />
        </div>
        <div className="dialog__row">
          <button type="button" className="btn" onClick={props.onClose}>Cancel</button>
          <button type="submit" className="btn btn--primary" disabled={busy}>
            {busy ? "Saving…" : isNew ? "Create skill" : "Save changes"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function changedSkillFields(skill: Skill, next: {
  name: string;
  slug: string;
  description: string;
  product_id: string;
}): SkillInput {
  const changed: SkillInput = {};
  if (skill.name !== next.name) changed.name = next.name;
  if (skill.slug !== next.slug) changed.slug = next.slug;
  if (skill.description !== next.description) changed.description = next.description;
  if ((skill.product_id ?? "") !== next.product_id) changed.product_id = next.product_id;
  return changed;
}

function shortSha(sha: string): string {
  return sha.length > 12 ? `${sha.slice(0, 12)}…` : sha;
}

function skillErrorMessage(err: unknown): string {
  const message = apiErrorMessage(err);
  if (err instanceof Error && "code" in err) {
    const code = (err as { code: string }).code;
    // Keep the backend's contract codes visible; they are useful to operators
    // and should not be hidden behind friendlier but ambiguous copy.
    return `${code}: ${message}`;
  }
  return message;
}
