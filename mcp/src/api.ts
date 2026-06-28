/** Minimal typed client for the Cadence REST API (Bearer auth). */

export type Kind = 'deep' | 'light' | 'admin' | 'meet' | 'personal';
export type Status = 'backlog' | 'scheduled' | 'focus' | 'done';
export type Recurrence = 'none' | 'daily' | 'weekdays' | 'weekly' | 'monthly';

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
export interface Link {
  label: string;
  url: string;
}

export interface Task {
  id: string;
  projectId: string | null;
  title: string;
  kind: Kind;
  status: Status;
  effortMinutes: number;
  urgent: boolean;
  note: string;
  place: string | null;
  scheduledAt: string | null;
  deadline: string | null;
  recurrence: Recurrence;
  doneAt: string | null;
  links: Link[];
  subtasks: Subtask[];
  assignees: Assignee[];
  version: number;
}

export interface Project {
  id: string;
  name: string;
  subtitle: string; // the outcome / why
  due: string | null;
  color: string;
  members: Assignee[];
}

export interface User {
  id: string;
  name: string;
  email: string;
}

export interface Bootstrap {
  tenant: { id: string; name: string };
  user: User;
  projects: Project[];
  tasks: Task[];
}

const API_URL = (process.env.CADENCE_API_URL ?? 'http://localhost:8088').replace(/\/$/, '');
const TOKEN = process.env.CADENCE_API_TOKEN ?? '';

export class CadenceError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  if (!TOKEN) {
    throw new CadenceError('CADENCE_API_TOKEN is not set. Create a token in Cadence (account menu → API tokens).', 0);
  }
  let res: Response;
  try {
    res = await fetch(`${API_URL}${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${TOKEN}`,
        ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new CadenceError(`Could not reach Cadence at ${API_URL}. Is the server running?`, 0);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    if (res.status === 401) throw new CadenceError('Unauthorized — check CADENCE_API_TOKEN.', 401);
    throw new CadenceError(data?.error?.message ?? `Request failed (${res.status})`, res.status);
  }
  return data as T;
}

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
  deadline?: string | null;
  recurrence?: Recurrence;
  subtasks?: Subtask[];
  links?: Link[];
}

export const api = {
  apiUrl: API_URL,
  hasToken: () => TOKEN !== '',
  bootstrap: () => req<Bootstrap>('GET', '/api/v1/bootstrap'),
  createTask: (input: CreateTaskInput) => req<Task>('POST', '/api/v1/tasks', input),
  updateTask: (id: string, patch: Record<string, unknown>) => req<Task>('PATCH', `/api/v1/tasks/${id}`, patch),
  deleteTask: (id: string) => req<void>('DELETE', `/api/v1/tasks/${id}`),
  createProject: (input: { name: string; subtitle?: string; due?: string | null }) =>
    req<Project>('POST', '/api/v1/projects', input),
};
