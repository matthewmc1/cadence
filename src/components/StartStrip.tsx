import '../styles/strips.css';
import { useEffect, useRef, useState } from 'react';
import type { Task } from '../api/types';
import { useApp, useActions } from '../state/store';
import { useStripChrome } from './ProofStrip';

/**
 * The start-of-work prompt. When an item goes into focus for the first time
 * Cadence asks two things, inline and once: what done looks like (the item's
 * definition of done) and who it is for (prefilled from the project's client).
 * Enter keeps both; Escape or a click elsewhere skips, and the item is never
 * asked again — an item whose definition of done is already set is not asked
 * at all. `docked` is the fallback when the item is on no surface.
 */
export function StartStrip({ task, docked = false }: { task: Task; docked?: boolean }) {
  const { projects, clients } = useApp();
  const { acceptStart, dismissStart } = useActions();
  const project = task.projectId ? projects.find((p) => p.id === task.projectId) : undefined;
  const client = project?.clientId ? clients.find((c) => c.id === project.clientId) : undefined;

  const [definitionOfDone, setDod] = useState(task.definitionOfDone ?? '');
  const [forWhom, setForWhom] = useState(task.ask?.forWhom || client?.name || '');
  const rootRef = useRef<HTMLDivElement>(null);
  const dodRef = useRef<HTMLInputElement>(null);

  const skip = () => dismissStart(task.id);
  useEffect(() => {
    dodRef.current?.focus();
  }, []);
  useStripChrome(rootRef, skip);

  return (
    <div
      ref={rootRef}
      className={'start' + (docked ? ' start--docked' : '')}
      data-task={task.id}
      role="group"
      aria-label="Starting this item"
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === 'Escape') {
          e.preventDefault();
          skip();
        } else if (e.key === 'Enter' && !(e.target as HTMLElement).closest('button')) {
          // a focused button owns its own ↵: Enter on "Skip" must skip, not keep
          e.preventDefault();
          void acceptStart(task.id, { definitionOfDone, forWhom });
        }
      }}
    >
      <div className="start__head">
        <span className="start__eyebrow">
          <span className="start__ring" aria-hidden />
          Starting{docked ? ' · ' : ''}
          {docked && <span className="start__name serif">{task.title}</span>}
        </span>
        <span className="start__hint">asked once · optional</span>
      </div>
      <div className="start__row">
        <label className="start__field start__field--dod">
          <span className="start__label">What does done look like?</span>
          <input
            ref={dodRef}
            className="start__input"
            value={definitionOfDone}
            onChange={(e) => setDod(e.target.value)}
            placeholder="The thing that exists when this is finished"
            aria-label="What does done look like"
          />
        </label>
        <label className="start__field start__field--who">
          <span className="start__label">Who is this for?</span>
          <input
            className="start__input"
            value={forWhom}
            onChange={(e) => setForWhom(e.target.value)}
            placeholder={client ? client.name : 'A client, a teammate, you'}
            aria-label="Who is this for"
          />
        </label>
        <div className="start__actions">
          <button type="button" className="start__go" onClick={() => void acceptStart(task.id, { definitionOfDone, forWhom })}>
            Keep <kbd className="qc__key">↵</kbd>
          </button>
          <button type="button" className="start__skip" onClick={skip}>
            Skip <kbd className="qc__key">esc</kbd>
          </button>
        </div>
      </div>
    </div>
  );
}
