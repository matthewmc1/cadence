import '../styles/capture.css';
import { useEffect, useRef, useState } from 'react';
import { useApp, useActions } from '../state/store';
import { KINDS, inferTask, bestSlot } from '../lib/energy';
import { clock } from '../state/selectors';
import { modalIsOpen, trapTab } from '../lib/focus';
import { MOD } from '../lib/undo';

/**
 * Global quick-capture omnibox. Cmd/Ctrl+K opens a centered command-palette:
 * type a title, see Cadence's inferred shape + best-fit slot, and drop it into
 * the backlog (Enter) or straight onto the plan (Cmd/Ctrl+Enter) — without the
 * full task editor. A strategic spark, captured mid-flow.
 */
export function QuickCapture() {
  const { energyProfile, editorOpen } = useApp();
  const { quickCapture } = useActions();

  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState('');
  const [important, setImportant] = useState(false);
  const [urgent, setUrgent] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  const reset = () => {
    setOpen(false);
    setTitle('');
    setImportant(false);
    setUrgent(false);
  };

  // global hotkey: Cmd/Ctrl+K opens — unless another modal already owns the
  // screen (the task editor drawer per the store, or any mounted aria-modal
  // dialog as a belt-and-braces check).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key.toLowerCase() !== 'k') return;
      if (editorOpen || modalIsOpen()) return;
      e.preventDefault();
      setOpen(true);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [editorOpen]);

  // Keyboard contract while the palette is open: Escape closes it, Tab wraps
  // inside the panel, and focus that drifts out (a click on the panel's static
  // text blurs the input to <body>) is pulled back to the input. Listening at
  // document level rather than on the panel is what makes Escape work from
  // <body>. It can only ever close the palette: the hotkey refuses to open on
  // top of another dialog, so no dialog underneath is listening for Escape.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return;
      if (e.key === 'Escape') {
        e.preventDefault();
        reset();
        return;
      }
      trapTab(e, panelRef.current);
    };
    const onFocusIn = (e: FocusEvent) => {
      const panel = panelRef.current;
      if (!panel || (e.target instanceof Node && panel.contains(e.target))) return;
      inputRef.current?.focus();
    };
    // autofocus the input each time the palette opens
    const id = window.setTimeout(() => inputRef.current?.focus(), 20);
    document.addEventListener('keydown', onKey);
    document.addEventListener('focusin', onFocusIn);
    return () => {
      window.clearTimeout(id);
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('focusin', onFocusIn);
    };
    // `reset` is only stable state setters, so re-binding on `open` is enough
  }, [open]);

  if (!open) return null;

  const submit = (schedule: boolean) => {
    if (!title.trim()) return;
    quickCapture(title, { schedule, important, urgent });
    reset();
  };

  const clean = title.trim();
  const inf = clean ? inferTask(clean) : null;
  const slot = inf ? bestSlot(inf.kind, 0, energyProfile) : null;

  return (
    <div className="qc__backdrop" onMouseDown={reset}>
      <div
        ref={panelRef}
        className="qc__panel"
        role="dialog"
        aria-modal="true"
        aria-label="Quick capture"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          className="qc__input serif"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              submit(e.metaKey || e.ctrlKey);
            }
          }}
          placeholder="Capture a spark…"
          aria-label="Task title"
        />

        <div className="qc__preview">
          {inf && slot ? (
            <>
              <span className="dot" style={{ background: KINDS[inf.kind].dot }} />
              <span>
                <b>{KINDS[inf.kind].label}</b> · {inf.effortLabel} · fits {slot.dayName.slice(0, 3)} {clock(slot.hour)}
              </span>
            </>
          ) : (
            <span className="qc__preview-empty">Type a task — Cadence reads its shape as you go</span>
          )}
        </div>

        <div className="qc__pills">
          <button
            type="button"
            className={'qc__pill' + (important ? ' is-on' : '')}
            onClick={() => setImportant((v) => !v)}
            aria-pressed={important}
          >
            Important
          </button>
          <button
            type="button"
            className={'qc__pill' + (urgent ? ' is-on' : '')}
            onClick={() => setUrgent((v) => !v)}
            aria-pressed={urgent}
          >
            Urgent
          </button>
        </div>

        <div className="qc__hint">
          <span className="qc__hint-item">
            <kbd className="qc__key">↵</kbd> Capture
          </span>
          <span className="qc__hint-item">
            <kbd className="qc__key">{MOD}↵</kbd> Schedule
          </span>
          <span className="qc__hint-item qc__hint-esc">
            <kbd className="qc__key">esc</kbd> Close
          </span>
        </div>
      </div>
    </div>
  );
}
