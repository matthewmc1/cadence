/**
 * A small undo stack for inbox dispositions and promotions.
 *
 * Every one-key disposition (snooze, dismiss, attach, promote) is reversible:
 * the action that performed it pushes an entry whose `revert` puts the server
 * back the way it was (PATCH the previous stage/snoozedUntil; for a promotion
 * also delete the created task — but only while nothing has been done to that
 * item since, otherwise the revert is refused). The stack is bounded so a long
 * triage session never holds more than the last few reversals — and each
 * `revert` closes over only ids/versions, never whole signals or bodies.
 *
 * The stack itself is plain state, independent of React; the store mirrors
 * its depth/label into AppState (for the toast + Cmd+Z affordance) via the
 * `onChange` listener.
 */

/** ⌘ on Apple platforms, Ctrl elsewhere — the modifier every hint names. */
export const MOD = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl+';

/**
 * The suffix every reversible toast ends with, in the viewer's own modifier.
 * One constant: the toasts that append it, the Toast that turns it into an
 * Undo button and the key legends all read the same string.
 */
export const UNDO_HINT = ` · ${MOD}Z to undo`;

/**
 * A revert the app refuses rather than performs — the world moved on since the
 * entry was pushed (the promoted item has been worked on). Its message is the
 * toast, so it says what happened instead of "Could not undo".
 */
export class UndoBlocked extends Error {}

export interface UndoEntry {
  /** What the entry undoes, in the toast's words — "Snoozed · Q3 board deck". */
  label: string;
  /** Put the server (and local state) back. Rejects when it can't. */
  revert: () => Promise<void>;
}

export const UNDO_MAX = 10;

export interface UndoStack {
  push: (entry: UndoEntry) => void;
  /** Pop the most recent entry, or null when the stack is empty. */
  pop: () => UndoEntry | null;
  peek: () => UndoEntry | null;
  size: () => number;
  clear: () => void;
  /** Subscribe to depth/label changes; returns an unsubscribe. */
  onChange: (fn: (depth: number, label: string | null) => void) => () => void;
}

export function createUndoStack(max: number = UNDO_MAX): UndoStack {
  const entries: UndoEntry[] = [];
  const listeners = new Set<(depth: number, label: string | null) => void>();
  const notify = () => {
    const top = entries[entries.length - 1] ?? null;
    for (const fn of listeners) fn(entries.length, top ? top.label : null);
  };
  return {
    push: (entry) => {
      entries.push(entry);
      // drop the oldest rather than refuse: the newest reversal is the one that matters
      while (entries.length > max) entries.shift();
      notify();
    },
    pop: () => {
      const e = entries.pop() ?? null;
      if (e) notify();
      return e;
    },
    peek: () => entries[entries.length - 1] ?? null,
    size: () => entries.length,
    clear: () => {
      if (!entries.length) return;
      entries.length = 0;
      notify();
    },
    onChange: (fn) => {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
  };
}

/** The app-wide stack the store pushes to and `undo()` pops from. */
export const undoStack = createUndoStack();
