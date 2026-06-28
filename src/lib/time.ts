/** Real-time helpers — drive the clock, Today, and the planning week off the
 *  actual current date rather than fixed demo values. */

export interface NowInfo {
  dayLabel: string; // "Thursday, 27 June"
  navLabel: string;
  time: string; // "9:41"
  hour: number; // decimal, e.g. 9.68
  weekday: number; // Mon=0 .. Sun=6
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const MONTHS_LONG = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];

export function computeNow(d: Date = new Date()): NowInfo {
  const h = d.getHours();
  const m = d.getMinutes();
  return {
    dayLabel: `${WEEKDAYS[d.getDay()]}, ${d.getDate()} ${MONTHS_LONG[d.getMonth()]}`,
    navLabel: 'Home',
    time: `${h}:${m.toString().padStart(2, '0')}`,
    hour: h + m / 60,
    weekday: (d.getDay() + 6) % 7,
  };
}

/** The current Mon–Sun week: dates (day-of-month) and a label. */
export function computeWeek(d: Date = new Date()): { label: string; dates: string[] } {
  const monday = new Date(d);
  monday.setDate(d.getDate() - ((d.getDay() + 6) % 7));
  monday.setHours(0, 0, 0, 0);

  const dates: string[] = [];
  let sunday = monday;
  for (let i = 0; i < 7; i++) {
    const day = new Date(monday);
    day.setDate(monday.getDate() + i);
    dates.push(String(day.getDate()));
    if (i === 6) sunday = day;
  }

  const sameMonth = monday.getMonth() === sunday.getMonth();
  const label = sameMonth
    ? `Week of ${monday.getDate()} – ${sunday.getDate()} ${MONTHS[monday.getMonth()]}`
    : `Week of ${monday.getDate()} ${MONTHS[monday.getMonth()]} – ${sunday.getDate()} ${MONTHS[sunday.getMonth()]}`;
  return { label, dates };
}

/* ----------------------------------------------------------- date utilities */

export function addDays(d: Date, n: number): Date {
  const x = new Date(d);
  x.setDate(d.getDate() + n);
  return x;
}

/** Monday 00:00 of d's week (local). */
export function startOfWeek(d: Date): Date {
  const m = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  m.setDate(m.getDate() - ((m.getDay() + 6) % 7));
  return m;
}

export function startOfMonth(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), 1);
}

export function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

export function ymd(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

/** Decimal local hour of a datetime (9.5 = 9:30). */
export function hourOf(d: Date): number {
  return d.getHours() + d.getMinutes() / 60;
}

/** Build an ISO string from a yyyy-mm-dd date and a decimal local hour. */
export function combine(dateStr: string, hour: number): string {
  const [y, m, day] = dateStr.split('-').map(Number);
  const h = Math.floor(hour);
  const min = Math.round((hour - h) * 60);
  return new Date(y, m - 1, day, h, min).toISOString();
}

export function monthLabel(d: Date): string {
  return `${MONTHS_LONG[d.getMonth()]} ${d.getFullYear()}`;
}

/**
 * Occurrences of a scheduled (possibly recurring) task within [start, end).
 * Non-recurring → at most one. Recurrence is expanded relative to the anchor's
 * weekday / day-of-month, never before the anchor date.
 */
export function occurrences(iso: string, recurrence: string, start: Date, end: Date): Date[] {
  const anchor = new Date(iso);
  const anchorDay = new Date(anchor.getFullYear(), anchor.getMonth(), anchor.getDate());
  const ah = anchor.getHours();
  const am = anchor.getMinutes();
  const out: Date[] = [];

  if (!recurrence || recurrence === 'none') {
    if (anchor >= start && anchor < end) out.push(anchor);
    return out;
  }

  // step day-by-day across the (small, ≤42-day) range
  let d = new Date(Math.max(start.getTime(), anchorDay.getTime()));
  d = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  let guard = 0;
  while (d < end && guard++ < 400) {
    const wd = d.getDay(); // 0 Sun .. 6 Sat
    let hit = false;
    switch (recurrence) {
      case 'daily':
        hit = true;
        break;
      case 'weekdays':
        hit = wd >= 1 && wd <= 5;
        break;
      case 'weekly':
        hit = wd === anchorDay.getDay();
        break;
      case 'monthly':
        hit = d.getDate() === anchorDay.getDate();
        break;
    }
    if (hit) out.push(new Date(d.getFullYear(), d.getMonth(), d.getDate(), ah, am));
    d = addDays(d, 1);
  }
  return out;
}
