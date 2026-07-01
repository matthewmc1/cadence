/**
 * Cadence's energy model.
 *
 * Everything Cadence "knows" about how your day flows lives here: the hourly
 * energy curve, the protected peak window, the post-lunch dip, weekday
 * factors, and the heuristics that infer a task's shape and find its best
 * slot. The numbers are lifted from the design canvas's `dayCurve`/heatmap
 * data so the product behaves consistently with the mockups.
 */

/* ---------------------------------------------------------------- task kinds */

export type Kind = 'deep' | 'light' | 'admin' | 'meet' | 'personal';

export interface KindMeta {
  key: Kind;
  label: string;
  /** dot / accent colour used in lists */
  dot: string;
  /** chip text + background (the pill on cards) */
  chipFg: string;
  chipBg: string;
  /** soft block fill + border used on the week grid */
  blockBg: string;
  blockBd: string;
}

export const KINDS: Record<Kind, KindMeta> = {
  deep: {
    key: 'deep',
    label: 'Deep focus',
    dot: '#C2743D',
    chipFg: '#A85F2C',
    chipBg: '#F2E4D4',
    blockBg: '#F4E7D8',
    blockBd: '#E8D3BC',
  },
  light: {
    key: 'light',
    label: 'Light focus',
    dot: '#B9A98F',
    chipFg: '#5F584C',
    chipBg: '#EFEBE0',
    blockBg: '#F1ECE1',
    blockBd: '#E5DDCC',
  },
  admin: {
    key: 'admin',
    label: 'Admin',
    dot: '#7E93A6',
    chipFg: '#41566A',
    chipBg: '#E8EDF1',
    blockBg: '#E8EDF1',
    blockBd: '#D8E0E7',
  },
  meet: {
    key: 'meet',
    label: 'Conversation',
    dot: '#7E93A6',
    chipFg: '#41566A',
    chipBg: '#E8EDF1',
    blockBg: '#E8EDF1',
    blockBd: '#D8E0E7',
  },
  personal: {
    key: 'personal',
    label: 'Personal',
    dot: '#94A08A',
    chipFg: '#4E5E45',
    chipBg: '#E9EDE5',
    blockBg: '#E9EDE5',
    blockBd: '#D9E0D2',
  },
};

/* ------------------------------------------------------------- hourly energy */

/**
 * Normalised energy (0–1) by hour of day. This is the shape of the curve you
 * see on Today and behind every column on Plan: a strong late-morning peak,
 * a deep post-lunch trough, and a smaller afternoon rebound.
 */
export const HOUR_ENERGY: Record<number, number> = {
  7: 0.12,
  8: 0.45,
  9: 0.92,
  10: 0.96,
  11: 0.74,
  12: 0.34,
  13: 0.22,
  14: 0.5,
  15: 0.7,
  16: 0.58,
  17: 0.42,
  18: 0.28,
  19: 0.18,
  20: 0.14,
};

/** The day's named windows (the canonical model; personalised per user below). */
export const PEAK = { start: 9, end: 11, label: '9:00 – 11:00', share: 68 } as const;
export const DIP = { start: 13, end: 14, label: '1:00 – 2:00' } as const;

/**
 * A personal energy profile — where *this* user's focus peak and low-energy dip
 * actually fall, learned from their completion history (see
 * `deriveEnergyProfile` in state/selectors). Until there's enough signal it
 * stays on the canonical defaults, so scheduling is unchanged for a new account.
 */
export interface EnergyProfile {
  peakStart: number; // first hour of the focus peak (e.g. 9)
  peakEnd: number; // exclusive end of the peak (e.g. 11)
  dipHour: number; // the low-energy hour best spent on admin/light work
  samples: number; // completed tasks this was learned from
  learned: boolean; // false → still on the canonical defaults
}

export const DEFAULT_PROFILE: EnergyProfile = {
  peakStart: PEAK.start,
  peakEnd: PEAK.end,
  dipHour: DIP.start,
  samples: 0,
  learned: false,
};

const peakCenter = (p: EnergyProfile) => (p.peakStart + p.peakEnd) / 2;
const CANON_PEAK_CENTER = (PEAK.start + PEAK.end) / 2;

/**
 * Continuous energy at any (possibly fractional) hour, linearly interpolated.
 * When a learned `profile` is supplied, the canonical curve is shifted so its
 * peak sits on the user's measured peak.
 */
export function energyAt(hour: number, profile: EnergyProfile = DEFAULT_PROFILE): number {
  const h = hour - (peakCenter(profile) - CANON_PEAK_CENTER);
  const lo = Math.floor(h);
  const hi = Math.ceil(h);
  if (lo === hi) return HOUR_ENERGY[lo] ?? 0;
  const a = HOUR_ENERGY[lo] ?? 0;
  const b = HOUR_ENERGY[hi] ?? 0;
  return a + (b - a) * (h - lo);
}

/** Map an hour (7–19) to a y-offset within a column of `height` px. */
export function hourToY(hour: number, height = 520): number {
  return ((hour - 7) / 12) * height;
}

export type Band = 'peak' | 'good' | 'dip' | 'low';

export function bandAt(hour: number, profile: EnergyProfile = DEFAULT_PROFILE): Band {
  if (hour >= profile.peakStart && hour < profile.peakEnd) return 'peak';
  if (hour >= profile.dipHour && hour < profile.dipHour + 1) return 'dip';
  return energyAt(hour, profile) >= 0.55 ? 'good' : 'low';
}

/* -------------------------------------------------------------- weekday model */

export interface DayModel {
  name: string;
  short: string;
  /** overall energy scale for the day's curve */
  scale: number;
  weekend: boolean;
  /** optional load badge shown on Plan */
  load?: string;
  loadColor?: string;
}

/** Mon-anchored week. Thursdays ship; Fridays stay light; weekends ease off. */
export const WEEK_MODEL: DayModel[] = [
  { name: 'Monday', short: 'Mon', scale: 0.9, weekend: false, load: 'Admin', loadColor: '#7E93A6' },
  { name: 'Tuesday', short: 'Tue', scale: 1.0, weekend: false },
  { name: 'Wednesday', short: 'Wed', scale: 0.95, weekend: false },
  { name: 'Thursday', short: 'Thu', scale: 1.0, weekend: false, load: 'Ship', loadColor: '#C2743D' },
  { name: 'Friday', short: 'Fri', scale: 0.8, weekend: false, load: 'Light', loadColor: '#94A08A' },
  { name: 'Saturday', short: 'Sat', scale: 1.0, weekend: true },
  { name: 'Sunday', short: 'Sun', scale: 1.0, weekend: true },
];

/** Per-weekday completion factor used by the insights heatmap (Mon→Sun). */
export const DAY_FACTOR = [0.72, 1, 0.94, 1, 0.8, 0.24, 0.18];

/* --------------------------------------------------------- where work happens */

export const LOCATIONS = [
  { key: 'home', label: 'Home', pct: 62, color: '#C2743D' },
  { key: 'office', label: 'Office', pct: 28, color: '#7E93A6' },
  { key: 'cafe', label: 'Café', pct: 10, color: '#94A08A' },
] as const;

export type Place = (typeof LOCATIONS)[number]['label'];

/* ---------------------------------------------------------------- inference */

const DEEP_HINTS = ['draft', 'write', 'design', 'build', 'refactor', 'spec', 'architect', 'plan ', 'strategy', 'deck', 'outline', 'prototype', 'analy', 'research', 'model'];
const ADMIN_HINTS = ['email', 'invoice', 'expense', 'schedule', 'book', 'submit', 'file', 'review contract', 'contract', 'legal', 'pull ', 'metrics', 'update', 'triage', 'inbox'];
const MEET_HINTS = ['1:1', 'meet', 'call', 'sync', 'standup', 'interview', 'review with', 'demo', 'chat', 'catch up'];
const PERSONAL_HINTS = ['run', 'gym', 'lunch', 'walk', 'doctor', 'family', 'birthday', 'groceries'];
const LIGHT_HINTS = ['reply', 'read', 'collect', 'gather', 'screenshot', 'snapshot', 'polish', 'tidy', 'organize', 'organise', 'book '];

export interface Inference {
  kind: Kind;
  effortHrs: number;
  effortLabel: string;
  needs: string;
  bestAt: string;
  place: Place;
}

/** Heuristically read a task's shape from its title — the "Cadence sees" step. */
export function inferTask(title: string): Inference {
  const t = title.toLowerCase();
  const has = (hints: string[]) => hints.some((h) => t.includes(h));

  let kind: Kind = 'light';
  if (has(MEET_HINTS)) kind = 'meet';
  else if (has(DEEP_HINTS)) kind = 'deep';
  else if (has(ADMIN_HINTS)) kind = 'admin';
  else if (has(PERSONAL_HINTS)) kind = 'personal';
  else if (has(LIGHT_HINTS)) kind = 'light';

  const effortHrs =
    kind === 'deep' ? 2 : kind === 'meet' ? 0.5 : kind === 'admin' ? 0.5 : kind === 'personal' ? 1 : 0.67;

  const effortLabel =
    effortHrs >= 1 ? `≈ ${effortHrs % 1 === 0 ? effortHrs : effortHrs.toFixed(1)} hrs` : `≈ ${Math.round(effortHrs * 60)} min`;

  const needs =
    kind === 'deep' ? 'A quiet desk' : kind === 'meet' ? 'A room & the right people' : kind === 'admin' ? 'A few clear minutes' : 'No special setup';

  const bestAt =
    kind === 'deep' ? 'Morning' : kind === 'meet' ? 'Early afternoon' : kind === 'admin' ? 'The afternoon dip' : 'Anytime light';

  const place: Place = kind === 'deep' ? 'Home' : kind === 'meet' ? 'Office' : 'Home';

  return { kind, effortHrs, effortLabel, needs, bestAt, place };
}

/* ---------------------------------------------------------------- best slot */

export interface Slot {
  /** index into WEEK_MODEL */
  dayIndex: number;
  dayName: string;
  /** decimal hour, e.g. 9.5 = 9:30 */
  hour: number;
  label: string;
  reason: string;
  place: Place;
}

const HHMM = (hour: number): string => {
  const h24 = Math.floor(hour);
  const m = Math.round((hour - h24) * 60);
  const ampm = h24 >= 12 ? 'PM' : 'AM';
  const h12 = ((h24 + 11) % 12) + 1;
  return `${h12}:${m.toString().padStart(2, '0')} ${ampm}`;
};

export const fmtTime = HHMM;

/**
 * Score how well an hour fits a kind of work. Deep work wants the peak;
 * admin is happiest in the dip; meetings sit in the early afternoon;
 * light work fills whatever's left.
 */
function fit(kind: Kind, hour: number, profile: EnergyProfile = DEFAULT_PROFILE): number {
  const e = energyAt(hour, profile);
  const deepTarget = profile.peakStart + 0.5; // settle just inside the peak
  const dipCenter = profile.dipHour + 0.5;
  switch (kind) {
    case 'deep':
      // ride high energy, but settle into the heart of the peak
      return e - Math.abs(hour - deepTarget) * 0.05;
    case 'admin':
      return 1 - Math.abs(hour - dipCenter) / 6; // gravitate to the dip
    case 'meet':
      return 1 - Math.abs(hour - profile.dipHour) / 6; // early afternoon
    case 'personal':
      return hour >= 12 ? 0.6 : 0.4;
    case 'light':
    default:
      return 0.5 + e * 0.3;
  }
}

/**
 * Find the best upcoming slot for a task. Searches the working week's mornings
 * and afternoons, preferring earlier days so deadlines have slack.
 */
export function bestSlot(kind: Kind, fromDayIndex = 0, profile: EnergyProfile = DEFAULT_PROFILE): Slot {
  const candidateHours = [8.5, 9, 9.5, 10, 11, 11.5, 13, 14, 15, 15.5, 16];
  let best: Slot | null = null;
  let bestScore = -Infinity;

  for (let d = fromDayIndex; d < 5; d++) {
    const day = WEEK_MODEL[d];
    for (const hour of candidateHours) {
      // weekday scale + fit, with a gentle preference for sooner days
      const score = fit(kind, hour, profile) * day.scale - d * 0.015;
      if (score > bestScore) {
        bestScore = score;
        best = {
          dayIndex: d,
          dayName: day.name,
          hour,
          label: `${day.name} · ${HHMM(hour)}`,
          reason:
            kind === 'deep'
              ? 'Your strongest focus window'
              : kind === 'admin'
                ? 'Slotted into your afternoon dip'
                : kind === 'meet'
                  ? 'Early afternoon, when you talk best'
                  : 'A light, low-stakes pocket',
          place: kind === 'deep' ? 'Home' : kind === 'meet' ? 'Office' : 'Home',
        };
      }
    }
  }
  // bestSlot always finds something across a 5-day search.
  return best as Slot;
}
