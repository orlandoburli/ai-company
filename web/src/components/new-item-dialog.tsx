"use client";

import { useState } from "react";
import { api } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useT } from "@/lib/i18n";
import type { Priority, Product, Project, Status } from "@/lib/types";
import { Dialog, apiErrorMessage, Field, KindCombobox, LabelInput, StatusSelect } from "./primitives";
export function NewItemDialog(props: {
  projects: Project[];
  products: Product[];
  defaultProjectId?: string;
  defaultStatus?: Status;
  onClose: () => void;
  onCreated: (item: { id: string; project_id: string }) => void;
}) {
  const t = useT();
  const [projectId, setProjectId] = useState(
    props.defaultProjectId ?? props.projects[0]?.id ?? "",
  );
  const [title, setTitle] = useState("");
  const [kind, setKind] = useState("task");
  const [status, setStatus] = useState<Status | "">(props.defaultStatus ?? "backlog");
  const [priority, setPriority] = useState("");
  const [labels, setLabels] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const kinds = useAsync(() => api.listProjectKinds(projectId), [projectId]);
  const usedLabels = useAsync(() => api.listLabels(projectId), [projectId]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!projectId) return;
    setBusy(true);
    setError(null);
    try {
      const created = await api.createItem({
        project_id: projectId,
        kind: kind.trim() || "task",
        title: title.trim(),
        status: status as Status | undefined,
        priority: priority as Priority | undefined,
        labels,
      });
      props.onCreated(created.item);
    } catch (err) {
      setError(apiErrorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Dialog
      title={t("newItem.title")}
      description={t("newItem.description")}
      onClose={props.onClose}
    >
      <form onSubmit={submit} style={{ display: "grid", gap: 12 }}>
        {error ? <div className="error-note">{error}</div> : null}
        {props.projects.length === 0 ? (
          <p className="tiny" style={{ color: "var(--warn)" }}>
            {t("newItem.noProject")}
          </p>
        ) : (
          <>
            <Field label={t("field.project")}>
              <select
                className="input"
                value={projectId}
                onChange={(e) => setProjectId(e.target.value)}
              >
                {props.projects.map((p) => {
                  const prod = props.products.find((x) => x.id === p.product_id);
                  return (
                    <option key={p.id} value={p.id}>
                      {prod ? `${prod.name} / ` : ""}
                      {p.name}
                    </option>
                  );
                })}
              </select>
            </Field>
            <Field label={t("field.title")}>
              <input
                className="input"
                autoFocus
                required
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            </Field>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10 }}>
              <Field
                label={t("field.kind")}
                hint={kinds.error ? t("newItem.kindError") : kinds.loading ? t("newItem.kindLoading") : t("newItem.kindHint")}
              >
                <KindCombobox
                  value={kind}
                  options={(kinds.data?.kinds ?? []).map((entry) => entry.name)}
                  onChange={setKind}
                />
              </Field>
              <Field label={t("field.priority")}>
                <select
                  className="input"
                  value={priority}
                  onChange={(e) => setPriority(e.target.value)}
                >
                  <option value="">{t("field.noPriority")}</option>
                  <option value="priority:p0">{t("newItem.p0")}</option>
                  <option value="priority:p1">P1</option>
                  <option value="priority:p2">P2</option>
                </select>
              </Field>
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10 }}>
              <Field label={t("field.status")}>
                <StatusSelect
                  value={status === "" ? "backlog" : status}
                  ariaLabel={t("field.status")}
                  onChange={setStatus}
                />
              </Field>
              <Field
                label={t("field.labels")}
                hint={usedLabels.error ? t("newItem.labelsError") : usedLabels.loading ? t("newItem.labelsLoading") : t("newItem.labelsHint")}
              >
                <LabelInput
                  value={labels}
                  options={usedLabels.data?.labels ?? []}
                  onChange={setLabels}
                />
              </Field>
            </div>
            <div className="dialog__row">
              <button type="button" className="btn" onClick={props.onClose}>
                {t("field.cancel")}
              </button>
              <button
                type="submit"
                className="btn btn--primary"
                disabled={busy || !title.trim()}
              >
                {busy ? t("newItem.creating") : t("newItem.create")}
              </button>
            </div>
          </>
        )}
      </form>
    </Dialog>
  );
}
