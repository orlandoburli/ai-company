"use client";

import Link from "next/link";
import { api } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { ErrorBlock, Skeleton } from "@/components/primitives";
import { ChatPanel } from "@/components/chat";
import { useT } from "@/lib/i18n";

/**
 * Full-page chat for one thread. Reaches back to the owning item for a
 * breadcrumb; the chat itself is the shared ChatPanel.
 */
export function ChatRouteClient({ threadId }: { threadId: string }) {
  const t = useT();
  const { canWrite } = useSession();
  const threadQ = useAsync(
    () => api.getThread(threadId).then((r) => r.thread),
    [threadId],
  );
  const itemId = threadQ.data?.item_id;
  const itemQ = useAsync(
    () =>
      itemId
        ? api.getItem(itemId).then((r) => r.item)
        : Promise.resolve(null),
    [itemId],
  );

  const item = itemQ.data ?? null;

  return (
    <div style={{ maxWidth: 980, margin: "0 auto" }}>
      <p className="tiny muted" style={{ margin: "0 0 6px" }}>
        <Link href="/board" className="rowlink">
          {t("board.title")}
        </Link>
        {item ? (
          <>
            {" / "}
            <Link href={`/items/${item.id}`} className="rowlink">
              {t("chat.breadcrumb.item", { number: item.number })}
            </Link>
          </>
        ) : threadQ.data ? (
          ` / ${t("chat.breadcrumb.unavailable")}`
        ) : null}
      </p>
      <div className="pagehead">
        <h1>{t("chat.title")}</h1>
        {item ? (
          <span className="pagehead__sub">
            #{item.number} · {item.title}
          </span>
        ) : threadQ.loading ? (
          <Skeleton h={16} w={220} />
        ) : threadQ.error ? (
          <span />
        ) : null}
      </div>

      {threadQ.error ? (
        <ErrorBlock
          title={t("chat.loadFailed")}
          message={threadQ.error.message}
          retry={() => void threadQ.reload()}
        />
      ) : !threadQ.data ? (
        <Skeleton h={420} />
      ) : (
        <ChatPanel
          threadId={threadQ.data.id}
          canWrite={canWrite}
          full
        />
      )}
    </div>
  );
}
