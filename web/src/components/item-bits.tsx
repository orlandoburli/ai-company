"use client";

import type { Item } from "@/lib/types";
import {
  IconBlocked,
  IconEpic,
  IconPause,
  IconPin,
} from "./icons";

/** Row of small state indicators used on cards, table cells and detail headers. */
export function ItemIndicators({
  item,
  size = 12,
}: {
  item: Pick<Item, "blocked" | "is_epic" | "paused" | "pinned">;
  size?: number;
}) {
  return (
    <span className="board-card__indicators">
      {item.pinned ? (
        <span className="icon-ind icon-ind--pin" title="Pinned to rank 1">
          <IconPin width={size} height={size} />
        </span>
      ) : null}
      {item.paused ? (
        <span className="icon-ind icon-ind--pause" title="Paused">
          <IconPause width={size} height={size} />
        </span>
      ) : null}
      {item.blocked ? (
        <span className="icon-ind icon-ind--block" title="Has an open blocker">
          <IconBlocked width={size} height={size} />
        </span>
      ) : null}
      {item.is_epic ? (
        <span className="icon-ind icon-ind--epic" title="Epic: has open sub-items">
          <IconEpic width={size} height={size} />
        </span>
      ) : null}
    </span>
  );
}

export function PausedNote({ item }: { item: Item }) {
  if (!item.paused_at) return null;
  return (
    <div className="tiny muted" style={{ marginTop: 4 }}>
      Paused{item.paused_by ? ` by @${item.paused_by}` : ""}
      {item.pause_reason ? ` — ${item.pause_reason}` : ""}
    </div>
  );
}
