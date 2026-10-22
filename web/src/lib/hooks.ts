"use client";

import { useCallback, useEffect, useRef, useState } from "react";

/**
 * A minimal data hook: load once, expose loading/error, and allow an awaited
 * manual reload without a query library. `awaiting reload()` resolves only
 * after fresh data has landed, which is what optimistic surfaces use to
 * clear their overlay at the right moment.
 */
export function useAsync<T>(
  fn: () => Promise<T>,
  deps: readonly unknown[],
): {
  data: T | null;
  error: Error | null;
  loading: boolean;
  reload: () => Promise<void>;
} {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(true);
  // The latest fn is mirrored into a ref inside an effect (never during
  // render) so every fetch uses the freshest closure.
  const fnRef = useRef(fn);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const d = await fnRef.current();
      setData(d);
      setError(null);
    } catch (e: unknown) {
      setError(e instanceof Error ? e : new Error(String(e)));
    } finally {
      setLoading(false);
    }
  }, []);

  // Sync the latest fn after every render; ordering puts this before the
  // fetch effect, whose deps re-trigger it on change anyway.
  useEffect(() => {
    fnRef.current = fn;
  });

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps]);

  const reload = useCallback(async () => {
    await load();
  }, [load]);

  return { data, error, loading, reload };
}

/** Transient messages at the bottom edge of the screen. */
export function useToasts(): {
  toasts: { id: number; kind: "error" | "info"; text: string }[];
  push: (kind: "error" | "info", text: string) => void;
} {
  const [toasts, setToasts] = useState<
    { id: number; kind: "error" | "info"; text: string }[]
  >([]);
  const idRef = useRef(0);
  const push = useCallback((kind: "error" | "info", text: string) => {
    const id = ++idRef.current;
    setToasts((t) => [...t.slice(-3), { id, kind, text }]);
    window.setTimeout(() => {
      setToasts((t) => t.filter((x) => x.id !== id));
    }, 6000);
  }, []);
  return { toasts, push };
}
