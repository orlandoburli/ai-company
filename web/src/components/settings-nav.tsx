"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

/**
 * Sub-navigation for the settings area. Runner and skill registries are
 * readable by every role; their APIs gate writes to owners and admins.
 */
export function SettingsNav() {
  const pathname = usePathname();

  const links = [
    { href: "/settings", label: "Company" },
    { href: "/settings/agents", label: "Agents" },
    { href: "/settings/runners", label: "Runners" },
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
