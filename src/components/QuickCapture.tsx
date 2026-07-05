import '../styles/capture.css';
import { useEffect, useRef, useState } from 'react';
import { useApp, useActions } from '../state/store';
import { KINDS, inferTask, bestSlot } from '../lib/energy';
import { clock } from '../state/selectors';

// ⌘ on Apple platforms, Ctrl elsewhere — the schedule shortcut is meta-or-ctrl.
const MOD = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl';

/**
 * Global quick-capture omnibox. Cmd/Ctrl+K opens a centered command-palette:
 * type a title, see Cadence's inferred shape + best-fit slot, and drop it into
 * the backlog (Enter) or straight onto the plan (Cmd/Ctrl+Enter) — without the
 * full task editor. A strategic spark, captured mid-flow.
 */
export function QuickCapture() {
  const { energyProfile } = useApp();
  const { quickCapture } = useActions();

  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState('');
  const [important, setImportant] = useState(false);
  const [urgent, setUrgent] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  // global hotkey: Cmd/Ctrl+K opens, Escape closes + resets
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        setOpen(true);
      } else if (e.key === 'Escape') {
        setOpen(false);
        setTitle('');
        setImportant(false);
        setUrgent(false);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  // autofocus the input each time the palette opens
  useEffect(() => {
    if (!open) return;
    const id = window.setTimeout(() => inputRef.current?.focus(), 20);
    return () => window.clearTimeout(id);
  }, [open]);

  if (!open) return null;

  const reset = () => {
    setOpen(false);
    setTitle('');
    setImportant(false);
    setUrgent(false);
  };

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
