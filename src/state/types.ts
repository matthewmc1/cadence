import type { Kind } from '../lib/energy';

export type View = 'today' | 'add' | 'insights' | 'plan' | 'board';

/* ----------------------------------------------------------------- Today */

export type TodayStatus = 'past' | 'now' | 'upcoming';

export interface TodayItem {
  id: string;
  title: string;
  kind: Kind;
  hour: number; // decimal start, e.g. 9 = 9:00
  end?: number; // decimal end
  timeLabel: string; // '11:15'
  status: TodayStatus;
  effortLabel?: string;
  place?: string;
  /** dot colour override; defaults to the kind's dot */
  dot?: string;
  /** the protected deep-focus hero card */
  hero?: boolean;
  /** faded, end-of-day reflective item */
  ghost?: boolean;
  done?: boolean;
  /** one-line rationale shown under the title */
  rationale?: string;
}

/* ------------------------------------------------------------------ Plan */

export interface PlacedTask {
  id: string;
  title: string;
  kind: Kind;
  hour: number; // decimal
  weekend?: boolean;
  recurring?: boolean;
  done?: boolean;
  assignees?: { initial: string; color: string }[];
}

export interface WeekDay {
  name: string;
  short: string;
  date: string;
  scale: number;
  weekend: boolean;
  isToday: boolean;
  load?: string;
  loadColor?: string;
  tasks: PlacedTask[];
}

export interface MonthDay {
  date: string; // yyyy-mm-dd
  dayNum: number;
  inMonth: boolean;
  isToday: boolean;
  weekend: boolean;
  tasks: PlacedTask[];
}

export interface MonthGrid {
  label: string;
  weeks: MonthDay[][]; // rows of 7
}

export interface BacklogTask {
  id: string;
  title: string;
  kind: Kind;
  tag: string; // 'Deep' | 'Admin' | 'Urgent' ...
  urgent?: boolean;
  effortHrs: number;
  bestFitNote?: string; // 'Best fit → Tuesday morning'
}

/* ----------------------------------------------------------------- Board */

export type BoardColumn = 'backlog' | 'week' | 'focus' | 'done';

export interface BoardTask {
  id: string;
  title: string;
  kind: Kind;
  column: BoardColumn;
  tagLabel: string; // 'Deep · 1.5h'
  when?: string; // 'Tue · 9:30'
  place?: string;
  until?: string; // focus column: 'until 11:00'
  doneDay?: string; // 'Mon'
  donePlace?: string;
}

export interface Project {
  id: string;
  name: string;
  subtitle: string;
  due: string;
  members: { initial: string; color: string }[];
  done: number;
  total: number;
  deepLeft: number;
  tasks: BoardTask[];
}

/* ------------------------------------------------------------------ Add */

export interface Draft {
  title: string;
}

/* ----------------------------------------------------------------- State */

export interface Now {
  dayLabel: string; // 'Thursday, 27 June'
  navLabel: string; // 'Home'
  time: string; // '9:41'
  hour: number; // decimal, 9.68
}

export interface AppState {
  view: View;
  now: Now;
  today: TodayItem[];
  weekLabel: string;
  week: WeekDay[];
  backlog: BacklogTask[];
  backlogTotal: number;
  selectedBacklogId: string | null;
  project: Project;
  draft: Draft;
  toast: string | null;
  /** transient: a task just placed/created, to pulse it into view */
  highlightTaskId: string | null;
}
