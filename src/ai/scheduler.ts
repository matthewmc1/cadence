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
import { bestSlot, type Kind, type EnergyProfile } from '../lib/energy';

export interface SchedTask {
  id: string;
  title: string;
  kind: Kind;
  effortHrs: number;
  urgent: boolean;
  important: boolean;
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
  /** the user's learned energy windows — where deep work and the dip fall */
  energy: { peakStart: number; peakEnd: number; dipHour: number; learned: boolean };
}

export interface Assignment {
  id: string;
  day: number; // 0..6
  hour: number; // 8..18
  why?: string;
}

const SYSTEM = `You are Cadence's scheduling assistant. You place a user's backlog tasks into a Mon–Sun week, arranged around human energy.

Rules:
- The user's PEAK (deep-focus) and DIP (low-energy) windows are given below — they are learned from when this person actually does their best work, so honour them over any generic assumption. Schedule "deep" work inside the PEAK; place only "light"/"admin" at the DIP. Other solid hours sit around them.
- Match kind to time: "deep" → the PEAK; "admin"/"meet" → the hours around the DIP; "light" → the DIP or as filler; "personal" → evening or weekend.
- Respect deadlines: a task must land on or before its deadline day.
- Priority: IMPORTANT work (deep, long-term) matters more than merely URGENT work. Give IMPORTANT and "deep" tasks the PEAK hours first; never let an urgent-but-shallow task take a PEAK hour that important/deep work needs. Order roughly: important+urgent, then important, then urgent, then the rest.
- Keep the PEAK hours for "deep"/important tasks only — place "admin"/"light"/"meet" outside the PEAK.
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
        `  - id=${t.id} | "${t.title}" | kind=${t.kind} | effort=${t.effortHrs}h${t.important ? ' | IMPORTANT' : ''}${t.urgent ? ' | URGENT' : ''}${t.deadline ? ` | deadline=${t.deadline}` : ''}${t.project ? ` | project=${t.project}` : ''}`,
    )
    .join('\n');
  const e = ctx.energy;
  const energyLine = `Energy — PEAK ${e.peakStart}:00–${e.peakEnd}:00 (deep-focus), DIP ${e.dipHour}:00 (admin/light)${e.learned ? ', learned from this user’s completed work' : ' (default rhythm — not enough history yet)'}.`;
  return `${ctx.weekLabel}. Today is day ${ctx.todayIndex}.\n${energyLine}\nDays:\n${days}\n\nBacklog to place (${tasks.length}):\n${list}`;
}

const clamp = (n: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, n));

/** Turn the model's (possibly messy) output into a valid, complete schedule. */
function reconcile(model: Assignment[], tasks: SchedTask[], ctx: WeekCtx): Assignment[] {
  const byId = new Map<string, Assignment>();
  for (const a of model) if (a && typeof a.id === 'string') byId.set(a.id, a);

  const firstOpen = ctx.days.find((d) => !d.isPast)?.index ?? 0;
  const occupied = new Set<string>(); // `${day}:${hour}`
  // fall back to the same learned profile the LLM was given
  const profile: EnergyProfile = {
    peakStart: ctx.energy.peakStart,
    peakEnd: ctx.energy.peakEnd,
    dipHour: ctx.energy.dipHour,
    samples: 0,
    learned: ctx.energy.learned,
  };

  // The defended deep lane: weekday PEAK hours are reserved for deep / important
  // work, so an urgent-but-shallow task can't evict the long-term work Cadence
  // exists to protect. `protectedTask` may sit in the peak; nothing else may.
  const peakHours = new Set<number>();
  for (let h = Math.round(ctx.energy.peakStart); h < Math.round(ctx.energy.peakEnd); h++) peakHours.add(h);
  const protectedTask = (t: SchedTask) => t.kind === 'deep' || t.important;

  const place = (day: number, hour: number, isProtected: boolean): { day: number; hour: number } => {
    let d = clamp(Math.round(day), 0, 6);
    let h = clamp(Math.round(hour), 8, 18);
    if (ctx.days[d]?.isPast) d = firstOpen;
    const blocked = (dd: number, hh: number) =>
      occupied.has(`${dd}:${hh}`) || (!isProtected && dd < 5 && peakHours.has(hh));
    let guard = 0;
    while (blocked(d, h) && guard++ < 200) {
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

  // place deep / important tasks first so they claim the peak before filler
  const order = tasks
    .map((t, idx) => ({ t, idx }))
    .sort((a, b) => Number(protectedTask(b.t)) - Number(protectedTask(a.t)));

  const out: Assignment[] = [];
  for (const { t, idx } of order) {
    const m = byId.get(t.id);
    let day: number;
    let hour: number;
    let why: string | undefined;
    if (m && Number.isFinite(m.day) && Number.isFinite(m.hour)) {
      day = m.day;
      hour = m.hour;
      why = typeof m.why === 'string' ? m.why : undefined;
    } else {
      const slot = bestSlot(t.kind, (firstOpen + idx) % 5, profile);
      day = slot.dayIndex;
      hour = slot.hour;
    }
    const p = place(day, hour, protectedTask(t));
    out.push({ id: t.id, day: p.day, hour: p.hour, why });
  }
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
