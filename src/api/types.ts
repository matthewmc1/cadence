import type { Kind } from '../lib/energy';

/** Server-side task lifecycle status (maps to Board columns). */
export type Status = 'backlog' | 'scheduled' | 'focus' | 'done';

/** Normalized task — the single record the server stores and streams. */
export interface Task {
  id: string;
  tenantId: string;
  projectId: string | null;
  title: string;
  kind: Kind;
  status: Status;
  effortMinutes: number;
  urgent: boolean;
  note: string;
  place: string | null;
  scheduledAt: string | null; // absolute ISO datetime the task is planned for
  position: number;
  doneAt: string | null;
  deadline: string | null;
  recurrence: Recurrence;
  links: Link[];
  subtasks: Subtask[];
  assignees: Assignee[];
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ProjectMember {
  userId: string;
  initial: string;
  color: string;
}

export interface Link {
  label: string;
  url: string;
}
export interface Subtask {
  id: string;
  title: string;
  done: boolean;
}
export interface Assignee {
  userId: string;
  initial: string;
  color: string;
}
export type Recurrence = 'none' | 'daily' | 'weekdays' | 'weekly' | 'monthly';

export interface Project {
  id: string;
  tenantId: string;
  name: string;
  subtitle: string;
  due: string | null;
  color: string;
  members: ProjectMember[];
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface Tenant {
  id: string;
  name: string;
  createdAt: string;
}

export interface User {
  id: string;
  tenantId: string;
  name: string;
  email: string;
  initial: string;
  color: string;
}

export interface Bootstrap {
  tenant: Tenant;
  user: User;
  projects: Project[];
  tasks: Task[];
  serverAt: string;
}

export interface ApiTokenInfo {
  id: string;
  name: string;
  createdAt: string;
  lastUsedAt: string | null;
}

export type EventType =
  | 'task.created'
  | 'task.updated'
  | 'task.deleted'
  | 'project.created'
  | 'project.updated'
  | 'project.deleted';

export interface ServerEvent {
  id: string;
  type: EventType;
  tenantId: string;
  actorId: string;
  task?: Task;
  project?: Project;
  entityId: string;
  at: string;
}

/** Fields accepted when creating a task. */
export interface CreateTaskInput {
  title: string;
  kind?: Kind;
  projectId?: string | null;
  status?: Status;
  effortMinutes?: number;
  urgent?: boolean;
  note?: string;
  place?: string | null;
  scheduledAt?: string | null;
  position?: number;
  deadline?: string | null;
  recurrence?: Recurrence;
  links?: Link[];
  subtasks?: Subtask[];
  assignees?: Assignee[];
}

/** Partial task update; any subset of these keys (null clears nullable ones). */
export type TaskPatch = Partial<{
  title: string;
  kind: Kind;
  status: Status;
  effortMinutes: number;
  urgent: boolean;
  note: string;
  projectId: string | null;
  place: string | null;
  scheduledAt: string | null;
  position: number;
  deadline: string | null;
  recurrence: Recurrence;
  links: Link[];
  subtasks: Subtask[];
  assignees: Assignee[];
}>;
