"use client";

import { Suspense, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { useSession } from "@/lib/session";
import type { InvitePeek } from "@/lib/types";

export default function AcceptInvitePage() {
  return (
    <Suspense
      fallback={
        <div className="auth-wrap">
          <div className="auth-card">
            <p className="sub">Loading…</p>
          </div>
        </div>
      }
    >
      <AcceptForm />
    </Suspense>
  );
}

function AcceptForm() {
  const params = useSearchParams();
  const router = useRouter();
  const { refresh } = useSession();
  const token = params.get("token") ?? "";

  const [peek, setPeek] = useState<InvitePeek | null>(null);
  const [peekError, setPeekError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    let alive = true;
    api
      .peekInvite(token)
      .then((p) => {
        if (alive) setPeek(p);
      })
      .catch((e: unknown) => {
        // Asynchronous state update inside a promise callback, never sync in
        // the effect body.
        if (alive) {
          setPeekError(
            e instanceof ApiError
              ? e.message
              : "Could not read this invitation.",
          );
        }
      });
    return () => {
      alive = false;
    };
  }, [token]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.acceptInvite({ token, password, name });
      await refresh();
      router.replace("/board");
    } catch (err) {
      setError(translate(err));
      setBusy(false);
    }
  }

  let body: React.ReactNode;
  if (peekError || !token) {
    body = (
      <>
        <p className="sub">
          {peekError ?? "This invitation link is incomplete."}
        </p>
        <p className="auth-foot">
          Ask for a fresh invitation link, or{" "}
          <Link href="/login">sign in</Link> if you already have an account.
        </p>
        <Link className="btn" href="/login">
          Go to sign in
        </Link>
      </>
    );
  } else if (!peek) {
    body = <p className="sub">Reading the invitation…</p>;
  } else {
    body = (
      <>
        <p className="sub">
          Invited as <strong>{peek.email}</strong> — {peek.role} at{" "}
          <strong>{peek.tenant_name}</strong>. Expires{" "}
          {new Date(peek.expires_at).toLocaleDateString()}.
        </p>
        <form onSubmit={submit}>
          {error ? <div className="error-note">{error}</div> : null}
          <div className="field">
            <label htmlFor="a-name">Your name</label>
            <input
              id="a-name"
              className="input"
              autoComplete="name"
              required
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="a-password">Password</label>
            <input
              id="a-password"
              type="password"
              className="input"
              autoComplete="new-password"
              required
              minLength={12}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <span className="tiny muted">
              At least 12 characters. This becomes the account{" "}
              <strong>{peek.email}</strong>.
            </span>
          </div>
          <button type="submit" className="btn btn--primary" disabled={busy}>
            {busy ? "Accepting…" : "Accept invitation"}
          </button>
        </form>
        <p className="auth-foot">
          Already have an account there? <Link href="/login">Sign in</Link> —
          the invitation will attach to it.
        </p>
      </>
    );
  }

  return (
    <div className="auth-wrap">
      <div className="auth-card">
        <h1>Join a company</h1>
        {body}
      </div>
    </div>
  );
}

function translate(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "invalid_invite":
      case "already_member":
      case "weak_password":
        return err.message;
      default:
        return err.message;
    }
  }
  return "Could not accept the invitation. Try again.";
}
