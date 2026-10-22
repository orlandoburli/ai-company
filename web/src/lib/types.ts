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

export interface QueueEntry {
  item: Item;
  score: {
    base_score: number;
    weights_version: string;
    factors: ScoreFactor[];
  };
  pinned: boolean;
  pinned_at?: string;
  paused: boolean;
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
