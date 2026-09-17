import '../styles/work.css';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useApp, useActions } from '../state/store';
import { KINDS, type Kind } from '../lib/energy';
import { clock, effortLabel, type TaskRow } from '../state/selectors';
import { hourOf, ymd, addDays, startOfWeek } from '../lib/time';
import type { Stage } from '../api/types';
import type { TodayItem } from '../state/types';
import { ProjectPicker } from '../components/ProjectPicker';
import { ProofStrip } from '../components/ProofStrip';
import { StartStrip } from '../components/StartStrip';
import { ContextLine } from '../components/ContextLine';

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const DOW = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const KIND_ORDER: Kind[] = ['deep', 'light', 'admin', 'meet', 'personal'];
const STAGE_ORDER: Stage[] = ['todo', 'doing', 'waiting', 'done'];
const STAGE_LABEL: Record<Stage, string> = { todo: 'To do', doing: 'Doing', waiting: 'Waiting', done: 'Done' };

type SortKey = 'title' | 'project' | 'client' | 'kind' | 'stage' | 'owner' | 'why' | 'origins' | 'waiting' | 'priority' | 'effort' | 'scheduled' | 'deadline' | 'created';
type GroupKey = 'none' | 'project' | 'client' | 'stage' | 'owner' | 'requirement';

/** Every column the register can show; the key is also its sort key. */
const COLS: { key: SortKey; label: string; cls?: string }[] = [
  { key: 'title', label: 'Item' },
  { key: 'project', label: 'Project' },
  { key: 'client', label: 'Client' },
  { key: 'kind', label: 'Type' },
  { key: 'stage', label: 'Stage' },
  { key: 'owner', label: 'Owner' },
  { key: 'why', label: 'Why' },
  { key: 'origins', label: 'From', cls: 'tk-num' },
  { key: 'waiting', label: 'Waiting on' },
  { key: 'priority', label: 'Priority' },
  { key: 'effort', label: 'Effort', cls: 'tk-num' },
  { key: 'scheduled', label: 'Scheduled' },
  { key: 'deadline', label: 'Deadline' },
  { key: 'created', label: 'Age', cls: 'tk-num' },
];

/**
 * Off by default, so the register opens on what a person actually scans (item,
 * project, stage, why, owner, when, deadline) and fits a laptop without
 * scrolling sideways. The rest is one click away under Columns, and the choice
 * is kept per browser like the List | Board mode.
 */
const DEFAULT_HIDDEN: SortKey[] = ['client', 'kind', 'origins', 'waiting', 'effort', 'created'];
const COLS_KEY = 'cadence.workCols';

function loadHiddenCols(): Set<SortKey> {
  try {
    const raw = localStorage.getItem(COLS_KEY);
    if (!raw) return new Set(DEFAULT_HIDDEN);
    const saved = JSON.parse(raw) as unknown;
    if (!Array.isArray(saved)) return new Set(DEFAULT_HIDDEN);
    const known = new Set<string>(COLS.map((c) => c.key));
    // 'title' is never hidden: it's the row's handle and its only tab stop
    return new Set(saved.filter((k): k is SortKey => typeof k === 'string' && k !== 'title' && known.has(k)));
  } catch {
    return new Set(DEFAULT_HIDDEN);
  }
}
function saveHiddenCols(hidden: Set<SortKey>) {
  try {
    localStorage.setItem(COLS_KEY, JSON.stringify([...hidden]));
  } catch {
    /* no storage (private window): the choice lasts this session */
  }
}

const startOfToday = () => {
  const d = new Date();
  d.setHours(0, 0, 0, 0);
  return d.getTime();
};
// Local today's calendar date as yyyy-mm-dd, for date-only comparisons.
const todayYMD = () => {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};
// A deadline is a calendar date stored at 17:00Z; read its day straight from the
// ISO prefix so it never shifts by one under the viewer's timezone.
const fmtDay = (iso: string) => {
  const p = iso.slice(0, 10).split('-');
  return `${Number(p[2])} ${MONTHS[Number(p[1]) - 1]}`;
};
const ageDays = (iso: string) => Math.max(0, Math.round((startOfToday() - new Date(iso).setHours(0, 0, 0, 0)) / 86400000));
/** Whole days an item has been waiting; null when it isn't (or the server never stamped it). */
const waitingDays = (r: TaskRow) => (r.stage === 'waiting' && r.waitingOnSince ? ageDays(r.waitingOnSince) : null);

/* ------------------------------------------------ the doing surface */

/** How many items "Next" offers before the rest fall through to the register. */
const NEXT_MAX = 6;
/** Roughly how tall the "when" menu is — enough to know whether it fits below a row. */
const MENU_H = 210;
/** A deadline this close is worth showing on the row itself. */
const SOON_DAYS = 2;

const isToday = (iso: string) => ymd(new Date(iso)) === todayYMD();
/** Overdue, or due within the next couple of days — the only deadlines a row shouts about. */
const deadlinePressure = (r: TaskRow): 'overdue' | 'soon' | null => {
  if (!r.deadline || r.stage === 'done') return null;
  const day = r.deadline.slice(0, 10);
  if (day < todayYMD()) return 'overdue';
  return day <= ymd(addDays(new Date(), SOON_DAYS)) ? 'soon' : null;
};
/** A scheduled time, said the way a person would: Today 14:00 · Tomorrow · Thu 11 Sep. */
function whenLabel(iso: string): string {
  const d = new Date(iso);
  const day = ymd(d);
  if (day === todayYMD()) return `Today ${clock(hourOf(d))}`;
  if (day === ymd(addDays(new Date(), 1))) return 'Tomorrow';
  return `${DOW[(d.getDay() + 6) % 7]} ${d.getDate()} ${MONTHS[d.getMonth()]}`;
}
/**
 * The third one-click "when": the end of this working week while there is one
 * left, otherwise next Monday. Named for where it actually lands, never for a
 * week that has already gone.
 */
function laterThisWeek(now: Date): { date: Date; label: string; hint: string } {
  const fri = addDays(startOfWeek(now), 4);
  const dayAfterTomorrow = addDays(new Date(now.getFullYear(), now.getMonth(), now.getDate()), 2);
  if (fri >= dayAfterTomorrow) return { date: fri, label: 'This week', hint: 'Fri' };
  return { date: addDays(startOfWeek(now), 7), label: 'Next week', hint: 'Mon' };
}

/**
 * How many signals an item came from. The count rides on the task itself
 * (`originCount`); the origins cache stands in for items whose provenance was
 * already loaded for the editor. Neither path fetches — a register of 200 rows
 * must never become 200 requests — so an item the server hasn't counted reads
 * as "—" until it's opened.
 */
const originCountOf = (r: TaskRow, cached: number | undefined): number | undefined => r.originCount ?? cached;

/** [value, isEmpty] for a row under a sort key; empties always sort last. */
function sortVal(r: TaskRow, key: SortKey, originCount: number | undefined): [number | string, boolean] {
  switch (key) {
    case 'title': return [r.title.toLowerCase(), false];
    case 'project': return [r.project?.name.toLowerCase() ?? '', r.project == null];
    case 'client': return [r.client?.name.toLowerCase() ?? '', r.client == null];
    case 'kind': return [KIND_ORDER.indexOf(r.kind), false];
    case 'stage': return [STAGE_ORDER.indexOf(r.stage), false];
    case 'owner': return [r.ownerId ?? '', r.ownerId == null];
    case 'why': return [r.requirement?.title.toLowerCase() ?? '', r.requirement == null];
    case 'origins': return [originCount ?? 0, originCount == null];
    case 'waiting': {
      const d = waitingDays(r);
      return [d ?? 0, d == null];
    }
    case 'priority': return [r.priority, false];
    case 'effort': return [r.effortMinutes, false];
    case 'scheduled': return [r.scheduledAt ? Date.parse(r.scheduledAt) : 0, r.scheduledAt == null];
    case 'deadline': return [r.deadline ? Date.parse(r.deadline) : 0, r.deadline == null];
    // sort on age (older = larger), matching the displayed "Age" value so the
    // direction arrow agrees with the numbers on screen.
    case 'created': return [-Date.parse(r.createdAt), false];
  }
}

export function WorkView() {
  const { allTasks: everyTask, projects, clients, people, user, originsByTask, proofFor, startFor, today } = useApp();
  // work under an archived project is shelved with it: kept, restorable, but not on Work
  const allTasks = useMemo(() => everyTask.filter((r) => !r.shelved), [everyTask]);
  const { openEditor, loadOrigins, completeTask, uncompleteTask, startTask } = useActions();

  // the register — filters, sort, grouping, columns — lives behind "the rest"
  const [restOpen, setRestOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [projectF, setProjectF] = useState<string>('all'); // 'all' | id | 'none'
  const [clientF, setClientF] = useState<string>('all');
  const [ownerF, setOwnerF] = useState<string>('all'); // 'all' | userId | 'none'
  const [kindF, setKindF] = useState<Set<Kind>>(new Set());
  const [stageF, setStageF] = useState<Set<Stage>>(new Set());
  const [importantOnly, setImportantOnly] = useState(false);
  const [urgentOnly, setUrgentOnly] = useState(false);
  const [unscheduledOnly, setUnscheduledOnly] = useState(false);
  const [hasDeadline, setHasDeadline] = useState(false);
  const [unscopedOnly, setUnscopedOnly] = useState(false);
  const [sort, setSort] = useState<{ key: SortKey; dir: 'asc' | 'desc' }>({ key: 'priority', dir: 'desc' });
  const [groupBy, setGroupBy] = useState<GroupKey>('none');
  const [hiddenCols, setHiddenCols] = useState<Set<SortKey>>(loadHiddenCols);
  const [colsOpen, setColsOpen] = useState(false);
  const colsRef = useRef<HTMLDivElement>(null);

  const anyFilter =
    search !== '' || projectF !== 'all' || clientF !== 'all' || ownerF !== 'all' || kindF.size > 0 || stageF.size > 0 || importantOnly || urgentOnly || unscheduledOnly || hasDeadline || unscopedOnly;

  const clearAll = () => {
    setSearch('');
    setProjectF('all');
    setClientF('all');
    setOwnerF('all');
    setKindF(new Set());
    setStageF(new Set());
    setImportantOnly(false);
    setUrgentOnly(false);
    setUnscheduledOnly(false);
    setHasDeadline(false);
    setUnscopedOnly(false);
  };

  const personById = useMemo(() => new Map(people.map((p) => [p.userId, p])), [people]);

  // workspace summary (over every item, independent of the current filter)
  const summary = useMemo(() => {
    let done = 0;
    let doing = 0;
    let waiting = 0;
    let openUnscheduled = 0;
    let unscoped = 0;
    let foundations = 0;
    for (const t of allTasks) {
      if (t.stage === 'done') done++;
      else if (t.stage === 'doing') doing++;
      else if (t.stage === 'waiting') waiting++;
      if (t.stage !== 'done' && !t.scheduledAt) openUnscheduled++;
      if (t.unscoped) unscoped++;
      if (t.important && t.stage !== 'done' && t.scheduledAt == null) foundations++;
    }
    return { total: allTasks.length, done, doing, waiting, openUnscheduled, unscoped, foundations };
  }, [allTasks]);

  /**
   * The doing surface: what is in flight, what to pick up, what is stuck.
   * Deliberately blind to the filters below — those belong to the register,
   * and these three answers should never change because a filter is set.
   * Next reads in the order a person would: what today already promised,
   * then what a deadline is pressing on, then the highest-ranked of the rest.
   */
  const sections = useMemo(() => {
    const doing: TaskRow[] = [];
    const waiting: TaskRow[] = [];
    const open: TaskRow[] = [];
    for (const t of allTasks) {
      if (t.stage === 'done') continue;
      if (t.stage === 'doing') doing.push(t);
      else if (t.stage === 'waiting') waiting.push(t);
      else open.push(t);
    }
    doing.sort((a, b) => b.priority - a.priority);
    waiting.sort((a, b) => (waitingDays(b) ?? 0) - (waitingDays(a) ?? 0));
    const band = (r: TaskRow) => (r.scheduledAt && isToday(r.scheduledAt) ? 0 : deadlinePressure(r) ? 1 : 2);
    open.sort((a, b) => {
      const ba = band(a);
      const bb = band(b);
      if (ba !== bb) return ba - bb;
      if (ba === 0) return Date.parse(a.scheduledAt!) - Date.parse(b.scheduledAt!);
      if (ba === 1) return String(a.deadline).localeCompare(String(b.deadline));
      return b.priority - a.priority || Date.parse(a.createdAt) - Date.parse(b.createdAt);
    });
    const next = open.slice(0, NEXT_MAX);
    const shown = new Set([...doing, ...next, ...waiting].map((r) => r.id));
    return { doing, next, waiting, shown, rest: allTasks.length - shown.size };
  }, [allTasks]);

  const rows = useMemo(() => {
    const q = search.trim().toLowerCase();
    const filtered = allTasks.filter((r) => {
      if (q && !r.title.toLowerCase().includes(q) && !(r.requirement?.title.toLowerCase().includes(q) ?? false)) return false;
      if (projectF === 'none' ? r.projectId != null : projectF !== 'all' && r.projectId !== projectF) return false;
      if (clientF === 'none' ? r.clientId != null : clientF !== 'all' && r.clientId !== clientF) return false;
      if (ownerF === 'none' ? r.ownerId != null : ownerF !== 'all' && r.ownerId !== ownerF) return false;
      if (kindF.size && !kindF.has(r.kind)) return false;
      if (stageF.size && !stageF.has(r.stage)) return false;
      if (importantOnly && !r.important) return false;
      if (urgentOnly && !r.urgent) return false;
      if (unscheduledOnly && (r.scheduledAt != null || r.stage === 'done')) return false;
      if (hasDeadline && r.deadline == null) return false;
      if (unscopedOnly && !r.unscoped) return false;
      return true;
    });
    const dir = sort.dir === 'asc' ? 1 : -1;
    return filtered.sort((a, b) => {
      const [va, ea] = sortVal(a, sort.key, originCountOf(a, originsByTask[a.id]?.length));
      const [vb, eb] = sortVal(b, sort.key, originCountOf(b, originsByTask[b.id]?.length));
      if (ea && eb) return Date.parse(b.createdAt) - Date.parse(a.createdAt);
      if (ea) return 1;
      if (eb) return -1;
      const base = typeof va === 'number' && typeof vb === 'number' ? va - vb : String(va).localeCompare(String(vb));
      return base !== 0 ? base * dir : Date.parse(b.createdAt) - Date.parse(a.createdAt);
    });
  }, [allTasks, originsByTask, search, projectF, clientF, ownerF, kindF, stageF, importantOnly, urgentOnly, unscheduledOnly, hasDeadline, unscopedOnly, sort]);

  // the Columns menu closes on any click outside it, like the account menu
  useEffect(() => {
    if (!colsOpen) return;
    const onDoc = (e: MouseEvent) => {
      if (colsRef.current && !colsRef.current.contains(e.target as Node)) setColsOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [colsOpen]);

  // group the (already sorted) rows for display
  const groups = useMemo(() => {
    if (groupBy === 'none') return [{ key: 'all', label: '', rows }];
    const map = new Map<string, { label: string; order: number; rows: TaskRow[] }>();
    for (const r of rows) {
      let key: string;
      let label: string;
      let order: number;
      if (groupBy === 'project') {
        key = r.projectId ?? '∅';
        label = r.project?.name ?? 'No project';
        order = r.project ? 0 : 1;
      } else if (groupBy === 'client') {
        key = r.clientId ?? '∅';
        label = r.client?.name ?? 'No client';
        order = r.client ? 0 : 1;
      } else if (groupBy === 'owner') {
        key = r.ownerId ?? '∅';
        label = r.ownerId ? (r.ownerId === user?.id ? 'You' : personById.get(r.ownerId)?.initial ?? 'Someone') : 'Unowned';
        order = r.ownerId ? 0 : 1;
      } else if (groupBy === 'requirement') {
        key = r.requirementId ?? '∅';
        label = r.requirement?.title ?? 'Unscoped';
        order = r.requirement ? 0 : 1;
      } else {
        key = r.stage;
        label = STAGE_LABEL[r.stage];
        order = STAGE_ORDER.indexOf(r.stage);
      }
      let g = map.get(key);
      if (!g) {
        g = { label, order, rows: [] };
        map.set(key, g);
      }
      g.rows.push(r);
    }
    return [...map.entries()]
      .map(([key, g]) => ({ key, label: g.label, order: g.order, rows: g.rows }))
      .sort((a, b) => a.order - b.order || a.label.localeCompare(b.label));
  }, [rows, groupBy, personById, user]);

  const toggle = <T,>(set: Set<T>, v: T): Set<T> => {
    const next = new Set(set);
    if (next.has(v)) next.delete(v);
    else next.add(v);
    return next;
  };

  const sortBy = (key: SortKey) =>
    setSort((s) => (s.key === key ? { key, dir: s.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: key === 'title' || key === 'project' || key === 'client' || key === 'why' || key === 'owner' ? 'asc' : 'desc' }));

  const arrow = (key: SortKey) => (sort.key === key ? (sort.dir === 'asc' ? ' ↑' : ' ↓') : '');

  const cols = useMemo(() => COLS.filter((c) => !hiddenCols.has(c.key)), [hiddenCols]);
  const colKeys = useMemo(() => cols.map((c) => c.key), [cols]);

  /** Show or hide one column. Hiding the one being sorted on moves the sort to the leftmost column left, so the arrow is always somewhere you can see it. */
  const toggleCol = (key: SortKey) => {
    if (key === 'title') return; // the item itself always shows
    const next = toggle(hiddenCols, key);
    setHiddenCols(next);
    saveHiddenCols(next);
    if (next.has(key) && sort.key === key) setSort((s) => ({ key: COLS.find((c) => !next.has(c.key))?.key ?? 'title', dir: s.dir }));
  };

  // an item's proof / start strip, wherever that item is on screen
  const stripFor = (r: TaskRow) => (proofFor?.id === r.id ? <ProofStrip task={proofFor} /> : startFor?.id === r.id ? <StartStrip task={startFor} /> : null);
  return (
    <div className="viewbody work">
      <div className="work__head">
        <div>
          <span className="eyebrow">Work</span>
          <h1 className="work__title serif">What you’re getting done</h1>
        </div>
      </div>

      {/* today's "when", re-homed from the Plan calendar: the day, in a line */}
      <TodayStrip items={today} onOpen={openEditor} />

      <section className="work__sec" aria-label="Now">
        <SectionHead name="Now" n={sections.doing.length} note="in flight" />
        {sections.doing.length === 0 ? (
          <p className="work__sec-empty">Nothing in flight — start something from Next.</p>
        ) : (
          <ul className="work__nowlist">
            {sections.doing.map((r) => (
              <NowCard key={r.id} r={r} onOpen={() => openEditor(r.id)} onDone={() => void completeTask(r.id)} strip={stripFor(r)} />
            ))}
          </ul>
        )}
      </section>

      <section className="work__sec" aria-label="Next">
        <SectionHead name="Next" n={sections.next.length} note="pick one up" />
        {sections.next.length === 0 ? (
          <p className="work__sec-empty">Nothing queued. Everything open is in flight, waiting or done.</p>
        ) : (
          <ul className="work__tasklist">
            {sections.next.map((r) => (
              <TaskLine
                key={r.id}
                r={r}
                onOpen={() => openEditor(r.id)}
                onTick={() => void completeTask(r.id)}
                onStart={() => void startTask(r.id)}
                strip={stripFor(r)}
              />
            ))}
          </ul>
        )}
      </section>

      {sections.waiting.length > 0 && (
        <section className="work__sec" aria-label="Waiting">
          <SectionHead name="Waiting" n={sections.waiting.length} note="on someone else" />
          <ul className="work__tasklist">
            {sections.waiting.map((r) => (
              <TaskLine
                key={r.id}
                r={r}
                waitingOn={r.waitingOnPersonId ? personById.get(r.waitingOnPersonId) ?? null : null}
                onOpen={() => openEditor(r.id)}
                onTick={() => void completeTask(r.id)}
                strip={stripFor(r)}
              />
            ))}
          </ul>
        </section>
      )}

      {/* Everything else — the register with its filters, grouping and columns,
          and the board — one disclosure below the work in hand. */}
      <div className="work__rest">
        <div className="work__rest-bar">
          <button type="button" className="work__rest-toggle" aria-expanded={restOpen} onClick={() => setRestOpen((v) => !v)}>
            <span className="work__rest-caret" aria-hidden>
              {restOpen ? '▾' : '▸'}
            </span>
            {restOpen ? 'Hide the rest' : sections.rest > 0 ? `${sections.rest} more` : 'All items'}
          </button>
        </div>

        {restOpen && (
          <>
            {/* projects, and each project's board, live on the Projects tab */}
              <>
                {/* summary strip */}
                <div className="work__summary">
                  <Metric n={summary.total} label="total" />
                  <Metric n={summary.doing} label="doing" />
                  <Metric n={summary.waiting} label="waiting" />
                  <Metric n={summary.openUnscheduled} label="unscheduled" />
                  <Metric n={summary.done} label="done" />
                  <button
                    className={'work__metric work__metric--risk' + (summary.unscoped === 0 ? ' is-clear' : '')}
                    onClick={() => {
                      clearAll();
                      setUnscopedOnly(true);
                    }}
                    title="Open items that serve no requirement yet — scope them so they count toward something"
                  >
                    <span className="work__metric-n">{summary.unscoped}</span>
                    <span className="work__metric-l">unscoped</span>
                  </button>
                  <button
                    className={'work__metric work__metric--risk' + (summary.foundations === 0 ? ' is-clear' : '')}
                    onClick={() => {
                      clearAll();
                      setImportantOnly(true);
                      setUnscheduledOnly(true);
                    }}
                    title="Important work that isn't scheduled yet — the foundations most at risk of slipping"
                  >
                    <span className="work__metric-n">{summary.foundations}</span>
                    <span className="work__metric-l">foundations at risk</span>
                  </button>
                </div>

                {/* filters */}
                <div className="work__filters">
                  <div className="work__filters-primary">
                    <input className="work__search" placeholder="Search items or requirements…" value={search} onChange={(e) => setSearch(e.target.value)} aria-label="Search work" />
                    <select className="work__select" value={projectF} onChange={(e) => setProjectF(e.target.value)} aria-label="Filter by project">
                      <option value="all">All projects</option>
                      {projects.map((p) => (
                        <option key={p.id} value={p.id}>
                          {p.name}
                        </option>
                      ))}
                      <option value="none">— No project</option>
                    </select>
                    <select className="work__select" value={clientF} onChange={(e) => setClientF(e.target.value)} aria-label="Filter by client">
                      <option value="all">All clients</option>
                      {clients.filter((c) => !c.archivedAt).map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.name}
                        </option>
                      ))}
                      <option value="none">— No client</option>
                    </select>
                    <select className="work__select" value={ownerF} onChange={(e) => setOwnerF(e.target.value)} aria-label="Filter by owner">
                      <option value="all">Any owner</option>
                      {people.map((p) => (
                        <option key={p.userId} value={p.userId}>
                          {p.userId === user?.id ? 'You' : p.initial}
                        </option>
                      ))}
                      <option value="none">— Unowned</option>
                    </select>
                    <div className="work__filters-right">
                      <label className="work__group">
                        Group
                        <select value={groupBy} onChange={(e) => setGroupBy(e.target.value as GroupKey)}>
                          <option value="none">None</option>
                          <option value="project">Project</option>
                          <option value="client">Client</option>
                          <option value="stage">Stage</option>
                          <option value="owner">Owner</option>
                          <option value="requirement">Requirement</option>
                        </select>
                      </label>
                      {/* which columns the register shows, kept per browser */}
                      <div className="work__cols" ref={colsRef}>
                        <button type="button" className="work__colsbtn" aria-haspopup="true" aria-expanded={colsOpen} onClick={() => setColsOpen((v) => !v)}>
                          Columns <span className="work__colsbtn-n tnum">{cols.length}</span>
                        </button>
                        {colsOpen && (
                          <div className="work__colsmenu" role="group" aria-label="Visible columns">
                            {COLS.map((c) => (
                              <label key={c.key} className={'work__colsitem' + (c.key === 'title' ? ' is-locked' : '')}>
                                <input type="checkbox" checked={!hiddenCols.has(c.key)} disabled={c.key === 'title'} onChange={() => toggleCol(c.key)} />
                                {c.label}
                              </label>
                            ))}
                          </div>
                        )}
                      </div>
                      {anyFilter && (
                        <button className="work__clear" onClick={clearAll}>
                          Clear filters
                        </button>
                      )}
                    </div>
                  </div>

                  <div className="work__filters-chips">
                    <div className="work__chipset" role="group" aria-label="Filter by type">
                      {KIND_ORDER.map((k) => (
                        <button key={k} className="work__chip" aria-pressed={kindF.has(k)} onClick={() => setKindF((s) => toggle(s, k))}>
                          <span className="dot" style={{ width: 6, height: 6, background: KINDS[k].dot }} />
                          {KINDS[k].label}
                        </button>
                      ))}
                    </div>
                    <span className="work__divider" aria-hidden />
                    <div className="work__chipset" role="group" aria-label="Filter by stage">
                      {STAGE_ORDER.map((s) => (
                        <button key={s} className={'work__chip work__chip--' + s} aria-pressed={stageF.has(s)} onClick={() => setStageF((prev) => toggle(prev, s))}>
                          {STAGE_LABEL[s]}
                        </button>
                      ))}
                    </div>
                    <span className="work__divider" aria-hidden />
                    <div className="work__chipset" role="group" aria-label="Quick filters">
                      <button className="work__chip" aria-pressed={importantOnly} onClick={() => setImportantOnly((v) => !v)}>
                        Important
                      </button>
                      <button className="work__chip" aria-pressed={urgentOnly} onClick={() => setUrgentOnly((v) => !v)}>
                        Urgent
                      </button>
                      <button className="work__chip" aria-pressed={unscheduledOnly} onClick={() => setUnscheduledOnly((v) => !v)}>
                        Unscheduled
                      </button>
                      <button className="work__chip" aria-pressed={hasDeadline} onClick={() => setHasDeadline((v) => !v)}>
                        Has deadline
                      </button>
                      <button className="work__chip" aria-pressed={unscopedOnly} onClick={() => setUnscopedOnly((v) => !v)}>
                        Unscoped
                      </button>
                    </div>
                  </div>
                </div>

                <div className="work__count">
                  Showing <b>{rows.length}</b> of {allTasks.length}
                </div>

                {/* table */}
                <div className="work__tablewrap">
                  <table className="work__table">
                    <thead>
                      <tr>
                        {cols.map((c) => (
                          <th key={c.key} scope="col" className={c.cls} aria-sort={sort.key === c.key ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}>
                            <button type="button" className="work__th-btn" onClick={() => sortBy(c.key)}>
                              {c.label}
                              <span className="work__arrow" aria-hidden>{arrow(c.key)}</span>
                            </button>
                          </th>
                        ))}
                      </tr>
                    </thead>
                    {groups.map((g) => (
                      <tbody key={g.key}>
                        {groupBy !== 'none' && (
                          <tr className="work__grouprow">
                            <th scope="rowgroup" colSpan={cols.length}>
                              {g.label} <span className="work__group-n">{g.rows.length}</span>
                            </th>
                          </tr>
                        )}
                        {g.rows.map((r) => (
                          <WorkRow
                            key={r.id}
                            r={r}
                            owner={r.ownerId ? personById.get(r.ownerId) ?? null : null}
                            isYou={r.ownerId != null && r.ownerId === user?.id}
                            waitingOn={r.waitingOnPersonId ? personById.get(r.waitingOnPersonId) ?? null : null}
                            originCount={originCountOf(r, originsByTask[r.id]?.length)}
                            onOpen={() => openEditor(r.id)}
                            onOpenOrigins={() => {
                              // the editor shows provenance from the cache; make sure it's warm
                              void loadOrigins(r.id);
                              openEditor(r.id);
                            }}
                            onTick={() => void (r.stage === 'done' ? uncompleteTask(r.id) : completeTask(r.id))}
                            cols={colKeys}
                            // an item already shown in Now / Next / Waiting carries its strip up there
                            strip={sections.shown.has(r.id) ? null : stripFor(r)}
                          />
                        ))}
                      </tbody>
                    ))}
                  </table>
                  {rows.length === 0 && (
                    <div className="work__empty">
                      {allTasks.length === 0 ? (
                        <>Nothing here yet — capture something with <b>⌘K</b>, or add an item with <b>+ New</b>.</>
                      ) : (
                        <>Nothing matches these filters. <button className="work__link" onClick={clearAll}>Clear them</button> to see everything.</>
                      )}
                    </div>
                  )}
                </div>
              </>
          </>
        )}
      </div>
    </div>
  );
}

function Metric({ n, label }: { n: number; label: string }) {
  return (
    <div className="work__metric">
      <span className="work__metric-n">{n}</span>
      <span className="work__metric-l">{label}</span>
    </div>
  );
}

/* ---------------------------------------------- the doing surface */

function SectionHead({ name, n, note }: { name: string; n: number; note: string }) {
  return (
    <h2 className="work__sec-head">
      <span className="work__sec-name">{name}</span>
      <span className="work__sec-n tnum">{n}</span>
      <span className="work__sec-note">{note}</span>
    </h2>
  );
}

/**
 * Today, in one line: what is on, at what time, without a calendar to visit.
 * This is the half of Plan people actually needed — the day, visible — and it
 * reads straight off the same scheduled occurrences the week grid used.
 */
function TodayStrip({ items, onOpen }: { items: TodayItem[]; onOpen: (id: string) => void }) {
  return (
    <div className="work__today">
      <span className="work__today-label">Today</span>
      {items.length === 0 ? (
        <span className="work__today-none">Nothing scheduled — put a day on anything below.</span>
      ) : (
        <ul className="work__today-list">
          {items.map((i) => (
            <li key={i.id + i.timeLabel} className={'work__today-item' + (i.done ? ' is-done' : i.status === 'past' ? ' is-past' : '')}>
              <span className="work__today-time tnum">{i.timeLabel}</span>
              <span className="dot" style={{ width: 6, height: 6, background: i.dot }} />
              <button type="button" className="work__today-title" onClick={() => onOpen(i.id)}>
                {i.title}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * One item in flight. Big, quiet, and carrying the single action that matters:
 * mark it done (through the store's one completion path, so the proof strip
 * still asks afterwards and never blocks the done).
 */
function NowCard({ r, onOpen, onDone, strip }: { r: TaskRow; onOpen: () => void; onDone: () => void; strip: React.ReactNode }) {
  const titleRef = useRef<HTMLButtonElement>(null);
  const press = deadlinePressure(r);
  return (
    <li className="work__now-item">
      <div
        className="work__now"
        onClick={() => {
          titleRef.current?.focus();
          onOpen();
        }}
      >
        <span className="dot work__now-dot" style={{ background: KINDS[r.kind].dot }} />
        <div className="work__now-main">
          <button ref={titleRef} type="button" className="work__now-title serif">
            {r.title}
          </button>
          <div className="work__now-meta">
            {r.project && <span className="work__task-proj"><span className="dot" style={{ width: 6, height: 6, background: r.project.color }} />{r.project.name}</span>}
            {r.requirement && <span className="work__task-why">{r.requirement.title}</span>}
            {r.definitionOfDone && <span className="work__now-dod">Done when: {r.definitionOfDone}</span>}
            {press && r.deadline && <span className={'work__due work__due--' + press}>{press === 'overdue' ? 'Overdue' : 'Due'} {fmtDay(r.deadline)}</span>}
          </div>
        </div>
        <div className="work__now-act" onClick={(e) => e.stopPropagation()}>
          <WhenPicker id={r.id} scheduledAt={r.scheduledAt} />
          <button type="button" className="work__done-btn" onClick={onDone}>
            Mark done
          </button>
        </div>
      </div>
      {strip}
    </li>
  );
}

/**
 * A line in Next or Waiting: the tick, the item, where it belongs, and the one
 * move it invites — Start (stage → doing) in Next, nothing in Waiting but the
 * name of whoever it is stuck behind and how long it has been there.
 */
function TaskLine({
  r,
  waitingOn,
  onOpen,
  onTick,
  onStart,
  strip,
}: {
  r: TaskRow;
  waitingOn?: { initial: string; color: string } | null;
  onOpen: () => void;
  onTick: () => void;
  onStart?: () => void;
  strip: React.ReactNode;
}) {
  const titleRef = useRef<HTMLButtonElement>(null);
  const press = deadlinePressure(r);
  const wDays = waitingDays(r);
  return (
    <li className="work__task-item">
      <div
        className="work__task"
        onClick={() => {
          titleRef.current?.focus();
          onOpen();
        }}
      >
        <button
          type="button"
          className="work__tick"
          onClick={(e) => {
            e.stopPropagation();
            onTick();
          }}
          aria-label={`Mark ${r.title} done`}
          title="Mark done"
        />
        <span className="dot" style={{ width: 7, height: 7, background: KINDS[r.kind].dot }} />
        <button ref={titleRef} type="button" className="work__task-title">
          {r.title}
        </button>
        <span className="work__task-meta">
          {r.project && <span className="work__task-proj"><span className="dot" style={{ width: 6, height: 6, background: r.project.color }} />{r.project.name}</span>}
          {r.stage === 'waiting' && (
            <span className="work__wait">
              {waitingOn ? (
                <span className="work__avatar" style={{ background: waitingOn.color }}>
                  {waitingOn.initial}
                </span>
              ) : (
                <span className="work__wait-reason">{r.waitingOnReason || 'someone'}</span>
              )}
              {wDays != null && <span className={'work__wait-days' + (wDays >= 5 ? ' is-long' : '')}>{wDays}d</span>}
            </span>
          )}
          {press && r.deadline && <span className={'work__due work__due--' + press}>{press === 'overdue' ? 'Overdue' : 'Due'} {fmtDay(r.deadline)}</span>}
        </span>
        <span className="work__task-act" onClick={(e) => e.stopPropagation()}>
          <WhenPicker id={r.id} scheduledAt={r.scheduledAt} />
          {onStart && (
            <button type="button" className="work__start" onClick={onStart}>
              Start
            </button>
          )}
        </span>
      </div>
      {/* why · when · where — the same line the Projects page shows, so an item reads alike
          everywhere. It steps aside for a proof / start strip, which must sit flush under the line. */}
      {!strip && (
        <div className="work__task-ctx">
          <ContextLine ctx={r.context} onClarify={onOpen} compact />
        </div>
      )}
      {strip}
    </li>
  );
}

/**
 * The "when", re-homed from the Plan calendar. A day is all it asks for —
 * today, tomorrow, the end of the week, or a date off the browser's own
 * picker — and the store opens that day at a sensible hour. Clearing it puts
 * the item back in the backlog. No grid to visit, no slot to aim at.
 */
function WhenPicker({ id, scheduledAt }: { id: string; scheduledAt: string | null }) {
  const { scheduleTaskOnDay, unscheduleTask } = useActions();
  const [open, setOpen] = useState(false);
  const [up, setUp] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const btnRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const now = new Date();
  const later = laterThisWeek(now);
  const pick = (d: Date) => {
    scheduleTaskOnDay(id, ymd(d));
    setOpen(false);
  };

  /**
   * The register scrolls inside .work__tablewrap, so a menu dropped from a row
   * near its bottom edge would be clipped. Measure once on open and flip it
   * upward when there is no room below (and there is room above).
   */
  const toggle = () => {
    const btn = btnRef.current;
    if (btn && !open) {
      const b = btn.getBoundingClientRect();
      const box = btn.closest('.work__tablewrap')?.getBoundingClientRect();
      const floor = Math.min(box ? box.bottom : Infinity, window.innerHeight);
      setUp(b.bottom + MENU_H > floor && b.top - MENU_H > (box ? box.top : 0));
    }
    setOpen((v) => !v);
  };

  return (
    <div className="work__when-pick" ref={ref}>
      <button
        ref={btnRef}
        type="button"
        className={'work__whenbtn' + (scheduledAt ? ' is-set' : '')}
        aria-haspopup="true"
        aria-expanded={open}
        onClick={toggle}
        title={scheduledAt ? 'When you will do this' : 'Set when you will do this'}
      >
        {scheduledAt ? whenLabel(scheduledAt) : 'When…'}
      </button>
      {open && (
        <div className={'work__whenmenu' + (up ? ' work__whenmenu--up' : '')} role="group" aria-label="When">
          <button type="button" className="work__whenitem" onClick={() => pick(now)}>
            Today
          </button>
          <button type="button" className="work__whenitem" onClick={() => pick(addDays(now, 1))}>
            Tomorrow
          </button>
          <button type="button" className="work__whenitem" onClick={() => pick(later.date)}>
            {later.label}
            <span className="work__whenhint">{later.hint}</span>
          </button>
          <label className="work__whenitem work__whenitem--date">
            Pick a date
            <input
              type="date"
              value={scheduledAt ? ymd(new Date(scheduledAt)) : ''}
              onChange={(e) => {
                if (!e.target.value) return;
                scheduleTaskOnDay(id, e.target.value);
                setOpen(false);
              }}
            />
          </label>
          {scheduledAt && (
            <button
              type="button"
              className="work__whenitem work__whenitem--clear"
              onClick={() => {
                unscheduleTask(id);
                setOpen(false);
              }}
            >
              Clear
            </button>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * One table row per work item. The title button is the row's single keyboard
 * tab stop; the ProjectPicker and the origins button that follow it in DOM
 * order are the row's only other controls, so Tab walks title → project →
 * origins → next row's title. There is one open handler, on the <tr>: a
 * pointer click anywhere on the row reaches it, and so does Enter/Space on
 * the title button (a button's activation is a click that bubbles). It
 * focuses the title first so that when the editor closes and hands focus
 * back, it lands on this row rather than on <body> — pointer clicks don't
 * reliably focus buttons on every browser, hence the explicit call.
 * The tick before the title is the register's one inline done control (the
 * store's completion path); a proof or start `strip` for this item renders
 * as a full-width row directly beneath it.
 * `cols` is the visible column set, in header order: every cell is built here
 * and only the shown ones are rendered, so the row can never drift out of step
 * with the header.
 */
function WorkRow({
  r,
  owner,
  isYou,
  waitingOn,
  originCount,
  onOpen,
  onOpenOrigins,
  onTick,
  cols,
  strip,
}: {
  r: TaskRow;
  owner: { initial: string; color: string } | null;
  isYou: boolean;
  waitingOn: { initial: string; color: string } | null;
  originCount: number | undefined;
  onOpen: () => void;
  onOpenOrigins: () => void;
  onTick: () => void;
  cols: SortKey[];
  strip: React.ReactNode;
}) {
  const tone = KINDS[r.kind];
  const titleRef = useRef<HTMLButtonElement>(null);
  const overdue = r.deadline != null && r.stage !== 'done' && r.deadline.slice(0, 10) < todayYMD();
  const wDays = waitingDays(r);
  const openFromRow = () => {
    titleRef.current?.focus();
    onOpen();
  };
  const cells: Record<SortKey, React.ReactNode> = {
    title: (
      <td key="title">
        <span className="work__c-title">
          <button
            type="button"
            className={'work__tick' + (r.stage === 'done' ? ' is-done' : '')}
            onClick={(e) => {
              e.stopPropagation();
              onTick();
            }}
            aria-pressed={r.stage === 'done'}
            aria-label={r.stage === 'done' ? `Mark ${r.title} not done` : `Mark ${r.title} done`}
            title={r.stage === 'done' ? 'Mark not done' : 'Mark done'}
          >
            {r.stage === 'done' ? '✓' : ''}
          </button>
          <span className="dot" style={{ width: 7, height: 7, background: tone.dot }} />
          <button ref={titleRef} type="button" className="work__title-btn">
            {r.title}
          </button>
          {r.recurring && <span className="work__recur" title="repeats">↻</span>}
        </span>
      </td>
    ),
    project: (
      <td key="project" onClick={(e) => e.stopPropagation()}>
        <ProjectPicker taskId={r.id} projectId={r.projectId} />
      </td>
    ),
    client: (
      <td key="client">
        {r.client ? (
          <span className="work__client">
            <span className="dot" style={{ width: 7, height: 7, background: r.client.color }} />
            {r.client.name}
            <span className="work__tier">{r.client.tier.toUpperCase()}</span>
          </span>
        ) : (
          <span className="work__muted">—</span>
        )}
      </td>
    ),
    kind: (
      <td key="kind">
        <span className="work__kind" style={{ color: tone.chipFg, background: tone.chipBg }}>
          {tone.label}
        </span>
      </td>
    ),
    stage: (
      <td key="stage">
        <span className={'work__stage work__stage--' + r.stage}>{STAGE_LABEL[r.stage]}</span>
      </td>
    ),
    owner: (
      <td key="owner">
        {owner ? (
          <span className="work__owner" title={isYou ? 'You' : 'Owner'}>
            <span className="work__avatar" style={{ background: owner.color }}>
              {owner.initial}
            </span>
            {isYou && <span className="work__owner-you">You</span>}
          </span>
        ) : r.ownerId ? (
          <span className="work__owner">
            <span className="work__avatar work__avatar--unknown">?</span>
          </span>
        ) : (
          <span className="work__muted">—</span>
        )}
      </td>
    ),
    why: (
      <td key="why">
        {r.requirement ? (
          <span className={'work__why' + (r.requirement.status === 'met' ? ' is-met' : '')} title={r.requirement.title}>
            {r.requirement.status === 'met' && <span className="work__why-check" aria-hidden>✓</span>}
            {r.requirement.title}
          </span>
        ) : r.unscoped ? (
          <span className="work__unscoped" title="Serves no requirement yet — open the item to scope it">
            Unscoped
          </span>
        ) : (
          <span className="work__muted">—</span>
        )}
      </td>
    ),
    // nothing behind it (or a count the server hasn't sent) reads as an em dash,
    // the same "nothing here" every other column uses; the button still opens it
    origins: (
      <td key="origins" className="tk-num" onClick={(e) => e.stopPropagation()}>
        <button
          type="button"
          className={'work__origins' + (originCount ? ' has-some' : '')}
          onClick={onOpenOrigins}
          title={originCount ? `From ${originCount} signal${originCount === 1 ? '' : 's'} — open to see them` : 'Where this came from'}
          aria-label={originCount ? `${originCount} origin${originCount === 1 ? '' : 's'}` : 'Origins'}
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
            <path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1 1" />
            <path d="M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1-1" />
          </svg>
          <span className="tnum">{originCount ? originCount : '—'}</span>
        </button>
      </td>
    ),
    waiting: (
      <td key="waiting">
        {r.stage === 'waiting' ? (
          <span className="work__wait" title={r.waitingOnReason || undefined}>
            {waitingOn ? (
              <span className="work__avatar" style={{ background: waitingOn.color }}>
                {waitingOn.initial}
              </span>
            ) : (
              <span className="work__wait-reason">{r.waitingOnReason || 'someone'}</span>
            )}
            {wDays != null && <span className={'work__wait-days' + (wDays >= 5 ? ' is-long' : '')}>{wDays}d</span>}
          </span>
        ) : (
          <span className="work__muted">—</span>
        )}
      </td>
    ),
    priority: (
      <td key="priority">
        {r.important || r.urgent ? (
          <span className="work__prio">
            {r.important && <span className="work__flag work__flag--imp" title="Important">Imp</span>}
            {r.urgent && <span className="work__flag work__flag--urg" title="Urgent">Urg</span>}
          </span>
        ) : (
          <span className="work__muted">—</span>
        )}
      </td>
    ),
    effort: (
      <td key="effort" className="tk-num">
        {effortLabel(r.effortMinutes).replace('≈ ', '')}
      </td>
    ),
    // the register's "when" is the same one-click control the sections carry
    scheduled: (
      <td key="scheduled" onClick={(e) => e.stopPropagation()}>
        <WhenPicker id={r.id} scheduledAt={r.scheduledAt} />
      </td>
    ),
    deadline: <td key="deadline">{r.deadline ? <span className={'work__deadline' + (overdue ? ' is-overdue' : '')}>{fmtDay(r.deadline)}</span> : <span className="work__muted">—</span>}</td>,
    created: (
      <td key="created" className="tk-num work__muted">
        {ageDays(r.createdAt)}d
      </td>
    ),
  };

  return (
    <>
      <tr className={'work__row' + (r.stage === 'done' ? ' work__row--done' : '')} onClick={openFromRow}>
        {cols.map((k) => cells[k])}
      </tr>
      {strip && (
        <tr className="work__striprow">
          <td colSpan={cols.length}>{strip}</td>
        </tr>
      )}
    </>
  );
}
