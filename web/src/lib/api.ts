import type {
  ActivityEntry,
  ApiErrorBody,
  BoardColumn,
  CreateInviteResult,
  DependencyEdge,
  Invite,
  InvitePeek,
  Item,
  ItemScore,
  MeResponse,
  Member,
  Product,
  Project,
  ProjectNode,
  QueueEntry,
  Role,
  Status,
} from "./types";

/**
 * Thin typed client for the Go REST API. All requests go to the same origin
 * (the Next dev server proxies /api/* to the backend), so the session cookie
 * is sent implicitly and never read from JS.
 */

export class ApiError extends Error {
  code: string;
  status: number;
  /** Extra payload carried alongside some errors, e.g. login's tenants list. */
  data: unknown;

  constructor(status: number, code: string, message: string, data?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.data = data;
  }
}

type Method = "GET" | "POST" | "PATCH" | "PUT" | "DELETE";

async function request<T>(
  method: Method,
  path: string,
  body?: unknown,
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      credentials: "include",
      headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      cache: "no-store",
    });
  } catch {
    throw new ApiError(0, "network", "Cannot reach the server. Is the backend running?");
  }

  if (res.status === 204) {
    return undefined as T;
  }

  let data: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }

  if (!res.ok) {
    const parsed = data as ApiErrorBody | null;
    const err = parsed?.error;
    throw new ApiError(
      res.status,
      err?.code ?? "unknown",
      err?.message ?? "Something went wrong.",
      parsed,
    );
  }

  return data as T;
}

const get = <T,>(path: string) => request<T>("GET", path);
const post = <T,>(path: string, body?: unknown) => request<T>("POST", path, body);
const patch = <T,>(path: string, body?: unknown) => request<T>("PATCH", path, body);
const del = (path: string) => request<void>("DELETE", path);

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v) q.set(k, v);
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

// --- auth ------------------------------------------------------------------

export const api = {
  register: (b: {
    email: string;
    password: string;
    name: string;
    tenant_name: string;
    tenant_slug: string;
  }) => post<MeResponse>("/api/auth/register", b),

  login: (b: { email: string; password: string; tenant_id?: string }) =>
    post<MeResponse>("/api/auth/login", b),

  logout: () => post<void>("/api/auth/logout"),

  me: () => get<MeResponse>("/api/auth/me"),

  switchTenant: (tenantId: string) =>
    post<MeResponse>("/api/auth/switch-tenant", { tenant_id: tenantId }),

  peekInvite: (token: string) =>
    get<InvitePeek>("/api/auth/invite" + query({ token })),

  acceptInvite: (b: { token: string; password: string; name: string }) =>
    post<MeResponse>("/api/auth/accept-invite", b),

  // --- tenant ---------------------------------------------------------------

  getTenant: () => get<{ tenant: MeResponse["tenant"] }>("/api/tenant"),

  updateTenant: (b: { name?: string; slug?: string }) =>
    patch<{ tenant: MeResponse["tenant"] }>("/api/tenant", b),

  listMembers: () => get<{ members: Member[] }>("/api/tenant/members"),

  setMemberRole: (userId: string, role: Role) =>
    patch<{ user_id: string; role: Role }>(`/api/tenant/members/${userId}`, { role }),

  listInvites: () => get<{ invites: Invite[] }>("/api/invites"),

  createInvite: (b: { email: string; role: string }) =>
    post<CreateInviteResult>("/api/invites", b),

  revokeInvite: (id: string) => del(`/api/invites/${id}`),

  // --- work -----------------------------------------------------------------

  listProducts: () => get<{ products: Product[] }>("/api/products"),

  createProduct: (b: {
    name: string;
    slug: string;
    repo_url?: string;
    jira_project_key?: string;
  }) => post<{ product: Product }>("/api/products", b),

  deleteProduct: (id: string) => del(`/api/products/${id}`),

  listProjects: (productId?: string) =>
    get<{ projects: Project[] }>("/api/projects" + query({ product_id: productId })),

  createProject: (b: {
    product_id: string;
    parent_id?: string;
    name: string;
    key?: string;
  }) => post<{ project: Project }>("/api/projects", b),

  deleteProject: (id: string) => del(`/api/projects/${id}`),

  tree: (productId?: string) =>
    get<{ projects: ProjectNode[] }>("/api/tree" + query({ product_id: productId })),

  listItems: (filter?: ListItemsFilter) =>
    get<{ items: Item[] }>("/api/items" + query({ ...filter })),

  createItem: (b: Partial<Item> & { project_id: string; kind: string; title: string }) =>
    post<{ item: Item }>("/api/items", b),

  getItem: (id: string) =>
    get<{ item: Item; blockers: DependencyEdge[]; dependents: DependencyEdge[] }>(
      `/api/items/${id}`,
    ),

  patchItem: (id: string, b: PatchItemBody) => patch<{ item: Item }>(`/api/items/${id}`, b),

  deleteItem: (id: string) => del(`/api/items/${id}`),

  bulkStatus: (ids: string[], status: Status) =>
    post<unknown>("/api/items/bulk-status", { ids, status }),

  addDependency: (id: string, blockedById: string) =>
    post<{ dependency: DependencyEdge }>(`/api/items/${id}/dependencies`, {
      blocked_by_id: blockedById,
    }),

  removeDependency: (id: string, depId: string) =>
    del(`/api/items/${id}/dependencies/${depId}`),

  activity: (id: string) =>
    get<{ activity: ActivityEntry[] }>(`/api/items/${id}/activity`),

  score: (id: string) => get<ItemScore>(`/api/items/${id}/score`),

  pauseItem: (id: string, reason?: string) =>
    post<{ item: Item }>(`/api/items/${id}/pause`, reason ? { reason } : {}),

  unpauseItem: (id: string) => post<{ item: Item }>(`/api/items/${id}/unpause`),

  pinItem: (id: string) => post<{ item: Item }>(`/api/items/${id}/pin`),

  unpinItem: (id: string) => post<{ item: Item }>(`/api/items/${id}/unpin`),

  queue: (projectId?: string) =>
    get<{ queue: QueueEntry[]; weights_version: string }>(
      "/api/queue" + query({ project_id: projectId }),
    ),

  board: (filter?: ListItemsFilter) =>
    get<{ columns: BoardColumn[] }>("/api/board" + query({ ...filter })),
};

/** Filters accepted by /api/items and /api/board. */
export interface ListItemsFilter {
  project_id?: string;
  status?: string;
  label?: string;
  assignee_user_id?: string;
}

type PatchItemBody = {
  title?: string;
  description_md?: string;
  kind?: string;
  status?: Status;
  priority?: string;
  parent_id?: string;
  due_date?: string;
  sort_order?: number;
  labels?: string[];
  assignee_kind?: string;
  assignee_user_id?: string;
  github_issue_url?: string;
  jira_issue_key?: string;
  external_url?: string;
};

export type { PatchItemBody };
