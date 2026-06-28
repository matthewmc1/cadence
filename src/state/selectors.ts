import type { Task as NTask, Project as NProject } from '../api/types';
import type { TodayItem, WeekDay, MonthGrid, MonthDay, PlacedTask, BacklogTask, Project, BoardTask, BoardColumn } from './types';
import { KINDS, WEEK_MODEL, type Kind } from '../lib/energy';
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

export function selectBacklog(tasks: NTask[]): BacklogTask[] {
  return tasks
    .filter((t) => t.status === 'backlog' && t.projectId == null && t.scheduledAt == null)
    .sort((a, b) => a.position - b.position)
    .map((t) => ({
      id: t.id,
      title: t.title,
      kind: t.kind,
      tag: t.urgent ? 'Urgent' : KIND_SHORT[t.kind],
      urgent: t.urgent,
      effortHrs: t.effortMinutes / 60,
    }));
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

  // peak 2-hour window (weekdays) with the most completions
  let peakStart = 9;
  let peakCount = 0;
  for (let h = 7; h <= 18; h++) {
    const hi = INSIGHT_HOURS.indexOf(h);
    const hi2 = INSIGHT_HOURS.indexOf(h + 1);
    const c = (hi >= 0 ? sumWeekday(counts[hi]) : 0) + (hi2 >= 0 ? sumWeekday(counts[hi2]) : 0);
    if (c > peakCount) {
      peakCount = c;
      peakStart = h;
    }
  }

  // dip: working hour (8..17) with the fewest completions
  let dipHour = 13;
  let dipCount = Infinity;
  for (let h = 8; h <= 17; h++) {
    const hi = INSIGHT_HOURS.indexOf(h);
    const c = hi >= 0 ? sumWeekday(counts[hi]) : 0;
    if (c < dipCount) {
      dipCount = c;
      dipHour = h;
    }
  }

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
