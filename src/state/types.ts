import type { Kind } from '../lib/energy';

/** The four tabs (Inbox · Work · Projects · Insights) plus the capture surface. Today and the "when" live inside Work; Projects is the PARA page, and each project's Board is a mode of it. */
export type View = 'inbox' | 'work' | 'projects' | 'insights' | 'add';

/**
 * A view collapsed out of the nav but not out of the codebase (Plan). Nothing
 * routes here — `go` redirects it to Work — so an older link or a call left in
 * a surface we don't own still lands somewhere real.
 */
export type ParkedView = 'plan';

/** How a project is looked at on the Projects tab: its actions list, or its kanban. (Named from when Board was a mode of Work.) */
export type WorkMode = 'list' | 'board';

/* ----------------------------------------------------------------- Inbox */

/** Bundled (grouped by project → person → kind) or flat chronological — one key apart. */
export type InboxMode = 'bundled' | 'flat';

/** How a bundle was formed, so the view can style the header. */
export type InboxBundleKind = 'project' | 'person' | 'kind';

export interface InboxBundle<S = unknown> {
  key: string; // 'project:<id>' | 'person:<name|email>' | 'kind:<signalKind>'
  kind: InboxBundleKind;
  label: string; // project name / participant name / "Emails"
  sublabel?: string; // the project's client, when it has one
  color?: string; // project or client colour
  items: S[]; // most recent first
}

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
  effortMinutes: number; // drives the block's height on the week grid
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
  tag: string; // 'Deep' | 'Admin' | 'Urgent' | 'Important' ...
  urgent?: boolean;
  important?: boolean;
  effortHrs: number;
  bestFitNote?: string; // 'Best fit → Tuesday morning'
  projectId: string | null; // for inline reassignment from the rail
  project?: { name: string; color: string }; // backlog spans every project
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
  clientId: string | null;
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
