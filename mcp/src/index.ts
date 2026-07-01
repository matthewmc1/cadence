#!/usr/bin/env node
/**
 * Cadence MCP server.
 *
 * Exposes a Cadence workspace to an AI assistant so it can help you focus on
 * what needs doing and see how each task ladders up to its project and outcome.
 * Auth is a Cadence personal access token (CADENCE_API_TOKEN), Bearer.
 */
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { z } from 'zod';
import { api, CadenceError, type Bootstrap, type Kind, type Project, type Recurrence, type Status, type Task } from './api.js';
import {
  addDays, startOfDay, startOfWeek, sameDay, fmtDate, fmtTime,
  STATUS_LABEL, energyOf, isOverdue, dueWithin,
  taskLine, inRange,
} from './logic.js';

const KINDS = ['deep', 'light', 'admin', 'meet', 'personal'] as const;
const RECURS = ['none', 'daily', 'weekdays', 'weekly', 'monthly'] as const;

/* ----------------------------------------------------------------- utils */

function text(t: string) {
  return { content: [{ type: 'text' as const, text: t }] };
}
function fail(e: unknown) {
  const msg = e instanceof CadenceError ? e.message : e instanceof Error ? e.message : String(e);
  return { content: [{ type: 'text' as const, text: `⚠️ ${msg}` }], isError: true };
}

/** Resolve a project by id or (case-insensitive) name. */
function resolveProject(ref: string | undefined, projects: Project[]): Project | undefined {
  if (!ref) return undefined;
  const byId = projects.find((p) => p.id === ref);
  if (byId) return byId;
  const lower = ref.toLowerCase();
  return projects.find((p) => p.name.toLowerCase() === lower) ?? projects.find((p) => p.name.toLowerCase().includes(lower));
}

/** Build an ISO datetime from a `when` (ISO or yyyy-mm-dd) and an optional hour. */
function parseWhen(when: string | undefined, hour: number | undefined): string | null {
  if (!when) return null;
  if (when.includes('T')) return new Date(when).toISOString();
  const [y, m, d] = when.split('-').map(Number);
  if (!y || !m || !d) throw new CadenceError(`Could not parse date "${when}" — use ISO or yyyy-mm-dd.`, 0);
  const h = hour ?? 9;
  const hh = Math.floor(h);
  const min = Math.round((h - hh) * 60);
  return new Date(y, m - 1, d, hh, min).toISOString();
}

function projectStats(p: Project, tasks: Task[]) {
  const items = tasks.filter((t) => t.projectId === p.id);
  const done = items.filter((t) => t.status === 'done').length;
  const open = items.filter((t) => t.status !== 'done');
  const deepLeft = open.filter((t) => t.kind === 'deep').length;
  const overdue = open.filter((t) => t.deadline && new Date(t.deadline) < new Date()).length;
  return { items, total: items.length, done, open, deepLeft, overdue };
}

/* --------------------------------------------------------------- renders */

function renderFocus(boot: Bootstrap): string {
  const { tasks, projects, user } = boot;
  const now = new Date();
  const dayStart = startOfDay(now);
  const dayEnd = addDays(dayStart, 1);

  const today = inRange(tasks, dayStart, dayEnd).filter((d) => d.task.status !== 'done');
  const overdue = tasks.filter((t) => isOverdue(t, now)).sort((a, b) => +new Date(a.deadline!) - +new Date(b.deadline!));
  const inFocus = tasks.filter((t) => t.status === 'focus');
  const dueSoon = tasks.filter((t) => dueWithin(t, now, 3) && !sameDay(new Date(t.deadline!), now));
  const urgent = tasks.filter((t) => t.urgent && t.status === 'backlog');

  const energy = energyOf(now.getHours());
  const out: string[] = [];
  out.push(`# Focus for ${user.name.split(' ')[0]} — ${fmtDate(now)}, ${fmtTime(now)}`);
  out.push(`Energy window right now: **${energy.toUpperCase()}**${energy === 'peak' ? ' — protect this for deep work.' : energy === 'dip' ? ' — good for light/admin.' : ''}`);

  if (overdue.length) {
    out.push(`\n## 🔴 Overdue (${overdue.length})`);
    overdue.forEach((t) => out.push(taskLine(t, projects)));
  }
  if (inFocus.length) {
    out.push(`\n## ▶️ In focus now`);
    inFocus.forEach((t) => out.push(taskLine(t, projects)));
  }

  out.push(`\n## 📅 Today (${today.length})`);
  if (!today.length) out.push('_Nothing scheduled today._');
  today.forEach(({ task, at }, i) => {
    const e = energyOf(at.getHours());
    const note = task.kind === 'deep' && e === 'peak' ? '  ← deep work in your **peak** window' : e === 'dip' && task.kind !== 'deep' ? '  ← fits the afternoon dip' : '';
    out.push(`${i + 1}. \`${fmtTime(at)}\` ${taskLine(task, projects).replace(/^- /, '')}${note}`);
  });

  if (dueSoon.length) {
    out.push(`\n## ⏰ Due soon (next 3 days)`);
    dueSoon.forEach((t) => out.push(taskLine(t, projects)));
  }
  if (urgent.length) {
    out.push(`\n## ⚡ Urgent in backlog`);
    urgent.forEach((t) => out.push(taskLine(t, projects)));
  }

  // a concrete suggestion
  const pick =
    overdue[0] ??
    inFocus[0] ??
    today.find((d) => d.task.kind === 'deep' && energyOf(d.at.getHours()) === 'peak')?.task ??
    today[0]?.task ??
    urgent[0];
  if (pick) {
    out.push(`\n---\n**Suggested next:** ${pick.title}${overdue.includes(pick) ? ' (overdue)' : pick.kind === 'deep' && energy === 'peak' ? " — you're in your peak window for deep work" : ''}.`);
  }
  return out.join('\n');
}

function renderProjects(boot: Bootstrap): string {
  const { projects, tasks } = boot;
  if (!projects.length) return 'No projects yet. Create one to group tasks toward an outcome.';
  const out = ['# Projects & outcomes\n'];
  for (const p of projects) {
    const s = projectStats(p, tasks);
    const pct = s.total ? Math.round((s.done / s.total) * 100) : 0;
    out.push(`## ${p.name}  \`#${p.id.slice(0, 8)}\``);
    if (p.subtitle) out.push(`*Outcome:* ${p.subtitle}`);
    out.push(`*Progress:* ${s.done}/${s.total} done (${pct}%)${p.due ? ` · due ${p.due}` : ''}${s.deepLeft ? ` · ${s.deepLeft} deep-work tasks left` : ''}${s.overdue ? ` · 🔴 ${s.overdue} overdue` : ''}\n`);
  }
  return out.join('\n');
}

function renderProjectStatus(p: Project, boot: Bootstrap): string {
  const s = projectStats(p, boot.tasks);
  const pct = s.total ? Math.round((s.done / s.total) * 100) : 0;
  const out = [`# ${p.name}`];
  if (p.subtitle) out.push(`**Outcome:** ${p.subtitle}`);
  out.push(`**Progress:** ${s.done}/${s.total} done (${pct}%)${p.due ? ` · target ${p.due}` : ''}`);
  const byStatus: Record<Status, Task[]> = { backlog: [], scheduled: [], focus: [], done: [] };
  for (const t of s.items) byStatus[t.status].push(t);
  const section = (label: string, list: Task[]) => {
    if (!list.length) return;
    out.push(`\n## ${label} (${list.length})`);
    list.forEach((t) => out.push(taskLine(t, boot.projects)));
  };
  out.push(`\n_What's left to reach the outcome:_`);
  section('In focus', byStatus.focus);
  section('Scheduled', byStatus.scheduled);
  section('Backlog', byStatus.backlog);
  section('Done', byStatus.done);
  return out.join('\n');
}

/* ----------------------------------------------------------------- server */

const server = new McpServer({ name: 'cadence', version: '1.0.0' });

// ---- whats_next: the focus tool ----
server.registerTool(
  'whats_next',
  {
    title: 'What should I focus on?',
    description:
      "The focus view: overdue work, what's in focus now, today's schedule (with energy-window notes), what's due soon, and urgent backlog — with a concrete suggestion for what to do next.",
    inputSchema: {},
  },
  async () => {
    try {
      return text(renderFocus(await api.bootstrap()));
    } catch (e) {
      return fail(e);
    }
  },
);

// ---- list_tasks ----
server.registerTool(
  'list_tasks',
  {
    title: 'List tasks',
    description: 'List tasks by scope and optional project/status. Recurrence is expanded for the today/week scopes.',
    inputSchema: {
      scope: z.enum(['today', 'week', 'overdue', 'unscheduled', 'backlog', 'done', 'all']).default('today').describe('Which tasks to show'),
      project: z.string().optional().describe('Filter to a project by name or id'),
      status: z.enum(['backlog', 'scheduled', 'focus', 'done']).optional(),
    },
  },
  async ({ scope, project, status }) => {
    try {
      const boot = await api.bootstrap();
      const proj = resolveProject(project, boot.projects);
      let tasks = boot.tasks;
      if (proj) tasks = tasks.filter((t) => t.projectId === proj.id);
      if (status) tasks = tasks.filter((t) => t.status === status);
      const now = new Date();
      let lines: string[] = [];

      if (scope === 'today' || scope === 'week') {
        const start = scope === 'today' ? startOfDay(now) : startOfWeek(now);
        const end = scope === 'today' ? addDays(start, 1) : addDays(start, 7);
        const dated = inRange(tasks, start, end);
        lines = dated.map(({ task, at }) => taskLine(task, boot.projects, at));
      } else {
        let filtered = tasks;
        if (scope === 'overdue') filtered = tasks.filter((t) => isOverdue(t, now));
        else if (scope === 'unscheduled') filtered = tasks.filter((t) => !t.scheduledAt && t.status !== 'done');
        else if (scope === 'backlog') filtered = tasks.filter((t) => t.status === 'backlog');
        else if (scope === 'done') filtered = tasks.filter((t) => t.status === 'done');
        lines = filtered.sort((a, b) => a.title.localeCompare(b.title)).map((t) => taskLine(t, boot.projects));
      }
      const header = `**${scope}**${proj ? ` · ${proj.name}` : ''}${status ? ` · ${STATUS_LABEL[status]}` : ''} — ${lines.length} task(s)`;
      return text(lines.length ? `${header}\n\n${lines.join('\n')}` : `${header}\n\n_None._`);
    } catch (e) {
      return fail(e);
    }
  },
);

// ---- create_task ----
server.registerTool(
  'create_task',
  {
    title: 'Create a task',
    description:
      'Create a task. Optionally schedule it (when = ISO datetime or yyyy-mm-dd, with hour), make it repeat, set a deadline, assign a kind, and link it to a project (by name or id).',
    inputSchema: {
      title: z.string().describe('What needs doing'),
      kind: z.enum(KINDS).optional().describe('deep | light | admin | meet | personal (inferred if omitted)'),
      project: z.string().optional().describe('Project name or id to link to'),
      when: z.string().optional().describe('Schedule date: ISO datetime, or yyyy-mm-dd'),
      hour: z.number().optional().describe('Hour of day (decimal, e.g. 9.5) when `when` is a date'),
      recurrence: z.enum(RECURS).optional().describe('Repeat cadence'),
      deadline: z.string().optional().describe('Hard deadline (ISO date)'),
      effortMinutes: z.number().optional(),
      urgent: z.boolean().optional(),
      important: z.boolean().optional().describe('Important (Eisenhower): protects deep, long-term work — not the same as urgent'),
      note: z.string().optional(),
    },
  },
  async ({ title, kind, project, when, hour, recurrence, deadline, effortMinutes, urgent, important, note }) => {
    try {
      const boot = await api.bootstrap();
      const proj = resolveProject(project, boot.projects);
      const scheduledAt = parseWhen(when, hour);
      const task = await api.createTask({
        title,
        kind: kind as Kind | undefined,
        projectId: proj?.id ?? null,
        scheduledAt,
        status: scheduledAt ? 'scheduled' : 'backlog',
        recurrence: recurrence as Recurrence | undefined,
        deadline: deadline ? new Date(deadline).toISOString() : undefined,
        effortMinutes,
        urgent,
        important,
        note,
      });
      return text(`✅ Created:\n${taskLine(task, proj ? [proj] : boot.projects)}`);
    } catch (e) {
      return fail(e);
    }
  },
);

// ---- update_task ----
server.registerTool(
  'update_task',
  {
    title: 'Update a task',
    description: 'Update fields on a task by id (the `#xxxxxxxx` handle or full id). Only provided fields change.',
    inputSchema: {
      id: z.string().describe('Task id (full or the #short handle)'),
      title: z.string().optional(),
      kind: z.enum(KINDS).optional(),
      status: z.enum(['backlog', 'scheduled', 'focus', 'done']).optional(),
      project: z.string().optional().describe('Move to project (name/id), or "none" to unlink'),
      when: z.string().optional().describe('Reschedule: ISO datetime or yyyy-mm-dd (or "none" to unschedule)'),
      hour: z.number().optional(),
      recurrence: z.enum(RECURS).optional(),
      deadline: z.string().optional().describe('ISO date, or "none" to clear'),
      urgent: z.boolean().optional(),
      note: z.string().optional(),
    },
  },
  async (args) => {
    try {
      const boot = await api.bootstrap();
      const id = resolveTaskId(args.id, boot.tasks);
      const patch: Record<string, unknown> = {};
      if (args.title !== undefined) patch.title = args.title;
      if (args.kind !== undefined) patch.kind = args.kind;
      if (args.status !== undefined) patch.status = args.status;
      if (args.recurrence !== undefined) patch.recurrence = args.recurrence;
      if (args.urgent !== undefined) patch.urgent = args.urgent;
      if (args.note !== undefined) patch.note = args.note;
      if (args.project !== undefined) patch.projectId = args.project.toLowerCase() === 'none' ? null : resolveProject(args.project, boot.projects)?.id ?? null;
      if (args.when !== undefined) patch.scheduledAt = args.when.toLowerCase() === 'none' ? null : parseWhen(args.when, args.hour);
      if (args.deadline !== undefined) patch.deadline = args.deadline.toLowerCase() === 'none' ? null : new Date(args.deadline).toISOString();
      const task = await api.updateTask(id, patch);
      return text(`✅ Updated:\n${taskLine(task, boot.projects)}`);
    } catch (e) {
      return fail(e);
    }
  },
);

// ---- complete_task ----
server.registerTool(
  'complete_task',
  {
    title: 'Complete a task',
    description: 'Mark a task done (by id or #handle).',
    inputSchema: { id: z.string() },
  },
  async ({ id }) => {
    try {
      const boot = await api.bootstrap();
      const task = await api.updateTask(resolveTaskId(id, boot.tasks), { status: 'done' });
      return text(`✅ Done: **${task.title}**`);
    } catch (e) {
      return fail(e);
    }
  },
);

// ---- list_projects ----
server.registerTool(
  'list_projects',
  {
    title: 'List projects & outcomes',
    description: 'All projects with their outcome (the goal/why), due date, and progress toward it.',
    inputSchema: {},
  },
  async () => {
    try {
      return text(renderProjects(await api.bootstrap()));
    } catch (e) {
      return fail(e);
    }
  },
);

// ---- project_status ----
server.registerTool(
  'project_status',
  {
    title: 'Project status',
    description: "A project's outcome, progress, and exactly what's left (grouped by status) to reach it.",
    inputSchema: { project: z.string().describe('Project name or id') },
  },
  async ({ project }) => {
    try {
      const boot = await api.bootstrap();
      const p = resolveProject(project, boot.projects);
      if (!p) return text(`No project matches "${project}". Available: ${boot.projects.map((x) => x.name).join(', ') || '(none)'}`);
      return text(renderProjectStatus(p, boot));
    } catch (e) {
      return fail(e);
    }
  },
);

function resolveTaskId(ref: string, tasks: Task[]): string {
  const clean = ref.replace(/^#/, '');
  const exact = tasks.find((t) => t.id === clean);
  if (exact) return exact.id;
  // handles are the id's unique suffix; also tolerate a prefix match
  const bySuffix = tasks.find((t) => t.id.endsWith(clean));
  if (bySuffix) return bySuffix.id;
  const byPrefix = tasks.find((t) => t.id.startsWith(clean));
  if (byPrefix) return byPrefix.id;
  return clean; // let the server 404 if truly unknown
}

/* ----------------------------------------------------------- resources */

server.registerResource(
  'today',
  'cadence://today',
  { title: "Today's focus", description: 'Your focus view for today', mimeType: 'text/markdown' },
  async (uri) => ({ contents: [{ uri: uri.href, mimeType: 'text/markdown', text: renderFocus(await api.bootstrap()) }] }),
);

server.registerResource(
  'projects',
  'cadence://projects',
  { title: 'Projects & outcomes', description: 'Projects with outcomes and progress', mimeType: 'text/markdown' },
  async (uri) => ({ contents: [{ uri: uri.href, mimeType: 'text/markdown', text: renderProjects(await api.bootstrap()) }] }),
);

/* ------------------------------------------------------------- prompts */

server.registerPrompt(
  'daily-focus',
  { title: 'Plan my day', description: 'Review today and propose a focused plan around your energy.' },
  async () => {
    const focus = renderFocus(await api.bootstrap());
    return {
      messages: [
        {
          role: 'user' as const,
          content: {
            type: 'text' as const,
            text:
              `Here is my Cadence focus view. Help me plan a realistic day: pick 1–3 things that matter most, ` +
              `protect my peak window for deep work, and flag anything overdue or at risk. Be concise.\n\n${focus}`,
          },
        },
      ],
    };
  },
);

server.registerPrompt(
  'weekly-review',
  { title: 'Weekly review', description: 'Review the week and progress toward each project outcome.' },
  async () => {
    const boot = await api.bootstrap();
    const week = inRange(boot.tasks, startOfWeek(new Date()), addDays(startOfWeek(new Date()), 7))
      .map(({ task, at }) => taskLine(task, boot.projects, at))
      .join('\n');
    const projects = renderProjects(boot);
    return {
      messages: [
        {
          role: 'user' as const,
          content: {
            type: 'text' as const,
            text:
              `Run a weekly review with me. What did I commit to this week, what's at risk, and is each project ` +
              `on track for its outcome? Suggest what to drop, reschedule, or push.\n\n## This week\n${week || '_nothing scheduled_'}\n\n${projects}`,
          },
        },
      ],
    };
  },
);

/* -------------------------------------------------------------- connect */

async function main() {
  const transport = new StdioServerTransport();
  await server.connect(transport);
  // stderr is safe for logs (stdout is the protocol channel)
  console.error(`cadence-mcp ready → ${api.apiUrl}${api.hasToken() ? '' : '  (no CADENCE_API_TOKEN set!)'}`);
}

main().catch((e) => {
  console.error('cadence-mcp fatal:', e);
  process.exit(1);
});
