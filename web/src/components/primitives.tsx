"use client";

import { cloneElement, useEffect, useId, useRef, useState } from "react";
import type { Priority, Status } from "@/lib/types";
import { PRIORITY_LABELS, STATUS_LABELS } from "@/lib/format";
import { IconCheck, IconClose } from "./icons";

/* --- Buttons, fields ------------------------------------------------------ */

export function Field(props: {
  label: string;
  children: React.ReactNode;
  hint?: string;
}) {
  return (
    <div className="field">
      <label>{props.label}</label>
      {props.children}
      {props.hint ? <span className="tiny muted">{props.hint}</span> : null}
    </div>
  );
}

/* --- Status / priority selects ------------------------------------------- */

export function StatusSelect(props: {
  value: Status;
  onChange: (s: Status) => void;
  disabled?: boolean;
  ariaLabel?: string;
  className?: string;
}) {
  return (
    <select
      className={`input input--inline ${props.className ?? ""}`}
      value={props.value}
      disabled={props.disabled}
      aria-label={props.ariaLabel ?? "Status"}
      onChange={(e) => props.onChange(e.target.value as Status)}
    >
      {(
        Object.keys(STATUS_LABELS) as Status[]
      ).map((s) => (
        <option key={s} value={s}>
          {STATUS_LABELS[s]}
        </option>
      ))}
    </select>
  );
}

export function PrioritySelect(props: {
  value: Priority | null;
  onChange: (p: Priority | null) => void;
  disabled?: boolean;
  ariaLabel?: string;
}) {
  const options: (Priority | null)[] = [null, "priority:p2", "priority:p1", "priority:p0"];
  return (
    <select
      className="input input--inline"
      value={props.value ?? ""}
      disabled={props.disabled}
      aria-label={props.ariaLabel ?? "Priority"}
      onChange={(e) =>
        props.onChange((e.target.value || null) as Priority | null)
      }
    >
      {options.map((p) => (
        <option key={p ?? "none"} value={p ?? ""}>
          {p ? PRIORITY_LABELS[p] : "No priority"}
        </option>
      ))}
    </select>
  );
}

export function PriorityChip({ priority }: { priority: Priority | null }) {
  if (!priority) return null;
  const cls = priority === "priority:p0" ? "p0" : priority === "priority:p1" ? "p1" : "p2";
  return <span className={`chip chip--${cls}`}>{PRIORITY_LABELS[priority]}</span>;
}

/* --- Progress ------------------------------------------------------------- */

export function ProgressBar({ percent, width }: { percent: number; width?: number }) {
  return (
    <span className="progress" style={width ? { width } : undefined} aria-hidden>
      <span className="progress__fill" style={{ width: `${percent}%` }} />
    </span>
  );
}

/* --- Skeletons ------------------------------------------------------------ */

export function Skeleton({ h = 16, w }: { h?: number; w?: number | string }) {
  return <div className="skeleton" style={{ height: h, width: w ?? "100%" }} />;
}

export function CardSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div style={{ display: "grid", gap: 10 }}>
      {Array.from({ length: rows }).map((_, i) => (
        <div
          key={i}
          style={{ display: "grid", gap: 6 }}
        >
          <Skeleton h={13} w="70%" />
          <Skeleton h={11} w="40%" />
        </div>
      ))}
    </div>
  );
}

/* --- Empty / error states -------------------------------------------------- */

export function EmptyState(props: {
  title: string;
  body: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="empty panel" style={{ padding: "48px 20px" }}>
      <h3>{props.title}</h3>
      <p>{props.body}</p>
      {props.action ? <div style={{ marginTop: 8 }}>{props.action}</div> : null}
    </div>
  );
}

export function ErrorBlock(props: { title?: string; message: string; retry?: () => void }) {
  return (
    <div role="alert" className="error-note" style={{ display: "grid", gap: 8 }}>
      <div>
        <strong>{props.title ?? "Something went wrong."}</strong>{" "}
        {props.message}
      </div>
      {props.retry ? (
        <div>
          <button type="button" className="btn btn--sm" onClick={props.retry}>
            Try again
          </button>
        </div>
      ) : null}
    </div>
  );
}

/* --- Dialog ---------------------------------------------------------------- */

export function Dialog(props: {
  title: string;
  description?: string;
  onClose: () => void;
  children: React.ReactNode;
  width?: number;
}) {
  const titleId = useId();
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    ref.current?.showModal?.();
  }, []);

  return (
    <div
      role="presentation"
      className="scrim"
      onClick={(e) => {
        if (e.target === e.currentTarget) props.onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="dialog"
        style={props.width ? { maxWidth: props.width } : undefined}
      >
        <div className="spread" style={{ marginBottom: 4 }}>
          <h2 id={titleId}>{props.title}</h2>
          <button
            type="button"
            className="btn btn--ghost btn--sm"
            aria-label="Close dialog"
            onClick={props.onClose}
            style={{ marginRight: -10 }}
          >
            <IconClose />
          </button>
        </div>
        {props.description ? <p className="dialog__sub">{props.description}</p> : null}
        {props.children}
      </div>
    </div>
  );
}

/* --- Dropdown menu (keyboard operable) ------------------------------------- */

export function Menu(props: {
  /** The interactive element that opens the menu; it becomes the trigger. */
  trigger: React.ReactElement<React.ButtonHTMLAttributes<HTMLButtonElement>>;
  align?: "left" | "right";
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    const onClick = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("mousedown", onClick);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("mousedown", onClick);
    };
  }, [open]);

  // Clone the caller's button instead of wrapping it: a <button> inside a
  // <button> is invalid HTML and React reports a hydration mismatch for it.
  const trigger = cloneElement(props.trigger, {
    "aria-haspopup": "menu",
    "aria-expanded": open,
    onClick: (e: React.MouseEvent<HTMLButtonElement>) => {
      props.trigger.props.onClick?.(e);
      setOpen((o) => !o);
    },
  });

  return (
    <div className="menu-anchor" ref={ref} onKeyDown={(e) => e.stopPropagation()}>
      {trigger}
      {open ? (
        <div
          role="menu"
          className={`menu ${props.align === "left" ? "menu--left" : ""}`}
          onClick={() => setOpen(false)}
        >
          {props.children}
        </div>
      ) : null}
    </div>
  );
}

export function MenuItem(props: {
  onSelect?: () => void;
  disabled?: boolean;
  danger?: boolean;
  children: React.ReactNode;
  trailing?: React.ReactNode;
}) {
  return (
    <button
      type="button"
      role="menuitem"
      className="menu__item"
      aria-disabled={props.disabled || undefined}
      style={props.danger ? { color: "var(--danger)" } : undefined}
      onClick={props.disabled ? undefined : props.onSelect}
    >
      {props.children}
      <span style={{ marginLeft: "auto", flex: "none" }}>{props.trailing}</span>
    </button>
  );
}

export function MenuItemCheck({ on = false }: { on?: boolean }) {
  return <span className="menu__check">{on ? <IconCheck width={12} height={12} /> : null}</span>;
}

/* --- Copy button ------------------------------------------------------------ */

export function CopyButton(props: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="btn btn--sm"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(props.text);
        } catch {
          // Clipboard may be blocked; selection fallback covers most cases.
        }
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1600);
      }}
    >
      {copied ? "Copied" : (props.label ?? "Copy")}
    </button>
  );
}

/* --- Toasts ----------------------------------------------------------------- */

export function Toasts(props: {
  toasts: { id: number; kind: "error" | "info"; text: string }[];
}) {
  return (
    <div className="toasts" role="status" aria-live="polite">
      {props.toasts.map((t) => (
        <div key={t.id} className={`toast ${t.kind === "error" ? "toast--error" : ""}`}>
          {t.text}
        </div>
      ))}
    </div>
  );
}

export function apiErrorMessage(err: unknown): string {
  if (err instanceof Error && "code" in err) {
    return err.message || "Something went wrong.";
  }
  if (err instanceof Error) return err.message;
  return "Something went wrong.";
}
