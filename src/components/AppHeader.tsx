import { useEffect, useRef, useState } from 'react';
import type { View } from '../state/types';
import { useApp, useActions } from '../state/store';

const TABS: { view: View; label: string }[] = [
  { view: 'today', label: 'Today' },
  { view: 'plan', label: 'Plan' },
  { view: 'board', label: 'Board' },
  { view: 'insights', label: 'Insights' },
];

export function AppHeader() {
  const { view, now, connection, editorOpen, editorMode, user } = useApp();
  const { go, openCreate, signOut } = useActions();
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
        <button className="appbar__brand" onClick={() => go('today')} aria-label="Cadence — Today">
          <span className="wordmark__name" style={{ fontSize: 22 }}>
            Cadence
          </span>
          <span className="wordmark__dot" aria-hidden />
        </button>

        <nav className="appbar__nav" aria-label="Primary">
          {TABS.map((t) => {
            const active = view === t.view;
            return (
              <button
                key={t.view}
                className={'tab' + (active ? ' tab--active' : '')}
                onClick={() => go(t.view)}
                aria-current={active ? 'page' : undefined}
              >
                {t.label}
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
            <span aria-hidden>+</span> New task
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
