"use client";

import type { Item } from "@/lib/types";
import { useT } from "@/lib/i18n";
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
  const t = useT();
  return (
    <span className="board-card__indicators">
      {item.pinned ? (
        <span className="icon-ind icon-ind--pin" title={t("item.pinned")}>
          <IconPin width={size} height={size} />
        </span>
      ) : null}
      {item.paused ? (
        <span className="icon-ind icon-ind--pause" title={t("item.paused")}>
          <IconPause width={size} height={size} />
        </span>
      ) : null}
      {item.blocked ? (
        <span className="icon-ind icon-ind--block" title={t("item.blocked")}>
          <IconBlocked width={size} height={size} />
        </span>
      ) : null}
      {item.is_epic ? (
        <span className="icon-ind icon-ind--epic" title={t("item.epic")}>
          <IconEpic width={size} height={size} />
        </span>
      ) : null}
    </span>
  );
}

export function PausedNote({ item }: { item: Item }) {
  const t = useT();
  if (!item.paused_at) return null;
  return (
    <div className="tiny muted" style={{ marginTop: 4 }}>
      {item.paused_by
        ? t("item.pausedBy", { name: `@${item.paused_by}` })
        : t("item.paused")}
      {item.pause_reason ? ` — ${item.pause_reason}` : ""}
    </div>
  );
}
