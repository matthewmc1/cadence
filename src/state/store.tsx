import {
  createContext,
  useContext,
  useReducer,
  useMemo,
  useEffect,
  useRef,
  type ReactNode,
} from 'react';
import type {
  Task as NTask, Project as NProject, Client as NClient, Signal as NSignal, Requirement as NRequirement, Origin, Output, SignalMeta,
  ServerEvent, TaskPatch, CreateTaskInput, CreateClientInput, CreateSignalInput, CreateOutputInput, CreateRequirementInput,
  RequirementPatch, SignalPatch, SignalStage, Ask, ClientTier, User, CreateProjectInput, ProjectPatch, ClientPatch,
} from '../api/types';
import { api, realtimeURL, ApiError } from '../api/client';
import { connectRealtime, type ConnState } from '../api/realtime';
import { inferTask, bestSlot, bandAt, type Kind, type EnergyProfile, type Band } from '../lib/energy';
import { selectToday, selectWeek, selectMonth, selectBacklog, selectProject, selectAllTasks, computeInsights, computeSignals, selectRecentDone, selectClientHealth, deriveEnergyProfile, priorityScore, stageOf, clock, selectInboxBundles, selectRemainder, signalOrder, type Insights, type Signal, type DoneItem, type ClientHealth, type TaskRow } from './selectors';
import { computeNow, computeWeek, startOfWeek, addDays, ymd, combine, sameDay, occurrences, tomorrow9am, hourOf } from '../lib/time';
import { undoStack, UndoBlocked, UNDO_HINT } from '../lib/undo';
import { modalIsOpen } from '../lib/focus';
import type { SchedTask, WeekCtx, Assignment } from '../ai/scheduler';
import type { View, ParkedView, WorkMode, Now, TodayItem, WeekDay, MonthGrid, BacklogTask, Project, InboxMode, InboxBundle } from './types';

const DOW = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const MONTHS_SHORT = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/* ------------------------------------------------------------------- model */

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';
type AuthStatus = 'checking' | 'anon' | 'authed';

/**
 * A magic-link token is one-shot: the server burns it on the first verify. In
 * dev, StrictMode double-invokes the session-check effect, so a naive second
 * call would 401 on the token its own first call just spent and bounce a
 * signed-in user back to the login form. Keyed by the token, the round-trip
 * happens exactly once and every caller awaits the same promise. Module-level
 * (not a ref) because StrictMode remounts the provider itself.
 */
const verifying = new Map<string, Promise<{ user: User }>>();
/** Set once a request comes back with a dev link: proof dev auth is on. */
let devAuthSeen = false;
function verifyOnce(token: string): Promise<{ user: User }> {
  let p = verifying.get(token);
  if (!p) {
    p = api.verify(token);
    verifying.set(token, p);
  }
  return p;
}

/** How many inbox signals one page carries; older pages load on demand. */
const INBOX_PAGE = 50;

/**
 * The loaded slice of the inbox. `items` is the flat, newest-first list of
 * every page fetched so far (the bundled view is derived from it); `count` is
 * the server's total for stage=inbox and drives the header badge, the landing
 * decision and the "and N more" remainder.
 */
interface InboxState {
  items: NSignal[];
  cursor: string | null; // next (older) page, or null on the last page
  loading: boolean;
  error: string | null; // the first page could not be read; `count` means nothing yet
  count: number;
  view: InboxMode;
  selectedId: string | null;
  selection: Set<string>; // multi-select for disposeMany
}

interface RawState {
  authStatus: AuthStatus;
  user: User | null;
  linkSent: string | null; // email a link was sent to
  devLink: string | null; // dev-only magic link
  linkNote: string | null; // why this request produced no new link (cooldown)
  status: LoadStatus;
  error: string | null;
  tasks: NTask[];
  projects: NProject[];
  clients: NClient[];
  requirements: NRequirement[];
  inbox: InboxState;
  // per-task provenance, fetched on demand and kept current by origin.*/output.* events
  originsByTask: Record<string, Origin[]>;
  outputsByTask: Record<string, Output[]>;
  undoDepth: number;
  undoLabel: string | null; // what Cmd+Z would undo next
  tenantId: string;
  userId: string;
  userInitial: string;
  userColor: string;
  view: View;
  viewExplicit: boolean; // the user picked a tab; the landing decision defers to it
  draft: string;
  selectedBacklogId: string | null;
  selectedProjectId: string | null;
  editorTaskId: string | null;
  editorMode: 'edit' | 'create' | null;
  tokensOpen: boolean;
  toast: string | null;
  highlightTaskId: string | null;
  connection: ConnState;
  clockTick: number; // bumped on a timer so the clock + week stay current
  planAnchor: number; // ms timestamp of the date the Plan view is centered on
  planMode: 'week' | 'month';
  workMode: WorkMode; // list | board — a saved way of looking at Work, kept per browser
  // the strips: one item just completed (its proof is being asked for) and
  // one just started (what done looks like, who it is for). Never a modal.
  proofTaskId: string | null;
  startTaskId: string | null;
  // this browser session's inbox work — what the inbox-zero payoff is built from
  session: SessionState;
}

/** What has happened in the inbox since the page loaded; nothing here persists. */
interface SessionState {
  dispositions: number; // one-key dispositions (snooze, dismiss, attach, promote, sweep) so far
  promotedTaskIds: string[]; // work items promoted here, in order
}

const WORK_MODE_KEY = 'cadence.workMode';

/**
 * The start strip is asked once per item: the ids it was skipped for live in
 * localStorage so a reload never asks again (an item whose definition of done
 * is set never asks at all).
 */
const START_SEEN_KEY = 'cadence.startSeen';
function loadStartSeen(): Set<string> {
  try {
    const raw = localStorage.getItem(START_SEEN_KEY);
    const ids = raw ? (JSON.parse(raw) as unknown) : [];
    return new Set(Array.isArray(ids) ? ids.filter((x): x is string => typeof x === 'string') : []);
  } catch {
    return new Set();
  }
}
function markStartSeen(id: string) {
  const seen = loadStartSeen();
  seen.add(id);
  try {
    // bounded: the newest few hundred are plenty; older items are long finished
    localStorage.setItem(START_SEEN_KEY, JSON.stringify([...seen].slice(-400)));
  } catch {
    /* private mode: the strip may ask once more after a reload */
  }
}
function loadWorkMode(): WorkMode {
  try {
    // a project is worked through its board; the list is the alternative
    return localStorage.getItem(WORK_MODE_KEY) === 'list' ? 'list' : 'board';
  } catch {
    return 'board';
  }
}

const initialState: RawState = {
  authStatus: 'checking',
  user: null,
  linkSent: null,
  devLink: null,
  linkNote: null,
  status: 'idle',
  error: null,
  tasks: [],
  projects: [],
  clients: [],
  requirements: [],
  inbox: { items: [], cursor: null, loading: false, error: null, count: 0, view: 'bundled', selectedId: null, selection: new Set() },
  originsByTask: {},
  outputsByTask: {},
  undoDepth: 0,
  undoLabel: null,
  tenantId: '',
  userId: '',
  userInitial: 'A',
  userColor: '#211E18',
  view: 'work', // LAND decides for real once the inbox count is known
  viewExplicit: false,
  draft: 'Draft the Q3 board deck',
  selectedBacklogId: null,
  selectedProjectId: null,
  editorTaskId: null,
  editorMode: null,
  tokensOpen: false,
  toast: null,
  highlightTaskId: null,
  connection: 'connecting',
  clockTick: 0,
  planAnchor: Date.now(),
  planMode: 'week',
  workMode: loadWorkMode(),
  proofTaskId: null,
  startTaskId: null,
  session: { dispositions: 0, promotedTaskIds: [] },
};

/* ---------------------------------------------------------------- actions */

type Action =
  | { type: 'AUTH_OK'; user: User }
  | { type: 'AUTH_ANON' }
  | { type: 'LINK_SENT'; email: string; devLink: string | null; note: string | null }
  | { type: 'BOOTSTRAP_START' }
  | { type: 'BOOTSTRAP_OK'; tasks: NTask[]; projects: NProject[]; clients: NClient[]; requirements: NRequirement[]; tenantId: string; userId: string; userInitial: string; userColor: string }
  | { type: 'BOOTSTRAP_ERR'; error: string }
  | { type: 'UPSERT_REQUIREMENT'; requirement: NRequirement }
  | { type: 'REMOVE_REQUIREMENT'; id: string }
  // inbox: a page (replace = first page / re-sync), the badge count, one row in or out
  | { type: 'INBOX_PAGE'; items: NSignal[]; cursor: string | null; replace: boolean }
  | { type: 'INBOX_LOADING'; loading: boolean }
  | { type: 'INBOX_ERROR'; error: string | null }
  | { type: 'INBOX_COUNT'; count: number }
  | { type: 'UPSERT_SIGNAL'; signal: NSignal }
  | { type: 'REMOVE_SIGNAL'; id: string }
  | { type: 'SET_INBOX_VIEW'; view: InboxMode }
  | { type: 'SELECT_INBOX'; id: string | null }
  | { type: 'TOGGLE_SELECT'; id: string }
  | { type: 'CLEAR_SELECTION' }
  | { type: 'LAND'; count: number }
  | { type: 'SET_ORIGINS'; taskId: string; origins: Origin[] }
  | { type: 'SET_OUTPUTS'; taskId: string; outputs: Output[] }
  | { type: 'SET_UNDO'; depth: number; label: string | null }
  | { type: 'UPSERT_TASK'; task: NTask }
  | { type: 'REMOVE_TASK'; id: string }
  | { type: 'UPSERT_PROJECT'; project: NProject }
  | { type: 'REMOVE_PROJECT'; id: string }
  | { type: 'UPSERT_CLIENT'; client: NClient }
  | { type: 'REMOVE_CLIENT'; id: string }
  | { type: 'SET_VIEW'; view: View }
  | { type: 'SET_DRAFT'; draft: string }
  | { type: 'SELECT_BACKLOG'; id: string | null }
  | { type: 'SELECT_PROJECT'; id: string | null }
  | { type: 'OPEN_EDITOR'; id: string | null; mode: 'edit' | 'create' }
  | { type: 'CLOSE_EDITOR' }
  | { type: 'SET_TOKENS_OPEN'; open: boolean }
  | { type: 'SET_TOAST'; toast: string | null }
  | { type: 'SET_HIGHLIGHT'; id: string | null }
  | { type: 'SET_CONNECTION'; state: ConnState }
  | { type: 'CLOCK_TICK' }
  | { type: 'SET_PLAN_ANCHOR'; anchor: number }
  | { type: 'SET_PLAN_MODE'; mode: 'week' | 'month' }
  | { type: 'SET_WORK_MODE'; mode: WorkMode }
  | { type: 'SET_PROOF'; id: string | null }
  | { type: 'SET_START'; id: string | null }
  | { type: 'NOTE_DISPOSITION'; promotedTaskId?: string };

function upsert<T extends { id: string }>(list: T[], item: T): T[] {
  const i = list.findIndex((x) => x.id === item.id);
  if (i === -1) return [...list, item];
  const next = list.slice();
  next[i] = item;
  return next;
}

/**
 * Put a signal row where its stage says it belongs: in the inbox list when
 * stage=inbox (newest first), out of it otherwise. The count follows the row
 * so the badge is right before the server's recount lands; selection and the
 * cursor row drop it when it leaves.
 */
function placeSignal(inbox: InboxState, signal: NSignal): InboxState {
  const had = inbox.items.some((s) => s.id === signal.id);
  const stays = signal.stage === 'inbox';
  if (!had && !stays) return inbox;
  const items = stays
    ? upsert(inbox.items, signal).sort(signalOrder)
    : inbox.items.filter((s) => s.id !== signal.id);
  const count = Math.max(0, inbox.count + (stays && !had ? 1 : !stays && had ? -1 : 0));
  return stays ? { ...inbox, items, count } : dropSignal({ ...inbox, items, count }, signal.id);
}

function dropSignal(inbox: InboxState, id: string): InboxState {
  const selection = inbox.selection.has(id) ? new Set([...inbox.selection].filter((x) => x !== id)) : inbox.selection;
  return { ...inbox, selection, selectedId: inbox.selectedId === id ? null : inbox.selectedId };
}

function reducer(state: RawState, action: Action): RawState {
  switch (action.type) {
    case 'AUTH_OK':
      return { ...state, authStatus: 'authed', user: action.user, linkSent: null, devLink: null, linkNote: null };
    case 'AUTH_ANON':
      return {
        ...initialState,
        authStatus: 'anon',
        // preserve a just-sent link prompt across the anon transition
        linkSent: state.linkSent,
        devLink: state.devLink,
        linkNote: state.linkNote,
      };
    case 'LINK_SENT':
      return {
        ...state,
        linkSent: action.email,
        // A cooled-down request carries no new link. Keep the one already on
        // screen (same address only) rather than blanking a link that works.
        devLink: action.devLink ?? (state.linkSent === action.email ? state.devLink : null),
        linkNote: action.note,
      };
    case 'BOOTSTRAP_START':
      return { ...state, status: 'loading' };
    case 'BOOTSTRAP_OK':
      return {
        ...state, status: 'ready', error: null, tasks: action.tasks, projects: action.projects, clients: action.clients,
        requirements: action.requirements,
        tenantId: action.tenantId, userId: action.userId, userInitial: action.userInitial, userColor: action.userColor,
      };
    case 'BOOTSTRAP_ERR':
      return { ...state, status: 'error', error: action.error };
    case 'UPSERT_REQUIREMENT':
      return { ...state, requirements: upsert(state.requirements, action.requirement) };
    case 'REMOVE_REQUIREMENT':
      // the server nulls requirementId on the items that served it; mirror that locally
      return {
        ...state,
        requirements: state.requirements.filter((r) => r.id !== action.id),
        tasks: state.tasks.map((t) => (t.requirementId === action.id ? { ...t, requirementId: null } : t)),
      };
    case 'INBOX_PAGE': {
      // a merged page never reorders what is already shown; a replace re-syncs the first page
      const items = action.replace
        ? action.items
        : action.items.reduce((acc, s) => upsert(acc, s), state.inbox.items).sort(signalOrder);
      return { ...state, inbox: { ...state.inbox, items, cursor: action.cursor, loading: false, error: null } };
    }
    case 'INBOX_LOADING':
      return { ...state, inbox: { ...state.inbox, loading: action.loading } };
    case 'INBOX_ERROR':
      return { ...state, inbox: { ...state.inbox, error: action.error } };
    case 'INBOX_COUNT':
      return { ...state, inbox: { ...state.inbox, count: action.count } };
    case 'UPSERT_SIGNAL':
      return { ...state, inbox: placeSignal(state.inbox, action.signal) };
    case 'REMOVE_SIGNAL': {
      const had = state.inbox.items.some((s) => s.id === action.id);
      if (!had) return state;
      const inbox = dropSignal({ ...state.inbox, items: state.inbox.items.filter((s) => s.id !== action.id), count: Math.max(0, state.inbox.count - 1) }, action.id);
      return { ...state, inbox };
    }
    case 'SET_INBOX_VIEW':
      return { ...state, inbox: { ...state.inbox, view: action.view } };
    case 'SELECT_INBOX':
      return { ...state, inbox: { ...state.inbox, selectedId: action.id } };
    case 'TOGGLE_SELECT': {
      const selection = new Set(state.inbox.selection);
      if (selection.has(action.id)) selection.delete(action.id);
      else selection.add(action.id);
      return { ...state, inbox: { ...state.inbox, selection } };
    }
    case 'CLEAR_SELECTION':
      return state.inbox.selection.size ? { ...state, inbox: { ...state.inbox, selection: new Set() } } : state;
    case 'LAND':
      // Inbox is the landing view while it has items; otherwise Work, the home
      // surface. A tab the user already picked (or a capture that navigated) wins.
      if (state.viewExplicit) return state;
      return { ...state, view: action.count > 0 ? 'inbox' : 'work' };
    case 'SET_ORIGINS':
      return { ...state, originsByTask: { ...state.originsByTask, [action.taskId]: action.origins } };
    case 'SET_OUTPUTS':
      return { ...state, outputsByTask: { ...state.outputsByTask, [action.taskId]: action.outputs } };
    case 'SET_UNDO':
      return { ...state, undoDepth: action.depth, undoLabel: action.label };
    case 'UPSERT_TASK':
      return { ...state, tasks: upsert(state.tasks, action.task) };
    case 'REMOVE_TASK':
      // a strip for an item that is gone has nothing to ask about
      return {
        ...state,
        tasks: state.tasks.filter((t) => t.id !== action.id),
        proofTaskId: state.proofTaskId === action.id ? null : state.proofTaskId,
        startTaskId: state.startTaskId === action.id ? null : state.startTaskId,
      };
    case 'UPSERT_PROJECT':
      return { ...state, projects: upsert(state.projects, action.project) };
    case 'REMOVE_PROJECT':
      return { ...state, projects: state.projects.filter((p) => p.id !== action.id) };
    case 'UPSERT_CLIENT':
      return { ...state, clients: upsert(state.clients, action.client) };
    case 'REMOVE_CLIENT':
      // a deleted client detaches its projects locally (server does the same)
      return {
        ...state,
        clients: state.clients.filter((c) => c.id !== action.id),
        projects: state.projects.map((p) => (p.clientId === action.id ? { ...p, clientId: null } : p)),
      };
    case 'SET_VIEW':
      return { ...state, view: action.view, viewExplicit: true };
    case 'SET_DRAFT':
      return { ...state, draft: action.draft };
    case 'SELECT_BACKLOG':
      return { ...state, selectedBacklogId: action.id };
    case 'SELECT_PROJECT':
      return { ...state, selectedProjectId: action.id };
    case 'OPEN_EDITOR':
      return { ...state, editorTaskId: action.id, editorMode: action.mode };
    case 'CLOSE_EDITOR':
      return { ...state, editorTaskId: null, editorMode: null };
    case 'SET_TOKENS_OPEN':
      return { ...state, tokensOpen: action.open };
    case 'SET_TOAST':
      return { ...state, toast: action.toast };
    case 'SET_HIGHLIGHT':
      return { ...state, highlightTaskId: action.id };
    case 'SET_CONNECTION':
      return { ...state, connection: action.state };
    case 'CLOCK_TICK':
      return { ...state, clockTick: state.clockTick + 1 };
    case 'SET_PLAN_ANCHOR':
      return { ...state, planAnchor: action.anchor };
    case 'SET_PLAN_MODE':
      return { ...state, planMode: action.mode };
    case 'SET_WORK_MODE':
      return { ...state, workMode: action.mode };
    case 'SET_PROOF':
      return { ...state, proofTaskId: action.id };
    case 'SET_START':
      return { ...state, startTaskId: action.id };
    case 'NOTE_DISPOSITION':
      return {
        ...state,
        session: {
          dispositions: state.session.dispositions + 1,
          promotedTaskIds: action.promotedTaskId ? [...state.session.promotedTaskIds, action.promotedTaskId] : state.session.promotedTaskIds,
        },
      };
    default:
      return state;
  }
}

/* ------------------------------------------------------------ derived view */

export interface AppState {
  authStatus: AuthStatus;
  user: User | null;
  linkSent: string | null;
  devLink: string | null;
  linkNote: string | null;
  status: LoadStatus;
  error: string | null;
  connection: ConnState;
  view: View;
  now: Now;
  planMode: 'week' | 'month';
  workMode: WorkMode;
  planLabel: string;
  isThisWeek: boolean;
  today: TodayItem[];
  week: WeekDay[];
  month: MonthGrid;
  weekDates: string[];
  backlog: BacklogTask[];
  backlogTotal: number;
  selectedBacklogId: string | null;
  project: Project | null;
  /** Active projects only — what every picker and filter offers. Archived ones live in `para`. */
  projects: { id: string; name: string; color: string; clientId: string | null }[];
  /** The Projects page's raw material: every project and every client/area, archived included. */
  para: { projects: NProject[]; areas: NClient[] };
  selectedProjectId: string | null;
  clients: { id: string; name: string; tier: ClientTier; kind: NClient['kind']; color: string; archivedAt: string | null }[];
  clientHealth: ClientHealth[];
  allTasks: TaskRow[];
  people: { userId: string; initial: string; color: string }[];
  requirements: NRequirement[];
  /** The inbox: flat list + its bundled fold, one key apart; `count` is the server total (badge). */
  inbox: {
    items: NSignal[]; // newest first — the flat chronological view
    bundles: InboxBundle<NSignal>[];
    count: number;
    remainder: number; // beyond the loaded pages — "and N more"
    hasMore: boolean;
    loading: boolean;
    /** The first page could not be read: `count` is unknown, not zero — offer `retryInbox`. */
    error: string | null;
    view: InboxMode;
    selectedId: string | null;
    selection: Set<string>;
  };
  originsByTask: Record<string, Origin[]>;
  outputsByTask: Record<string, Output[]>;
  /** What Cmd+Z would undo next (label) and how deep the stack is. */
  undo: { depth: number; label: string | null };
  insights: Insights;
  signals: Signal[];
  recentDone: DoneItem[];
  energyProfile: EnergyProfile;
  editorOpen: boolean;
  editorMode: 'edit' | 'create' | null;
  editingTask: NTask | null;
  tokensOpen: boolean;
  draft: { title: string };
  toast: string | null;
  highlightTaskId: string | null;
  /** The item whose proof strip is up (just completed), and the one whose start strip is up (just started). */
  proofFor: NTask | null;
  startFor: NTask | null;
  /** This page load's inbox work: dispositions so far and the items promoted here. */
  session: { dispositions: number; promotedTaskIds: string[] };
}

function derive(s: RawState): AppState {
  const backlog = selectBacklog(s.tasks, s.projects);
  const insights = computeInsights(s.tasks);
  const liveProjects = s.projects.filter((p) => !p.archivedAt);
  const activeProject = s.projects.find((p) => p.id === s.selectedProjectId) ?? liveProjects[0];
  const anchorDate = new Date(s.planAnchor);
  const wk = computeWeek(anchorDate);
  const wkStart = startOfWeek(anchorDate);
  const month = selectMonth(s.tasks, anchorDate);
  const isThisWeek = startOfWeek(new Date()).getTime() === wkStart.getTime();
  const planLabel = s.planMode === 'week' ? wk.label : month.label;

  // people pool = current user + everyone on any project
  const peopleMap = new Map<string, { userId: string; initial: string; color: string }>();
  if (s.userId) peopleMap.set(s.userId, { userId: s.userId, initial: s.userInitial, color: s.userColor });
  for (const p of s.projects) for (const m of p.members ?? []) peopleMap.set(m.userId, m);
  const people = [...peopleMap.values()];
  const inboxItems = s.inbox.items;

  return {
    authStatus: s.authStatus,
    user: s.user,
    linkSent: s.linkSent,
    devLink: s.devLink,
    linkNote: s.linkNote,
    status: s.status,
    error: s.error,
    connection: s.connection,
    view: s.view,
    now: computeNow(),
    planMode: s.planMode,
    workMode: s.workMode,
    planLabel,
    isThisWeek,
    today: selectToday(s.tasks, new Date()),
    week: selectWeek(s.tasks, wkStart),
    month,
    weekDates: wk.dates,
    backlog,
    backlogTotal: backlog.length,
    selectedBacklogId: s.selectedBacklogId ?? backlog[0]?.id ?? null,
    project: selectProject(s.tasks, activeProject),
    projects: liveProjects.map((p) => ({ id: p.id, name: p.name, color: p.color, clientId: p.clientId })),
    para: { projects: s.projects, areas: s.clients },
    selectedProjectId: activeProject?.id ?? null,
    clients: s.clients.map((c) => ({ id: c.id, name: c.name, tier: c.tier, kind: c.kind, color: c.color, archivedAt: c.archivedAt })),
    clientHealth: selectClientHealth(s.tasks, s.projects, s.clients, new Date()),
    allTasks: selectAllTasks(s.tasks, s.projects, s.clients, s.requirements),
    people,
    requirements: s.requirements,
    inbox: {
      items: inboxItems,
      bundles: selectInboxBundles(inboxItems, s.projects, s.clients, people),
      count: s.inbox.count,
      remainder: selectRemainder(s.inbox.count, inboxItems.length),
      hasMore: s.inbox.cursor != null,
      loading: s.inbox.loading,
      error: s.inbox.error,
      view: s.inbox.view,
      selectedId: s.inbox.selectedId ?? inboxItems[0]?.id ?? null,
      selection: s.inbox.selection,
    },
    originsByTask: s.originsByTask,
    outputsByTask: s.outputsByTask,
    undo: { depth: s.undoDepth, label: s.undoLabel },
    insights,
    signals: computeSignals(s.tasks, s.projects, s.clients, insights.profile, new Date()),
    recentDone: selectRecentDone(s.tasks, s.projects),
    energyProfile: insights.profile,
    editorOpen: s.editorMode != null,
    editorMode: s.editorMode,
    editingTask: s.editorMode === 'edit' ? (s.tasks.find((t) => t.id === s.editorTaskId) ?? null) : null,
    tokensOpen: s.tokensOpen,
    draft: { title: s.draft },
    toast: s.toast,
    highlightTaskId: s.highlightTaskId,
    proofFor: s.proofTaskId ? (s.tasks.find((t) => t.id === s.proofTaskId) ?? null) : null,
    startFor: s.startTaskId ? (s.tasks.find((t) => t.id === s.startTaskId) ?? null) : null,
    session: s.session,
  };
}

/* --------------------------------------------------------------- contexts */

const StateCtx = createContext<AppState | null>(null);
const ActionsCtx = createContext<Actions | null>(null);

export interface Actions {
  reload: () => void;
  /** Switch tabs. A parked view ('plan') redirects to Work rather than dead-ending. */
  go: (view: View | ParkedView) => void;
  setDraft: (title: string) => void;
  scheduleDraft: () => void;
  quickCapture: (title: string, opts?: { schedule?: boolean; important?: boolean; urgent?: boolean }) => void;
  toggleToday: (id: string) => void;
  selectBacklog: (id: string | null) => void;
  placeBacklog: (id: string, dayIndex: number, hour: number) => void;
  moveTaskToSlot: (id: string, dayIndex: number, hour: number) => void;
  setTaskProject: (id: string, projectId: string | null) => void;
  autoArrange: () => void;
  schedContext: () => { tasks: SchedTask[]; ctx: WeekCtx };
  applySchedule: (assignments: Assignment[]) => void;
  moveBoard: (id: string, column: 'backlog' | 'week' | 'focus' | 'done') => void;
  autoScheduleRemaining: () => void;
  deleteTask: (id: string) => void;
  openEditor: (id: string) => void;
  openCreate: () => void;
  closeEditor: () => void;
  saveTask: (id: string, patch: TaskPatch) => Promise<void>;
  setReflection: (id: string, text: string) => void;
  createTaskFull: (input: CreateTaskInput) => Promise<void>;
  createProject: (name: string, subtitle?: string) => void;
  /** The Projects page's creator: a project with its outcome, date and area; lands on it. */
  addProject: (input: CreateProjectInput) => void;
  /** Patch a project (outcome, due, area, archive…); optimistic, rolled back on failure. */
  patchProject: (id: string, patch: ProjectPatch) => void;
  /** Create a client or a PARA area. */
  addArea: (input: CreateClientInput) => Promise<NClient | null>;
  patchArea: (id: string, patch: ClientPatch) => void;
  /** One-line next action straight onto a project, without leaving the Projects page. */
  addProjectAction: (projectId: string, title: string) => void;
  selectProject: (id: string) => void;
  createClientForProject: (projectId: string, input: CreateClientInput) => void;
  assignProjectClient: (projectId: string, clientId: string | null) => void;
  planPrev: () => void;
  planNext: () => void;
  planToday: () => void;
  setPlanMode: (mode: 'week' | 'month') => void;
  /** List | Board on the Work tab; remembered per browser. */
  setWorkMode: (mode: WorkMode) => void;
  scheduleTaskAt: (id: string, dateStr: string, hour: number) => void;
  /**
   * Work's inline "when": put an item on a day without choosing an hour. The
   * day opens at 09:00, or at the next whole hour when that day is today.
   */
  scheduleTaskOnDay: (id: string, dateStr: string) => void;
  /** Take the "when" off an item: back to the backlog, nothing else touched. */
  unscheduleTask: (id: string) => void;
  requestMagicLink: (email: string) => Promise<void>;
  signOut: () => void;
  openTokens: () => void;
  closeTokens: () => void;
  clearToast: () => void;
  clearHighlight: () => void;

  // ---- inbox: capture, one-key dispositions (all undoable), promotion ----
  /** The single capture field (Cmd+K / paste box) → a signal in the inbox. */
  captureSignal: (input: CaptureInput) => Promise<NSignal | null>;
  /** One disposition, guarded by the row's version; pushes an undo entry and toasts. */
  dispose: (id: string, patch: Disposition) => Promise<void>;
  /** The same disposition across several rows — sequential PATCHes, one undo entry. */
  disposeMany: (ids: string[], patch: Disposition) => Promise<void>;
  /** One key → tomorrow 09:00 local; alternatives pass an explicit `until`. */
  snoozeSignal: (id: string, until?: Date) => Promise<void>;
  dismissSignal: (id: string) => Promise<void>;
  /** Signal → work item: creates the task (stage todo, origin attached, ask filled), then marks the signal promoted. */
  promoteSignal: (id: string, input: PromoteInput) => Promise<NTask | null>;
  /** Link the signal to an existing work item as its origin; the signal becomes `attached`. */
  attachSignal: (id: string, taskId: string) => Promise<void>;
  /** Cmd+Z: revert the most recent disposition/promotion; the toast names what it undid. */
  undo: () => Promise<void>;
  loadMoreInbox: () => Promise<void>;
  /** Re-read the first page + count after `inbox.error`; lands the user if they never picked a tab. */
  retryInbox: () => Promise<void>;
  setInboxView: (view: InboxMode) => void;
  selectInbox: (id: string | null) => void;
  toggleSelect: (id: string) => void;
  clearSelection: () => void;
  /** Pull facts (dates, asks, links, a suggested title/project) out of a signal's body. */
  extractFacts: (id: string) => Promise<void>;
  /** Replace a signal's extracted facts (a corrected chip); version-guarded, rolls back on failure. */
  setSignalFacts: (id: string, extracted: Record<string, unknown>) => Promise<void>;

  // ---- provenance caches + requirements ----
  loadOrigins: (taskId: string) => Promise<void>;
  loadOutputs: (taskId: string) => Promise<void>;
  addOutput: (taskId: string, input: CreateOutputInput) => Promise<void>;
  removeOutput: (taskId: string, outputId: string) => Promise<void>;
  createRequirement: (input: CreateRequirementInput) => Promise<NRequirement | null>;
  updateRequirement: (id: string, patch: RequirementPatch) => Promise<void>;
  deleteRequirement: (id: string) => Promise<void>;

  // ---- the single completion + start paths every surface calls ----
  /**
   * Stage → done, optimistically, then the proof strip opens on the item
   * (`proof: false` completes silently). Undoable: ⌘Z puts the stage back.
   */
  completeTask: (id: string, opts?: { proof?: boolean }) => Promise<void>;
  /** Done → back to where it was (scheduled or backlog); the undo path and the Today un-tick. */
  uncompleteTask: (id: string) => Promise<void>;
  /** Stage → doing. The start strip opens once per item (definition of done empty and never skipped). */
  startTask: (id: string) => Promise<void>;
  /** The proof strip's Enter: an output (when there is a reference) and the reflection. Done is already saved. */
  acceptProof: (id: string, proof: ProofInput) => Promise<void>;
  /** The proof strip's Escape / click-away: nothing recorded, nothing lost. */
  dismissProof: () => void;
  /** The start strip's Enter: definition of done + who it is for; the strip never returns for this item. */
  acceptStart: (id: string, input: StartInput) => Promise<void>;
  /** The start strip's Escape: remembered per item in this browser. */
  dismissStart: (id: string) => void;

  // ---- inbox-zero payoff: the heuristic placement, previewed then applied ----
  /** Where the heuristic would put these items this week (next week only once this one is spent). Pure: nothing is written. */
  previewPlacement: (ids: string[]) => Placement[];
  /** Write a previewed placement: one PATCH per item, one undo entry, and Plan opens on that week. */
  applyPlacement: (placements: Placement[]) => void;
}

/** What the proof strip records at completion; every field optional in practice. */
export interface ProofInput {
  kind: CreateOutputInput['kind'];
  ref: string; // a URL or a source-native locator; '' records no output
  title: string; // the output's name; falls back to the reference
  reflection: string; // "what did this advance?"
}

export interface StartInput {
  definitionOfDone: string;
  forWhom: string;
}

/** One item's heuristic slot: absolute time plus the energy band it lands in, for the preview line. */
export interface Placement {
  id: string;
  title: string;
  kind: Kind;
  at: string; // ISO local slot
  hour: number; // decimal local hour
  band: Band;
}

/** What the capture field sends: one field's worth, everything but `text`/`title` optional. */
export type CaptureInput = Pick<CreateSignalInput, 'kind' | 'title' | 'text' | 'participants' | 'links' | 'projectHint'>;

/** An inbox disposition: the target stage plus what that stage needs. */
export interface Disposition {
  stage: SignalStage;
  snoozedUntil?: string | null;
  projectHint?: string | null;
}

/**
 * Scope asked at promotion, prefilled and skippable: a work item without a
 * requirement is created "unscoped" (requirementId null) — visibly, never a
 * silent default. `expectedOutput` becomes the item's definition of done.
 */
export interface PromoteInput {
  title: string;
  projectId?: string | null;
  requirementId?: string | null;
  expectedOutput?: string;
  ask?: Ask;
  ownerId?: string | null; // "hand to someone": the item is theirs from the start
  askBy?: string | null; // ISO; prefilled from the strongest extracted deadline
}

const COLUMN_STATUS: Record<'backlog' | 'week' | 'focus' | 'done', NTask['status']> = {
  backlog: 'backlog',
  week: 'scheduled',
  focus: 'focus',
  done: 'done',
};

export function StoreProvider({ children }: { children: ReactNode }) {
  const [state, dispatch] = useReducer(reducer, initialState);
  const ref = useRef(state);
  ref.current = state;

  // ---- data load (also used to re-sync after a reconnect) ----
  const refresh = useRef(async (initial: boolean) => {
    try {
      const boot = await api.bootstrap();
      // Signals are never in bootstrap: the first inbox page + count ride
      // alongside it and, on the initial load, decide where the user lands —
      // settled before `ready` so the stage never flashes another view first.
      // A blip must not read as an empty inbox, so an unreachable inbox (null)
      // is retried once and then lands nowhere: the error state offers a retry
      // and the badge corrects itself on the next recount.
      const first = await loadInbox(dispatch);
      const count = first ?? (await loadInbox(dispatch));
      if (initial && count != null) dispatch({ type: 'LAND', count });
      dispatch({
        type: 'BOOTSTRAP_OK', tasks: boot.tasks, projects: boot.projects, clients: boot.clients, requirements: boot.requirements ?? [],
        tenantId: boot.tenant.id, userId: boot.user.id, userInitial: boot.user.initial, userColor: boot.user.color,
      });
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        dispatch({ type: 'AUTH_ANON' });
      } else if (initial) {
        dispatch({ type: 'BOOTSTRAP_ERR', error: e instanceof Error ? e.message : 'Failed to load' });
      }
    }
  });

  // ---- session check (consumes a magic-link token from the URL if present) ----
  const checkAuth = useRef(async () => {
    const url = new URL(window.location.href);
    const token = url.searchParams.get('token');
    if (token) {
      try {
        const { user } = await verifyOnce(token);
        window.history.replaceState({}, '', '/');
        dispatch({ type: 'AUTH_OK', user });
        return;
      } catch {
        // The token is spent. That is the failure case AND the success case:
        // our own earlier verify may have burned it and set the cookie. Ask
        // who we are before sending a signed-in user back to the login form.
        window.history.replaceState({}, '', '/');
        try {
          const { user } = await api.me();
          dispatch({ type: 'AUTH_OK', user });
        } catch {
          dispatch({ type: 'AUTH_ANON' });
        }
        return;
      }
    }
    try {
      const { user } = await api.me();
      dispatch({ type: 'AUTH_OK', user });
    } catch {
      dispatch({ type: 'AUTH_ANON' });
    }
  });
  useEffect(() => {
    void checkAuth.current();
  }, []);

  // once authed, load the tenant's data
  useEffect(() => {
    if (state.authStatus === 'authed' && state.status === 'idle') {
      dispatch({ type: 'BOOTSTRAP_START' });
      void refresh.current(true);
    }
  }, [state.authStatus, state.status]);

  // ---- realtime ----
  const firstOpen = useRef(true);
  useEffect(() => {
    if (state.status !== 'ready') return;
    firstOpen.current = true;
    const dispose = connectRealtime(realtimeURL(), {
      onState: (st) => {
        dispatch({ type: 'SET_CONNECTION', state: st });
        // On a (re)connect after the first, re-bootstrap to recover any events
        // missed while offline (the broker drops on a full buffer).
        if (st === 'open') {
          if (firstOpen.current) firstOpen.current = false;
          else void refresh.current(false);
        }
      },
      onEvent: (ev: ServerEvent) => applyEvent(dispatch, ref, ev),
    });
    return dispose;
  }, [state.status]);

  // keep the clock + planning week current
  useEffect(() => {
    const id = window.setInterval(() => dispatch({ type: 'CLOCK_TICK' }), 30_000);
    return () => window.clearInterval(id);
  }, []);

  // mirror the undo stack's depth/label so the Inbox can offer Cmd+Z honestly;
  // a sign-out empties it (its reverts belong to the old session)
  useEffect(() => undoStack.onChange((depth, label) => dispatch({ type: 'SET_UNDO', depth, label })), []);
  useEffect(() => {
    if (state.authStatus === 'anon') undoStack.clear();
  }, [state.authStatus]);

  const actions = useMemo<Actions>(() => makeActions(dispatch, ref), []);

  // ⌘Z everywhere, not just in the Inbox: a completion or a placement is as
  // reversible from Plan or Work as a disposition is from the gate. Fields keep
  // their native undo; a modal keeps the screen; the Inbox's own handler and
  // this one each yield to whichever ran first.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || !(e.metaKey || e.ctrlKey) || e.shiftKey || e.altKey || e.key.toLowerCase() !== 'z') return;
      const t = e.target as HTMLElement | null;
      if (t?.closest('input, textarea, select, [contenteditable="true"]') || modalIsOpen()) return;
      if (undoStack.size() === 0) return;
      e.preventDefault();
      void actions.undo();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [actions]);

  const derived = useMemo(() => derive(state), [state]);

  return (
    <StateCtx.Provider value={derived}>
      <ActionsCtx.Provider value={actions}>{children}</ActionsCtx.Provider>
    </StateCtx.Provider>
  );
}

/* ------------------------------------------------------- realtime applying */

function applyEvent(dispatch: React.Dispatch<Action>, ref: React.MutableRefObject<RawState>, ev: ServerEvent) {
  if (ev.type.startsWith('task.')) {
    if (ev.type === 'task.deleted') {
      dispatch({ type: 'REMOVE_TASK', id: ev.entityId });
      return;
    }
    if (!ev.task) return;
    const local = ref.current.tasks.find((t) => t.id === ev.task!.id);
    // version-aware: only apply if newer/equal (self-echoes are harmless no-ops)
    if (!local || ev.task.version >= local.version) {
      dispatch({ type: 'UPSERT_TASK', task: ev.task });
    }
  } else if (ev.type.startsWith('project.')) {
    if (ev.type === 'project.deleted') {
      dispatch({ type: 'REMOVE_PROJECT', id: ev.entityId });
      return;
    }
    if (ev.project) dispatch({ type: 'UPSERT_PROJECT', project: ev.project });
  } else if (ev.type.startsWith('client.')) {
    if (ev.type === 'client.deleted') {
      dispatch({ type: 'REMOVE_CLIENT', id: ev.entityId });
      return;
    }
    if (ev.client) dispatch({ type: 'UPSERT_CLIENT', client: ev.client });
  } else if (ev.type.startsWith('requirement.')) {
    if (ev.type === 'requirement.deleted') {
      dispatch({ type: 'REMOVE_REQUIREMENT', id: ev.entityId });
      return;
    }
    const r = ev.entity as NRequirement | undefined;
    if (r && r.id) dispatch({ type: 'UPSERT_REQUIREMENT', requirement: r });
  } else if (ev.type.startsWith('signal.')) {
    // signal.* events are metadata-only (never excerpt/body/participants):
    // refetch the row, then recount. A self-echo we already applied is skipped.
    if (ev.type === 'signal.deleted') {
      dispatch({ type: 'REMOVE_SIGNAL', id: ev.entityId });
      scheduleRecount(dispatch);
      return;
    }
    const meta = ev.entity as SignalMeta | undefined;
    const local = ref.current.inbox.items.find((s) => s.id === ev.entityId);
    if (local && meta && local.version >= meta.version && local.stage === meta.stage) return;
    if (meta && meta.stage !== 'inbox' && !local) {
      // left the inbox somewhere else and was never shown here: nothing to fetch
      scheduleRecount(dispatch);
      return;
    }
    api
      .getSignal(ev.entityId)
      .then((sig) => dispatch({ type: 'UPSERT_SIGNAL', signal: sig }))
      .catch(() => dispatch({ type: 'REMOVE_SIGNAL', id: ev.entityId }))
      .finally(() => scheduleRecount(dispatch));
  } else if (ev.type.startsWith('origin.')) {
    // entityId is the task; only a cache we already hold is refreshed
    if (ref.current.originsByTask[ev.entityId]) {
      api.listOrigins(ev.entityId).then(({ origins }) => dispatch({ type: 'SET_ORIGINS', taskId: ev.entityId, origins })).catch(() => {});
    }
  } else if (ev.type.startsWith('output.')) {
    const out = ev.entity as Partial<Output> | undefined;
    const taskId = out?.taskId ?? ev.entityId;
    if (ref.current.outputsByTask[taskId]) {
      api.listOutputs(taskId).then(({ outputs }) => dispatch({ type: 'SET_OUTPUTS', taskId, outputs })).catch(() => {});
    }
  }
}

/* ------------------------------------------------------------ inbox loading */

/**
 * First page + count for stage=inbox. Resolves to the count — 0 when the
 * server has no signal surface yet (an older server must not break the
 * bootstrap that just succeeded), and `null` when the inbox could not be read
 * at all. Null is not zero: a timed-out count must never look like an empty
 * inbox, so the caller keeps the error and decides nothing on it.
 */
async function loadInbox(dispatch: React.Dispatch<Action>): Promise<number | null> {
  dispatch({ type: 'INBOX_LOADING', loading: true });
  try {
    const [page, { count }] = await Promise.all([
      api.listSignals({ stage: 'inbox', limit: INBOX_PAGE }),
      api.countSignals({ stage: 'inbox' }),
    ]);
    dispatch({ type: 'INBOX_PAGE', items: page.items, cursor: page.nextCursor ?? null, replace: true });
    dispatch({ type: 'INBOX_COUNT', count });
    return count;
  } catch (e) {
    dispatch({ type: 'INBOX_LOADING', loading: false });
    // an older server has no signal surface at all: an empty inbox, honestly
    if (e instanceof ApiError && e.status === 404) {
      dispatch({ type: 'INBOX_ERROR', error: null });
      dispatch({ type: 'INBOX_COUNT', count: 0 });
      return 0;
    }
    dispatch({ type: 'INBOX_ERROR', error: e instanceof ApiError ? e.message : 'Could not load the inbox' });
    return null;
  }
}

// A burst of signal events (a connector sync) recounts once, not once per row.
let recountTimer: number | undefined;
function scheduleRecount(dispatch: React.Dispatch<Action>) {
  if (recountTimer) window.clearTimeout(recountTimer);
  recountTimer = window.setTimeout(() => {
    recountTimer = undefined;
    api.countSignals({ stage: 'inbox' }).then(({ count }) => dispatch({ type: 'INBOX_COUNT', count })).catch(() => {});
  }, 250);
}

/** The disposition a revert restores: exactly the mutable fields that left the inbox. */
function dispositionOf(s: NSignal): Disposition {
  return { stage: s.stage, snoozedUntil: s.snoozedUntil, projectHint: s.projectHint };
}

/** Human words for a disposition, for toasts and undo labels. */
function dispositionVerb(d: Disposition): string {
  switch (d.stage) {
    case 'snoozed': {
      const at = d.snoozedUntil ? new Date(d.snoozedUntil) : null;
      const tomorrow = tomorrow9am();
      if (at && at.getTime() === tomorrow.getTime()) return 'Snoozed until tomorrow 9:00';
      return at ? `Snoozed until ${at.toLocaleString('en-GB', { weekday: 'short', hour: 'numeric', minute: '2-digit' })}` : 'Snoozed';
    }
    case 'dismissed':
      return 'Dismissed';
    case 'attached':
      return 'Attached';
    case 'promoted':
      return 'Promoted';
    case 'inbox':
      return 'Back in inbox';
  }
}

/* ------------------------------------------------------------ action impls */

function makeActions(dispatch: React.Dispatch<Action>, ref: React.MutableRefObject<RawState>): Actions {
  const toast = (msg: string) => dispatch({ type: 'SET_TOAST', toast: msg });

  // optimistic update with rollback; resolves true once the server agreed
  const patchTask = async (id: string, patch: TaskPatch, optimistic: Partial<NTask>): Promise<boolean> => {
    const prev = ref.current.tasks.find((t) => t.id === id);
    if (!prev) return false;
    dispatch({ type: 'UPSERT_TASK', task: { ...prev, ...optimistic, version: prev.version + 1 } });
    try {
      const server = await api.updateTask(id, patch);
      dispatch({ type: 'UPSERT_TASK', task: server });
      return true;
    } catch (e) {
      dispatch({ type: 'UPSERT_TASK', task: prev });
      toast(e instanceof ApiError ? e.message : 'Update failed');
      return false;
    }
  };

  // The lifecycle a completion leaves behind, so ⌘Z can put it back exactly.
  const lifecycleOf = (t: NTask) => ({ status: t.status, stage: stageOf(t) });

  /**
   * The lifecycle pair to send when only the "when" is changing. The server
   * re-derives status/stage together, so a patch that names a status but no
   * stage resets the item to todo — which would knock in-flight work out of
   * Now the moment someone gave it a day. Keep the stage the item is already
   * in and pair it with the status that stage implies; `fallback` is the
   * status for an item that is merely queued (todo), which is the only stage
   * a "when" has any business moving.
   */
  const whenLifecycle = (t: NTask | undefined, fallback: NTask['status']) => {
    const stage = t ? stageOf(t) : 'todo';
    const status: NTask['status'] = stage === 'doing' ? 'focus' : stage === 'done' ? 'done' : stage === 'waiting' ? 'backlog' : fallback;
    return { status, stage };
  };

  /**
   * The one completion path. Stage → done now (optimistic), the proof strip
   * opens on the item at once, and the undo entry lands once the server has
   * agreed — so ⌘Z never "undoes" a completion that did not happen.
   */
  const completeTask = async (id: string, opts: { proof?: boolean } = {}) => {
    const prev = ref.current.tasks.find((t) => t.id === id);
    if (!prev || prev.status === 'done') return;
    const was = lifecycleOf(prev);
    if (opts.proof !== false) dispatch({ type: 'SET_PROOF', id });
    const ok = await patchTask(id, { status: 'done', stage: 'done' }, { status: 'done', stage: 'done', doneAt: new Date().toISOString() });
    if (!ok) {
      if (ref.current.proofTaskId === id) dispatch({ type: 'SET_PROOF', id: null });
      return;
    }
    undoStack.push({
      label: `Done · ${prev.title}`,
      revert: async () => {
        if (ref.current.proofTaskId === id) dispatch({ type: 'SET_PROOF', id: null });
        const reverted = await patchTask(id, { status: was.status, stage: was.stage }, { status: was.status, stage: was.stage, doneAt: null });
        if (!reverted) throw new Error('could not revert');
      },
    });
    toast(`Done · ${prev.title}${UNDO_HINT}`);
  };

  // Done → back where it was: a slot keeps it scheduled, otherwise the backlog.
  const uncompleteTask = async (id: string) => {
    const t = ref.current.tasks.find((x) => x.id === id);
    if (!t || t.status !== 'done') return;
    const status: NTask['status'] = t.scheduledAt != null ? 'scheduled' : 'backlog';
    if (ref.current.proofTaskId === id) dispatch({ type: 'SET_PROOF', id: null });
    await patchTask(id, { status, stage: 'todo' }, { status, stage: 'todo', doneAt: null });
  };

  /**
   * The one start path. Stage → doing; the start strip opens once per item —
   * skipped when done is already defined, or when it was waved away before.
   * An item already in focus (the Today hero) only gets the strip.
   */
  const startTask = async (id: string) => {
    const t = ref.current.tasks.find((x) => x.id === id);
    if (!t || t.status === 'done') return;
    const ask = !(t.definitionOfDone ?? '').trim() && !loadStartSeen().has(id);
    if (ask) dispatch({ type: 'SET_START', id });
    if (stageOf(t) === 'doing') {
      if (!ask) toast(`Already in focus · ${t.title}`);
      return;
    }
    const ok = await patchTask(id, { status: 'focus', stage: 'doing' }, { status: 'focus', stage: 'doing' });
    if (!ok) {
      if (ref.current.startTaskId === id) dispatch({ type: 'SET_START', id: null });
      return;
    }
    dispatch({ type: 'SET_HIGHLIGHT', id });
    toast(`Started · ${t.title}`);
  };

  // POST an output and keep the per-task cache honest (creating it if this is the first look).
  const addOutput = async (taskId: string, input: CreateOutputInput) => {
    try {
      const out = await api.createOutput(taskId, input);
      dispatch({ type: 'SET_OUTPUTS', taskId, outputs: [...(ref.current.outputsByTask[taskId] ?? []), out] });
      return true;
    } catch (e) {
      toast(e instanceof ApiError ? e.message : 'Could not record the output');
      return false;
    }
  };

  // optimistically re-point a project at a client (or null), rolling back on failure.
  // No If-Match version (last-write-wins, like patchTask): a rapid re-pick would
  // otherwise 409 against its own in-flight optimistic version bump.
  const setProjectClient = (projectId: string, clientId: string | null) => {
    const prev = ref.current.projects.find((p) => p.id === projectId);
    if (!prev) return;
    dispatch({ type: 'UPSERT_PROJECT', project: { ...prev, clientId, version: prev.version + 1 } });
    api
      .updateProject(projectId, { clientId })
      .then((p) => dispatch({ type: 'UPSERT_PROJECT', project: p }))
      .catch((e) => {
        dispatch({ type: 'UPSERT_PROJECT', project: prev });
        toast(e instanceof ApiError ? e.message : 'Could not update project');
      });
  };

  // ISO datetime for a (weekday-index, hour) within the currently displayed week
  const slotISO = (dayIndex: number, hour: number) =>
    combine(ymd(addDays(startOfWeek(new Date(ref.current.planAnchor)), dayIndex)), hour);

  // the user's learned energy profile, from their completion history
  const profileNow = (): EnergyProfile => deriveEnergyProfile(ref.current.tasks);

  // Optimistic disposition with rollback — the signal twin of patchTask, but
  // guarded by the row's version: a signal disposed elsewhere first answers
  // 409, and we show what the server has rather than our stale copy.
  const disposeSignal = async (id: string, patch: Disposition): Promise<{ prev: NSignal; server: NSignal } | null> => {
    const prev = ref.current.inbox.items.find((s) => s.id === id);
    if (!prev) return null;
    const body: SignalPatch = { stage: patch.stage };
    if (patch.snoozedUntil !== undefined) body.snoozedUntil = patch.snoozedUntil;
    if (patch.projectHint !== undefined) body.projectHint = patch.projectHint;
    dispatch({
      type: 'UPSERT_SIGNAL',
      signal: {
        ...prev,
        stage: patch.stage,
        snoozedUntil: patch.stage === 'snoozed' ? (patch.snoozedUntil ?? prev.snoozedUntil) : null,
        projectHint: patch.projectHint === undefined ? prev.projectHint : patch.projectHint,
        version: prev.version + 1,
      },
    });
    try {
      const server = await api.updateSignal(id, body, prev.version);
      dispatch({ type: 'UPSERT_SIGNAL', signal: server });
      return { prev, server };
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        api.getSignal(id)
          .then((s) => dispatch({ type: 'UPSERT_SIGNAL', signal: s }))
          .catch(() => dispatch({ type: 'REMOVE_SIGNAL', id }));
        toast('Changed elsewhere · refreshed');
      } else {
        dispatch({ type: 'UPSERT_SIGNAL', signal: prev });
        toast(e instanceof ApiError ? e.message : 'Update failed');
      }
      return null;
    }
  };

  // Revert = PATCH the previous disposition back, against the row's current
  // version (it moved when we disposed it, so the undo entry never trusts the
  // one it closed over).
  const restoreSignal = async (id: string, d: Disposition) => {
    const fresh = await api.getSignal(id);
    const server = await api.updateSignal(
      id,
      { stage: d.stage, snoozedUntil: d.snoozedUntil ?? null, projectHint: d.projectHint ?? null },
      fresh.version,
    );
    dispatch({ type: 'UPSERT_SIGNAL', signal: server });
  };

  // one disposition: optimistic PATCH, an undo entry, a toast that names it
  const dispose = async (id: string, patch: Disposition) => {
    const done = await disposeSignal(id, patch);
    if (!done) return;
    const { prev } = done;
    dispatch({ type: 'NOTE_DISPOSITION' });
    undoStack.push({ label: `${dispositionVerb(patch)} · ${prev.title}`, revert: () => restoreSignal(id, dispositionOf(prev)) });
    toast(`${dispositionVerb(patch)} · ${prev.title}${UNDO_HINT}`);
  };

  const slotPatch = (kind: Kind, fromDay: number, profile: EnergyProfile): { patch: TaskPatch; opt: Partial<NTask> } => {
    const slot = bestSlot(kind, fromDay, profile);
    const at = slotISO(slot.dayIndex, slot.hour);
    return {
      patch: { status: 'scheduled', scheduledAt: at },
      opt: { status: 'scheduled', scheduledAt: at },
    };
  };

  return {
    reload: () => window.location.reload(),
    // Plan is collapsed: scheduling lives inside Work now, so anything still
    // asking for it lands on Work instead of nowhere.
    go: (view) => dispatch({ type: 'SET_VIEW', view: view === 'plan' ? 'work' : view }),
    setDraft: (title) => dispatch({ type: 'SET_DRAFT', draft: title }),
    selectBacklog: (id) => dispatch({ type: 'SELECT_BACKLOG', id }),
    clearToast: () => dispatch({ type: 'SET_TOAST', toast: null }),
    clearHighlight: () => dispatch({ type: 'SET_HIGHLIGHT', id: null }),

    scheduleDraft: () => {
      const title = ref.current.draft.trim();
      if (!title) return;
      const inf = inferTask(title);
      const slot = bestSlot(inf.kind, 0, profileNow());
      const at = slotISO(slot.dayIndex, slot.hour);
      const input: CreateTaskInput = {
        title,
        kind: inf.kind,
        status: 'scheduled',
        effortMinutes: Math.round(inf.effortHrs * 60),
        scheduledAt: at,
        place: slot.place,
      };
      dispatch({ type: 'SET_VIEW', view: 'work' });
      dispatch({ type: 'SET_DRAFT', draft: '' });
      api
        .createTask(input)
        .then((task) => {
          dispatch({ type: 'UPSERT_TASK', task });
          dispatch({ type: 'SET_HIGHLIGHT', id: task.id });
          toast(`Scheduled · ${slot.dayName} ${clock(slot.hour)} · ${slot.place}`);
        })
        .catch((e) => toast(e instanceof ApiError ? e.message : 'Could not schedule'));
    },

    // Global quick-capture: infer a task's shape from its title and drop it
    // straight into the backlog — or, with `schedule`, into its best-fit slot.
    quickCapture: (title, opts = {}) => {
      const clean = title.trim();
      if (!clean) return;
      const inf = inferTask(clean);
      const input: CreateTaskInput = {
        title: clean,
        kind: inf.kind,
        status: opts.schedule ? 'scheduled' : 'backlog',
        effortMinutes: Math.round(inf.effortHrs * 60),
        important: opts.important ?? false,
        urgent: opts.urgent ?? false,
      };
      const slot = opts.schedule ? bestSlot(inf.kind, 0, profileNow()) : null;
      if (slot) {
        input.scheduledAt = slotISO(slot.dayIndex, slot.hour);
        input.place = slot.place;
        dispatch({ type: 'SET_VIEW', view: 'work' });
      }
      api
        .createTask(input)
        .then((task) => {
          dispatch({ type: 'UPSERT_TASK', task });
          dispatch({ type: 'SET_HIGHLIGHT', id: task.id });
          if (slot) toast(`Scheduled · ${slot.dayName} ${clock(slot.hour)} · ${slot.place}`);
          else toast(`Captured · ${clean}`);
        })
        .catch((e) => toast(e instanceof ApiError ? e.message : 'Could not capture'));
    },

    // Today's tick: done goes through the one completion path (proof strip and
    // all); un-ticking a done item puts it back where it was.
    toggleToday: (id) => {
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t) return;
      if (t.status === 'done') void uncompleteTask(id);
      else void completeTask(id);
    },

    placeBacklog: (id, dayIndex, hour) => {
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t) return;
      const at = slotISO(dayIndex, hour);
      void patchTask(id, { status: 'scheduled', scheduledAt: at }, { status: 'scheduled', scheduledAt: at });
      dispatch({ type: 'SET_HIGHLIGHT', id });
      dispatch({ type: 'SELECT_BACKLOG', id: null });
      toast(`Placed · ${['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'][dayIndex]} ${clock(hour)}`);
    },

    // Reschedule an already-placed task to a new (weekday, hour) within the
    // displayed week — the drag/drop + click-to-move calendar interaction.
    moveTaskToSlot: (id, dayIndex, hour) => {
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t) return;
      // A recurring task's scheduledAt is the series ANCHOR, and the week
      // shows every occurrence from that anchor onwards. Dragging one
      // occurrence must not move the anchor (that would drop every earlier
      // occurrence from the week), so for a series only the time-of-day
      // changes; the day it repeats on stays with the recurrence rule.
      const recurring = t.recurrence && t.recurrence !== 'none' && t.scheduledAt;
      const at = recurring ? combine(ymd(new Date(t.scheduledAt!)), hour) : slotISO(dayIndex, hour);
      if (at === t.scheduledAt) return; // no-op: dropped where it already was
      void patchTask(id, { status: 'scheduled', scheduledAt: at }, { status: 'scheduled', scheduledAt: at });
      dispatch({ type: 'SET_HIGHLIGHT', id });
      toast(
        recurring
          ? `Repeats at ${clock(hour)} · the series keeps its days`
          : `Moved · ${['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'][dayIndex]} ${clock(hour)}`,
      );
    },

    // Reassign a task to a different project (or none) — the inline Plan/Tasks
    // project picker. Optimistic, last-write-wins like the other quick edits.
    setTaskProject: (id, projectId) => {
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t || t.projectId === projectId) return;
      void patchTask(id, { projectId }, { projectId });
    },

    autoArrange: () => {
      const backlog = selectBacklog(ref.current.tasks, ref.current.projects);
      if (!backlog.length) return;
      const profile = profileNow();
      backlog.forEach((b, i) => {
        const { patch, opt } = slotPatch(b.kind, i % 5, profile);
        void patchTask(b.id, patch, opt);
      });
      dispatch({ type: 'SELECT_BACKLOG', id: null });
      toast(`Arranged ${backlog.length} tasks across your week`);
    },

    // Build the LLM scheduling context from raw tasks/projects + displayed week.
    schedContext: () => {
      const s = ref.current;
      const projById = new Map(s.projects.map((p) => [p.id, p]));
      const tasks: SchedTask[] = s.tasks
        .filter((t) => t.status === 'backlog' && t.scheduledAt == null)
        .sort((a, b) => priorityScore(b) - priorityScore(a) || a.position - b.position)
        .map((t) => ({
          id: t.id,
          title: t.title,
          kind: t.kind,
          effortHrs: t.effortMinutes / 60,
          urgent: t.urgent,
          important: t.important,
          deadline: t.deadline ? ymd(new Date(t.deadline)) : undefined,
          project: t.projectId ? projById.get(t.projectId)?.name : undefined,
        }));

      const weekStart = startOfWeek(new Date(s.planAnchor));
      const now = new Date();
      const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate());
      const days = Array.from({ length: 7 }, (_, i) => {
        const d = addDays(weekStart, i);
        const dayStart = new Date(d.getFullYear(), d.getMonth(), d.getDate());
        const dayEnd = addDays(dayStart, 1);
        let existing = 0;
        for (const t of s.tasks) {
          if (!t.scheduledAt || t.status === 'done') continue;
          existing += occurrences(t.scheduledAt, t.recurrence, dayStart, dayEnd).length;
        }
        return { index: i, name: DOW[i], date: String(d.getDate()), existing, isPast: dayStart < todayStart };
      });
      const todayIndex = days.findIndex((_, i) => sameDay(addDays(weekStart, i), now));
      const wkEnd = addDays(weekStart, 6);
      const fmt = (d: Date) => d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' });
      const p = deriveEnergyProfile(s.tasks);
      const ctx: WeekCtx = {
        weekLabel: `Week of ${fmt(weekStart)} – ${fmt(wkEnd)}`,
        todayIndex,
        days,
        energy: { peakStart: p.peakStart, peakEnd: p.peakEnd, dipHour: p.dipHour, learned: p.learned },
      };
      return { tasks, ctx };
    },

    applySchedule: (assignments) => {
      for (const a of assignments) {
        const at = slotISO(a.day, a.hour);
        void patchTask(a.id, { status: 'scheduled', scheduledAt: at }, { status: 'scheduled', scheduledAt: at });
      }
      dispatch({ type: 'SELECT_BACKLOG', id: null });
      if (assignments.length) {
        dispatch({ type: 'SET_HIGHLIGHT', id: assignments[0].id });
        toast(`Scheduled ${assignments.length} task${assignments.length === 1 ? '' : 's'} with AI`);
      }
    },

    moveBoard: (id, column) => {
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t) return;
      // the last two columns are the start and completion paths, not plain moves
      if (column === 'done') {
        void completeTask(id);
        return;
      }
      if (column === 'focus') {
        void startTask(id);
        return;
      }
      const status = COLUMN_STATUS[column];
      const patch: TaskPatch = { status };
      const opt: Partial<NTask> = { status };
      if (column === 'week' && t.scheduledAt == null) {
        const slot = bestSlot(t.kind, 0, profileNow());
        const at = slotISO(slot.dayIndex, slot.hour);
        patch.scheduledAt = at;
        opt.scheduledAt = at;
      }
      void patchTask(id, patch, opt);
      dispatch({ type: 'SET_HIGHLIGHT', id });
    },

    autoScheduleRemaining: () => {
      // the project on screen — the same fallback derive() uses for the board
      const live = ref.current.projects.filter((p) => !p.archivedAt);
      const project = ref.current.projects.find((p) => p.id === ref.current.selectedProjectId) ?? live[0];
      if (!project) return;
      const remaining = ref.current.tasks.filter((t) => t.projectId === project.id && t.status === 'backlog');
      if (!remaining.length) {
        toast('Everything is already scheduled');
        return;
      }
      const profile = profileNow();
      remaining.forEach((t, i) => {
        const { patch, opt } = slotPatch(t.kind, i % 5, profile);
        void patchTask(t.id, patch, opt);
      });
      toast(`Scheduled ${remaining.length} tasks into open focus slots`);
    },

    deleteTask: (id) => {
      const prev = ref.current.tasks.find((t) => t.id === id);
      if (!prev) return;
      dispatch({ type: 'REMOVE_TASK', id });
      dispatch({ type: 'CLOSE_EDITOR' });
      api.deleteTask(id).catch((e) => {
        dispatch({ type: 'UPSERT_TASK', task: prev });
        toast(e instanceof ApiError ? e.message : 'Delete failed');
      });
    },

    openEditor: (id) => dispatch({ type: 'OPEN_EDITOR', id, mode: 'edit' }),
    openCreate: () => dispatch({ type: 'OPEN_EDITOR', id: null, mode: 'create' }),
    closeEditor: () => dispatch({ type: 'CLOSE_EDITOR' }),

    // The editor's Save. A status flipped to done here is a completion like any
    // other: the drawer closes and the proof strip opens on the item wherever
    // it shows (or docked, when it is on no surface), with ⌘Z to take it back.
    saveTask: async (id, patch) => {
      const prev = ref.current.tasks.find((t) => t.id === id);
      const completing = !!prev && prev.status !== 'done' && (patch.status === 'done' || patch.stage === 'done');
      const was = prev ? lifecycleOf(prev) : null;
      const ok = await patchTask(id, patch, patch as Partial<NTask>);
      dispatch({ type: 'CLOSE_EDITOR' });
      if (!ok) return;
      if (completing && prev && was) {
        dispatch({ type: 'SET_PROOF', id });
        undoStack.push({
          label: `Done · ${prev.title}`,
          revert: async () => {
            if (ref.current.proofTaskId === id) dispatch({ type: 'SET_PROOF', id: null });
            const reverted = await patchTask(id, { status: was.status, stage: was.stage }, { status: was.status, stage: was.stage, doneAt: null });
            if (!reverted) throw new Error('could not revert');
          },
        });
        toast(`Done · ${prev.title}${UNDO_HINT}`);
        return;
      }
      toast('Saved');
    },

    // silent inline patch of the "what it advanced" reflection (Insights review)
    setReflection: (id, text) => {
      void patchTask(id, { reflection: text }, { reflection: text });
    },

    createTaskFull: async (input) => {
      try {
        const task = await api.createTask(input);
        dispatch({ type: 'UPSERT_TASK', task });
        dispatch({ type: 'SET_HIGHLIGHT', id: task.id });
        dispatch({ type: 'CLOSE_EDITOR' });
        // Work is where every item now lives: today's strip, the sections and
        // the register are all on it, so there is nowhere else to land.
        dispatch({ type: 'SET_VIEW', view: 'work' });
        toast(`Added · ${task.title}`);
      } catch (e) {
        toast(e instanceof ApiError ? e.message : 'Could not create task');
      }
    },

    createProject: (name, subtitle) => {
      api
        .createProject({ name, subtitle })
        .then((p) => {
          dispatch({ type: 'UPSERT_PROJECT', project: p });
          dispatch({ type: 'SELECT_PROJECT', id: p.id });
          // a new project opens on its own page, where its outcome and date get asked for
          dispatch({ type: 'SET_VIEW', view: 'projects' });
          toast(`Project created · ${p.name}`);
        })
        .catch((e) => toast(e instanceof ApiError ? e.message : 'Could not create project'));
    },

    addProject: (input) => {
      api
        .createProject(input)
        .then((p) => {
          dispatch({ type: 'UPSERT_PROJECT', project: p });
          dispatch({ type: 'SELECT_PROJECT', id: p.id });
          toast(`Project created · ${p.name}`);
        })
        .catch((e) => toast(e instanceof ApiError ? e.message : 'Could not create project'));
    },

    // last-write-wins like setProjectClient: no If-Match, so quick successive
    // edits to one project never 409 against their own optimistic bump
    patchProject: (id, patch) => {
      const prev = ref.current.projects.find((p) => p.id === id);
      if (!prev) return;
      const { archived, ...fields } = patch;
      const optimistic: NProject = { ...prev, ...fields, version: prev.version + 1 };
      if (archived !== undefined) optimistic.archivedAt = archived ? (prev.archivedAt ?? new Date().toISOString()) : null;
      dispatch({ type: 'UPSERT_PROJECT', project: optimistic });
      api
        .updateProject(id, patch)
        .then((p) => {
          dispatch({ type: 'UPSERT_PROJECT', project: p });
          if (archived !== undefined) toast(archived ? `Archived · ${p.name}` : `Restored · ${p.name}`);
        })
        .catch((e) => {
          dispatch({ type: 'UPSERT_PROJECT', project: prev });
          toast(e instanceof ApiError ? e.message : 'Could not update project');
        });
    },

    addArea: async (input) => {
      try {
        const c = await api.createClient(input);
        dispatch({ type: 'UPSERT_CLIENT', client: c });
        toast(`${c.kind === 'area' ? 'Area' : 'Client'} added · ${c.name}`);
        return c;
      } catch (e) {
        toast(e instanceof ApiError ? e.message : 'Could not add area');
        return null;
      }
    },

    patchArea: (id, patch) => {
      const prev = ref.current.clients.find((c) => c.id === id);
      if (!prev) return;
      const { archived, ...fields } = patch;
      const optimistic: NClient = { ...prev, ...fields, version: prev.version + 1 };
      if (archived !== undefined) optimistic.archivedAt = archived ? (prev.archivedAt ?? new Date().toISOString()) : null;
      dispatch({ type: 'UPSERT_CLIENT', client: optimistic });
      api
        .updateClient(id, patch)
        .then((c) => dispatch({ type: 'UPSERT_CLIENT', client: c }))
        .catch((e) => {
          dispatch({ type: 'UPSERT_CLIENT', client: prev });
          toast(e instanceof ApiError ? e.message : 'Could not update area');
        });
    },

    addProjectAction: (projectId, title) => {
      const t = title.trim();
      if (!t) return;
      const guess = inferTask(t);
      api
        .createTask({ title: t, projectId, kind: guess.kind, effortMinutes: Math.round(guess.effortHrs * 60) })
        .then((task) => {
          dispatch({ type: 'UPSERT_TASK', task });
          dispatch({ type: 'SET_HIGHLIGHT', id: task.id });
        })
        .catch((e) => toast(e instanceof ApiError ? e.message : 'Could not add the action'));
    },

    selectProject: (id) => dispatch({ type: 'SELECT_PROJECT', id }),

    createClientForProject: (projectId, input) => {
      api
        .createClient(input)
        .then((c) => {
          dispatch({ type: 'UPSERT_CLIENT', client: c });
          setProjectClient(projectId, c.id);
          toast(`Client added · ${c.name}`);
        })
        .catch((e) => toast(e instanceof ApiError ? e.message : 'Could not add client'));
    },

    assignProjectClient: (projectId, clientId) => setProjectClient(projectId, clientId),

    planPrev: () => {
      const d = new Date(ref.current.planAnchor);
      if (ref.current.planMode === 'week') d.setDate(d.getDate() - 7);
      else d.setMonth(d.getMonth() - 1);
      dispatch({ type: 'SET_PLAN_ANCHOR', anchor: d.getTime() });
    },
    planNext: () => {
      const d = new Date(ref.current.planAnchor);
      if (ref.current.planMode === 'week') d.setDate(d.getDate() + 7);
      else d.setMonth(d.getMonth() + 1);
      dispatch({ type: 'SET_PLAN_ANCHOR', anchor: d.getTime() });
    },
    planToday: () => dispatch({ type: 'SET_PLAN_ANCHOR', anchor: Date.now() }),
    setPlanMode: (mode) => dispatch({ type: 'SET_PLAN_MODE', mode }),
    setWorkMode: (mode) => {
      dispatch({ type: 'SET_WORK_MODE', mode });
      try {
        localStorage.setItem(WORK_MODE_KEY, mode);
      } catch {
        /* private mode / storage blocked: the choice lasts the session */
      }
    },

    scheduleTaskAt: (id, dateStr, hour) => {
      const at = combine(dateStr, hour);
      void patchTask(id, { status: 'scheduled', scheduledAt: at }, { status: 'scheduled', scheduledAt: at });
      dispatch({ type: 'SET_HIGHLIGHT', id });
      toast('Scheduled');
    },

    // A day is all Work's "when" control asks for. Today opens at the next
    // whole hour (never a slot that has already gone, never past 17:00); any
    // other day opens at 09:00. No energy fit is consulted — this is a person
    // saying when they will do it, not the app arranging their day.
    scheduleTaskOnDay: (id, dateStr) => {
      const now = new Date();
      const isToday = dateStr === ymd(now);
      const hour = isToday ? Math.min(17, Math.max(9, Math.floor(hourOf(now)) + 1)) : 9;
      const at = combine(dateStr, hour);
      // Only the "when" moves — the stage does not. Sending a status without a
      // stage would let the server re-derive the pair and quietly reset an
      // in-flight item to todo, dropping it out of Now mid-work.
      const lc = whenLifecycle(ref.current.tasks.find((x) => x.id === id), 'scheduled');
      void patchTask(id, { ...lc, scheduledAt: at }, { ...lc, scheduledAt: at });
      dispatch({ type: 'SET_HIGHLIGHT', id });
      const d = new Date(at);
      const day = isToday ? 'today' : sameDay(d, addDays(now, 1)) ? 'tomorrow' : `${DOW[(d.getDay() + 6) % 7]} ${d.getDate()} ${MONTHS_SHORT[d.getMonth()]}`;
      toast(`On ${day} · ${clock(hour)}`);
    },

    unscheduleTask: (id) => {
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t || (t.scheduledAt == null && t.status === 'backlog')) return;
      const lc = whenLifecycle(t, 'backlog');
      void patchTask(id, { ...lc, scheduledAt: null }, { ...lc, scheduledAt: null });
      toast('When cleared');
    },

    requestMagicLink: async (email) => {
      try {
        const res = await api.requestLink(email);
        // The server answers a per-address cooldown with the same 200 and no
        // link, so nothing is leaked about who has an account. Once we have
        // seen one dev link we know dev auth is on, so a later empty response
        // can only be the cooldown — say so instead of showing a dead end.
        if (res.devLink) devAuthSeen = true;
        const cooled = !res.devLink && devAuthSeen;
        dispatch({
          type: 'LINK_SENT',
          email: res.email,
          devLink: res.devLink ?? null,
          note: cooled ? 'A link went out moments ago — the server holds off for a minute before sending another. The link below still works.' : null,
        });
      } catch (e) {
        toast(e instanceof ApiError ? e.message : 'Could not send the link');
      }
    },

    signOut: () => {
      api.logout().finally(() => dispatch({ type: 'AUTH_ANON' }));
    },

    openTokens: () => dispatch({ type: 'SET_TOKENS_OPEN', open: true }),
    closeTokens: () => dispatch({ type: 'SET_TOKENS_OPEN', open: false }),

    /* ------------------------------------------------------------ inbox */

    captureSignal: async (input) => {
      const text = input.text?.trim() ?? '';
      const title = input.title?.trim() ?? '';
      if (!text && !title) return null;
      try {
        const sig = await api.createSignal({ ...input, kind: input.kind ?? 'text', text, title: title || undefined });
        dispatch({ type: 'UPSERT_SIGNAL', signal: sig });
        toast(`Captured · ${sig.title}`);
        return sig;
      } catch (e) {
        toast(e instanceof ApiError ? e.message : 'Could not capture');
        return null;
      }
    },

    dispose,

    disposeMany: async (ids, patch) => {
      const restored: Array<{ id: string; prev: NSignal }> = [];
      for (const id of ids) {
        const done = await disposeSignal(id, patch); // sequential: one PATCH at a time, in order
        if (done) restored.push({ id, prev: done.prev });
      }
      dispatch({ type: 'CLEAR_SELECTION' });
      if (!restored.length) return;
      dispatch({ type: 'NOTE_DISPOSITION' });
      const label = restored.length === 1 ? `${dispositionVerb(patch)} · ${restored[0].prev.title}` : `${dispositionVerb(patch)} · ${restored.length} signals`;
      undoStack.push({
        label,
        revert: async () => {
          for (const { id, prev } of restored) await restoreSignal(id, dispositionOf(prev));
        },
      });
      toast(`${label}${UNDO_HINT}`);
    },

    snoozeSignal: (id, until = tomorrow9am()) => dispose(id, { stage: 'snoozed', snoozedUntil: until.toISOString() }),
    dismissSignal: (id) => dispose(id, { stage: 'dismissed' }),

    promoteSignal: async (id, input) => {
      const sig = ref.current.inbox.items.find((s) => s.id === id);
      const title = input.title.trim();
      if (!sig || !title) return null;
      const inf = inferTask(title);
      // `undefined` means the strip never asked — fall back to the capture's
      // hint. An explicit null is the user choosing "No project" and survives.
      const projectId = input.projectId !== undefined ? input.projectId : (sig.projectHint ?? null);
      const unscoped = !input.requirementId;
      // optimistic: the signal leaves the inbox now; the task lands when the server says so
      dispatch({ type: 'UPSERT_SIGNAL', signal: { ...sig, stage: 'promoted', version: sig.version + 1 } });
      let task: NTask | null = null;
      let attached = false; // the origin landed: the server has already moved the signal
      try {
        task = await api.createTask({
          title,
          kind: inf.kind,
          effortMinutes: Math.round(inf.effortHrs * 60),
          stage: 'todo',
          status: 'backlog',
          projectId,
          requirementId: input.requirementId ?? null,
          definitionOfDone: input.expectedOutput?.trim() || undefined,
          ask: input.ask,
          ownerId: input.ownerId || undefined,
          askBy: input.askBy || undefined,
        });
        dispatch({ type: 'UPSERT_TASK', task });
        // attaching the origin already disposes the signal (→ attached); the
        // final PATCH names the disposition honestly and keeps the project hint
        await api.attachOrigin(task.id, id);
        attached = true;
        const server = await api.updateSignal(id, { stage: 'promoted', projectHint: projectId });
        dispatch({ type: 'UPSERT_SIGNAL', signal: server });
        const created = task;
        undoStack.push({
          label: `Promoted · ${title}`,
          revert: async () => {
            // Undo takes back the promotion, never the work done since. If the
            // item has been edited, given outputs, or attached to another
            // signal, deleting it would destroy all of that (outputs and
            // origins cascade), so the revert is refused and says so.
            const now = ref.current.tasks.find((t) => t.id === created.id);
            if (now) {
              const [{ outputs }, { origins }] = await Promise.all([api.listOutputs(created.id), api.listOrigins(created.id)]);
              if (now.version !== created.version || outputs.length > 0 || origins.some((o) => o.signalId !== id)) {
                throw new UndoBlocked('Task has changed since it was promoted');
              }
            }
            // signal first: a signal left promoted with no task behind it is
            // invisible everywhere, while a task without its origin is not
            await restoreSignal(id, dispositionOf(sig));
            if (now) {
              await api.deleteTask(created.id); // the origin goes with it
              dispatch({ type: 'REMOVE_TASK', id: created.id });
            }
          },
        });
        dispatch({ type: 'SET_HIGHLIGHT', id: task.id });
        dispatch({ type: 'NOTE_DISPOSITION', promotedTaskId: task.id });
        toast(`Promoted · ${title}${unscoped ? ' · unscoped' : ''}${UNDO_HINT}`);
        return task;
      } catch (e) {
        // A failed promotion must leave nothing behind: undo whatever got
        // through, in reverse. Deleting the task cascades the origin row, but
        // the attach has already disposed the signal server-side (→ attached,
        // version bumped), so its disposition is PATCHed back against a freshly
        // read version — otherwise the row sits attached to nothing, out of the
        // inbox, and the next key press 409s on a stale version.
        if (task) {
          await api.deleteTask(task.id).catch(() => {});
          dispatch({ type: 'REMOVE_TASK', id: task.id });
        }
        const restored = attached
          ? await restoreSignal(id, dispositionOf(sig)).then(
              () => true,
              () => false,
            )
          : false;
        if (!restored) dispatch({ type: 'UPSERT_SIGNAL', signal: sig });
        toast(e instanceof ApiError ? e.message : 'Could not promote');
        return null;
      }
    },

    attachSignal: async (id, taskId) => {
      const sig = ref.current.inbox.items.find((s) => s.id === id);
      const task = ref.current.tasks.find((t) => t.id === taskId);
      if (!sig || !task) return;
      dispatch({ type: 'UPSERT_SIGNAL', signal: { ...sig, stage: 'attached', version: sig.version + 1 } });
      try {
        const origin = await api.attachOrigin(taskId, id);
        const cached = ref.current.originsByTask[taskId];
        if (cached) dispatch({ type: 'SET_ORIGINS', taskId, origins: [...cached.filter((o) => o.signalId !== id), origin] });
        // the store disposes on attach; an adapter that does not gets the explicit PATCH
        let server = await api.getSignal(id);
        if (server.stage !== 'attached') server = await api.updateSignal(id, { stage: 'attached' }, server.version);
        dispatch({ type: 'UPSERT_SIGNAL', signal: server });
        undoStack.push({
          label: `Attached · ${sig.title}`,
          revert: async () => {
            await api.detachOrigin(taskId, id);
            const c = ref.current.originsByTask[taskId];
            if (c) dispatch({ type: 'SET_ORIGINS', taskId, origins: c.filter((o) => o.signalId !== id) });
            await restoreSignal(id, dispositionOf(sig));
          },
        });
        dispatch({ type: 'NOTE_DISPOSITION' });
        toast(`Attached · ${sig.title} → ${task.title}${UNDO_HINT}`);
      } catch (e) {
        dispatch({ type: 'UPSERT_SIGNAL', signal: sig });
        toast(e instanceof ApiError ? e.message : 'Could not attach');
      }
    },

    undo: async () => {
      const entry = undoStack.pop();
      if (!entry) {
        toast('Nothing to undo');
        return;
      }
      try {
        await entry.revert();
        toast(`Undid · ${entry.label}`);
      } catch (e) {
        // a refused revert says why in its own words, and stays popped: the
        // world has moved on, so the entry will never apply again
        if (e instanceof UndoBlocked) toast(e.message);
        else toast(e instanceof ApiError ? `Could not undo · ${e.message}` : 'Could not undo');
      }
    },

    loadMoreInbox: async () => {
      const { cursor, loading } = ref.current.inbox;
      if (!cursor || loading) return;
      dispatch({ type: 'INBOX_LOADING', loading: true });
      try {
        const page = await api.listSignals({ stage: 'inbox', limit: INBOX_PAGE, cursor });
        dispatch({ type: 'INBOX_PAGE', items: page.items, cursor: page.nextCursor ?? null, replace: false });
      } catch (e) {
        dispatch({ type: 'INBOX_LOADING', loading: false });
        toast(e instanceof ApiError ? e.message : 'Could not load more');
      }
    },
    // The way back from a failed first load: read the page again and, if the
    // user has not picked a tab since, make the landing decision that the
    // failure could not make.
    retryInbox: async () => {
      const count = await loadInbox(dispatch);
      if (count == null) {
        toast('Could not load the inbox');
        return;
      }
      dispatch({ type: 'LAND', count });
    },
    setInboxView: (view) => dispatch({ type: 'SET_INBOX_VIEW', view }),
    selectInbox: (id) => dispatch({ type: 'SELECT_INBOX', id }),
    toggleSelect: (id) => dispatch({ type: 'TOGGLE_SELECT', id }),
    clearSelection: () => dispatch({ type: 'CLEAR_SELECTION' }),

    // The extraction endpoint is being built alongside this client: its
    // response is read defensively (a Signal, a {signal}, or just {extracted}),
    // and a 404 means the server does not offer it yet — say so, don't fail.
    extractFacts: async (id) => {
      try {
        const res = await api.extractSignal(id);
        const asSignal = (v: unknown): v is NSignal => !!v && typeof v === 'object' && 'id' in v && 'stage' in v && 'version' in v;
        let signal: NSignal | null = null;
        if (asSignal(res)) signal = res;
        else if (asSignal(res?.signal)) signal = res.signal;
        else if (res?.extracted) {
          const local = ref.current.inbox.items.find((s) => s.id === id);
          signal = local ? { ...local, extracted: res.extracted } : await api.getSignal(id);
        } else signal = await api.getSignal(id);
        dispatch({ type: 'UPSERT_SIGNAL', signal });
        toast('Facts extracted');
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) toast('Fact extraction is not available on this server yet');
        else toast(e instanceof ApiError ? e.message : 'Could not extract facts');
      }
    },

    // A chip correction PATCHes the whole `extracted` object (the server
    // replaces it, never merges a patch), guarded by the row's version.
    setSignalFacts: async (id, extracted) => {
      const prev = ref.current.inbox.items.find((s) => s.id === id);
      if (!prev) return;
      dispatch({ type: 'UPSERT_SIGNAL', signal: { ...prev, extracted, version: prev.version + 1 } });
      try {
        const server = await api.updateSignal(id, { extracted }, prev.version);
        dispatch({ type: 'UPSERT_SIGNAL', signal: server });
      } catch (e) {
        dispatch({ type: 'UPSERT_SIGNAL', signal: prev });
        toast(e instanceof ApiError ? e.message : 'Could not save the correction');
      }
    },

    /* ----------------------------------------- provenance + requirements */

    loadOrigins: async (taskId) => {
      try {
        const { origins } = await api.listOrigins(taskId);
        dispatch({ type: 'SET_ORIGINS', taskId, origins });
      } catch {
        /* the item may be gone; the panel shows nothing */
      }
    },
    loadOutputs: async (taskId) => {
      try {
        const { outputs } = await api.listOutputs(taskId);
        dispatch({ type: 'SET_OUTPUTS', taskId, outputs });
      } catch {
        /* as above */
      }
    },
    addOutput: async (taskId, input) => {
      await addOutput(taskId, input);
    },
    removeOutput: async (taskId, outputId) => {
      const prev = ref.current.outputsByTask[taskId] ?? [];
      dispatch({ type: 'SET_OUTPUTS', taskId, outputs: prev.filter((o) => o.id !== outputId) });
      try {
        await api.deleteOutput(taskId, outputId);
      } catch (e) {
        dispatch({ type: 'SET_OUTPUTS', taskId, outputs: prev });
        toast(e instanceof ApiError ? e.message : 'Could not remove the output');
      }
    },

    createRequirement: async (input) => {
      try {
        const r = await api.createRequirement(input);
        dispatch({ type: 'UPSERT_REQUIREMENT', requirement: r });
        return r;
      } catch (e) {
        toast(e instanceof ApiError ? e.message : 'Could not add the requirement');
        return null;
      }
    },
    updateRequirement: async (id, patch) => {
      const prev = ref.current.requirements.find((r) => r.id === id);
      if (!prev) return;
      dispatch({ type: 'UPSERT_REQUIREMENT', requirement: { ...prev, ...patch, archivedAt: prev.archivedAt, version: prev.version + 1 } });
      try {
        const r = await api.updateRequirement(id, patch);
        dispatch({ type: 'UPSERT_REQUIREMENT', requirement: r });
      } catch (e) {
        dispatch({ type: 'UPSERT_REQUIREMENT', requirement: prev });
        toast(e instanceof ApiError ? e.message : 'Update failed');
      }
    },
    deleteRequirement: async (id) => {
      const prev = ref.current.requirements.find((r) => r.id === id);
      if (!prev) return;
      dispatch({ type: 'REMOVE_REQUIREMENT', id });
      try {
        await api.deleteRequirement(id);
      } catch (e) {
        dispatch({ type: 'UPSERT_REQUIREMENT', requirement: prev });
        toast(e instanceof ApiError ? e.message : 'Delete failed');
      }
    },

    /* --------------------------------------------- completion + start */

    completeTask,
    uncompleteTask,
    startTask,

    acceptProof: async (id, proof) => {
      if (ref.current.proofTaskId === id) dispatch({ type: 'SET_PROOF', id: null });
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t) return;
      const ref_ = proof.ref.trim();
      const title = proof.title.trim() || ref_;
      const reflection = proof.reflection.trim();
      const jobs: Promise<boolean>[] = [];
      if (title) jobs.push(addOutput(id, { kind: proof.kind, title, url: ref_ || undefined }));
      if (reflection && reflection !== t.reflection) jobs.push(patchTask(id, { reflection }, { reflection }));
      if (!jobs.length) return;
      const results = await Promise.all(jobs);
      if (results.every(Boolean)) toast(`Proof · ${t.title}`);
    },
    dismissProof: () => dispatch({ type: 'SET_PROOF', id: null }),

    acceptStart: async (id, input) => {
      if (ref.current.startTaskId === id) dispatch({ type: 'SET_START', id: null });
      markStartSeen(id);
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t) return;
      const dod = input.definitionOfDone.trim();
      const forWhom = input.forWhom.trim();
      const patch: TaskPatch = {};
      if (dod) patch.definitionOfDone = dod;
      if (forWhom && forWhom !== (t.ask?.forWhom ?? '')) patch.ask = { what: t.ask?.what ?? '', forWhom, why: t.ask?.why ?? '' };
      if (!Object.keys(patch).length) return;
      await patchTask(id, patch, patch as Partial<NTask>);
    },
    dismissStart: (id) => {
      markStartSeen(id);
      if (ref.current.startTaskId === id) dispatch({ type: 'SET_START', id: null });
    },

    /* ---------------------------------------------- inbox-zero payoff */

    // The same heuristic quick-arrange uses (bestSlot on the learned profile),
    // but forward-looking and collision-aware: never a past hour, never on
    // top of something already placed, and only once the week is genuinely
    // spent (a weekend, or Friday evening) does it plan next Monday on.
    previewPlacement: (ids) => {
      const s = ref.current;
      const profile = profileNow();
      const now = new Date();
      let weekStart = startOfWeek(now);
      let fromDay = (now.getDay() + 6) % 7;
      // Only a Friday evening or a weekend has no room left this week: an
      // earlier weekday evening still has tomorrow through Friday, and the
      // past-hour guard below keeps today's gone hours out of it.
      const thisWeek = fromDay < 4 || (fromDay === 4 && hourOf(now) < 17.5);
      if (!thisWeek) {
        weekStart = addDays(weekStart, 7);
        fromDay = 0;
      }
      const todayIdx = thisWeek ? fromDay : -1;
      const weekEnd = addDays(weekStart, 7);
      const occupied = new Set<string>(); // `${day}:${wholeHour}`
      for (const t of s.tasks) {
        if (!t.scheduledAt || t.status === 'done') continue;
        for (const occ of occurrences(t.scheduledAt, t.recurrence, weekStart, weekEnd)) {
          const d = Math.round((new Date(occ.getFullYear(), occ.getMonth(), occ.getDate()).getTime() - weekStart.getTime()) / 86400000);
          occupied.add(`${d}:${Math.floor(hourOf(occ))}`);
        }
      }
      const items = ids
        .map((id) => s.tasks.find((t) => t.id === id))
        .filter((t): t is NTask => !!t)
        .sort((a, b) => priorityScore(b) - priorityScore(a) || a.position - b.position);
      const span = 5 - fromDay;
      const out: Placement[] = [];
      items.forEach((t, i) => {
        const slot = bestSlot(t.kind, fromDay + (i % span), profile);
        let d = slot.dayIndex;
        let h = slot.hour;
        const taken = (dd: number, hh: number) => occupied.has(`${dd}:${Math.floor(hh)}`) || (dd === todayIdx && hh <= hourOf(now) + 0.25);
        let guard = 0;
        while (taken(d, h) && guard++ < 60) {
          h = Math.floor(h) + 1;
          if (h > 18) {
            h = 8;
            d = d + 1 > 4 ? fromDay : d + 1;
          }
        }
        occupied.add(`${d}:${Math.floor(h)}`);
        out.push({ id: t.id, title: t.title, kind: t.kind, at: combine(ymd(addDays(weekStart, d)), h), hour: h, band: bandAt(h, profile) });
      });
      return out;
    },

    applyPlacement: (placements) => {
      const prevs = placements
        .map((p) => ref.current.tasks.find((t) => t.id === p.id))
        .filter((t): t is NTask => !!t)
        .map((t) => ({ id: t.id, status: t.status, scheduledAt: t.scheduledAt }));
      if (!prevs.length) return;
      for (const p of placements) void patchTask(p.id, { status: 'scheduled', scheduledAt: p.at }, { status: 'scheduled', stage: 'todo', scheduledAt: p.at });
      const n = prevs.length;
      undoStack.push({
        label: `Gave ${n} a time`,
        revert: async () => {
          for (const prev of prevs) await patchTask(prev.id, { status: prev.status, scheduledAt: prev.scheduledAt }, { status: prev.status, scheduledAt: prev.scheduledAt });
        },
      });
      // show them where they landed: Work carries today's strip and the list
      dispatch({ type: 'SET_PLAN_ANCHOR', anchor: new Date(placements[0].at).getTime() });
      dispatch({ type: 'SET_PLAN_MODE', mode: 'week' });
      dispatch({ type: 'SET_HIGHLIGHT', id: placements[0].id });
      dispatch({ type: 'SET_VIEW', view: 'work' });
      toast(`Gave ${n} a time${UNDO_HINT}`);
    },
  };
}

/* ----------------------------------------------------------------- hooks */

export function useApp(): AppState {
  const s = useContext(StateCtx);
  if (!s) throw new Error('useApp must be used within StoreProvider');
  return s;
}

export function useActions(): Actions {
  const a = useContext(ActionsCtx);
  if (!a) throw new Error('useActions must be used within StoreProvider');
  return a;
}

export type { Kind };
