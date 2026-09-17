import { useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useApp, useActions } from '../state/store';

/**
 * Inline project reassignment — pick which project a task belongs to (or none)
 * without opening the full editor. The menu is rendered in a portal with fixed
 * positioning so it's never clipped by an overflow:auto ancestor (the Tasks
 * table's scroll wrapper, or the Plan rail). Reuses the `.cpick` styles.
 */
export function ProjectPicker({ taskId, projectId }: { taskId: string; projectId: string | null }) {
  const { projects } = useApp();
  const { setTaskProject } = useActions();
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ left: number; top?: number; bottom?: number } | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const current = projects.find((p) => p.id === projectId) ?? null;

  // The menu lives in a portal at the end of the document, so focus has to be
  // moved into it by hand (and handed back to the pill on close) — otherwise a
  // keyboard user opens it with Enter and is left behind a full-page scrim.
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) btnRef.current?.focus();
  };
  const items = () => Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('.cpick__item') ?? []);
  useLayoutEffect(() => {
    if (!open || !pos) return;
    const list = items();
    (list.find((el) => el.classList.contains('is-on')) ?? list[0])?.focus();
  }, [open, pos]);
  const onMenuKeyDown = (e: React.KeyboardEvent) => {
    const list = items();
    const i = list.indexOf(document.activeElement as HTMLButtonElement);
    const go = (n: number) => {
      e.preventDefault();
      list[(n + list.length) % list.length]?.focus();
    };
    switch (e.key) {
      case 'Escape':
        e.preventDefault();
        e.stopPropagation();
        close(true);
        break;
      case 'ArrowDown':
        go(i + 1);
        break;
      case 'ArrowUp':
        go(i - 1);
        break;
      case 'Home':
        go(0);
        break;
      case 'End':
        go(list.length - 1);
        break;
      case 'Tab':
        // tabbing out of the menu dismisses it rather than leaving the scrim up
        close(false);
        break;
    }
  };

  // Anchor the fixed menu to the button — flipping above it when there isn't
  // room below — and close on any scroll/resize (a fixed menu can't follow a
  // scrolling ancestor, so dismiss instead of letting it drift off-anchor).
  useLayoutEffect(() => {
    if (!open) return;
    const r = btnRef.current?.getBoundingClientRect();
    if (r) {
      const n = Math.max(1, projects.length + (projectId ? 1 : 0));
      const estH = n * 40 + 14; // rough menu height for the flip decision
      const below = window.innerHeight - r.bottom;
      if (below < estH && r.top > below) setPos({ left: r.left, bottom: window.innerHeight - r.top + 6 });
      else setPos({ left: r.left, top: r.bottom + 6 });
    }
    const close = () => setOpen(false);
    window.addEventListener('scroll', close, true);
    window.addEventListener('resize', close);
    return () => {
      window.removeEventListener('scroll', close, true);
      window.removeEventListener('resize', close);
    };
  }, [open, projects.length, projectId]);

  const pick = (id: string | null) => {
    if (id !== projectId) setTaskProject(taskId, id);
    close(true);
  };

  return (
    <div className="cpick" onKeyDown={(e) => { if (e.key === 'Escape' && open) { e.stopPropagation(); close(true); } }}>
      <button
        ref={btnRef}
        className={'cpick__btn' + (current ? '' : ' cpick__btn--empty')}
        onClick={(e) => {
          e.stopPropagation();
          setOpen((v) => !v);
        }}
        aria-haspopup="menu"
        aria-expanded={open}
        title="Move to project"
      >
        {current ? (
          <>
            <span className="dot" style={{ width: 7, height: 7, background: current.color }} />
            {current.name}
          </>
        ) : (
          '+ Project'
        )}
        <span className="cpick__caret">▾</span>
      </button>
      {open &&
        pos &&
        createPortal(
          <>
            <div className="cpick__scrim" onClick={(e) => { e.stopPropagation(); setOpen(false); }} />
            <div
              ref={menuRef}
              className="cpick__menu cpick__menu--fixed"
              role="menu"
              aria-label="Move to project"
              style={{ top: pos.top, bottom: pos.bottom, left: pos.left }}
              onClick={(e) => e.stopPropagation()}
              onKeyDown={onMenuKeyDown}
            >
              {projects.length === 0 && <div className="cpick__empty">No projects yet — create one from Work.</div>}
              {projects.map((p) => (
                <button
                  key={p.id}
                  role="menuitem"
                  className={'cpick__item' + (p.id === projectId ? ' is-on' : '')}
                  onClick={() => pick(p.id)}
                >
                  <span className="dot" style={{ width: 7, height: 7, background: p.color }} />
                  <span className="cpick__item-name">{p.name}</span>
                </button>
              ))}
              {projectId && (
                <button role="menuitem" className="cpick__item cpick__item--none" onClick={() => pick(null)}>
                  No project
                </button>
              )}
            </div>
          </>,
          document.body,
        )}
    </div>
  );
}
