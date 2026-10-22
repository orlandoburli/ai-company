"use client";

import { Suspense, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { useSession } from "@/lib/session";
import type { TenantView } from "@/lib/types";

export default function LoginPage() {
  return (
    <Suspense fallback={<div className="auth-wrap"><div className="auth-card"><p className="sub">Loading…</p></div></div>}>
      <LoginForm />
    </Suspense>
  );
}

function LoginForm() {
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
        setError("Your account belongs to more than one company. Choose which one to open.");
      } else {
        setError(translate(err));
      }
      setBusy(false);
    }
  }

  return (
    <div className="auth-wrap">
      <div className="auth-card">
        <h1>Sign in</h1>
        <p className="sub">Use the email you registered or were invited with.</p>
        <form onSubmit={(e) => void submit(e)}>
          {error ? <div className="error-note">{error}</div> : null}
          {tenants ? (
            <div className="field" role="group" aria-label="Choose company">
              {tenants.map((t) => (
                <button
                  key={t.id}
                  type="button"
                  className="btn"
                  disabled={busy}
                  onClick={(e) => void submit(e, t.id)}
                  style={{ justifyContent: "flex-start" }}
                >
                  {t.name}
                  <span className="tiny muted" style={{ marginLeft: "auto" }}>
                    {t.role}
                  </span>
                </button>
              ))}
            </div>
          ) : (
            <>
              <div className="field">
                <label htmlFor="l-email">Email</label>
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
                <label htmlFor="l-password">Password</label>
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
                {busy ? "Signing in…" : "Sign in"}
              </button>
            </>
          )}
        </form>
        <p className="auth-foot">
          New here? <Link href="/register">Create a company</Link> ·{" "}
          <Link href="/accept-invite">Have an invite?</Link>
        </p>
      </div>
    </div>
  );
}

function translate(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "invalid_credentials":
        return "Email or password is incorrect.";
      case "no_tenant":
        return "This account is not a member of any company yet. Ask its owner for an invitation.";
      case "forbidden":
        return err.message;
      default:
        return err.message;
    }
  }
  return "Could not sign in. Try again.";
}
