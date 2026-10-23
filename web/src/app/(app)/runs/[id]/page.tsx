"use client";

import { useParams } from "next/navigation";
import { RunMonitor } from "@/components/run-ui";

export default function RunDetailPage() {
  const params = useParams<{ id: string }>();
  return <RunMonitor runId={params.id} />;
}
