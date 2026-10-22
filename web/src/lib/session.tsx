"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import { useRouter } from "next/navigation";
import { api, ApiError } from "./api";
import type { MeResponse, Role } from "./types";

/**
 * Client-side session. The HttpOnly cookie is never read in JS; /api/auth/me
 * is the single source of truth for who is signed in.
 */

interface SessionValue {
  me: MeResponse | null;
  loading: boolean;
  /** Users who can write to work items (everyone except guest). */
  canWrite: boolean;
  refresh: () => Promise<MeResponse | null>;
  signOut: () => Promise<void>;
  switchTenant: (tenantId: string) => Promise<void>;
}

const SessionContext = createContext<SessionValue | null>(null);

export function SessionProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  const router = useRouter();
  const [me, setMe] = useState<MeResponse | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async (): Promise<MeResponse | null> => {
    try {
      const next = await api.me();
      setMe(next);
      return next;
    } catch (err) {
      setMe(null);
      if (!(err instanceof ApiError)) throw err;
      return null;
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    let alive = true;
    api
      .me()
      .then((next) => {
        if (alive) setMe(next);
      })
      .catch((err: unknown) => {
        if (alive) setMe(null);
        if (err instanceof ApiError) return;
        throw err;
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, []);

  const signOut = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setMe(null);
      router.push("/login");
    }
  }, [router]);

  const switchTenant = useCallback(
    async (tenantId: string) => {
      const next = await api.switchTenant(tenantId);
      setMe(next);
      router.refresh();
      // Tenant-scoped data changes wholesale; a full navigation reload is the
      // simplest correct behaviour.
      window.location.reload();
    },
    [router],
  );

  const canWrite = useMemo(() => {
    const role: Role | undefined = me?.tenant.role;
    return role !== undefined && role !== "guest";
  }, [me]);

  const value = useMemo(
    () => ({ me, loading, canWrite, refresh, signOut, switchTenant }),
    [me, loading, canWrite, refresh, signOut, switchTenant],
  );

  return (
    <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
  );
}

export function useSession(): SessionValue {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used inside SessionProvider");
  return ctx;
}
