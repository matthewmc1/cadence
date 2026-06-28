import '../styles/editor.css';
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useApp, useActions } from '../state/store';
import type { CreateTaskInput, Link, Recurrence, Subtask, TaskPatch } from '../api/types';
import { KINDS, inferTask, bestSlot, type Kind } from '../lib/energy';
import { clock, effortLabel } from '../state/selectors';
import { combine, hourOf, ymd, startOfWeek, addDays } from '../lib/time';

type Status = 'backlog' | 'scheduled' | 'focus' | 'done';

interface Form {
  title: string;
  kind: Kind;
  status: Status;
  effortMinutes: number;
  urgent: boolean;
  note: string;
  projectId: string | null;
  place: string | null;
  scheduledDate: string; // yyyy-mm-dd, '' = unscheduled
  scheduledHour: number;
  deadline: string | null; // ISO
  recurrence: Recurrence;
  links: Link[];
  subtasks: Subtask[];
  assigneeIds: string[];
}

const STATUSES: { v: Status; label: string }[] = [
  { v: 'backlog', label: 'Backlog' },
  { v: 'scheduled', label: 'Scheduled' },
  { v: 'focus', label: 'In focus' },
  { v: 'done', label: 'Done' },
];
const KIND_LIST: Kind[] = ['deep', 'light', 'admin', 'meet', 'personal'];
const EFFORTS = [15, 30, 45, 60, 90, 120];
const PLACES = ['Home', 'Office', 'Café'];
const RECUR: Recurrence[] = ['none', 'daily', 'weekdays', 'weekly', 'monthly'];

function blankForm(): Form {
  return {
    title: '', kind: 'light', status: 'backlog', effortMinutes: 40, urgent: false, note: '',
    projectId: null, place: null, scheduledDate: '', scheduledHour: 9,
    deadline: null, recurrence: 'none', links: [], subtasks: [], assigneeIds: [],
  };
}

let tmp = 0;
const tmpId = () => `sub-tmp-${tmp++}`;

export function TaskEditor() {
  const { editorOpen, editorMode, editingTask, projects, people } = useApp();
  const actions = useActions();

  const [form, setForm] = useState<Form>(blankForm());
  const touchedKind = useRef(false);
  const titleRef = useRef<HTMLInputElement>(null);

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
        title: t.title, kind: t.kind, status: t.status, effortMinutes: t.effortMinutes, urgent: t.urgent,
        note: t.note, projectId: t.projectId, place: t.place,
        scheduledDate: t.scheduledAt ? ymd(new Date(t.scheduledAt)) : '',
        scheduledHour: t.scheduledAt ? hourOf(new Date(t.scheduledAt)) : 9,
        deadline: t.deadline, recurrence: t.recurrence,
        links: t.links ?? [], subtasks: t.subtasks ?? [], assigneeIds: (t.assignees ?? []).map((a) => a.userId),
      });
    } else {
      setForm(blankForm());
    }
    setTimeout(() => titleRef.current?.focus(), 60);
  }, [editorOpen, openKey, editorMode, editingTask]);

  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm((f) => ({ ...f, [k]: v }));

  // live inference while creating (until the user picks a type)
  const inf = useMemo(() => inferTask(form.title || 'task'), [form.title]);
  useEffect(() => {
    if (editorMode === 'create' && !touchedKind.current) {
      setForm((f) => ({ ...f, kind: inf.kind, effortMinutes: Math.round(inf.effortHrs * 60) }));
    }
  }, [inf, editorMode]);

  if (!editorOpen) return null;

  const suggestion = bestSlot(form.kind, 0);
  const applyBestSlot = () => {
    const date = ymd(addDays(startOfWeek(new Date()), suggestion.dayIndex));
    setForm((f) => ({ ...f, status: 'scheduled', scheduledDate: date, scheduledHour: suggestion.hour, place: f.place ?? suggestion.place }));
  };

  const close = () => actions.closeEditor();

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
    const common = {
      title, kind: form.kind, status: form.status, effortMinutes: form.effortMinutes, urgent: form.urgent,
      note: form.note, projectId: form.projectId, place: form.place, scheduledAt,
      deadline: form.deadline, recurrence: form.recurrence, links: form.links, subtasks: form.subtasks, assignees,
    };

    if (editorMode === 'edit' && editingTask) {
      void actions.saveTask(editingTask.id, common as TaskPatch);
    } else {
      void actions.createTaskFull(common as CreateTaskInput);
    }
  };

  const doneSubs = form.subtasks.filter((s) => s.done).length;

  return (
    <div className="editor-scrim" onMouseDown={close}>
      <aside
        className="editor"
        role="dialog"
        aria-modal="true"
        aria-label={editorMode === 'edit' ? 'Edit task' : 'New task'}
        onMouseDown={(e) => e.stopPropagation()}
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
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) submit();
            }}
            placeholder="What needs doing?"
            aria-label="Task title"
          />

          {editorMode === 'create' && (
            <button className="editor__suggest" onClick={applyBestSlot} type="button">
              <span className="dot" style={{ width: 6, height: 6, background: 'var(--accent)' }} />
              Cadence suggests <b>{KINDS[form.kind].label}</b> · {suggestion.dayName} {clock(suggestion.hour)} — apply
            </button>
          )}

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

          <Field label="Effort">
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

          <Field label="Status">
            <Pills options={STATUSES.map((s) => ({ v: s.v, label: s.label }))} value={form.status} onChange={(v) => set('status', v as Status)} />
          </Field>

          <Field label="Schedule">
            <div className="editor__sched">
              <input
                type="date"
                className="editor__date"
                value={form.scheduledDate}
                onChange={(e) => setForm((f) => ({ ...f, scheduledDate: e.target.value, status: e.target.value && f.status === 'backlog' ? 'scheduled' : f.status }))}
              />
              {form.scheduledDate && (
                <>
                  <span className="editor__at">at</span>
                  <TimeInput value={form.scheduledHour} onChange={(v) => set('scheduledHour', v)} />
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
                  Clear
                </button>
              )}
            </div>
          </Field>

          <div className="editor__grid2">
            <Field label="Place">
              <Pills
                options={[{ v: '', label: '—' }, ...PLACES.map((p) => ({ v: p, label: p }))]}
                value={form.place ?? ''}
                onChange={(v) => set('place', v || null)}
              />
            </Field>
            <Field label="Project">
              <select className="editor__select" value={form.projectId ?? ''} onChange={(e) => set('projectId', e.target.value || null)}>
                <option value="">No project</option>
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </Field>
          </div>

          <div className="editor__grid2">
            <Field label="Deadline">
              <input
                type="date"
                className="editor__select"
                value={form.deadline ? form.deadline.slice(0, 10) : ''}
                onChange={(e) => set('deadline', e.target.value ? new Date(e.target.value + 'T17:00:00Z').toISOString() : null)}
              />
            </Field>
            <Field label="Repeats">
              <select className="editor__select" value={form.recurrence} onChange={(e) => set('recurrence', e.target.value as Recurrence)}>
                {RECUR.map((r) => (
                  <option key={r} value={r}>
                    {r === 'none' ? 'One-off' : r[0].toUpperCase() + r.slice(1)}
                  </option>
                ))}
              </select>
            </Field>
          </div>

          <Field label="Urgent">
            <Toggle on={form.urgent} onChange={(v) => set('urgent', v)} label="Mark urgent" />
          </Field>

          {people.length > 0 && (
            <Field label="With">
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
                  <button type="button" className="editor__x" onClick={() => set('subtasks', form.subtasks.filter((_, j) => j !== i))} aria-label="Remove subtask">
                    ✕
                  </button>
                </div>
              ))}
              <button type="button" className="editor__add" onClick={() => set('subtasks', [...form.subtasks, { id: tmpId(), title: '', done: false }])}>
                + Add subtask
              </button>
            </div>
          </Field>

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

function Toggle({ on, onChange, label }: { on: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <button type="button" className={'editor__toggle' + (on ? ' is-on' : '')} onClick={() => onChange(!on)} role="switch" aria-checked={on} aria-label={label}>
      <span className="editor__toggle-knob" />
    </button>
  );
}

function TimeInput({ value, onChange }: { value: number | null; onChange: (v: number) => void }) {
  return (
    <input
      type="number"
      className="editor__time tnum"
      min={6}
      max={22}
      step={0.5}
      value={value ?? 9}
      onChange={(e) => onChange(Number(e.target.value))}
      aria-label="Time (hour)"
    />
  );
}
