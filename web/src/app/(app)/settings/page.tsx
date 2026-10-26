"use client";

import { FormEvent, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useConfirm } from "@/lib/confirm";
import { useSession } from "@/lib/session";
import type { Member, Role } from "@/lib/types";
import { formatDate } from "@/lib/format";
import {
  CopyButton,
  Dialog,
  ErrorBlock,
  Field,
  Skeleton,
  Toasts,
  apiErrorMessage,
} from "@/components/primitives";
import { IconBuilding } from "@/components/icons";
import { SettingsNav } from "@/components/settings-nav";
import { useT } from "@/lib/i18n";

const ROLES: Role[] = ["owner", "admin", "member", "guest"];

export default function SettingsPage() {
  const t = useT();
  return (
    <div style={{ maxWidth: 860 }}>
      <div className="pagehead">
        <h1>{t("settings.title")}</h1>
        <span className="pagehead__sub">
          {t("settings.subtitle")}
        </span>
      </div>
      <SettingsNav />
      <TenantForm />
      <ProductsSection />
      <MembersSection />
      <InvitesSection />
    </div>
  );
}

/* --- company ----------------------------------------------------------------- */

function TenantForm() {
  const t = useT();
  const { me, refresh } = useSession();
  const tenant = useAsync(() => api.getTenant(), []);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [language, setLanguage] = useState<"en" | "pt-BR">("en");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const loaded = tenant.data?.tenant;
  useEffect(() => {
    if (loaded?.language) setLanguage(loaded.language);
  }, [loaded?.language]);

  const canEdit = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const current = loaded ?? null;
  const nameValue = loaded ? name || loaded.name : "";
  const slugValue = loaded ? slug || loaded.slug : "";

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      await api.updateTenant({ name: nameValue, slug: slugValue, language });
      await tenant.reload();
      await refresh();
      setSaved(true);
      setName("");
      setSlug("");
    } catch (err) {
      setError(translateTenant(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="section">
      <div className="section__head">
        <IconBuilding />
        <h2>{t("settings.company")}</h2>
        {!canEdit ? (
          <span className="tiny muted" style={{ marginLeft: "auto" }}>
            {t("settings.company.permission")}
          </span>
        ) : null}
      </div>
      <div className="section__body">
        {tenant.error ? (
          <ErrorBlock message={apiErrorMessage(tenant.error)} retry={() => void tenant.reload()} />
        ) : !current ? (
          <Skeleton h={70} />
        ) : (
          <form
            onSubmit={submit}
            style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12, alignItems: "end" }}
          >
            <Field label={t("settings.company.name")}>
              <input
                className="input"
                value={nameValue}
                disabled={!canEdit}
                onChange={(e) => setName(e.target.value)}
              />
            </Field>
            <Field label={t("settings.company.handle")} hint={t("settings.company.handleHint")}>
              <input
                className="input mono"
                value={slugValue}
                disabled={!canEdit}
                onChange={(e) => setSlug(e.target.value.toLowerCase())}
              />
            </Field>
            <Field label={t("settings.language")} hint={t("settings.languageHint")}>
              <select
                className="input"
                value={language}
                disabled={!canEdit}
                onChange={(e) => setLanguage(e.target.value as "en" | "pt-BR")}
              >
                <option value="en">{t("settings.language.english")}</option>
                <option value="pt-BR">{t("settings.language.portuguese")}</option>
              </select>
            </Field>
            <div style={{ gridColumn: "1 / -1", display: "flex", gap: 10, alignItems: "center" }}>
              <button type="submit" className="btn btn--primary" disabled={busy || !canEdit}>
                {busy ? t("settings.company.saving") : t("settings.company.save")}
              </button>
              {saved ? <span className="tiny" style={{ color: "var(--accent)" }}>{t("settings.company.saved")}</span> : null}
              {error ? <span className="tiny" style={{ color: "var(--danger)" }}>{error}</span> : null}
            </div>
          </form>
        )}
      </div>
    </div>
  );
}

/* --- products & projects -------------------------------------------------------- */

function ProductsSection() {
  const t = useT();
  const { me, refresh } = useSession();
  const { toasts, push } = useToasts();
  const confirm = useConfirm();
  const products = useAsync(
    () => api.listProducts().then((r) => r.products),
    [],
  );
  const projects = useAsync(
    () => api.listProjects().then((r) => r.projects),
    [],
  );
  const canEdit = me?.tenant.role !== "guest";
  const [productName, setProductName] = useState("");
  const [productSlug, setProductSlug] = useState("");
  const [repoUrl, setRepoUrl] = useState("");
  const [jiraKey, setJiraKey] = useState("");
  const [busyProduct, setBusyProduct] = useState(false);

  // Per-product project creation state: id → {name, key}
  const [projName, setProjName] = useState<Record<string, string>>({});
  const [projKey, setProjKey] = useState<Record<string, string>>({});

  async function createProduct(e: FormEvent) {
    e.preventDefault();
    setBusyProduct(true);
    try {
      await api.createProduct({
        name: productName.trim(),
        slug: productSlug.trim(),
        repo_url: repoUrl.trim() || undefined,
        jira_project_key: jiraKey.trim() || undefined,
      });
      setProductName("");
      setProductSlug("");
      setRepoUrl("");
      setJiraKey("");
      await products.reload();
      push("info", t("settings.products.created"));
    } catch (err) {
      push("error", apiErrorMessage(err));
    } finally {
      setBusyProduct(false);
    }
  }

  async function createProject(productId: string) {
    if (!projName[productId]?.trim()) return;
    try {
      await api.createProject({
        product_id: productId,
        name: projName[productId].trim(),
        key: projKey[productId]?.trim() || undefined,
      });
      setProjName((p) => ({ ...p, [productId]: "" }));
      setProjKey((p) => ({ ...p, [productId]: "" }));
      await projects.reload();
      push("info", t("settings.products.projectCreated"));
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  async function deleteProduct(id: string) {
    if (!await confirm({ title: t("settings.products.deleteProduct.title"), message: t("settings.products.deleteProduct.message"), confirmLabel: t("settings.products.delete"), destructive: true })) return;
    try {
      await api.deleteProduct(id);
      await products.reload();
      await refresh();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  async function deleteProject(id: string) {
    if (!await confirm({ title: t("settings.products.deleteProject.title"), message: t("settings.products.deleteProject.message"), confirmLabel: t("settings.products.delete"), destructive: true })) return;
    try {
      await api.deleteProject(id);
      await projects.reload();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  return (
    <div className="section">
      <div className="section__head">
        <h2>{t("settings.products.title")}</h2>
      </div>
      <div className="section__body" style={{ display: "grid", gap: 14 }}>
        {products.error ? (
          <ErrorBlock message={apiErrorMessage(products.error)} retry={() => void products.reload()} />
        ) : null}
        {!products.data ? (
          <Skeleton h={60} />
        ) : products.data.length === 0 ? (
          <p className="tiny muted" style={{ margin: 0 }}>
            {t("settings.products.empty")}
          </p>
        ) : (
          products.data.map((p) => {
            const ps = (projects.data ?? []).filter((x) => x.product_id === p.id);
            return (
              <div
                key={p.id}
                style={{
                  border: "1px solid var(--border)",
                  borderRadius: "var(--radius)",
                  padding: "10px 12px",
                  background: "var(--bg-raised)",
                }}
              >
                <div className="spread">
                  <div>
                    <span style={{ fontWeight: 600, color: "var(--text-strong)" }}>{p.name}</span>
                    <span className="mono tiny muted" style={{ marginLeft: 8 }}>
                      {p.slug}
                    </span>
                    {p.repo_url ? (
                      <a
                        href={p.repo_url}
                        target="_blank"
                        rel="noreferrer noopener"
                        className="tiny rowlink"
                        style={{ marginLeft: 10 }}
                      >
                        {t("settings.products.repo")}
                      </a>
                    ) : null}
                    {p.jira_project_key ? (
                      <span className="tiny muted" style={{ marginLeft: 10 }}>
                        Jira {p.jira_project_key}
                      </span>
                    ) : null}
                  </div>
                  {canEdit ? (
                    <button
                      type="button"
                      className="btn btn--sm btn--ghost btn--danger"
                      onClick={() => void deleteProduct(p.id)}
                    >
                      {t("settings.products.delete")}
                    </button>
                  ) : null}
                </div>
                <div className="tiny muted" style={{ marginTop: 8 }}>
                  {t("settings.products.projects")} {ps.length === 0 ? t("settings.products.none") : `(${ps.length})`}
                </div>
                <ul style={{ margin: "4px 0 8px", paddingLeft: 18 }}>
                  {ps.map((x) => (
                    <li key={x.id} className="stack tiny" style={{ justifyContent: "space-between" }}>
                      <span>
                        {x.name}
                        {x.key ? <span className="mono muted"> · {x.key}</span> : null}
                      </span>
                      {canEdit ? (
                      <button
                          type="button"
                          className="btn btn--sm btn--ghost btn--danger"
                          onClick={() => void deleteProject(x.id)}
                        >
                          {t("settings.products.delete")}
                        </button>
                      ) : null}
                    </li>
                  ))}
                </ul>
                {canEdit ? (
                  <div className="stack" style={{ flexWrap: "wrap" }}>
                    <input
                      className="input input--inline"
                      placeholder={t("settings.products.newProject")}
                      aria-label={t("settings.products.projectNameFor", { name: p.name })}
                      value={projName[p.id] ?? ""}
                      onChange={(e) =>
                        setProjName((m) => ({ ...m, [p.id]: e.target.value }))
                      }
                    />
                    <input
                      className="input input--inline mono"
                      placeholder="KEY"
                      aria-label={t("settings.products.projectKeyFor", { name: p.name })}
                      style={{ width: 90 }}
                      value={projKey[p.id] ?? ""}
                      onChange={(e) =>
                        setProjKey((m) => ({ ...m, [p.id]: e.target.value.toUpperCase() }))
                      }
                    />
                    <button
                      type="button"
                      className="btn btn--sm"
                      disabled={!projName[p.id]?.trim()}
                      onClick={() => void createProject(p.id)}
                    >
                      {t("settings.products.addProject")}
                    </button>
                  </div>
                ) : null}
              </div>
            );
          })
        )}
        {canEdit ? (
          <form
            onSubmit={createProduct}
            style={{
              display: "grid",
              gridTemplateColumns: "1fr 130px 1fr 120px auto",
              gap: 8,
              alignItems: "center",
              borderTop: "1px solid var(--border)",
              paddingTop: 12,
            }}
          >
            <input
              className="input input--inline"
              placeholder={t("settings.products.productName")}
              aria-label={t("settings.products.newProductName")}
              value={productName}
              onChange={(e) => setProductName(e.target.value)}
              required
            />
            <input
              className="input input--inline mono"
              placeholder={t("settings.products.slug")}
              aria-label={t("settings.products.newProductSlug")}
              value={productSlug}
              onChange={(e) =>
                setProductSlug(e.target.value.toLowerCase().replace(/[^a-z0-9]+/g, "-"))
              }
              required
            />
            <input
              className="input input--inline"
              placeholder={t("settings.products.repoUrl")}
              aria-label={t("settings.products.newRepoUrl")}
              value={repoUrl}
              onChange={(e) => setRepoUrl(e.target.value)}
            />
            <input
              className="input input--inline"
              placeholder={t("settings.products.jiraKey")}
              aria-label={t("settings.products.newJiraKey")}
              value={jiraKey}
              onChange={(e) => setJiraKey(e.target.value)}
            />
            <button
              type="submit"
              className="btn btn--sm btn--primary"
              disabled={busyProduct}
            >
              {busyProduct ? t("settings.products.adding") : t("settings.products.addProduct")}
            </button>
          </form>
        ) : null}
      </div>
      <Toasts toasts={toasts} />
    </div>
  );
}

/* --- members ------------------------------------------------------------------ */

function MembersSection() {
  const t = useT();
  const { me } = useSession();
  const members = useAsync(() => api.listMembers().then((r) => r.members), []);
  const { toasts, push } = useToasts();
  const canEdit = me?.tenant.role === "owner" || me?.tenant.role === "admin";

  async function setRole(m: Member, role: Role) {
    try {
      await api.setMemberRole(m.user_id, role);
      await members.reload();
    } catch (err) {
      push(
        "error",
        apiErrorMessage(err),
      );
    }
  }

  return (
    <div className="section">
      <div className="section__head">
        <h2>{t("settings.members.title")}</h2>
        <span className="tiny muted" style={{ marginLeft: "auto" }}>
          {t("settings.members.rolesHint", { roles: ROLES.map((role) => roleLabel(role, t)).join(" · ") })}
        </span>
      </div>
      <div className="section__body">
        {members.error ? (
          <ErrorBlock message={apiErrorMessage(members.error)} retry={() => void members.reload()} />
        ) : !members.data ? (
          <Skeleton h={90} />
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th scope="col">{t("settings.members.name")}</th>
                  <th scope="col">{t("settings.members.email")}</th>
                  <th scope="col">{t("settings.members.joined")}</th>
                  <th scope="col">{t("settings.members.role")}</th>
                </tr>
              </thead>
              <tbody>
                {members.data.map((m) => (
                  <tr key={m.user_id}>
                    <td style={{ fontWeight: 500 }}>
                      {m.name}
                      {m.user_id === me?.user.id ? (
                        <span className="tiny muted" style={{ marginLeft: 6 }}>
                          {t("settings.members.you")}
                        </span>
                      ) : null}
                    </td>
                    <td className="tiny">{m.email}</td>
                    <td className="tiny muted">{formatDate(m.joined_at)}</td>
                    <td>
                      {canEdit ? (
                        <select
                          className="input input--inline"
                          value={m.role}
                          aria-label={t("settings.members.roleOf", { name: m.name })}
                          onChange={(e) => void setRole(m, e.target.value as Role)}
                        >
                          {ROLES.map((r) => (
                            <option key={r} value={r}>
                              {roleLabel(r, t)}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <span className="badge-outline">{roleLabel(m.role, t)}</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {members.error === null && members.data && members.data.length === 1 ? (
          <p className="tiny muted" style={{ marginTop: 8 }}>
            {t("settings.members.only")}
          </p>
        ) : null}
      </div>
      <Toasts toasts={toasts} />
    </div>
  );
}

/* --- invites ------------------------------------------------------------------- */

interface CreatedInvite {
  email: string;
  role: Role;
  token: string;
  accept_url: string;
  expires_at: string;
}

function InvitesSection() {
  const t = useT();
  const invites = useAsync(() => api.listInvites().then((r) => r.invites), []);
  const { toasts, push } = useToasts();
  const { me } = useSession();
  const canEdit = me?.tenant.role === "owner" || me?.tenant.role === "admin";
  const [created, setCreated] = useState<CreatedInvite | null>(null);
  const [showCreate, setShowCreate] = useState(false);

  async function revoke(id: string) {
    try {
      await api.revokeInvite(id);
      await invites.reload();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  return (
    <div className="section">
      <div className="section__head">
        <h2>{t("settings.invites.title")}</h2>
        {canEdit ? (
          <button
            type="button"
            className="btn btn--sm"
            style={{ marginLeft: "auto" }}
            onClick={() => setShowCreate(true)}
          >
            {t("settings.invites.inviteSomeone")}
          </button>
        ) : null}
      </div>
      <div className="section__body">
        {created ? (
          <div
            className="panel section__body"
            role="alert"
            style={{
              background: "var(--theme-success-bg-subtle)",
              border: "1px solid var(--theme-success-border-subtle)",
              marginBottom: 12,
            }}
          >
            <p style={{ margin: "0 0 8px", fontWeight: 600, color: "var(--text-strong)" }}>
              {t("settings.invites.created", { email: created.email })}
            </p>
            <div className="stack" style={{ gap: 8, flexWrap: "wrap" }}>
              <code
                className="mono"
                style={{
                  background: "var(--bg-raised)",
                  padding: "6px 10px",
                  borderRadius: 6,
                  overflowWrap: "anywhere",
                }}
              >
                {created.token}
              </code>
              <CopyButton text={created.token} label={t("settings.invites.copyToken")} />
              <CopyButton
                text={`${window.location.origin}${created.accept_url}`}
                label={t("settings.invites.copyLink")}
              />
              <button
                type="button"
                className="btn btn--sm btn--ghost"
                onClick={() => setCreated(null)}
              >
                {t("settings.invites.done")}
              </button>
            </div>
            <p className="tiny muted" style={{ margin: "8px 0 0" }}>
              {t("settings.invites.expires", { date: formatDate(created.expires_at) })}
            </p>
          </div>
        ) : null}

        {invites.error ? (
          <ErrorBlock message={apiErrorMessage(invites.error)} retry={() => void invites.reload()} />
        ) : !invites.data ? (
          <Skeleton h={60} />
        ) : invites.data.length === 0 ? (
          <p className="tiny muted">{t("settings.invites.empty")}</p>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th scope="col">{t("settings.invites.email")}</th>
                  <th scope="col">{t("settings.members.role")}</th>
                  <th scope="col">{t("settings.invites.expiresHeader")}</th>
                  <th scope="col">{t("settings.invites.createdHeader")}</th>
                  <th scope="col">
                    <span className="visually-hidden">{t("settings.invites.actions")}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {invites.data.map((inv) => (
                  <tr key={inv.id}>
                    <td style={{ fontWeight: 500 }}>{inv.email}</td>
                    <td>
                      <span className="badge-outline">{roleLabel(inv.role, t)}</span>
                    </td>
                    <td className="tiny muted">{formatDate(inv.expires_at)}</td>
                    <td className="tiny muted">{formatDate(inv.created_at)}</td>
                    <td>
                      {canEdit ? (
                        <button
                          type="button"
                          className="btn btn--sm btn--danger"
                          onClick={() => void revoke(inv.id)}
                        >
                          {t("settings.invites.revoke")}
                        </button>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {showCreate ? (
        <CreateInviteDialog
          onClose={() => setShowCreate(false)}
          onCreated={(token, accept_url, inv) => {
            setShowCreate(false);
            setCreated({ ...inv, token, accept_url });
            void invites.reload();
          }}
        />
      ) : null}

      <Toasts toasts={toasts} />
    </div>
  );
}

function CreateInviteDialog(props: {
  onClose: () => void;
  onCreated: (
    token: string,
    accept_url: string,
    inv: { email: string; role: Role; expires_at: string },
  ) => void;
}) {
  const t = useT();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role | "">("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await api.createInvite({
        email: email.trim(),
        role: role || "member",
      });
      props.onCreated(res.token, res.accept_url, {
        email: res.invite.email,
        role: res.invite.role,
        expires_at: res.invite.expires_at,
      });
    } catch (err) {
      setError(translateInvite(err));
      setBusy(false);
    }
  }

  return (
    <Dialog
      title={t("settings.invites.dialogTitle")}
      description={t("settings.invites.dialogDescription")}
      onClose={props.onClose}
    >
      <form onSubmit={submit} style={{ display: "grid", gap: 12 }}>
        {error ? <div className="error-note">{error}</div> : null}
        <Field label={t("settings.invites.email")}>
          <input
            type="email"
            className="input"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoFocus
          />
        </Field>
        <Field
          label={t("settings.members.role")}
          hint={t("settings.invites.roleHint")}
        >
          <select
            className="input"
            value={role}
            onChange={(e) => setRole(e.target.value as Role | "")}
          >
            <option value="">{t("settings.invites.memberDefault")}</option>
            <option value="admin">{roleLabel("admin", t)}</option>
            <option value="member">{roleLabel("member", t)}</option>
            <option value="guest">{t("settings.invites.guestReadonly")}</option>
          </select>
        </Field>
        <div className="dialog__row">
          <button type="button" className="btn" onClick={props.onClose}>
            {t("settings.invites.cancel")}
          </button>
          <button type="submit" className="btn btn--primary" disabled={busy}>
            {busy ? t("settings.invites.creating") : t("settings.invites.create")}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

/* --- error messages ------------------------------------------------------------- */

function translateTenant(err: unknown): string {
  return apiErrorMessage(err);
}

function translateInvite(err: unknown): string {
  return apiErrorMessage(err);
}

function roleLabel(role: Role, t: ReturnType<typeof useT>): string {
  switch (role) {
    case "owner": return t("settings.members.owner");
    case "admin": return t("settings.members.admin");
    case "member": return t("settings.members.member");
    case "guest": return t("settings.members.guest");
  }
}
