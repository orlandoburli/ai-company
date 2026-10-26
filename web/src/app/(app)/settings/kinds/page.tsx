"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { Project, ProjectKind } from "@/lib/types";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { SettingsNav } from "@/components/settings-nav";
import { ErrorBlock, Skeleton, Toasts, apiErrorMessage } from "@/components/primitives";
import { useT } from "@/lib/i18n";

type ProjectKindRow = { project: Project; kinds: ProjectKind[] | null; error: string | null };

export default function KindsSettingsPage() {
  const t = useT();
  const { me } = useSession();
  const canEdit = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const { toasts, push } = useToasts();
  const projects = useAsync(async () => {
    const listed = await api.listProjects();
    return Promise.all(listed.projects.map(async (project): Promise<ProjectKindRow> => {
      try {
        const result = await api.listProjectKinds(project.id);
        return { project, kinds: [...result.kinds].sort((a, b) => a.position - b.position), error: null };
      } catch (error) {
        return { project, kinds: null, error: apiErrorMessage(error) };
      }
    }));
  }, []);
  const [drafts, setDrafts] = useState<Record<string, string[]>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);

  useEffect(() => {
    if (!projects.data) return;
    setDrafts(Object.fromEntries(projects.data
      .filter((row) => row.kinds)
      .map((row) => [row.project.id, row.kinds!.map((kind) => kind.name)])));
  }, [projects.data]);

  function edit(projectId: string, names: string[]) {
    setDrafts((current) => ({ ...current, [projectId]: names }));
    setSaved((current) => current === projectId ? null : current);
  }

  async function save(row: ProjectKindRow) {
    if (!canEdit || !row.kinds) return;
    const names = (drafts[row.project.id] ?? []).map((name) => name.trim()).filter(Boolean);
    const duplicate = names.find((name, index) => names.findIndex((other) => other.toLocaleLowerCase() === name.toLocaleLowerCase()) !== index);
    if (duplicate) {
      push("error", t("settings.kinds.duplicate", { name: duplicate }));
      return;
    }
    setBusy(row.project.id);
    setSaved(null);
    try {
      const result = await api.setProjectKinds(row.project.id, names.map((name, position) => ({ name, position })));
      edit(row.project.id, [...result.kinds].sort((a, b) => a.position - b.position).map((kind) => kind.name));
      setSaved(row.project.id);
      push("info", t("settings.kinds.savedFor", { name: row.project.name }));
    } catch (error) {
      push("error", apiErrorMessage(error));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div style={{ maxWidth: 900 }}>
      <div className="pagehead">
          <h1>{t("settings.kinds.title")}</h1>
          <span className="pagehead__sub">{t("settings.kinds.subtitle")}</span>
      </div>
      <SettingsNav />
      <div className="section">
        <div className="section__head"><h2>{t("settings.kinds.projectKinds")}</h2></div>
        <div className="section__body" style={{ display: "grid", gap: 12 }}>
          <p className="tiny muted" style={{ margin: 0 }}>
            {t("settings.kinds.hint")}
          </p>
          {!canEdit ? <p className="tiny muted" style={{ margin: 0 }}>{t("settings.kinds.readonly")}</p> : null}
          {projects.error ? (
            <ErrorBlock message={apiErrorMessage(projects.error)} retry={() => void projects.reload()} />
          ) : projects.loading && !projects.data ? (
            <div style={{ display: "grid", gap: 8 }}><Skeleton h={68} /><Skeleton h={68} /></div>
          ) : projects.data?.length === 0 ? (
            <p className="tiny muted" style={{ margin: 0 }}>{t("settings.kinds.noProjects")}</p>
          ) : (
            projects.data?.map((row) => {
              const names = drafts[row.project.id] ?? [];
              return (
                <section className="kind-settings-card" key={row.project.id} aria-label={t("settings.kinds.forProject", { name: row.project.name })}>
                  <div className="spread" style={{ flexWrap: "wrap" }}>
                    <strong>{row.project.name}</strong>
                    {row.project.key ? <span className="mono tiny muted">{row.project.key}</span> : null}
                  </div>
                  {row.error ? (
                    <div className="error-note" role="alert">
                      {t("settings.kinds.loadError", { message: row.error })}
                      <button type="button" className="btn btn--sm" style={{ marginLeft: 8 }} onClick={() => void projects.reload()}>{t("settings.kinds.retry")}</button>
                    </div>
                  ) : row.kinds ? (
                    <>
                      {names.length === 0 ? <p className="tiny muted" style={{ margin: 0 }}>{t("settings.kinds.none")}</p> : null}
                      <ol className="kind-settings-list">
                        {names.map((name, index) => (
                          <li key={`${row.project.id}-${index}`}>
                            <span className="kind-settings-list__position">{index + 1}</span>
                            {canEdit ? (
                              <input
                                className="input"
                                aria-label={t("settings.kinds.kindFor", { index: index + 1, name: row.project.name })}
                                value={name}
                                onChange={(e) => edit(row.project.id, names.map((entry, i) => i === index ? e.target.value : entry))}
                              />
                            ) : <span className="chip chip--kind">{name}</span>}
                            {canEdit ? (
                              <div className="stack" style={{ gap: 4 }}>
                                <button type="button" className="btn btn--sm" aria-label={t("settings.kinds.moveUp", { name: name || t("settings.kinds.kindNumber", { index: index + 1 }) })} disabled={index === 0} onClick={() => edit(row.project.id, swap(names, index, index - 1))}>↑</button>
                                <button type="button" className="btn btn--sm" aria-label={t("settings.kinds.moveDown", { name: name || t("settings.kinds.kindNumber", { index: index + 1 }) })} disabled={index === names.length - 1} onClick={() => edit(row.project.id, swap(names, index, index + 1))}>↓</button>
                                <button type="button" className="btn btn--sm btn--ghost btn--danger" aria-label={t("settings.kinds.remove", { name: name || t("settings.kinds.kindNumber", { index: index + 1 }) })} onClick={() => edit(row.project.id, names.filter((_, i) => i !== index))}>{t("settings.kinds.removeButton")}</button>
                              </div>
                            ) : null}
                          </li>
                        ))}
                      </ol>
                      {canEdit ? (
                        <div className="stack" style={{ flexWrap: "wrap" }}>
                          <button type="button" className="btn btn--sm" onClick={() => edit(row.project.id, [...names, ""])}>{t("settings.kinds.add")}</button>
                          <button type="button" className="btn btn--sm btn--primary" disabled={busy === row.project.id} onClick={() => void save(row)}>
                            {busy === row.project.id ? t("settings.kinds.saving") : t("settings.kinds.save")}
                          </button>
                          {saved === row.project.id ? <span className="tiny" style={{ color: "var(--accent)" }}>{t("settings.kinds.saved")}</span> : null}
                        </div>
                      ) : null}
                    </>
                  ) : <Skeleton h={54} />}
                </section>
              );
            })
          )}
        </div>
      </div>
      <Toasts toasts={toasts} />
    </div>
  );
}

function swap(values: string[], first: number, second: number): string[] {
  if (second < 0 || second >= values.length) return values;
  const next = [...values];
  [next[first], next[second]] = [next[second], next[first]];
  return next;
}
