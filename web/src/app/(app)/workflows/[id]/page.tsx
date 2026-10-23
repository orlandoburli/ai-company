"use client";

import { useParams } from "next/navigation";
import { WorkflowEditor } from "@/components/workflows-ui";

export default function Page() {
  const { id } = useParams<{ id: string }>();
  return <WorkflowEditor templateId={id} />;
}
