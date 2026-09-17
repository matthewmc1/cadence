import '../styles/inbox.css';
import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useApp, useActions } from '../state/store';
import { api } from '../api/client';
import type { Signal, SignalBody, SignalKind } from '../api/types';
import { isExtractedFacts } from '../api/types';
import type { InboxBundle } from '../state/types';
import { CaptureBox } from '../components/CaptureBox';
import { PromoteStrip, suggestedTitle } from '../components/PromoteStrip';
import { FactChips } from '../components/FactChips';
import { InboxZero } from '../components/InboxZero';
import { modalIsOpen } from '../lib/focus';
import { tomorrow9am, ymd } from '../lib/time';
import { undoStack, MOD } from '../lib/undo';

/* ------------------------------------------------------------------ bits */

/** A bundle whose newest item is older than this folds itself; "sweep all" is its one action. */
const STALE_DAYS = 7;
const SEEN_KEY = 'cadence:inbox-seen'; // first-run copy shows until the inbox has ever held something

type Strip = { kind: 'promote' | 'attach' | 'hand' | 'snooze'; id: string };
/** Everything a row can be dispositioned into: the strips, plus the one that needs none. */
type Act = Strip['kind'] | 'dismiss';

/**
 * The five dispositions, in the order the legend names them. Each is a key
 * for the keyboard and a button for everyone else — a phone has no `t`.
 */
const ACTS: { kind: Act; label: string; key: string; full: string }[] = [
  { kind: 'promote', label: 'Promote', key: 't', full: 'Promote to a work item' },
  { kind: 'attach', label: 'Attach', key: 'a', full: 'Attach to a work item' },
  { kind: 'snooze', label: 'Snooze', key: 's', full: 'Snooze' },
  { kind: 'dismiss', label: 'Dismiss', key: 'e', full: 'Dismiss' },
  { kind: 'hand', label: 'Hand', key: 'd', full: 'Hand over to someone' },
];

/**
 * The legend along the bottom. Three keys carry the triage and are always
 * shown; the rest are one "?" away rather than a permanent wall of ten. The
 * keys themselves are unchanged — this is only what gets named.
 */
const CORE_KEYS: [string, string][] = [
  ['t', 'promote'],
  ['s', 'snooze'],
  ['e', 'dismiss'],
];
const MORE_KEYS: [string, string][] = [
  ['j k', 'move'],
  ['a', 'attach'],
  ['d', 'hand over'],
  ['x', 'select'],
  ['↵', 'open'],
  ['f', 'flat'],
  [`${MOD}Z`, 'undo'],
];

/** Touch has no double-tap-to-open worth the name, so one tap opens the detail. */
const COARSE = typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia('(pointer: coarse)').matches;

const KIND_GLYPH: Record<SignalKind, string> = { meeting: '◷', email: '✉', note: '✎', doc: '▤', chat: '◌', link: '↗', text: '¶' };
const KIND_NAME: Record<SignalKind, string> = { meeting: 'Meeting', email: 'Email', note: 'Note', doc: 'Document', chat: 'Chat', link: 'Link', text: 'Text' };
const SHORT_DAY = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const SHORT_MONTH = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

const hm = (d: Date) => `${d.getHours()}:${String(d.getMinutes()).padStart(2, '0')}`;

/** When a signal occurred, at the precision that matters: time today, day this week, date otherwise. */
function whenLabel(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const dayMs = 86400000;
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const day = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const diff = Math.round((today.getTime() - day.getTime()) / dayMs);
  if (diff === 0) return hm(d);
  if (diff === 1) return `Yesterday ${hm(d)}`;
  if (diff < 7 && diff > 0) return `${SHORT_DAY[d.getDay()]} ${hm(d)}`;
  if (d.getFullYear() === now.getFullYear()) return `${d.getDate()} ${SHORT_MONTH[d.getMonth()]}`;
  return `${d.getDate()} ${SHORT_MONTH[d.getMonth()]} ${d.getFullYear()}`;
}

function untilLabel(d: Date): string {
  return `${SHORT_DAY[d.getDay()]} ${d.getDate()} ${SHORT_MONTH[d.getMonth()]} ${hm(d)}`;
}

/**
 * The first line of the excerpt that isn't the title again. A title is the
 * body's first line cut to length, so a line that begins with it is the same
 * line — show what follows the cut, or the next line, rather than a repeat.
 */
function excerptLine(s: Signal): string {
  const title = s.title.trim().replace(/…$/, '');
  const lines = s.excerpt.split('\n').map((l) => l.trim()).filter(Boolean);
  let first = '';
  for (const l of lines) {
    if (title && l.startsWith(title)) {
      if (l.length === title.length) continue;
      // the cut can land mid-word: resume from the last whole word of the title
      const cut = title.lastIndexOf(' ');
      first = '…' + l.slice(cut > 0 ? cut : title.length).trim();
      break;
    }
    first = l;
    break;
  }
  return first.length > 160 ? first.slice(0, 157) + '…' : first;
}

function isStale(b: InboxBundle<Signal>, now: number): boolean {
  const newest = b.items[0];
  return !!newest && now - Date.parse(newest.occurredAt) > STALE_DAYS * 86400000;
}

/** Monday 09:00 after today. */
function nextMonday9(from = new Date()): Date {
  const add = ((8 - from.getDay()) % 7) || 7;
  return new Date(from.getFullYear(), from.getMonth(), from.getDate() + add, 9, 0, 0, 0);
}

/* ------------------------------------------------------------------ view */

/**
 * Inbox — the gate. Every captured signal lands here and leaves by one key:
 * promote (t), attach (a), snooze (s), dismiss (e), hand over (d) — and by
 * one button, since a phone has no keyboard: the same five ride each row
 * (on hover, always on touch) and the detail rail. Nothing here opens a
 * modal or asks twice; everything is undoable (⌘Z, or the toast).
 * Bundles fold the flat list by project → person → kind and `f` unfolds it;
 * the row under the cursor is the one the keys act on, unless a selection
 * exists, in which case snooze/dismiss/hand apply to all of it as one action.
 */
export function InboxView() {
  const { inbox, projects, clients, people, allTasks, editorOpen, user } = useApp();
  const {
    snoozeSignal, dismissSignal, disposeMany, promoteSignal, attachSignal, undo,
    loadMoreInbox, setInboxView, selectInbox, toggleSelect, clearSelection, retryInbox,
  } = useActions();

  const [strip, setStrip] = useState<Strip | null>(null);
  const [detail, setDetail] = useState(false);
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({}); // the user's overrides of the stale fold
  const [allKeys, setAllKeys] = useState(false); // the legend names three keys; "?" names the rest
  const [seen, setSeen] = useState(() => {
    try {
      return localStorage.getItem(SEEN_KEY) === '1';
    } catch {
      return false;
    }
  });
  const rowRefs = useRef(new Map<string, HTMLDivElement>());
  const focusPending = useRef(false);
  const nowMs = Date.now();

  const projById = useMemo(() => new Map(projects.map((p) => [p.id, p])), [projects]);
  const cliById = useMemo(() => new Map(clients.map((c) => [c.id, c])), [clients]);

  // once something has ever been here, the zero state is a reward, not a tutorial
  useEffect(() => {
    if (inbox.count > 0 && !seen) {
      setSeen(true);
      try {
        localStorage.setItem(SEEN_KEY, '1');
      } catch {
        /* private mode: the copy just shows again next time */
      }
    }
  }, [inbox.count, seen]);

  const isFolded = (b: InboxBundle<Signal>) => collapsed[b.key] ?? isStale(b, nowMs);

  // the rows the cursor can reach, in the order it walks them
  const visible: Signal[] = inbox.view === 'flat' ? inbox.items : inbox.bundles.flatMap((b) => (isFolded(b) ? [] : b.items));
  const currentId = visible.some((s) => s.id === inbox.selectedId) ? inbox.selectedId : visible[0]?.id ?? null;
  const current = currentId ? inbox.items.find((s) => s.id === currentId) ?? null : null;
  const currentProject = current?.projectHint ? projById.get(current.projectHint) : undefined;
  const currentClient = currentProject?.clientId ? cliById.get(currentProject.clientId) : undefined;
  const bundleOf = (id: string | null) => (id ? inbox.bundles.find((b) => b.items.some((s) => s.id === id)) ?? null : null);

  // a strip or the detail for a row that left the inbox closes with it
  useEffect(() => {
    if (strip && !inbox.items.some((s) => s.id === strip.id)) setStrip(null);
  }, [inbox.items, strip]);

  // keyboard moves land focus on the row; scroll just enough to show it
  useEffect(() => {
    if (!focusPending.current || !currentId) return;
    focusPending.current = false;
    const el = rowRefs.current.get(currentId);
    if (el) {
      el.focus({ preventScroll: true });
      el.scrollIntoView({ block: 'nearest' });
    }
  }, [currentId, strip]);

  const moveTo = (id: string | null) => {
    focusPending.current = true;
    selectInbox(id);
  };
  const move = (delta: number, extend: boolean) => {
    if (!visible.length) return;
    const i = Math.max(0, visible.findIndex((s) => s.id === currentId));
    const j = Math.min(visible.length - 1, Math.max(0, i + delta));
    if (extend) {
      // Shift+j/k grows the selection from the cursor: both ends in, never toggled out
      if (currentId && !inbox.selection.has(currentId)) toggleSelect(currentId);
      const to = visible[j].id;
      if (!inbox.selection.has(to) && to !== currentId) toggleSelect(to);
    }
    if (j === i && delta > 0 && inbox.hasMore) void loadMoreInbox();
    moveTo(visible[j].id);
  };

  /**
   * What a disposition acts on. A pointer names its row, and that row wins
   * unless it is part of the selection — then the whole selection goes, the
   * way it does for the keys, which name no row and always mean "here".
   */
  const targets = (only?: string | null): string[] => {
    if (only && !inbox.selection.has(only)) return [only];
    if (inbox.selection.size) return [...inbox.selection];
    return currentId ? [currentId] : [];
  };

  /** Move the cursor off rows that are about to leave, to the next one that stays. */
  const advanceFrom = (ids: string[]) => {
    if (!currentId || !ids.includes(currentId)) return;
    const i = visible.findIndex((s) => s.id === currentId);
    const after = visible.slice(i + 1).find((s) => !ids.includes(s.id));
    const before = visible.slice(0, i).reverse().find((s) => !ids.includes(s.id));
    moveTo(after?.id ?? before?.id ?? null);
  };

  const snooze = (until?: Date, only?: string | null) => {
    const ids = targets(only);
    if (!ids.length) return;
    setStrip(null);
    advanceFrom(ids);
    if (ids.length > 1) void disposeMany(ids, { stage: 'snoozed', snoozedUntil: (until ?? tomorrow9am()).toISOString() });
    else void snoozeSignal(ids[0], until);
  };
  const dismiss = (only?: string | null) => {
    const ids = targets(only);
    if (!ids.length) return;
    setStrip(null);
    advanceFrom(ids);
    if (ids.length > 1) void disposeMany(ids, { stage: 'dismissed' });
    else void dismissSignal(ids[0]);
  };
  const sweep = (b: InboxBundle<Signal>) => {
    const ids = b.items.map((s) => s.id);
    advanceFrom(ids);
    void disposeMany(ids, { stage: 'dismissed' });
  };
  // Hand over = promote as their work item, title only (scope is theirs to set).
  // Several at once collapse into one undo entry so ⌘Z takes the lot back.
  const hand = async (ownerId: string, only?: string | null) => {
    const ids = targets(only);
    if (!ids.length) return;
    setStrip(null);
    advanceFrom(ids);
    const undone: Array<() => Promise<void>> = [];
    for (const id of ids) {
      const sig = inbox.items.find((s) => s.id === id);
      if (!sig) continue;
      const task = await promoteSignal(id, { title: suggestedTitle(sig), ownerId });
      if (task) {
        const entry = undoStack.pop();
        if (entry) undone.push(entry.revert);
      }
    }
    if (undone.length === 1) undoStack.push({ label: 'Handed over', revert: undone[0] });
    else if (undone.length > 1) {
      undoStack.push({
        label: `Handed over ${undone.length} signals`,
        revert: async () => {
          for (const r of undone.slice().reverse()) await r();
        },
      });
    }
  };
  const selectAllInBundle = () => {
    const rows = inbox.view === 'flat' ? visible : bundleOf(currentId)?.items ?? [];
    for (const s of rows) if (!inbox.selection.has(s.id)) toggleSelect(s.id);
  };
  const closeStrip = () => {
    setStrip(null);
    focusPending.current = true;
    if (currentId) selectInbox(currentId);
  };
  const openStrip = (kind: Strip['kind'], id: string | null = currentId) => {
    if (!id) return;
    if (id !== currentId) selectInbox(id);
    setStrip({ kind, id });
  };
  /**
   * One disposition, however it was asked for. The keys pass no row; a button
   * in a row or in the rail passes its own. Snooze from a pointer opens the
   * strip (tomorrow 09:00 is its first, highlighted chip) so the alternatives
   * stay one tap away — plain `s` keeps taking the default outright.
   */
  const act = (kind: Act, id: string) => {
    if (kind === 'dismiss') dismiss(id);
    else openStrip(kind, id);
  };

  // One document-level handler (bound once; reads the latest closure through
  // a ref) so the keys work as soon as the view is on screen — no click to
  // "arm" the list. Whatever has focus keeps its own keys: a field keeps all
  // of them, a focused control keeps its Enter and Space (↵ on "sweep" must
  // sweep, not open the detail), and an open strip owns the lot — only an
  // Escape from outside it reaches here. A modal owns the screen.
  const onKey = (e: KeyboardEvent) => {
    if (e.defaultPrevented || editorOpen || modalIsOpen()) return;
    const t = e.target as HTMLElement | null;
    const inField = !!t?.closest('input, textarea, select, [contenteditable="true"]');
    const onControl = !!t?.closest('button, a, [role="button"], [role="menuitem"], summary');
    const mod = e.metaKey || e.ctrlKey;
    if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'z' && !inField) {
      e.preventDefault();
      void undo();
      return;
    }
    if (inField || onControl) return;
    if (strip) {
      if (e.key === 'Escape') {
        e.preventDefault();
        closeStrip();
      }
      return;
    }
    if (mod && e.key.toLowerCase() === 'a') {
      e.preventDefault();
      selectAllInBundle();
      return;
    }
    if (mod || e.altKey) return;
    switch (e.key) {
      case 'j':
      case 'J':
      case 'ArrowDown':
        e.preventDefault();
        move(1, e.shiftKey);
        break;
      case 'k':
      case 'K':
      case 'ArrowUp':
        e.preventDefault();
        move(-1, e.shiftKey);
        break;
      case 't':
        e.preventDefault();
        openStrip('promote');
        break;
      case 'a':
        e.preventDefault();
        openStrip('attach');
        break;
      case 's':
        e.preventDefault();
        snooze();
        break;
      case 'S':
        e.preventDefault();
        openStrip('snooze');
        break;
      case 'e':
        e.preventDefault();
        dismiss();
        break;
      case 'd':
        e.preventDefault();
        openStrip('hand');
        break;
      case 'x':
        e.preventDefault();
        if (currentId) toggleSelect(currentId);
        break;
      case 'f':
        e.preventDefault();
        setInboxView(inbox.view === 'flat' ? 'bundled' : 'flat');
        break;
      case 'Enter':
        if (!currentId) return;
        e.preventDefault();
        setDetail((v) => !v);
        break;
      case 'Escape':
        e.preventDefault();
        if (detail) setDetail(false);
        else if (inbox.selection.size) clearSelection();
        break;
    }
  };
  const onKeyRef = useRef(onKey);
  onKeyRef.current = onKey;
  useEffect(() => {
    const fn = (e: KeyboardEvent) => onKeyRef.current(e);
    document.addEventListener('keydown', fn);
    return () => document.removeEventListener('keydown', fn);
  }, []);

  // A load that failed knows nothing about the count, so it must not be
  // dressed up as inbox zero: say the page could not be read, and offer the
  // one thing that helps.
  const failed = !!inbox.error && inbox.items.length === 0;
  const empty = !failed && inbox.count === 0 && inbox.items.length === 0 && !inbox.loading;

  /* ---- one row ---- */
  const renderRow = (s: Signal, bundle: InboxBundle<Signal> | null) => {
    const isCurrent = s.id === currentId;
    const isSelected = inbox.selection.has(s.id);
    const proj = s.projectHint ? projById.get(s.projectHint) : undefined;
    const showProject = proj && (inbox.view === 'flat' || bundle?.kind !== 'project');
    const line = excerptLine(s);
    const name = s.title || '(untitled)';
    return (
      <div key={s.id} className="inbox__item" role="listitem">
        <div
          id={`sig-${s.id}`}
          ref={(el) => {
            if (el) rowRefs.current.set(s.id, el);
            else rowRefs.current.delete(s.id);
          }}
          role="article"
          aria-labelledby={`sig-t-${s.id}`}
          aria-current={isCurrent ? 'true' : undefined}
          tabIndex={isCurrent ? 0 : -1}
          className={'inbox__row' + (isCurrent ? ' is-current' : '') + (isSelected ? ' is-selected' : '')}
          onClick={(e) => {
            if (e.shiftKey) {
              if (currentId && !inbox.selection.has(currentId)) toggleSelect(currentId);
              if (!inbox.selection.has(s.id)) toggleSelect(s.id);
            } else if (e.metaKey || e.ctrlKey) toggleSelect(s.id);
            else if (COARSE) setDetail(true); // one tap opens it; there is no second tap to wait for
            selectInbox(s.id);
          }}
          onDoubleClick={() => setDetail(true)}
        >
          <button
            type="button"
            role="checkbox"
            aria-checked={isSelected}
            aria-label={`Select ${name}`}
            tabIndex={-1}
            className="inbox__check"
            onClick={(e) => {
              e.stopPropagation();
              selectInbox(s.id);
              toggleSelect(s.id);
            }}
          >
            <span aria-hidden>{isSelected ? '✓' : ''}</span>
          </button>
          <span className="inbox__glyph" aria-hidden title={KIND_NAME[s.kind]}>
            {KIND_GLYPH[s.kind]}
          </span>
          <div className="inbox__body">
            <div className="inbox__line">
              <span id={`sig-t-${s.id}`} className="inbox__title serif">
                {name}
              </span>
              {showProject && (
                <span className="inbox__proj">
                  <span className="dot" style={{ width: 6, height: 6, background: proj.color }} />
                  {proj.name}
                </span>
              )}
              <span className="inbox__when tnum" title={new Date(s.occurredAt).toLocaleString()}>
                {whenLabel(s.occurredAt)}
              </span>
            </div>
            {line && <div className="inbox__excerpt">{line}</div>}
            <FactChips signal={s} />
          </div>
          {/* The pointer's copy of the five keys: on hover here, always there on
              touch. They stay out of the tab order — the row is the one tab
              stop, the keys are the keyboard's path, and the detail rail
              carries the same five buttons for anyone who wants to Tab to them. */}
          <div className="inbox__acts" role="group" aria-label={`Dispositions for ${name}`}>
            {ACTS.map((a) => (
              <button
                key={a.kind}
                type="button"
                tabIndex={-1}
                className={'inbox__act inbox__act--' + a.kind}
                title={`${a.full} · ${a.key}`}
                aria-label={`${a.full}: ${name}`}
                onClick={(e) => {
                  e.stopPropagation();
                  act(a.kind, s.id);
                }}
              >
                {a.label}
              </button>
            ))}
          </div>
        </div>
        {strip?.id === s.id && strip.kind === 'promote' && (
          <PromoteStrip
            signal={s}
            projectHint={bundle?.kind === 'project' ? bundle.key.slice('project:'.length) : null}
            onDone={() => {
              setStrip(null);
              advanceFrom([s.id]);
            }}
            onCancel={closeStrip}
          />
        )}
        {strip?.id === s.id && strip.kind === 'attach' && (
          <AttachStrip
            tasks={allTasks.filter((t) => t.status !== 'done' && !t.shelved)}
            onPick={(taskId) => {
              setStrip(null);
              advanceFrom([s.id]);
              void attachSignal(s.id, taskId);
            }}
            onClose={closeStrip}
          />
        )}
        {strip?.id === s.id && strip.kind === 'hand' && (
          <HandStrip people={people} selfId={user?.id ?? ''} count={targets(s.id).length} onPick={(uid) => void hand(uid, s.id)} onClose={closeStrip} />
        )}
        {strip?.id === s.id && strip.kind === 'snooze' && (
          <SnoozeStrip
            nextMeeting={nextMeetingAt(allTasks)}
            count={targets(s.id).length}
            onPick={(until) => snooze(until, s.id)}
            onClose={closeStrip}
          />
        )}
      </div>
    );
  };

  return (
    <div className={'viewbody inbox' + (detail && current ? ' has-rail' : '')}>
      <div className="inbox__main">
        <header className="inbox__head">
          <div className="inbox__head-left">
            <span className="eyebrow">Inbox</span>
            <div className="inbox__count" aria-live="polite">
              <span className="inbox__count-n serif tnum">{inbox.count}</span>
              <span className="inbox__count-l">left</span>
            </div>
          </div>
          <div className="inbox__head-right">
            {inbox.selection.size > 0 && (
              <span className="inbox__selcount">
                {inbox.selection.size} selected <kbd className="qc__key">esc</kbd>
              </span>
            )}
            <div className="inbox__modes" role="group" aria-label="View">
              <button type="button" className={'inbox__mode' + (inbox.view === 'bundled' ? ' is-on' : '')} onClick={() => setInboxView('bundled')} aria-pressed={inbox.view === 'bundled'}>
                Bundled
              </button>
              <button type="button" className={'inbox__mode' + (inbox.view === 'flat' ? ' is-on' : '')} onClick={() => setInboxView('flat')} aria-pressed={inbox.view === 'flat'}>
                Flat
              </button>
              <kbd className="qc__key inbox__mode-key" title="Toggle with f">
                f
              </kbd>
            </div>
          </div>
        </header>

        <CaptureBox onCaptured={(id) => moveTo(id)} />

        {failed ? (
          <FailedState error={inbox.error!} loading={inbox.loading} onRetry={() => void retryInbox()} />
        ) : empty ? (
          seen ? <ZeroState /> : <FirstRun />
        ) : (
          <>
            {/*
              Deliberately not an ARIA listbox. A listbox may own nothing but
              options, and a row here carries buttons (the disposition bar, the
              fact chips) and, when it is open, a strip full of fields. So the
              honest structure is a list of listitems: the row is the one tab
              stop (a roving tabindex), aria-current marks the cursor, and the
              selection is a real checkbox control rather than aria-selected on
              something that isn't an option. In bundled view each bundle is a
              labelled group with its own list inside, so the head's buttons
              sit outside the list rather than inside an option.
            */}
            <div className="inbox__list" role={inbox.view === 'flat' ? 'list' : undefined} aria-label={inbox.view === 'flat' ? 'Inbox' : undefined}>
              {inbox.view === 'flat'
                ? inbox.items.map((s) => renderRow(s, null))
                : inbox.bundles.map((b) => {
                    const folded = isFolded(b);
                    const stale = isStale(b, nowMs);
                    const headId = `bundle-${b.key.replace(/[^a-z0-9]/gi, '-')}`;
                    return (
                      <div key={b.key} role="group" aria-labelledby={headId} className={'inbox__bundle' + (folded ? ' is-folded' : '') + (stale ? ' is-stale' : '')}>
                        <div className="inbox__bundle-head">
                          <button
                            type="button"
                            className="inbox__bundle-toggle"
                            aria-expanded={!folded}
                            onClick={() => setCollapsed((c) => ({ ...c, [b.key]: !folded }))}
                          >
                            <span className="inbox__bundle-caret" aria-hidden>
                              {folded ? '▸' : '▾'}
                            </span>
                            <span className="dot" style={{ width: 8, height: 8, background: b.color ?? 'var(--tan)' }} />
                            <span id={headId} className="inbox__bundle-label">
                              {b.label}
                            </span>
                            {b.sublabel && <span className="inbox__bundle-sub">{b.sublabel}</span>}
                            <span className="inbox__bundle-n tnum">{b.items.length}</span>
                            {stale && <span className="inbox__bundle-stale">older than a week</span>}
                          </button>
                          <button type="button" className="inbox__sweep" onClick={() => sweep(b)} title="Dismiss everything in this bundle — ⌘Z brings it back">
                            {stale || folded ? `sweep all ${b.items.length}` : 'sweep'}
                          </button>
                        </div>
                        {!folded && (
                          <div className="inbox__bundle-rows" role="list" aria-labelledby={headId}>
                            {b.items.map((s) => renderRow(s, b))}
                          </div>
                        )}
                      </div>
                    );
                  })}
            </div>
            {inbox.hasMore && (
              <button type="button" className="inbox__more" onClick={() => void loadMoreInbox()} disabled={inbox.loading}>
                {inbox.loading ? 'Loading…' : `and ${inbox.remainder} more`}
              </button>
            )}
          </>
        )}

        <footer className="inbox__keys" aria-label="Keyboard">
          {(allKeys ? [...CORE_KEYS, ...MORE_KEYS] : CORE_KEYS).map(([k, l]) => (
            <span key={k} className="inbox__key">
              <kbd className="qc__key">{k}</kbd> {l}
            </span>
          ))}
          <button
            type="button"
            className="inbox__keys-more"
            aria-expanded={allKeys}
            aria-label={allKeys ? 'Hide the rest of the keys' : 'Show every key'}
            title={allKeys ? 'Fewer keys' : 'Every key'}
            onClick={() => setAllKeys((v) => !v)}
          >
            {allKeys ? 'fewer' : '?'}
          </button>
        </footer>
      </div>

      {detail && current && (
        <SignalDetail
          signal={current}
          projectName={currentProject?.name}
          clientName={currentClient?.name}
          onClose={() => setDetail(false)}
          onAct={(kind) => {
            // the sheet covers the list on a phone: step out of it so the
            // strip it opens (or the row it advances to) is the thing on screen
            setDetail(false);
            act(kind, current.id);
          }}
        />
      )}
    </div>
  );
}

/* --------------------------------------------------------------- strips */

function nextMeetingAt(tasks: { kind: string; scheduledAt: string | null; status: string }[]): Date | null {
  const now = Date.now();
  let best: number | null = null;
  for (const t of tasks) {
    if (t.kind !== 'meet' || !t.scheduledAt || t.status === 'done') continue;
    const at = Date.parse(t.scheduledAt);
    if (at > now && (best == null || at < best)) best = at;
  }
  return best == null ? null : new Date(best);
}

/** Field keys stay in the strip: typing "j" in a search must not move the cursor. */
const stopKeys = (e: React.KeyboardEvent) => e.stopPropagation();

/** a: link the signal to an existing open work item as its origin. */
function AttachStrip({
  tasks,
  onPick,
  onClose,
}: {
  tasks: { id: string; title: string; project: { name: string; color: string } | null }[];
  onPick: (taskId: string) => void;
  onClose: () => void;
}) {
  const [q, setQ] = useState('');
  const [hi, setHi] = useState(0);
  const ref = useRef<HTMLInputElement>(null);
  const uid = useId();
  const listId = `${uid}-list`;
  const optId = (i: number) => `${uid}-opt-${i}`;
  useEffect(() => {
    ref.current?.focus();
  }, []);
  const query = q.trim().toLowerCase();
  const hits = (query ? tasks.filter((t) => t.title.toLowerCase().includes(query)) : tasks).slice(0, 8);
  useEffect(() => setHi(0), [query]);
  return (
    <div className="strip strip--attach" role="group" aria-label="Attach to a work item" onKeyDown={stopKeys} onClick={(e) => e.stopPropagation()}>
      {/* a combobox, so the option the arrows land on is announced and not just tinted */}
      <input
        ref={ref}
        className="strip__search"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder="Attach to… (open work items)"
        aria-label="Search work items"
        role="combobox"
        aria-expanded={hits.length > 0}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={hits[hi] ? optId(hi) : undefined}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown') {
            e.preventDefault();
            setHi((i) => Math.min(hits.length - 1, i + 1));
          } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            setHi((i) => Math.max(0, i - 1));
          } else if (e.key === 'Enter') {
            e.preventDefault();
            if (hits[hi]) onPick(hits[hi].id);
          } else if (e.key === 'Escape') {
            e.preventDefault();
            onClose();
          }
        }}
      />
      <div id={listId} className="strip__list" role="listbox" aria-label="Matching work items">
        {hits.length === 0 && <div className="strip__empty">No open work items match.</div>}
        {hits.map((t, i) => (
          <div
            key={t.id}
            id={optId(i)}
            role="option"
            aria-selected={i === hi}
            className={'strip__opt' + (i === hi ? ' is-hi' : '')}
            onMouseEnter={() => setHi(i)}
            onClick={() => onPick(t.id)}
          >
            <span className="strip__opt-title">{t.title}</span>
            {t.project && (
              <span className="strip__opt-proj">
                <span className="dot" style={{ width: 6, height: 6, background: t.project.color }} />
                {t.project.name}
              </span>
            )}
          </div>
        ))}
      </div>
      <div className="strip__hint">
        <kbd className="qc__key">↵</kbd> attach <kbd className="qc__key">esc</kbd> close
      </div>
    </div>
  );
}

/** d: hand the signal to a teammate as their work item. A team of one has nobody to hand to. */
function HandStrip({
  people,
  selfId,
  count,
  onPick,
  onClose,
}: {
  people: { userId: string; initial: string; color: string }[];
  selfId: string;
  count: number;
  onPick: (userId: string) => void;
  onClose: () => void;
}) {
  const [q, setQ] = useState('');
  const [hi, setHi] = useState(0);
  const ref = useRef<HTMLInputElement>(null);
  const noteRef = useRef<HTMLDivElement>(null);
  const uid = useId();
  const listId = `${uid}-people`;
  const optId = (i: number) => `${uid}-person-${i}`;
  const others = people.filter((p) => p.userId !== selfId);
  useEffect(() => {
    (ref.current ?? noteRef.current)?.focus();
  }, []);
  const query = q.trim().toLowerCase();
  const hits = query ? others.filter((p) => p.initial.toLowerCase().startsWith(query)) : others;
  useEffect(() => setHi(0), [query]);
  if (others.length === 0) {
    return (
      <div className="strip strip--note" role="group" aria-label="Hand over" tabIndex={-1} ref={noteRef} onKeyDown={(e) => {
        stopKeys(e);
        if (e.key === 'Escape' || e.key === 'Enter') {
          e.preventDefault();
          onClose();
        }
      }}>
        <span className="strip__note">You're the only member — invite from the account menu.</span>
        <kbd className="qc__key">esc</kbd>
      </div>
    );
  }
  return (
    <div className="strip strip--hand" role="group" aria-label="Hand to someone" onKeyDown={stopKeys} onClick={(e) => e.stopPropagation()}>
      <input
        ref={ref}
        className="strip__search"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder={count > 1 ? `Hand ${count} signals to…` : 'Hand to…'}
        aria-label="Search people"
        role="combobox"
        aria-expanded={hits.length > 0}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={hits[hi] ? optId(hi) : undefined}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' || e.key === 'ArrowRight') {
            e.preventDefault();
            setHi((i) => Math.min(hits.length - 1, i + 1));
          } else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') {
            e.preventDefault();
            setHi((i) => Math.max(0, i - 1));
          } else if (e.key === 'Enter') {
            e.preventDefault();
            if (hits[hi]) onPick(hits[hi].userId);
          } else if (e.key === 'Escape') {
            e.preventDefault();
            onClose();
          }
        }}
      />
      <div id={listId} className="strip__people" role="listbox" aria-label="People">
        {hits.length === 0 && <div className="strip__empty">Nobody matches.</div>}
        {hits.map((p, i) => (
          <div
            key={p.userId}
            id={optId(i)}
            role="option"
            aria-selected={i === hi}
            aria-label={p.initial}
            className={'strip__person' + (i === hi ? ' is-hi' : '')}
            onMouseEnter={() => setHi(i)}
            onClick={() => onPick(p.userId)}
          >
            <span className="avatar" style={{ width: 22, height: 22, background: p.color, color: 'var(--surface)', fontSize: 10 }}>
              {p.initial}
            </span>
          </div>
        ))}
      </div>
      <div className="strip__hint">
        <kbd className="qc__key">↵</kbd> hand over <kbd className="qc__key">esc</kbd> close
      </div>
    </div>
  );
}

/** Shift+S: the snooze alternatives, one chip row; plain s never comes here. */
function SnoozeStrip({ nextMeeting, count, onPick, onClose }: { nextMeeting: Date | null; count: number; onPick: (until: Date) => void; onClose: () => void }) {
  const [hi, setHi] = useState(0);
  const [date, setDate] = useState('');
  const ref = useRef<HTMLDivElement>(null);
  const dateRef = useRef<HTMLInputElement>(null);
  const uid = useId();
  const chipId = (i: number) => `${uid}-chip-${i}`;
  useEffect(() => {
    ref.current?.focus();
  }, []);
  const opts: { label: string; at: Date | null }[] = [
    { label: 'tomorrow 9:00', at: tomorrow9am() },
    { label: nextMeeting ? `next meeting · ${untilLabel(nextMeeting)}` : 'next meeting · none scheduled', at: nextMeeting },
    { label: `next Monday · ${untilLabel(nextMonday9())}`, at: nextMonday9() },
    { label: 'pick a date', at: null },
  ];
  const pickDate = () => {
    if (!date) return;
    const [y, m, d] = date.split('-').map(Number);
    onPick(new Date(y, m - 1, d, 9, 0, 0, 0));
  };
  const choose = (i: number) => {
    if (i === 3) {
      dateRef.current?.focus();
      return;
    }
    const o = opts[i];
    if (o.at) onPick(o.at);
  };
  return (
    <div
      ref={ref}
      tabIndex={-1}
      className="strip strip--snooze"
      role="toolbar"
      aria-label={count > 1 ? `Snooze ${count} signals until` : 'Snooze until'}
      aria-activedescendant={chipId(hi)}
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        stopKeys(e);
        const inDate = e.target === dateRef.current;
        if (e.key === 'Escape') {
          e.preventDefault();
          onClose();
        } else if (e.key === 'Enter') {
          // a focused chip (or the date field's ↵) fires its own click
          if ((e.target as HTMLElement).closest('button')) return;
          e.preventDefault();
          if (inDate) pickDate();
          else choose(hi);
        } else if (!inDate && (e.key === 'ArrowRight' || e.key === 'l')) {
          e.preventDefault();
          setHi((i) => Math.min(opts.length - 1, i + 1));
        } else if (!inDate && (e.key === 'ArrowLeft' || e.key === 'h')) {
          e.preventDefault();
          setHi((i) => Math.max(0, i - 1));
        } else if (!inDate && /^[1-4]$/.test(e.key)) {
          e.preventDefault();
          choose(Number(e.key) - 1);
        }
      }}
    >
      <span className="strip__lead">{count > 1 ? `Snooze ${count} until` : 'Snooze until'}</span>
      {opts.map((o, i) =>
        i === 3 ? (
          <span key="date" className={'strip__chip strip__chip--date' + (hi === i ? ' is-hi' : '')} onMouseEnter={() => setHi(i)}>
            <kbd className="strip__num">4</kbd>
            <input
              ref={dateRef}
              id={chipId(3)}
              type="date"
              className="strip__date tnum"
              value={date}
              min={ymd(new Date())}
              onChange={(e) => setDate(e.target.value)}
              onFocus={() => setHi(3)}
              aria-label="Pick a date"
            />
            <button type="button" className="strip__chip-go" onClick={pickDate} disabled={!date} aria-label="Snooze until this date">
              ↵
            </button>
          </span>
        ) : (
          <button
            key={o.label}
            type="button"
            id={chipId(i)}
            className={'strip__chip' + (hi === i ? ' is-hi' : '')}
            disabled={!o.at}
            onMouseEnter={() => setHi(i)}
            onClick={() => choose(i)}
            tabIndex={-1}
          >
            <kbd className="strip__num">{i + 1}</kbd>
            {o.label}
          </button>
        ),
      )}
      <kbd className="qc__key strip__esc">esc</kbd>
    </div>
  );
}

/* --------------------------------------------------------------- detail */

/** Enter: the signal itself — body fetched on demand (an audited read), people, links, where the facts came from. */
function SignalDetail({
  signal,
  projectName,
  clientName,
  onClose,
  onAct,
}: {
  signal: Signal;
  projectName?: string;
  clientName?: string;
  onClose: () => void;
  onAct: (kind: Act) => void;
}) {
  const { extractFacts } = useActions();
  const [body, setBody] = useState<SignalBody | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setBody(null);
    setErr(null);
    api
      .getSignalBody(signal.id)
      .then((b) => live && setBody(b))
      .catch((e: unknown) => live && setErr(e instanceof Error ? e.message : 'Could not load the body'));
    return () => {
      live = false;
    };
  }, [signal.id]);
  const facts = isExtractedFacts(signal.extracted) ? signal.extracted : null;
  const links = facts ? facts.links : ((signal.extracted as { links?: { url: string; label?: string }[] }).links ?? []);
  const bodyText = body ? body.body : null;
  return (
    <aside className="inbox__rail" aria-label="Signal detail">
      <div className="inbox__rail-head">
        <span className="eyebrow">
          {KIND_NAME[signal.kind]} · {whenLabel(signal.occurredAt)}
        </span>
        <button type="button" className="inbox__rail-close" onClick={onClose} aria-label="Close detail">
          esc
        </button>
      </div>
      <h2 className="inbox__rail-title serif">{signal.title || '(untitled)'}</h2>
      {(projectName || clientName) && (
        <div className="inbox__rail-meta">
          {projectName}
          {clientName ? ` · ${clientName}` : ''}
        </div>
      )}
      {/* every disposition, in reach of a thumb and of the Tab key */}
      <div className="inbox__rail-acts" role="group" aria-label="Dispositions">
        {ACTS.map((a) => (
          <button key={a.kind} type="button" className={'inbox__rail-act inbox__rail-act--' + a.kind} title={a.full} onClick={() => onAct(a.kind)}>
            {a.label} <kbd className="qc__key">{a.key}</kbd>
          </button>
        ))}
      </div>

      <FactChips signal={signal} interactive />

      {signal.participants.length > 0 && (
        <section className="inbox__rail-sec">
          <span className="inbox__rail-label">People</span>
          <ul className="inbox__rail-people">
            {signal.participants.map((p) => (
              <li key={p.idx}>
                <span className="inbox__rail-role">{p.role}</span> {p.name || p.email}
                {p.name && p.email && <span className="inbox__rail-email"> {p.email}</span>}
              </li>
            ))}
          </ul>
        </section>
      )}

      {links.length > 0 && (
        <section className="inbox__rail-sec">
          <span className="inbox__rail-label">Links</span>
          <ul className="inbox__rail-links">
            {links.map((l, i) => (
              <li key={i}>
                <a href={l.url} target="_blank" rel="noreferrer noopener">
                  {l.label && l.label !== l.url ? l.label : l.url}
                </a>
              </li>
            ))}
          </ul>
        </section>
      )}

      <section className="inbox__rail-sec">
        <span className="inbox__rail-label">Body</span>
        {bodyText == null && !err && <div className="inbox__rail-muted">Loading…</div>}
        {err && <div className="inbox__rail-muted">{err}</div>}
        {bodyText != null && (bodyText ? <pre className="inbox__rail-body">{bodyText}</pre> : <div className="inbox__rail-muted">No body was captured.</div>)}
        {signal.bodyRef && (
          <div className="inbox__rail-muted">
            Source: <code>{signal.bodyRef}</code>
          </div>
        )}
      </section>

      <section className="inbox__rail-sec inbox__rail-sec--origin">
        <span className="inbox__rail-label">Facts</span>
        {facts ? (
          <div className="inbox__rail-muted">
            {facts.model === 'heuristic' ? 'Read by rules' : `Read by ${facts.model}`} · {facts.promptVersion} · {whenLabel(facts.extractedAt)}
          </div>
        ) : (
          <div className="inbox__rail-muted">Not read yet.</div>
        )}
        <button type="button" className="inbox__rail-btn" onClick={() => void extractFacts(signal.id)}>
          {facts ? 'Read again' : 'Read the facts'}
        </button>
      </section>
    </aside>
  );
}

/* --------------------------------------------------------- empty states */

/**
 * The first page could not be read. An unknown count is not zero, so this
 * says so plainly rather than showing the inbox-zero reward for a failure.
 */
function FailedState({ error, loading, onRetry }: { error: string; loading: boolean; onRetry: () => void }) {
  return (
    <div className="inbox__zero" role="alert">
      <p className="inbox__zero-title serif">The inbox didn't load.</p>
      <p className="inbox__zero-sub">{error}</p>
      <button type="button" className="inbox__rail-btn" onClick={onRetry} disabled={loading}>
        {loading ? 'Trying…' : 'Try again'}
      </button>
    </div>
  );
}

/** The reward: after real triage, the energy placement of what was just promoted rides in the slot. */
function ZeroState() {
  return (
    <div className="inbox__zero">
      <p className="inbox__zero-title serif">Inbox zero.</p>
      <p className="inbox__zero-sub">Nothing waiting on a decision. What's left is the work itself.</p>
      <div id="inbox-zero-slot" className="inbox__zero-slot">
        <InboxZero />
      </div>
    </div>
  );
}

function FirstRun() {
  return (
    <div className="inbox__first">
      <p className="inbox__first-title serif">Nothing here yet.</p>
      <p className="inbox__first-sub">Paste in what came in. Everything here leaves by one key — or one tap — as work, or not at all.</p>
      <ol className="inbox__first-ways">
        <li>
          <span className="inbox__first-n serif">1</span>
          <div>
            <b>Paste here.</b> A meeting note, an email, a link — the box above reads what it is, and files it in the inbox.
          </div>
        </li>
        <li>
          <span className="inbox__first-n serif">2</span>
          <div>
            <b>One key each.</b> <kbd className="qc__key">t</kbd> promote · <kbd className="qc__key">a</kbd> attach · <kbd className="qc__key">s</kbd> snooze ·{' '}
            <kbd className="qc__key">e</kbd> dismiss · <kbd className="qc__key">d</kbd> hand over. Every one of them is a button on the row too.
          </div>
        </li>
        <li>
          <span className="inbox__first-n serif">3</span>
          <div>
            <b>
              <kbd className="qc__key">{MOD}K</kbd> anywhere.
            </b>{' '}
            When you already know what the work is, this makes the work item straight away — past the inbox, not through it.
          </div>
        </li>
      </ol>
    </div>
  );
}
