"use client";

import { FormEvent, useState } from "react";
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

const ROLES: Role[] = ["owner", "admin", "member", "guest"];

export default function SettingsPage() {
  return (
    <div style={{ maxWidth: 860 }}>
      <div className="pagehead">
        <h1>Settings</h1>
        <span className="pagehead__sub">
          Company, products, members and invitations. Some actions need owner or admin.
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
  const { me, refresh } = useSession();
  const tenant = useAsync(() => api.getTenant(), []);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const loaded = tenant.data?.tenant;

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
      await api.updateTenant({ name: nameValue, slug: slugValue });
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
        <h2>Company</h2>
        {!canEdit ? (
          <span className="tiny muted" style={{ marginLeft: "auto" }}>
            Only the owner or an admin can change this.
          </span>
        ) : null}
      </div>
      <div className="section__body">
        {tenant.error ? (
          <ErrorBlock message={tenant.error.message} retry={() => void tenant.reload()} />
        ) : !current ? (
          <Skeleton h={70} />
        ) : (
          <form
            onSubmit={submit}
            style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12, alignItems: "end" }}
          >
            <Field label="Company name">
              <input
                className="input"
                value={nameValue}
                disabled={!canEdit}
                onChange={(e) => setName(e.target.value)}
              />
            </Field>
            <Field label="Handle (slug)" hint="3–63 chars: lowercase letters, digits, hyphens.">
              <input
                className="input mono"
                value={slugValue}
                disabled={!canEdit}
                onChange={(e) => setSlug(e.target.value.toLowerCase())}
              />
            </Field>
            <div style={{ gridColumn: "1 / -1", display: "flex", gap: 10, alignItems: "center" }}>
              <button type="submit" className="btn btn--primary" disabled={busy || !canEdit}>
                {busy ? "Saving…" : "Save changes"}
              </button>
              {saved ? <span className="tiny" style={{ color: "var(--accent)" }}>Saved.</span> : null}
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
      push("info", "Product created.");
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
      push("info", "Project created.");
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  async function deleteProduct(id: string) {
    if (!await confirm({ title: "Delete product?", message: "Delete this product? Empty it first — products with projects cannot be deleted.", confirmLabel: "Delete", destructive: true })) return;
    try {
      await api.deleteProduct(id);
      await products.reload();
      await refresh();
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  async function deleteProject(id: string) {
    if (!await confirm({ title: "Delete project?", message: "Delete this project? It must have no items or sub-projects.", confirmLabel: "Delete", destructive: true })) return;
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
        <h2>Products &amp; projects</h2>
      </div>
      <div className="section__body" style={{ display: "grid", gap: 14 }}>
        {products.error ? (
          <ErrorBlock message={products.error.message} retry={() => void products.reload()} />
        ) : null}
        {!products.data ? (
          <Skeleton h={60} />
        ) : products.data.length === 0 ? (
          <p className="tiny muted" style={{ margin: 0 }}>
            No products yet. A product groups the work for one surface of the
            business (for example an ERP or an online store).
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
                        repo
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
                      Delete
                    </button>
                  ) : null}
                </div>
                <div className="tiny muted" style={{ marginTop: 8 }}>
                  Projects {ps.length === 0 ? "— none yet" : `(${ps.length})`}
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
                          Delete
                        </button>
                      ) : null}
                    </li>
                  ))}
                </ul>
                {canEdit ? (
                  <div className="stack" style={{ flexWrap: "wrap" }}>
                    <input
                      className="input input--inline"
                      placeholder="New project name"
                      aria-label={`New project name for ${p.name}`}
                      value={projName[p.id] ?? ""}
                      onChange={(e) =>
                        setProjName((m) => ({ ...m, [p.id]: e.target.value }))
                      }
                    />
                    <input
                      className="input input--inline mono"
                      placeholder="KEY"
                      aria-label={`Project key for ${p.name}`}
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
                      Add project
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
              placeholder="Product name"
              aria-label="New product name"
              value={productName}
              onChange={(e) => setProductName(e.target.value)}
              required
            />
            <input
              className="input input--inline mono"
              placeholder="slug"
              aria-label="New product slug"
              value={productSlug}
              onChange={(e) =>
                setProductSlug(e.target.value.toLowerCase().replace(/[^a-z0-9]+/g, "-"))
              }
              required
            />
            <input
              className="input input--inline"
              placeholder="Repo URL (optional)"
              aria-label="New product repo URL"
              value={repoUrl}
              onChange={(e) => setRepoUrl(e.target.value)}
            />
            <input
              className="input input--inline"
              placeholder="Jira key (opt.)"
              aria-label="New product Jira key"
              value={jiraKey}
              onChange={(e) => setJiraKey(e.target.value)}
            />
            <button
              type="submit"
              className="btn btn--sm btn--primary"
              disabled={busyProduct}
            >
              {busyProduct ? "Adding…" : "Add product"}
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
        <h2>Members</h2>
        <span className="tiny muted" style={{ marginLeft: "auto" }}>
          {ROLES.join(" · ")} — guests can read everything but change nothing.
        </span>
      </div>
      <div className="section__body">
        {members.error ? (
          <ErrorBlock message={members.error.message} retry={() => void members.reload()} />
        ) : !members.data ? (
          <Skeleton h={90} />
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th scope="col">Name</th>
                  <th scope="col">Email</th>
                  <th scope="col">Joined</th>
                  <th scope="col">Role</th>
                </tr>
              </thead>
              <tbody>
                {members.data.map((m) => (
                  <tr key={m.user_id}>
                    <td style={{ fontWeight: 500 }}>
                      {m.name}
                      {m.user_id === me?.user.id ? (
                        <span className="tiny muted" style={{ marginLeft: 6 }}>
                          (you)
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
                          aria-label={`Role of ${m.name}`}
                          onChange={(e) => void setRole(m, e.target.value as Role)}
                        >
                          {ROLES.map((r) => (
                            <option key={r} value={r}>
                              {r}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <span className="badge-outline">{m.role}</span>
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
            You are the only member. Invite teammates below — the invitation link
            is created once.
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
        <h2>Invitations</h2>
        {canEdit ? (
          <button
            type="button"
            className="btn btn--sm"
            style={{ marginLeft: "auto" }}
            onClick={() => setShowCreate(true)}
          >
            Invite someone
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
              Invitation for {created.email} created. The token is shown once —
              copy it now, it cannot be retrieved again.
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
              <CopyButton text={created.token} label="Copy token" />
              <CopyButton
                text={`${window.location.origin}${created.accept_url}`}
                label="Copy invite link"
              />
              <button
                type="button"
                className="btn btn--sm btn--ghost"
                onClick={() => setCreated(null)}
              >
                Done
              </button>
            </div>
            <p className="tiny muted" style={{ margin: "8px 0 0" }}>
              Expires {formatDate(created.expires_at)}. Single use — accepting it
              consumes the token.
            </p>
          </div>
        ) : null}

        {invites.error ? (
          <ErrorBlock message={invites.error.message} retry={() => void invites.reload()} />
        ) : !invites.data ? (
          <Skeleton h={60} />
        ) : invites.data.length === 0 ? (
          <p className="tiny muted">No pending invitations.</p>
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th scope="col">Email</th>
                  <th scope="col">Role</th>
                  <th scope="col">Expires</th>
                  <th scope="col">Created</th>
                  <th scope="col">
                    <span className="visually-hidden">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {invites.data.map((inv) => (
                  <tr key={inv.id}>
                    <td style={{ fontWeight: 500 }}>{inv.email}</td>
                    <td>
                      <span className="badge-outline">{inv.role}</span>
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
                          Revoke
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
      title="Invite someone"
      description={'They get a link with a one-time token. No email is sent — you pass the link on.'}
      onClose={props.onClose}
    >
      <form onSubmit={submit} style={{ display: "grid", gap: 12 }}>
        {error ? <div className="error-note">{error}</div> : null}
        <Field label="Email">
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
          label="Role"
          hint="Admin can manage settings and people. Guest is read-only."
        >
          <select
            className="input"
            value={role}
            onChange={(e) => setRole(e.target.value as Role | "")}
          >
            <option value="">Member (default)</option>
            <option value="admin">Admin</option>
            <option value="member">Member</option>
            <option value="guest">Guest (read-only)</option>
          </select>
        </Field>
        <div className="dialog__row">
          <button type="button" className="btn" onClick={props.onClose}>
            Cancel
          </button>
          <button type="submit" className="btn btn--primary" disabled={busy}>
            {busy ? "Creating…" : "Create invitation"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

/* --- error messages ------------------------------------------------------------- */

function translateTenant(err: unknown): string {
  if (err instanceof Error && "code" in err) {
    switch ((err as { code: string }).code) {
      case "invalid_tenant_slug":
        return "The handle must be 3–63 characters: lowercase letters, digits and hyphens.";
      case "conflict":
        return "That handle is already in use.";
      case "forbidden":
        return "Only the owner or an admin can change company settings.";
      default:
        return err.message;
    }
  }
  return "Could not save. Try again.";
}

function translateInvite(err: unknown): string {
  if (err instanceof Error && "code" in err) {
    switch ((err as { code: string }).code) {
      case "invalid_email":
        return "Enter a valid email address.";
      case "already_member":
        return "That person is already a member of this company.";
      case "already_invited":
        return "There is already a pending invitation for that email.";
      case "forbidden":
        return "Your role does not allow inviting people.";
      default:
        return err.message;
    }
  }
  return "Could not create the invitation.";
}
