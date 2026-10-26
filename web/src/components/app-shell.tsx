"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { api, type Notification } from "@/lib/api";
import { useSession } from "@/lib/session";
import { useI18n } from "@/lib/i18n";
import { LANGUAGES, type TranslationKey } from "@/lib/locales";
import type { Product, ProjectNode } from "@/lib/types";
import { Menu, MenuItem, ProgressBar } from "./primitives";
import {
  IconBuilding,
  IconChevron,
  IconLogout,
  IconMenu,
  IconUser,
} from "./icons";

const NAV: { href: string; labelKey: TranslationKey }[] = [
  { href: "/board", labelKey: "nav.board" },
  { href: "/backlog", labelKey: "nav.backlog" },
  { href: "/queue", labelKey: "nav.queue" },
  { href: "/runs", labelKey: "nav.runs" },
  { href: "/workflows", labelKey: "nav.workflows" },
  { href: "/checkpoints", labelKey: "nav.checkpoints" },
  { href: "/usage", labelKey: "nav.usage" },
  { href: "/settings", labelKey: "nav.settings" },
];

const NAV_GLYPHS: Record<string, string> = {
  "/board": "▦",
  "/backlog": "▤",
  "/queue": "⇧",
  "/runs": "◷",
  "/workflows": "⌘",
  "/checkpoints": "✓",
  "/usage": "▥",
  "/settings": "⚙",
};

export function AppShell({ children }: { children: React.ReactNode }) {
  const params = useSearchParams();
  const pathname = usePathname();
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const activeProject = params.get("project_id") ?? "";
  const breadcrumb = getBreadcrumb(pathname);

  const close = () => setOpen(false);

  return (
    <div className={`shell ${open ? "shell--sidebar-open" : ""}`}>
      {open ? (
        <div
          className="scrim mobile-scrim"
          style={{ zIndex: 39 }}
          onClick={close}
          aria-hidden
        />
      ) : null}
      <Sidebar activeProject={activeProject} pathname={pathname} onNavigate={close} />
      <div className="main">
        <header className="topbar">
          <button
            type="button"
            className="btn btn--ghost btn--sm sidebar-toggle"
            aria-expanded={open}
            aria-label={t("topbar.toggleTree")}
            onClick={() => setOpen((o) => !o)}
            style={{ marginRight: 2 }}
          >
            <IconMenu />
          </button>
          <span className="brand__name" style={{ padding: 0 }}>
            AI Company
          </span>
          <nav className="topbar__breadcrumb" aria-label={t("topbar.breadcrumb")}>
            <Link href={breadcrumb.href}>{t(breadcrumb.parent)}</Link>
            <span aria-hidden>›</span>
            <span aria-current="page">{t(breadcrumb.current)}</span>
          </nav>
          <LanguageSwitcher />
          <TenantSwitcher />
          <NotificationCentre />
          <UserMenu />
        </header>
        <main className="content">{children}</main>
      </div>
    </div>
  );
}

/**
 * The language of the interface, in the top bar's cluster: two short codes,
 * the live one filled in the product's indigo. One click swaps the whole
 * interface — no reload, no lost state.
 */
function LanguageSwitcher() {
  const { lang, setLang, t } = useI18n();
  return (
    <div
      className="lang-switch"
      role="group"
      aria-label={t("lang.switcher.label")}
      title={t("lang.switcher.title")}
    >
      {LANGUAGES.map((entry) => {
        const on = entry.lang === lang;
        return (
          <button
            key={entry.lang}
            type="button"
            className={on ? "lang-switch__opt lang-switch__opt--on" : "lang-switch__opt"}
            aria-pressed={on}
            lang={entry.lang}
            title={t(entry.nameKey)}
            onClick={() => setLang(entry.lang)}
          >
            {t(entry.shortKey)}
          </button>
        );
      })}
    </div>
  );
}

/** The two halves of the breadcrumb, as translation keys. */
interface Crumb {
  href: string;
  parent: TranslationKey;
  current: TranslationKey;
}

function getBreadcrumb(pathname: string): Crumb {
  if (pathname.startsWith("/settings/")) {
    const current = pathname.slice("/settings/".length).split("/")[0];
    const labels: Record<string, TranslationKey> = {
      agents: "settings.agents",
      runners: "settings.runners",
      notifications: "settings.notifications",
      skills: "settings.skills",
    };
    return {
      href: "/settings",
      parent: "nav.settings",
      current: labels[current] ?? "nav.settings",
    };
  }
  if (pathname.startsWith("/items/"))
    return { href: "/backlog", parent: "crumb.tasks", current: "crumb.taskDetails" };
  if (pathname.startsWith("/runs/"))
    return { href: "/runs", parent: "nav.runs", current: "crumb.runMonitor" };
  if (pathname.startsWith("/workflows/"))
    return { href: "/workflows", parent: "nav.workflows", current: "crumb.workflowEditor" };
  if (pathname.startsWith("/chat/"))
    return { href: "/board", parent: "crumb.workspace", current: "crumb.chat" };
  const current = pathname === "/" ? "home" : pathname.slice(1).split("/")[0];
  const labels: Record<string, TranslationKey> = {
    home: "crumb.home",
    board: "nav.board",
    backlog: "nav.backlog",
    queue: "nav.queue",
    runs: "nav.runs",
    workflows: "nav.workflows",
    checkpoints: "nav.checkpoints",
    usage: "nav.usage",
    rankings: "nav.rankings",
    settings: "nav.settings",
  };
  return { href: "/board", parent: "crumb.workspace", current: labels[current] ?? "nav.page" };
}

function CheckpointBadge() {
  const { t } = useI18n();
  const [count, setCount] = useState<number | null>(null);
  useEffect(() => {
    let alive = true;
    const refresh = () => void api.listCheckpoints("pending", 50).then((r) => { if (alive) setCount(r.checkpoints.length); }).catch(() => { if (alive) setCount(null); });
    refresh();
    const timer = window.setInterval(refresh, 15000);
    return () => { alive = false; window.clearInterval(timer); };
  }, []);
  return count ? <span className="nav-badge" aria-label={t("notifications.pendingCheckpoints", { count })}>{count > 99 ? "99+" : count}</span> : null;
}

/** The closed set of notification kinds the server sends. */
const NOTIFICATION_KINDS: Record<string, TranslationKey> = {
  checkpoint_pending: "notificationKind.checkpoint_pending",
  run_failed: "notificationKind.run_failed",
  run_completed: "notificationKind.run_completed",
  sync_error: "notificationKind.sync_error",
};

function notificationKind(
  kind: string,
  t: (key: TranslationKey) => string,
): string {
  const key = NOTIFICATION_KINDS[kind];
  return key ? t(key) : kind.replaceAll("_", " ");
}

function NotificationCentre() {
  const router = useRouter();
  const { t } = useI18n();
  const [notifications, setNotifications] = useState<Notification[]>([]);
  const [unavailable, setUnavailable] = useState(false);
  useEffect(() => {
    let alive = true;
    const refresh = () => void api.listNotifications(true, 50).then((r) => { if (alive) { setNotifications(r.notifications); setUnavailable(false); } }).catch(() => { if (alive) setUnavailable(true); });
    refresh();
    const timer = window.setInterval(refresh, 15000);
    return () => { alive = false; window.clearInterval(timer); };
  }, []);
  async function openNotification(notification: Notification) {
    try {
      await api.markNotificationRead(notification.id);
      setNotifications((current) => current.filter((row) => row.id !== notification.id));
    } catch { /* keep the unread badge honest when the server cannot mark it */ }
    router.push(notification.run_id ? `/runs/${notification.run_id}` : "/checkpoints");
  }
  const label = t("notifications.title");
  const unread = notifications.length
    ? t("notifications.unread", { count: notifications.length })
    : "";
  return <Menu trigger={<button type="button" className="btn btn--ghost btn--sm notification-trigger" aria-label={unread ? `${label}, ${unread}` : label}><svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden><path d="M3 11.5h10l-1.2-1.7V6.7a3.8 3.8 0 0 0-7.6 0v3.1L3 11.5Z"/><path d="M6.3 13.2a1.8 1.8 0 0 0 3.4 0"/></svg>{notifications.length ? <span className="nav-badge nav-badge--bell">{notifications.length > 99 ? "99+" : notifications.length}</span> : null}</button>}>
    <div className="notification-menu-head"><strong>{t("notifications.title")}</strong><span className="tiny muted">{t("notifications.unread", { count: notifications.length })}</span></div>
    {unavailable ? <div className="notification-empty">{t("notifications.unavailable")}</div> : notifications.length ? notifications.slice(0, 10).map((notification) => <MenuItem key={notification.id} onSelect={() => void openNotification(notification)}><span className="notification-kind">{notificationKind(notification.kind, t)}</span><span className="notification-copy">{notification.body ?? notification.title ?? notification.kind.replaceAll("_", " ")}</span></MenuItem>) : <div className="notification-empty">{t("notifications.caughtUp")}</div>}
    <div className="menu__sep" /><MenuItem onSelect={() => router.push("/checkpoints")}>{t("notifications.openInbox")}</MenuItem>
  </Menu>;
}

/* --- Sidebar: product → project tree -------------------------------------- */

function Sidebar({
  activeProject,
  pathname,
  onNavigate,
}: {
  activeProject: string;
  pathname: string;
  onNavigate: () => void;
}) {
  const [products, setProducts] = useState<Product[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [trees, setTrees] = useState<Record<string, ProjectNode[]>>({});
  const [loading, setLoading] = useState<Record<string, boolean>>({});
  const { t } = useI18n();

  useEffect(() => {
    api
      .listProducts()
      .then((r) => setProducts(r.products))
      .catch((e: Error) => setError(e.message));
  }, []);

  async function toggle(productId: string) {
    const isOpen = expanded[productId];
    setExpanded((e) => ({ ...e, [productId]: !isOpen }));
    if (!isOpen && !trees[productId]) {
      setLoading((l) => ({ ...l, [productId]: true }));
      try {
        const r = await api.tree(productId);
        setTrees((t) => ({ ...t, [productId]: r.projects }));
      } catch (e) {
        setError(e instanceof Error ? e.message : t("tree.failed"));
      } finally {
        setLoading((l) => ({ ...l, [productId]: false }));
      }
    }
  }

  return (
    <aside className="sidebar" aria-label={t("nav.label.tree")}>
      <div className="brand">
        <span className="brand__mark" aria-hidden>
          <IconChevron width={12} height={12} />
        </span>
        <span className="brand__name">AI Company</span>
      </div>
      <nav className="sidebar-nav" aria-label={t("nav.label.primary")}>
        <div className="tree__heading">{t("nav.section.main")}</div>
        {NAV.map((n) => (
          <Link
            key={n.href}
            href={n.href}
            className="sidebar-nav__link"
            aria-current={pathname === n.href || pathname.startsWith(`${n.href}/`) ? "page" : undefined}
            onClick={onNavigate}
          >
            <span className="sidebar-nav__glyph" aria-hidden>{NAV_GLYPHS[n.href]}</span>
            <span>{t(n.labelKey)}</span>
            {n.href === "/checkpoints" ? <CheckpointBadge /> : null}
          </Link>
        ))}
      </nav>
      <div className="tree">
        <div className="tree__heading">{t("nav.section.apps")}</div>
        <div className="tree__subheading">{t("nav.section.products")}</div>
        {error ? (
          <div className="error-note" style={{ margin: 8 }}>
            {error}
          </div>
        ) : null}
        {!products ? (
          <div style={{ padding: 8, display: "grid", gap: 8 }}>
            {[0, 1, 2].map((i) => (
              <div key={i} className="skeleton" style={{ height: 18 }} />
            ))}
          </div>
        ) : products.length === 0 ? (
          <p className="tiny muted" style={{ padding: "4px 8px" }}>
            {t("tree.noProducts")}
          </p>
        ) : (
          <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
            {products.map((p) => (
              <li key={p.id}>
                <button
                  type="button"
                  className="tree-row"
                  aria-expanded={Boolean(expanded[p.id])}
                  onClick={() => void toggle(p.id)}
                >
                  <span className="tree-row__caret">
                    {expanded[p.id] ? <IconChevron style={{ transform: "rotate(90deg)" }} /> : <IconChevron />}
                  </span>
                  <span className="tree-row__name">{p.name}</span>
                </button>
                {expanded[p.id] ? (
                  <div style={{ paddingLeft: 18 }}>
                    {loading[p.id] ? (
                      <div className="skeleton" style={{ height: 16, margin: 4 }} />
                    ) : (
                      <ProjectTree nodes={trees[p.id] ?? []} active={activeProject} depth={0} onNavigate={onNavigate} />
                    )}
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </div>
    </aside>
  );
}

function ProjectTree({
  nodes,
  active,
  depth,
  onNavigate,
}: {
  nodes: ProjectNode[];
  active: string;
  depth: number;
  onNavigate: () => void;
}) {
  const { t } = useI18n();
  if (nodes.length === 0 && depth === 0) {
    return <p className="tiny muted" style={{ padding: "2px 8px 6px" }}>{t("tree.noProjects")}</p>;
  }
  return (
    <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
      {nodes.map((n) => (
        <li key={n.project.id}>
          <Link
            href={`/board?project_id=${n.project.id}`}
            className={`tree-row ${depth > 0 ? "tree-row--nested" : ""}`}
            aria-current={active === n.project.id || undefined}
            style={{ paddingLeft: 6 }}
            onClick={onNavigate}
          >
            {n.project.key ? <span className="tree-row__key">{n.project.key}</span> : null}
            <span className="tree-row__name">{n.project.name}</span>
            <span className="tree-row__pct">
              <ProgressBar percent={n.progress.percent} />{" "}
              {n.progress.total > 0 ? `${n.progress.percent}%` : ""}
            </span>
          </Link>
          {n.children.length > 0 ? (
            <div className="tree-nest">
              <ProjectTree nodes={n.children} active={active} depth={depth + 1} onNavigate={onNavigate} />
            </div>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

/* --- Top bar widgets -------------------------------------------------------- */

function TenantSwitcher() {
  const { me, switchTenant } = useSession();
  const { t } = useI18n();
  if (!me) return null;
  return (
    <Menu
      trigger={
        <button type="button" className="btn btn--sm" data-tenant-switch aria-label={t("tenant.current")}>
          <IconBuilding />
          <span className="">{me.tenant.name}</span>
          <span className="chip" style={{ background: "none", borderColor: "var(--border-strong)", color: "var(--muted)" }}>
            {me.tenant.role}
          </span>
        </button>
      }
    >
      <div className="tiny muted" style={{ padding: "4px 10px 6px" }}>
        {t("tenant.switch")}
      </div>
          {me.tenants.map((entry) => (
        <MenuItem
          key={entry.id}
          disabled={entry.id === me.tenant.id}
          trailing={entry.id === me.tenant.id ? <span className="tiny muted">{t("tenant.currentBadge")}</span> : null}
          onSelect={() => switchTenant(entry.id)}
        >
          <IconBuilding />
          {entry.name}
        </MenuItem>
      ))}
    </Menu>
  );
}

function UserMenu() {
  const { me, signOut } = useSession();
  const router = useRouter();
  const { t } = useI18n();
  if (!me) return null;
  return (
    <Menu
      trigger={
        <button
          type="button"
          className="btn btn--ghost btn--sm"
          data-account-menu
          aria-label={t("account.menu")}
        >
          <IconUser />
          <span className="tiny" style={{ marginLeft: 4 }}>
            {me.user.name}
          </span>
        </button>
      }
    >
      <div style={{ padding: "4px 10px 6px" }}>
        <div style={{ fontWeight: 600, color: "var(--text-strong)" }}>{me.user.name}</div>
        <div className="tiny muted">{me.user.email}</div>
      </div>
      <div className="menu__sep" />
      <DesktopNotifications />
      <div className="menu__sep" />
      <MenuItem
        onSelect={() => {
          router.push("/settings");
        }}
      >
        {t("account.settings")}
      </MenuItem>
      <MenuItem
        danger
        onSelect={() => {
          void signOut();
        }}
      >
        <IconLogout />
        {t("account.signOut")}
      </MenuItem>
    </Menu>
  );
}

type DesktopState = "checking" | "off" | "on" | "unavailable";

/** The browser's own permission states, named in the interface's language. */
const PERMISSION_LABELS: Record<string, TranslationKey> = {
  granted: "push.permission.granted",
  denied: "push.permission.denied",
  default: "push.permission.default",
};

/** Push setup lives in the account menu; it never displays subscription keys. */
function DesktopNotifications() {
  const { canWrite } = useSession();
  const { t } = useI18n();
  const [state, setState] = useState<DesktopState>("checking");
  const [reason, setReason] = useState("");
  const [permission, setPermission] = useState("checking");
  const [serverStatus, setServerStatus] = useState("not checked");
  const [subscriptionId, setSubscriptionId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let alive = true;
    async function inspect() {
      if (!("Notification" in window)) {
        if (alive) { setState("unavailable"); setReason(t("push.err.noSupport")); setPermission("unsupported"); setServerStatus("not reached"); }
        return;
      }
      setPermission(Notification.permission);
      if (!("serviceWorker" in navigator)) {
        if (alive) { setState("unavailable"); setReason(t("push.err.noServiceWorker")); setServerStatus("not reached"); }
        return;
      }
      try {
        const registration = await navigator.serviceWorker.getRegistration();
        if (!registration?.pushManager) {
          if (alive) { setState("unavailable"); setReason(t("push.err.noWorker")); setServerStatus("not reached"); }
          return;
        }
        const local = await registration.pushManager.getSubscription();
        if (!local) {
          if (alive) { setState("off"); setReason(""); setServerStatus("not subscribed"); }
          return;
        }
        const result = await api.listPushSubscriptions();
        const saved = result.subscriptions.find((entry) => entry.endpoint === local.endpoint);
        if (alive) {
          setState(saved ? "on" : "off");
          setSubscriptionId(saved?.id ?? null);
          setReason(saved ? "" : t("push.err.notRegistered"));
          setServerStatus(saved ? "subscription saved" : "not subscribed");
        }
      } catch (error) {
        if (alive) { setState("unavailable"); setReason(""); setServerStatus(pushIssue(error, t)); }
      }
    }
    void inspect();
    return () => { alive = false; };
  }, [t]);

  async function toggle() {
    if (state === "checking" || state === "unavailable" || busy) return;
    setBusy(true);
    setReason("");
    try {
      const registration = await navigator.serviceWorker.getRegistration();
      if (!registration?.pushManager) throw new Error(t("push.err.noWorker"));
      const local = await registration.pushManager.getSubscription();
      if (state === "on") {
        if (!local || !subscriptionId) throw new Error(t("push.err.noMatch"));
        await api.deletePushSubscription(subscriptionId);
        await local.unsubscribe();
        setSubscriptionId(null);
        setState("off");
        setReason("");
        setServerStatus("subscription removed");
      } else {
        let currentPermission = Notification.permission;
        if (currentPermission === "default") currentPermission = await Notification.requestPermission();
        setPermission(currentPermission);
        if (currentPermission !== "granted") throw new Error(currentPermission === "denied" ? t("push.err.denied") : t("push.err.notGranted"));
        const pushSub = local ?? await registration.pushManager.subscribe({ userVisibleOnly: true });
        const serialized = pushSub.toJSON();
        if (!serialized.endpoint || !serialized.keys?.p256dh || !serialized.keys.auth) throw new Error(t("push.err.incomplete"));
        try {
          const result = await api.createPushSubscription({ endpoint: serialized.endpoint, keys: { p256dh: serialized.keys.p256dh, auth: serialized.keys.auth }, expirationTime: serialized.expirationTime });
          setSubscriptionId(result.subscription.id);
          setState("on");
          setReason("");
          setServerStatus("subscription saved");
        } catch (error) {
          if (error instanceof Error && "code" in error) setServerStatus(pushIssue(error, t));
          if (!local) await pushSub.unsubscribe().catch(() => false);
          throw error;
        }
      }
    } catch (error) {
      setState("unavailable");
      const message = pushIssue(error, t);
      setReason(error instanceof Error && "code" in error ? "" : message);
      if (error instanceof Error && "code" in error) setServerStatus(message);
      else if (serverStatus === "not checked") setServerStatus("not reached");
    } finally {
      setBusy(false);
    }
  }

  const status = state === "on" ? t("push.on") : state === "off" ? t("push.off") : state === "checking" ? t("push.checking") : t("push.unavailable");
  const permissionCopy = permission === "checking"
    ? t("push.checking").toLowerCase()
    : PERMISSION_LABELS[permission]
      ? t(PERMISSION_LABELS[permission])
      : permission;
  const browserSupported = typeof window !== "undefined" && "Notification" in window && "serviceWorker" in navigator && "PushManager" in window;
  const serverLabel = serverStatus === "subscription saved"
    ? t("push.state.saved")
    : serverStatus === "subscription removed"
      ? t("push.state.removed")
      : serverStatus === "not subscribed"
        ? t("push.state.notSubscribed")
        : serverStatus === "not checked"
          ? t("push.state.notChecked")
          : t("push.state.notReached");
  return <div className="desktop-push">
    <MenuItem onSelect={() => void toggle()} disabled={state === "checking" || state === "unavailable" || busy || !canWrite} trailing={<span className={`push-state push-state--${state}`}>{busy ? t("push.working") : status}</span>}>
      {t("push.title")}
    </MenuItem>
    <div className="desktop-push__details">
      <span>{t("push.browser", { state: browserSupported ? t("push.supported") : t("push.unsupported") })}</span>
      <span>{t("push.permission", { state: permissionCopy })}</span>
      <span>{t("push.server", { state: serverLabel })}</span>
      {state === "unavailable" && reason ? <span className="desktop-push__reason">{t("push.setup", { reason })}</span> : null}
      {!canWrite ? <span className="desktop-push__reason">{t("push.guest")}</span> : null}
    </div>
  </div>;
}

function pushIssue(error: unknown, t: (key: "push.err.generic") => string): string {
  if (error instanceof Error && "code" in error) {
    const coded = error as Error & { code: string };
    return `${coded.code}: ${coded.message}`;
  }
  return error instanceof Error ? error.message : t("push.err.generic");
}
