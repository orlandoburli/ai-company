"use client";

import { Suspense, useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { api, ApiError, type PatchItemBody } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useConfirm } from "@/lib/confirm";
import { useSession } from "@/lib/session";
import type {
  ActivityEntry,
  DependencyEdge,
  FailureState,
  GitHubItemLink,
  Item,
  ItemChildRef,
  ItemFamilyRef,
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
  KindCombobox,
  LabelInput,
  PrioritySelect,
  Skeleton,
  StatusSelect,
  Toasts,
  apiErrorMessage,
} from "@/components/primitives";
import { ChatPanel } from "@/components/chat";
import { FactorTable } from "@/components/factor-table";
import { ItemIndicators } from "@/components/item-bits";
import { IconPause, IconPin, IconTrash } from "@/components/icons";
import { useT } from "@/lib/i18n";
import type { TranslationKey } from "@/lib/locales";

/** The translator, for the helpers that live outside a component. */
type T = ReturnType<typeof useT>;

export default function ItemPage() {
  return (
    <Suspense fallback={<Skeleton h={400} />}>
      <ItemDetail />
    </Suspense>
  );
}

function ItemDetail() {
  const t = useT();
  const params = useParams<{ id: string }>();
  const id = typeof params.id === "string" ? params.id : "";
  const router = useRouter();
  const { me, canWrite } = useSession();
  const { toasts, push } = useToasts();
  const confirm = useConfirm();

  const detail = useAsync(() => api.getItem(id), [id]);
  const activity = useAsync(() => api.activity(id), [id]);
  const score = useAsync(() => api.score(id), [id]);
  const members = useAsync(() => api.listMembers().then((r) => r.members), []);

  const memberName = useMemo(() => {
    const m = new Map<string, string>();
    for (const mem of members.data ?? []) m.set(mem.user_id, mem.name);
    return m;
  }, [members.data]);

  const [tab, setTab] = useState<"details" | "activity" | "chat">("details");

  const item = detail.data?.item ?? null;
  const kinds = useAsync(
    () => item ? api.listProjectKinds(item.project_id) : Promise.resolve({ kinds: [] }),
    [item?.project_id],
  );
  const labels = useAsync(
    () => item ? api.listLabels(item.project_id) : Promise.resolve({ labels: [] }),
    [item?.project_id],
  );
  const githubLinks = useAsync(
    () => item ? api.githubLinks(item.id) : Promise.resolve({ links: [] }),
    [item?.id],
  );
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
      push("info", successMsg ?? t("item.saved"));
    } catch (err) {
      push("error", apiIssue(t, err));
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
      push("error", apiIssue(t, err));
    }
  }

  async function remove() {
    if (!await confirm({ title: t("item.delete.title"), message: t("item.delete.message"), confirmLabel: t("item.delete.confirm"), destructive: true })) return;
    try {
      await api.deleteItem(id);
      router.push("/backlog");
    } catch (err) {
      push("error", apiIssue(t, err));
    }
  }

  if (detail.error) {
    return (
      <div style={{ maxWidth: 760 }}>
        <ErrorBlock
          title={t("item.loadFailed")}
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

      <div className="tabs" role="tablist" aria-label={t("item.tabs.aria")}>
        {(["details", "activity", "chat"] as const).map((t2) => (
          <button
            key={t2}
            role="tab"
            type="button"
            id={`tab-${t2}`}
            aria-selected={tab === t2}
            aria-controls={`panel-${t2}`}
            onClick={() => setTab(t2)}
          >
            {t2 === "details" ? t("item.tab.details") : t2 === "chat" ? t("item.tab.chat") : t("item.tab.activity")}
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
              kinds={(kinds.data?.kinds ?? []).map((entry) => entry.name)}
              kindsLoading={kinds.loading}
              kindsError={Boolean(kinds.error)}
              labelOptions={labels.data?.labels ?? []}
              labelsLoading={labels.loading}
              labelsError={Boolean(labels.error)}
              onSave={(d) => {
                void (async () => {
                  try {
                    await api.patchItem(id, {
                      title: d.title.trim(),
                      kind: d.kind.trim() || "task",
                      description_md: d.description_md,
                      due_date: d.due_date,
                      labels: d.labels,
                      jira_issue_key: d.jira_issue_key,
                      external_url: d.external_url,
                    });
                    await detail.reload();
                    setDraft(null);
                    push("info", t("item.saved"));
                  } catch (err) {
                    push("error", apiIssue(t, err));
                  }
                })();
              }}
            />
          ) : null}
          <ItemFamily parent={detail.data?.parent ?? null} children={detail.data?.children ?? []} />
          <GitHubLinksPanel
            links={githubLinks.data?.links ?? []}
            loading={githubLinks.loading}
            error={githubLinks.error?.message ?? null}
            onRetry={() => void githubLinks.reload()}
          />
          <FailurePanel itemId={item.id} paused={Boolean(item.paused || item.paused_at)} canWrite={canWrite} push={push} />
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
      ) : tab === "activity" ? (
        <ActivityTimeline
          entries={activity.data?.activity ?? []}
          loading={activity.loading}
          error={activity.error?.message ?? null}
          memberName={memberName}
          onRetry={() => void activity.reload()}
        />
      ) : (
        <ChatPanelSlot itemId={item.id} canWrite={canWrite} />
      )}

      <Toasts toasts={toasts} />
    </div>
  );
}

/* --- chat tab: get-or-create the item's thread --------------------------------- */

function ChatPanelSlot(props: { itemId: string; canWrite: boolean }) {
  const t = useT();
  const [threadId, setThreadId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const open = useCallback(async () => {
    setError(null);
    try {
      const r = await api.itemThread(props.itemId);
      setThreadId(r.thread.id);
    } catch (err) {
      setError(apiIssue(t, err));
    }
  }, [props.itemId, t]);

  useEffect(() => {
    void open();
  }, [open]);

  if (error) {
    return (
      <ErrorBlock
        title={t("item.chat.openFailed")}
        message={error}
        retry={() => void open()}
      />
    );
  }
  if (!threadId) {
    return <Skeleton h={220} />;
  }
  return (
    <ChatPanel
      threadId={threadId}
      canWrite={props.canWrite}
      fullHref={`/chat/${threadId}`}
    />
  );
}

type FailurePanelData =
  | { kind: "failure"; failure: FailureState }
  | { kind: "absent"; reason: string; code?: string };

function FailurePanel(props: {
  itemId: string;
  paused: boolean;
  canWrite: boolean;
  push: (kind: "error" | "info", text: string) => void;
}) {
  const t = useT();
  const [now, setNow] = useState(() => Date.now());
  const [saving, setSaving] = useState(false);
  const state = useAsync<FailurePanelData>(async () => {
    if (props.paused) {
      return { kind: "absent", reason: t("item.failure.paused") };
    }
    const runs = (await api.listRuns({ item_id: props.itemId })).runs;
    const latest = [...runs].sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))[0];
    if (!latest?.template_id) {
      return { kind: "absent", reason: t("item.failure.noRun") };
    }
    try {
      return { kind: "failure", failure: (await api.getFailure(props.itemId, latest.template_id)).failure };
    } catch (error) {
      if (error instanceof ApiError && error.code === "failure_not_found") {
        return { kind: "absent", code: error.code, reason: t("item.failure.none") };
      }
      throw error;
    }
  }, [props.itemId, props.paused, t]);

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  async function retryNow(failure: FailureState) {
    setSaving(true);
    try {
      await api.resetFailure(props.itemId, failure.workflow_id);
      await state.reload();
      props.push("info", t("item.failure.cleared"));
    } catch (error) {
      props.push("error", apiIssue(t, error));
    } finally {
      setSaving(false);
    }
  }

  const failure = state.data?.kind === "failure" ? state.data.failure : null;
  const absent = state.data?.kind === "absent" ? state.data : null;
  const nextAt = failure?.next_eligible_at ? Date.parse(failure.next_eligible_at) : NaN;
  const timeLeft = nextAt - now;
  const perFailure = failure
    ? failure.curve === "exponential"
      ? t("item.failure.perFailure.exponential", { factor: failure.backoff_params.y })
      : t("item.failure.perFailure.linear", { step: failure.backoff_params.step_seconds })
    : "";

  return (
    <section className="section" aria-labelledby="failure-heading">
      <div className="section__head">
        <h2 id="failure-heading">{t("item.failure.title")}</h2>
        {failure ? <span className={`chip ${failure.state === "exhausted" ? "chip--p0" : "chip--kind"}`} style={{ marginLeft: "auto" }}>{failure.state === "exhausted" ? t("item.failure.state.exhausted") : t("item.failure.state.backingOff")}</span> : null}
      </div>
      <div className="section__body">
        {state.error ? (
          <ErrorBlock title={t("item.failure.loadFailed")} message={apiIssue(t, state.error)} retry={() => void state.reload()} />
        ) : state.loading ? (
          <Skeleton h={76} />
        ) : failure ? (
          <div style={{ display: "grid", gap: 10 }}>
            <div className="stack" style={{ flexWrap: "wrap", gap: 18 }}>
              <div><div className="tiny muted">{t("item.failure.consecutive")}</div><strong className="metric" style={{ fontSize: 20 }}>{failure.consecutive_failures}</strong></div>
              <div><div className="tiny muted">{t("item.failure.curve")}</div><strong>{failure.curve}</strong> <span className="mono tiny muted">{failure.backoff_version}</span></div>
              <div><div className="tiny muted">{t("item.failure.workflow")}</div><span className="mono tiny">{failure.workflow_id}</span></div>
            </div>
            <div className="tiny muted">
              {t("item.failure.parameters", {
                initial: failure.backoff_params.x_seconds,
                perFailure,
                cap: durationWords(failure.backoff_params.max_backoff_seconds, t),
                max: failure.backoff_params.max_failures,
              })}
            </div>
            {failure.state === "backing_off" ? (
              <div className="error-note" style={{ color: !Number.isFinite(nextAt) || timeLeft > 0 ? "var(--theme-warning-text-emphasis)" : "var(--theme-success-text-emphasis)", borderColor: !Number.isFinite(nextAt) || timeLeft > 0 ? "var(--theme-warning-border-subtle)" : "var(--theme-success-border-subtle)", background: !Number.isFinite(nextAt) || timeLeft > 0 ? "var(--theme-warning-bg-subtle)" : "var(--theme-success-bg-subtle)" }}>
                {Number.isFinite(nextAt) && timeLeft > 0
                  ? t("item.failure.backingOff", { remaining: durationWords(Math.ceil(timeLeft / 1000), t), time: clock(nextAt) })
                  : Number.isFinite(nextAt)
                    ? t("item.failure.backoffPassed")
                    : t("item.failure.backoffUnknown")}
              </div>
            ) : failure.state === "exhausted" ? (
              <div className="error-note" style={{ color: "var(--danger)" }}>
                {t("item.failure.exhausted", { since: failure.exhausted_at ? t("item.failure.exhaustedSince", { when: formatDateTime(failure.exhausted_at) }) : "" })}
              </div>
            ) : <div className="tiny muted">{t("item.failure.noBackoff")}</div>}
            <div style={{ borderLeft: "2px solid var(--border-strong)", paddingLeft: 10 }}>
              <div className="tiny muted" style={{ marginBottom: 3 }}>{t("item.failure.lastError")}{failure.last_failure_kind ? t("item.failure.lastErrorKind", { kind: failure.last_failure_kind }) : ""}</div>
              <div className="tiny" style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{failure.last_error_md || t("item.failure.noError")}</div>
            </div>
            {props.canWrite ? (
              <div className="stack" style={{ justifyContent: "space-between", flexWrap: "wrap" }}>
                <span className="tiny muted">{t("item.failure.retryHint")}</span>
                <button type="button" className="btn btn--sm btn--primary" disabled={saving} onClick={() => void retryNow(failure)}>{saving ? t("item.failure.resetting") : t("item.failure.retryNow")}</button>
              </div>
            ) : <p className="tiny muted" style={{ margin: 0 }}>{t("item.failure.readOnly")}</p>}
          </div>
        ) : (
          <p className="tiny muted" style={{ margin: 0 }}>
            {absent?.code ? <><span className="mono">{absent.code}</span> — </> : null}{absent?.reason ?? t("item.failure.unavailable")}
          </p>
        )}
      </div>
    </section>
  );
}

function clock(time: number): string {
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(time);
}

function durationWords(seconds: number, t: T): string {
  const value = Math.max(0, Math.floor(seconds));
  if (value < 60) return t("item.duration.seconds", { count: value });
  const minutes = Math.ceil(value / 60);
  if (minutes < 60) return t("item.duration.minutes", { count: minutes });
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest ? t("item.duration.hoursRest", { count: hours, rest }) : t("item.duration.hours", { count: hours });
}

/** The message for an API error: the screen's own wording for a code it knows,
 *  the shared wording for a code the product knows, the server's own message
 *  for every other code. */
function apiIssue(t: T, error: unknown, known: Record<string, TranslationKey> = {}): string {
  if (error instanceof ApiError) {
    const key = known[error.code];
    if (key) return t(key);
    return apiErrorMessage(error);
  }
  if (error instanceof Error) return error.message;
  return t("error.title");
}

/* --- editable-draft plumbing -------------------------------------------------- */

interface EditableDraft {
  title: string;
  kind: string;
  description_md: string;
  due_date: string;
  labels: string[];
  jira_issue_key: string;
  external_url: string;
}

function draftFrom(it: Item): EditableDraft {
  return {
    title: it.title,
    kind: it.kind,
    description_md: it.description_md ?? "",
    due_date: inputDate(it.due_date),
    labels: [...it.labels],
    jira_issue_key: it.jira_issue_key ?? "",
    external_url: it.external_url ?? "",
  };
}

/* --- breadcrumb & header ------------------------------------------------------ */

function Breadcrumb(props: { itemId: string; number: number }) {
  const t = useT();
  return (
    <p className="tiny muted" style={{ margin: "0 0 6px" }}>
      <Link href="/board" className="rowlink">
        {t("board.title")}
      </Link>{" "}
      / {t("item.breadcrumb.item", { number: props.number })}
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
  const t = useT();
  const [pauseOpen, setPauseOpen] = useState(false);
  const [reason, setReason] = useState("");
  const kindLabel = it.kind || t("item.kindFallback");
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
                aria-label={it.pinned ? t("item.unpinAria") : t("item.pinAria")}
                aria-pressed={Boolean(it.pinned)}
                onClick={props.onPin}
              >
                <IconPin width={12} height={12} /> {it.pinned ? t("item.unpin") : t("item.pin")}
              </button>
              {it.paused ? (
                <button type="button" className="btn btn--sm" onClick={props.onUnpause}>
                  {t("item.unpause")}
                </button>
              ) : (
                <button
                  type="button"
                  className="btn btn--sm"
                  onClick={() => setPauseOpen(true)}
                >
                  <IconPause width={12} height={12} /> {t("item.pause")}
                </button>
              )}
              <button
                type="button"
                className="btn btn--sm btn--ghost btn--danger"
                aria-label={t("item.deleteAria")}
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
              background: "var(--theme-warning-bg-subtle)",
              border: "1px solid var(--theme-warning-border-subtle)",
              color: "var(--theme-warning-text-emphasis)",
            }}
          >
            {t("item.paused")}
            {it.paused_at ? ` ${t("item.pausedSince", { when: formatDateTime(it.paused_at) })}` : ""}
            {it.paused_by ? ` ${t("item.pausedBy", { name: firstName(props.memberName.get(it.paused_by) ?? "") || it.paused_by })}` : ""}
            {it.pause_reason ? ` ${t("item.pauseReason", { reason: it.pause_reason })}` : ""}
            {t("item.pausedExplains")}
          </div>
        ) : null}
        {it.pinned ? (
          <div className="tiny muted" style={{ marginTop: 6 }}>
            {t("item.pinnedNote")}
            {it.pinned_by
              ? ` ${t("item.pinnedBy", { name: firstName(props.memberName.get(it.pinned_by) ?? "") || it.pinned_by })}`
              : ""}
            {it.pinned_at ? t("item.pinnedAt", { when: formatDateTime(it.pinned_at) }) : ""}.
          </div>
        ) : null}

        <div
          className="stack"
          style={{ marginTop: 12, gap: 10, flexWrap: "wrap", alignItems: "center" }}
        >
          {props.canWrite ? (
            <StatusSelect
              value={it.status}
              ariaLabel={t("field.status")}
              onChange={props.onStatus}
            />
          ) : (
            <span className={`status-pill status-pill--${it.status}`}>{STATUS_LABELS[it.status]}</span>
          )}
          {props.canWrite ? (
            <PrioritySelect value={it.priority} onChange={props.onPriority} />
          ) : (
            <span className="tiny">
              {it.priority ? PRIORITY_LABELS[it.priority] : t("field.noPriority")}
              {it.priority_source === "inherited" ? (
                <span className="muted"> {t("item.inherited")}</span>
              ) : null}
            </span>
          )}
          {props.canWrite ? (
            <select
              className="input input--inline"
              value={it.assignee_user_id ?? ""}
              aria-label={t("field.assignee")}
              onChange={(e) => props.onAssignee(e.target.value)}
            >
              <option value="">{t("copy.unassigned")}</option>
              {props.members.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.name}
                </option>
              ))}
            </select>
          ) : it.assignee_user_id ? (
            <span className="tiny">
              {t("item.assignedTo", { name: props.memberName.get(it.assignee_user_id) ?? it.assignee_user_id })}
            </span>
          ) : null}
          {it.due_date ? (
            <span className="tiny muted">{t("item.due", { when: it.due_date.slice(0, 10) })}</span>
          ) : null}
        </div>
      </div>

      {pauseOpen ? (
        <Dialog
          title={t("item.pauseDialog.title", { number: it.number })}
          description={t("item.pauseDialog.description")}
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
              <label htmlFor="i-reason">{t("item.pauseDialog.reason")}</label>
              <input
                id="i-reason"
                className="input"
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder={t("item.pauseDialog.placeholder")}
              />
            </div>
            <div className="dialog__row">
              <button type="button" className="btn" onClick={() => setPauseOpen(false)}>
                {t("field.cancel")}
              </button>
              <button type="submit" className="btn btn--primary">
                {t("item.pause")}
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
  kinds: string[];
  kindsLoading: boolean;
  kindsError: boolean;
  labelOptions: string[];
  labelsLoading: boolean;
  labelsError: boolean;
  onSave: (d: EditableDraft) => void;
}) {
  const [d, setD] = useState<EditableDraft>(props.draft);
  const t = useT();
  useEffect(() => {
    setD(props.draft);
  }, [props.draft]);
  const set = <K extends keyof EditableDraft>(k: K, v: EditableDraft[K]) =>
    setD((x) => ({ ...x, [k]: v }));

  const dirty = JSON.stringify(d) !== JSON.stringify(props.draft);

  return (
    <div className="section">
      <div className="section__head">
        <h2>{t("item.details.title")}</h2>
      </div>
      <div className="section__body" style={{ display: "grid", gap: 12 }}>
        {props.canWrite ? (
          <>
            <div className="field">
              <label htmlFor="i-title">{t("field.title")}</label>
              <input
                id="i-title"
                className="input"
                value={d.title}
                onChange={(e) => set("title", e.target.value)}
              />
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "160px 1fr", gap: 12 }}>
              <div className="field">
                <label>{t("field.kind")}</label>
                <KindCombobox
                  value={d.kind}
                  options={props.kinds}
                  onChange={(value) => set("kind", value)}
                />
                <span className="tiny muted">
                  {props.kindsError ? t("newItem.kindError") : props.kindsLoading ? t("newItem.kindLoading") : t("newItem.kindHint")}
                </span>
              </div>
              <div className="field">
                <label htmlFor="i-due">{t("item.dueDate")}</label>
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
              <label htmlFor="i-desc">{t("item.description")}</label>
              <textarea
                id="i-desc"
                className="input"
                rows={6}
                value={d.description_md}
                onChange={(e) => set("description_md", e.target.value)}
              />
            </div>
            <div className="field">
              <label>{t("field.labels")}</label>
              <LabelInput
                value={d.labels}
                options={props.labelOptions}
                onChange={(value) => set("labels", value)}
              />
              <span className="tiny muted">
                {props.labelsError ? t("newItem.labelsError") : props.labelsLoading ? t("newItem.labelsLoading") : t("newItem.labelsHint")}
              </span>
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
              <div className="field">
                <label htmlFor="i-jira">{t("item.jiraKey")}</label>
                <input
                  id="i-jira"
                  className="input"
                  value={d.jira_issue_key}
                  onChange={(e) => set("jira_issue_key", e.target.value)}
                />
              </div>
              <div className="field">
                <label htmlFor="i-url">{t("item.externalUrl")}</label>
                <input
                  id="i-url"
                  className="input"
                  value={d.external_url}
                  onChange={(e) => set("external_url", e.target.value)}
                />
              </div>
            </div>
            <div className="stack" style={{ justifyContent: "flex-end" }}>
              {!dirty ? <span className="tiny muted">{t("item.noChanges")}</span> : null}
              <button
                type="button"
                className="btn"
                disabled={!dirty}
                onClick={() => setD(props.draft)}
              >
                {t("item.reset")}
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
                {t("item.saveChanges")}
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
  const t = useT();
  const md = item.description_md;
  return (
    <div style={{ display: "grid", gap: 10 }}>
      <div className="tiny muted" style={{ whiteSpace: "pre-wrap" }}>
        {md || t("item.noDescription")}
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

function ItemFamily(props: {
  parent: ItemFamilyRef | null;
  children: ItemChildRef[];
}) {
  const t = useT();
  return (
    <section className="section" aria-labelledby="family-heading">
      <div className="section__head">
        <h2 id="family-heading">{t("item.subItems")} <span className="tiny muted">({props.children.length})</span></h2>
      </div>
      <div className="section__body" style={{ display: "grid", gap: 12 }}>
        {props.parent ? (
          <p className="tiny" style={{ margin: 0 }}>
            <span className="muted">{t("item.partOf")}</span>
            <Link href={`/items/${props.parent.id}`} className="rowlink">
              #{props.parent.number} · {props.parent.title}
            </Link>
          </p>
        ) : null}
        {props.children.length === 0 ? (
          <p className="tiny muted" style={{ margin: 0 }}>{t("item.noSubItems")}</p>
        ) : (
          <ul className="family-list">
            {props.children.map((child) => (
              <li key={child.id}>
                <Link href={`/items/${child.id}`} className="family-list__link">
                  <span className="mono muted">#{child.number}</span>
                  <span className="family-list__title">{child.title}</span>
                  <span className="chip chip--kind">{child.kind}</span>
                  <span className={`status-pill status-pill--${child.status}`}>{STATUS_LABELS[child.status]}</span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

function GitHubLinksPanel(props: {
  links: GitHubItemLink[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
}) {
  const t = useT();
  return (
    <section className="section" aria-labelledby="github-links-heading">
      <div className="section__head">
        <h2 id="github-links-heading">{t("item.github.title")}</h2>
        {!props.loading && !props.error ? <span className="tiny muted">{props.links.length}</span> : null}
      </div>
      <div className="section__body">
        {props.error ? (
          <ErrorBlock title={t("item.github.loadFailed")} message={props.error} retry={props.onRetry} />
        ) : props.loading ? (
          <Skeleton h={52} />
        ) : props.links.length === 0 ? (
          <p className="tiny muted" style={{ margin: 0 }}>{t("item.github.empty")}</p>
        ) : (
          <ul className="github-link-list">
            {props.links.map((link) => (
              <li key={`${link.kind}:${link.repo}:${link.number}`}>
                <div className="github-link-list__main">
                  <div className="stack" style={{ flexWrap: "wrap", gap: 6 }}>
                    <span className={`badge ${link.kind === "issue" ? "badge--soft-info" : "badge--soft-secondary"}`}>
                      {link.kind === "issue" ? t("item.github.issue") : t("item.github.pullRequest")}
                    </span>
                    <span className="mono tiny muted">{link.repo} #{link.number}</span>
                    <span className={`badge ${link.state.toLowerCase() === "open" ? "badge--soft-success" : "badge--soft-secondary"}`}>
                      {githubStateLabel(t, link.state)}
                    </span>
                  </div>
                  <a href={link.url} target="_blank" rel="noreferrer noopener" className="github-link-list__title">
                    {link.title}
                    <span className="visually-hidden"> {t("item.github.opensNewTab")}</span>
                  </a>
                  {link.kind === "pr" && link.head_branch ? (
                    <span className="tiny muted">{t("item.github.headBranch")} <code>{link.head_branch}</code></span>
                  ) : null}
                </div>
                <a href={link.url} target="_blank" rel="noreferrer noopener" className="btn btn--sm">
                  {t("item.github.open")} <span aria-hidden>↗</span>
                </a>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

/** The state of a GitHub link, in the language on screen. */
function githubStateLabel(t: T, state: string): string {
  const key = `item.github.state.${state.toLowerCase()}` as TranslationKey;
  const known: TranslationKey[] = [
    "item.github.state.open",
    "item.github.state.closed",
    "item.github.state.merged",
    "item.github.state.draft",
  ];
  return known.includes(key) ? t(key) : state;
}

function LinksOf({ item }: { item: Item }) {
  const t = useT();
  const links: { href: string; label: string }[] = [];
  if (item.external_url) links.push({ href: item.external_url, label: t("item.externalLink") });
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
  const t = useT();
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
      push("info", t("item.dependencies.added"));
      setPick("");
      setAdding(false);
      await props.onReload();
    } catch (err) {
      // The 409 dependency_cycle answer must reach the user verbatim.
      push("error", apiIssue(t, err, { dependency_cycle: "item.dependencies.cycle" }));
    }
  }

  async function removeEdge(depId: string) {
    try {
      await api.removeDependency(props.item.id, depId);
      await props.onReload();
    } catch (err) {
      push("error", apiIssue(t, err));
    }
  }

  return (
    <div className="section">
      <div className="section__head">
        <h2>{t("item.dependencies.title")}</h2>
        <span className="tiny muted" style={{ marginLeft: "auto" }}>
          {t("item.dependencies.hint")}
        </span>
      </div>
      <div className="section__body" style={{ display: "grid", gap: 10 }}>
        <ListEdges
          label={t("item.dependencies.blockedBy")}
          empty={t("item.dependencies.noBlockers")}
          edges={props.blockers}
          canWrite={props.canWrite}
          onRemove={removeEdge}
          push={push}
        />
        <ListEdges
          label={t("item.dependencies.blocks")}
          empty={t("item.dependencies.nothingWaits")}
          edges={props.dependents}
          canWrite={props.canWrite}
          onRemove={removeEdge}
          push={push}
        />
        {props.canWrite ? (
          adding ? (
            <div className="stack" style={{ alignItems: "flex-end", flexWrap: "wrap" }}>
              <div className="field" style={{ flex: "1 1 280px" }}>
                <label htmlFor="i-blk">{t("item.dependencies.pick")}</label>
                <select
                  id="i-blk"
                  className="input"
                  value={pick}
                  onChange={(e) => setPick(e.target.value)}
                >
                  <option value="">{t("copy.choose")}</option>
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
                {t("item.dependencies.add")}
              </button>
              <button
                type="button"
                className="btn btn--ghost"
                onClick={() => {
                  setAdding(false);
                  setPick("");
                }}
              >
                {t("field.cancel")}
              </button>
            </div>
          ) : (
            <div>
              <button type="button" className="btn btn--sm" onClick={() => setAdding(true)}>
                {t("item.dependencies.addBlocker")}
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
  const t = useT();
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
                  aria-label={t("item.dependencies.removeAria", { title: edge.other.title })}
                  onClick={() => void props.onRemove(edge.id)}
                >
                  {t("item.dependencies.remove")}
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
  const t = useT();
  return (
    <div className="section">
      <div className="section__head">
        <h2>{t("item.score.title")}</h2>
        {props.score ? (
          <span className="tiny muted" style={{ marginLeft: "auto" }}>
            {t("item.score.weights", { version: props.score.weights_version })}
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
                  color: "var(--theme-warning-text-emphasis)",
                  background: "var(--theme-warning-bg-subtle)",
                  border: "1px solid var(--theme-warning-border-subtle)",
                  borderRadius: 6,
                  padding: "7px 10px",
                }}
              >
                {explainIneligible(t, props.score.ineligible)}
              </p>
            ) : null}
            <div style={{ marginBottom: 8 }}>
              <span className="metric" style={{ fontSize: 20 }}>
                {props.score.base_score}
              </span>
              <span className="tiny muted" style={{ marginLeft: 8 }}>
                {t("item.score.base")}
              </span>
            </div>
            <FactorTable
              factors={props.score.factors}
              baseScore={props.score.base_score}
            />
            <p className="tiny muted" style={{ marginTop: 10 }}>
              {t("item.score.explain")}
            </p>
          </>
        ) : (
          <EmptyState
            title={t("item.score.empty.title")}
            body={t("item.score.empty.body")}
          />
        )}
      </div>
    </div>
  );
}

function explainIneligible(t: T, reason: string): string {
  switch (reason) {
    case "closed":
      return t("item.score.ineligible.closed");
    case "paused":
      return t("item.score.ineligible.paused");
    case "blocked":
      return t("item.score.ineligible.blocked");
    case "epic":
      return t("item.score.ineligible.epic");
    default:
      return t("item.score.ineligible.other", { reason });
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
  const t = useT();
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
        title={t("item.activity.empty.title")}
        body={t("item.activity.empty.body")}
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
              {actorName(e, props.memberName, t)} {activitySentence(e.action, e.before, e.after)}
            </div>
            <AfterDiff entry={e} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function actorName(e: ActivityEntry, memberName: Map<string, string>, t: T): string {
  if (e.actor_kind === "system") return t("item.activity.actor.system");
  if (e.actor_kind === "agent") return t("item.activity.actor.agent");
  const name = e.actor_user_id
    ? firstName(memberName.get(e.actor_user_id) ?? "")
    : "";
  return name ? <strong>{name}</strong> as unknown as string : t("item.activity.actor.someone");
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
