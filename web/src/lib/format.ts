import type { Item, Priority, Status } from "./types";

export const STATUS_LABELS: Record<Status, string> = {
  backlog: "Backlog",
  todo: "To do",
  in_progress: "In progress",
  waiting_human: "Waiting on human",
  in_review: "In review",
  done: "Done",
  cancelled: "Cancelled",
};

export const PRIORITY_LABELS: Record<Priority, string> = {
  "priority:p0": "P0",
  "priority:p1": "P1",
  "priority:p2": "P2",
};

/** Short score factor name for display. Matches internal/priority/score.go keys. */
export const FACTOR_LABELS: Record<string, string> = {
  priority: "Priority",
  bug: "Bug label",
  unblocks: "Unblocks",
  open_pr: "Open PR",
  age: "Age",
};

export function factorLabel(key: string): string {
  return (
    FACTOR_LABELS[key] ??
    key
      .replace(/^hard_label_/, "Label ")
      .replace(/_/g, " ")
      .replace(/^\w/, (c) => c.toUpperCase())
  );
}

export function formatDate(value?: string): string {
  if (!value) return "";
  const d = new Date(value);
  // due_date arrives as a timestamp in JSON; show the date part only.
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

export function formatDateTime(value?: string): string {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** "3d ago", "2h ago" — compact relative time for tables and timelines. */
export function relTime(value?: string): string {
  if (!value) return "";
  const t = Date.parse(value);
  if (Number.isNaN(t)) return "";
  const s = Math.round((Date.now() - t) / 1000);
  if (s < 60) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d}d ago`;
  const mo = Math.round(d / 30);
  if (mo < 12) return `${mo}mo ago`;
  return `${Math.round(mo / 12)}y ago`;
}

/** YYYY-MM-DD for date inputs. */
export function inputDate(value?: string | null): string {
  if (!value) return "";
  const d = new Date(value);
  return Number.isNaN(d.getTime())
    ? value.slice(0, 10)
    : d.toISOString().slice(0, 10);
}

export function firstName(name?: string): string {
  if (!name) return "";
  return name.split(/\s+/)[0] ?? name;
}

/**
 * Human-readable one-liner for an item activity action, with an optional
 * field-level diff summary for before/after payloads.
 */
export function activitySentence(
  action: string,
  before?: unknown,
  after?: unknown,
): string {
  const diff = diffSummary(before, after);
  switch (action) {
    case "item.created":
      return "created this item";
    case "item.deleted":
      return "deleted this item";
    case "item.updated":
      return diff ? `updated ${diff}` : "edited details";
    case "item.status_changed": {
      const b = fieldOf(before, "status");
      const a = fieldOf(after, "status");
      if (b && a) return `moved status ${b} → ${a}`;
      if (a) return `moved status to ${a}`;
      return "changed status";
    }
    case "item.paused":
      return diff ? `paused (${diff})` : "paused this item";
    case "item.unpaused":
      return "unpaused this item";
    case "item.pinned":
      return "pinned this item to rank 1";
    case "item.unpinned":
      return "unpinned this item";
    case "item.dependency_added":
      return `added a dependency: blocked by ${diff || "another item"}`;
    case "item.dependency_removed":
      return `removed a dependency: ${diff || "edge"}`;
    case "product.created":
      return "created a product";
    case "product.updated":
      return diff ? `updated a product ${diff}` : "edited a product";
    case "product.deleted":
      return "deleted a product";
    case "project.created":
      return "created a project";
    case "project.updated":
      return diff ? `updated a project ${diff}` : "edited a project";
    case "project.deleted":
      return "deleted a project";
    default:
      return diff ? `${action.replace(/_/g, " ")} ${diff}` : action.replace(/_/g, " ");
  }
}

function fieldOf(payload: unknown, field: string): string | undefined {
  if (payload && typeof payload === "object" && field in (payload as object)) {
    const v = (payload as Record<string, unknown>)[field];
    if (typeof v === "string") return STATUS_LABELS[v as Status] ?? v;
  }
  return undefined;
}

/** Compares before/after JSON objects and names the changed fields. */
export function diffSummary(before?: unknown, after?: unknown): string {
  if (!before && !after) return "";
  const b = (before ?? {}) as Record<string, unknown>;
  const a = (after ?? {}) as Record<string, unknown>;

  // Dependency actions carry the other item under a key; prefer its title.
  for (const side of [a, b]) {
    const title = titleOfPayload(side);
    if (title) return title;
  }

  const keys = new Set([...Object.keys(b), ...Object.keys(a)]);
  keys.delete("updated_at");
  const changed: string[] = [];
  for (const k of keys) {
    const bv = JSON.stringify(normalize(b[k]));
    const av = JSON.stringify(normalize(a[k]));
    if (before && !after && k in b) continue;
    if (bv !== av) {
      changed.push(prettyKey(k));
    }
  }
  return changed.slice(0, 4).join(", ");
}

function titleOfPayload(payload: unknown): string | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const o = payload as Record<string, unknown>;
  if (typeof o.title === "string" && (typeof o.number === "number" || o.id)) {
    const n = typeof o.number === "number" ? `#${o.number} ` : "";
    return `${n}${o.title}`;
  }
  return undefined;
}

function normalize(v: unknown): unknown {
  // Timestamps round-trip with formatting differences that we do not want to
  // surface as real changes.
  if (typeof v === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(v)) {
    return v.slice(0, 10);
  }
  if (Array.isArray(v)) return v.slice().sort().join(", ");
  return v;
}

function prettyKey(k: string): string {
  const map: Record<string, string> = {
    description_md: "description",
    status: "status",
    blocked_by_id: "blocked-by",
  };
  return (map[k] ?? k).replace(/_id?$/, "").replace(/_/g, " ");
}

/** True when the item participates in queue overrides. */
export function isPinned(item: Pick<Item, "pinned">): boolean {
  return Boolean(item.pinned);
}
