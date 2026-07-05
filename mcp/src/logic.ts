/** Date math, recurrence expansion, focus heuristics, and formatting. */
import type { Task, Project, Kind, Status, Recurrence } from './api.js';

/* ---------------------------------------------------------------- dates */

export function startOfDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}
export function addDays(d: Date, n: number): Date {
  const x = new Date(d);
  x.setDate(d.getDate() + n);
  return x;
}
export function startOfWeek(d: Date): Date {
  const m = startOfDay(d);
  m.setDate(m.getDate() - ((m.getDay() + 6) % 7));
  return m;
}
export function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

const WD = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const MO = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

export function fmtTime(d: Date): string {
  const h = d.getHours();
  const m = d.getMinutes();
  const h12 = ((h + 11) % 12) + 1;
  return `${h12}:${String(m).padStart(2, '0')}${h >= 12 ? 'pm' : 'am'}`;
}
export function fmtDate(d: Date): string {
  return `${WD[d.getDay()]} ${d.getDate()} ${MO[d.getMonth()]}`;
}
export function fmtWhen(iso: string | null): string {
  if (!iso) return 'unscheduled';
  const d = new Date(iso);
  return `${fmtDate(d)}, ${fmtTime(d)}`;
}

/** Occurrences of a (possibly recurring) scheduled task within [start, end). */
export function occurrences(iso: string, recurrence: Recurrence, start: Date, end: Date): Date[] {
  const anchor = new Date(iso);
  const anchorDay = startOfDay(anchor);
  const ah = anchor.getHours();
  const am = anchor.getMinutes();
  const out: Date[] = [];
  if (recurrence === 'none') {
    if (anchor >= start && anchor < end) out.push(anchor);
    return out;
  }
  let d = startOfDay(new Date(Math.max(start.getTime(), anchorDay.getTime())));
  let guard = 0;
  while (d < end && guard++ < 400) {
    const wd = d.getDay();
    let hit = false;
    if (recurrence === 'daily') hit = true;
    else if (recurrence === 'weekdays') hit = wd >= 1 && wd <= 5;
    else if (recurrence === 'weekly') hit = wd === anchorDay.getDay();
    else if (recurrence === 'monthly') hit = d.getDate() === anchorDay.getDate();
    if (hit) out.push(new Date(d.getFullYear(), d.getMonth(), d.getDate(), ah, am));
    d = addDays(d, 1);
  }
  return out;
}

/* ------------------------------------------------------------- labels */

export const KIND_LABEL: Record<Kind, string> = {
  deep: 'Deep focus',
  light: 'Light',
  admin: 'Admin',
  meet: 'Meeting',
  personal: 'Personal',
};
export const STATUS_LABEL: Record<Status, string> = {
  backlog: 'Backlog',
  scheduled: 'Scheduled',
  focus: 'In focus',
  done: 'Done',
};

export function effortLabel(min: number): string {
  if (min >= 60) {
    const h = min / 60;
    return `${Number.isInteger(h) ? h : h.toFixed(1)}h`;
  }
  return `${min}m`;
}

/** Cadence's energy windows (peak late morning, post-lunch dip). */
export function energyOf(hour: number): 'peak' | 'good' | 'dip' | 'low' {
  if (hour >= 9 && hour < 11) return 'peak';
  if (hour >= 13 && hour < 14) return 'dip';
  if ((hour >= 8 && hour < 12) || (hour >= 14 && hour < 17)) return 'good';
  return 'low';
}

/* --------------------------------------------------------------- helpers */

export function projectOf(t: Task, projects: Project[]): Project | undefined {
  return t.projectId ? projects.find((p) => p.id === t.projectId) : undefined;
}

export function isOverdue(t: Task, now: Date): boolean {
  return !!t.deadline && t.status !== 'done' && new Date(t.deadline) < now;
}
export function dueWithin(t: Task, now: Date, days: number): boolean {
  if (!t.deadline || t.status === 'done') return false;
  const dl = new Date(t.deadline).getTime();
  return dl >= now.getTime() && dl <= now.getTime() + days * 86400000;
}

/** Markdown one-liner for a task. */
export function taskLine(t: Task, projects: Project[], at?: Date): string {
  const p = projectOf(t, projects);
  const bits: string[] = [`**${t.title}**`];
  bits.push(`_${KIND_LABEL[t.kind]} · ${effortLabel(t.effortMinutes)}_`);
  if (at) bits.push(`@ ${fmtTime(at)}`);
  else if (t.scheduledAt && !at) bits.push(`@ ${fmtWhen(t.scheduledAt)}`);
  if (t.recurrence !== 'none') bits.push(`↻ ${t.recurrence}`);
  if (p) bits.push(`→ ${p.name}`);
  if (t.deadline) bits.push(`⏰ due ${fmtDate(new Date(t.deadline))}`);
  if (t.urgent) bits.push('🔴 urgent');
  if (t.important) bits.push('⭐ important');
  if (t.place) bits.push(`· ${t.place}`);
  if (t.status === 'done' && t.reflection) bits.push(`— advanced: ${t.reflection}`);
  const subs = t.subtasks.length ? ` (${t.subtasks.filter((s) => s.done).length}/${t.subtasks.length} subtasks)` : '';
  const who = t.assignees.length ? ` 👥 ${t.assignees.map((a) => a.initial).join(',')}` : '';
  // UUIDv7 ids share a timestamp prefix, so the unique part is the suffix.
  return `- ${bits.join(' ')}${subs}${who}  \`#${t.id.slice(-8)}\``;
}

/* ----------------------------------------------------------- task views */

export interface DatedTask {
  task: Task;
  at: Date;
}

/** Tasks (with recurrence expanded) that fall in [start, end). */
export function inRange(tasks: Task[], start: Date, end: Date, includeDone = false): DatedTask[] {
  const out: DatedTask[] = [];
  for (const t of tasks) {
    if (!t.scheduledAt) continue;
    if (!includeDone && t.status === 'done') continue;
    for (const at of occurrences(t.scheduledAt, t.recurrence, start, end)) out.push({ task: t, at });
  }
  return out.sort((a, b) => a.at.getTime() - b.at.getTime());
}
