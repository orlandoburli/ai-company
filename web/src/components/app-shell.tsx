"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { api, type Notification } from "@/lib/api";
import { useSession } from "@/lib/session";
import type { Product, ProjectNode } from "@/lib/types";
import { Menu, MenuItem, ProgressBar } from "./primitives";
import {
  IconBuilding,
  IconChevron,
  IconLogout,
  IconMenu,
  IconUser,
} from "./icons";

const NAV = [
  { href: "/board", label: "Board" },
  { href: "/backlog", label: "Backlog" },
  { href: "/queue", label: "Queue" },
  { href: "/runs", label: "Runs" },
  { href: "/workflows", label: "Workflows" },
  { href: "/checkpoints", label: "Checkpoints" },
  { href: "/usage", label: "Usage" },
  { href: "/settings", label: "Settings" },
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
            aria-label="Toggle product tree"
            onClick={() => setOpen((o) => !o)}
            style={{ marginRight: 2 }}
          >
            <IconMenu />
          </button>
          <span className="brand__name" style={{ padding: 0 }}>
            AI Company
          </span>
          <nav className="topbar__breadcrumb" aria-label="Breadcrumb">
            <Link href={breadcrumb.href}>{breadcrumb.parent}</Link>
            <span aria-hidden>›</span>
            <span aria-current="page">{breadcrumb.current}</span>
          </nav>
          <TenantSwitcher />
          <NotificationCentre />
          <UserMenu />
        </header>
        <main className="content">{children}</main>
      </div>
    </div>
  );
}

function getBreadcrumb(pathname: string) {
  if (pathname.startsWith("/settings/")) {
    const current = pathname.slice("/settings/".length).split("/")[0];
    const labels: Record<string, string> = {
      agents: "Agents",
      runners: "Runners",
      notifications: "Notifications",
      skills: "Skills",
    };
    return { href: "/settings", parent: "Settings", current: labels[current] ?? "Settings" };
  }
  if (pathname.startsWith("/items/")) return { href: "/backlog", parent: "Tasks", current: "Task details" };
  if (pathname.startsWith("/runs/")) return { href: "/runs", parent: "Runs", current: "Run monitor" };
  if (pathname.startsWith("/workflows/")) return { href: "/workflows", parent: "Workflows", current: "Workflow editor" };
  if (pathname.startsWith("/chat/")) return { href: "/board", parent: "Workspace", current: "Chat" };
  const current = pathname === "/" ? "Home" : pathname.slice(1).split("/")[0];
  const labels: Record<string, string> = {
    board: "Board",
    backlog: "Backlog",
    queue: "Queue",
    runs: "Runs",
    workflows: "Workflows",
    checkpoints: "Checkpoints",
    usage: "Usage",
    rankings: "Rankings",
    settings: "Settings",
  };
  return { href: "/board", parent: "Workspace", current: labels[current] ?? current };
}

function CheckpointBadge() {
  const [count, setCount] = useState<number | null>(null);
  useEffect(() => {
    let alive = true;
    const refresh = () => void api.listCheckpoints("pending", 50).then((r) => { if (alive) setCount(r.checkpoints.length); }).catch(() => { if (alive) setCount(null); });
    refresh();
    const timer = window.setInterval(refresh, 15000);
    return () => { alive = false; window.clearInterval(timer); };
  }, []);
  return count ? <span className="nav-badge" aria-label={`${count} pending checkpoints`}>{count > 99 ? "99+" : count}</span> : null;
}

function NotificationCentre() {
  const router = useRouter();
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
  return <Menu trigger={<button type="button" className="btn btn--ghost btn--sm notification-trigger" aria-label={`Notifications${notifications.length ? `, ${notifications.length} unread` : ""}`}><svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden><path d="M3 11.5h10l-1.2-1.7V6.7a3.8 3.8 0 0 0-7.6 0v3.1L3 11.5Z"/><path d="M6.3 13.2a1.8 1.8 0 0 0 3.4 0"/></svg>{notifications.length ? <span className="nav-badge nav-badge--bell">{notifications.length > 99 ? "99+" : notifications.length}</span> : null}</button>}>
    <div className="notification-menu-head"><strong>Notifications</strong><span className="tiny muted">{notifications.length} unread</span></div>
    {unavailable ? <div className="notification-empty">Notifications are not available yet.</div> : notifications.length ? notifications.slice(0, 10).map((notification) => <MenuItem key={notification.id} onSelect={() => void openNotification(notification)}><span className="notification-kind">{notification.kind.replaceAll("_", " ")}</span><span className="notification-copy">{notification.body ?? notification.title ?? notification.kind.replaceAll("_", " ")}</span></MenuItem>) : <div className="notification-empty">All caught up.</div>}
    <div className="menu__sep" /><MenuItem onSelect={() => router.push("/checkpoints")}>Open checkpoint inbox</MenuItem>
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
        setError(e instanceof Error ? e.message : "Failed to load the tree.");
      } finally {
        setLoading((l) => ({ ...l, [productId]: false }));
      }
    }
  }

  return (
    <aside className="sidebar" aria-label="Product tree">
      <div className="brand">
        <span className="brand__mark" aria-hidden>
          <IconChevron width={12} height={12} />
        </span>
        <span className="brand__name">AI Company</span>
      </div>
      <nav className="sidebar-nav" aria-label="Primary">
        <div className="tree__heading">Main</div>
        {NAV.map((n) => (
          <Link
            key={n.href}
            href={n.href}
            className="sidebar-nav__link"
            aria-current={pathname === n.href || pathname.startsWith(`${n.href}/`) ? "page" : undefined}
            onClick={onNavigate}
          >
            <span className="sidebar-nav__glyph" aria-hidden>{NAV_GLYPHS[n.href]}</span>
            <span>{n.label}</span>
            {n.href === "/checkpoints" ? <CheckpointBadge /> : null}
          </Link>
        ))}
      </nav>
      <div className="tree">
        <div className="tree__heading">Apps</div>
        <div className="tree__subheading">Products</div>
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
            No products yet. Create one in Settings.
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
  if (nodes.length === 0 && depth === 0) {
    return <p className="tiny muted" style={{ padding: "2px 8px 6px" }}>No projects.</p>;
  }
  return (
    <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
      {nodes.map((n) => (
        <li key={n.project.id}>
          <Link
            href={`/board?project_id=${n.project.id}`}
            className="tree-row"
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
            <div style={{ paddingLeft: 12 }}>
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
  if (!me) return null;
  return (
    <Menu
      trigger={
        <button type="button" className="btn btn--sm" aria-label="Current company, open to switch">
          <IconBuilding />
          <span className="">{me.tenant.name}</span>
          <span className="chip" style={{ background: "none", borderColor: "var(--border-strong)", color: "var(--muted)" }}>
            {me.tenant.role}
          </span>
        </button>
      }
    >
      <div className="tiny muted" style={{ padding: "4px 10px 6px" }}>
        Switch company
      </div>
          {me.tenants.map((t) => (
        <MenuItem
          key={t.id}
          disabled={t.id === me.tenant.id}
          trailing={t.id === me.tenant.id ? <span className="tiny muted">current</span> : null}
          onSelect={() => switchTenant(t.id)}
        >
          <IconBuilding />
          {t.name}
        </MenuItem>
      ))}
    </Menu>
  );
}

function UserMenu() {
  const { me, signOut } = useSession();
  const router = useRouter();
  if (!me) return null;
  return (
    <Menu
      trigger={
        <button
          type="button"
          className="btn btn--ghost btn--sm"
          aria-label="Account menu"
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
        Settings
      </MenuItem>
      <MenuItem
        danger
        onSelect={() => {
          void signOut();
        }}
      >
        <IconLogout />
        Sign out
      </MenuItem>
    </Menu>
  );
}

type DesktopState = "checking" | "off" | "on" | "unavailable";

/** Push setup lives in the account menu; it never displays subscription keys. */
function DesktopNotifications() {
  const { canWrite } = useSession();
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
        if (alive) { setState("unavailable"); setReason("This browser does not support notifications."); setPermission("unsupported"); setServerStatus("not reached"); }
        return;
      }
      setPermission(Notification.permission);
      if (!("serviceWorker" in navigator)) {
        if (alive) { setState("unavailable"); setReason("This browser does not support service workers, which push needs."); setServerStatus("not reached"); }
        return;
      }
      try {
        const registration = await navigator.serviceWorker.getRegistration();
        if (!registration?.pushManager) {
          if (alive) { setState("unavailable"); setReason("No service worker is installed yet. Push needs one to receive notifications."); setServerStatus("not reached"); }
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
          setReason(saved ? "" : "A browser subscription exists but is not registered with this server.");
          setServerStatus(saved ? "subscription saved" : "not subscribed");
        }
      } catch (error) {
        if (alive) { setState("unavailable"); setReason(""); setServerStatus(pushIssue(error)); }
      }
    }
    void inspect();
    return () => { alive = false; };
  }, []);

  async function toggle() {
    if (state === "checking" || state === "unavailable" || busy) return;
    setBusy(true);
    setReason("");
    try {
      const registration = await navigator.serviceWorker.getRegistration();
      if (!registration?.pushManager) throw new Error("No service worker is installed yet. Push needs one to receive notifications.");
      const local = await registration.pushManager.getSubscription();
      if (state === "on") {
        if (!local || !subscriptionId) throw new Error("The browser subscription could not be matched to the server.");
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
        if (currentPermission !== "granted") throw new Error(currentPermission === "denied" ? "Browser permission is denied. Allow notifications in browser settings to continue." : "Browser permission was not granted.");
        const pushSub = local ?? await registration.pushManager.subscribe({ userVisibleOnly: true });
        const serialized = pushSub.toJSON();
        if (!serialized.endpoint || !serialized.keys?.p256dh || !serialized.keys.auth) throw new Error("The browser did not provide a complete push subscription.");
        try {
          const result = await api.createPushSubscription({ endpoint: serialized.endpoint, keys: { p256dh: serialized.keys.p256dh, auth: serialized.keys.auth }, expirationTime: serialized.expirationTime });
          setSubscriptionId(result.subscription.id);
          setState("on");
          setReason("");
          setServerStatus("subscription saved");
        } catch (error) {
          if (error instanceof Error && "code" in error) setServerStatus(pushIssue(error));
          if (!local) await pushSub.unsubscribe().catch(() => false);
          throw error;
        }
      }
    } catch (error) {
      setState("unavailable");
      const message = pushIssue(error);
      setReason(error instanceof Error && "code" in error ? "" : message);
      if (error instanceof Error && "code" in error) setServerStatus(message);
      else if (serverStatus === "not checked") setServerStatus("not reached");
    } finally {
      setBusy(false);
    }
  }

  const status = state === "on" ? "On · this browser" : state === "off" ? "Off" : state === "checking" ? "Checking…" : "Unavailable";
  const permissionCopy = permission === "checking" ? "checking" : permission;
  const browserSupported = typeof window !== "undefined" && "Notification" in window && "serviceWorker" in navigator && "PushManager" in window;
  return <div className="desktop-push">
    <MenuItem onSelect={() => void toggle()} disabled={state === "checking" || state === "unavailable" || busy || !canWrite} trailing={<span className={`push-state push-state--${state}`}>{busy ? "Working…" : status}</span>}>
      Desktop notifications
    </MenuItem>
    <div className="desktop-push__details">
      <span>Browser: {browserSupported ? "supported" : "unsupported"}</span>
      <span>Permission: {permissionCopy}</span>
      <span>Server: {serverStatus}</span>
      {state === "unavailable" && reason ? <span className="desktop-push__reason">Setup: {reason}</span> : null}
      {!canWrite ? <span className="desktop-push__reason">Guests can’t subscribe to push notifications.</span> : null}
    </div>
  </div>;
}

function pushIssue(error: unknown): string {
  if (error instanceof Error && "code" in error) {
    const coded = error as Error & { code: string };
    return `${coded.code}: ${coded.message}`;
  }
  return error instanceof Error ? error.message : "Something went wrong.";
}
