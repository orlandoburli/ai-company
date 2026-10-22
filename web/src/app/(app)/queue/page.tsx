"use client";

import { Suspense, useCallback, useMemo, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { api } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import type { Item, QueueEntry } from "@/lib/types";
import { firstName, relTime, formatDateTime } from "@/lib/format";
import {
  Dialog,
  ErrorBlock,
  Skeleton,
  Toasts,
  apiErrorMessage,
} from "@/components/primitives";
import { FactorTable } from "@/components/factor-table";
import { IconPin } from "@/components/icons";

export default function QueuePage() {
  return (
    <Suspense
      fallback={
        <div style={{ display: "grid", gap: 12 }}>
          <Skeleton h={22} w={140} />
          <Skeleton h={300} />
        </div>
      }
    >
      <QueueView />
    </Suspense>
  );
}

function QueueView() {
  const params = useSearchParams();
  const pathname = usePathname();
  const router = useRouter();
  const projectFilter = params.get("project_id") ?? "";

  const { canWrite } = useSession();
  const { toasts, push } = useToasts();

  const queue = useAsync(
    () => api.queue(projectFilter),
    [projectFilter],
  );
  const members = useAsync(
    () => api.listMembers().then((r) => r.members),
    [],
  );
  const projects = useAsync(
    () => api.listProjects().then((r) => r.projects),
    [],
  );

  const memberName = useMemo(() => {
    const m = new Map<string, string>();
    for (const mem of members.data ?? []) m.set(mem.user_id, mem.name);
    return m;
  }, [members.data]);

  const [openFactors, setOpenFactors] = useState<Set<string>>(new Set());
  const [pauseTarget, setPauseTarget] = useState<Item | null>(null);
  const [pauseReason, setPauseReason] = useState("");

  const setParam = useCallback(
    (key: string, value: string) => {
      const next = new URLSearchParams(params.toString());
      if (value) next.set(key, value);
      else next.delete(key);
      router.replace(`${pathname}?${next.toString()}`, { scroll: false });
    },
    [pathname, params, router],
  );

  async function override(
    id: string,
    action: "pin" | "unpin" | "pause" | "unpause",
    reason?: string,
  ) {
    try {
      if (action === "pin") await api.pinItem(id);
      else if (action === "unpin") await api.unpinItem(id);
      else if (action === "pause") await api.pauseItem(id, reason);
      else await api.unpauseItem(id);
      await queue.reload();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  const entries = useMemo(() => queue.data?.queue ?? [], [queue.data]);
  const rankOf = useMemo(() => {
    const m = new Map<string, number>();
    let r = 0;
    for (const e of entries) {
      if (!(e.paused || e.item.paused_at)) {
        r += 1;
        m.set(e.item.id, r);
      }
    }
    return m;
  }, [entries]);

  return (
    <>
      <div className="pagehead">
        <h1>Queue</h1>
        <span className="pagehead__sub">
          Dispatch order — pinned first, then base score.
          {queue.data ? ` Weights ${queue.data.weights_version}.` : ""}
        </span>
        <span style={{ marginLeft: "auto" }} className="field">
          <label htmlFor="q-project" className="tiny muted" style={{ textTransform: "none" }}>
            Project
          </label>
          <select
            id="q-project"
            className="input input--inline"
            value={projectFilter}
            onChange={(e) => setParam("project_id", e.target.value)}
          >
            <option value="">All</option>
            {(projects.data ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </span>
      </div>

      {queue.error ? (
        <ErrorBlock
          message={queue.error.message}
          retry={() => void queue.reload()}
        />
      ) : queue.loading ? (
        <div style={{ display: "grid", gap: 8 }}>
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} h={48} />
          ))}
        </div>
      ) : (
        <div>
          {entries.length === 0 ? (
            <div className="empty panel" style={{ padding: "40px 20px" }}>
              <h3>Nothing is dispatchable</h3>
              <p>
                The queue covers open, unblocked, non-epic items that are not
                paused. Move some backlog items to “To do”, or check the
                project filter above.
              </p>
              <Link className="btn" href="/backlog">
                Open backlog
              </Link>
            </div>
          ) : (
            <>
              {(() => {
                const pausedList = entries.filter(
                  (e) => e.paused || e.item.paused_at,
                );
                return pausedList.length > 0 ? (
                  <p className="tiny muted" style={{ marginBottom: 6 }}>
                    {entries.length - pausedList.length} dispatchable ·{" "}
                    {pausedList.length} paused and listed below the order.
                  </p>
                ) : null;
              })()}
              <div>
                {entries.map((e) => {
                  const isPaused = e.paused || Boolean(e.item.paused_at);
                  return (
                    <QueueRow
                      key={e.item.id}
                      entry={e}
                      rank={rankOf.get(e.item.id) ?? null}
                      canWrite={canWrite}
                      muted={isPaused}
                      open={openFactors.has(e.item.id)}
                      onToggleFactors={() => {
                        setOpenFactors((s) => {
                          const next = new Set(s);
                          if (next.has(e.item.id)) next.delete(e.item.id);
                          else next.add(e.item.id);
                          return next;
                        });
                      }}
                      memberName={memberName}
                      onOverride={(action) => void override(e.item.id, action)}
                      onPause={() => {
                        setPauseTarget(e.item);
                        setPauseReason("");
                      }}
                      onUnpause={() => void override(e.item.id, "unpause")}
                    />
                  );
                })}
              </div>
            </>
          )}
        </div>
      )}

      {pauseTarget ? (
        <Dialog
          title={`Pause #${pauseTarget.number}`}
          description="A pause removes the item from dispatch. It is not a failure and never expires by itself — unpause to make it eligible again."
          onClose={() => setPauseTarget(null)}
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const target = pauseTarget;
              setPauseTarget(null);
              if (target) void override(target.id, "pause", pauseReason);
            }}
            style={{ display: "grid", gap: 12 }}
          >
            <div className="field">
              <label htmlFor="q-reason">Reason (optional)</label>
              <input
                id="q-reason"
                className="input"
                value={pauseReason}
                onChange={(e) => setPauseReason(e.target.value)}
                placeholder="Why this item waits"
              />
            </div>
            <div className="dialog__row">
              <button type="button" className="btn" onClick={() => setPauseTarget(null)}>
                Cancel
              </button>
              <button type="submit" className="btn btn--primary">
                Pause item
              </button>
            </div>
          </form>
        </Dialog>
      ) : null}

      <Toasts toasts={toasts} />
    </>
  );
}

/* --- row --------------------------------------------------------------------- */

function QueueRow(props: {
  entry: QueueEntry;
  rank: number | null;
  canWrite: boolean;
  open: boolean;
  muted: boolean;
  onToggleFactors: () => void;
  onPause: () => void;
  onUnpause: () => void;
  onOverride: (action: "pin" | "unpin") => void;
  memberName: Map<string, string>;
}) {
  const { item, score, pinned } = props.entry;
  const paused = props.muted || Boolean(item.paused_at);

  return (
    <div>
      {props.open ? (
        <div
          className="panel section__body"
          style={{ marginBottom: 6, borderTop: "none", borderRadius: "0 0 8px 8px" }}
        >
          <div className="tiny muted" style={{ marginBottom: 6 }}>
            Base score {score.base_score} · weights {score.weights_version} · this
            is deterministic: same inputs, same number.
          </div>
          <FactorTable factors={score.factors} baseScore={score.base_score} />
        </div>
      ) : null}
      <div
        className={[
          "queue-row",
          pinned ? "queue-row--pinned" : "",
          paused ? "queue-row--paused" : "",
        ].join(" ")}
      >
        <span className="queue-row__rank" title={pinned ? "Pinned to rank 1" : "Dispatch rank"}>
          {props.rank ?? "—"}
        </span>
        <div style={{ minWidth: 0 }}>
          <Link
            href={`/items/${item.id}`}
            className="rowlink"
            style={{ fontWeight: 600, display: "inline-flex", gap: 6, alignItems: "baseline", flexWrap: "wrap" }}
          >
            <span className="mono tiny muted">#{item.number}</span>
            <span>{item.title}</span>
            <span className="chip chip--kind">{item.kind}</span>
          </Link>
          <div className="tiny muted" style={{ marginTop: 3 }}>
            Score {score.base_score}
            {item.is_epic ? " · epic: has open sub-items" : ""}
            {item.blocked ? " · blocked by an open dependency" : ""}
            {paused
              ? ` · paused${item.paused_by ? ` by ${firstName(props.memberName.get(item.paused_by) ?? "")}` : ""}${
                  item.pause_reason ? ` — ${item.pause_reason}` : ""
                }`
              : ""}
            {pinned && item.pinned_by ? ` · pinned by ${firstName(props.memberName.get(item.pinned_by) ?? "") || "someone"}` : ""}
            {pinned && item.pinned_at ? ` ${relTime(item.pinned_at)}` : ""}
          </div>
          {paused && item.paused_at ? (
            <div className="tiny muted">
              Paused since {formatDateTime(item.paused_at)}.
            </div>
          ) : null}
        </div>
        <span className="mono" title="Base score">
          {score.base_score}
        </span>
        <span style={{ display: "flex", gap: 6, alignItems: "center" }}>
          <button
            type="button"
            className="btn btn--sm"
            aria-expanded={props.open}
            onClick={props.onToggleFactors}
          >
            Why?
          </button>
          {props.canWrite ? (
            <>
              <button
                type="button"
                className="btn btn--sm"
                onClick={() => props.onOverride(pinned ? "unpin" : "pin")}
                aria-label={pinned ? "Unpin" : "Pin to top"}
              >
                <IconPin width={12} height={12} />
                {pinned ? "Unpin" : "Pin"}
              </button>
              {paused ? (
                <button
                  type="button"
                  className="btn btn--sm"
                  onClick={props.onUnpause}
                >
                  Unpause
                </button>
              ) : (
                <button type="button" className="btn btn--sm" onClick={props.onPause}>
                  Pause
                </button>
              )}
            </>
          ) : null}
        </span>
      </div>
    </div>
  );
}
