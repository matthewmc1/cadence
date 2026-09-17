/**
 * Keyboard-focus helpers for modal surfaces (the task editor drawer, the
 * quick-capture palette). Kept dependency-free: a selector for tabbable
 * elements and a Tab-wrap that keeps focus inside a root while it's open.
 */

const TABBABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Tabbable descendants of `root`, in DOM order, skipping anything not rendered. */
export function tabbablesIn(root: HTMLElement | null): HTMLElement[] {
  if (!root) return [];
  return [...root.querySelectorAll<HTMLElement>(TABBABLE)].filter((el) => el.getClientRects().length > 0);
}

/**
 * Handle a Tab / Shift+Tab keydown so focus wraps within `root`. Call from a
 * keydown listener; it's a no-op for any other key. If focus has somehow
 * drifted outside the root, the next Tab pulls it back to the first/last item.
 */
export function trapTab(e: KeyboardEvent, root: HTMLElement | null): void {
  if (e.key !== 'Tab' || !root) return;
  const items = tabbablesIn(root);
  if (items.length === 0) {
    e.preventDefault();
    return;
  }
  const first = items[0];
  const last = items[items.length - 1];
  const active = document.activeElement;
  const inside = active instanceof Node && root.contains(active);
  if (e.shiftKey) {
    if (!inside || active === first) {
      e.preventDefault();
      last.focus();
    }
  } else if (!inside || active === last) {
    e.preventDefault();
    first.focus();
  }
}

/** True while any modal dialog is mounted — used to suppress global hotkeys. */
export function modalIsOpen(): boolean {
  return document.querySelector('[aria-modal="true"]') != null;
}
