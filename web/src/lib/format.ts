import type { Item, Priority, Status } from "./types";
import { getCurrentLang, translate, type TranslationKey, type TranslateValues } from "./locales";

/** The string for a key, in the language on screen. */
function tr<K extends TranslationKey>(key: K, values?: TranslateValues<K>): string {
  return translate(getCurrentLang(), key, values);
}

/**
 * The status names, in the language on screen. They are the product's shared
 * vocabulary — the board's columns, the status picker, the activity feed — so
 * they follow the interface everywhere it is used.
 */
export const STATUS_LABELS: Record<Status, string> = {
  get backlog() {
    return translate(getCurrentLang(), "status.backlog");
  },
  get todo() {
    return translate(getCurrentLang(), "status.todo");
  },
  get in_progress() {
    return translate(getCurrentLang(), "status.in_progress");
  },
  get waiting_human() {
    return translate(getCurrentLang(), "status.waiting_human");
  },
  get in_review() {
    return translate(getCurrentLang(), "status.in_review");
  },
  get done() {
    return translate(getCurrentLang(), "status.done");
  },
  get cancelled() {
    return translate(getCurrentLang(), "status.cancelled");
  },
};

export const PRIORITY_LABELS: Record<Priority, string> = {
  "priority:p0": "P0",
  "priority:p1": "P1",
  "priority:p2": "P2",
};

/** Short score factor name for display. Matches internal/priority/score.go keys. */
const FACTOR_KEYS: Record<string, TranslationKey> = {
  priority: "item.factor.priority",
  bug: "item.factor.bug",
  unblocks: "item.factor.unblocks",
  open_pr: "item.factor.openPr",
  age: "item.factor.age",
};

export function factorLabel(key: string): string {
  const known = FACTOR_KEYS[key];
  if (known) return tr(known);
  if (key.startsWith("hard_label_")) return `${tr("item.factor.label")} ${key.slice("hard_label_".length)}`;
  return key
    .replace(/^hard_label_/, "Label ")
    .replace(/_/g, " ")
    .replace(/^\w/, (c) => c.toUpperCase());
}

export function formatDate(value?: string): string {
  if (!value) return "";
  const d = new Date(value);
  // due_date arrives as a timestamp in JSON; show the date part only.
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleDateString(getCurrentLang(), {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

export function formatDateTime(value?: string): string {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleString(getCurrentLang(), {
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
      return tr("item.activity.created");
    case "item.deleted":
      return tr("item.activity.deleted");
    case "item.updated":
      return diff ? tr("item.activity.updated", { diff }) : tr("item.activity.edited");
    case "item.status_changed": {
      const b = fieldOf(before, "status");
      const a = fieldOf(after, "status");
      if (b && a) return tr("item.activity.statusChanged", { from: b, to: a });
      if (a) return tr("item.activity.statusChangedTo", { to: a });
      return tr("item.activity.statusChangedGeneric");
    }
    case "item.paused":
      return diff ? tr("item.activity.paused", { diff }) : tr("item.activity.pausedGeneric");
    case "item.unpaused":
      return tr("item.activity.unpaused");
    case "item.pinned":
      return tr("item.activity.pinned");
    case "item.unpinned":
      return tr("item.activity.unpinned");
    case "item.dependency_added":
      return tr("item.activity.dependencyAdded", { diff: diff || tr("item.activity.dependencyOther") });
    case "item.dependency_removed":
      return tr("item.activity.dependencyRemoved", { diff: diff || tr("item.activity.dependencyEdge") });
    case "product.created":
      return tr("item.activity.productCreated");
    case "product.updated":
      return diff ? tr("item.activity.productUpdated", { diff }) : tr("item.activity.productEdited");
    case "product.deleted":
      return tr("item.activity.productDeleted");
    case "project.created":
      return tr("item.activity.projectCreated");
    case "project.updated":
      return diff ? tr("item.activity.projectUpdated", { diff }) : tr("item.activity.projectEdited");
    case "project.deleted":
      return tr("item.activity.projectDeleted");
    default:
      return diff ? tr("item.activity.other", { action: action.replace(/_/g, " "), diff }) : action.replace(/_/g, " ");
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
