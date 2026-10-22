"use client";

import { useEffect, Suspense } from "react";
import { usePathname, useRouter } from "next/navigation";
import { useSession } from "@/lib/session";
import { AppShell } from "@/components/app-shell";

/**
 * Auth guard for every app page: /api/auth/me decides; the HttpOnly cookie is
 * never read from JS. Children render only once a session exists.
 */
export default function AppLayout({ children }: { children: React.ReactNode }) {
  const { me, loading } = useSession();
  const router = useRouter();
  const pathname = usePathname();

  useEffect(() => {
    if (!loading && !me) {
      router.replace(`/login?next=${encodeURIComponent(pathname)}`);
    }
  }, [loading, me, router, pathname]);

  if (loading || !me) {
    return (
      <div style={{ display: "grid", placeItems: "center", minHeight: "60dvh" }}>
        <div className="skeleton" style={{ width: 220, height: 22 }} />
      </div>
    );
  }

  return (
    <Suspense fallback={null}>
      <AppShell>{children}</AppShell>
    </Suspense>
  );
}
