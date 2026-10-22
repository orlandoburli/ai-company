import type { Metadata } from "next";
import { ChatRouteClient } from "./client";

/** Title flows through the root template: "Chat — AI Company". */
export const metadata: Metadata = {
  title: "Chat",
};

export default async function ChatPage({
  params,
}: {
  params: Promise<{ threadId: string }>;
}) {
  const { threadId } = await params;
  return <ChatRouteClient threadId={threadId} />;
}
