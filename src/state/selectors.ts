import type { Task as NTask, Project as NProject, Client as NClient, ClientTier, ClientKind } from '../api/types';
import type { TodayItem, WeekDay, MonthGrid, MonthDay, PlacedTask, BacklogTask, Project, BoardTask, BoardColumn } from './types';
import { KINDS, WEEK_MODEL, DEFAULT_PROFILE, type Kind, type EnergyProfile } from '../lib/energy';
import { occurrences, hourOf, sameDay, addDays, startOfWeek, startOfMonth, monthLabel, ymd } from '../lib/time';

/* ------------------------------------------------------------- formatters */

export function clock(hour: number): string {
  const h = Math.floor(hour);
  const m = Math.round((hour - h) * 60);
  const h12 = ((h + 11) % 12) + 1;
  return `${h12}:${m.toString().padStart(2, '0')}`;
}

export function effortLabel(min: number): string {
  if (min >= 60) {
    const hrs = min / 60;
    return `≈ ${Number.isInteger(hrs) ? hrs : hrs.toFixed(1)} hrs`;
  }
  return `≈ ${min} min`;
}

function effortShort(min: number): string {
  if (min >= 60) {
    const hrs = min / 60;
    return `${Number.isInteger(hrs) ? hrs : hrs.toFixed(1)}h`;
  }
  return `${min}m`;
}

const KIND_SHORT: Record<Kind, string> = {
  deep: 'Deep',
  light: 'Light',
  admin: 'Admin',
  meet: 'Meet',
  personal: 'Personal',
};

const DAY_SHORT = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const STATUS_TO_COLUMN: Record<NTask['status'], BoardColumn> = {
  backlog: 'backlog',
  scheduled: 'week',
  focus: 'focus',
  done: 'done',
};

/* ------------------------------------------------------------------ Today */

function assigneesOf(t: NTask) {
  return (t.assignees ?? []).map((a) => ({ initial: a.initial, color: a.color }));
}

export function selectToday(tasks: NTask[], today: Date): TodayItem[] {
  const start = new Date(today.getFullYear(), today.getMonth(), today.getDate());
  const end = addDays(start, 1);
  const items: TodayItem[] = [];
  for (const t of tasks) {
    if (!t.scheduledAt) continue;
    for (const occ of occurrences(t.scheduledAt, t.recurrence, start, end)) {
      const hour = hourOf(occ);
      const hero = t.status === 'focus';
      const done = t.status === 'done';
      items.push({
        id: t.id,
        title: t.title,
        kind: t.kind,
        hour,
        end: hero ? hour + t.effortMinutes / 60 : undefined,
        timeLabel: clock(hour),
        status: done ? 'past' : hero ? 'now' : 'upcoming',
        effortLabel: effortLabel(t.effortMinutes),
        place: t.place ?? undefined,
        dot: KINDS[t.kind].dot,
        hero,
        ghost: !hero && !done && hour >= 18,
        done,
        rationale: t.note || undefined,
      });
    }
  }
  return items.sort((a, b) => a.hour - b.hour);
}

/* ------------------------------------------------------------------ Plan */

function placedFor(tasks: NTask[], dayStart: Date, weekend: boolean): PlacedTask[] {
  const dayEnd = addDays(dayStart, 1);
  const out: PlacedTask[] = [];
  for (const t of tasks) {
    if (!t.scheduledAt || t.status === 'done') continue;
    for (const occ of occurrences(t.scheduledAt, t.recurrence, dayStart, dayEnd)) {
      out.push({
        id: t.id,
        title: t.title,
        kind: t.kind,
        hour: hourOf(occ),
        weekend,
        recurring: t.recurrence !== 'none',
        assignees: assigneesOf(t),
      });
    }
  }
  return out.sort((a, b) => a.hour - b.hour);
}

/** Tasks placed on the displayed Mon–Sun week (recurrence expanded). */
export function selectWeek(tasks: NTask[], weekStart: Date): WeekDay[] {
  const today = new Date();
  return WEEK_MODEL.map((d, di) => {
    const dayStart = addDays(weekStart, di);
    return {
      name: d.name,
      short: d.short,
      date: String(dayStart.getDate()),
      scale: d.scale,
      weekend: d.weekend,
      isToday: sameDay(dayStart, today),
      load: d.load,
      loadColor: d.loadColor,
      tasks: placedFor(tasks, dayStart, d.weekend),
    };
  });
}

/** A 6×7 calendar grid for the anchor's month (recurrence expanded). */
export function selectMonth(tasks: NTask[], anchor: Date): MonthGrid {
  const today = new Date();
  const gridStart = startOfWeek(startOfMonth(anchor));
  const weeks: MonthDay[][] = [];
  for (let w = 0; w < 6; w++) {
    const row: MonthDay[] = [];
    for (let i = 0; i < 7; i++) {
      const day = addDays(gridStart, w * 7 + i);
      const wd = day.getDay();
      row.push({
        date: ymd(day),
        dayNum: day.getDate(),
        inMonth: day.getMonth() === anchor.getMonth(),
        isToday: sameDay(day, today),
        weekend: wd === 0 || wd === 6,
        tasks: placedFor(tasks, day, wd === 0 || wd === 6),
      });
    }
    weeks.push(row);
  }
  return { label: monthLabel(anchor), weeks };
}

/**
 * Eisenhower priority. Importance outranks mere urgency so deep, long-term work
 * isn't perpetually crowded out by loud-but-shallow tasks:
 * important+urgent (3) > important (2) > urgent (1) > neither (0).
 */
export function priorityScore(t: { urgent?: boolean | null; important?: boolean | null }): number {
  return (t.important ? 2 : 0) + (t.urgent ? 1 : 0);
}

/** Every unscheduled backlog task — across all projects — for the planning board. */
export function selectBacklog(tasks: NTask[], projects: NProject[] = []): BacklogTask[] {
  const projById = new Map(projects.map((p) => [p.id, p]));
  return tasks
    .filter((t) => t.status === 'backlog' && t.scheduledAt == null)
    .sort((a, b) => priorityScore(b) - priorityScore(a) || a.position - b.position)
    .map((t) => {
      const proj = t.projectId ? projById.get(t.projectId) : undefined;
      return {
        id: t.id,
        title: t.title,
        kind: t.kind,
        tag: t.urgent ? 'Urgent' : t.important ? 'Important' : KIND_SHORT[t.kind],
        urgent: t.urgent,
        important: t.important,
        effortHrs: t.effortMinutes / 60,
        project: proj ? { name: proj.name, color: proj.color } : undefined,
      };
    });
}

/* ----------------------------------------------------------------- Board */

export function selectProject(tasks: NTask[], project: NProject | undefined): Project | null {
  if (!project) return null;
  const items = tasks.filter((t) => t.projectId === project.id);

  const boardTasks: BoardTask[] = items
    .slice()
    .sort((a, b) => a.position - b.position)
    .map((t) => {
      const column = STATUS_TO_COLUMN[t.status];
      const base: BoardTask = {
        id: t.id,
        title: t.title,
        kind: t.kind,
        column,
        tagLabel: `${KIND_SHORT[t.kind]} · ${effortShort(t.effortMinutes)}`,
      };
      if (column === 'week' && t.scheduledAt) {
        const dt = new Date(t.scheduledAt);
        base.when = `${DAY_SHORT[(dt.getDay() + 6) % 7]} · ${clock(hourOf(dt))}`;
        base.tagLabel = base.when;
        if (t.place) base.place = t.place;
      }
      if (column === 'focus') {
        base.until = 'until 11:00';
        if (t.place) base.place = t.place;
      }
      if (column === 'done' && t.doneAt) {
        base.doneDay = new Date(t.doneAt).toLocaleDateString('en-US', { weekday: 'short' });
        base.donePlace = t.place ?? undefined;
      }
      return base;
    });

  const done = items.filter((t) => t.status === 'done').length;
  const deepLeft = items.filter((t) => t.kind === 'deep' && t.status !== 'done').length;

  return {
    id: project.id,
    clientId: project.clientId,
    name: project.name,
    subtitle: project.subtitle,
    due: project.due ?? '',
    members: (project.members ?? []).map((m) => ({ initial: m.initial, color: m.color })),
    done,
    total: items.length,
    deepLeft,
    tasks: boardTasks,
  };
}

/* -------------------------------------------------------------- Insights */

export interface Insights {
  hours: number[];
  grid: number[][]; // [hourIndex][dayIndex] normalized 0..1
  total: number;
  peak: { label: string; share: number };
  dip: { label: string };
  locations: { key: string; label: string; pct: number; color: string }[];
  heroDay: string; // weekday you ship most
  adminDay: string; // weekday that runs admin-heavy
  bestWhen: string; // e.g. "weekday mornings"
  topPlace: string; // e.g. "home"
  profile: EnergyProfile; // the learned peak/dip that scheduling now uses
}

const MIN_PROFILE_SAMPLES = 5;

/**
 * Learn where the user's focus peak and low-energy dip actually fall, from the
 * hours at which they complete work (weekdays only). Falls back to the
 * canonical DEFAULT_PROFILE until there's enough signal, so a new account
 * schedules exactly as before. This is what closes the loop between what
 * Insights *measures* and how `bestSlot` / the AI scheduler *place* work.
 */
export function deriveEnergyProfile(tasks: NTask[]): EnergyProfile {
  const perHour = new Array(24).fill(0);
  let samples = 0;
  for (const t of tasks) {
    if (t.status !== 'done' || !t.doneAt) continue;
    const d = new Date(t.doneAt);
    if (((d.getUTCDay() + 6) % 7) > 4) continue; // weekdays shape the working profile
    perHour[d.getUTCHours()]++;
    samples++;
  }
  if (samples < MIN_PROFILE_SAMPLES) return DEFAULT_PROFILE;

  let peakStart = DEFAULT_PROFILE.peakStart;
  let peakBest = -1;
  for (let h = 7; h <= 17; h++) {
    const c = perHour[h] + perHour[h + 1];
    if (c > peakBest) {
      peakBest = c;
      peakStart = h;
    }
  }
  let dipHour = DEFAULT_PROFILE.dipHour;
  let dipBest = Infinity;
  for (let h = 8; h <= 17; h++) {
    if (h >= peakStart && h < peakStart + 2) continue; // the dip isn't the peak
    if (perHour[h] < dipBest) {
      dipBest = perHour[h];
      dipHour = h;
    }
  }
  return { peakStart, peakEnd: peakStart + 2, dipHour, samples, learned: true };
}

const INSIGHT_HOURS = [7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20];
const LOC_META: Record<string, { label: string; color: string }> = {
  Home: { label: 'Home', color: '#C2743D' },
  Office: { label: 'Office', color: '#7E93A6' },
  'Café': { label: 'Café', color: '#94A08A' },
  Cafe: { label: 'Café', color: '#94A08A' },
};

function hourLabel(h: number): string {
  const h12 = ((h + 11) % 12) + 1;
  const ampm = h >= 12 ? ' PM' : ' AM';
  return `${h12}:00${ampm}`;
}

/** Compute Insights purely from completed (`done`) tasks — no mock data. */
export function computeInsights(tasks: NTask[]): Insights {
  const done = tasks.filter((t) => t.status === 'done' && t.doneAt);
  const counts = INSIGHT_HOURS.map(() => [0, 0, 0, 0, 0, 0, 0]);
  const dayDeep = [0, 0, 0, 0, 0, 0, 0];
  const dayAdmin = [0, 0, 0, 0, 0, 0, 0];
  const placeCount: Record<string, number> = {};
  let total = 0;

  for (const t of done) {
    const d = new Date(t.doneAt as string);
    const di = (d.getUTCDay() + 6) % 7; // Mon=0
    const hi = INSIGHT_HOURS.indexOf(d.getUTCHours());
    if (hi >= 0) {
      counts[hi][di]++;
      total++;
    }
    if (t.kind === 'deep') dayDeep[di]++;
    if (t.kind === 'admin') dayAdmin[di]++;
    if (t.place) {
      const key = t.place === 'Cafe' ? 'Café' : t.place;
      placeCount[key] = (placeCount[key] ?? 0) + 1;
    }
  }

  const max = Math.max(1, ...counts.flat());
  const grid = counts.map((row) => row.map((c) => c / max));

  // peak/dip come from the learned energy profile, so Insights and the
  // scheduler agree on where the user's focus actually falls
  const profile = deriveEnergyProfile(tasks);
  const peakStart = profile.peakStart;
  const dipHour = profile.dipHour;
  const idx = (h: number) => INSIGHT_HOURS.indexOf(h);
  const peakCount =
    (idx(peakStart) >= 0 ? sumWeekday(counts[idx(peakStart)]) : 0) +
    (idx(peakStart + 1) >= 0 ? sumWeekday(counts[idx(peakStart + 1)]) : 0);

  const locTotal = Object.values(placeCount).reduce((a, b) => a + b, 0) || 1;
  const locations = Object.entries(placeCount)
    .map(([k, n]) => ({
      key: k,
      label: LOC_META[k]?.label ?? k,
      color: LOC_META[k]?.color ?? '#B9A98F',
      pct: Math.round((n / locTotal) * 100),
    }))
    .sort((a, b) => b.pct - a.pct);

  const heroDay = DAY_SHORT[argmax(dayDeep)];
  const adminDay = DAY_SHORT[argmax(dayAdmin)];
  const topPlace = locations[0]?.label.toLowerCase() ?? 'home';
  const bestWhen = peakStart < 12 ? 'weekday mornings' : peakStart < 17 ? 'weekday afternoons' : 'weekday evenings';

  return {
    hours: INSIGHT_HOURS,
    grid,
    total,
    peak: { label: `${hourLabel(peakStart).replace(' AM', '').replace(' PM', '')} – ${hourLabel(peakStart + 2)}`, share: Math.round((peakCount / Math.max(1, total)) * 100) },
    dip: { label: `${hourLabel(dipHour).replace(' AM', '').replace(' PM', '')} – ${hourLabel(dipHour + 1)}` },
    locations,
    heroDay,
    adminDay,
    bestWhen,
    topPlace,
    profile,
  };
}

function sumWeekday(row: number[]): number {
  return row[0] + row[1] + row[2] + row[3] + row[4];
}
function argmax(a: number[]): number {
  let mi = 0;
  for (let i = 1; i < a.length; i++) if (a[i] > a[mi]) mi = i;
  return mi;
}

/* -------------------------------------------------------------- Signals */

/**
 * A prescriptive nudge — something that needs attention, computed from data
 * already on the tasks. Unlike Insights (which describes the past), a Signal
 * tells you what to *do* now to protect long-horizon work.
 */
export interface Signal {
  id: string;
  kind: 'act' | 'watch'; // act = accent (do it), watch = quieter heads-up
  title: string; // imperative headline
  detail: string; // one line of why
  taskId?: string; // optional deep-link to the task it's about
}

// Thresholds (kept here so a later effort-budget model can supersede them).
const SLIP_AGING_DAYS = 3;
const STALL_DAYS = 10;
const WEEKLY_DEEP_BUDGET_HRS = 15; // ~3h/day × 5 — the scarce deep-focus capacity

// Whole calendar days between two instants, both floored to local midnight
// (so "yesterday at 23:00 → today at 01:00" is 1 day, not 0). Shared by every
// aging calculation so slip/stall/underserved all count days the same way.
function startOfDayMs(ms: number): number {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}
function calDaysSince(ms: number, nowMs: number): number {
  return Math.round((startOfDayMs(nowMs) - startOfDayMs(ms)) / 86400000);
}

/**
 * Turn the current task set into a short list of actionable signals. Everything
 * here is derived from fields already present (important/urgent, projectId,
 * scheduledAt, doneAt, effortMinutes) — no new data required.
 */
export function computeSignals(tasks: NTask[], projects: NProject[], clients: NClient[], profile: EnergyProfile, now: Date): Signal[] {
  const out: Signal[] = [];
  const nowMs = now.getTime();
  const dSince = (ms: number) => calDaysSince(ms, nowMs);
  const weekStart = startOfWeek(now);
  const weekEnd = addDays(weekStart, 7);

  // 1) important, non-urgent work aging unscheduled in the backlog — the exact
  //    long-horizon work that gets crowded out. Flag it, and note if urgent
  //    work is currently occupying the peak it should be in.
  const urgentInPeak = tasks.some((t) => {
    if (t.status === 'done' || !t.urgent || !t.scheduledAt) return false;
    return occurrences(t.scheduledAt, t.recurrence, weekStart, weekEnd).some((d) => {
      const h = hourOf(d);
      return h >= profile.peakStart && h < profile.peakEnd;
    });
  });
  const slipping = tasks
    .filter((t) => t.status === 'backlog' && t.scheduledAt == null && t.important && !t.urgent && dSince(new Date(t.createdAt).getTime()) >= SLIP_AGING_DAYS)
    .sort((a, b) => new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime());
  slipping.slice(0, 2).forEach((t, i) => {
    const age = dSince(new Date(t.createdAt).getTime()); // always ≥ SLIP_AGING_DAYS (3), so plural
    // only the first (oldest) card carries the peak-contention note, to avoid repeating it
    const peakNote = i === 0 && urgentInPeak ? ', while urgent work fills your peak this week' : '';
    out.push({
      id: 'slip:' + t.id,
      kind: 'act',
      title: `Give “${t.title}” a peak slot`,
      detail: `Important, not urgent — waiting ${age} days in the backlog${peakNote}.`,
      taskId: t.id,
    });
  });

  // 2) projects with open work but no recent completion — strategic drift.
  const stalled: { p: NProject; idle: number; deepLeft: number; hasDone: boolean }[] = [];
  for (const p of projects) {
    const items = tasks.filter((t) => t.projectId === p.id);
    const open = items.filter((t) => t.status !== 'done');
    if (!open.length) continue;
    const doneMs = items.filter((t) => t.doneAt).map((t) => new Date(t.doneAt as string).getTime());
    const oldestOpen = Math.min(...open.map((t) => new Date(t.createdAt).getTime()));
    const hasDone = doneMs.length > 0;
    // a project can't be "idle" longer than it has existed — clamp to project age
    const projAge = dSince(new Date(p.createdAt).getTime());
    const idle = Math.min(hasDone ? dSince(Math.max(...doneMs)) : dSince(oldestOpen), projAge);
    const deepLeft = open.filter((t) => t.kind === 'deep').length;
    if (idle >= STALL_DAYS && deepLeft > 0) stalled.push({ p, idle, deepLeft, hasDone });
  }
  stalled.sort((a, b) => b.idle - a.idle);
  for (const s of stalled.slice(0, 2)) {
    const lead = s.hasDone ? `No completed work in ${s.idle} days` : `No completed work yet · oldest task waiting ${s.idle} days`;
    out.push({
      id: 'stall:' + s.p.id,
      kind: 'watch',
      title: `“${s.p.name}” is stalling`,
      detail: `${lead} · ${s.deepLeft} deep task${s.deepLeft === 1 ? '' : 's'} still waiting.`,
    });
  }

  // 3) this week scheduled over the realistic deep-focus budget — overcommitment.
  const deepMins = tasks.reduce((sum, t) => {
    if (t.status === 'done' || t.kind !== 'deep' || !t.scheduledAt) return sum;
    return sum + occurrences(t.scheduledAt, t.recurrence, weekStart, weekEnd).length * t.effortMinutes;
  }, 0);
  const deepHrs = deepMins / 60;
  if (deepHrs > WEEKLY_DEEP_BUDGET_HRS) {
    out.push({
      id: 'overload:week',
      kind: 'act',
      title: 'This week is over your focus budget',
      detail: `${Math.round(deepHrs * 10) / 10}h of deep work scheduled vs ~${WEEKLY_DEEP_BUDGET_HRS}h you can realistically protect — defer or delegate some.`,
    });
  }

  // 4) clients past their touch cadence — strategic neglect. Completions roll up
  //    through projects to the client they serve; A-tier surfaces first.
  const quiet = clients
    .map((c) => ({ c, s: clientTouch(c, projects, tasks, nowMs) }))
    .filter(({ s }) => s.underserved)
    .sort((a, b) => tierRank(a.c.tier) - tierRank(b.c.tier));
  for (const { c, s } of quiet.slice(0, 2)) {
    const since = s.daysSince == null ? 'no completed work yet' : `${s.daysSince} days since last touch`;
    out.push({
      id: 'client:' + c.id,
      kind: 'act',
      title: `${c.name} is going quiet`,
      detail: `Tier ${c.tier.toUpperCase()} · ${since} — aim to touch every ${c.expectedTouchDays} day${c.expectedTouchDays === 1 ? '' : 's'}.`,
    });
  }

  return out;
}

/* ---------------------------------------------------------- Recently shipped */

/** A completed task, with its "what it advanced" reflection and project. */
export interface DoneItem {
  id: string;
  title: string;
  kind: Kind;
  doneAt: string;
  reflection: string;
  project?: { name: string; color: string };
}

/** The most recent completions — the "what got done, and why" review surface. */
export function selectRecentDone(tasks: NTask[], projects: NProject[] = [], limit = 8): DoneItem[] {
  const projById = new Map(projects.map((p) => [p.id, p]));
  return tasks
    .filter((t) => t.status === 'done' && t.doneAt)
    .sort((a, b) => new Date(b.doneAt as string).getTime() - new Date(a.doneAt as string).getTime())
    .slice(0, limit)
    .map((t) => {
      const p = t.projectId ? projById.get(t.projectId) : undefined;
      return {
        id: t.id,
        title: t.title,
        kind: t.kind,
        doneAt: t.doneAt as string,
        reflection: t.reflection,
        project: p ? { name: p.name, color: p.color } : undefined,
      };
    });
}

/* --------------------------------------------------------------- Clients */

function tierRank(t: ClientTier): number {
  return t === 'a' ? 0 : t === 'b' ? 1 : 2;
}

interface ClientTouch {
  projectCount: number;
  openCount: number;
  doneCount: number;
  lastTouchMs: number | null; // most recent completion across the client's work
  daysSince: number | null;
  underserved: boolean;
}

/**
 * Roll a client's activity up through its projects: how much open/done work it
 * has, when it was last touched (a completion), and whether that exceeds its
 * expected touch cadence. Tasks inherit their client through their project, so
 * a task counts for a client iff its project belongs to that client.
 */
function clientTouch(client: NClient, projects: NProject[], tasks: NTask[], nowMs: number): ClientTouch {
  const projectIds = new Set(projects.filter((p) => p.clientId === client.id).map((p) => p.id));
  let openCount = 0;
  let doneCount = 0;
  let lastTouchMs: number | null = null;
  for (const t of tasks) {
    if (!t.projectId || !projectIds.has(t.projectId)) continue;
    if (t.status === 'done') {
      doneCount++;
      if (t.doneAt) {
        const ms = new Date(t.doneAt).getTime();
        if (lastTouchMs == null || ms > lastTouchMs) lastTouchMs = ms;
      }
    } else {
      openCount++;
    }
  }
  const daysSince = lastTouchMs == null ? null : calDaysSince(lastTouchMs, nowMs);
  let underserved = false;
  if (!client.archivedAt && client.expectedTouchDays != null && projectIds.size > 0) {
    // never touched → measure neglect from when the client was created
    underserved = lastTouchMs == null
      ? calDaysSince(new Date(client.createdAt).getTime(), nowMs) > client.expectedTouchDays
      : daysSince! > client.expectedTouchDays;
  }
  return { projectCount: projectIds.size, openCount, doneCount, lastTouchMs, daysSince, underserved };
}

/** A client with its rolled-up health — the "who is this for / who's underserved" surface. */
export interface ClientHealth {
  id: string;
  name: string;
  tier: ClientTier;
  kind: ClientKind;
  color: string;
  expectedTouchDays: number | null;
  projectCount: number;
  openCount: number;
  doneCount: number;
  lastTouch: string | null; // ISO of most recent completion
  daysSince: number | null;
  underserved: boolean;
}

/** Active clients ranked by what needs attention: underserved first, then tier. */
export function selectClientHealth(tasks: NTask[], projects: NProject[], clients: NClient[], now: Date): ClientHealth[] {
  const nowMs = now.getTime();
  return clients
    .filter((c) => !c.archivedAt)
    .map((c) => {
      const s = clientTouch(c, projects, tasks, nowMs);
      return {
        id: c.id,
        name: c.name,
        tier: c.tier,
        kind: c.kind,
        color: c.color,
        expectedTouchDays: c.expectedTouchDays,
        projectCount: s.projectCount,
        openCount: s.openCount,
        doneCount: s.doneCount,
        lastTouch: s.lastTouchMs == null ? null : new Date(s.lastTouchMs).toISOString(),
        daysSince: s.daysSince,
        underserved: s.underserved,
      };
    })
    .sort((a, b) => {
      if (a.underserved !== b.underserved) return a.underserved ? -1 : 1;
      if (a.tier !== b.tier) return tierRank(a.tier) - tierRank(b.tier);
      return a.name.localeCompare(b.name);
    });
}
