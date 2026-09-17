import { useEffect, useRef, useState, type ReactNode } from 'react';
import type { View } from '../state/types';
import { useApp, useActions } from '../state/store';

/* Line icons for the four tabs — shown beside the label on narrow screens,
   where the words alone would crowd the bar. 16px, 1.5px stroke, currentColor. */
const ICON_ATTRS = { width: 16, height: 16, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 1.5, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const };
const ICONS: Record<Exclude<View, 'add'>, ReactNode> = {
  inbox: (
    <svg {...ICON_ATTRS} aria-hidden>
      <path d="M4 13V6.5A1.5 1.5 0 0 1 5.5 5h13A1.5 1.5 0 0 1 20 6.5V13" />
      <path d="M4 13h4.5l1.5 2.5h4l1.5-2.5H20v4.5a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 17.5V13z" />
    </svg>
  ),
  work: (
    <svg {...ICON_ATTRS} aria-hidden>
      <path d="M5 7h14M5 12h14M5 17h9" />
    </svg>
  ),
  projects: (
    <svg {...ICON_ATTRS} aria-hidden>
      <path d="M4 7.5A1.5 1.5 0 0 1 5.5 6h4l2 2.5h7A1.5 1.5 0 0 1 20 10v7.5a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 17.5z" />
    </svg>
  ),
  insights: (
    <svg {...ICON_ATTRS} aria-hidden>
      <path d="M4 18l5-6 4 3 7-9" />
    </svg>
  ),
};

/**
 * Four: Inbox · Work · Projects · Insights. Work is the home surface — today's
 * strip, the "when" control and the register all sit on it, so there is no
 * separate Plan tab to visit. Projects is where work is organised (PARA):
 * what each project is for, by when, and the actions that serve it.
 */
const TABS: { view: Exclude<View, 'add'>; label: string }[] = [
  { view: 'inbox', label: 'Inbox' },
  { view: 'work', label: 'Work' },
  { view: 'projects', label: 'Projects' },
  { view: 'insights', label: 'Insights' },
];

export function AppHeader() {
  const { view, now, connection, editorOpen, editorMode, user, inbox } = useApp();
  const { go, openCreate, signOut, openTokens } = useActions();
  // the brand goes where the app lands: the inbox while it has items, else Work
  const home: View = inbox.count > 0 ? 'inbox' : 'work';
  const [menu, setMenu] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!menu) return;
    const onDoc = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setMenu(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [menu]);

  return (
    <header className="appbar">
      <div className="appbar__inner">
        <button className="appbar__brand" onClick={() => go(home)} aria-label="Cadence — home">
          <span className="wordmark__name" style={{ fontSize: 22 }}>
            Cadence
          </span>
          <span className="wordmark__dot" aria-hidden />
        </button>

        <nav className="appbar__nav" aria-label="Primary">
          {TABS.map((t) => {
            const active = view === t.view;
            const badge = t.view === 'inbox' && inbox.count > 0 ? inbox.count : 0;
            return (
              <button
                key={t.view}
                className={'tab' + (active ? ' tab--active' : '')}
                onClick={() => go(t.view)}
                aria-current={active ? 'page' : undefined}
                aria-label={badge ? `${t.label}, ${badge} to triage` : undefined}
              >
                <span className="tab__icon">{ICONS[t.view]}</span>
                <span className="tab__label">{t.label}</span>
                {badge > 0 && (
                  <span className="tab__badge tnum" aria-hidden>
                    {badge > 99 ? '99+' : badge}
                  </span>
                )}
              </button>
            );
          })}
        </nav>

        <div className="appbar__right">
          <span
            className={'appbar__live appbar__live--' + connection}
            title={connection === 'open' ? 'Live — realtime connected' : 'Reconnecting…'}
            role="status"
            aria-live="polite"
          >
            <span className="appbar__live-dot" />
            {connection === 'open' ? 'Live' : 'Offline'}
          </span>
          <span className="appbar__clock">
            <span className="appbar__date">{now.dayLabel}</span>
            <span className="appbar__time tnum">{now.time}</span>
          </span>
          <button
            className={'appbar__new' + (editorOpen && editorMode === 'create' ? ' appbar__new--active' : '')}
            onClick={openCreate}
          >
            <span aria-hidden>+</span> New
          </button>
          <div className="appbar__account" ref={menuRef}>
            <button
              className="appbar__avatar"
              aria-label="Your account"
              aria-haspopup="menu"
              aria-expanded={menu}
              onClick={() => setMenu((v) => !v)}
            >
              {user?.initial ?? '·'}
            </button>
            {menu && (
              <div className="appbar__menu" role="menu">
                <div className="appbar__menu-id">
                  <div className="appbar__menu-name">{user?.name}</div>
                  <div className="appbar__menu-email">{user?.email}</div>
                </div>
                <button
                  className="appbar__menu-item"
                  role="menuitem"
                  onClick={() => {
                    setMenu(false);
                    openTokens();
                  }}
                >
                  API tokens
                </button>
                <button className="appbar__menu-item" role="menuitem" onClick={signOut}>
                  Sign out
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
    </header>
  );
}
