"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { api, type PatchItemBody } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import type {
  ActivityEntry,
  DependencyEdge,
  Item,
  ItemScore,
  Member,
  Priority,
  Status,
} from "@/lib/types";
import {
  activitySentence,
  formatDateTime,
  inputDate,
    factorLabel,
  firstName,
  STATUS_LABELS,
  PRIORITY_LABELS,
} from "@/lib/format";
import {
  Dialog,
  EmptyState,
  ErrorBlock,
  PrioritySelect,
  Skeleton,
  StatusSelect,
  Toasts,
  apiErrorMessage,
} from "@/components/primitives";
import { FactorTable } from "@/components/factor-table";
import { ItemIndicators } from "@/components/item-bits";
import { IconPause, IconPin, IconTrash } from "@/components/icons";

export default function ItemPage() {
  return (
    <Suspense fallback={<Skeleton h={400} />}>
      <ItemDetail />
    </Suspense>
  );
}

function ItemDetail() {
  const params = useParams<{ id: string }>();
  const id = typeof params.id === "string" ? params.id : "";
  const router = useRouter();
  const { me, canWrite } = useSession();
  const { toasts, push } = useToasts();

  const detail = useAsync(() => api.getItem(id), [id]);
  const activity = useAsync(() => api.activity(id), [id]);
  const score = useAsync(() => api.score(id), [id]);
  const members = useAsync(() => api.listMembers().then((r) => r.members), []);

  const memberName = useMemo(() => {
    const m = new Map<string, string>();
    for (const mem of members.data ?? []) m.set(mem.user_id, mem.name);
    return m;
  }, [members.data]);

  const [tab, setTab] = useState<"details" | "activity">("details");

  const item = detail.data?.item ?? null;
  const [draft, setDraft] = useState<EditableDraft | null>(null);
  useEffect(() => {
    if (item && !draft) {
      setDraft(draftFrom(item));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [item]);

  async function patch(body: PatchItemBody, successMsg?: string) {
    try {
      await api.patchItem(id, body);
      await detail.reload();
      push("info", successMsg ?? "Saved.");
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  async function quickOverride(
    action: "pin" | "unpin" | "pause" | "unpause",
    reason?: string,
  ) {
    try {
      if (action === "pin") await api.pinItem(id);
      else if (action === "unpin") await api.unpinItem(id);
      else if (action === "pause") await api.pauseItem(id, reason);
      else await api.unpauseItem(id);
      await detail.reload();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  async function remove() {
    if (!window.confirm("Delete this item? This cannot be undone.")) return;
    try {
      await api.deleteItem(id);
      router.push("/backlog");
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  if (detail.error) {
    return (
      <div style={{ maxWidth: 760 }}>
        <ErrorBlock
          title="That item could not load."
          message={detail.error.message}
          retry={() => void detail.reload()}
        />
      </div>
    );
  }
  if (detail.loading || !item) {
    return (
      <div style={{ display: "grid", gap: 10, maxWidth: 760 }}>
        <Skeleton h={18} w={160} />
        <Skeleton h={28} w="70%" />
        <Skeleton h={120} />
      </div>
    );
  }

  return (
    <div>
      <Breadcrumb itemId={item.id} number={item.number} />
      <HeaderRow
        item={item}
        canWrite={canWrite}
        canWriteAllowed={canWrite}
        members={members.data ?? []}
        memberName={memberName}
        me={me}
        onStatus={(s) => void patch({ status: s })}
        onPriority={(p) => void patch({ priority: p ?? "" })}
        onAssignee={(assignee_user_id) =>
          void patch({ assignee_kind: assignee_user_id ? "human" : "", assignee_user_id })
        }
        onPause={(reason) => void quickOverride("pause", reason)}
        onUnpause={() => void quickOverride("unpause")}
        onPin={() => void quickOverride(item.pinned ? "unpin" : "pin")}
        onDelete={() => void remove()}
      />

      <div className="tabs" role="tablist" aria-label="Item tabs">
        {(["details", "activity"] as const).map((t) => (
          <button
            key={t}
            role="tab"
            type="button"
            id={`tab-${t}`}
            aria-selected={tab === t}
            aria-controls={`panel-${t}`}
            onClick={() => setTab(t)}
          >
            {t === "details" ? "Details" : "Activity"}
          </button>
        ))}
      </div>

      {tab === "details" ? (
        <div id="panel-details" role="tabpanel" aria-labelledby="tab-details">
          {draft ? (
            <DetailsForm
              draft={draft}
              item={item}
              canWrite={canWrite}
              members={members.data ?? []}
              onSave={(d) => {
                void (async () => {
                  try {
                    await api.patchItem(id, {
                      title: d.title.trim(),
                      kind: d.kind.trim() || "task",
                      description_md: d.description_md,
                      due_date: d.due_date,
                      labels: labelsOf(d.labels),
                      github_issue_url: d.github_issue_url,
                      jira_issue_key: d.jira_issue_key,
                      external_url: d.external_url,
                    });
                    await detail.reload();
                    setDraft(null);
                    push("info", "Saved.");
                  } catch (err) {
                    push("error", apiErrorMessage(err));
                  }
                })();
              }}
            />
          ) : null}
          <DependenciesPanel
            item={item}
            blockers={detail.data?.blockers ?? []}
            dependents={detail.data?.dependents ?? []}
            canWrite={canWrite}
            onReload={async () => {
              await detail.reload();
            }}
          />
          <ScorePanel item={item} score={score.data} loading={score.loading} />
        </div>
      ) : (
        <ActivityTimeline
          entries={activity.data?.activity ?? []}
          loading={activity.loading}
          error={activity.error?.message ?? null}
          memberName={memberName}
          onRetry={() => void activity.reload()}
        />
      )}

      <Toasts toasts={toasts} />
    </div>
  );
}

/* --- editable-draft plumbing -------------------------------------------------- */

interface EditableDraft {
  title: string;
  kind: string;
  description_md: string;
  due_date: string;
  labels: string;
  github_issue_url: string;
  jira_issue_key: string;
  external_url: string;
}

function draftFrom(it: Item): EditableDraft {
  return {
    title: it.title,
    kind: it.kind,
    description_md: it.description_md ?? "",
    due_date: inputDate(it.due_date),
    labels: it.labels.join(", "),
    github_issue_url: it.github_issue_url ?? "",
    jira_issue_key: it.jira_issue_key ?? "",
    external_url: it.external_url ?? "",
  };
}

function labelsOf(s: string): string[] {
  return s
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
}

/* --- breadcrumb & header ------------------------------------------------------ */

function Breadcrumb(props: { itemId: string; number: number }) {
  return (
    <p className="tiny muted" style={{ margin: "0 0 6px" }}>
      <Link href="/board" className="rowlink">
        Board
      </Link>{" "}
      / item #{props.number}
    </p>
  );
}

function HeaderRow(props: {
  item: Item;
  canWrite: boolean;
  canWriteAllowed: boolean;
  members: Member[];
  memberName: Map<string, string>;
  me: ReturnType<typeof useSession>["me"];
  onStatus: (s: Status) => void;
  onPriority: (p: Priority | null) => void;
  onAssignee: (assignee_user_id: string) => void;
  onPause: (reason: string) => void;
  onUnpause: () => void;
  onPin: () => void;
  onDelete: () => void;
}) {
  const it = props.item;
  const [pauseOpen, setPauseOpen] = useState(false);
  const [reason, setReason] = useState("");
  const kindLabel = it.kind || "item";
  return (
    <div className="section" style={{ marginBottom: 16 }}>
      <div className="section__body" style={{ paddingTop: 12 }}>
        <div style={{ display: "flex", flexWrap: "wrap", gap: 10, alignItems: "center" }}>
          <span className="mono muted">#{it.number}</span>
          <span className="chip chip--kind">{kindLabel}</span>
          <ItemIndicators item={it} size={13} />
          <h1 style={{ fontSize: 18, margin: 0, flex: "1 1 260px", minWidth: 240 }}>
            {it.title}
          </h1>
          {props.canWrite ? (
            <span className="stack">
              <button
                type="button"
                className="btn btn--sm"
                aria-label={it.pinned ? "Unpin this item" : "Pin to queue top"}
                aria-pressed={Boolean(it.pinned)}
                onClick={props.onPin}
              >
                <IconPin width={12} height={12} /> {it.pinned ? "Unpin" : "Pin"}
              </button>
              {it.paused ? (
                <button type="button" className="btn btn--sm" onClick={props.onUnpause}>
                  Unpause
                </button>
              ) : (
                <button
                  type="button"
                  className="btn btn--sm"
                  onClick={() => setPauseOpen(true)}
                >
                  <IconPause width={12} height={12} /> Pause
                </button>
              )}
              <button
                type="button"
                className="btn btn--sm btn--ghost btn--danger"
                aria-label="Delete this item"
                onClick={props.onDelete}
              >
                <IconTrash />
              </button>
            </span>
          ) : null}
        </div>

        {it.paused ? (
          <div
            className="tiny"
            style={{
              marginTop: 8,
              padding: "7px 10px",
              borderRadius: 6,
              background: "rgb(240 180 41 / 0.08)",
              border: "1px solid rgb(240 180 41 / 0.3)",
              color: "var(--warn)",
            }}
          >
            Paused
            {it.paused_at ? ` since ${formatDateTime(it.paused_at)}` : ""}
            {it.paused_by ? ` by @${firstName(props.memberName.get(it.paused_by) ?? "") || it.paused_by}` : ""}
            {it.pause_reason ? ` — “${it.pause_reason}”` : ""}. A pause is not a
            failure: it only removes the item from dispatch.
          </div>
        ) : null}
        {it.pinned ? (
          <div className="tiny muted" style={{ marginTop: 6 }}>
            Pinned to rank 1 in the queue
            {it.pinned_by
              ? ` by @${firstName(props.memberName.get(it.pinned_by) ?? "") || it.pinned_by}`
              : ""}
            {it.pinned_at ? `, ${formatDateTime(it.pinned_at)}` : ""}.
          </div>
        ) : null}

        <div
          className="stack"
          style={{ marginTop: 12, gap: 10, flexWrap: "wrap", alignItems: "center" }}
        >
          {props.canWrite ? (
            <StatusSelect
              value={it.status}
              ariaLabel="Status"
              onChange={props.onStatus}
            />
          ) : (
            <span className="badge-outline">{STATUS_LABELS[it.status]}</span>
          )}
          {props.canWrite ? (
            <PrioritySelect value={it.priority} onChange={props.onPriority} />
          ) : (
            <span className="tiny">
              {it.priority ? PRIORITY_LABELS[it.priority] : "No priority"}
              {it.priority_source === "inherited" ? (
                <span className="muted"> (inherited)</span>
              ) : null}
            </span>
          )}
          {props.canWrite ? (
            <select
              className="input input--inline"
              value={it.assignee_user_id ?? ""}
              aria-label="Assignee"
              onChange={(e) => props.onAssignee(e.target.value)}
            >
              <option value="">Unassigned</option>
              {props.members.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.name}
                </option>
              ))}
            </select>
          ) : it.assignee_user_id ? (
            <span className="tiny">
              Assigned to{" "}
              {props.memberName.get(it.assignee_user_id) ?? it.assignee_user_id}
            </span>
          ) : null}
          {it.due_date ? (
            <span className="tiny muted">Due {it.due_date.slice(0, 10)}</span>
          ) : null}
        </div>
      </div>

      {pauseOpen ? (
        <Dialog
          title={`Pause #${it.number}`}
          description="A pause removes the item from dispatch. It is not a failure and never expires by itself."
          onClose={() => setPauseOpen(false)}
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setPauseOpen(false);
              props.onPause(reason.trim());
            }}
            style={{ display: "grid", gap: 12 }}
          >
            <div className="field">
              <label htmlFor="i-reason">Reason (optional)</label>
              <input
                id="i-reason"
                className="input"
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder="Why this waits"
              />
            </div>
            <div className="dialog__row">
              <button type="button" className="btn" onClick={() => setPauseOpen(false)}>
                Cancel
              </button>
              <button type="submit" className="btn btn--primary">
                Pause
              </button>
            </div>
          </form>
        </Dialog>
      ) : null}
    </div>
  );
}

/* --- details form -------------------------------------------------------------- */

function DetailsForm(props: {
  draft: EditableDraft;
  item: Item;
  canWrite: boolean;
  members: Member[];
  onSave: (d: EditableDraft) => void;
}) {
  const [d, setD] = useState<EditableDraft>(props.draft);
  useEffect(() => {
    setD(props.draft);
  }, [props.draft]);
  const set = <K extends keyof EditableDraft>(k: K, v: string) =>
    setD((x) => ({ ...x, [k]: v }));

  const dirty = JSON.stringify(d) !== JSON.stringify(props.draft);

  return (
    <div className="section">
      <div className="section__head">
        <h2>Details</h2>
      </div>
      <div className="section__body" style={{ display: "grid", gap: 12 }}>
        {props.canWrite ? (
          <>
            <div className="field">
              <label htmlFor="i-title">Title</label>
              <input
                id="i-title"
                className="input"
                value={d.title}
                onChange={(e) => set("title", e.target.value)}
              />
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "160px 1fr", gap: 12 }}>
              <div className="field">
                <label htmlFor="i-kind">Kind</label>
                <input
                  id="i-kind"
                  className="input"
                  value={d.kind}
                  onChange={(e) => set("kind", e.target.value)}
                />
                <span className="tiny muted">Free-form: task, bug, epic…</span>
              </div>
              <div className="field">
                <label htmlFor="i-due">Due date</label>
                <input
                  id="i-due"
                  type="date"
                  className="input"
                  value={d.due_date}
                  onChange={(e) => set("due_date", e.target.value)}
                />
              </div>
            </div>
            <div className="field">
              <label htmlFor="i-desc">Description (markdown)</label>
              <textarea
                id="i-desc"
                className="input"
                rows={6}
                value={d.description_md}
                onChange={(e) => set("description_md", e.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="i-labels">Labels</label>
              <input
                id="i-labels"
                className="input"
                value={d.labels}
                onChange={(e) => set("labels", e.target.value)}
                placeholder="bug, frontend, infra"
              />
              <span className="tiny muted">Comma separated.</span>
            </div>
            <div className="field">
              <label htmlFor="i-gh">GitHub issue or PR URL</label>
              <input
                id="i-gh"
                className="input"
                value={d.github_issue_url}
                onChange={(e) => set("github_issue_url", e.target.value)}
              />
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
              <div className="field">
                <label htmlFor="i-jira">Jira issue key</label>
                <input
                  id="i-jira"
                  className="input"
                  value={d.jira_issue_key}
                  onChange={(e) => set("jira_issue_key", e.target.value)}
                />
              </div>
              <div className="field">
                <label htmlFor="i-url">Other URL</label>
                <input
                  id="i-url"
                  className="input"
                  value={d.external_url}
                  onChange={(e) => set("external_url", e.target.value)}
                />
              </div>
            </div>
            <div className="stack" style={{ justifyContent: "flex-end" }}>
              {!dirty ? <span className="tiny muted">No changes.</span> : null}
              <button
                type="button"
                className="btn"
                disabled={!dirty}
                onClick={() => setD(props.draft)}
              >
                Reset
              </button>
              <button
                type="submit"
                className="btn btn--primary"
                disabled={!dirty}
                onClick={(e) => {
                  e.preventDefault();
                  props.onSave(d);
                }}
              >
                Save changes
              </button>
            </div>
          </>
        ) : (
          <ReadOnlyDetails item={props.item} />
        )}
      </div>
    </div>
  );
}

function ReadOnlyDetails({ item }: { item: Item }) {
  const md = item.description_md;
  return (
    <div style={{ display: "grid", gap: 10 }}>
      <div className="tiny muted" style={{ whiteSpace: "pre-wrap" }}>
        {md || "No description."}
      </div>
      {item.labels.length > 0 ? (
        <div className="stack">
          {item.labels.map((l) => (
            <span key={l} className="badge-outline">
              {l}
            </span>
          ))}
        </div>
      ) : null}
      <LinksOf item={item} />
    </div>
  );
}

function LinksOf({ item }: { item: Item }) {
  const links: { href: string; label: string }[] = [];
  if (item.github_issue_url)
    links.push({ href: item.github_issue_url, label: "GitHub issue" });
  if (item.external_url) links.push({ href: item.external_url, label: "External link" });
  if (item.jira_issue_key)
    links.push({ href: `https://jira.atlassian.com/browse/${item.jira_issue_key}`, label: item.jira_issue_key });
  if (links.length === 0) return null;
  return (
    <div className="stack" style={{ flexWrap: "wrap" }}>
      {links.map((l) => (
        <a
          key={l.label}
          href={l.href}
          target="_blank"
          rel="noreferrer noopener"
          className="badge-outline"
          style={{ color: "var(--info)" }}
        >
          {l.label}
        </a>
      ))}
    </div>
  );
}

/* --- dependencies --------------------------------------------------------------- */

function DependenciesPanel(props: {
  item: Item;
  blockers: DependencyEdge[];
  dependents: DependencyEdge[];
  canWrite: boolean;
  onReload: () => Promise<void>;
}) {
  const { toasts, push } = useToasts();
  const [adding, setAdding] = useState(false);
  const [pick, setPick] = useState("");
  const candidates = useAsync(
    // Same-project list is what /api/items can search; dependencies may
    // reference any item id, but choosing from the current project keeps the
    // picker small and honest.
    () => api.listItems({ project_id: props.item.project_id }).then((r) => r.items),
    [props.item.project_id],
  );

  async function add() {
    if (!pick) return;
    try {
      await api.addDependency(props.item.id, pick);
      push("info", "Dependency added.");
      setPick("");
      setAdding(false);
      await props.onReload();
    } catch (err) {
      // The 409 dependency_cycle answer must reach the user verbatim.
      push("error", apiErrorMessage(err));
    }
  }

  async function removeEdge(depId: string) {
    try {
      await api.removeDependency(props.item.id, depId);
      await props.onReload();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  return (
    <div className="section">
      <div className="section__head">
        <h2>Dependencies</h2>
        <span className="tiny muted" style={{ marginLeft: "auto" }}>
          Blocked-by edges gate the queue: a blocked item is not dispatchable.
        </span>
      </div>
      <div className="section__body" style={{ display: "grid", gap: 10 }}>
        <ListEdges
          label="Blocked by"
          empty="No open blockers."
          edges={props.blockers}
          canWrite={props.canWrite}
          onRemove={removeEdge}
          push={push}
        />
        <ListEdges
          label="Blocks"
          empty="Nothing waits on this item."
          edges={props.dependents}
          canWrite={props.canWrite}
          onRemove={removeEdge}
          push={push}
        />
        {props.canWrite ? (
          adding ? (
            <div className="stack" style={{ alignItems: "flex-end", flexWrap: "wrap" }}>
              <div className="field" style={{ flex: "1 1 280px" }}>
                <label htmlFor="i-blk">Pick an item to wait on</label>
                <select
                  id="i-blk"
                  className="input"
                  value={pick}
                  onChange={(e) => setPick(e.target.value)}
                >
                  <option value="">Choose…</option>
                  {(candidates.data ?? [])
                    .filter(
                      (c) =>
                        c.id !== props.item.id &&
                        !props.blockers.some((b) => b.other.id === c.id),
                    )
                    .map((c) => (
                      <option key={c.id} value={c.id}>
                        #{c.number} — {c.title} ({STATUS_LABELS[c.status]})
                      </option>
                    ))}
                </select>
              </div>
              <button
                type="button"
                className="btn"
                disabled={!pick}
                onClick={() => void add()}
              >
                Add dependency
              </button>
              <button
                type="button"
                className="btn btn--ghost"
                onClick={() => {
                  setAdding(false);
                  setPick("");
                }}
              >
                Cancel
              </button>
            </div>
          ) : (
            <div>
              <button type="button" className="btn btn--sm" onClick={() => setAdding(true)}>
                Add a blocker
              </button>
            </div>
          )
        ) : null}
      </div>
      <Toasts toasts={toasts} />
    </div>
  );
}

function ListEdges(props: {
  label: string;
  empty: string;
  edges: DependencyEdge[];
  canWrite: boolean;
  onRemove: (depId: string) => Promise<void>;
  push: (kind: "error" | "info", text: string) => void;
}) {
  return (
    <div>
      <div className="tiny muted" style={{ marginBottom: 4 }}>
        <strong>{props.label}</strong>
      </div>
      {props.edges.length === 0 ? (
        <p className="tiny muted" style={{ margin: 0 }}>
          {props.empty}
        </p>
      ) : (
        <ul style={{ margin: 0, padding: 0, listStyle: "none", display: "grid", gap: 4 }}>
          {props.edges.map((edge) => (
            <li
              key={edge.id}
              className="stack tiny"
              style={{
                justifyContent: "space-between",
                padding: "5px 8px",
                background: "var(--bg-raised)",
                borderRadius: 6,
              }}
            >
              <Link href={`/items/${edge.other.id}`} className="rowlink">
                <span className="mono muted">#{edge.other.number}</span>{" "}
                {edge.other.title}{" "}
                <span className="muted">— {STATUS_LABELS[edge.other.status]}</span>
              </Link>
              {props.canWrite ? (
                <button
                  type="button"
                  className="btn btn--sm btn--ghost"
                  aria-label={`Remove dependency on ${edge.other.title}`}
                  onClick={() => void props.onRemove(edge.id)}
                >
                  remove
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/* --- score ------------------------------------------------------------------------ */

function ScorePanel(props: {
  item: Item;
  score: ItemScore | null;
  loading: boolean;
}) {
  return (
    <div className="section">
      <div className="section__head">
        <h2>Score</h2>
        {props.score ? (
          <span className="tiny muted" style={{ marginLeft: "auto" }}>
            weights {props.score.weights_version}
          </span>
        ) : null}
      </div>
      <div className="section__body">
        {props.loading ? (
          <Skeleton h={70} />
        ) : props.score ? (
          <>
            {props.score.ineligible ? (
              <p
                className="tiny"
                style={{
                  margin: "0 0 10px",
                  color: "var(--warn)",
                  background: "rgb(240 180 41 / 0.07)",
                  border: "1px solid rgb(240 180 41 / 0.3)",
                  borderRadius: 6,
                  padding: "7px 10px",
                }}
              >
                {explainIneligible(props.score.ineligible)}
              </p>
            ) : null}
            <div style={{ marginBottom: 8 }}>
              <span className="metric" style={{ fontSize: 20 }}>
                {props.score.base_score}
              </span>
              <span className="tiny muted" style={{ marginLeft: 8 }}>
                base score
              </span>
            </div>
            <FactorTable
              factors={props.score.factors}
              baseScore={props.score.base_score}
            />
            <p className="tiny muted" style={{ marginTop: 10 }}>
              Why this number: the base score is the sum of each factor’s
              contribution. Each factor takes its input (a label, a count, the
              item’s age) times its weight — with nothing it contributes zero.
              The same inputs always produce the same number, which is what
              makes it reproducible.
            </p>
          </>
        ) : (
          <EmptyState
            title="No score available"
            body="The score panel could not be loaded for this item."
          />
        )}
      </div>
    </div>
  );
}

function explainIneligible(reason: string): string {
  switch (reason) {
    case "closed":
      return "Closed items (done or cancelled) are not dispatched, so the score is informational only.";
    case "paused":
      return "Paused — a human removed this from dispatch. Unpause to make it eligible again.";
    case "blocked":
      return "Blocked — an open blocked-by dependency must be resolved first.";
    case "epic":
      return "Epic — this item has open sub-items, so it is not dispatched itself.";
    default:
      return `Not eligible (${reason}).`;
  }
}

/* --- activity ------------------------------------------------------------------------ */

function ActivityTimeline(props: {
  entries: ActivityEntry[];
  loading: boolean;
  error: string | null;
  memberName: Map<string, string>;
  onRetry: () => void;
}) {
  const sorted = useMemo(
    () =>
      [...props.entries].sort(
        (a, b) => Date.parse(b.created_at) - Date.parse(a.created_at),
      ),
    [props.entries],
  );

  if (props.loading) {
    return (
      <div className="panel section__body" style={{ display: "grid", gap: 10 }}>
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} h={14} w={`${50 + ((i * 11) % 40)}%`} />
        ))}
      </div>
    );
  }
  if (props.error) {
    return <ErrorBlock message={props.error} retry={props.onRetry} />;
  }
  if (sorted.length === 0) {
    return (
      <EmptyState
        title="No activity yet"
        body="Changes to this item — status moves, edits, queue overrides — are recorded here with who made them."
      />
    );
  }

  return (
    <div className="panel">
      <ul className="timeline section__body">
        {sorted.map((e) => (
          <li key={e.id}>
            <div className="tiny muted">{formatDateTime(e.created_at)}</div>
            <div>
              {actorName(e, props.memberName)} {activitySentence(e.action, e.before, e.after)}
            </div>
            <AfterDiff entry={e} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function actorName(e: ActivityEntry, memberName: Map<string, string>): string {
  if (e.actor_kind === "system") return "The system";
  if (e.actor_kind === "agent") return "An agent";
  const name = e.actor_user_id
    ? firstName(memberName.get(e.actor_user_id) ?? "")
    : "";
  return name ? <strong>{name}</strong> as unknown as string : "Someone";
}

function AfterDiff({ entry }: { entry: ActivityEntry }) {
  const after = entry.after as Record<string, unknown> | null;
  if (!after || typeof after !== "object") return null;
  const parts: string[] = [];
  for (const [k, v] of Object.entries(after)) {
    if (k === "updated_at" || k === "id") continue;
    if (typeof v === "string" && v.length < 60) {
      parts.push(`${factorLabel(k.replace(/_id$/, ""))}: ${v}`);
    }
  }
  if (parts.length === 0) return null;
  return (
    <div className="tiny muted mono" style={{ marginTop: 2 }}>
      {parts.slice(0, 3).join(" · ")}
    </div>
  );
}

// end of file

