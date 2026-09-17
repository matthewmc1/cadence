import '../styles/strips.css';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useApp, useActions } from '../state/store';
import type { TaskRow } from '../state/selectors';
import { KINDS } from '../lib/energy';
import { addDays, clock, hourOf, ymd } from '../lib/time';

/** How many items the payoff names before it stops listing and starts counting. */
const SHOWN = 5;
/** How many unscheduled items the fallback (nothing promoted this session) draws on. */
const FALLBACK_MAX = 8;
const DOW = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const WORDS = ['no', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten'];

const word = (n: number) => WORDS[n] ?? String(n);
const todayYMD = () => ymd(new Date());
const onToday = (t: { scheduledAt: string | null }) => !!t.scheduledAt && ymd(new Date(t.scheduledAt)) === todayYMD();

/** A time, said the way Work says it: Today 14:00 · Tomorrow · Thu 11 Sep. */
function whenLabel(iso: string): string {
  const d = new Date(iso);
  const day = ymd(d);
  if (day === todayYMD()) return `Today ${clock(hourOf(d))}`;
  if (day === ymd(addDays(new Date(), 1))) return 'Tomorrow';
  return `${DOW[(d.getDay() + 6) % 7]} ${d.getDate()} ${MONTHS[d.getMonth()]}`;
}

/**
 * The inbox-zero payoff. Once the gate is clear — after real triage, not on a
 * fresh page — Cadence says what came out of it and hands the user straight
 * into doing: the next few items, and Enter to start the first one on Work.
 * Times are stated plainly ("three scheduled for today"), never in the
 * language of energy windows; giving the rest a time is an offer beside the
 * headline action, not the headline itself. Nothing is written until asked.
 */
export function InboxZero() {
  const { session, allTasks } = useApp();
  const { previewPlacement, applyPlacement, startTask, go } = useActions();
  const [answer, setAnswer] = useState<'left' | null>(null);

  // what is ready to pick up: this session's promotions first, else the open pile
  const ready = useMemo(() => {
    const open = allTasks.filter((t) => t.stage !== 'done');
    const promoted = session.promotedTaskIds.map((id) => open.find((t) => t.id === id)).filter((t): t is TaskRow => !!t);
    const pool = promoted.length ? promoted : open.filter((t) => !t.scheduledAt).slice(0, FALLBACK_MAX);
    // what today already promised first, then whatever carries the most weight
    return pool.slice().sort((a, b) => Number(onToday(b)) - Number(onToday(a)) || b.priority - a.priority || Date.parse(a.createdAt) - Date.parse(b.createdAt));
  }, [allTasks, session.promotedTaskIds]);

  const scheduledToday = ready.filter(onToday).length;
  const scheduledLater = ready.filter((t) => t.scheduledAt && ymd(new Date(t.scheduledAt)) > todayYMD()).length;

  // The heuristic placement is kept, but demoted: it is an offer for whatever
  // has no time yet, described as a time rather than as an energy window.
  const loose = useMemo(() => ready.filter((t) => !t.scheduledAt).map((t) => t.id), [ready]);
  const placements = useMemo(() => previewPlacement(loose), [loose, previewPlacement]);
  const placedToday = placements.every((p) => ymd(new Date(p.at)) === todayYMD());

  const first = ready[0];
  const start = () => {
    if (!first) return;
    void startTask(first.id);
    go('work');
  };
  const leave = () => setAnswer('left');

  // Enter / Escape from anywhere on the page — but never from inside the
  // capture box, and only while there is something to pick up. Capture phase,
  // so the Inbox's own Escape handling never sees it first.
  const live = session.dispositions > 0 && answer == null && !!first;
  const keys = useRef({ start, leave });
  keys.current = { start, leave };
  useEffect(() => {
    if (!live) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      // a field keeps its keys; a focused control keeps its Enter; a modal keeps the screen
      if (t?.closest('input, textarea, select, button, a, [role="button"], [contenteditable="true"], [aria-modal="true"]')) return;
      if (e.key === 'Enter') {
        e.preventDefault();
        e.stopPropagation();
        keys.current.start();
      } else if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        keys.current.leave();
      }
    };
    document.addEventListener('keydown', onKey, true);
    return () => document.removeEventListener('keydown', onKey, true);
  }, [live]);

  if (session.dispositions === 0) return null;

  if (!first || answer === 'left') {
    return (
      <div className="zero">
        <p className="zero__lead serif">{answer === 'left' ? "Nothing started — it's all in Work when you want it." : 'Nothing open to pick up.'}</p>
        <button type="button" className="zero__link" onClick={() => go('work')}>
          Open Work →
        </button>
      </div>
    );
  }

  const times = [scheduledToday ? `${word(scheduledToday)} scheduled for today` : '', scheduledLater ? `${word(scheduledLater)} with a time later on` : '']
    .filter(Boolean)
    .join(' · ');
  const rest = ready.length - SHOWN;

  return (
    <div className="zero" role="group" aria-label="What came out of triage">
      <h2 className="zero__title serif">
        Inbox clear.
        <span className="zero__title-sub"> {word(ready.length)} ready to pick up.</span>
      </h2>
      {times && <p className="zero__note">{times}.</p>}
      <ol className="zero__list">
        {ready.slice(0, SHOWN).map((t, i) => (
          <li key={t.id} className={'zero__item' + (i === 0 ? ' zero__item--first' : '')}>
            <span className="dot" style={{ width: 7, height: 7, background: KINDS[t.kind].dot }} />
            <span className="zero__item-title">{t.title}</span>
            {t.project && <span className="zero__proj">{t.project.name}</span>}
            <span className={t.scheduledAt ? 'zero__when tnum' : 'zero__when zero__when--none'}>{t.scheduledAt ? whenLabel(t.scheduledAt) : 'no time yet'}</span>
          </li>
        ))}
      </ol>
      {rest > 0 && <p className="zero__note zero__note--rest">and {rest} more in Work.</p>}
      <div className="zero__actions">
        <button type="button" className="zero__go" onClick={start}>
          {first.stage === 'doing' ? 'Pick up' : 'Start'} <span className="zero__go-name">{first.title}</span> <kbd className="qc__key">↵</kbd>
        </button>
        {/* the old headline, now an offer: a time for whatever has none */}
        {placements.length > 0 && (
          <button type="button" className="zero__offer" onClick={() => applyPlacement(placements)}>
            Give {word(placements.length)} of them a time {placedToday ? 'today' : 'this week'}
          </button>
        )}
        <button type="button" className="zero__skip" onClick={leave}>
          Not now <kbd className="qc__key">esc</kbd>
        </button>
      </div>
    </div>
  );
}
