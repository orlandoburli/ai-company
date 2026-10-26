import type { Metadata, Viewport } from "next";
import { IBM_Plex_Mono, Outfit } from "next/font/google";
import { SessionProvider } from "@/lib/session";
import { ConfirmProvider } from "@/lib/confirm";
import { I18nProvider } from "@/lib/i18n";
import "./globals.css";

const outfit = Outfit({
  weight: ["400", "500", "600", "700", "800"],
  subsets: ["latin"],
  variable: "--font-outfit",
  display: "swap",
});

const plexMono = IBM_Plex_Mono({
  weight: ["400", "500", "600"],
  subsets: ["latin"],
  variable: "--font-plex-mono",
  display: "swap",
});

export const metadata: Metadata = {
  title: {
    default: "AI Company",
    template: "%s — AI Company",
  },
  description:
    "Control plane for running a software company with AI agents: products, projects, work items and a dispatch queue.",
};

export const viewport: Viewport = {
  themeColor: "#2b3845",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" className={`${outfit.variable} ${plexMono.variable}`}>
      <body>
        <I18nProvider>
          <SessionProvider>
            <ConfirmProvider>{children}</ConfirmProvider>
          </SessionProvider>
        </I18nProvider>
      </body>
    </html>
  );
}
