import '../styles/board.css';
import { useState } from 'react';
import { useApp, useActions } from '../state/store';
import type { BoardColumn, BoardTask } from '../state/types';
import { KINDS } from '../lib/energy';
import { Avatar } from '../components/primitives';

const COLUMNS: { key: BoardColumn; label: string; aside?: string }[] = [
  { key: 'backlog', label: 'Backlog' },
  { key: 'week', label: 'This week', aside: 'scheduled' },
  { key: 'focus', label: 'In focus' },
  { key: 'done', label: 'Done', aside: 'where it got done' },
];

const NEXT: Record<BoardColumn, BoardColumn | null> = {
  backlog: 'week',
  week: 'focus',
  focus: 'done',
  done: null,
};
const ADVANCE_LABEL: Record<BoardColumn, string> = {
  backlog: 'Schedule',
  week: 'Start',
  focus: 'Complete',
  done: '',
};

export function BoardView() {
  const { project, projects, selectedProjectId, highlightTaskId } = useApp();
  const { moveBoard, autoScheduleRemaining, openEditor, selectProject, createProject } = useActions();
  const [newName, setNewName] = useState('');
  const [creating, setCreating] = useState(false);

  const submitProject = () => {
    const name = newName.trim();
    if (!name) return;
    createProject(name);
    setNewName('');
    setCreating(false);
  };

  const switcher = (
    <div className="board__switch">
      <span className="board__switch-label">Projects</span>
      {projects.map((p) => (
        <button
          key={p.id}
          className={'board__switch-chip' + (p.id === selectedProjectId ? ' is-on' : '')}
          onClick={() => selectProject(p.id)}
        >
          <span className="dot" style={{ width: 7, height: 7, background: p.color }} />
          {p.name}
        </button>
      ))}
      {creating ? (
        <span className="board__switch-new">
          <input
            autoFocus
            className="board__switch-input"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') submitProject();
              if (e.key === 'Escape') setCreating(false);
            }}
            placeholder="Project name"
          />
          <button className="board__switch-go" onClick={submitProject}>
            Create
          </button>
        </span>
      ) : (
        <button className="board__switch-add" onClick={() => setCreating(true)}>
          + New project
        </button>
      )}
    </div>
  );

  if (!project) {
    return (
      <div className="viewbody board">
        {switcher}
        <div className="stage__state">
          <p className="stage__state-msg">No project yet — create one to start a board.</p>
        </div>
      </div>
    );
  }

  const byCol = (c: BoardColumn) => project.tasks.filter((t) => t.column === c);
  const pct = project.total ? Math.round((project.done / project.total) * 100) : 0;

  const renderCard = (t: BoardTask) => {
    const tone = KINDS[t.kind];
    const next = NEXT[t.column];
    const hot = highlightTaskId === t.id;

    if (t.column === 'done') {
      return (
        <div key={t.id} className="bcard bcard--done bcard--click" role="button" tabIndex={0} onClick={() => openEditor(t.id)}>
          <div className="bcard__row">
            <span className="check check--done" aria-hidden>
              ✓
            </span>
            <span className="bcard__title bcard__title--strike">{t.title}</span>
          </div>
          <div className="bcard__done-meta">
            {t.doneDay} · {t.donePlace}
          </div>
        </div>
      );
    }

    if (t.column === 'focus') {
      return (
        <div
          key={t.id}
          className={'bcard bcard--focus bcard--click' + (hot ? ' is-highlight' : '')}
          role="button"
          tabIndex={0}
          onClick={() => openEditor(t.id)}
        >
          <div className="bcard__focus-top">
            <span className="bcard__focus-eyebrow">IN FOCUS · NOW</span>
            <span className="bcard__focus-until">{t.until}</span>
          </div>
          <div className="bcard__title bcard__title--strong">{t.title}</div>
          <div className="bcard__chips">
            <span className="chip chip--warm">{t.tagLabel}</span>
            {t.place && (
              <span className="bcard__place">
                <span className="bcard__ring" />
                {t.place}
              </span>
            )}
          </div>
          {next && (
            <button className="bcard__advance" onClick={(e) => { e.stopPropagation(); moveBoard(t.id, next); }}>
              {ADVANCE_LABEL[t.column]} →
            </button>
          )}
        </div>
      );
    }

    // backlog & week
    return (
      <div key={t.id} className={'bcard bcard--click' + (hot ? ' is-highlight' : '')} role="button" tabIndex={0} onClick={() => openEditor(t.id)}>
        <div className="bcard__row">
          <span className="dot" style={{ width: 7, height: 7, background: tone.dot }} />
          <span className="bcard__title">{t.title}</span>
          {next && (
            <button
              className="bcard__advance bcard__advance--inline"
              onClick={(e) => { e.stopPropagation(); moveBoard(t.id, next); }}
              title={ADVANCE_LABEL[t.column]}
              aria-label={`${ADVANCE_LABEL[t.column]} ${t.title}`}
            >
              →
            </button>
          )}
        </div>
        <div className="bcard__chips">
          {t.column === 'week' ? (
            <>
              <span className="chip chip--warm chip--strong">{t.when}</span>
              {t.place && (
                <span className="bcard__place">
                  <span className="bcard__ring" />
                  {t.place}
                </span>
              )}
            </>
          ) : (
            <span className="chip chip--neutral">{t.tagLabel}</span>
          )}
        </div>
      </div>
    );
  };

  return (
    <div className="viewbody board">
      {switcher}
      {/* project header */}
        <header className="board__header">
          <div className="board__head-row">
            <div>
              <div className="eyebrow">Project</div>
              <h1 className="board__name serif">{project.name}</h1>
              <p className="board__sub">{project.subtitle}</p>
            </div>
            <div className="board__head-right">
              <div className="board__due">
                <div className="board__due-label">DUE</div>
                <div className="board__due-date">{project.due}</div>
              </div>
              <div className="board__members">
                {project.members.map((m, i) => (
                  <span key={m.initial} style={{ marginLeft: i === 0 ? 0 : -9 }}>
                    <Avatar initial={m.initial} color={m.color} fg="var(--surface)" ring />
                  </span>
                ))}
              </div>
              <button className="pill-btn" onClick={autoScheduleRemaining}>
                <span className="spark">✦</span> Auto-schedule remaining
              </button>
            </div>
          </div>

          <div className="board__progress">
            <div className="board__track">
              <div className="board__fill" style={{ width: `${pct}%` }} />
            </div>
            <span>
              <b>{project.done}</b> of {project.total} done
            </span>
            <span className="plan__dot">·</span>
            <span>
              <b style={{ color: 'var(--accent-deep)' }}>{project.deepLeft}</b> deep-work tasks left — Cadence has slots for
              all of them
            </span>
          </div>
        </header>

        {/* columns */}
        <div className="board__cols">
          {COLUMNS.map((col) => {
            const tasks = byCol(col.key);
            return (
              <section key={col.key} className="board__col">
                <div className="board__col-head">
                  <span className="board__col-title">{col.label}</span>
                  <span className="board__col-count">{tasks.length}</span>
                  {col.aside && <span className="board__col-aside">{col.aside}</span>}
                </div>
                <div className="board__col-body">
                  {tasks.map(renderCard)}
                  {col.key === 'focus' && (
                    <div className="bcard bcard--empty">
                      Nothing else in focus.
                      <br />
                      Your morning is protected.
                    </div>
                  )}
                </div>
              </section>
            );
          })}
        </div>
    </div>
  );
}
