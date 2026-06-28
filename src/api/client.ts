import type { Bootstrap, CreateTaskInput, Project, Task, TaskPatch, User } from './types';

/**
 * Base URL of the Cadence API. Override with VITE_API_URL at build time; the
 * default matches `cadence-server` running locally on :8088.
 */
const API_URL: string =
  (import.meta.env.VITE_API_URL as string | undefined)?.replace(/\/$/, '') ?? 'http://localhost:8088';

export const apiBase = API_URL;

export class ApiError extends Error {
  status: number;
  field?: string;
  constructor(message: string, status: number, field?: string) {
    super(message);
    this.status = status;
    this.field = field;
  }
}

async function request<T>(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
  const h: Record<string, string> = { ...headers };
  if (body !== undefined) h['Content-Type'] = 'application/json';

  let res: Response;
  try {
    res = await fetch(`${API_URL}${path}`, {
      method,
      headers: h,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'include', // send/receive the session cookie
    });
  } catch {
    throw new ApiError('Could not reach the Cadence server', 0);
  }

  if (res.status === 204) return undefined as T;

  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;

  if (!res.ok) {
    const err = data?.error;
    throw new ApiError(err?.message ?? `Request failed (${res.status})`, res.status, err?.field);
  }
  return data as T;
}

export const api = {
  // ---- auth ----
  me: () => request<{ user: User }>('GET', '/api/v1/auth/me'),
  requestLink: (email: string) => request<{ ok: boolean; email: string; devLink?: string }>('POST', '/api/v1/auth/request', { email }),
  verify: (token: string) => request<{ user: User }>('POST', '/api/v1/auth/verify', { token }),
  logout: () => request<void>('POST', '/api/v1/auth/logout'),

  // ---- data ----
  bootstrap: () => request<Bootstrap>('GET', '/api/v1/bootstrap'),
  createTask: (input: CreateTaskInput) => request<Task>('POST', '/api/v1/tasks', input),
  updateTask: (id: string, patch: TaskPatch, version?: number) =>
    request<Task>('PATCH', `/api/v1/tasks/${id}`, patch, version != null ? { 'If-Match': String(version) } : undefined),
  deleteTask: (id: string) => request<void>('DELETE', `/api/v1/tasks/${id}`),

  createProject: (input: { name: string; subtitle?: string; due?: string | null; color?: string }) =>
    request<Project>('POST', '/api/v1/projects', input),
  updateProject: (id: string, patch: Record<string, unknown>, version?: number) =>
    request<Project>('PATCH', `/api/v1/projects/${id}`, patch, version != null ? { 'If-Match': String(version) } : undefined),
  deleteProject: (id: string) => request<void>('DELETE', `/api/v1/projects/${id}`),
};

/** WebSocket URL for realtime. Identity rides the session cookie. */
export function realtimeURL(): string {
  const base = API_URL.replace(/^http/, 'ws');
  return `${base}/api/v1/realtime`;
}
