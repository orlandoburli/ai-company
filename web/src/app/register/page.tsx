"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { useSession } from "@/lib/session";
import { slugify } from "@/lib/text";
import { useT } from "@/lib/i18n";
import { apiErrorMessage } from "@/components/primitives";

export default function RegisterPage() {
  const t = useT();
  const router = useRouter();
  const { refresh } = useSession();
  const [form, setForm] = useState({
    name: "",
    email: "",
    password: "",
    tenant_name: "",
    tenant_slug: "",
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function set<K extends keyof typeof form>(k: K, v: string) {
    setForm((f) => ({
      ...f,
      [k]: k === "tenant_slug" ? slugify(v) : v,
    }));
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.register(form);
      await refresh();
      router.replace("/board");
    } catch (err) {
      setError(translate(err, t));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="auth-wrap">
      <div className="auth-card">
        <h1>{t("auth.register.title")}</h1>
        <p className="sub">
          {t("auth.register.subtitle")}
        </p>
        <form onSubmit={submit}>
          {error ? <div className="error-note">{error}</div> : null}
          <div className="field">
            <label htmlFor="r-name">{t("auth.register.name")}</label>
            <input
              id="r-name"
              className="input"
              autoComplete="name"
              required
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="r-email">{t("auth.register.email")}</label>
            <input
              id="r-email"
              type="email"
              className="input"
              autoComplete="email"
              required
              value={form.email}
              onChange={(e) => set("email", e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="r-password">{t("auth.register.password")}</label>
            <input
              id="r-password"
              type="password"
              className="input"
              autoComplete="new-password"
              required
              minLength={12}
              value={form.password}
              onChange={(e) => set("password", e.target.value)}
            />
            <span className="tiny muted">
              {t("auth.register.passwordHint")}
            </span>
          </div>
          <div className="field">
            <label htmlFor="r-tenant">{t("auth.register.companyName")}</label>
            <input
              id="r-tenant"
              className="input"
              required
              value={form.tenant_name}
              onChange={(e) => set("tenant_name", e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="r-slug">{t("auth.register.companyHandle")}</label>
            <input
              id="r-slug"
              className="input mono"
              required
              placeholder={t("auth.register.slugPlaceholder")}
              value={form.tenant_slug}
              onChange={(e) => set("tenant_slug", e.target.value)}
            />
            <span className="tiny muted">
              {t("auth.register.slugHint")}
            </span>
          </div>
          <button type="submit" className="btn btn--primary" disabled={busy}>
            {busy ? t("auth.register.creating") : t("auth.register.submit")}
          </button>
        </form>
        <p className="auth-foot">
          {t("auth.register.haveAccount")} <Link href="/login">{t("auth.register.signIn")}</Link>
        </p>
      </div>
    </div>
  );
}

function translate(err: unknown, t: ReturnType<typeof useT>): string {
  if (err instanceof ApiError && err.code === "conflict") return t("auth.register.conflict");
  if (!(err instanceof ApiError)) return t("auth.register.failed");
  return apiErrorMessage(err);
}
