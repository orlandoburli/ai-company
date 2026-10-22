"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { useSession } from "@/lib/session";
import { slugify } from "@/lib/text";

export default function RegisterPage() {
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
      setError(translate(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="auth-wrap">
      <div className="auth-card">
        <h1>Create your company account</h1>
        <p className="sub">
          Registers you as the owner and creates a new company.
        </p>
        <form onSubmit={submit}>
          {error ? <div className="error-note">{error}</div> : null}
          <div className="field">
            <label htmlFor="r-name">Your name</label>
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
            <label htmlFor="r-email">Email</label>
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
            <label htmlFor="r-password">Password</label>
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
              At least 12 characters. A pass-phrase of four words works well.
            </span>
          </div>
          <div className="field">
            <label htmlFor="r-tenant">Company name</label>
            <input
              id="r-tenant"
              className="input"
              required
              value={form.tenant_name}
              onChange={(e) => set("tenant_name", e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="r-slug">Company handle</label>
            <input
              id="r-slug"
              className="input mono"
              required
              placeholder="acme"
              value={form.tenant_slug}
              onChange={(e) => set("tenant_slug", e.target.value)}
            />
            <span className="tiny muted">
              3–63 characters: lowercase letters, digits, hyphens. Used in the URL.
            </span>
          </div>
          <button type="submit" className="btn btn--primary" disabled={busy}>
            {busy ? "Creating…" : "Create account"}
          </button>
        </form>
        <p className="auth-foot">
          Already have an account? <Link href="/login">Sign in</Link>
        </p>
      </div>
    </div>
  );
}

function translate(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "conflict":
        return "That email or company handle is already in use.";
      case "weak_password":
        return err.message;
      case "invalid_tenant_slug":
        return err.message;
      case "invalid_email":
        return err.message;
      default:
        return err.message;
    }
  }
  return "Could not register. Try again.";
}
