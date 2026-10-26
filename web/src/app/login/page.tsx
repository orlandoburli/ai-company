"use client";

import { Suspense, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { useSession } from "@/lib/session";
import type { TenantView } from "@/lib/types";
import { useT } from "@/lib/i18n";
import { apiErrorMessage } from "@/components/primitives";

export default function LoginPage() {
  const t = useT();
  return (
    <Suspense fallback={<div className="auth-wrap"><div className="auth-card"><p className="sub">{t("auth.loading")}</p></div></div>}>
      <LoginForm />
    </Suspense>
  );
}

function LoginForm() {
  const t = useT();
  const params = useSearchParams();
  const router = useRouter();
  const { refresh } = useSession();
  const next = params.get("next") || "/board";

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tenants, setTenants] = useState<TenantView[] | null>(null);

  async function submit(e: React.FormEvent, tenantId?: string) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.login({ email, password, tenant_id: tenantId });
      await refresh();
      router.replace(next);
    } catch (err) {
      if (err instanceof ApiError && err.code === "tenant_selection_required") {
        const body = err.data as { tenants?: TenantView[] } | null;
        setTenants(body?.tenants ?? null);
        setError(t("auth.login.multipleCompanies"));
      } else {
        setError(translate(err, t));
      }
      setBusy(false);
    }
  }

  return (
    <div className="auth-wrap">
      <div className="auth-card">
        <h1>{t("auth.login.title")}</h1>
        <p className="sub">{t("auth.login.subtitle")}</p>
        <form onSubmit={(e) => void submit(e)}>
          {error ? <div className="error-note">{error}</div> : null}
          {tenants ? (
            <div className="field" role="group" aria-label={t("auth.login.chooseCompany")}>
              {tenants.map((tenant) => (
                <button
                  key={tenant.id}
                  type="button"
                  className="btn"
                  disabled={busy}
                  onClick={(e) => void submit(e, tenant.id)}
                  style={{ justifyContent: "flex-start" }}
                >
                  {tenant.name}
                  <span className="tiny muted" style={{ marginLeft: "auto" }}>
                    {roleLabel(tenant.role, t)}
                  </span>
                </button>
              ))}
            </div>
          ) : (
            <>
              <div className="field">
                <label htmlFor="l-email">{t("auth.login.email")}</label>
                <input
                  id="l-email"
                  type="email"
                  className="input"
                  autoComplete="email"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                />
              </div>
              <div className="field">
                <label htmlFor="l-password">{t("auth.login.password")}</label>
                <input
                  id="l-password"
                  type="password"
                  className="input"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </div>
              <button type="submit" className="btn btn--primary" disabled={busy}>
                {busy ? t("auth.login.signingIn") : t("auth.login.submit")}
              </button>
            </>
          )}
        </form>
        <p className="auth-foot">
          {t("auth.login.newHere")} <Link href="/register">{t("auth.login.createCompany")}</Link> ·{" "}
          <Link href="/accept-invite">{t("auth.login.haveInvite")}</Link>
        </p>
      </div>
    </div>
  );
}

function translate(err: unknown, t: ReturnType<typeof useT>): string {
  if (err instanceof ApiError && err.code === "invalid_credentials") return t("auth.login.invalidCredentials");
  if (err instanceof ApiError && err.code === "no_tenant") return t("auth.login.noTenant");
  return apiErrorMessage(err);
}

function roleLabel(role: string, t: ReturnType<typeof useT>): string {
  switch (role) {
    case "owner": return t("settings.members.owner");
    case "admin": return t("settings.members.admin");
    case "member": return t("settings.members.member");
    case "guest": return t("settings.members.guest");
    default: return role;
  }
}
