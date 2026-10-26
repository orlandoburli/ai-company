"use client";

import { cloneElement, useEffect, useId, useRef, useState } from "react";
import type { Priority, Status } from "@/lib/types";
import { PRIORITY_LABELS, STATUS_LABELS } from "@/lib/format";
import { getCurrentLang, translate } from "@/lib/locales";
import { useT } from "@/lib/i18n";
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

/** An editable project-kind picker. Existing values remain valid even when the
 * project has not offered them; the suggestions are vocabulary, not a rule. */
export function KindCombobox(props: {
  value: string;
  options: string[];
  onChange: (value: string) => void;
  ariaLabel?: string;
  disabled?: boolean;
}) {
  const id = useId();
  const t = useT();
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const matches = props.options.filter((name) =>
    name.toLocaleLowerCase().includes(props.value.trim().toLocaleLowerCase()),
  );
  const addCustom = props.value.trim() && !props.options.some(
    (name) => name.toLocaleLowerCase() === props.value.trim().toLocaleLowerCase(),
  );
  const menuOptions = [...matches, ...(addCustom ? [props.value.trim()] : [])];

  return (
    <div className="kind-combobox">
      <input
        className="input"
        role="combobox"
        aria-label={props.ariaLabel ?? t("field.kind")}
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls={`${id}-options`}
        aria-activedescendant={open && activeIndex >= 0 ? `${id}-option-${activeIndex}` : undefined}
        value={props.value}
        disabled={props.disabled}
        onFocus={() => { setOpen(true); setActiveIndex(-1); }}
        onChange={(e) => { props.onChange(e.target.value); setOpen(true); setActiveIndex(-1); }}
        onKeyDown={(e) => {
          if (e.key === "Escape") setOpen(false);
          if (e.key === "ArrowDown" && menuOptions.length) {
            e.preventDefault(); setOpen(true); setActiveIndex((index) => Math.min(menuOptions.length - 1, index + 1));
          } else if (e.key === "ArrowUp" && menuOptions.length) {
            e.preventDefault(); setActiveIndex((index) => Math.max(0, index < 0 ? 0 : index - 1));
          } else if (e.key === "Enter" && open) {
            e.preventDefault();
            if (activeIndex >= 0 && menuOptions[activeIndex]) props.onChange(menuOptions[activeIndex]);
            setOpen(false);
          }
        }}
        onBlur={() => setOpen(false)}
      />
      {open && !props.disabled ? (
        <div className="picker-menu" id={`${id}-options`} role="listbox" aria-label={t("field.projectKinds")}>
          {matches.map((name, index) => (
            <button
              id={`${id}-option-${index}`}
              key={name}
              type="button"
              role="option"
              aria-selected={index === activeIndex || name === props.value}
              className="picker-option"
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => { props.onChange(name); setOpen(false); }}
            >{name}</button>
          ))}
          {addCustom ? (
            <button
              id={`${id}-option-${matches.length}`}
              type="button"
              role="option"
              aria-selected={activeIndex === matches.length}
              className="picker-option picker-option--custom"
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => { props.onChange(props.value.trim()); setOpen(false); }}
            >{t("field.useValue", { value: props.value.trim() })}</button>
          ) : null}
          {matches.length === 0 && !addCustom ? <span className="picker-empty">{t("field.noKinds")}</span> : null}
        </div>
      ) : null}
    </div>
  );
}

/** Chip editor with suggestions from labels already in use and free-value add. */
export function LabelInput(props: {
  value: string[];
  options: string[];
  onChange: (labels: string[]) => void;
  ariaLabel?: string;
  disabled?: boolean;
}) {
  const id = useId();
  const t = useT();
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const normalized = query.trim().toLocaleLowerCase();
  const matches = props.options.filter((label) =>
    label.toLocaleLowerCase().includes(normalized) && !props.value.includes(label),
  );
  const custom = query.trim() && !props.value.some(
    (label) => label.toLocaleLowerCase() === normalized,
  ) && !matches.some((label) => label.toLocaleLowerCase() === normalized);
  const menuOptions = [...matches, ...(custom ? [query.trim()] : [])];

  function add(label: string) {
    const next = label.trim();
    if (next && !props.value.some((item) => item.toLocaleLowerCase() === next.toLocaleLowerCase())) {
      props.onChange([...props.value, next]);
    }
    setQuery("");
    setOpen(false);
    setActiveIndex(-1);
  }

  return (
    <div className={`label-input${props.disabled ? " label-input--disabled" : ""}`}>
      {props.value.length ? (
        <div className="label-input__chips">
          {props.value.map((label) => (
            <span key={label} className="label-chip">
              {label}
              {!props.disabled ? (
                <button
                  type="button"
                  aria-label={t("field.removeLabel", { label })}
                  onClick={() => props.onChange(props.value.filter((item) => item !== label))}
                >×</button>
              ) : null}
            </span>
          ))}
        </div>
      ) : null}
      <div className="label-input__anchor">
        <input
          className="label-input__text"
          role="combobox"
          aria-label={props.ariaLabel ?? t("field.addLabel")}
          aria-autocomplete="list"
          aria-expanded={open}
          aria-controls={`${id}-options`}
          aria-activedescendant={open && activeIndex >= 0 ? `${id}-label-option-${activeIndex}` : undefined}
          placeholder={props.value.length ? t("field.addAnotherLabel") : t("field.typeLabel")}
          value={query}
          disabled={props.disabled}
          onFocus={() => { setOpen(true); setActiveIndex(-1); }}
          onChange={(e) => { setQuery(e.target.value); setOpen(true); setActiveIndex(-1); }}
          onBlur={() => setOpen(false)}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown" && menuOptions.length) {
              e.preventDefault(); setOpen(true); setActiveIndex((index) => Math.min(menuOptions.length - 1, index + 1));
            } else if (e.key === "ArrowUp" && menuOptions.length) {
              e.preventDefault(); setActiveIndex((index) => Math.max(0, index < 0 ? 0 : index - 1));
            } else if (e.key === "Enter" || e.key === ",") {
              e.preventDefault();
              if (e.key === "Enter" && open && activeIndex >= 0 && menuOptions[activeIndex]) add(menuOptions[activeIndex]);
              else add(query);
            } else if (e.key === "Escape") setOpen(false);
            else if (e.key === "Backspace" && !query && props.value.length) {
              props.onChange(props.value.slice(0, -1));
            }
          }}
        />
        {open && !props.disabled && (matches.length > 0 || custom) ? (
          <div className="picker-menu" id={`${id}-options`} role="listbox" aria-label={t("field.availableLabels")}>
            {matches.map((label, index) => (
              <button
                id={`${id}-label-option-${index}`}
                key={label}
                type="button"
                role="option"
                aria-selected={activeIndex === index}
                className="picker-option"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => add(label)}
              >{label}</button>
            ))}
            {custom ? (
              <button
                id={`${id}-label-option-${matches.length}`}
                type="button"
                role="option"
                aria-selected={activeIndex === matches.length}
                className="picker-option picker-option--custom"
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => add(query)}
              >{t("field.addValue", { value: query.trim() })}</button>
            ) : null}
          </div>
        ) : null}
      </div>
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
  const t = useT();
  return (
    <select
      className={`input input--inline ${props.className ?? ""}`}
      value={props.value}
      disabled={props.disabled}
      aria-label={props.ariaLabel ?? t("field.status")}
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
  const t = useT();
  const options: (Priority | null)[] = [null, "priority:p2", "priority:p1", "priority:p0"];
  return (
    <select
      className="input input--inline"
      value={props.value ?? ""}
      disabled={props.disabled}
      aria-label={props.ariaLabel ?? t("field.priority")}
      onChange={(e) =>
        props.onChange((e.target.value || null) as Priority | null)
      }
    >
      {options.map((p) => (
        <option key={p ?? "none"} value={p ?? ""}>
          {p ? PRIORITY_LABELS[p] : t("field.noPriority")}
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
  const t = useT();
  return (
    <div role="alert" className="error-note" style={{ display: "grid", gap: 8 }}>
      <div>
        <strong>{props.title ?? t("error.title")}</strong>{" "}
        {props.message}
      </div>
      {props.retry ? (
        <div>
          <button type="button" className="btn btn--sm" onClick={props.retry}>
            {t("error.retry")}
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
  showCloseButton?: boolean;
}) {
  const titleId = useId();
  const t = useT();
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
          {props.showCloseButton === false ? null : (
            <button
              type="button"
              className="btn btn--ghost btn--sm"
              aria-label={t("dialog.close")}
              onClick={props.onClose}
              style={{ marginRight: -10 }}
            >
              <IconClose />
            </button>
          )}
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
  const t = useT();
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
      {copied ? t("copy.copied") : (props.label ?? t("copy.copy"))}
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
    return err.message || translate(getCurrentLang(), "error.title");
  }
  if (err instanceof Error) return err.message;
  return translate(getCurrentLang(), "error.title");
}
