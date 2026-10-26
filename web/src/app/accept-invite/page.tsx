"use client";

import { Suspense, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { useSession } from "@/lib/session";
import type { InvitePeek } from "@/lib/types";
import { useT, useI18n } from "@/lib/i18n";
import { apiErrorMessage } from "@/components/primitives";

export default function AcceptInvitePage() {
  const t = useT();
  return (
    <Suspense
      fallback={
        <div className="auth-wrap">
          <div className="auth-card">
            <p className="sub">{t("auth.loading")}</p>
          </div>
        </div>
      }
    >
      <AcceptForm />
    </Suspense>
  );
}

function AcceptForm() {
  const t = useT();
  const { lang } = useI18n();
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
          setPeekError(e instanceof ApiError ? apiErrorMessage(e) : t("auth.invite.readFailed"));
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
      setError(translate(err, t));
      setBusy(false);
    }
  }

  let body: React.ReactNode;
  if (peekError || !token) {
    body = (
      <>
        <p className="sub">
          {peekError ?? t("auth.invite.incomplete")}
        </p>
        <p className="auth-foot">
          {t("auth.invite.freshLink")} {" "}
          <Link href="/login">{t("auth.login.submit")}</Link> {t("auth.invite.alreadyAccount")}
        </p>
        <Link className="btn" href="/login">
          {t("auth.invite.goToSignIn")}
        </Link>
      </>
    );
  } else if (!peek) {
    body = <p className="sub">{t("auth.invite.reading")}</p>;
  } else {
    body = (
      <>
        <p className="sub">
          {t("auth.invite.invitedAs")} <strong>{peek.email}</strong> — {roleLabel(peek.role, t)} {t("auth.invite.at")} {" "}
          <strong>{peek.tenant_name}</strong>. {t("auth.invite.expires")} {" "}
          {new Date(peek.expires_at).toLocaleDateString(lang)}.
        </p>
        <form onSubmit={submit}>
          {error ? <div className="error-note">{error}</div> : null}
          <div className="field">
            <label htmlFor="a-name">{t("auth.invite.name")}</label>
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
            <label htmlFor="a-password">{t("auth.invite.password")}</label>
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
              {t("auth.invite.passwordHint", { email: peek.email })}
            </span>
          </div>
          <button type="submit" className="btn btn--primary" disabled={busy}>
            {busy ? t("auth.invite.accepting") : t("auth.invite.accept")}
          </button>
        </form>
        <p className="auth-foot">
          {t("auth.invite.haveAccountThere")} <Link href="/login">{t("auth.login.submit")}</Link> — {t("auth.invite.attachAccount")}
        </p>
      </>
    );
  }

  return (
    <div className="auth-wrap">
      <div className="auth-card">
        <h1>{t("auth.invite.title")}</h1>
        {body}
      </div>
    </div>
  );
}

function translate(err: unknown, t: ReturnType<typeof useT>): string {
  return err instanceof ApiError ? apiErrorMessage(err) : t("auth.invite.acceptFailed");
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
