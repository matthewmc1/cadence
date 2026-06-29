/**
 * LLM-driven scheduling of backlog tasks into the displayed week.
 *
 * The model does the *judgement* (energy-aware placement, deadline awareness,
 * spreading the load); `reconcile()` then guarantees the result is always
 * complete, in-range and collision-free — so a small local model can't produce
 * a broken schedule. Missing/invalid placements fall back to the heuristic
 * `bestSlot`.
 */
import { chatJSON } from './llm';
import { bestSlot, type Kind } from '../lib/energy';

export interface SchedTask {
  id: string;
  title: string;
  kind: Kind;
  effortHrs: number;
  urgent: boolean;
  deadline?: string; // ISO date
  project?: string; // project name
}

export interface DayCtx {
  index: number; // 0 = Mon … 6 = Sun
  name: string; // 'Mon'
  date: string; // '29'
  existing: number; // tasks already scheduled that day
  isPast: boolean;
}

export interface WeekCtx {
  weekLabel: string; // 'Week of 29 Jun – 5 Jul'
  todayIndex: number; // 0..6, or -1 if the displayed week isn't the current one
  days: DayCtx[];
}

export interface Assignment {
  id: string;
  day: number; // 0..6
  hour: number; // 8..18
  why?: string;
}

const SYSTEM = `You are Cadence's scheduling assistant. You place a user's backlog tasks into a Mon–Sun week, arranged around human energy.

Rules:
- Energy windows: 09:00–11:00 is the deep-focus PEAK — schedule "deep" work there. 13:00 is the post-lunch DIP — only "light" or "admin" there. Other solid hours: 08–12 and 14–17.
- Match kind to time: "deep" → morning peak; "admin"/"meet" → late morning or afternoon; "light" → the dip or as filler; "personal" → evening or weekend.
- Respect deadlines: a task must land on or before its deadline day.
- Urgent tasks come first / earliest.
- Spread the load: aim for ≤ 3 focused tasks per day, and never put two tasks at the same hour on the same day.
- Never use a day marked PAST. Prefer earlier in the week.
- Avoid weekends (days 5 and 6) unless the task kind is "personal".
- Use integer hours from 8 to 18. day is an integer 0=Mon … 6=Sun.

Return ONLY JSON, no prose:
{"schedule":[{"id":"<taskId>","day":<0-6>,"hour":<8-18>,"why":"<≤6 words>"}]}
Include every task exactly once.`;

function buildUser(tasks: SchedTask[], ctx: WeekCtx): string {
  const days = ctx.days
    .map((d) => `  day ${d.index} = ${d.name} ${d.date}${d.isPast ? ' (PAST — do not use)' : ''} — ${d.existing} already scheduled`)
    .join('\n');
  const list = tasks
    .map(
      (t) =>
        `  - id=${t.id} | "${t.title}" | kind=${t.kind} | effort=${t.effortHrs}h${t.urgent ? ' | URGENT' : ''}${t.deadline ? ` | deadline=${t.deadline}` : ''}${t.project ? ` | project=${t.project}` : ''}`,
    )
    .join('\n');
  return `${ctx.weekLabel}. Today is day ${ctx.todayIndex}.\nDays:\n${days}\n\nBacklog to place (${tasks.length}):\n${list}`;
}

const clamp = (n: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, n));

/** Turn the model's (possibly messy) output into a valid, complete schedule. */
function reconcile(model: Assignment[], tasks: SchedTask[], ctx: WeekCtx): Assignment[] {
  const byId = new Map<string, Assignment>();
  for (const a of model) if (a && typeof a.id === 'string') byId.set(a.id, a);

  const firstOpen = ctx.days.find((d) => !d.isPast)?.index ?? 0;
  const occupied = new Set<string>(); // `${day}:${hour}`

  const place = (day: number, hour: number): { day: number; hour: number } => {
    let d = clamp(Math.round(day), 0, 6);
    let h = clamp(Math.round(hour), 8, 18);
    if (ctx.days[d]?.isPast) d = firstOpen;
    let guard = 0;
    while (occupied.has(`${d}:${h}`) && guard++ < 100) {
      h += 1;
      if (h > 18) {
        h = 8;
        d = (d + 1) % 7;
        if (ctx.days[d]?.isPast) d = firstOpen;
      }
    }
    occupied.add(`${d}:${h}`);
    return { day: d, hour: h };
  };

  const out: Assignment[] = [];
  tasks.forEach((t, idx) => {
    const m = byId.get(t.id);
    let day: number;
    let hour: number;
    let why: string | undefined;
    if (m && Number.isFinite(m.day) && Number.isFinite(m.hour)) {
      day = m.day;
      hour = m.hour;
      why = typeof m.why === 'string' ? m.why : undefined;
    } else {
      const slot = bestSlot(t.kind, (firstOpen + idx) % 5);
      day = slot.dayIndex;
      hour = slot.hour;
    }
    const p = place(day, hour);
    out.push({ id: t.id, day: p.day, hour: p.hour, why });
  });
  return out;
}

export async function scheduleBacklog(tasks: SchedTask[], ctx: WeekCtx): Promise<Assignment[]> {
  if (!tasks.length) return [];
  const fallback = { schedule: [] as Assignment[] };
  let raw: { schedule?: Assignment[] } = fallback;
  try {
    raw = await chatJSON<{ schedule?: Assignment[] }>(SYSTEM, buildUser(tasks, ctx), fallback);
  } catch {
    raw = fallback;
  }
  return reconcile(Array.isArray(raw.schedule) ? raw.schedule : [], tasks, ctx);
}
