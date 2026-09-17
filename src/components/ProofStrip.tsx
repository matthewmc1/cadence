import '../styles/strips.css';
import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { Origin, OutputKind, SignalKind, Task } from '../api/types';
import { useApp, useActions } from '../state/store';
import { StartStrip } from './StartStrip';

const KIND_ORDER: OutputKind[] = ['link', 'doc', 'pr', 'email', 'decision', 'file', 'note'];
const KIND_LABEL: Record<OutputKind, string> = { link: 'Link', doc: 'Doc', pr: 'PR', email: 'Email', decision: 'Decision', file: 'File', note: 'Note' };

/** What kind of output a reference most likely is; an email origin says email whatever the locator looks like. */
export function detectOutputKind(ref: string, originKind?: SignalKind): OutputKind {
  if (originKind === 'email') return 'email';
  const u = ref.trim().toLowerCase();
  if (/github\.com\/[^/]+\/[^/]+\/pull\//.test(u) || /gitlab\.com\/.+\/merge_requests\//.test(u)) return 'pr';
  if (/docs\.google\.com|notion\.so|notion\.site|coda\.io|confluence/.test(u)) return 'doc';
  if (u.startsWith('mailto:')) return 'email';
  return 'link';
}

/**
 * The best guess at what a completed item produced, in the order the spec
 * names: the first origin signal's locator, else the item's last link, else
 * nothing — with the output's name taken from wherever the reference came from.
 */
export function guessProof(task: Pick<Task, 'links'>, origins: Origin[] | undefined): { ref: string; kind: OutputKind; title: string } {
  const origin = origins?.[0];
  if (origin && origin.snapshot.bodyRef) {
    return { ref: origin.snapshot.bodyRef, kind: detectOutputKind(origin.snapshot.bodyRef, origin.snapshot.kind), title: origin.snapshot.title };
  }
  const last = (task.links ?? []).filter((l) => l.url.trim()).slice(-1)[0];
  if (last) return { ref: last.url, kind: detectOutputKind(last.url), title: last.label || last.url };
  if (origin) return { ref: '', kind: origin.snapshot.kind === 'email' ? 'email' : 'link', title: origin.snapshot.title };
  return { ref: '', kind: 'link', title: '' };
}

/**
 * The strips' shared manners: a click anywhere outside is a skip, and focus
 * goes back to whatever had it (the tick, the card's button) when the strip
 * leaves — so a keyboard user is exactly where they were.
 */
export function useStripChrome(root: React.RefObject<HTMLElement>, onDismiss: () => void) {
  const dismiss = useRef(onDismiss);
  dismiss.current = onDismiss;
  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const onDown = (e: MouseEvent) => {
      if (root.current && e.target instanceof Node && !root.current.contains(e.target)) dismiss.current();
    };
    document.addEventListener('mousedown', onDown);
    return () => {
      document.removeEventListener('mousedown', onDown);
      if (opener && opener.isConnected && (document.activeElement === document.body || root.current?.contains(document.activeElement))) opener.focus();
    };
  }, [root]);
}

/**
 * The proof strip. Done is already saved when this mounts; it asks, once and
 * inline under the row or card that was just ticked, what came out of the
 * work — one prefilled reference and one line on what it advanced. Enter
 * records both, Escape or a click elsewhere records nothing, and either way
 * the item stays done. `docked` is the fallback when the item is on no surface.
 */
export function ProofStrip({ task, docked = false }: { task: Task; docked?: boolean }) {
  const { originsByTask, requirements } = useApp();
  const { loadOrigins, acceptProof, dismissProof } = useActions();
  const origins = originsByTask[task.id];
  const requirement = task.requirementId ? requirements.find((r) => r.id === task.requirementId) : undefined;

  const initial = guessProof(task, origins);
  const [kind, setKind] = useState<OutputKind>(initial.kind);
  const [ref, setRef] = useState(initial.ref);
  const [reflection, setReflection] = useState(task.reflection || requirement?.title || '');
  const touched = useRef(false); // the user edited the reference: origins arriving later must not overwrite it
  const rootRef = useRef<HTMLDivElement>(null);
  const whyRef = useRef<HTMLInputElement>(null);

  // the origin (and so the best reference) is fetched on demand
  useEffect(() => {
    if (origins == null) void loadOrigins(task.id);
  }, [task.id, origins, loadOrigins]);
  useEffect(() => {
    if (touched.current || !origins) return;
    const g = guessProof(task, origins);
    setKind(g.kind);
    setRef(g.ref);
  }, [origins, task]);

  useEffect(() => {
    whyRef.current?.focus();
    whyRef.current?.select();
  }, []);
  useStripChrome(rootRef, dismissProof);

  const accept = () => {
    const g = guessProof(task, origins);
    void acceptProof(task.id, { kind, ref, title: ref.trim() === g.ref.trim() ? g.title : '', reflection });
  };

  return (
    <div
      ref={rootRef}
      className={'proof' + (docked ? ' proof--docked' : '')}
      data-task={task.id}
      role="group"
      aria-label="What came out of it"
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        e.stopPropagation(); // the list's keys (j/k, t, e…) stay out of the fields
        if (e.key === 'Escape') {
          e.preventDefault();
          dismissProof();
        } else if (e.key === 'Enter' && !(e.target as HTMLElement).closest('button')) {
          // a focused button owns its own ↵: Enter on "Skip" must skip, not record
          e.preventDefault();
          accept();
        }
      }}
    >
      <div className="proof__head">
        <span className="proof__eyebrow">
          <span className="check check--done proof__tick" aria-hidden>
            ✓
          </span>
          Done{docked ? ' · ' : ''}
          {docked && <span className="proof__name serif">{task.title}</span>}
        </span>
        <span className="proof__hint">optional · never blocks</span>
      </div>
      <div className="proof__row">
        <select
          className="proof__kind"
          value={kind}
          onChange={(e) => {
            touched.current = true;
            setKind(e.target.value as OutputKind);
          }}
          aria-label="Output kind"
        >
          {KIND_ORDER.map((k) => (
            <option key={k} value={k}>
              {KIND_LABEL[k]}
            </option>
          ))}
        </select>
        <input
          className="proof__ref"
          value={ref}
          onChange={(e) => {
            touched.current = true;
            setRef(e.target.value);
            if (!e.target.value.trim()) return;
            setKind(detectOutputKind(e.target.value, origins?.[0]?.snapshot.kind));
          }}
          placeholder="Where it lives — a link, PR, doc, or thread"
          aria-label="Output reference"
          spellCheck={false}
        />
      </div>
      <div className="proof__row">
        <input
          ref={whyRef}
          className="proof__why"
          value={reflection}
          onChange={(e) => setReflection(e.target.value)}
          placeholder="What did this advance?"
          aria-label="What did this advance"
        />
        <div className="proof__actions">
          <button type="button" className="proof__go" onClick={accept}>
            Record <kbd className="qc__key">↵</kbd>
          </button>
          <button type="button" className="proof__skip" onClick={dismissProof}>
            Skip <kbd className="qc__key">esc</kbd>
          </button>
        </div>
      </div>
    </div>
  );
}

/**
 * Where a strip goes when its item is on no surface — a completion from the
 * editor while Work is filtered, say. It looks for an inline strip in the DOM
 * after every render that could have placed one, and docks only when none did,
 * so an item never carries two.
 */
export function ProofDock() {
  const { proofFor, startFor, view, workMode } = useApp();
  const [dock, setDock] = useState({ proof: false, start: false });
  useLayoutEffect(() => {
    const inline = (cls: string, id: string | undefined) => !!id && !!document.querySelector(`.${cls}[data-task="${CSS.escape(id)}"]:not(.${cls}--docked)`);
    setDock({ proof: !!proofFor && !inline('proof', proofFor?.id), start: !!startFor && !inline('start', startFor?.id) });
  }, [proofFor, startFor, view, workMode]);
  return (
    <div className="dock">
      {dock.start && startFor && <StartStrip task={startFor} docked />}
      {dock.proof && proofFor && <ProofStrip task={proofFor} docked />}
    </div>
  );
}
