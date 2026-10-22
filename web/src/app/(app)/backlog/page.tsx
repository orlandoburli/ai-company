"use client";

import { Suspense, useCallback, useMemo, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import type { Item, Priority, Project, Status } from "@/lib/types";
import { STATUSES } from "@/lib/types";
import { formatDate, firstName, relTime, STATUS_LABELS } from "@/lib/format";
import {
  ErrorBlock,
  PrioritySelect,
  Skeleton,
  StatusSelect,
  Toasts,
  apiErrorMessage,
} from "@/components/primitives";
import { ItemIndicators } from "@/components/item-bits";
import { NewItemDialog } from "@/components/new-item-dialog";
import { IconPlus } from "@/components/icons";

export default function BacklogPage() {
  return (
    <Suspense
      fallback={
        <div style={{ display: "grid", gap: 12 }}>
          <Skeleton h={22} w={140} />
          <Skeleton h={320} />
        </div>
      }
    >
      <BacklogView />
    </Suspense>
  );
}

interface Row {
  item: Item;
  depth: number;
}

function BacklogView() {
  const params = useSearchParams();
  const pathname = usePathname();
  const router = useRouter();

  const projectFilter = params.get("project_id") ?? "";
  const assigneeFilter = params.get("assignee_user_id") ?? "";
  const labelFilter = params.get("label") ?? "";
  const productFilter = params.get("product_id") ?? "";

  const { canWrite } = useSession();
  const { toasts, push } = useToasts();

  const products = useAsync(
    () => api.listProducts().then((r) => r.products),
    [],
  );
  const projects = useAsync(
    () => api.listProjects().then((r) => r.projects),
    [],
  );
  const members = useAsync(
    () => api.listMembers().then((r) => r.members),
    [],
  );
  const items = useAsync(
    () =>
      api
        .listItems({
          project_id: projectFilter,
          assignee_user_id: assigneeFilter,
        })
        .then((r) => r.items),
    [projectFilter, assigneeFilter],
  );

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [statusDraft, setStatusDraft] = useState<Record<string, Status>>({});
  const [showNew, setShowNew] = useState(false);

  const projectsList = useMemo(() => projects.data ?? [], [projects.data]);
  const productsList = useMemo(() => products.data ?? [], [products.data]);

  const memberName = useMemo(() => {
    const m = new Map<string, string>();
    for (const mem of members.data ?? []) m.set(mem.user_id, mem.name);
    return m;
  }, [members.data]);

  // Label filtering is client-side: the API has no labels endpoint, so labels
  // are derived from the items already loaded.
  const labelOptions = useMemo(() => {
    const s = new Set<string>();
    for (const it of items.data ?? []) for (const l of it.labels) s.add(l);
    return [...s].sort();
  }, [items.data]);

  const visibleItems = useMemo(() => {
    const list = items.data ?? [];
    if (!labelFilter) return list;
    return list.filter((it) => it.labels.includes(labelFilter));
  }, [items.data, labelFilter]);

  /**
   * Groups: top-level projects after the product/project filter, with their
   * sub-projects folded in. Each group flattens its item hierarchy into rows
   * with a depth for indentation.
   */
  const groups = useMemo(() => {
    if (!projectsList) return [];

    const permitted = projectFilter
      ? subtreeOf(projectFilter, projectsList)
      : productFilter
        ? productSubtree(productFilter, projectsList)
        : new Set(projectsList.map((p) => p.id));

    const inScope = projectsList.filter((p) => permitted.has(p.id));
    const scopeSet = new Set(inScope.map((p) => p.id));

    const itemsOf = new Map<string, Set<string>>();
    for (const it of visibleItems) {
      if (!scopeSet.has(it.project_id)) continue;
      const set = itemsOf.get(it.project_id) ?? new Set<string>();
      set.add(it.id);
      itemsOf.set(it.project_id, set);
    }

    const byId = new Map(visibleItems.map((it) => [it.id, it]));
    const topProjects = inScope.filter(
      (p) => !p.parent_id || !scopeSet.has(p.parent_id),
    );

    const out: { project: Project; rows: Row[] }[] = [];
    for (const proj of topProjects) {
      const rows: Row[] = [];
      // Every item filed under this project or any sub-project of it.
      const groupIds = new Set<string>(
        [...scopeSet].filter((pid) => subtreeOf(pid, inScope).has(proj.id) || pid === proj.id),
      );
      const pool: Item[] = [];
      for (const pid of groupIds) {
        for (const iid of itemsOf.get(pid) ?? []) {
          const it = byId.get(iid);
          if (it) pool.push(it);
        }
      }
      const parentIn = new Map<string, Item | undefined>();
      for (const it of pool) {
        parentIn.set(it.id, it.parent_id ? byId.get(it.parent_id) : undefined);
      }
      const children = new Map<string, Item[]>();
      const roots: Item[] = [];
      for (const it of pool) {
        const parent = parentIn.get(it.id);
        const isRoot = !parent || !pool.includes(parent);
        if (isRoot) roots.push(it);
        else {
          const arr = children.get(it.parent_id!) ?? [];
          arr.push(it);
          children.set(it.parent_id!, arr);
        }
      }
      const sortNum = (a: Item, b: Item) => a.sort_order - b.sort_order || a.number - b.number;
      roots.sort(sortNum);
      const walk = (list: Item[], depth: number) => {
        for (const it of list) {
          rows.push({ item: it, depth });
          walk((children.get(it.id) ?? []).sort(sortNum), depth + 1);
        }
      };
      walk(roots, 0);
      if (rows.length > 0) out.push({ project: proj, rows });
    }
    return out;
  }, [visibleItems, projectFilter, productFilter, projectsList]);

  const setParam = useCallback(
    (key: string, value: string) => {
      const next = new URLSearchParams(params.toString());
      if (value) next.set(key, value);
      else next.delete(key);
      router.replace(`${pathname}?${next.toString()}`, { scroll: false });
    },
    [pathname, params, router],
  );

  async function setStatus(itemId: string, status: Status) {
    if (!canWrite) return;
    setStatusDraft((e) => ({ ...e, [itemId]: status }));
    try {
      await api.patchItem(itemId, { status });
      await items.reload();
      setStatusDraft({});
    } catch (err) {
      setStatusDraft((e) => {
        const next = { ...e };
        delete next[itemId];
        return next;
      });
      push("error", err instanceof ApiError ? err.message : "Could not set status.");
    }
  }

  async function setPriority(itemId: string, priority: Priority | null) {
    if (!canWrite) return;
    try {
      // Empty string clears the field on the API.
      await api.patchItem(itemId, { priority: priority ?? "" });
      await items.reload();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  async function applyBulk(to: Status) {
    const ids = [...selected];
    setSelected(new Set());
    try {
      await api.bulkStatus(ids, to);
      await items.reload();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  const activeSelectedProject = projectsList.find((p) => p.id === projectFilter);

  return (
    <>
      <div className="pagehead">
        <h1>Backlog</h1>
        <span className="pagehead__sub">
          {activeSelectedProject ? activeSelectedProject.name : "All projects"}
          {labelFilter ? ` · label “${labelFilter}”` : ""}
        </span>
        <span style={{ marginLeft: "auto" }}>
          {canWrite && projectsList.length > 0 ? (
            <button
              type="button"
              className="btn btn--sm"
              onClick={() => setShowNew(true)}
            >
              <IconPlus /> New item
            </button>
          ) : null}
        </span>
      </div>

      {renderFilters({ productFilter, projectFilter, labelFilter, assigneeFilter, productsList, projectsList, labelOptions, members, setParam })}

      {selected.size > 0 && canWrite ? (
        <div className="bulkbar" style={{ marginBottom: 12 }}>
          <span className="count">{selected.size} selected</span>
          <button
            type="button"
            className="btn btn--sm"
            style={{ marginRight: "auto" }}
            onClick={() => setSelected(new Set())}
          >
            Clear
          </button>
          <span className="tiny muted">Set status:</span>
          {STATUSES.map((s) => (
            <button
              key={s}
              type="button"
              className="btn btn--sm"
              onClick={() => void applyBulk(s)}
            >
              {STATUS_LABELS[s]}
            </button>
          ))}
        </div>
      ) : null}

      {items.error ? (
        <ErrorBlock
          message={items.error.message}
          retry={() => void items.reload()}
        />
      ) : items.loading ? (
        <div className="panel">
          <div className="section__body">
            {Array.from({ length: 8 }).map((_, i) => (
              <div key={i} style={{ marginBottom: 10 }}>
                <Skeleton h={14} w={`${55 + ((i * 37) % 40)}%`} />
              </div>
            ))}
          </div>
        </div>
      ) : visibleItems.length === 0 ? (
        <div className="empty panel" style={{ padding: "48px 20px" }}>
          <h3>Nothing in the backlog</h3>
          <p>
            {canWrite
              ? projectsList.length === 0
                ? "Create a product and a project first — items live inside projects."
                : "Create the first item, or clear a filter above."
              : "No work items match. Ask someone with write access to add work."}
          </p>
          {canWrite && projectsList.length > 0 ? (
            <button
              type="button"
              className="btn"
              onClick={() => setShowNew(true)}
            >
              <IconPlus /> New item
            </button>
          ) : null}
        </div>
      ) : (
        <div className="table-wrap panel">
          {groups.map((g) => (
            <table className="data" key={g.project.id}>
              <thead>
                <tr>
                  {canWrite ? (
                    <th scope="col" style={{ width: 26 }}>
                      <span className="visually-hidden">Select</span>
                    </th>
                  ) : null}
                  <th scope="col" style={{ width: 58 }}>
                    #
                  </th>
                  <th scope="col">Title</th>
                  <th scope="col">Kind</th>
                  <th scope="col">Status</th>
                  <th scope="col">Priority</th>
                  <th scope="col">Due</th>
                  <th scope="col">Updated</th>
                </tr>
                <tr className="group-row">
                  <td colSpan={canWrite ? 8 : 7}>
                    <Link
                      href={`/backlog?project_id=${g.project.id}`}
                      className="rowlink group-title"
                    >
                      {g.project.name}
                    </Link>
                    <span className="tiny muted">
                      {g.project.key ? ` / ${g.project.key}` : ""} ·{" "}
                      {g.rows.length} item{g.rows.length === 1 ? "" : "s"}
                    </span>
                  </td>
                </tr>
              </thead>
              <tbody>
                {g.rows.map((row) => (
                  <BacklogRow
                    key={row.item.id}
                    row={row}
                    canWrite={canWrite}
                    selected={selected.has(row.item.id)}
                    onToggleSelect={() => {
                      setSelected((s) => {
                        const next = new Set(s);
                        if (next.has(row.item.id)) next.delete(row.item.id);
                        else next.add(row.item.id);
                        return next;
                      });
                    }}
                    onStatus={(st) => void setStatus(row.item.id, st)}
                    onPriority={(p) => void setPriority(row.item.id, p)}
                    statusValue={statusDraft[row.item.id] ?? row.item.status}
                    memberName={memberName}
                  />
                ))}
              </tbody>
            </table>
          ))}
        </div>
      )}

      {showNew ? (
        <NewItemDialog
          projects={projectsList}
          products={productsList}
          defaultProjectId={projectFilter || undefined}
          onClose={() => setShowNew(false)}
          onCreated={(item) => {
            setShowNew(false);
            router.push(`/items/${item.id}`);
          }}
        />
      ) : null}

      <Toasts toasts={toasts} />
    </>
  );
}

/* --- filter row -------------------------------------------------------------- */

function renderFilters(props: {
  productFilter: string;
  projectFilter: string;
  labelFilter: string;
  assigneeFilter: string;
  productsList: { id: string; name: string }[];
  projectsList: Project[];
  labelOptions: string[];
  members: { data: { user_id: string; name: string }[] | null };
  setParam: (key: string, value: string) => void;
}) {
  return (
    <div className="filters">
      <div className="field">
        <label htmlFor="b-product">Product</label>
        <select
          id="b-product"
          className="input input--inline"
          value={props.productFilter}
          onChange={(e) => {
            props.setParam("product_id", e.target.value);
            props.setParam("project_id", "");
          }}
        >
          <option value="">All</option>
          {props.productsList.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label htmlFor="b-project">Project</label>
        <select
          id="b-project"
          className="input input--inline"
          value={props.projectFilter}
          onChange={(e) => props.setParam("project_id", e.target.value)}
        >
          <option value="">All</option>
          {props.projectsList
            .filter((p) => !props.productFilter || p.product_id === props.productFilter)
            .map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
        </select>
      </div>
      <div className="field">
        <label htmlFor="b-label">Label</label>
        <select
          id="b-label"
          className="input input--inline"
          value={props.labelFilter}
          onChange={(e) => props.setParam("label", e.target.value)}
        >
          <option value="">All</option>
          {props.labelOptions.map((l) => (
            <option key={l} value={l}>
              {l}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label htmlFor="b-assignee">Assignee</label>
        <select
          id="b-assignee"
          className="input input--inline"
          value={props.assigneeFilter}
          onChange={(e) => props.setParam("assignee_user_id", e.target.value)}
        >
          <option value="">All</option>
          {(props.members.data ?? []).map((m) => (
            <option key={m.user_id} value={m.user_id}>
              {m.name}
            </option>
          ))}
        </select>
      </div>
    </div>
  );
}

/* --- row ---------------------------------------------------------------------- */

function BacklogRow(props: {
  row: { item: Item; depth: number };
  canWrite: boolean;
  selected: boolean;
  onToggleSelect: () => void;
  onStatus: (s: Status) => void;
  onPriority: (p: Priority | null) => void;
  statusValue: Status;
  memberName: Map<string, string>;
}) {
  const it = props.row.item;
  return (
    <tr className={props.selected ? "is-selected" : ""}>
      {props.canWrite ? (
        <td>
          <input
            type="checkbox"
            className="row-check"
            aria-label={`Select ${it.title}`}
            checked={props.selected}
            onChange={props.onToggleSelect}
          />
        </td>
      ) : null}
      <td className="mono muted tiny">{it.number}</td>
      <td>
        <div
          style={{
            paddingLeft: props.row.depth * 16,
            display: "flex",
            alignItems: "baseline",
            gap: 6,
            minWidth: 0,
          }}
        >
          {it.is_epic ? (
            <span className="chip chip--epic" title="Epic: has open sub-items">
              epic
            </span>
          ) : null}
          <Link
            className="rowlink"
            href={`/items/${it.id}`}
            style={{ overflowWrap: "anywhere" }}
          >
            {it.title}
          </Link>
          <ItemIndicators item={it} size={11} />
        </div>
        {it.labels.length > 0 ? (
          <div
            className="tiny muted"
            style={{ marginTop: 1, paddingLeft: props.row.depth * 16 + 2 }}
          >
            {it.labels.join(" · ")}
          </div>
        ) : null}
      </td>
      <td className="tiny">{it.kind}</td>
      <td>
        {props.canWrite ? (
          <StatusSelect
            value={props.statusValue}
            ariaLabel={`Status of ${it.title}`}
            onChange={props.onStatus}
          />
        ) : (
          <span className="tiny">{STATUS_LABELS[it.status]}</span>
        )}
      </td>
      <td>
        {props.canWrite ? (
          <PrioritySelect
            value={it.priority}
            ariaLabel={`Priority of ${it.title}`}
            onChange={props.onPriority}
          />
        ) : (
          <span className="mono tiny">{it.priority ?? "—"}</span>
        )}
      </td>
      <td className="tiny">{formatDate(it.due_date)}</td>
      <td className="tiny muted" title={it.updated_at}>
        {relTime(it.updated_at)}
        {it.assignee_user_id && props.memberName.get(it.assignee_user_id)
          ? ` · ${firstName(props.memberName.get(it.assignee_user_id))}`
          : ""}
      </td>
    </tr>
  );
}

/* --- helpers ---------------------------------------------------------------- */

/** All descendants of a project id (including itself). */
function subtreeOf(projectId: string, projects: Project[]): Set<string> {
  const keep = new Set<string>([projectId]);
  for (let changed = true; changed; ) {
    changed = false;
    for (const p of projects) {
      if (!keep.has(p.id) && p.parent_id && keep.has(p.parent_id)) {
        keep.add(p.id);
        changed = true;
      }
    }
  }
  return keep;
}

/** Every project belonging to a product (including all sub-projects). */
function productSubtree(productId: string, projects: Project[]): Set<string> {
  const out = new Set<string>();
  for (const p of projects) {
    if (p.product_id === productId) {
      for (const id of subtreeOf(p.id, projects)) out.add(id);
    }
  }
  return out;
}
