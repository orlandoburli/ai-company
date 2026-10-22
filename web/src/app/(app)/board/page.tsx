"use client";

import { useCallback, Suspense, useMemo, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import type { BoardColumn, Item, Status } from "@/lib/types";
import { STATUSES } from "@/lib/types";
import { firstName, STATUS_LABELS } from "@/lib/format";
import {
  ErrorBlock,
  PriorityChip,
  Skeleton,
  Toasts,
  apiErrorMessage,
} from "@/components/primitives";
import { ItemIndicators } from "@/components/item-bits";
import { NewItemDialog } from "@/components/new-item-dialog";
import { IconPause, IconPin, IconPlus } from "@/components/icons";

const STATUS_COLORS: Record<Status, string> = {
  backlog: "#8590a4",
  todo: "#58a6ff",
  in_progress: "#f0b429",
  waiting_human: "#a78bfa",
  in_review: "#56d4dd",
  done: "#3ecf6e",
  cancelled: "#f4756b",
};

export default function BoardPage() {
  return (
    <Suspense
      fallback={
        <div style={{ display: "grid", gap: 12 }}>
          <Skeleton h={22} w={140} />
          <Skeleton h={320} />
        </div>
      }
    >
      <BoardView />
    </Suspense>
  );
}

function BoardView() {
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
  const board = useAsync(
    () =>
      api
        .board({
          project_id: projectFilter,
          assignee_user_id: assigneeFilter,
        })
        .then((r) => r.columns),
    [projectFilter, assigneeFilter],
  );

  const [moved, setMoved] = useState<Record<string, Status>>({});
  const [overlay, setOverlay] = useState<Record<string, Partial<Item>>>({});
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [dragId, setDragId] = useState<string | null>(null);
  const [dragOver, setDragOver] = useState<Status | null>(null);
  const [showNew, setShowNew] = useState(false);

  const projectsList = projects.data ?? [];
  const productsList = products.data ?? [];

  const memberName = useMemo(() => {
    const m = new Map<string, string>();
    for (const mem of members.data ?? []) m.set(mem.user_id, mem.name);
    return m;
  }, [members.data]);

  /** Server columns merged with optimistic state so the UI never flickers. */
  const columns: BoardColumn[] = useMemo(() => {
    const data = board.data;
    if (!data) return [];
    const out: BoardColumn[] = [];
    for (const col of data) {
      const items: Item[] = [];
      for (const src of data) {
        for (const it of src.items) {
          const effStatus = moved[it.id] ?? it.status;
          if (effStatus !== col.status) continue;
          const eff: Item = { ...it, ...overlay[it.id], status: effStatus };
          items.push(eff);
        }
      }
      out.push({ status: col.status, items });
    }
    return out;
  }, [board.data, moved, overlay]);

  const labelOptions = useMemo(() => {
    const s = new Set<string>();
    for (const col of board.data ?? [])
      for (const it of col.items) for (const l of it.labels) s.add(l);
    return [...s].sort();
  }, [board.data]);

  const itemCount = useMemo(
    () => (board.data ?? []).reduce((n, c) => n + c.items.length, 0),
    [board.data],
  );

  const setParam = useCallback(
    (key: string, value: string) => {
      const next = new URLSearchParams(params.toString());
      if (value) next.set(key, value);
      else next.delete(key);
      router.replace(`${pathname}?${next.toString()}`, { scroll: false });
    },
    [pathname, params, router],
  );

  const activeProject = projectsList.find((p) => p.id === projectFilter);

  async function moveItems(ids: string[], to: Status) {
    if (!canWrite || ids.length === 0) return;
    setMoved((m) => {
      const next = { ...m };
      for (const id of ids) next[id] = to;
      return next;
    });
    try {
      if (ids.length === 1) {
        await api.patchItem(ids[0], { status: to });
      } else {
        await api.bulkStatus(ids, to);
      }
      await board.reload();
      setMoved({});
      setOverlay({});
    } catch (err) {
      setMoved((m) => {
        const next = { ...m };
        for (const id of ids) delete next[id];
        return next;
      });
      push(
        "error",
        err instanceof ApiError ? err.message : "Could not move the card.",
      );
    }
  }

  async function queueOverride(id: string, action: "pin" | "unpin" | "pause" | "unpause") {
    setOverlay((o) => ({
      ...o,
      [id]: { ...o[id], pinned: action === "pin", paused: action === "pause" },
    }));
    try {
      if (action === "pin") await api.pinItem(id);
      else if (action === "unpin") await api.unpinItem(id);
      else if (action === "pause") await api.pauseItem(id);
      else await api.unpauseItem(id);
      await board.reload();
      setOverlay({});
    } catch (err) {
      setOverlay((o) => {
        const next = { ...o };
        delete next[id];
        return next;
      });
      push("error", apiErrorMessage(err));
    }
  }

  function toggleSelect(id: string) {
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function handleDrop(status: Status) {
    const id = dragId;
    setDragId(null);
    setDragOver(null);
    if (!id) return;
    // Dropping a card that is part of a multi-selection moves the whole block.
    const ids = selected.has(id) && selected.size > 1 ? [...selected] : [id];
    setSelected(new Set());
    void moveItems(ids, status);
  }

  return (
    <>
      <div className="pagehead">
        <h1>Board</h1>
        <span className="pagehead__sub">
          {activeProject ? activeProject.name : "All projects"}
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

      <div className="filters">
        <div className="field">
          <label htmlFor="f-product">Product</label>
          <select
            id="f-product"
            className="input input--inline"
            value={productFilter}
            onChange={(e) => {
              setParam("product_id", e.target.value);
              // A project can only belong to one product.
              setParam("project_id", "");
            }}
          >
            <option value="">All</option>
            {productsList.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="f-project">Project</label>
          <select
            id="f-project"
            className="input input--inline"
            value={projectFilter}
            onChange={(e) => setParam("project_id", e.target.value)}
          >
            <option value="">All</option>
            {projectsList
              .filter((p) => !productFilter || p.product_id === productFilter)
              .map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="f-label">Label</label>
          <select
            id="f-label"
            className="input input--inline"
            value={labelFilter}
            onChange={(e) => setParam("label", e.target.value)}
          >
            <option value="">All</option>
            {labelOptions.map((l) => (
              <option key={l} value={l}>
                {l}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="f-assignee">Assignee</label>
          <select
            id="f-assignee"
            className="input input--inline"
            value={assigneeFilter}
            onChange={(e) => setParam("assignee_user_id", e.target.value)}
          >
            <option value="">All</option>
            {(members.data ?? []).map((m) => (
              <option key={m.user_id} value={m.user_id}>
                {m.name}
              </option>
            ))}
          </select>
        </div>
        <label className="field" style={{ visibility: "hidden" }} aria-hidden>
          <label htmlFor="f-select-label">Selection filter</label>
          <span
            id="f-select-label"
            className="input input--inline"
            aria-label="Label filter mirrored for screen readers"
          >
            {labelFilter || "All"}
          </span>
        </label>
      </div>

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
          <span className="tiny muted">Move to:</span>
          {STATUSES.filter((s) => s !== "backlog").map((s) => (
            <button
              key={s}
              type="button"
              className="btn btn--sm"
              onClick={() => {
                const ids = [...selected];
                setSelected(new Set());
                void moveItems(ids, s);
              }}
            >
              {STATUS_LABELS[s]}
            </button>
          ))}
        </div>
      ) : null}

      {board.error ? (
        <ErrorBlock
          message={board.error.message}
          retry={() => void board.reload()}
        />
      ) : board.loading ? (
        <div className="board">
          {STATUS_LIST_PLACEHOLDERS.map((s) => (
            <div key={s} className="board-col">
              <div className="board-col__head">
                <strong style={{ fontSize: 12.5 }}>{s}</strong>
              </div>
              <div className="board-col__list">
                <Skeleton h={66} />
                <Skeleton h={66} />
              </div>
            </div>
          ))}
        </div>
      ) : (
        <BoardBody
          columns={columns}
          canWrite={canWrite}
          dragId={dragId}
          dragOverStatus={dragOver}
          selected={selected}
          memberName={memberName}
          onDragStart={setDragId}
          onDragEnd={() => {
            setDragId(null);
            setDragOver(null);
          }}
          onDragOverColumn={(s) => setDragOver(s)}
          onDragLeaveColumn={(s) => setDragOver((c) => (c === s ? null : c))}
          onDropColumn={handleDrop}
          onKeyMove={(id, dir) => {
            const col = columns.find((c) => c.items.some((i) => i.id === id));
            if (!col) return;
            const idx = STATUSES.indexOf(col.status);
            const nextIdx = idx + dir;
            if (nextIdx < 0 || nextIdx >= STATUSES.length) return;
            void moveItems([id], STATUSES[nextIdx]);
          }}
          onOverride={(id, action) => void queueOverride(id, action)}
          onToggleSelect={toggleSelect}
          onCreateClick={() => setShowNew(true)}
          itemCount={itemCount}
        />
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

const STATUS_LIST_PLACEHOLDERS = [
  "Backlog",
  "To do",
  "In progress",
  "Waiting on human",
];

/* --- columns ---------------------------------------------------------------- */

function BoardBody(props: {
  columns: BoardColumn[];
  canWrite: boolean;
  dragId: string | null;
  dragOverStatus: Status | null;
  selected: Set<string>;
  memberName: Map<string, string>;
  onDragStart: (id: string) => void;
  onDragEnd: () => void;
  onDragOverColumn: (s: Status) => void;
  onDragLeaveColumn: (s: Status) => void;
  onDropColumn: (s: Status) => void;
  onKeyMove: (id: string, dir: -1 | 1) => void;
  onOverride: (id: string, action: "pin" | "unpin" | "pause" | "unpause") => void;
  onToggleSelect: (id: string) => void;
  onCreateClick: () => void;
  itemCount: number;
}) {
  if (props.itemCount === 0) {
    return (
      <EmptyBoard
        canWrite={props.canWrite}
        onCreateClick={props.onCreateClick}
      />
    );
  }

  return (
    <div className="board">
      {props.columns.map((col) => (
        <section
          key={col.status}
          className={`board-col ${
            props.dragOverStatus === col.status ? "board-col--drop" : ""
          }`}
          aria-label={`${STATUS_LABELS[col.status]} column`}
          onDragOver={(e) => {
            if (!props.canWrite) return;
            e.preventDefault();
            props.onDragOverColumn(col.status);
          }}
          onDragLeave={() => props.onDragLeaveColumn(col.status)}
          onDrop={(e) => {
            if (!props.canWrite) return;
            e.preventDefault();
            props.onDropColumn(col.status);
          }}
        >
          <div className="board-col__head">
            <GlyphDot color={STATUS_COLORS[col.status]} />
            <strong style={{ fontSize: 12.5 }}>
              {STATUS_LABELS[col.status]}
            </strong>
            <span className="board-col__count">{col.items.length}</span>
          </div>
          <div className="board-col__list">
            {col.items.length === 0 && props.canWrite && props.dragId ? (
              <p
                className="tiny muted"
                style={{ margin: "2px 4px 6px" }}
                aria-hidden
              >
                Drop here to move to {STATUS_LABELS[col.status]}.
              </p>
            ) : null}
            {col.items.map((it) => (
              <BoardCard
                key={it.id}
                item={it}
                canWrite={props.canWrite}
                selected={props.selected.has(it.id)}
                memberName={props.memberName}
                onDragStart={props.onDragStart}
                onDragEnd={props.onDragEnd}
                onKeyMove={props.onKeyMove}
                onOverride={props.onOverride}
                onToggleSelect={props.onToggleSelect}
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function EmptyBoard(props: { canWrite: boolean; onCreateClick: () => void }) {
  return (
    <div className="empty panel" style={{ padding: "48px 20px" }}>
      <h3>No cards to show</h3>
      <p>
        {props.canWrite
          ? "Create a work item, or check the filters above — an active filter can hide everything."
          : "Nothing matches the filters. Ask someone with write access to add work."}
      </p>
      {props.canWrite ? (
        <button type="button" className="btn" onClick={props.onCreateClick}>
          <IconPlus /> New item
        </button>
      ) : null}
    </div>
  );
}

/* --- card -------------------------------------------------------------------- */

function BoardCard(props: {
  item: Item;
  canWrite: boolean;
  selected: boolean;
  memberName: Map<string, string>;
  onDragStart: (id: string) => void;
  onDragEnd: () => void;
  onKeyMove: (id: string, dir: -1 | 1) => void;
  onOverride: (id: string, action: "pin" | "unpin" | "pause" | "unpause") => void;
  onToggleSelect: (id: string) => void;
}) {
  const it = props.item;
  return (
    <Link
      href={`/items/${it.id}`}
      data-id={it.id}
      className={[
        "board-card",
        it.paused ? "board-card--paused" : "",
        props.selected ? "board-card--selected" : "",
      ].join(" ")}
      aria-current={props.selected ? "true" : undefined}
      onDragStart={(e) => {
        if (!props.canWrite) return;
        e.dataTransfer.setData("text/plain", it.id);
        e.dataTransfer.effectAllowed = "move";
        props.onDragStart(it.id);
      }}
      onDragEnd={props.onDragEnd}
      onKeyDown={(e) => {
        if (e.metaKey || e.ctrlKey) {
          if (e.key === "ArrowRight") {
            e.preventDefault();
            props.onKeyMove(it.id, 1);
          } else if (e.key === "ArrowLeft") {
            e.preventDefault();
            props.onKeyMove(it.id, -1);
          }
        }
      }}
    >
      <span className="board-card__title">{it.title}</span>
      <div className="board-card__row">
        <span className="board-card__num">#{it.number}</span>
        <span className="chip chip--kind">{it.kind}</span>
        <PriorityChip priority={it.priority} />
        <ItemIndicators item={it} />
        {it.assignee_user_id && !props.canWrite ? (
          <span
            className="attr-assignee"
            title={props.memberName.get(it.assignee_user_id) ?? "Assigned"}
            style={{ marginLeft: "auto", flex: "none", maxWidth: 80, overflow: "hidden", textOverflow: "ellipsis" }}
          >
            {firstName(props.memberName.get(it.assignee_user_id)) || "@user"}
          </span>
        ) : null}
        {props.canWrite ? (
          <span
            className="attr-assignee"
            title={props.memberName.get(it.assignee_user_id ?? "") ?? "Unassigned"}
            style={{ marginLeft: "auto", overflow: "hidden", textOverflow: "ellipsis", maxWidth: it.assignee_user_id ? 80 : undefined, flex: "none" }}
          >
            {it.assignee_user_id
              ? firstName(props.memberName.get(it.assignee_user_id)) || "@user"
              : "Unassigned"}
          </span>
        ) : null}
        {props.canWrite ? (
          <span className="card-actions">
            <button
              type="button"
              className="minibtn"
              aria-label={it.pinned ? "Unpin" : "Pin to rank 1"}
              aria-pressed={Boolean(it.pinned)}
              onClick={(e) => {
                e.preventDefault();
                e.stopPropagation();
                props.onOverride(it.id, it.pinned ? "unpin" : "pin");
              }}
            >
              <IconPin width={12} height={12} />
            </button>
            <button
              type="button"
              className="minibtn"
              aria-label={it.paused ? "Unpause" : "Pause"}
              aria-pressed={Boolean(it.paused)}
              onClick={(e) => {
                e.preventDefault();
                e.stopPropagation();
                props.onOverride(it.id, it.paused ? "unpause" : "pause");
              }}
            >
              <IconPause width={12} height={12} />
            </button>
            <button
              type="button"
              className={props.selected ? "selbtn on" : "selbtn"}
              aria-label={props.selected ? "Deselect" : "Select"}
              aria-pressed={props.selected}
              onClick={(e) => {
                e.preventDefault();
                e.stopPropagation();
                props.onToggleSelect(it.id);
              }}
            >
              <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden>
                <rect
                  x="2"
                  y="2"
                  width="12"
                  height="12"
                  rx="3"
                  fill="none"
                  stroke={props.selected ? "var(--accent)" : "currentColor"}
                  strokeWidth="1.6"
                />
                {props.selected ? (
                  <path
                    d="m5 8.5 2 2 4-5"
                    fill="none"
                    stroke="var(--accent)"
                    strokeWidth="1.8"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                ) : null}
              </svg>
            </button>
          </span>
        ) : null}
      </div>
      {it.paused_at ? (
        <div className="tiny muted" style={{ marginTop: 4 }}>
          Paused{props.memberName.get(it.paused_by ?? "") ? ` by ${firstName(props.memberName.get(it.paused_by ?? ""))}` : ""}
          {it.pause_reason ? ` — ${it.pause_reason}` : ""}
        </div>
      ) : null}
    </Link>
  );
}

function GlyphDot({ color }: { color: string }) {
  return (
    <span
      aria-hidden
      style={{
        width: 8,
        height: 8,
        borderRadius: 999,
        background: color,
        flex: "none",
        display: "inline-block",
      }}
    />
  );
}
