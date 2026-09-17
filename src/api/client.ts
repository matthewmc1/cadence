import type {
  AiChatInput,
  AiChatResponse,
  AiStatus,
  ApiTokenInfo,
  Bootstrap,
  Client,
  CreateClientInput,
  CreateOutputInput,
  CreateProjectInput,
  CreateRequirementInput,
  CreateSignalInput,
  CreateSourceInput,
  CreateTaskInput,
  ExtractResponse,
  Origin,
  Output,
  Page,
  Project,
  Requirement,
  RequirementPatch,
  Signal,
  SignalBody,
  SignalFilter,
  SignalListParams,
  SignalPatch,
  Source,
  SourcePatch,
  Task,
  TaskPatch,
  User,
} from './types';

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

  // ---- API tokens (for the MCP server / programmatic access) ----
  listTokens: () => request<{ tokens: ApiTokenInfo[] }>('GET', '/api/v1/auth/tokens'),
  createToken: (name: string) => request<{ token: string; id: string; name: string; createdAt: string }>('POST', '/api/v1/auth/tokens', { name }),
  revokeToken: (id: string) => request<void>('DELETE', `/api/v1/auth/tokens/${id}`),

  // ---- data ----
  bootstrap: () => request<Bootstrap>('GET', '/api/v1/bootstrap'),
  createTask: (input: CreateTaskInput) => request<Task>('POST', '/api/v1/tasks', input),
  updateTask: (id: string, patch: TaskPatch, version?: number) =>
    request<Task>('PATCH', `/api/v1/tasks/${id}`, patch, version != null ? { 'If-Match': String(version) } : undefined),
  deleteTask: (id: string) => request<void>('DELETE', `/api/v1/tasks/${id}`),

  createProject: (input: CreateProjectInput) =>
    request<Project>('POST', '/api/v1/projects', input),
  updateProject: (id: string, patch: Record<string, unknown>, version?: number) =>
    request<Project>('PATCH', `/api/v1/projects/${id}`, patch, version != null ? { 'If-Match': String(version) } : undefined),
  deleteProject: (id: string) => request<void>('DELETE', `/api/v1/projects/${id}`),

  createClient: (input: CreateClientInput) => request<Client>('POST', '/api/v1/clients', input),
  updateClient: (id: string, patch: Record<string, unknown>, version?: number) =>
    request<Client>('PATCH', `/api/v1/clients/${id}`, patch, version != null ? { 'If-Match': String(version) } : undefined),
  deleteClient: (id: string) => request<void>('DELETE', `/api/v1/clients/${id}`),

  // ---- requirements (in bootstrap; kept current by requirement.* events) ----
  listRequirements: (projectId?: string) =>
    request<{ requirements: Requirement[] }>('GET', `/api/v1/requirements${query({ projectId })}`),
  createRequirement: (input: CreateRequirementInput) => request<Requirement>('POST', '/api/v1/requirements', input),
  updateRequirement: (id: string, patch: RequirementPatch, version?: number) =>
    request<Requirement>('PATCH', `/api/v1/requirements/${id}`, patch, ifMatch(version)),
  deleteRequirement: (id: string) => request<void>('DELETE', `/api/v1/requirements/${id}`),

  // ---- signals (paged, never in bootstrap; the body is its own audited read) ----
  listSignals: (params: SignalListParams = {}) => request<Page<Signal>>('GET', `/api/v1/signals${query(params)}`),
  countSignals: (filter: SignalFilter = { stage: 'inbox' }) =>
    request<{ count: number }>('GET', `/api/v1/signals/count${query(filter)}`),
  createSignal: (input: CreateSignalInput) => request<Signal>('POST', '/api/v1/signals', input),
  getSignal: (id: string) => request<Signal>('GET', `/api/v1/signals/${id}`),
  getSignalBody: (id: string) => request<SignalBody>('GET', `/api/v1/signals/${id}/body`),
  // disposition only (stage, snoozedUntil, projectHint, extracted, retentionUntil); If-Match guards the version
  updateSignal: (id: string, patch: SignalPatch, version?: number) =>
    request<Signal>('PATCH', `/api/v1/signals/${id}`, patch, ifMatch(version)),
  deleteSignal: (id: string) => request<void>('DELETE', `/api/v1/signals/${id}`),
  // fact extraction (Round 1, built concurrently) — see ExtractResponse for the defensive read
  extractSignal: (id: string) => request<ExtractResponse>('POST', `/api/v1/signals/${id}/extract`),

  listSources: () => request<{ sources: Source[] }>('GET', '/api/v1/sources'),
  createSource: (input: CreateSourceInput) => request<Source>('POST', '/api/v1/sources', input),
  updateSource: (id: string, patch: SourcePatch, version?: number) =>
    request<Source>('PATCH', `/api/v1/sources/${id}`, patch, ifMatch(version)),
  deleteSource: (id: string) => request<void>('DELETE', `/api/v1/sources/${id}`),

  // ---- a work item's provenance: origins (signals it came from) and outputs (what it produced) ----
  listOrigins: (taskId: string) => request<{ origins: Origin[] }>('GET', `/api/v1/tasks/${taskId}/origins`),
  attachOrigin: (taskId: string, signalId: string) => request<Origin>('POST', `/api/v1/tasks/${taskId}/origins`, { signalId }),
  detachOrigin: (taskId: string, signalId: string) => request<void>('DELETE', `/api/v1/tasks/${taskId}/origins/${signalId}`),
  listOutputs: (taskId: string) => request<{ outputs: Output[] }>('GET', `/api/v1/tasks/${taskId}/outputs`),
  createOutput: (taskId: string, input: CreateOutputInput) => request<Output>('POST', `/api/v1/tasks/${taskId}/outputs`, input),
  deleteOutput: (taskId: string, outputId: string) => request<void>('DELETE', `/api/v1/tasks/${taskId}/outputs/${outputId}`),

  // ---- AI gateway (server-side Ollama; nothing leaves the deployment) ----
  aiStatus: () => request<AiStatus>('GET', '/api/v1/ai/status'),
  aiChat: (input: AiChatInput) => request<AiChatResponse>('POST', '/api/v1/ai/chat', input),
};

/** The optimistic-concurrency header the PATCH endpoints read (absent → last-write-wins). */
function ifMatch(version?: number): Record<string, string> | undefined {
  return version != null ? { 'If-Match': String(version) } : undefined;
}

/** `?a=1&b=2` from an object, skipping undefined/null/'' values; '' when nothing is set. */
function query(params: object): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params as Record<string, unknown>)) {
    if (v === undefined || v === null || v === '') continue;
    q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

/**
 * WebSocket URL for realtime. Identity rides the session cookie. With a
 * relative API base (VITE_API_URL="" — the app served by cadence-server
 * itself) the socket targets the page's own origin.
 */
export function realtimeURL(): string {
  const base = API_URL
    ? API_URL.replace(/^http/, 'ws')
    : `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}`;
  return `${base}/api/v1/realtime`;
}
