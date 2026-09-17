import { useApp, useActions } from '../state/store';
import { UNDO_HINT } from '../lib/undo';

/**
 * The one toast. A message that names something reversible ("Snoozed until
 * tomorrow 9:00 · Q3 deck · ⌘Z to undo") gets an Undo control in place of
 * the hint, so a mouse user has the same one-click way back as the keyboard.
 * A long message clamps to two lines so it never pushes Undo off the screen.
 * Polite live region: it announces, never interrupts.
 */
export function Toast() {
  const { toast, undo: undoState } = useApp();
  const { undo } = useActions();
  const undoable = !!toast && toast.endsWith(UNDO_HINT) && undoState.depth > 0;
  const text = undoable && toast ? toast.slice(0, -UNDO_HINT.length) : toast;
  return (
    <div className="toast-wrap" aria-live="polite" aria-atomic="true">
      {toast && (
        <div className="toast" role="status" key={toast}>
          <span className="toast__spark" aria-hidden>
            ✦
          </span>
          <span className="toast__msg">{text}</span>
          {undoable && (
            <button type="button" className="toast__undo" onClick={() => void undo()}>
              Undo
            </button>
          )}
        </div>
      )}
    </div>
  );
}
