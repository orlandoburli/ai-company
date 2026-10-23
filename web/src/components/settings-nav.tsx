"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useSession } from "@/lib/session";

/**
 * Sub-navigation for the settings area. Skills are visible to every role, but
 * the registry itself gates writes to owners and admins. Agents remain
 * owner/admin-only.
 */
export function SettingsNav() {
  const { me } = useSession();
  const pathname = usePathname();
  const isManager =
    me?.tenant.role === "owner" || me?.tenant.role === "admin";

  const links = [
    { href: "/settings", label: "Company" },
    ...(isManager ? [{ href: "/settings/agents", label: "Agents" }] : []),
    { href: "/settings/notifications", label: "Notifications" },
    { href: "/settings/skills", label: "Skills" },
  ];

  return (
    <nav className="subnav" aria-label="Settings sections">
      {links.map((l) => (
        <Link
          key={l.href}
          href={l.href}
          aria-current={pathname === l.href ? "page" : undefined}
        >
          {l.label}
        </Link>
      ))}
    </nav>
  );
}
