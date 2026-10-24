"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { Project, ProjectKind } from "@/lib/types";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { SettingsNav } from "@/components/settings-nav";
import { ErrorBlock, Skeleton, Toasts, apiErrorMessage } from "@/components/primitives";

type ProjectKindRow = { project: Project; kinds: ProjectKind[] | null; error: string | null };

export default function KindsSettingsPage() {
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
      push("error", `“${duplicate}” appears more than once. Kind names must be unique.`);
      return;
    }
    setBusy(row.project.id);
    setSaved(null);
    try {
      const result = await api.setProjectKinds(row.project.id, names.map((name, position) => ({ name, position })));
      edit(row.project.id, [...result.kinds].sort((a, b) => a.position - b.position).map((kind) => kind.name));
      setSaved(row.project.id);
      push("info", `Kinds saved for ${row.project.name}.`);
    } catch (error) {
      push("error", apiErrorMessage(error));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div style={{ maxWidth: 900 }}>
      <div className="pagehead">
        <h1>Kinds</h1>
        <span className="pagehead__sub">Set the vocabulary offered when creating and editing work.</span>
      </div>
      <SettingsNav />
      <div className="section">
        <div className="section__head"><h2>Project kinds</h2></div>
        <div className="section__body" style={{ display: "grid", gap: 12 }}>
          <p className="tiny muted" style={{ margin: 0 }}>
            The kinds this project’s items may be. An existing item keeps its kind, even if it is no longer listed.
          </p>
          {!canEdit ? <p className="tiny muted" style={{ margin: 0 }}>Read only — only an owner or admin can change project kinds.</p> : null}
          {projects.error ? (
            <ErrorBlock message={projects.error.message} retry={() => void projects.reload()} />
          ) : projects.loading && !projects.data ? (
            <div style={{ display: "grid", gap: 8 }}><Skeleton h={68} /><Skeleton h={68} /></div>
          ) : projects.data?.length === 0 ? (
            <p className="tiny muted" style={{ margin: 0 }}>There are no projects yet. Create one in Settings.</p>
          ) : (
            projects.data?.map((row) => {
              const names = drafts[row.project.id] ?? [];
              return (
                <section className="kind-settings-card" key={row.project.id} aria-label={`Kinds for ${row.project.name}`}>
                  <div className="spread" style={{ flexWrap: "wrap" }}>
                    <strong>{row.project.name}</strong>
                    {row.project.key ? <span className="mono tiny muted">{row.project.key}</span> : null}
                  </div>
                  {row.error ? (
                    <div className="error-note" role="alert">
                      Project kinds could not load. Try again before editing. {row.error}
                      <button type="button" className="btn btn--sm" style={{ marginLeft: 8 }} onClick={() => void projects.reload()}>Retry</button>
                    </div>
                  ) : row.kinds ? (
                    <>
                      {names.length === 0 ? <p className="tiny muted" style={{ margin: 0 }}>No kinds offered yet.</p> : null}
                      <ol className="kind-settings-list">
                        {names.map((name, index) => (
                          <li key={`${row.project.id}-${index}`}>
                            <span className="kind-settings-list__position">{index + 1}</span>
                            {canEdit ? (
                              <input
                                className="input"
                                aria-label={`Kind ${index + 1} for ${row.project.name}`}
                                value={name}
                                onChange={(e) => edit(row.project.id, names.map((entry, i) => i === index ? e.target.value : entry))}
                              />
                            ) : <span className="chip chip--kind">{name}</span>}
                            {canEdit ? (
                              <div className="stack" style={{ gap: 4 }}>
                                <button type="button" className="btn btn--sm" aria-label={`Move ${name || `kind ${index + 1}`} up`} disabled={index === 0} onClick={() => edit(row.project.id, swap(names, index, index - 1))}>↑</button>
                                <button type="button" className="btn btn--sm" aria-label={`Move ${name || `kind ${index + 1}`} down`} disabled={index === names.length - 1} onClick={() => edit(row.project.id, swap(names, index, index + 1))}>↓</button>
                                <button type="button" className="btn btn--sm btn--ghost btn--danger" aria-label={`Remove ${name || `kind ${index + 1}`}`} onClick={() => edit(row.project.id, names.filter((_, i) => i !== index))}>Remove</button>
                              </div>
                            ) : null}
                          </li>
                        ))}
                      </ol>
                      {canEdit ? (
                        <div className="stack" style={{ flexWrap: "wrap" }}>
                          <button type="button" className="btn btn--sm" onClick={() => edit(row.project.id, [...names, ""])}>Add kind</button>
                          <button type="button" className="btn btn--sm btn--primary" disabled={busy === row.project.id} onClick={() => void save(row)}>
                            {busy === row.project.id ? "Saving…" : "Save kinds"}
                          </button>
                          {saved === row.project.id ? <span className="tiny" style={{ color: "var(--accent)" }}>Saved.</span> : null}
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
