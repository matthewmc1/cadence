import type { Kind } from '../lib/energy';

/** Server-side task lifecycle status (maps to Board columns). Being replaced by Stage. */
export type Status = 'backlog' | 'scheduled' | 'focus' | 'done';

/**
 * Work-item lifecycle (migration 0013) — the successor to Status. The server
 * keeps the two coherent for one release: set either and the other is derived
 * (backlog|scheduled → todo, focus → doing, done → done; todo → scheduled if
 * scheduledAt is set else backlog, doing → focus, waiting → backlog).
 */
export type Stage = 'todo' | 'doing' | 'waiting' | 'done';

/** What a work item asks of someone else. Each field is bounded (2000 chars). */
export interface Ask {
  what: string;
  forWhom: string;
  why: string;
}

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
  important: boolean; // Eisenhower's second axis — protects deep, long-term work
  note: string;
  reflection: string; // "what did this advance?" captured at completion
  place: string | null;
  scheduledAt: string | null; // absolute ISO datetime the task is planned for
  position: number;
  doneAt: string | null;
  deadline: string | null;
  recurrence: Recurrence;
  links: Link[];
  subtasks: Subtask[];
  assignees: Assignee[];
  // Work-item fields (0013). Always present from current servers; optional
  // here so the UI's locally-built tasks keep compiling until it adopts them.
  stage?: Stage;
  ownerId?: string | null;
  createdBy?: string | null; // stamped from the actor at create; never patchable
  requirementId?: string | null; // the requirement this item serves; nulled if it is deleted
  definitionOfDone?: string;
  waitingOnPersonId?: string | null; // stage=waiting: on whom
  waitingOnReason?: string; // stage=waiting: why (waiting needs a reason or a person)
  waitingOnSince?: string | null; // stamped entering waiting, cleared leaving; never patchable
  ask?: Ask;
  askBy?: string | null; // when the ask is needed by
  // Provenance counts (0014/0015): how many signals this item came from and
  // how many artefacts it produced. Server-derived and read-only — never sent
  // in a patch; the lists live at /tasks/{id}/origins and /outputs. Optional
  // because a locally-built task has neither yet.
  originCount?: number;
  outputCount?: number;
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

/** A client (or internal initiative) — the strategic spine above projects. */
export type ClientTier = 'a' | 'b' | 'c';
/** 'area' is a PARA area of responsibility: ongoing, held to a standard, never finished. */
export type ClientKind = 'client' | 'internal' | 'area';

export interface Client {
  id: string;
  tenantId: string;
  name: string;
  tier: ClientTier;
  kind: ClientKind;
  color: string;
  standard: string; // what the area is held to; '' when unset
  expectedTouchDays: number | null; // cadence target; null = no expectation
  archivedAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface Project {
  id: string;
  tenantId: string;
  clientId: string | null; // the client this project serves
  name: string;
  subtitle: string;
  outcome: string; // why the project exists / what finishing looks like — its items' "why"
  due: string | null;
  color: string;
  archivedAt: string | null;
  members: ProjectMember[];
  version: number;
  createdAt: string;
  updatedAt: string;
}

/** One thing a project must deliver. Never a task — work items derive from it. */
export type RequirementStatus = 'open' | 'met' | 'dropped';

export interface Requirement {
  id: string;
  tenantId: string;
  projectId: string; // always set; a requirement belongs to exactly one project
  title: string;
  description: string;
  weight: number; // 1 (nice to have) … 5 (project fails without it)
  acceptance: string; // how we will know it is met
  status: RequirementStatus;
  position: number;
  archivedAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

/** Fields accepted when creating a requirement. */
export interface CreateRequirementInput {
  projectId: string;
  title: string;
  description?: string;
  weight?: number;
  acceptance?: string;
  status?: RequirementStatus;
  position?: number;
}

/** Partial requirement update; any subset of these keys. projectId moves it, never clears it. */
export type RequirementPatch = Partial<{
  title: string;
  projectId: string;
  description: string;
  weight: number;
  acceptance: string;
  status: RequirementStatus;
  position: number;
  archived: boolean;
}>;

/** One immutable row of the tenant's audit log (GET /api/v1/audit, paged). No PII: see docs/SCHEMA.md. */
export interface AuditEntry {
  id: string;
  tenantId: string;
  at: string;
  actorId: string | null; // null for system-initiated rows
  kind: string; // auth.login.issued | auth.login.verified | auth.token.create | auth.token.revoke | ai.chat | mcp.read | export | purge | connector.*
  entityType: string | null;
  entityId: string | null;
  detail: unknown; // bounded JSON; {} when there is nothing to say
  ipHash: string | null;
}

/** One keyset page. Pass nextCursor back as ?cursor= for the next (older) page; absent on the last page. */
export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

// ---- signals (migration 0014): captured things — never tasks, never in Bootstrap ----

/** Where signals come from. 'manual' is the paste box; every tenant has exactly one, created lazily. */
export type SourceKind = 'manual' | 'calendar' | 'email' | 'notes' | 'docs' | 'chat';

export interface Source {
  id: string;
  tenantId: string;
  kind: SourceKind;
  name: string;
  ownerId: string | null;
  config: Record<string, unknown>; // NON-secret connector settings; credentials never live here
  consentAt: string | null;
  retentionDays: number | null; // signals expire this many days after they occurred; null = keep
  disabledAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface CreateSourceInput {
  kind: Exclude<SourceKind, 'manual'>;
  name: string;
  ownerId?: string | null;
  config?: Record<string, unknown>;
  consentAt?: string | null;
  retentionDays?: number | null;
}

/** Partial source update; kind is immutable. */
export type SourcePatch = Partial<{
  name: string;
  ownerId: string | null;
  config: Record<string, unknown> | null;
  consentAt: string | null;
  retentionDays: number | null;
  disabled: boolean;
}>;

export type SignalKind = 'meeting' | 'email' | 'note' | 'doc' | 'chat' | 'link' | 'text';

/** The signal's disposition — the ONLY thing about a signal that changes after capture. */
export type SignalStage = 'inbox' | 'snoozed' | 'promoted' | 'attached' | 'dismissed';

export type ParticipantRole = 'from' | 'to' | 'cc' | 'attendee' | 'speaker';

/** Someone on a signal. PII: returned with the signal, never in an event. */
export interface Participant {
  idx: number;
  name: string;
  email: string | null;
  role: ParticipantRole;
  personId: string | null; // reserved for the people table
}

/**
 * A captured thing. Everything above the disposition block is immutable after
 * capture. The body is NOT here — fetch GET /api/v1/signals/{id}/body.
 */
export interface Signal {
  id: string;
  tenantId: string;
  sourceId: string;
  externalId: string; // the source's own id; the signal id for manual captures
  kind: SignalKind;
  title: string;
  excerpt: string; // first 2000 characters of the body
  bodyRef: string; // source-native locator for the full thing; may be ''
  occurredAt: string;
  capturedBy: string | null;
  // disposition
  stage: SignalStage;
  snoozedUntil: string | null;
  dispositionAt: string | null; // when it left inbox; null while in inbox
  extracted: Record<string, unknown>; // facts pulled from the body; {"links": […]} seeded by manual capture
  projectHint: string | null;
  retentionUntil: string | null;
  participants: Participant[];
  version: number;
  createdAt: string;
  updatedAt: string;
}

/** GET /api/v1/signals/{id}/body — the full captured text ('' when the signal has none). */
export interface SignalBody {
  tenantId: string;
  signalId: string;
  body: string;
  fetchedAt: string;
}

/** Metadata-only body of signal.* events (never excerpt, body or participants). */
export interface SignalMeta {
  id: string;
  version: number;
  kind: SignalKind;
  title: string;
  occurredAt: string;
  stage: SignalStage;
}

/** POST /api/v1/signals — manual capture. text ≤ 200 KB; its first line is the title when none is given. */
export interface CreateSignalInput {
  kind?: SignalKind; // default 'text'
  title?: string;
  text?: string;
  occurredAt?: string;
  participants?: Array<Omit<Participant, 'idx' | 'personId'> & Partial<Pick<Participant, 'personId'>>>;
  links?: Link[]; // seeded into extracted.links
  extracted?: Record<string, unknown>;
  projectHint?: string | null;
  retentionUntil?: string | null;
  // connector fields; a manual capture leaves them unset
  sourceId?: string;
  externalId?: string;
  bodyRef?: string;
}

/** PATCH /api/v1/signals/{id} — disposition only; any other key is a 400. */
export type SignalPatch = Partial<{
  stage: SignalStage;
  snoozedUntil: string | null; // required with stage 'snoozed'; alone it implies it
  projectHint: string | null;
  extracted: Record<string, unknown> | null;
  retentionUntil: string | null;
}>;

// ---- extracted facts (server/internal/extract) ----

/** A moment the text mentions: the phrase as written and what it resolved to. */
export interface ExtractedDate {
  text: string;
  at: string; // RFC3339
  confidence: number; // 0..1; ≤ 0.6 from the heuristic, ≤ 0.95 from the model
  corrected?: boolean; // a human rewrote this one — see the note on ExtractedFacts
}

export interface ExtractedPerson {
  name: string;
  email?: string; // only when it appeared in the text
  confidence: number;
  corrected?: boolean;
}

/** Same shape as Link (label, url) so the manual capture's seeded links merge in. */
export interface ExtractedLink {
  url: string;
  label?: string;
}

/**
 * What Signal.extracted holds once extraction has run (POST
 * /api/v1/signals/{id}/extract, or the background pass after every capture).
 * `model` is 'heuristic' when only the regex extractor ran, else the Ollama
 * model name; `promptVersion` is 'heuristic-v1' or 'facts-v1'. Before
 * extraction the object is {} (or {links} from a manual capture) — read it
 * with `isExtractedFacts`.
 */
export interface ExtractedFacts {
  dates: ExtractedDate[];
  people: ExtractedPerson[];
  links: ExtractedLink[];
  deadlines: ExtractedDate[];
  suggestedTitle: string;
  model: string;
  promptVersion: string;
  extractedAt: string;
}

export function isExtractedFacts(v: unknown): v is ExtractedFacts {
  return typeof v === 'object' && v !== null && typeof (v as ExtractedFacts).model === 'string' && Array.isArray((v as ExtractedFacts).dates);
}

/** POST /api/v1/signals/{id}/extract answers with the stored row and the facts. */
export interface ExtractResult {
  signal: Signal;
  extracted: ExtractedFacts;
}

/** Query for GET /api/v1/signals and /signals/count. */
export interface SignalFilter {
  stage?: SignalStage;
  sourceId?: string;
  projectHint?: string;
  occurredAfter?: string;
}

/** What a work item keeps of the signal it came from; survives the signal being purged. */
export interface OriginSnapshot {
  title: string;
  kind: SignalKind;
  occurredAt: string;
  bodyRef: string;
}

/** One work_item_signals row: this work item derives from this signal (GET /api/v1/tasks/{id}/origins). */
export interface Origin {
  tenantId: string;
  taskId: string;
  signalId: string; // may no longer resolve; snapshot always does
  snapshot: OriginSnapshot;
  createdAt: string;
}

// ---- outputs (migration 0015): what a work item produced ----

export type OutputKind = 'link' | 'doc' | 'pr' | 'email' | 'decision' | 'file' | 'note';

export interface Output {
  id: string;
  tenantId: string;
  taskId: string;
  kind: OutputKind;
  title: string;
  url: string;
  detail: string;
  createdBy: string | null;
  createdAt: string;
}

export interface CreateOutputInput {
  kind: OutputKind;
  title: string;
  url?: string;
  detail?: string;
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
  clients: Client[];
  projects: Project[];
  requirements: Requirement[];
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
  | 'project.deleted'
  | 'client.created'
  | 'client.updated'
  | 'client.deleted'
  | 'requirement.created' // body arrives in `entity` (generic envelope), not a typed field
  | 'requirement.updated'
  | 'requirement.deleted'
  | 'source.created' // entity: Source
  | 'source.updated'
  | 'source.deleted'
  | 'signal.created' // entity: SignalMeta — never the excerpt, body or participants
  | 'signal.updated'
  | 'signal.deleted'
  | 'origin.attached' // entityId: the task; entity: Origin
  | 'origin.detached' // entity: {taskId, signalId}
  | 'output.created' // entity: Output
  | 'output.deleted';

export interface ServerEvent {
  id: string;
  type: EventType;
  tenantId: string;
  actorId: string;
  task?: Task;
  project?: Project;
  client?: Client;
  /** Generic envelope: what changed (task|project|client|…). Always set by current servers. */
  entityType?: string;
  /** Body for entity types without a typed field above — metadata only, never bodies/PII. */
  entity?: unknown;
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
  important?: boolean;
  note?: string;
  reflection?: string;
  place?: string | null;
  scheduledAt?: string | null;
  position?: number;
  deadline?: string | null;
  recurrence?: Recurrence;
  links?: Link[];
  subtasks?: Subtask[];
  assignees?: Assignee[];
  // work-item fields (0013); stage wins over status when both are given
  stage?: Stage;
  ownerId?: string | null;
  requirementId?: string | null;
  definitionOfDone?: string;
  waitingOnPersonId?: string | null;
  waitingOnReason?: string;
  ask?: Ask;
  askBy?: string | null;
}

/** Fields accepted when creating a client. */
export interface CreateClientInput {
  name: string;
  tier?: ClientTier;
  kind?: ClientKind;
  color?: string;
  standard?: string;
  expectedTouchDays?: number | null;
}

/** Partial client update; any subset of these keys. */
export type ClientPatch = Partial<{
  name: string;
  tier: ClientTier;
  kind: ClientKind;
  color: string;
  standard: string;
  expectedTouchDays: number | null;
  archived: boolean;
}>;

/** Fields accepted when creating a project. */
export interface CreateProjectInput {
  name: string;
  subtitle?: string;
  outcome?: string;
  due?: string | null;
  color?: string;
  clientId?: string | null;
}

/** Partial project update; any subset of these keys. */
export type ProjectPatch = Partial<{
  name: string;
  subtitle: string;
  outcome: string;
  due: string | null;
  color: string;
  clientId: string | null;
  archived: boolean;
}>;

/** Partial task update; any subset of these keys (null clears nullable ones). */
export type TaskPatch = Partial<{
  title: string;
  kind: Kind;
  status: Status;
  effortMinutes: number;
  urgent: boolean;
  important: boolean;
  note: string;
  reflection: string;
  projectId: string | null;
  place: string | null;
  scheduledAt: string | null;
  position: number;
  deadline: string | null;
  recurrence: Recurrence;
  links: Link[];
  subtasks: Subtask[];
  assignees: Assignee[];
  // work-item fields (0013); createdBy and waitingOnSince are server-owned
  stage: Stage;
  ownerId: string | null;
  requirementId: string | null;
  definitionOfDone: string;
  waitingOnPersonId: string | null;
  waitingOnReason: string;
  ask: Ask | null; // null clears
  askBy: string | null;
}>;

// ---- signal list paging + extraction ----

/** Query for GET /api/v1/signals: the filter plus keyset paging (limit ≤ 1000, default 100). */
export interface SignalListParams extends SignalFilter {
  limit?: number;
  cursor?: string;
}

/**
 * POST /api/v1/signals/{id}/extract — pull facts (dates, asks, links, a
 * suggested title/project) out of the body. Built alongside this client, so
 * the shape is read defensively: a full Signal, or a partial with just the
 * new `extracted` object. Anything else is treated as "refetch the row".
 */
export type ExtractResponse = Signal | { signal?: Signal; extracted?: Record<string, unknown> };

// ---- AI gateway (server-side Ollama) ----

/** GET /api/v1/ai/status — what this deployment can run. */
export interface AiStatus {
  ollama: { configured: boolean; reachable: boolean; models: string[] };
  webllm: { localModels: boolean; models: string[] };
  policy: 'local' | 'local+cloud';
}

export type AiRole = 'system' | 'user' | 'assistant';

export interface AiChatMessage {
  role: AiRole;
  content: string;
}

/** POST /api/v1/ai/chat — one non-streaming turn (≤ 64 messages, ≤ 200k chars). */
export interface AiChatInput {
  model: string;
  messages: AiChatMessage[];
  format?: 'json';
  temperature?: number; // 0..2; the server defaults to 0.2
}

/** Ollama's /api/chat response, forwarded verbatim. */
export interface AiChatResponse {
  model: string;
  message: AiChatMessage;
  done: boolean;
  total_duration?: number;
  eval_count?: number;
}
