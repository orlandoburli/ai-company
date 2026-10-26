"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useT } from "@/lib/i18n";

/**
 * Sub-navigation for the settings area. Runner and skill registries are
 * readable by every role; their APIs gate writes to owners and admins.
 */
export function SettingsNav() {
  const pathname = usePathname();
  const t = useT();

  const links = [
    { href: "/settings", label: t("settings.nav.company") },
    { href: "/settings/agents", label: t("settings.nav.agents") },
    { href: "/settings/runners", label: t("settings.nav.runners") },
    { href: "/settings/notifications", label: t("settings.nav.notifications") },
    { href: "/settings/skills", label: t("settings.nav.skills") },
    { href: "/settings/kinds", label: t("settings.nav.kinds") },
  ];

  return (
    <nav className="subnav" aria-label={t("settings.nav.label")}>
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
