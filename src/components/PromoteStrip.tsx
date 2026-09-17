import { useEffect, useMemo, useRef, useState } from 'react';
import type { Signal, Task } from '../api/types';
import { isExtractedFacts } from '../api/types';
import { useApp, useActions } from '../state/store';
import { ymd } from '../lib/time';

const NEW_REQ = '__new__';

/** The output a signal of this kind most often wants — a prefill, never a demand. */
export function defaultOutput(signal: Signal): string {
  const x = signal.extracted;
  const firstLink = isExtractedFacts(x) ? x.links?.[0] : (x as { links?: { label?: string; url?: string }[] }).links?.[0];
  switch (signal.kind) {
    case 'email':
      return 'a reply to this thread';
    case 'meeting':
      return 'notes from this meeting';
    case 'chat':
      return 'a reply in the thread';
    case 'link':
    case 'doc':
      return firstLink ? (firstLink.label && firstLink.label !== firstLink.url ? firstLink.label : firstLink.url ?? '') : 'the linked doc';
    default:
      return '';
  }
}

/** The extracted deadline the model was surest about, as a local yyyy-mm-dd; '' when none. */
export function strongestDeadline(signal: Signal): string {
  const x = signal.extracted;
  if (!isExtractedFacts(x)) return '';
  const best = [...(x.deadlines ?? [])].filter((d) => d && d.at).sort((a, b) => (b.confidence ?? 0) - (a.confidence ?? 0))[0];
  if (!best) return '';
  const d = new Date(best.at);
  return Number.isNaN(d.getTime()) ? '' : ymd(d);
}

export function suggestedTitle(signal: Signal): string {
  const x = signal.extracted;
  const t = isExtractedFacts(x) ? x.suggestedTitle?.trim() : '';
  return t || signal.title;
}

/**
 * The scope moment. Promotion is where a signal becomes a work item, so this
 * is where — and only where — Cadence asks what requirement it serves and
 * what it should produce. Everything is prefilled; Enter takes it all, ⇧Enter
 * takes the title alone and the item is created visibly unscoped, and Escape
 * backs out having created nothing — the same contract as every other strip,
 * so a mistaken `t` always has a way out. It sits inline under the row: no
 * modal, no second question.
 */
export function PromoteStrip({
  signal,
  projectHint,
  onDone,
  onCancel,
}: {
  signal: Signal;
  projectHint: string | null; // the bundle's project, when the row has none of its own
  onDone: (task: Task | null) => void;
  onCancel: () => void;
}) {
  const { projects, requirements, para } = useApp();
  const { promoteSignal, createRequirement } = useActions();

  const knownProject = (id: string | null | undefined) => (id && projects.some((p) => p.id === id) ? id : '');
  const [title, setTitle] = useState(() => suggestedTitle(signal));
  const [projectId, setProjectId] = useState<string>(() => knownProject(signal.projectHint) || knownProject(projectHint));
  const [reqId, setReqId] = useState<string>('');
  const [newReq, setNewReq] = useState('');
  const [output, setOutput] = useState(() => defaultOutput(signal));
  const [askBy, setAskBy] = useState(() => strongestDeadline(signal));
  const [busy, setBusy] = useState(false);
  const titleRef = useRef<HTMLInputElement>(null);
  const newReqRef = useRef<HTMLInputElement>(null);
  const reqSelectRef = useRef<HTMLSelectElement>(null);
  // set when the inline "new requirement" field is about to unmount: focus
  // must land back on the select, not fall to <body> where Enter/Escape are lost
  const refocusSelect = useRef(false);

  const openReqs = useMemo(
    () => requirements.filter((r) => r.projectId === projectId && r.status === 'open' && !r.archivedAt).sort((a, b) => a.position - b.position),
    [requirements, projectId],
  );

  // PARA filing: projects are offered under the area they serve, and the pick
  // says what the new item will inherit as its why (the project's outcome,
  // else its area's standard) — or that, unfiled, it inherits none.
  const projectGroups = useMemo(() => {
    const areaName = new Map(para.areas.map((a) => [a.id, a.name]));
    const groups = new Map<string, { label: string; items: typeof projects }>();
    for (const p of projects) {
      const label = (p.clientId && areaName.get(p.clientId)) || 'No area';
      const g = groups.get(label);
      if (g) g.items.push(p);
      else groups.set(label, { label, items: [p] });
    }
    return [...groups.values()].sort((a, b) => (a.label === 'No area' ? 1 : b.label === 'No area' ? -1 : a.label.localeCompare(b.label)));
  }, [projects, para.areas]);
  const inheritedWhy = useMemo(() => {
    const p = para.projects.find((x) => x.id === projectId);
    if (!p) return null;
    const area = p.clientId ? para.areas.find((a) => a.id === p.clientId) : undefined;
    return p.outcome.trim() || area?.standard.trim() || '';
  }, [para, projectId]);

  // the project's single open requirement is the obvious answer; several means a real choice
  const soleReq = openReqs.length === 1 ? openReqs[0].id : '';
  // Prefill once per project pick. The list of open requirements also moves
  // when one is added from this strip, and that must not undo the choice the
  // add just made (two open ⇒ no sole ⇒ a blank pick, silently unscoped).
  const prefilledFor = useRef<string | null>(null);
  useEffect(() => {
    if (prefilledFor.current === projectId) return;
    prefilledFor.current = projectId;
    setReqId(soleReq);
  }, [projectId, soleReq]);

  useEffect(() => {
    titleRef.current?.focus();
    titleRef.current?.select();
  }, []);
  useEffect(() => {
    if (reqId === NEW_REQ) {
      newReqRef.current?.focus();
    } else if (refocusSelect.current) {
      refocusSelect.current = false;
      reqSelectRef.current?.focus();
    }
  }, [reqId]);

  const submit = async (full: boolean) => {
    if (busy) return;
    const clean = title.trim() || signal.title;
    setBusy(true);
    const task = full
      ? await promoteSignal(signal.id, {
          title: clean,
          projectId: projectId || null,
          requirementId: reqId && reqId !== NEW_REQ ? reqId : null,
          expectedOutput: output.trim() || undefined,
          askBy: askBy ? `${askBy}T17:00:00Z` : null,
        })
      : await promoteSignal(signal.id, { title: clean, projectId: projectId || null });
    setBusy(false);
    onDone(task);
  };

  const addRequirement = async () => {
    const t = newReq.trim();
    if (!t || !projectId) return;
    const r = await createRequirement({ projectId, title: t });
    setNewReq('');
    refocusSelect.current = true;
    setReqId(r ? r.id : '');
  };

  // Enter anywhere promotes with the whole strip, ⇧Enter with the title alone,
  // Escape backs out and promotes nothing. All of them stop here so the list
  // never sees them — and a focused button keeps its own Enter, so ↵ on "Skip
  // scope" skips rather than promoting the full scope.
  const onKeyDown = (e: React.KeyboardEvent) => {
    e.stopPropagation(); // typing j/k in a field must not move the cursor
    if (e.key === 'Escape') {
      e.preventDefault();
      onCancel();
    } else if (e.key === 'Enter' && !(e.target as HTMLElement).closest('button')) {
      e.preventDefault();
      void submit(!e.shiftKey);
    }
  };

  const unscoped = !reqId || reqId === NEW_REQ;

  return (
    <div className="promote" role="group" aria-label="Promote to a work item" onKeyDown={onKeyDown} onClick={(e) => e.stopPropagation()}>
      <div className="promote__row">
        <label className="promote__field promote__field--title">
          <span className="promote__label">Work item</span>
          <input ref={titleRef} className="promote__title serif" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="What needs doing" aria-label="Title" />
        </label>
        <label className="promote__field">
          <span className="promote__label">Project</span>
          <select className="promote__select" value={projectId} onChange={(e) => setProjectId(e.target.value)} aria-label="Project">
            <option value="">No project</option>
            {projectGroups.map((g) => (
              <optgroup key={g.label} label={g.label}>
                {g.items.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </label>
        <label className={'promote__field' + (unscoped ? ' is-unscoped' : '')}>
          <span className="promote__label">
            Requirement{unscoped && <span className="promote__flag"> · unscoped</span>}
          </span>
          {reqId === NEW_REQ ? (
            <input
              ref={newReqRef}
              className="promote__input"
              value={newReq}
              onChange={(e) => setNewReq(e.target.value)}
              onKeyDown={(e) => {
                // this one field owns its Enter/Escape: add, or step back to the list of requirements
                e.stopPropagation();
                if (e.key === 'Enter') {
                  e.preventDefault();
                  void addRequirement();
                } else if (e.key === 'Escape') {
                  e.preventDefault();
                  refocusSelect.current = true;
                  setReqId('');
                }
              }}
              placeholder="New requirement · ↵ adds"
              aria-label="New requirement"
            />
          ) : (
            <select ref={reqSelectRef} className="promote__select" value={reqId} onChange={(e) => setReqId(e.target.value)} aria-label="Requirement" disabled={!projectId}>
              <option value="">{projectId ? (openReqs.length ? 'Pick a requirement' : 'No open requirements') : 'Pick a project first'}</option>
              {openReqs.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.title}
                </option>
              ))}
              {projectId && <option value={NEW_REQ}>+ new requirement</option>}
            </select>
          )}
        </label>
      </div>
      <p className={'promote__why' + (inheritedWhy ? '' : ' is-missing')}>
        <span className="promote__label">Why</span>{' '}
        {inheritedWhy
          ? inheritedWhy
          : inheritedWhy === null
            ? 'Unfiled — with no project this item inherits no why. Pick one, or it lands in Projects → Unfiled actions.'
            : 'This project has no outcome yet, so its items have no why. Add one on the Projects page.'}
      </p>
      <div className="promote__row">
        <label className="promote__field promote__field--out">
          <span className="promote__label">Expected output</span>
          <input className="promote__input" value={output} onChange={(e) => setOutput(e.target.value)} placeholder="What this should produce" aria-label="Expected output" />
        </label>
        <label className="promote__field promote__field--by">
          <span className="promote__label">Ask by</span>
          <input className="promote__input tnum" type="date" value={askBy} onChange={(e) => setAskBy(e.target.value)} aria-label="Ask by" />
        </label>
        <div className="promote__actions">
          <button type="button" className="promote__go" onClick={() => void submit(true)} disabled={busy}>
            Promote <kbd className="qc__key">↵</kbd>
          </button>
          <button type="button" className="promote__skip" onClick={() => void submit(false)} disabled={busy} title="Promote with the title only — the item stays unscoped">
            Skip scope <kbd className="qc__key">⇧↵</kbd>
          </button>
          <button type="button" className="promote__cancel" onClick={onCancel} disabled={busy} title="Back out — nothing is promoted">
            Cancel <kbd className="qc__key">esc</kbd>
          </button>
        </div>
      </div>
    </div>
  );
}
