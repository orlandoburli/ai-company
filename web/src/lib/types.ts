/** Shared API types, mirroring the Go backend's JSON contracts. */

export type Role = "owner" | "admin" | "member" | "guest";

export type Status =
  | "backlog"
  | "todo"
  | "in_progress"
  | "waiting_human"
  | "in_review"
  | "done"
  | "cancelled";

/** Fixed column order used by the board and any status list. */
export const STATUSES: Status[] = [
  "backlog",
  "todo",
  "in_progress",
  "waiting_human",
  "in_review",
  "done",
  "cancelled",
];

export type Priority = "priority:p0" | "priority:p1" | "priority:p2";

export const PRIORITIES: (Priority | null)[] = [
  null,
  "priority:p2",
  "priority:p1",
  "priority:p0",
];

export type AssigneeKind = "human" | "agent";

export interface Item {
  id: string;
  project_id: string;
  number: number;
  parent_id?: string;
  kind: string;
  title: string;
  description_md: string;
  status: Status;
  priority: Priority | null;
  priority_source: string | null;
  paused?: boolean;
  pinned?: boolean;
  paused_at?: string;
  paused_by?: string;
  pause_reason?: string;
  pinned_at?: string;
  pinned_by?: string;
  assignee_kind?: AssigneeKind;
  assignee_user_id?: string;
  github_issue_url?: string;
  jira_issue_key?: string;
  external_url?: string;
  labels: string[];
  due_date?: string;
  sort_order: number;
  created_at: string;
  updated_at: string;
  blocked: boolean;
  is_epic: boolean;
}

/** Compact item refs included by the item detail endpoint for its hierarchy. */
export interface ItemFamilyRef {
  id: string;
  number: number;
  title: string;
  kind: string;
  status: Status;
}

export interface ItemChildRef extends ItemFamilyRef {
  priority: Priority | null;
}

export interface GitHubItemLink {
  kind: "issue" | "pr";
  repo: string;
  number: number;
  title: string;
  url: string;
  state: string;
  head_branch?: string | null;
  base_branch?: string | null;
}

export interface ProjectKind {
  name: string;
  position: number;
}

export interface Product {
  id: string;
  name: string;
  slug: string;
  repo_url?: string;
  jira_project_key?: string;
  created_at: string;
}

export interface Project {
  id: string;
  product_id: string;
  parent_id?: string;
  name: string;
  key?: string;
  status: string;
  created_at: string;
}

export interface Progress {
  done: number;
  total: number;
  percent: number;
}

export interface ItemNode {
  item: Item;
  progress: Progress;
  children: ItemNode[];
}

export interface ProjectNode {
  project: Project;
  progress: Progress;
  items: ItemNode[];
  children: ProjectNode[];
}

export interface ItemRef {
  id: string;
  number: number;
  title: string;
  status: Status;
}

export interface DependencyEdge {
  id: string;
  item_id: string;
  blocked_by_id: string;
  kind: string;
  created_at: string;
  other: ItemRef;
}

export interface ActivityEntry {
  id: string;
  item_id?: string;
  action: string;
  actor_kind: string;
  actor_user_id?: string;
  before?: unknown;
  after?: unknown;
  created_at: string;
}

export interface ScoreFactor {
  key: string;
  input: string;
  weight: number;
  contribution: number;
}

export interface ItemScore {
  item_id: string;
  number: number;
  /** Empty when eligible; otherwise set by the API, e.g. "paused". */
  ineligible: string;
  base_score: number;
  weights_version: string;
  factors: ScoreFactor[];
}

export interface QueueItem {
  item: Item;
  score: {
    base_score: number;
    weights_version: string;
    factors: ScoreFactor[];
  };
  pinned: boolean;
  pinned_at?: string;
  paused: boolean;
  /** Present when an active ranking run decided this item's relative order. */
  rank?: number;
  /** Failure summary attached by the queue endpoint (held-back items may be omitted). */
  failure_state?: "ok" | "backing_off" | "exhausted" | string;
  next_eligible_at?: string | null;
  start_now_at?: string | null;
}

/** Kept as an alias for M1 components that still use the older name. */
export type QueueEntry = QueueItem;

export interface FailureState {
  item_id: string;
  workflow_id: string;
  consecutive_failures: number;
  state: "ok" | "backing_off" | "exhausted" | string;
  curve: "exponential" | "linear" | string;
  backoff_params: {
    x_seconds: number;
    y: number;
    step_seconds: number;
    max_backoff_seconds: number;
    max_failures: number;
  };
  backoff_version: string;
  last_failure_kind?: string;
  last_error_md?: string;
  last_failed_at?: string | null;
  next_eligible_at?: string | null;
  exhausted_at?: string | null;
}

export type RankingState = "running" | "ready" | "failed" | "superseded";
export type RankingDegradedReason = "llm_failed" | "invalid_output" | "over_budget";

export interface RankingRun {
  id: string;
  tenant_id: string;
  state: RankingState;
  weights_version: string;
  llm_model?: string | null;
  degraded_reason?: RankingDegradedReason | string;
  input_fingerprint: string;
  summary_md: string;
  roots_json: Array<{ item_id: string; unblocks: number; status: string }> | string;
  computed_at: string;
  expires_at: string;
  superseded_by?: string | null;
}

export interface RankingEntry {
  id: string;
  ranking_id: string;
  item_id: string;
  rank: number;
  score: number;
  reason_md: string;
  base_score: number;
  factors_json: unknown[] | string;
}

export interface BoardColumn {
  status: Status;
  items: Item[];
}

export interface UserView {
  id: string;
  email: string;
  name: string;
}

export interface TenantView {
  id: string;
  name: string;
  slug: string;
  role: Role;
  language?: "en" | "pt-BR";
}

export interface MeResponse {
  user: UserView;
  tenant: TenantView;
  tenants: TenantView[];
}

export interface Member {
  user_id: string;
  email: string;
  name: string;
  role: Role;
  joined_at: string;
}

export interface Invite {
  id: string;
  email: string;
  role: Role;
  expires_at: string;
  created_at: string;
}

export interface InvitePeek {
  tenant_name: string;
  email: string;
  role: Role;
  expires_at: string;
}

export interface CreateInviteResult {
  invite: Invite;
  token: string;
  accept_url: string;
}

export interface ApiErrorBody {
  error: { code: string; message: string };
}
