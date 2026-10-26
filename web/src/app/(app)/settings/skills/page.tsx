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
import { useConfirm } from "@/lib/confirm";
import { useSession } from "@/lib/session";
import { formatDate } from "@/lib/format";
import { SettingsNav } from "@/components/settings-nav";
import { useT } from "@/lib/i18n";
import {
  Dialog,
  EmptyState,
  ErrorBlock,
  Skeleton,
  Toasts,
  apiErrorMessage,
} from "@/components/primitives";

export default function SkillsSettingsPage() {
  const t = useT();
  const { me } = useSession();
  const { toasts, push } = useToasts();
  const confirm = useConfirm();
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
      push("info", skill.enabled ? t("settings.skills.disabledToast") : t("settings.skills.enabledToast"));
    } catch (err) {
      push("error", skillErrorMessage(err));
    } finally {
      setBusyId(null);
    }
  }

  async function remove(skill: Skill) {
    if (!await confirm({ title: t("settings.skills.deleteTitle"), message: t("settings.skills.deleteMessage", { name: skill.name }), confirmLabel: t("settings.skills.delete"), destructive: true })) return;
    setBusyId(skill.id);
    try {
      await api.deleteSkill(skill.id);
      if (selectedId === skill.id) setSelectedId(null);
      await skillsQ.reload();
      push("info", t("settings.skills.deleted", { name: skill.name }));
    } catch (err) {
      push("error", skillErrorMessage(err));
    } finally {
      setBusyId(null);
    }
  }

  return (
    <div style={{ maxWidth: 1120 }}>
      <div className="pagehead">
          <h1>{t("settings.skills.title")}</h1>
        <span className="pagehead__sub">
          {t("settings.skills.subtitle")}
        </span>
      </div>
      <SettingsNav />

      <div className="section">
        <div className="section__head">
          <div>
            <h2>{t("settings.skills.registry")}</h2>
            <div className="tiny muted" style={{ marginTop: 3 }}>
              {t("settings.skills.registryHint")}
            </div>
          </div>
          {isManager ? (
            <button
              type="button"
              className="btn btn--sm btn--primary"
              style={{ marginLeft: "auto" }}
              onClick={() => setEditing("new")}
            >
              {t("settings.skills.new")}
            </button>
          ) : (
            <span className="tiny muted" style={{ marginLeft: "auto" }}>
              {t("settings.skills.readonly")}
            </span>
          )}
        </div>
        <div className="section__body">
          {skillsQ.error ? (
            <ErrorBlock
              title={t("settings.skills.loadError")}
              message={skillErrorMessage(skillsQ.error)}
              retry={() => void skillsQ.reload()}
            />
          ) : !skillsQ.data ? (
            <Skeleton h={112} />
          ) : skills.length === 0 ? (
            <EmptyState
              title={t("settings.skills.emptyTitle")}
              body={t("settings.skills.emptyBody")}
              action={
                isManager ? (
                  <button type="button" className="btn btn--primary" onClick={() => setEditing("new")}>
                    {t("settings.skills.first")}
                  </button>
                ) : (
                  <span className="tiny muted">{t("settings.skills.createPermission")}</span>
                )
              }
            />
          ) : (
            <div className="table-wrap">
              <table className="data skills-table">
                <thead>
                  <tr>
                    <th scope="col">{t("settings.skills.skill")}</th>
                    <th scope="col">{t("settings.skills.scope")}</th>
                    <th scope="col">{t("settings.skills.descriptionMatch")}</th>
                    <th scope="col">{t("settings.skills.current")}</th>
                    <th scope="col">{t("settings.skills.enabled")}</th>
                    <th scope="col"><span className="visually-hidden">{t("settings.skills.actions")}</span></th>
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
                          : t("settings.skills.tenantWide")}
                      </td>
                      <td className="skills-description">{skill.description || <span className="muted">—</span>}</td>
                      <td className="mono tiny">
                        {skill.current_version == null ? "—" : `v${skill.current_version}`}
                      </td>
                      <td>
                        <span className={`jobchip ${skill.enabled ? "jobchip--ok" : "jobchip--queued"}`}>
                          <span className="jobchip__dot" />{skill.enabled ? t("settings.skills.enabled") : t("settings.skills.disabled")}
                        </span>
                      </td>
                      <td>
                        {isManager ? (
                          <div className="stack skills-actions">
                            <button type="button" className="btn btn--sm" onClick={() => setEditing(skill)}>
                              {t("settings.skills.edit")}
                            </button>
                            <button
                              type="button"
                              className="btn btn--sm"
                              disabled={busyId === skill.id}
                              onClick={() => void toggle(skill)}
                            >
                              {skill.enabled ? t("settings.skills.disable") : t("settings.skills.enable")}
                            </button>
                            <button
                              type="button"
                              className="btn btn--sm"
                              onClick={() => {
                                setSelectedId(skill.id);
                                window.setTimeout(() => document.getElementById("skill-new-version")?.focus(), 0);
                              }}
                            >
                              {t("settings.skills.newVersion")}
                            </button>
                            <button
                              type="button"
                              className="btn btn--sm btn--ghost btn--danger"
                              disabled={busyId === skill.id}
                              onClick={() => void remove(skill)}
                            >
                              {t("settings.skills.delete")}
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
          key={selectedId}
          skill={selectedSkill}
          data={detailQ.data?.skill.id === selectedId ? detailQ.data : null}
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
            push("info", editing === "new" ? t("settings.skills.createdToast") : t("settings.skills.updatedToast"));
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
  const t = useT();
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
      props.push("info", t("settings.skills.versionCreated", { version: result.version.version }));
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
      props.push("info", t("settings.skills.versionApproved", { version: version.version }));
    } catch (err) {
      props.push("error", skillErrorMessage(err));
    } finally {
      props.setBusyId(null);
    }
  }

  return (
    <div className="section skills-detail" aria-label={t("settings.skills.detailsAria")}>
      <div className="section__head">
        <div>
          <h2>{skill ? skill.name : t("settings.skills.details")}</h2>
          <div className="tiny muted" style={{ marginTop: 3 }}>
            {skill ? `/${skill.slug} · ${t("settings.skills.versionCount", { count: versions.length })}` : t("settings.skills.loadingVersion")}
          </div>
        </div>
        <span className="tiny muted" style={{ marginLeft: "auto" }}>{t("settings.skills.versionHistoryTrusted")}</span>
      </div>
      <div className="section__body">
        {props.error ? (
          <ErrorBlock
            title={t("settings.skills.loadDetailsError")}
            message={skillErrorMessage(props.error)}
            retry={props.onRetry}
          />
        ) : props.loading && !props.data ? (
          <Skeleton h={180} />
        ) : !props.data ? (
          <p className="tiny muted">{t("settings.skills.noDetail")}</p>
        ) : (
          <div className="skills-detail-grid">
            <div className="skills-history">
              <div className="spread">
                <h3 className="skills-subhead">{t("settings.skills.versionHistory")}</h3>
                {pending ? <span className="jobchip jobchip--warn">{t("settings.skills.awaitingApproval")}</span> : null}
              </div>
              {versions.length === 0 ? (
                <p className="tiny muted">{t("settings.skills.noVersions")}</p>
              ) : (
                <div className="table-wrap">
                  <table className="data skills-version-table">
                    <thead><tr><th>{t("settings.skills.version")}</th><th>{t("settings.skills.author")}</th><th>{t("settings.skills.approvedBy")}</th><th>{t("settings.skills.fingerprint")}</th></tr></thead>
                    <tbody>
                      {versions.map((version) => {
                        const isCurrent = version.version === currentVersion?.version;
                        const isPending = version.author_kind === "agent_draft" && !version.approved_at;
                        return (
                          <tr key={version.id} className={version.version === shownVersion?.version ? "is-selected" : undefined}>
                            <td>
                              <button type="button" className="link-button mono tiny" onClick={() => setVersionChoice(version.version)}>
                                v{version.version}{isCurrent ? <span className="skills-current">{t("settings.skills.currentVersion")}</span> : null}
                                {isPending ? <span className="skills-pending">{t("settings.skills.draft")}</span> : null}
                              </button>
                            </td>
                            <td className="tiny">{authorLabel(version.author_kind, t)}</td>
                            <td className="tiny muted">
                              {version.approved_at
                                ? `${version.approved_by ?? version.approved_by_user_id ?? t("settings.skills.human")} · ${formatDate(version.approved_at)}`
                                : t("settings.skills.notApproved")}
                            </td>
                            <td>
                              <code className="skills-sha" title={version.content_sha || t("settings.skills.noFingerprint")}>
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
                      {shownVersion.version === currentVersion?.version ? t("settings.skills.currentContent") : t("settings.skills.versionContent", { version: shownVersion.version })}
                    </h3>
                    <span className={`jobchip ${pending ? "jobchip--warn" : "jobchip--ok"}`}>
                      <span className="jobchip__dot" />{pending ? t("settings.skills.inertDraft") : t("settings.skills.approved")}
                    </span>
                  </div>
                  {pending ? (
                    <div className="skills-warning" role="note">
                      {t("settings.skills.inertWarning")}
                    </div>
                  ) : null}
                  <pre className="skills-content" aria-label={t("settings.skills.readonlyContent", { version: shownVersion.version })}>
                    {shownVersion.content_md || t("settings.skills.emptyContent")}
                  </pre>
                  {pending && props.isManager ? (
                    <button
                      type="button"
                      className="btn btn--primary"
                      disabled={props.busyId === skill?.id}
                      onClick={() => void approve(shownVersion)}
                    >
                      {t("settings.skills.approveVersion", { version: shownVersion.version })}
                    </button>
                  ) : pending ? (
                    <p className="tiny muted">{t("settings.skills.approvePermission")}</p>
                  ) : null}
                </div>
              ) : null}
            </div>

            <form className="skills-new-version" onSubmit={createVersion}>
              <div className="spread">
                <h3 className="skills-subhead">{t("settings.skills.newVersionTitle")}</h3>
                <span className="tiny muted" aria-live="off">{t("settings.skills.characterCount", { count: newContent.length.toLocaleString() })}</span>
              </div>
              {props.isManager ? (
                <>
                  <label className="visually-hidden" htmlFor="skill-new-version">{t("settings.skills.newVersionContent")}</label>
                  <textarea
                    id="skill-new-version"
                    className="input mono skills-editor"
                    value={newContent}
                    onChange={(e) => setNewContent(e.target.value)}
                    placeholder={t("settings.skills.writeProcedure")}
                    disabled={saving}
                  />
                  <div className="skills-warning" role="note">
                    {t("settings.skills.newVersionWarning")}
                  </div>
                  <button type="submit" className="btn btn--primary" disabled={saving || !newContent.trim()}>
                    {saving ? t("settings.skills.saving") : t("settings.skills.saveVersion")}
                  </button>
                </>
              ) : (
                <div className="skills-readonly-note">
                  {t("settings.skills.readonlyNote")}
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
  const t = useT();
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
      setError(t("settings.skills.required"));
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
      title={isNew ? t("settings.skills.newTitle") : t("settings.skills.editTitle", { name: props.skill?.name ?? t("settings.skills.skill") })}
      description={t("settings.skills.dialogDescription")}
      onClose={props.onClose}
      width={560}
    >
      <form onSubmit={submit} className="skills-form">
        {error ? <div className="error-note" role="alert">{error}</div> : null}
        <div className="skills-form-row">
          <div className="field">
            <label htmlFor="skill-name">{t("settings.skills.name")}</label>
            <input id="skill-name" className="input" value={name} onChange={(e) => setName(e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor="skill-slug">{t("settings.skills.slug")}</label>
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
          <label htmlFor="skill-scope">{t("settings.skills.scope")}</label>
          <select id="skill-scope" className="input" value={productId} onChange={(e) => setProductId(e.target.value)}>
            <option value="">{t("settings.skills.tenantWide")}</option>
            {props.products.map((product) => <option key={product.id} value={product.id}>{product.name}</option>)}
          </select>
          <span className="tiny muted">{t("settings.skills.scopeHint")}</span>
        </div>
        <div className="field">
          <label htmlFor="skill-description">{t("settings.skills.description")}</label>
          <textarea
            id="skill-description"
            className="input"
            rows={3}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={t("settings.skills.descriptionPlaceholder")}
          />
        </div>
        <div className="dialog__row">
          <button type="button" className="btn" onClick={props.onClose}>{t("settings.skills.cancel")}</button>
          <button type="submit" className="btn btn--primary" disabled={busy}>
            {busy ? t("settings.skills.saving") : isNew ? t("settings.skills.create") : t("settings.skills.saveChanges")}
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
  return apiErrorMessage(err);
}

function authorLabel(author: string, t: ReturnType<typeof useT>): string {
  if (author === "agent_draft") return t("settings.skills.agentDraft");
  if (author === "human") return t("settings.skills.human");
  return author;
}
