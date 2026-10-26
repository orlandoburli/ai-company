"use client";

import { FormEvent, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { useAsync, useToasts } from "@/lib/hooks";
import { useConfirm } from "@/lib/confirm";
import { useSession } from "@/lib/session";
import type { Member, Product, Project, Role } from "@/lib/types";
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

/** The values a product is written with. */
interface ProductFields {
  name: string;
  slug: string;
  repo: string;
  jira: string;
}

/**
 * The form a product is written in — the same one creates a product and
 * edits one. The repository is the product's link to GitHub, so it gets a
 * label of its own and says what it is for; the slug is the product's key,
 * so it is normalised the way the company names things.
 */
function ProductForm(props: {
  fields: ProductFields;
  onChange: (fields: ProductFields) => void;
  onSubmit: () => void;
  busy: boolean;
  submitLabel: string;
  busyLabel: string;
  cancelLabel?: string;
  onCancel?: () => void;
  /** Makes the field ids unique when two forms share the page. */
  idPrefix: string;
}) {
  const t = useT();
  const { fields } = props;
  const set = (patch: Partial<ProductFields>) => props.onChange({ ...fields, ...patch });

  return (
    <form
      className="product-form"
      onSubmit={(e) => {
        e.preventDefault();
        props.onSubmit();
      }}
    >
      <div className="field">
        <label htmlFor={`${props.idPrefix}-name`}>{t("settings.products.productName")}</label>
        <input
          id={`${props.idPrefix}-name`}
          className="input"
          value={fields.name}
          onChange={(e) => set({ name: e.target.value })}
          required
        />
      </div>
      <div className="field">
        <label htmlFor={`${props.idPrefix}-slug`}>{t("settings.products.slugLabel")}</label>
        <input
          id={`${props.idPrefix}-slug`}
          className="input mono"
          value={fields.slug}
          onChange={(e) => set({ slug: e.target.value.toLowerCase().replace(/[^a-z0-9]+/g, "-") })}
          required
        />
        <span className="tiny muted">{t("settings.products.slugHint")}</span>
      </div>
      <div className="field product-form__wide">
        <label htmlFor={`${props.idPrefix}-repo`}>{t("settings.products.repoLabel")}</label>
        <input
          id={`${props.idPrefix}-repo`}
          className="input mono"
          value={fields.repo}
          onChange={(e) => set({ repo: e.target.value })}
          placeholder={t("settings.products.repoPlaceholder")}
        />
        <span className="tiny muted">{t("settings.products.repoHint")}</span>
      </div>
      <div className="field">
        <label htmlFor={`${props.idPrefix}-jira`}>{t("settings.products.jiraLabel")}</label>
        <input
          id={`${props.idPrefix}-jira`}
          className="input mono"
          value={fields.jira}
          onChange={(e) => set({ jira: e.target.value })}
        />
        <span className="tiny muted">{t("settings.products.jiraHint")}</span>
      </div>
      <div className="product-form__actions">
        {props.onCancel ? (
          <button type="button" className="btn" onClick={props.onCancel}>
            {props.cancelLabel ?? t("settings.products.cancel")}
          </button>
        ) : null}
        <button type="submit" className="btn btn--primary" disabled={props.busy}>
          {props.busy ? props.busyLabel : props.submitLabel}
        </button>
      </div>
    </form>
  );
}

/**
 * The dialog that edits a product. It opens the same form the new-product
 * form uses, filled with the product's own values, and saves through the
 * update endpoint — only the fields that changed are sent, so an untouched
 * repository is never rewritten.
 */
function EditProductDialog(props: {
  product: Product;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT();
  const confirm = useConfirm();
  const product = props.product;
  const [fields, setFields] = useState<ProductFields>({
    name: product.name,
    slug: product.slug,
    repo: product.repo_url ?? "",
    jira: product.jira_project_key ?? "",
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save() {
    if (busy) return;
    const next = {
      name: fields.name.trim(),
      slug: fields.slug.trim(),
      repo: fields.repo.trim(),
      jira: fields.jira.trim(),
    };
    if (!next.name || !next.slug) {
      setError(t("settings.products.required"));
      return;
    }
    setBusy(true);
    setError(null);
    // The slug is the product's key. Renaming it renames the product, so the
    // person is told what that means before it happens.
    if (next.slug !== product.slug) {
      const ok = await confirm({
        title: t("settings.products.slugChange.title"),
        message: t("settings.products.slugChange.message"),
        confirmLabel: t("settings.products.saveChanges"),
      });
      if (!ok) {
        setBusy(false);
        return;
      }
    }
    // Only what changed is sent: the endpoint leaves an absent field alone,
    // so an untouched repository is never rewritten.
    const body: {
      name?: string;
      slug?: string;
      repo_url?: string;
      jira_project_key?: string;
    } = {};
    if (next.name !== product.name) body.name = next.name;
    if (next.slug !== product.slug) body.slug = next.slug;
    if (next.repo !== (product.repo_url ?? "")) body.repo_url = next.repo;
    if (next.jira !== (product.jira_project_key ?? "")) body.jira_project_key = next.jira;
    if (Object.keys(body).length === 0) {
      props.onClose();
      return;
    }
    try {
      await api.updateProduct(product.id, body);
      props.onSaved();
    } catch (err) {
      setError(productErrorMessage(err, next.slug !== product.slug, t));
      setBusy(false);
    }
  }

  return (
    <Dialog
      title={t("settings.products.editTitle", { name: product.name })}
      description={t("settings.products.editDescription")}
      onClose={props.onClose}
      width={560}
    >
      {error ? (
        <div className="error-note" role="alert" style={{ marginBottom: 12 }}>
          {error}
        </div>
      ) : null}
      <ProductForm
        fields={fields}
        onChange={setFields}
        onSubmit={() => void save()}
        busy={busy}
        submitLabel={t("settings.products.saveChanges")}
        busyLabel={t("settings.products.saving")}
        cancelLabel={t("settings.products.cancel")}
        onCancel={props.onClose}
        idPrefix={`edit-${product.id}`}
      />
    </Dialog>
  );
}

/** A project and the projects that sit under it. */
interface ProjectBranch {
  project: Project;
  children: ProjectBranch[];
}

/**
 * The projects of a product, arranged by the parent each one names. A project
 * whose parent is not among them stays at the top: it is still a project of
 * this product, and hiding it would lose work.
 */
function buildProjectTree(projects: Project[]): ProjectBranch[] {
  const known = new Set(projects.map((project) => project.id));
  const under = new Map<string, Project[]>();
  const roots: Project[] = [];
  for (const project of projects) {
    const parent = project.parent_id;
    if (parent && known.has(parent)) {
      const list = under.get(parent) ?? [];
      list.push(project);
      under.set(parent, list);
    } else {
      roots.push(project);
    }
  }
  // The walk remembers where it has been: a parent chain that loops (the
  // server refuses to write one, but old rows may carry one) must not spin.
  const branch = (project: Project, seen: Set<string>): ProjectBranch => {
    const next = new Set(seen).add(project.id);
    const children = (under.get(project.id) ?? []).filter((child) => !seen.has(child.id));
    return { project, children: children.map((child) => branch(child, next)) };
  };
  // A loop leaves no root at all; every project then stays at the top rather
  // than disappearing from the list.
  const top = roots.length ? roots : projects;
  return top.map((project) => branch(project, new Set()));
}

/** The small name-and-key form a project — or a subproject — is created with. */
function NewProjectForm(props: {
  namePlaceholder: string;
  nameLabel: string;
  keyLabel: string;
  submitLabel: string;
  onSubmit: (name: string, key: string) => void | Promise<void>;
}) {
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!name.trim() || busy) return;
    setBusy(true);
    try {
      await props.onSubmit(name.trim(), key.trim());
      setName("");
      setKey("");
    } catch {
      /* the caller reports the failure; the form keeps what was typed */
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="stack project-form" onSubmit={submit} style={{ flexWrap: "wrap" }}>
      <input
        className="input input--inline"
        placeholder={props.namePlaceholder}
        aria-label={props.nameLabel}
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      <input
        className="input input--inline mono"
        placeholder="KEY"
        aria-label={props.keyLabel}
        style={{ width: 90 }}
        value={key}
        onChange={(e) => setKey(e.target.value.toUpperCase())}
      />
      <button type="submit" className="btn btn--sm" disabled={busy || !name.trim()}>
        {props.submitLabel}
      </button>
    </form>
  );
}

/** One project in the tree, with the projects that sit under it. */
function ProjectRow(props: {
  branch: ProjectBranch;
  depth: number;
  canEdit: boolean;
  onCreate: (parent: Project, name: string, key: string) => void | Promise<void>;
  onDelete: (project: Project) => void | Promise<void>;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const project = props.branch.project;
  return (
    <li className="project-row">
      <div className="spread project-row__line">
        <span className="project-row__name">
          {props.depth > 0 ? (
            <span className="project-row__elbow" aria-hidden>
              ↳
            </span>
          ) : null}
          {project.name}
          {project.key ? <span className="mono muted"> · {project.key}</span> : null}
        </span>
        {props.canEdit ? (
          <span className="stack project-row__actions">
            <button
              type="button"
              className="btn btn--sm btn--ghost"
              aria-expanded={open}
              onClick={() => setOpen((value) => !value)}
            >
              {t("settings.products.newSubproject")}
            </button>
            <button
              type="button"
              className="btn btn--sm btn--ghost btn--danger"
              onClick={() => props.onDelete(project)}
            >
              {t("settings.products.delete")}
            </button>
          </span>
        ) : null}
      </div>
      {open && props.canEdit ? (
        <NewProjectForm
          namePlaceholder={t("settings.products.newSubproject")}
          nameLabel={t("settings.products.subprojectNameFor", { name: project.name })}
          keyLabel={t("settings.products.subprojectKeyFor", { name: project.name })}
          submitLabel={t("settings.products.addSubproject")}
          onSubmit={(name, key) => props.onCreate(project, name, key)}
        />
      ) : null}
      {props.branch.children.length ? (
        <ul className="project-tree project-tree--nested">
          {props.branch.children.map((child) => (
            <ProjectRow
              key={child.project.id}
              branch={child}
              depth={props.depth + 1}
              canEdit={props.canEdit}
              onCreate={props.onCreate}
              onDelete={props.onDelete}
            />
          ))}
        </ul>
      ) : null}
    </li>
  );
}

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
  const [productFields, setProductFields] = useState<ProductFields>({
    name: "",
    slug: "",
    repo: "",
    jira: "",
  });
  const [busyProduct, setBusyProduct] = useState(false);
  const [editing, setEditing] = useState<Product | null>(null);

  async function createProduct() {
    if (busyProduct) return;
    const name = productFields.name.trim();
    const slug = productFields.slug.trim();
    if (!name || !slug) {
      push("error", t("settings.products.required"));
      return;
    }
    setBusyProduct(true);
    try {
      await api.createProduct({
        name,
        slug,
        repo_url: productFields.repo.trim() || undefined,
        jira_project_key: productFields.jira.trim() || undefined,
      });
      setProductFields({ name: "", slug: "", repo: "", jira: "" });
      await products.reload();
      push("info", t("settings.products.created"));
    } catch (err) {
      push("error", apiErrorMessage(err));
    } finally {
      setBusyProduct(false);
    }
  }

  /** A project of the product: a first-level one, with no parent. */
  async function createProject(productId: string, name: string, key: string) {
    try {
      await api.createProject({
        product_id: productId,
        name,
        key: key || undefined,
      });
      await projects.reload();
      push("info", t("settings.products.projectCreated"));
    } catch (err) {
      push("error", apiErrorMessage(err));
    }
  }

  /** A project under another one: the parent decides where it belongs. */
  async function createSubproject(parent: Project, name: string, key: string) {
    try {
      await api.createProject({
        product_id: parent.product_id,
        parent_id: parent.id,
        name,
        key: key || undefined,
      });
      await projects.reload();
      push("info", t("settings.products.subprojectCreated"));
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
            const tree = buildProjectTree(ps);
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
                    <span className="stack" style={{ gap: 6 }}>
                      <button
                        type="button"
                        className="btn btn--sm btn--ghost"
                        onClick={() => setEditing(p)}
                      >
                        {t("settings.products.edit")}
                      </button>
                      <button
                        type="button"
                        className="btn btn--sm btn--ghost btn--danger"
                        onClick={() => void deleteProduct(p.id)}
                      >
                        {t("settings.products.delete")}
                      </button>
                    </span>
                  ) : null}
                </div>
                {!p.repo_url ? (
                  <p className="tiny muted" style={{ margin: "6px 0 0" }}>
                    {t("settings.products.noRepository")}
                  </p>
                ) : null}
                <div className="tiny muted" style={{ marginTop: 8 }}>
                  {t("settings.products.projects")} {ps.length === 0 ? t("settings.products.none") : `(${ps.length})`}
                </div>
                {tree.length ? (
                  <ul className="project-tree">
                    {tree.map((branch) => (
                      <ProjectRow
                        key={branch.project.id}
                        branch={branch}
                        depth={0}
                        canEdit={canEdit}
                        onCreate={createSubproject}
                        onDelete={(project) => deleteProject(project.id)}
                      />
                    ))}
                  </ul>
                ) : null}
                {canEdit ? (
                  <p className="tiny muted project-nesting-hint">{t("settings.products.nestingHint")}</p>
                ) : null}
                {canEdit ? (
                  <NewProjectForm
                    namePlaceholder={t("settings.products.newProject")}
                    nameLabel={t("settings.products.projectNameFor", { name: p.name })}
                    keyLabel={t("settings.products.projectKeyFor", { name: p.name })}
                    submitLabel={t("settings.products.addProject")}
                    onSubmit={(name, key) => createProject(p.id, name, key)}
                  />
                ) : null}
              </div>
            );
          })
        )}
        {canEdit ? (
          <div className="product-create">
            <p className="product-create__title">{t("settings.products.new")}</p>
            <ProductForm
              fields={productFields}
              onChange={setProductFields}
              onSubmit={() => void createProduct()}
              busy={busyProduct}
              submitLabel={t("settings.products.addProduct")}
              busyLabel={t("settings.products.adding")}
              idPrefix="new-product"
            />
          </div>
        ) : null}
      </div>
      {editing ? (
        <EditProductDialog
          product={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void products.reload();
            push("info", t("settings.products.updated"));
          }}
        />
      ) : null}
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

/**
 * The message a failed product edit shows. The failure this form can cause
 * with its own meaning — a key another product already holds — is named
 * rather than swallowed by a generic sentence.
 */
function productErrorMessage(err: unknown, slugChanged: boolean, t: ReturnType<typeof useT>): string {
  if (err instanceof ApiError && err.code === "conflict" && slugChanged) {
    return t("settings.products.slugTaken");
  }
  return apiErrorMessage(err);
}

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
