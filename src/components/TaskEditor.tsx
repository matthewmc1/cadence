import '../styles/editor.css';
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useApp, useActions } from '../state/store';
import type { Ask, CreateTaskInput, Link, Recurrence, Stage, Status, Subtask, TaskPatch } from '../api/types';
import { KINDS, inferTask, type Kind } from '../lib/energy';
import { effortLabel, stageOf } from '../state/selectors';
import { combine, hourOf, ymd, startOfWeek, addDays } from '../lib/time';
import { tabbablesIn, trapTab } from '../lib/focus';

/**
 * The task drawer. It answers one question — what is this piece of work? — with
 * five fields: what it is, what done looks like, whose project it serves, when
 * it happens and where it stands. Everything else the record can carry is still
 * here, one click down under "More details", grouped by the question it answers
 * (Why · Who · Detail · Shape). Nothing was dropped; it just stopped arriving
 * all at once.
 */

interface AskForm {
  what: string;
  forWhom: string;
  why: string;
}

interface Form {
  // the five that always show
  title: string;
  definitionOfDone: string;
  projectId: string | null;
  scheduledDate: string; // yyyy-mm-dd, '' = no date
  scheduledHour: number; // decimal; only meaningful with a date
  stage: Stage;
  // why
  requirementId: string | null;
  ask: AskForm;
  askBy: string; // yyyy-mm-dd, '' = none
  important: boolean;
  urgent: boolean;
  // who
  ownerId: string | null;
  waitingOnPersonId: string | null;
  waitingOnReason: string;
  assigneeIds: string[];
  // detail
  note: string;
  subtasks: Subtask[];
  links: Link[];
  reflection: string;
  // shape
  kind: Kind;
  effortMinutes: number;
  place: string | null;
  recurrence: Recurrence;
  deadline: string | null; // ISO
}

const STAGES: { v: Stage; label: string }[] = [
  { v: 'todo', label: 'To do' },
  { v: 'doing', label: 'Doing' },
  { v: 'waiting', label: 'Waiting' },
  { v: 'done', label: 'Done' },
];
const KIND_LIST: Kind[] = ['deep', 'light', 'admin', 'meet', 'personal'];
const EFFORTS = [15, 30, 45, 60, 90, 120];
const PLACES = ['Home', 'Office', 'Café'];
const RECUR: Recurrence[] = ['none', 'daily', 'weekdays', 'weekly', 'monthly'];

/** Whether "More details" is open, remembered for the browsing session only. */
const MORE_KEY = 'cadence.editor.more';
function loadMore(): boolean {
  try {
    return sessionStorage.getItem(MORE_KEY) === '1';
  } catch {
    return false; // private mode / storage blocked: start closed
  }
}
function saveMore(open: boolean): void {
  try {
    sessionStorage.setItem(MORE_KEY, open ? '1' : '0');
  } catch {
    /* nothing to remember it with */
  }
}

/**
 * The status the server derives from a stage, sent alongside it so the
 * optimistic row lands in the right Board column before the echo arrives.
 */
function statusFor(stage: Stage, scheduledAt: string | null): Status {
  if (stage === 'done') return 'done';
  if (stage === 'doing') return 'focus';
  if (stage === 'waiting') return 'backlog';
  return scheduledAt ? 'scheduled' : 'backlog';
}

/** The ask as the API wants it: a whole object, or null when nothing was said. */
function composeAsk(a: AskForm): Ask | null {
  const what = a.what.trim();
  const forWhom = a.forWhom.trim();
  const why = a.why.trim();
  return what || forWhom || why ? { what, forWhom, why } : null;
}

const hhmm = (h: number) => {
  const mins = Math.max(0, Math.min(24 * 60 - 1, Math.round(h * 60)));
  return `${String(Math.floor(mins / 60)).padStart(2, '0')}:${String(mins % 60).padStart(2, '0')}`;
};
const fromHhmm = (v: string) => {
  const [h, m] = v.split(':').map(Number);
  return Number.isFinite(h) ? h + (Number.isFinite(m) ? m : 0) / 60 : 9;
};

function blankForm(): Form {
  return {
    title: '', definitionOfDone: '', projectId: null, scheduledDate: '', scheduledHour: 9, stage: 'todo',
    requirementId: null, ask: { what: '', forWhom: '', why: '' }, askBy: '', important: false, urgent: false,
    ownerId: null, waitingOnPersonId: null, waitingOnReason: '', assigneeIds: [],
    note: '', subtasks: [], links: [], reflection: '',
    kind: 'light', effortMinutes: 40, place: null, recurrence: 'none', deadline: null,
  };
}

let tmp = 0;
const tmpId = () => `sub-tmp-${tmp++}`;

export function TaskEditor() {
  const { editorOpen, editorMode, editingTask, projects, requirements, people, user, originsByTask } = useApp();
  const actions = useActions();
  // Where the item came from: the inbox signals promoted into it or attached
  // to it. Fetched when the editor opens, so an attach is visible on the item.
  const editingId = editorMode === 'edit' ? (editingTask?.id ?? null) : null;
  const { loadOrigins } = actions;
  useEffect(() => {
    if (editingId) void loadOrigins(editingId);
  }, [editingId, loadOrigins]);
  const origins = editingId ? (originsByTask[editingId] ?? []) : [];

  const [form, setForm] = useState<Form>(blankForm());
  const [more, setMore] = useState(loadMore);
  const touchedKind = useRef(false);
  const titleRef = useRef<HTMLInputElement>(null);
  const panelRef = useRef<HTMLElement>(null);

  // (re)initialize when the editor opens or the target changes
  const openKey = editorMode + ':' + (editingTask?.id ?? 'new');
  const lastKey = useRef('');
  const prevOpen = useRef(false);
  useEffect(() => {
    if (!editorOpen) {
      prevOpen.current = false;
      return;
    }
    const justOpened = !prevOpen.current;
    prevOpen.current = true;
    // reset every time the drawer opens, or when switching to a different task
    if (!justOpened && lastKey.current === openKey) return;
    lastKey.current = openKey;
    touchedKind.current = editorMode === 'edit';
    if (editorMode === 'edit' && editingTask) {
      const t = editingTask;
      setForm({
        title: t.title,
        definitionOfDone: t.definitionOfDone ?? '',
        projectId: t.projectId,
        scheduledDate: t.scheduledAt ? ymd(new Date(t.scheduledAt)) : '',
        scheduledHour: t.scheduledAt ? hourOf(new Date(t.scheduledAt)) : 9,
        stage: stageOf(t),
        requirementId: t.requirementId ?? null,
        ask: { what: t.ask?.what ?? '', forWhom: t.ask?.forWhom ?? '', why: t.ask?.why ?? '' },
        askBy: t.askBy ? ymd(new Date(t.askBy)) : '',
        important: t.important,
        urgent: t.urgent,
        ownerId: t.ownerId ?? null,
        waitingOnPersonId: t.waitingOnPersonId ?? null,
        waitingOnReason: t.waitingOnReason ?? '',
        assigneeIds: (t.assignees ?? []).map((a) => a.userId),
        note: t.note,
        subtasks: t.subtasks ?? [],
        links: t.links ?? [],
        reflection: t.reflection,
        kind: t.kind,
        effortMinutes: t.effortMinutes,
        place: t.place,
        recurrence: t.recurrence,
        deadline: t.deadline,
      });
    } else {
      setForm(blankForm());
    }
    setTimeout(() => titleRef.current?.focus(), 60);
  }, [editorOpen, openKey, editorMode, editingTask]);

  // Keyboard contract while the drawer is open: Escape closes, Tab/Shift+Tab
  // wrap inside the panel, focus that lands outside is pulled back in, and on
  // close focus returns to whatever opened the drawer (the row, "+ New task").
  useEffect(() => {
    if (!editorOpen) return;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const onKey = (e: KeyboardEvent) => {
      // a control inside the panel that already consumed the key keeps it
      if (e.defaultPrevented) return;
      if (e.key === 'Escape') {
        e.preventDefault();
        actions.closeEditor();
        return;
      }
      trapTab(e, panelRef.current);
    };
    // A stray focus (browser chrome, a portal behind the scrim) gets redirected
    // to the first control; skipped while the target IS inside the panel.
    const onFocusIn = (e: FocusEvent) => {
      const panel = panelRef.current;
      if (!panel || (e.target instanceof Node && panel.contains(e.target))) return;
      (titleRef.current ?? tabbablesIn(panel)[0])?.focus();
    };
    document.addEventListener('keydown', onKey);
    document.addEventListener('focusin', onFocusIn);
    return () => {
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('focusin', onFocusIn);
      if (opener && opener.isConnected) opener.focus();
    };
  }, [editorOpen, actions]);

  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm((f) => ({ ...f, [k]: v }));

  // live inference while creating (until the user picks a type)
  const inf = useMemo(() => inferTask(form.title || 'task'), [form.title]);
  useEffect(() => {
    if (editorMode === 'create' && !touchedKind.current) {
      setForm((f) => ({ ...f, kind: inf.kind, effortMinutes: Math.round(inf.effortHrs * 60) }));
    }
  }, [inf, editorMode]);

  // the requirements this project can be scoped to, plus whichever one is
  // already set (a met requirement must not vanish out from under the item)
  const openReqs = useMemo(
    () =>
      requirements
        .filter((r) => r.projectId === form.projectId && !r.archivedAt && (r.status === 'open' || r.id === form.requirementId))
        .sort((a, b) => a.position - b.position),
    [requirements, form.projectId, form.requirementId],
  );

  if (!editorOpen) return null;

  const close = () => actions.closeEditor();

  const personLabel = (userId: string) => {
    if (user && user.id === userId) return user.name ? `${user.name} (you)` : 'You';
    return people.find((p) => p.userId === userId)?.initial ?? 'Someone';
  };

  const pickProject = (id: string | null) =>
    setForm((f) => ({
      ...f,
      projectId: id,
      // a requirement belongs to exactly one project; moving project drops it
      requirementId: requirements.some((r) => r.id === f.requirementId && r.projectId === id) ? f.requirementId : null,
    }));

  const submit = () => {
    const title = form.title.trim();
    if (!title) {
      titleRef.current?.focus();
      return;
    }
    const assignees = form.assigneeIds
      .map((id) => people.find((p) => p.userId === id))
      .filter((p): p is NonNullable<typeof p> => !!p)
      .map((p) => ({ userId: p.userId, initial: p.initial, color: p.color }));

    const scheduledAt = form.scheduledDate ? combine(form.scheduledDate, form.scheduledHour) : null;
    const base = {
      title,
      kind: form.kind,
      stage: form.stage,
      status: statusFor(form.stage, scheduledAt),
      effortMinutes: form.effortMinutes,
      urgent: form.urgent,
      important: form.important,
      note: form.note,
      reflection: form.reflection,
      projectId: form.projectId,
      requirementId: form.requirementId,
      definitionOfDone: form.definitionOfDone.trim(),
      ownerId: form.ownerId,
      waitingOnPersonId: form.waitingOnPersonId,
      waitingOnReason: form.waitingOnReason.trim(),
      askBy: form.askBy ? `${form.askBy}T17:00:00Z` : null,
      place: form.place,
      scheduledAt,
      deadline: form.deadline,
      recurrence: form.recurrence,
      links: form.links,
      subtasks: form.subtasks,
      assignees,
    };
    const ask = composeAsk(form.ask);

    if (editorMode === 'edit' && editingTask) {
      const patch: TaskPatch = { ...base, ask }; // null clears the ask
      void actions.saveTask(editingTask.id, patch);
    } else {
      const input: CreateTaskInput = { ...base };
      if (ask) input.ask = ask;
      void actions.createTaskFull(input);
    }
  };

  const doneSubs = form.subtasks.filter((s) => s.done).length;

  // how much is folded away, so nothing that has been filled in hides silently
  const filled = [
    form.requirementId,
    composeAsk(form.ask),
    form.askBy,
    form.important,
    form.urgent,
    form.ownerId,
    form.waitingOnPersonId,
    form.waitingOnReason.trim(),
    form.assigneeIds.length,
    form.note.trim(),
    form.subtasks.length,
    form.links.length,
    form.place,
    form.recurrence !== 'none',
    form.deadline,
  ].filter(Boolean).length;

  // Waiting needs a person or a reason, so those two ride up next to the stage
  // while it is set — the only field that moves, and only when it is load-bearing.
  const waitingFields = (
    <div className="editor__grid2">
      <Field label="Waiting on">
        <select
          className="editor__select"
          value={form.waitingOnPersonId ?? ''}
          onChange={(e) => set('waitingOnPersonId', e.target.value || null)}
        >
          <option value="">Nobody in particular</option>
          {people.map((p) => (
            <option key={p.userId} value={p.userId}>
              {personLabel(p.userId)}
            </option>
          ))}
        </select>
      </Field>
      <Field label="What for">
        <input
          className="editor__input"
          value={form.waitingOnReason}
          onChange={(e) => set('waitingOnReason', e.target.value)}
          placeholder="What has to land first"
        />
      </Field>
    </div>
  );

  return (
    <div className="editor-scrim" onMouseDown={close}>
      <aside
        ref={panelRef}
        className="editor"
        role="dialog"
        aria-modal="true"
        aria-label={editorMode === 'edit' ? 'Edit task' : 'New task'}
        onMouseDown={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          // ⌘↵ / Ctrl+↵ saves from anywhere in the drawer, notes included
          if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            submit();
          }
        }}
      >
        <header className="editor__head">
          <span className="eyebrow">{editorMode === 'edit' ? 'Edit task' : 'New task'}</span>
          <button className="editor__close" onClick={close} aria-label="Close">
            ✕
          </button>
        </header>

        <div className="editor__body">
          <input
            ref={titleRef}
            className="editor__title serif"
            value={form.title}
            onChange={(e) => set('title', e.target.value)}
            placeholder="What needs doing?"
            aria-label="Task title"
          />

          <Field label="What done looks like">
            <input
              className="editor__input"
              value={form.definitionOfDone}
              onChange={(e) => set('definitionOfDone', e.target.value)}
              placeholder="The thing that exists when this is finished"
            />
          </Field>

          <Field label="Project">
            <select className="editor__select" value={form.projectId ?? ''} onChange={(e) => pickProject(e.target.value || null)}>
              <option value="">No project</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>

          <Field label="When">
            <div className="editor__sched">
              <input
                type="date"
                className="editor__date"
                value={form.scheduledDate}
                onChange={(e) => set('scheduledDate', e.target.value)}
                aria-label="Date"
              />
              {form.scheduledDate && (
                <>
                  <span className="editor__at">at</span>
                  <input
                    type="time"
                    className="editor__time tnum"
                    step={1800}
                    value={hhmm(form.scheduledHour)}
                    onChange={(e) => set('scheduledHour', e.target.value ? fromHhmm(e.target.value) : 9)}
                    aria-label="Time"
                  />
                </>
              )}
            </div>
            <div className="editor__quick">
              <button type="button" className="editor__quick-btn" onClick={() => set('scheduledDate', ymd(new Date()))}>
                Today
              </button>
              <button type="button" className="editor__quick-btn" onClick={() => set('scheduledDate', ymd(addDays(new Date(), 1)))}>
                Tomorrow
              </button>
              <button type="button" className="editor__quick-btn" onClick={() => set('scheduledDate', ymd(addDays(startOfWeek(new Date()), 7)))}>
                Next Monday
              </button>
              {form.scheduledDate && (
                <button type="button" className="editor__quick-btn" onClick={() => set('scheduledDate', '')}>
                  No date
                </button>
              )}
            </div>
          </Field>

          <Field label="Stage">
            <Pills
              options={STAGES.map((s) => ({ v: s.v, label: s.label }))}
              value={form.stage}
              onChange={(v) =>
                setForm((f) => {
                  const stage = v as Stage;
                  // Leaving "waiting" empties what it was waiting on — in the
                  // form, where it can be seen, never quietly on the way out.
                  return f.stage === 'waiting' && stage !== 'waiting'
                    ? { ...f, stage, waitingOnPersonId: null, waitingOnReason: '' }
                    : { ...f, stage };
                })
              }
            />
          </Field>

          {form.stage === 'waiting' && waitingFields}

          {form.stage === 'done' && (
            <Field label="What it advanced">
              <input
                className="editor__input"
                value={form.reflection}
                onChange={(e) => set('reflection', e.target.value)}
                placeholder="What outcome did finishing this move forward?"
              />
            </Field>
          )}

          <button
            type="button"
            className={'editor__more' + (more ? ' is-open' : '')}
            aria-expanded={more}
            aria-controls="editor-more"
            onClick={() => {
              setMore(!more);
              saveMore(!more);
            }}
          >
            <span className="editor__more-chev" aria-hidden>
              ›
            </span>
            More details
            {!more && filled > 0 && <span className="editor__more-count">{filled} set</span>}
          </button>

          <div className="editor__more-body" id="editor-more" hidden={!more}>
            <Group title="Why" hint="What this work is for">
              <Field label="Requirement">
                <select
                  className="editor__select"
                  value={form.requirementId ?? ''}
                  onChange={(e) => set('requirementId', e.target.value || null)}
                  disabled={!form.projectId}
                >
                  <option value="">{form.projectId ? (openReqs.length ? 'Unscoped' : 'No open requirements') : 'Pick a project first'}</option>
                  {openReqs.map((r) => (
                    <option key={r.id} value={r.id}>
                      {r.title}
                    </option>
                  ))}
                </select>
              </Field>

              <Field label="The ask">
                <div className="editor__stack">
                  <input
                    className="editor__input"
                    value={form.ask.what}
                    onChange={(e) => setForm((f) => ({ ...f, ask: { ...f.ask, what: e.target.value } }))}
                    placeholder="What is being asked for"
                  />
                  <div className="editor__grid2">
                    <input
                      className="editor__input"
                      value={form.ask.forWhom}
                      onChange={(e) => setForm((f) => ({ ...f, ask: { ...f.ask, forWhom: e.target.value } }))}
                      placeholder="Who it is for"
                      aria-label="Who the ask is for"
                    />
                    <input
                      className="editor__input"
                      value={form.ask.why}
                      onChange={(e) => setForm((f) => ({ ...f, ask: { ...f.ask, why: e.target.value } }))}
                      placeholder="Why they need it"
                      aria-label="Why the ask matters"
                    />
                  </div>
                </div>
              </Field>

              <Field label="Needed by">
                <input
                  type="date"
                  className="editor__select"
                  value={form.askBy}
                  onChange={(e) => set('askBy', e.target.value)}
                  aria-label="Needed by"
                />
              </Field>

              <div className="editor__switches">
                <ToggleRow
                  on={form.important}
                  onChange={(v) => set('important', v)}
                  title="Important"
                  hint="Moves an outcome forward, not just the day"
                />
                <ToggleRow on={form.urgent} onChange={(v) => set('urgent', v)} title="Urgent" hint="Time-sensitive — it goes stale if it slips" />
              </div>
            </Group>

            <Group title="Who" hint="Whose work this is">
              <Field label="Owner">
                <select className="editor__select" value={form.ownerId ?? ''} onChange={(e) => set('ownerId', e.target.value || null)}>
                  <option value="">Unassigned</option>
                  {people.map((p) => (
                    <option key={p.userId} value={p.userId}>
                      {personLabel(p.userId)}
                    </option>
                  ))}
                </select>
              </Field>

              {form.stage !== 'waiting' && waitingFields}

              {people.length > 0 && (
                <Field label="Also on it">
                  <div className="editor__people">
                    {people.map((p) => {
                      const on = form.assigneeIds.includes(p.userId);
                      return (
                        <button
                          key={p.userId}
                          type="button"
                          className={'editor__person' + (on ? ' is-on' : '')}
                          onClick={() =>
                            set('assigneeIds', on ? form.assigneeIds.filter((x) => x !== p.userId) : [...form.assigneeIds, p.userId])
                          }
                          aria-pressed={on}
                          aria-label={personLabel(p.userId)}
                        >
                          <span className="editor__avatar" style={{ background: p.color }}>
                            {p.initial}
                          </span>
                        </button>
                      );
                    })}
                  </div>
                </Field>
              )}
            </Group>

            <Group title="Detail" hint="Everything that comes with it">
              <Field label="Notes">
                <textarea
                  className="editor__note"
                  value={form.note}
                  onChange={(e) => set('note', e.target.value)}
                  placeholder="Anything to remember…"
                  rows={2}
                />
              </Field>

              <Field label={`Subtasks${form.subtasks.length ? ` · ${doneSubs}/${form.subtasks.length}` : ''}`}>
                <div className="editor__subs">
                  {form.subtasks.map((s, i) => (
                    <div className="editor__sub" key={s.id}>
                      <button
                        type="button"
                        className={'editor__check' + (s.done ? ' is-on' : '')}
                        onClick={() => set('subtasks', form.subtasks.map((x, j) => (j === i ? { ...x, done: !x.done } : x)))}
                        aria-pressed={s.done}
                        aria-label={s.done ? 'Mark subtask not done' : 'Mark subtask done'}
                      >
                        {s.done ? '✓' : ''}
                      </button>
                      <input
                        className="editor__sub-input"
                        value={s.title}
                        onChange={(e) => set('subtasks', form.subtasks.map((x, j) => (j === i ? { ...x, title: e.target.value } : x)))}
                        placeholder="Subtask"
                      />
                      <button
                        type="button"
                        className="editor__x"
                        onClick={() => set('subtasks', form.subtasks.filter((_, j) => j !== i))}
                        aria-label="Remove subtask"
                      >
                        ✕
                      </button>
                    </div>
                  ))}
                  <button type="button" className="editor__add" onClick={() => set('subtasks', [...form.subtasks, { id: tmpId(), title: '', done: false }])}>
                    + Add subtask
                  </button>
                </div>
              </Field>

              {origins.length > 0 && (
                <Field label="Came from">
                  <ul className="editor__origins">
                    {origins.map((o) => (
                      <li key={o.signalId} className="editor__origin">
                        <span className="editor__origin-kind">{o.snapshot.kind}</span>
                        <span className="editor__origin-title">{o.snapshot.title}</span>
                        <span className="editor__origin-date tnum">{new Date(o.snapshot.occurredAt).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })}</span>
                      </li>
                    ))}
                  </ul>
                </Field>
              )}

              <Field label="Links">
                <div className="editor__subs">
                  {form.links.map((l, i) => (
                    <div className="editor__link" key={i}>
                      <input
                        className="editor__sub-input"
                        value={l.label}
                        onChange={(e) => set('links', form.links.map((x, j) => (j === i ? { ...x, label: e.target.value } : x)))}
                        placeholder="Label"
                      />
                      <input
                        className="editor__sub-input editor__sub-input--url"
                        value={l.url}
                        onChange={(e) => set('links', form.links.map((x, j) => (j === i ? { ...x, url: e.target.value } : x)))}
                        placeholder="https://"
                      />
                      <button type="button" className="editor__x" onClick={() => set('links', form.links.filter((_, j) => j !== i))} aria-label="Remove link">
                        ✕
                      </button>
                    </div>
                  ))}
                  <button type="button" className="editor__add" onClick={() => set('links', [...form.links, { label: '', url: '' }])}>
                    + Add link
                  </button>
                </div>
              </Field>
            </Group>

            <Group title="Shape" hint="What kind of work it is">
              <Field label="Type">
                <Pills
                  options={KIND_LIST.map((k) => ({ v: k, label: KINDS[k].label, dot: KINDS[k].dot }))}
                  value={form.kind}
                  onChange={(v) => {
                    touchedKind.current = true;
                    set('kind', v as Kind);
                  }}
                />
              </Field>

              <Field label="Rough size">
                <div className="editor__chips">
                  {EFFORTS.map((m) => (
                    <button
                      key={m}
                      type="button"
                      className={'editor__chip' + (form.effortMinutes === m ? ' is-on' : '')}
                      onClick={() => set('effortMinutes', m)}
                    >
                      {effortLabel(m).replace('≈ ', '')}
                    </button>
                  ))}
                </div>
              </Field>

              <Field label="Place">
                <Pills
                  options={[{ v: '', label: '—' }, ...PLACES.map((p) => ({ v: p, label: p }))]}
                  value={form.place ?? ''}
                  onChange={(v) => set('place', v || null)}
                />
              </Field>

              <div className="editor__grid2">
                <Field label="Repeats">
                  <select className="editor__select" value={form.recurrence} onChange={(e) => set('recurrence', e.target.value as Recurrence)}>
                    {RECUR.map((r) => (
                      <option key={r} value={r}>
                        {r === 'none' ? 'One-off' : r[0].toUpperCase() + r.slice(1)}
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label="Hard deadline">
                  <input
                    type="date"
                    className="editor__select"
                    value={form.deadline ? form.deadline.slice(0, 10) : ''}
                    onChange={(e) => set('deadline', e.target.value ? new Date(e.target.value + 'T17:00:00Z').toISOString() : null)}
                  />
                </Field>
              </div>
            </Group>
          </div>
        </div>

        <footer className="editor__foot">
          {editorMode === 'edit' && editingTask && (
            <button className="editor__delete" onClick={() => actions.deleteTask(editingTask.id)}>
              Delete
            </button>
          )}
          <div className="editor__foot-right">
            <button className="btn-ghost" onClick={close}>
              Cancel
            </button>
            <button className="btn-primary" onClick={submit} disabled={!form.title.trim()}>
              {editorMode === 'edit' ? 'Save changes' : 'Add task'}
              <kbd className="editor__kbd" aria-hidden>
                ⌘↵
              </kbd>
            </button>
          </div>
        </footer>
      </aside>
    </div>
  );
}

/* ---- small controls ---- */

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="editor__field">
      <div className="editor__field-label">{label}</div>
      {children}
    </div>
  );
}

/** One plain heading inside "More details" — the question its fields answer. */
function Group({ title, hint, children }: { title: string; hint: string; children: ReactNode }) {
  return (
    <section className="editor__group">
      <h3 className="editor__group-title">
        {title}
        <span className="editor__group-hint">{hint}</span>
      </h3>
      {children}
    </section>
  );
}

function Pills({
  options,
  value,
  onChange,
}: {
  options: { v: string; label: string; dot?: string }[];
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="editor__pills">
      {options.map((o) => (
        <button
          key={o.v}
          type="button"
          className={'editor__pill' + (value === o.v ? ' is-on' : '')}
          onClick={() => onChange(o.v)}
          aria-pressed={value === o.v}
        >
          {o.dot && <span className="dot" style={{ width: 6, height: 6, background: o.dot }} />}
          {o.label}
        </button>
      ))}
    </div>
  );
}

/** A switch that carries its own name and one line of what it means. */
function ToggleRow({ on, onChange, title, hint }: { on: boolean; onChange: (v: boolean) => void; title: string; hint: string }) {
  return (
    <button
      type="button"
      className={'editor__switch' + (on ? ' is-on' : '')}
      onClick={() => onChange(!on)}
      role="switch"
      aria-checked={on}
      aria-label={title}
    >
      <span className="editor__toggle" aria-hidden>
        <span className="editor__toggle-knob" />
      </span>
      <span className="editor__switch-text">
        <span className="editor__switch-title">{title}</span>
        <span className="editor__switch-hint">{hint}</span>
      </span>
    </button>
  );
}
