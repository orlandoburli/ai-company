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

  // --- agents, chat threads, jobs (M2 contract) -----------------------------

  listAgents: () => get<{ agents: Agent[] }>("/api/agents"),

  createAgent: (b: AgentInput) => post<{ agent: Agent }>("/api/agents", b),

  getAgent: (id: string) => get<{ agent: Agent }>(`/api/agents/${id}`),

  updateAgent: (id: string, b: AgentInput) =>
    patch<{ agent: Agent }>(`/api/agents/${id}`, b),

  deleteAgent: (id: string) => del(`/api/agents/${id}`),

  /** Get-or-create the one thread attached to an item. */
  itemThread: (itemId: string) =>
    post<{ thread: Thread }>(`/api/items/${itemId}/thread`),

  getThread: (id: string) => get<{ thread: Thread }>(`/api/threads/${id}`),

  threadMessages: (threadId: string, afterSeq?: number, limit?: number) =>
    get<{ messages: Message[] }>(
      `/api/threads/${threadId}/messages` +
        query({ after_seq: afterSeq?.toString(), limit: limit?.toString() }),
    ),

  postThreadMessage: (threadId: string, body_md: string) =>
    post<{ message: Message; jobs: Job[] }>(
      `/api/threads/${threadId}/messages`,
      { body_md },
    ),

  threadJobs: (threadId: string, limit?: number) =>
    get<{ jobs: Job[] }>(
      `/api/threads/${threadId}/jobs` + query({ limit: limit?.toString() }),
    ),

  /** Manual "Run agent" — queues a job with the same context as a mention. */
  createThreadJob: (
    threadId: string,
    b: { agent_id: string; instructions?: string },
  ) =>
    post<{ job: Job; message: Message | null }>(
      `/api/threads/${threadId}/jobs`,
      b,
    ),

  getJob: (id: string) => get<{ job: Job }>(`/api/jobs/${id}`),

  jobLogs: (id: string, afterSeq?: number, limit?: number) =>
    get<{ logs: LogEntry[]; job: Job }>(
      `/api/jobs/${id}/logs` +
        query({ after_seq: afterSeq?.toString(), limit: limit?.toString() }),
    ),

  cancelJob: (id: string) => post<{ job: Job }>(`/api/jobs/${id}/cancel`),
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

// --- M2 types: agents, threads, messages, jobs ------------------------------

export type AgentKind = "cli" | "api";

/**
 * Opaque config object; the server validates it per `kind`
 * (cli: bin/args/output_format/timeout_sec, api: provider/model/base_url/
 * key_env/max_tokens). Secrets never live here — `key_env` names an
 * environment variable.
 */
export type AgentConfig = Record<string, unknown>;

export interface Agent {
  id: string;
  name: string;
  role: string;
  kind: AgentKind;
  config: AgentConfig;
  capabilities: AgentConfig;
  concurrency_limit: number;
  enabled: boolean;
  soul_md: string;
  skills_policy: AgentConfig;
  fallbacks: string[];
  created_at: string;
  updated_at: string;
}

/**
 * Body for POST /api/agents and PATCH /api/agents/{id}. House convention:
 * an absent field leaves it unchanged, "" clears a nullable string.
 */
export interface AgentInput {
  name?: string;
  role?: string;
  kind?: AgentKind;
  config?: AgentConfig;
  capabilities?: AgentConfig;
  concurrency_limit?: number;
  enabled?: boolean;
  soul_md?: string;
  skills_policy?: AgentConfig;
  fallbacks?: string[];
}

export interface Thread {
  id: string;
  /** Nullable: standalone threads may not point at an item (Go pointer). */
  item_id?: string | null;
  created_at: string;
  updated_at?: string;
}

export type MessageAuthorKind = "human" | "agent" | "system";

export interface Message {
  id: string;
  thread_id: string;
  seq: number;
  author_kind: MessageAuthorKind;
  author_user_id?: string;
  author_agent_id?: string;
  body_md: string;
  created_at: string;
}

export type JobState =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled";

export interface Job {
  id: string;
  /** Go structs are pointers and can emit null. */
  thread_id: string | null;
  agent_id?: string | null;
  state: JobState;
  error?: string | null;
  instructions?: string | null;
  /** Set once the job's reply message exists; null until then. */
  message_id?: string | null;
  created_at: string;
  started_at?: string | null;
  finished_at?: string | null;
}

export interface LogEntry {
  seq: number;
  job_id?: string;
  stream?: string;
  level?: string;
  kind?: string;
  message: string;
  ts?: string;
}

/** True once a job is past its terminal transition. */
export function isTerminalJobState(state: JobState): boolean {
  return state === "succeeded" || state === "failed" || state === "cancelled";
}

/* --- job event stream (SSE) ------------------------------------------------- */

export interface JobStreamHandlers {
  onLog: (entry: LogEntry) => void;
  onState: (job: Job) => void;
  onDone: () => void;
  /** EventSource failed; per the contract the caller falls back to polling. */
  onError: () => void;
}

/**
 * Opens the text/event-stream at /api/jobs/{id}/events?after_seq=N and parses
 * the `log` / `state` / `done` frames. Same-origin, so the session cookie is
 * sent implicitly. Returns a close function.
 */
export function openJobEvents(
  jobId: string,
  afterSeq: number,
  h: JobStreamHandlers,
): () => void {
  const es = new EventSource(
    `/api/jobs/${jobId}/events?after_seq=${encodeURIComponent(String(afterSeq))}`,
  );

  es.addEventListener("log", (e: MessageEvent<string>) => {
    const entry = parseFrame<LogEntry>(e.data);
    if (entry) h.onLog(entry);
  });
  es.addEventListener("state", (e: MessageEvent<string>) => {
    const job = parseFrame<{ job: Job }>(e.data)?.job;
    if (job) h.onState(job);
  });
  es.addEventListener("done", () => h.onDone());
  es.addEventListener("error", () => {
    // Stop the EventSource (it would otherwise auto-reconnect) and let the
    // caller fall back to polling /logs + /jobs.
    es.close();
    h.onError();
  });

  return () => es.close();
}

function parseFrame<T>(data: string): T | null {
  try {
    return JSON.parse(data) as T;
  } catch {
    // A malformed frame is not an error envelope; skip it.
    return null;
  }
}
