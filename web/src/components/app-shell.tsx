"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { api } from "@/lib/api";
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
  { href: "/settings", label: "Settings" },
];

export function AppShell({ children }: { children: React.ReactNode }) {
  const params = useSearchParams();
  const pathname = usePathname();
  const [open, setOpen] = useState(false);
  const activeProject = params.get("project_id") ?? "";

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
      <Sidebar activeProject={activeProject} onNavigate={close} />
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
          <nav className="nav" aria-label="Primary">
            {NAV.map((n) => (
              <Link
                key={n.href}
                href={n.href}
                aria-current={pathname === n.href ? "page" : undefined}
              >
                {n.label}
              </Link>
            ))}
          </nav>
          <span style={{ marginLeft: "auto" }} />
          <TenantSwitcher />
          <UserMenu />
        </header>
        <main className="content">{children}</main>
      </div>
    </div>
  );
}

/* --- Sidebar: product → project tree -------------------------------------- */

function Sidebar({
  activeProject,
  onNavigate,
}: {
  activeProject: string;
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
      <div className="tree">
        <div className="tree__heading">Products</div>
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
