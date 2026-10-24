"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import type { ReactNode, KeyboardEvent } from "react";
import { Dialog } from "@/components/primitives";

export type ConfirmOptions = {
  title: string;
  message?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  destructive?: boolean;
};

type ConfirmRequest = {
  id: number;
  options: ConfirmOptions;
  opener: HTMLElement | null;
  resolve: (answer: boolean) => void;
};

type ConfirmFunction = (options: ConfirmOptions) => Promise<boolean>;

const ConfirmContext = createContext<ConfirmFunction | null>(null);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [active, setActive] = useState<ConfirmRequest | null>(null);
  const activeRef = useRef<ConfirmRequest | null>(null);
  const queueRef = useRef<ConfirmRequest[]>([]);
  const nextIdRef = useRef(0);
  const confirm = useCallback<ConfirmFunction>((options) => {
    return new Promise<boolean>((resolve) => {
      const request = {
        id: ++nextIdRef.current,
        options,
        opener: document.activeElement instanceof HTMLElement ? document.activeElement : null,
        resolve,
      };
      if (activeRef.current) {
        queueRef.current.push(request);
      } else {
        activeRef.current = request;
        setActive(request);
      }
    });
  }, []);

  const settle = useCallback((request: ConfirmRequest, answer: boolean) => {
    if (activeRef.current !== request) return;

    request.resolve(answer);
    const next = queueRef.current.shift() ?? null;
    activeRef.current = next;
    setActive(next);

    if (!next) {
      window.requestAnimationFrame(() => {
        if (request.opener?.isConnected) request.opener.focus();
      });
    }
  }, []);

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {active ? (
        <ConfirmDialog
          key={active.id}
          request={active}
          onResolve={(answer) => settle(active, answer)}
        />
      ) : null}
    </ConfirmContext.Provider>
  );
}

export function useConfirm(): ConfirmFunction {
  const confirm = useContext(ConfirmContext);
  if (!confirm) throw new Error("useConfirm must be used within ConfirmProvider.");
  return confirm;
}

function ConfirmDialog({
  request,
  onResolve,
}: {
  request: ConfirmRequest;
  onResolve: (answer: boolean) => void;
}) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const settledRef = useRef(false);
  const { options } = request;

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      (options.destructive ? cancelRef.current : confirmRef.current)?.focus();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [options.destructive]);

  function resolve(answer: boolean) {
    if (settledRef.current) return;
    settledRef.current = true;
    onResolve(answer);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      resolve(false);
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      event.stopPropagation();
      resolve(true);
      return;
    }
    if (event.key !== "Tab") return;

    const panel = event.currentTarget.querySelector<HTMLElement>(".dialog");
    const focusable = panel?.querySelectorAll<HTMLElement>(
      'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
    );
    if (!focusable?.length) {
      event.preventDefault();
      return;
    }
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && (document.activeElement === first || !panel?.contains(document.activeElement))) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (document.activeElement === last || !panel?.contains(document.activeElement))) {
      event.preventDefault();
      first.focus();
    }
  }

  return (
    <div className="confirm-dialog-motion" onKeyDown={handleKeyDown}>
      <Dialog
        title={options.title}
        onClose={() => resolve(false)}
        width={440}
        showCloseButton={false}
      >
        {options.message ? <p className="confirm-dialog__message">{options.message}</p> : null}
        <div className="dialog__row confirm-dialog__actions">
          <button ref={cancelRef} type="button" className="btn btn--ghost" onClick={() => resolve(false)}>
            {options.cancelLabel ?? "Cancel"}
          </button>
          <button
            ref={confirmRef}
            type="button"
            className={`btn ${options.destructive ? "btn--confirm-danger" : "btn--primary"}`}
            onClick={() => resolve(true)}
          >
            {options.confirmLabel ?? "Confirm"}
          </button>
        </div>
      </Dialog>
    </div>
  );
}
