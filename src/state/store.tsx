import {
  createContext,
  useContext,
  useReducer,
  useMemo,
  useEffect,
  useRef,
  type ReactNode,
} from 'react';
import type { Task as NTask, Project as NProject, ServerEvent, TaskPatch, CreateTaskInput, User } from '../api/types';
import { api, realtimeURL, ApiError } from '../api/client';
import { connectRealtime, type ConnState } from '../api/realtime';
import { inferTask, bestSlot, type Kind } from '../lib/energy';
import { selectToday, selectWeek, selectMonth, selectBacklog, selectProject, computeInsights, clock, type Insights } from './selectors';
import { computeNow, computeWeek, startOfWeek, addDays, ymd, combine, sameDay } from '../lib/time';
import type { View, Now, TodayItem, WeekDay, MonthGrid, BacklogTask, Project } from './types';

/* ------------------------------------------------------------------- model */

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';
type AuthStatus = 'checking' | 'anon' | 'authed';

interface RawState {
  authStatus: AuthStatus;
  user: User | null;
  linkSent: string | null; // email a link was sent to
  devLink: string | null; // dev-only magic link
  status: LoadStatus;
  error: string | null;
  tasks: NTask[];
  projects: NProject[];
  tenantId: string;
  userId: string;
  userInitial: string;
  userColor: string;
  view: View;
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
}

const initialState: RawState = {
  authStatus: 'checking',
  user: null,
  linkSent: null,
  devLink: null,
  status: 'idle',
  error: null,
  tasks: [],
  projects: [],
  tenantId: '',
  userId: '',
  userInitial: 'A',
  userColor: '#211E18',
  view: 'today',
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
};

/* ---------------------------------------------------------------- actions */

type Action =
  | { type: 'AUTH_OK'; user: User }
  | { type: 'AUTH_ANON' }
  | { type: 'LINK_SENT'; email: string; devLink: string | null }
  | { type: 'BOOTSTRAP_START' }
  | { type: 'BOOTSTRAP_OK'; tasks: NTask[]; projects: NProject[]; tenantId: string; userId: string; userInitial: string; userColor: string }
  | { type: 'BOOTSTRAP_ERR'; error: string }
  | { type: 'UPSERT_TASK'; task: NTask }
  | { type: 'REMOVE_TASK'; id: string }
  | { type: 'UPSERT_PROJECT'; project: NProject }
  | { type: 'REMOVE_PROJECT'; id: string }
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
  | { type: 'SET_PLAN_MODE'; mode: 'week' | 'month' };

function upsert<T extends { id: string }>(list: T[], item: T): T[] {
  const i = list.findIndex((x) => x.id === item.id);
  if (i === -1) return [...list, item];
  const next = list.slice();
  next[i] = item;
  return next;
}

function reducer(state: RawState, action: Action): RawState {
  switch (action.type) {
    case 'AUTH_OK':
      return { ...state, authStatus: 'authed', user: action.user, linkSent: null, devLink: null };
    case 'AUTH_ANON':
      return {
        ...initialState,
        authStatus: 'anon',
        // preserve a just-sent link prompt across the anon transition
        linkSent: state.linkSent,
        devLink: state.devLink,
      };
    case 'LINK_SENT':
      return { ...state, linkSent: action.email, devLink: action.devLink };
    case 'BOOTSTRAP_START':
      return { ...state, status: 'loading' };
    case 'BOOTSTRAP_OK':
      return {
        ...state, status: 'ready', error: null, tasks: action.tasks, projects: action.projects,
        tenantId: action.tenantId, userId: action.userId, userInitial: action.userInitial, userColor: action.userColor,
      };
    case 'BOOTSTRAP_ERR':
      return { ...state, status: 'error', error: action.error };
    case 'UPSERT_TASK':
      return { ...state, tasks: upsert(state.tasks, action.task) };
    case 'REMOVE_TASK':
      return { ...state, tasks: state.tasks.filter((t) => t.id !== action.id) };
    case 'UPSERT_PROJECT':
      return { ...state, projects: upsert(state.projects, action.project) };
    case 'REMOVE_PROJECT':
      return { ...state, projects: state.projects.filter((p) => p.id !== action.id) };
    case 'SET_VIEW':
      return { ...state, view: action.view };
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
  status: LoadStatus;
  error: string | null;
  connection: ConnState;
  view: View;
  now: Now;
  planMode: 'week' | 'month';
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
  projects: { id: string; name: string; color: string }[];
  selectedProjectId: string | null;
  people: { userId: string; initial: string; color: string }[];
  insights: Insights;
  editorOpen: boolean;
  editorMode: 'edit' | 'create' | null;
  editingTask: NTask | null;
  tokensOpen: boolean;
  draft: { title: string };
  toast: string | null;
  highlightTaskId: string | null;
}

function derive(s: RawState): AppState {
  const backlog = selectBacklog(s.tasks);
  const activeProject = s.projects.find((p) => p.id === s.selectedProjectId) ?? s.projects[0];
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

  return {
    authStatus: s.authStatus,
    user: s.user,
    linkSent: s.linkSent,
    devLink: s.devLink,
    status: s.status,
    error: s.error,
    connection: s.connection,
    view: s.view,
    now: computeNow(),
    planMode: s.planMode,
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
    projects: s.projects.map((p) => ({ id: p.id, name: p.name, color: p.color })),
    selectedProjectId: activeProject?.id ?? null,
    people: [...peopleMap.values()],
    insights: computeInsights(s.tasks),
    editorOpen: s.editorMode != null,
    editorMode: s.editorMode,
    editingTask: s.editorMode === 'edit' ? (s.tasks.find((t) => t.id === s.editorTaskId) ?? null) : null,
    tokensOpen: s.tokensOpen,
    draft: { title: s.draft },
    toast: s.toast,
    highlightTaskId: s.highlightTaskId,
  };
}

/* --------------------------------------------------------------- contexts */

const StateCtx = createContext<AppState | null>(null);
const ActionsCtx = createContext<Actions | null>(null);

export interface Actions {
  reload: () => void;
  go: (view: View) => void;
  setDraft: (title: string) => void;
  scheduleDraft: () => void;
  toggleToday: (id: string) => void;
  selectBacklog: (id: string | null) => void;
  placeBacklog: (id: string, dayIndex: number, hour: number) => void;
  autoArrange: () => void;
  moveBoard: (id: string, column: 'backlog' | 'week' | 'focus' | 'done') => void;
  autoScheduleRemaining: () => void;
  deleteTask: (id: string) => void;
  openEditor: (id: string) => void;
  openCreate: () => void;
  closeEditor: () => void;
  saveTask: (id: string, patch: TaskPatch) => Promise<void>;
  createTaskFull: (input: CreateTaskInput) => Promise<void>;
  createProject: (name: string, subtitle?: string) => void;
  selectProject: (id: string) => void;
  planPrev: () => void;
  planNext: () => void;
  planToday: () => void;
  setPlanMode: (mode: 'week' | 'month') => void;
  scheduleTaskAt: (id: string, dateStr: string, hour: number) => void;
  requestMagicLink: (email: string) => Promise<void>;
  signOut: () => void;
  openTokens: () => void;
  closeTokens: () => void;
  clearToast: () => void;
  clearHighlight: () => void;
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
      dispatch({
        type: 'BOOTSTRAP_OK', tasks: boot.tasks, projects: boot.projects,
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
        const { user } = await api.verify(token);
        window.history.replaceState({}, '', '/');
        dispatch({ type: 'AUTH_OK', user });
        return;
      } catch {
        window.history.replaceState({}, '', '/');
        dispatch({ type: 'AUTH_ANON' });
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

  const actions = useMemo<Actions>(() => makeActions(dispatch, ref), []);

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
  }
}

/* ------------------------------------------------------------ action impls */

function makeActions(dispatch: React.Dispatch<Action>, ref: React.MutableRefObject<RawState>): Actions {
  const toast = (msg: string) => dispatch({ type: 'SET_TOAST', toast: msg });

  // optimistic update with rollback
  const patchTask = async (id: string, patch: TaskPatch, optimistic: Partial<NTask>) => {
    const prev = ref.current.tasks.find((t) => t.id === id);
    if (!prev) return;
    dispatch({ type: 'UPSERT_TASK', task: { ...prev, ...optimistic, version: prev.version + 1 } });
    try {
      const server = await api.updateTask(id, patch);
      dispatch({ type: 'UPSERT_TASK', task: server });
    } catch (e) {
      dispatch({ type: 'UPSERT_TASK', task: prev });
      toast(e instanceof ApiError ? e.message : 'Update failed');
    }
  };

  // ISO datetime for a (weekday-index, hour) within the currently displayed week
  const slotISO = (dayIndex: number, hour: number) =>
    combine(ymd(addDays(startOfWeek(new Date(ref.current.planAnchor)), dayIndex)), hour);

  const slotPatch = (kind: Kind, fromDay: number): { patch: TaskPatch; opt: Partial<NTask> } => {
    const slot = bestSlot(kind, fromDay);
    const at = slotISO(slot.dayIndex, slot.hour);
    return {
      patch: { status: 'scheduled', scheduledAt: at },
      opt: { status: 'scheduled', scheduledAt: at },
    };
  };

  return {
    reload: () => window.location.reload(),
    go: (view) => dispatch({ type: 'SET_VIEW', view }),
    setDraft: (title) => dispatch({ type: 'SET_DRAFT', draft: title }),
    selectBacklog: (id) => dispatch({ type: 'SELECT_BACKLOG', id }),
    clearToast: () => dispatch({ type: 'SET_TOAST', toast: null }),
    clearHighlight: () => dispatch({ type: 'SET_HIGHLIGHT', id: null }),

    scheduleDraft: () => {
      const title = ref.current.draft.trim();
      if (!title) return;
      const inf = inferTask(title);
      const slot = bestSlot(inf.kind, 0);
      const at = slotISO(slot.dayIndex, slot.hour);
      const input: CreateTaskInput = {
        title,
        kind: inf.kind,
        status: 'scheduled',
        effortMinutes: Math.round(inf.effortHrs * 60),
        scheduledAt: at,
        place: slot.place,
      };
      dispatch({ type: 'SET_VIEW', view: 'plan' });
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

    toggleToday: (id) => {
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t) return;
      const done = t.status === 'done';
      const nextStatus: NTask['status'] = done ? (t.scheduledAt != null ? 'scheduled' : 'backlog') : 'done';
      void patchTask(id, { status: nextStatus }, { status: nextStatus, doneAt: done ? null : new Date().toISOString() });
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

    autoArrange: () => {
      const backlog = selectBacklog(ref.current.tasks);
      if (!backlog.length) return;
      backlog.forEach((b, i) => {
        const { patch, opt } = slotPatch(b.kind, i % 5);
        void patchTask(b.id, patch, opt);
      });
      dispatch({ type: 'SELECT_BACKLOG', id: null });
      toast(`Arranged ${backlog.length} tasks across your week`);
    },

    moveBoard: (id, column) => {
      const t = ref.current.tasks.find((x) => x.id === id);
      if (!t) return;
      const status = COLUMN_STATUS[column];
      const patch: TaskPatch = { status };
      const opt: Partial<NTask> = { status };
      if (column === 'week' && t.scheduledAt == null) {
        const slot = bestSlot(t.kind, 0);
        const at = slotISO(slot.dayIndex, slot.hour);
        patch.scheduledAt = at;
        opt.scheduledAt = at;
      }
      void patchTask(id, patch, opt);
      dispatch({ type: 'SET_HIGHLIGHT', id });
    },

    autoScheduleRemaining: () => {
      const project = ref.current.projects[0];
      if (!project) return;
      const remaining = ref.current.tasks.filter((t) => t.projectId === project.id && t.status === 'backlog');
      if (!remaining.length) {
        toast('Everything is already scheduled');
        return;
      }
      remaining.forEach((t, i) => {
        const { patch, opt } = slotPatch(t.kind, i % 5);
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

    saveTask: async (id, patch) => {
      await patchTask(id, patch, patch as Partial<NTask>);
      dispatch({ type: 'CLOSE_EDITOR' });
      toast('Saved');
    },

    createTaskFull: async (input) => {
      try {
        const task = await api.createTask(input);
        dispatch({ type: 'UPSERT_TASK', task });
        dispatch({ type: 'SET_HIGHLIGHT', id: task.id });
        dispatch({ type: 'CLOSE_EDITOR' });
        // land the user where the task now lives
        const sched = task.scheduledAt ? new Date(task.scheduledAt) : null;
        const view: View = sched && sameDay(sched, new Date()) ? 'today' : task.projectId ? 'board' : 'plan';
        dispatch({ type: 'SET_VIEW', view });
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
          dispatch({ type: 'SET_VIEW', view: 'board' });
          toast(`Project created · ${p.name}`);
        })
        .catch((e) => toast(e instanceof ApiError ? e.message : 'Could not create project'));
    },

    selectProject: (id) => dispatch({ type: 'SELECT_PROJECT', id }),

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

    scheduleTaskAt: (id, dateStr, hour) => {
      const at = combine(dateStr, hour);
      void patchTask(id, { status: 'scheduled', scheduledAt: at }, { status: 'scheduled', scheduledAt: at });
      dispatch({ type: 'SET_HIGHLIGHT', id });
      toast('Scheduled');
    },

    requestMagicLink: async (email) => {
      try {
        const res = await api.requestLink(email);
        dispatch({ type: 'LINK_SENT', email: res.email, devLink: res.devLink ?? null });
      } catch (e) {
        toast(e instanceof ApiError ? e.message : 'Could not send the link');
      }
    },

    signOut: () => {
      api.logout().finally(() => dispatch({ type: 'AUTH_ANON' }));
    },

    openTokens: () => dispatch({ type: 'SET_TOKENS_OPEN', open: true }),
    closeTokens: () => dispatch({ type: 'SET_TOKENS_OPEN', open: false }),
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
