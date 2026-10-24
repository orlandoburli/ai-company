"use client";

import { useState } from "react";
import { api } from "@/lib/api";
import type { Priority, Product, Project, Status } from "@/lib/types";
import { Dialog, apiErrorMessage, Field, StatusSelect } from "./primitives";
export function NewItemDialog(props: {
  projects: Project[];
  products: Product[];
  defaultProjectId?: string;
  defaultStatus?: Status;
  onClose: () => void;
  onCreated: (item: { id: string; project_id: string }) => void;
}) {
  const [projectId, setProjectId] = useState(
    props.defaultProjectId ?? props.projects[0]?.id ?? "",
  );
  const [title, setTitle] = useState("");
  const [kind, setKind] = useState("task");
  const [status, setStatus] = useState<Status | "">(props.defaultStatus ?? "backlog");
  const [priority, setPriority] = useState("");
  const [labels, setLabels] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
        labels: labels
          .split(",")
          .map((l) => l.trim())
          .filter(Boolean),
      });
      props.onCreated(created.item);
    } catch (err) {
      setError(apiErrorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Dialog
      title="New work item"
      description="Items live in a project and can nest under another item later on its own page."
      onClose={props.onClose}
    >
      <form onSubmit={submit} style={{ display: "grid", gap: 12 }}>
        {error ? <div className="error-note">{error}</div> : null}
        {props.projects.length === 0 ? (
          <p className="tiny" style={{ color: "var(--warn)" }}>
            Create a project first — this company has none yet.
          </p>
        ) : (
          <>
            <Field label="Project">
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
            <Field label="Title">
              <input
                className="input"
                autoFocus
                required
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            </Field>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10 }}>
              <Field label="Kind" hint="Free-form: task, bug, epic…">
                <input
                  className="input"
                  value={kind}
                  onChange={(e) => setKind(e.target.value)}
                />
              </Field>
              <Field label="Priority">
                <select
                  className="input"
                  value={priority}
                  onChange={(e) => setPriority(e.target.value)}
                >
                  <option value="">No priority</option>
                  <option value="priority:p0">P0 — highest</option>
                  <option value="priority:p1">P1</option>
                  <option value="priority:p2">P2</option>
                </select>
              </Field>
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10 }}>
              <Field label="Status">
                <StatusSelect
                  value={status === "" ? "backlog" : status}
                  ariaLabel="Status"
                  onChange={setStatus}
                />
              </Field>
              <Field label="Labels" hint="Comma separated">
                <input
                  className="input"
                  value={labels}
                  onChange={(e) => setLabels(e.target.value)}
                />
              </Field>
            </div>
            <div className="dialog__row">
              <button type="button" className="btn" onClick={props.onClose}>
                Cancel
              </button>
              <button
                type="submit"
                className="btn btn--primary"
                disabled={busy || !title.trim()}
              >
                {busy ? "Creating…" : "Create item"}
              </button>
            </div>
          </>
        )}
      </form>
    </Dialog>
  );
}
