import type {
  ActivityEntry,
  ApiErrorBody,
  BoardColumn,
  CreateInviteResult,
  DependencyEdge,
  FailureState,
  Invite,
  InvitePeek,
  Item,
  ItemScore,
  MeResponse,
  Member,
  Product,
  Project,
  ProjectNode,
  QueueItem,
  RankingEntry,
  RankingRun,
  Role,
  Status,
} from "./types";

export type UsageBucket = {
  product_id: string;
  product_name: string;
  jobs: number;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  /** Go time.Duration encoded as nanoseconds. */
  wall_time: number;
  failures: number;
};

export type JobUsage = {
  id: string;
  agent_id: string;
  agent: string;
  step: string;
  state: string;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  /** Go time.Duration encoded as nanoseconds. */
  wall_time: number;
  failure: boolean;
};

export type PushSubscription = {
  id: string;
  tenant_id: string;
  user_id: string;
  endpoint: string;
  p256dh: string;
  auth: string;
  created_at: string;
};

export type PushSubscriptionInput = {
  endpoint: string;
  keys: { p256dh: string; auth: string };
  expirationTime?: number | null;
};

export type NotificationEvent = "checkpoint_pending" | "run_failed" | "run_completed";

export type NotificationChannel = {
  id: string;
  tenant_id: string;
  name: string;
  command_template: string;
  events: NotificationEvent[];
  enabled: boolean;
};

export type NotificationChannelInput = {
  name: string;
  command_template: string;
  events: NotificationEvent[];
  enabled: boolean;
};

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

  getTenantVariables: () => get<{ variables: Record<string, string> }>("/api/tenant/variables"),
  setTenantVariables: (variables: Record<string, string>) =>
    request<{ variables: Record<string, string> }>("PUT", "/api/tenant/variables", { variables }),

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

  listItems: (filter?: ListItemsFilter & { limit?: number }) =>
    get<{ items: Item[] }>("/api/items" + query({ ...filter, limit: filter?.limit?.toString() })),

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
    get<{ queue: QueueItem[]; weights_version: string }>(
      "/api/queue" + query({ project_id: projectId }),
    ),

  startNow: (id: string) =>
    post<{ item: Item }>(`/api/items/${encodeURIComponent(id)}/start-now`),

  getFailure: (id: string, workflowId: string) =>
    get<{ failure: FailureState }>(
      `/api/items/${encodeURIComponent(id)}/failure` +
        query({ workflow_id: workflowId }),
    ),

  resetFailure: (id: string, workflowId: string) =>
    post<{ failure: FailureState }>(
      `/api/items/${encodeURIComponent(id)}/failure/reset`,
      { workflow_id: workflowId },
    ),

  listRankings: () => get<{ rankings: RankingRun[] }>("/api/rankings"),

  getRanking: (id: string) =>
    get<{ ranking: RankingRun; entries: RankingEntry[] }>(
      `/api/rankings/${encodeURIComponent(id)}`,
    ),

  runRanking: () => post<{ ranking: RankingRun }>("/api/rankings/run"),

  board: (filter?: ListItemsFilter) =>
    get<{ columns: BoardColumn[] }>("/api/board" + query({ ...filter })),

  // --- agents, chat threads, jobs (M2 contract) -----------------------------

  listAgents: () => get<{ agents: Agent[] }>("/api/agents"),

  createAgent: (b: AgentInput) => post<{ agent: Agent }>("/api/agents", b),

  getAgent: (id: string) => get<{ agent: Agent }>(`/api/agents/${id}`),

  updateAgent: (id: string, b: AgentInput) =>
    patch<{ agent: Agent }>(`/api/agents/${id}`, b),

  deleteAgent: (id: string) => del(`/api/agents/${id}`),

  // --- runner registry and tenant defaults ---------------------------------
  listRunners: () => get<{ runners: Runner[] }>("/api/runners"),
  createRunner: (body: RunnerInput) => post<{ runner: Runner }>("/api/runners", body),
  getRunner: (id: string) => get<{ runner: Runner }>(`/api/runners/${encodeURIComponent(id)}`),
  updateRunner: (id: string, body: Partial<RunnerInput>) =>
    patch<{ runner: Runner }>(`/api/runners/${encodeURIComponent(id)}`, body),
  deleteRunner: (id: string) => del(`/api/runners/${encodeURIComponent(id)}`),

  // --- trusted skill registry ----------------------------------------------
  listSkills: (productId?: string) =>
    get<{ skills: Skill[] }>("/api/skills" + query({ product_id: productId })),

  createSkill: (b: SkillInput) => post<{ skill: Skill }>("/api/skills", b),

  getSkill: (id: string) =>
    get<{ skill: Skill; versions: SkillVersion[] }>(`/api/skills/${encodeURIComponent(id)}`),

  updateSkill: (id: string, b: SkillInput) =>
    patch<{ skill: Skill }>(`/api/skills/${encodeURIComponent(id)}`, b),

  deleteSkill: (id: string) => del(`/api/skills/${encodeURIComponent(id)}`),

  createSkillVersion: (id: string, content_md: string) =>
    post<{ version: SkillVersion }>(
      `/api/skills/${encodeURIComponent(id)}/versions`,
      { content_md },
    ),

  approveSkillVersion: (id: string, version: number) =>
    post<{ version: SkillVersion }>(
      `/api/skills/${encodeURIComponent(id)}/versions/${encodeURIComponent(String(version))}/approve`,
    ),

  enableSkill: (id: string) =>
    post<{ skill: Skill }>(`/api/skills/${encodeURIComponent(id)}/enable`),

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

  // --- workflow runs, checkpoints and notifications (M3 contract) ---------
  listWorkflowTemplates: () => get<{ templates: WorkflowTemplateSummary[] }>("/api/workflow-templates"),
  createWorkflowTemplate: (body: WorkflowTemplateInput) =>
    post<{ template: WorkflowTemplate }>("/api/workflow-templates", body),
  getWorkflowTemplate: (id: string) =>
    get<{ template: WorkflowTemplate }>(`/api/workflow-templates/${encodeURIComponent(id)}`),
  updateWorkflowTemplate: (id: string, body: WorkflowTemplateInput) =>
    patch<{ template: WorkflowTemplate }>(`/api/workflow-templates/${encodeURIComponent(id)}`, body),
  deleteWorkflowTemplate: (id: string) =>
    del(`/api/workflow-templates/${encodeURIComponent(id)}`),
  publishWorkflowTemplate: (id: string) =>
    post<{ template: WorkflowTemplate }>(`/api/workflow-templates/${encodeURIComponent(id)}/publish`),
  createWorkflowVersion: (id: string) =>
    post<{ template: WorkflowTemplate }>(`/api/workflow-templates/${encodeURIComponent(id)}/versions`),
  validateWorkflowTemplate: (graph: WorkflowGraph) =>
    post<{ problems: WorkflowProblem[] }>("/api/workflow-templates/validate", { graph }),
  startItemRun: (itemId: string, templateId: string, variables?: Record<string, string>) =>
    post<{ run: Run }>(`/api/items/${encodeURIComponent(itemId)}/runs`, { template_id: templateId, ...(variables ? { variables } : {}) }),
  listRuns: (filter?: { item_id?: string; state?: RunState; limit?: number }) =>
    get<{ runs: Run[] }>("/api/runs" + query({ ...filter, limit: filter?.limit?.toString() })),
  getRun: (id: string) => get<{ run: Run; steps: StepRun[] }>(`/api/runs/${encodeURIComponent(id)}`),
  cancelRun: (id: string) => post<{ run: Run }>(`/api/runs/${encodeURIComponent(id)}/cancel`),
  runLogs: (id: string, afterSeq = 0, limit = 500) =>
    get<{ logs: LogEntry[] }>(`/api/runs/${encodeURIComponent(id)}/logs` + query({ after_seq: String(afterSeq), limit: String(limit) })),
  listCheckpoints: (state = "pending", limit = 50) =>
    get<{ checkpoints: Checkpoint[] }>("/api/checkpoints" + query({ state, limit: String(limit) })),
  getCheckpoint: (id: string) => get<{ checkpoint: Checkpoint }>(`/api/checkpoints/${encodeURIComponent(id)}`),
  decideCheckpoint: (id: string, body: { decision: CheckpointDecision; decision_md?: string; fields?: Record<string, unknown> }) =>
    post<{ checkpoint: Checkpoint }>(`/api/checkpoints/${encodeURIComponent(id)}/decision`, body),
  intervene: (id: string, body: { kind: Intervention["kind"]; node_id?: string; step_run_id?: string; body_md?: string }) =>
    post<{ intervention: Intervention }>(`/api/runs/${encodeURIComponent(id)}/intervene`, body),
  listInterventions: (id: string) => get<{ interventions: Intervention[] }>(`/api/runs/${encodeURIComponent(id)}/interventions`),
  listMemory: (params: { scope_kind?: string; scope_ref?: string; limit?: number }) =>
    get<{ entries: MemoryEntry[] }>("/api/memory" + query({ ...params, limit: params.limit?.toString() })),
  memoryManifest: (stepRunId: string) => get<{ manifest: unknown[] }>("/api/memory/manifest" + query({ step_run_id: stepRunId })),
  listNotifications: (unread = true, limit = 50) =>
    get<{ notifications: Notification[] }>("/api/notifications" + query({ unread: unread ? "true" : undefined, limit: String(limit) })),
  markNotificationRead: (id: string) => post<{ notification: Notification }>(`/api/notifications/${encodeURIComponent(id)}/read`),

  // --- usage, push subscriptions and command notification channels (M5) ----
  usage: (params?: { since?: string; until?: string; product_id?: string; item_id?: string }) =>
    get<{ usage: UsageBucket[]; jobs: JobUsage[] }>("/api/usage" + query(params ?? {})),

  listPushSubscriptions: () =>
    get<{ subscriptions: PushSubscription[] }>("/api/push/subscriptions"),
  createPushSubscription: (body: PushSubscriptionInput) =>
    post<{ subscription: PushSubscription }>("/api/push/subscriptions", body),
  deletePushSubscription: (id: string) =>
    del(`/api/push/subscriptions/${encodeURIComponent(id)}`),

  listNotificationChannels: () =>
    get<{ channels: NotificationChannel[] }>("/api/notification-channels"),
  createNotificationChannel: (body: NotificationChannelInput) =>
    post<{ channel: NotificationChannel }>("/api/notification-channels", body),
  updateNotificationChannel: (id: string, body: Partial<NotificationChannelInput>) =>
    patch<{ channel: NotificationChannel }>(`/api/notification-channels/${encodeURIComponent(id)}`, body),
  deleteNotificationChannel: (id: string) =>
    del(`/api/notification-channels/${encodeURIComponent(id)}`),
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

export type RunnerKind = "cli" | "api";
export type AgentConfig = Record<string, unknown>;
export interface RunnerRef { id: string; name: string; }
export interface Runner {
  id: string; name: string; kind: RunnerKind; config: AgentConfig; credential_ref: string;
  budget_tokens: number; budget_cost_usd: number; enabled: boolean; created_at: string; updated_at: string;
}
export interface RunnerInput {
  name: string; kind: RunnerKind; config: AgentConfig; credential_ref?: string;
  budget_tokens: number; budget_cost_usd: number; enabled: boolean;
}

export interface Agent {
  id: string;
  name: string;
  role: string;
  runners: RunnerRef[];
  capabilities: AgentConfig;
  concurrency_limit: number;
  enabled: boolean;
  soul_md: string;
  skills_policy: AgentConfig;
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
  runners?: string[];
  capabilities?: AgentConfig;
  concurrency_limit?: number;
  enabled?: boolean;
  soul_md?: string;
  skills_policy?: AgentConfig;
}

/** A trusted procedure in the tenant skill registry. A null product_id means
 * the skill is available across the tenant; current_version is a version number. */
export interface Skill {
  id: string;
  name: string;
  slug: string;
  product_id: string | null;
  description: string;
  enabled: boolean;
  current_version: number | null;
  created_at?: string;
  updated_at?: string;
}

/** Version content is immutable after approval. `version` is its number and
 * `content_sha` is the server-computed content fingerprint. */
export interface SkillVersion {
  id: string;
  skill_id: string;
  version: number;
  content_md: string;
  content_sha: string;
  author_kind: string;
  author_id?: string | null;
  approved_by?: string | null;
  approved_by_user_id?: string | null;
  approved_at?: string | null;
  created_at?: string;
}

/** PATCH callers send only changed fields; empty product_id clears the scope. */
export interface SkillInput {
  name?: string;
  slug?: string;
  product_id?: string;
  description?: string;
  enabled?: boolean;
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
  run_id?: string;
  step_run_id?: string;
  stream?: string;
  level?: string;
  kind?: string;
  message: string;
  ts?: string;
}

// --- M3 workflow runtime ---------------------------------------------------
export type RunState = "queued" | "running" | "waiting_human" | "completed" | "failed" | "cancelled";
export type StepState = "pending" | "ready" | "running" | "waiting" | "succeeded" | "failed" | "skipped" | "blocked" | "cancelled" | "superseded";
export type ReceiptState = "delivered" | "skipped" | "dead";
export interface Receipt { state: ReceiptState; port?: string; value?: unknown; }
export interface WorkflowNode {
  id: string; kind: string; name: string; type?: string; type_version?: number;
  config?: Record<string, unknown>; position?: { x: number; y: number };
  on_error?: string; always_output_data?: boolean; workspace?: string;
}
export interface WorkflowEdge { id: string; from: { node: string; port: string }; to: { node: string; port: string }; }
export interface WorkflowGraph { schema_version?: number; nodes: WorkflowNode[]; edges: WorkflowEdge[]; }
export interface WorkflowProblem { code: string; message: string; node_id?: string; edge_id?: string; }
export interface WorkflowTemplateSummary {
  id: string; name: string; applies_to?: string; version?: number; published_at?: string | null;
  max_concurrency?: number; created_at?: string;
}
export interface WorkflowTemplateInput {
  name: string; applies_to: string; max_concurrency: number; graph: WorkflowGraph;
}
export interface WorkflowTemplate {
  id: string; name: string; applies_to?: string; version?: number; published?: boolean;
  published_at?: string | null; status?: string; max_concurrency?: number; graph?: WorkflowGraph; created_at?: string; updated_at?: string;
}
export interface Run {
  id: string; item_id: string; template_id: string; template_version: number; state: RunState;
  variables?: Record<string, string>;
  max_concurrency?: number; started_by?: string; started_at: string; finished_at?: string | null;
  error?: string | null; failure_kind?: string | null; branch?: string | null; worktree_path?: string | null;
  definition_snapshot_json?: WorkflowGraph | string;
}
export interface TokenUsage { in?: number; out?: number; }
export interface StepResult {
  status?: string; exit_reason?: string; summary?: string; tokens?: TokenUsage;
  cost_usd?: number; failure_kind?: string | null; artifacts?: unknown[];
  runner?: string; runners_tried?: string[]; runner_attempt?: number;
}
export interface StepRun {
  id: string; run_id: string; node_id: string; iteration_path?: string; run_index?: number;
  kind: string; name: string; state: StepState; input_edges?: Record<string, Receipt>;
  output_ports?: Record<string, { value?: unknown; summary?: string }>; result?: StepResult | null;
  failure_kind?: string | null; workspace_kind?: string; workdir?: string | null; agent_job_id?: string | null;
  memory_manifest?: unknown[]; context_manifest?: unknown[]; claim_expires_at?: string | null;
  started_at?: string | null; finished_at?: string | null;
}
export interface CheckpointFieldSpec { key: string; type: "string" | "text" | "number" | "boolean" | "enum"; label: string; required?: boolean; options?: string[]; }
export type CheckpointDecision = "approve" | "approve_with_edits" | "reject" | "abort";
export type CheckpointArtifactKind =
  | "branch" | "commit" | "pr" | "diff_summary" | "file" | "report"
  | "issue" | "issue_body" | "document" | "wiki_page" | "link" | "item";
export interface CheckpointArtifact {
  kind: CheckpointArtifactKind | (string & {});
  uri?: string | null;
  meta?: Record<string, unknown> | string | null;
  step_name?: string;
}
export interface CheckpointItemRef { id: string; number: number; title: string; }
export interface Checkpoint {
  id: string; run_id: string; step_run_id: string; node_id: string; lap: number; state: string;
  summary_md: string; question?: string | null; artifacts: CheckpointArtifact[]; item: CheckpointItemRef | null;
  decision_md?: string | null; fields?: Record<string, unknown> | null;
  fields_spec?: CheckpointFieldSpec[]; decided_by_kind?: string | null; decided_by?: string | null;
  decided_at?: string | null; remind_after?: string | null; created_at?: string;
}
export interface Intervention {
  id: string; run_id: string; step_run_id?: string | null; node_id?: string | null; seq: number;
  kind: "inject" | "redo" | "stop"; body_md?: string; created_at?: string; delivered_at?: string | null;
}
export interface MemoryEntry {
  id: string; scope_kind: string; scope_ref: string; key: string; value?: unknown; revision: number;
  seq?: number; superseded_by?: string | null; created_at?: string;
}
export interface Notification {
  id: string; kind: "checkpoint_pending" | "run_failed" | "run_completed" | "sync_error";
  title?: string; body?: string; read_at?: string | null; created_at: string; run_id?: string; item_id?: string;
}

export interface RunEventHandlers {
  onStep: (step: StepRun) => void; onLog: (entry: LogEntry) => void;
  onCheckpoint: (checkpoint: Checkpoint) => void; onRun: (run: Run) => void;
  onDone: () => void; onError: () => void;
}
export function openRunEvents(runId: string, afterSeq: number, h: RunEventHandlers): () => void {
  const es = new EventSource(`/api/runs/${encodeURIComponent(runId)}/events?after_seq=${encodeURIComponent(String(afterSeq))}`);
  es.addEventListener("step", (e: MessageEvent<string>) => { const value = parseFrame<{ step: StepRun }>(e.data)?.step; if (value) h.onStep(value); });
  es.addEventListener("log", (e: MessageEvent<string>) => { const value = parseFrame<LogEntry>(e.data); if (value) h.onLog(value); });
  es.addEventListener("checkpoint", (e: MessageEvent<string>) => { const value = parseFrame<{ checkpoint: Checkpoint }>(e.data)?.checkpoint; if (value) h.onCheckpoint(value); });
  es.addEventListener("run", (e: MessageEvent<string>) => { const value = parseFrame<{ run: Run }>(e.data)?.run; if (value) h.onRun(value); });
  es.addEventListener("done", () => h.onDone());
  es.addEventListener("error", () => { es.close(); h.onError(); });
  return () => es.close();
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
