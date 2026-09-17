import { useEffect, useRef, useState } from 'react';
import type { ExtractedDate, ExtractedPerson, Signal } from '../api/types';
import { isExtractedFacts } from '../api/types';
import { useActions } from '../state/store';
import { clock } from '../lib/time';

/** Which list of `extracted` a chip came from, and its index there. */
type ChipRef = { list: 'dates' | 'deadlines' | 'people'; i: number };

interface Chip extends ChipRef {
  key: string;
  text: string;
  confidence: number;
  corrected: boolean;
  when?: string; // resolved date, for the title
}

const SHORT_DAY = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const SHORT_MONTH = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

function whenLabel(iso: string): string | undefined {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return undefined;
  // same zero-padded 24-hour clock as the app bar, Plan and every toast
  const hm = d.getHours() || d.getMinutes() ? ` ${clock(d.getHours() + d.getMinutes() / 60)}` : '';
  return `${SHORT_DAY[d.getDay()]} ${d.getDate()} ${SHORT_MONTH[d.getMonth()]}${hm}`;
}

/** A chip's confidence tint: three steps, not a gradient, so it reads at a glance. */
function tier(c: number): 'high' | 'mid' | 'low' {
  return c >= 0.8 ? 'high' : c >= 0.5 ? 'mid' : 'low';
}

/** What a chip is, in the words the tooltip and the screen reader both use. */
function kindOf(list: ChipRef['list']): string {
  return list === 'deadlines' ? 'deadline' : list === 'dates' ? 'date' : 'person';
}

export function chipsOf(signal: Signal): Chip[] {
  const x = signal.extracted;
  if (!isExtractedFacts(x)) return [];
  const out: Chip[] = [];
  const dates = (list: 'dates' | 'deadlines', arr: ExtractedDate[]) =>
    arr.forEach((d, i) => {
      if (!d || !d.text) return;
      out.push({ list, i, key: `${list}:${i}`, text: d.text, confidence: d.confidence ?? 0, corrected: (d as { corrected?: boolean }).corrected === true, when: whenLabel(d.at) });
    });
  dates('deadlines', x.deadlines ?? []);
  dates('dates', x.dates ?? []);
  (x.people ?? []).forEach((p: ExtractedPerson, i) => {
    if (!p || !p.name) return;
    out.push({ list: 'people', i, key: `people:${i}`, text: p.name, confidence: p.confidence ?? 0, corrected: (p as { corrected?: boolean }).corrected === true, when: p.email });
  });
  return out.slice(0, 6);
}

/**
 * The facts extraction pulled out of a signal — dates, deadlines, people — as
 * chips under the row. Dotted underline says "a reading, not a record"; the
 * tint says how sure it was. Activate a chip to correct its text inline —
 * ↵ commits, esc cancels; a correction is written back to the signal's
 * `extracted` and the chip is marked so nobody mistakes it for the model's
 * own words.
 *
 * Every chip is a real button, so the correction is a keyboard path and not
 * just a mouse one. `interactive` puts them in the tab order: true in the
 * detail rail, false in a list row, where the row itself is the one tab stop
 * and the chips are reached from the rail (or by a screen reader in browse
 * mode).
 */
export function FactChips({ signal, interactive = false }: { signal: Signal; interactive?: boolean }) {
  const { setSignalFacts } = useActions();
  const [editing, setEditing] = useState<ChipRef | null>(null);
  const chips = chipsOf(signal);
  if (!chips.length) return null;

  const commit = (ref: ChipRef, text: string) => {
    setEditing(null);
    const clean = text.trim();
    const x = signal.extracted as Record<string, unknown>;
    const list = Array.isArray(x[ref.list]) ? (x[ref.list] as Record<string, unknown>[]) : [];
    const cur = list[ref.i];
    if (!cur) return;
    const field = ref.list === 'people' ? 'name' : 'text';
    if (!clean || clean === cur[field]) return;
    const next = list.slice();
    next[ref.i] = { ...cur, [field]: clean, corrected: true };
    void setSignalFacts(signal.id, { ...x, [ref.list]: next });
  };

  return (
    <span className="facts" onClick={(e) => e.stopPropagation()}>
      {chips.map((c) =>
        editing && editing.list === c.list && editing.i === c.i ? (
          <ChipEditor key={c.key} initial={c.text} onCommit={(t) => commit(c, t)} onCancel={() => setEditing(null)} />
        ) : (
          <button
            key={c.key}
            type="button"
            tabIndex={interactive ? undefined : -1}
            className={`fact fact--${c.list} fact--${tier(c.confidence)}` + (c.corrected ? ' is-corrected' : '')}
            title={[kindOf(c.list), c.when, c.corrected ? 'corrected' : `${Math.round(c.confidence * 100)}% sure`, 'edit to correct'].filter(Boolean).join(' · ')}
            aria-label={[`${kindOf(c.list)}: ${c.text}`, c.when, c.corrected ? 'corrected' : `${Math.round(c.confidence * 100)}% sure`, 'edit to correct'].filter(Boolean).join(' · ')}
            onClick={() => setEditing({ list: c.list, i: c.i })}
          >
            {c.list === 'deadlines' && (
              <span className="fact__pre" aria-hidden>
                due
              </span>
            )}
            {c.text}
            {c.corrected && (
              <span className="fact__tick" aria-hidden>
                ✓
              </span>
            )}
          </button>
        ),
      )}
    </span>
  );
}

function ChipEditor({ initial, onCommit, onCancel }: { initial: string; onCommit: (t: string) => void; onCancel: () => void }) {
  const [v, setV] = useState(initial);
  const ref = useRef<HTMLInputElement>(null);
  const done = useRef(false); // the blur that follows Enter/Escape must not commit twice
  useEffect(() => {
    ref.current?.focus();
    ref.current?.select();
  }, []);
  const finish = (fn: () => void) => {
    if (done.current) return;
    done.current = true;
    fn();
  };
  return (
    <input
      ref={ref}
      className="fact__edit"
      value={v}
      size={Math.max(6, v.length + 1)}
      onChange={(e) => setV(e.target.value)}
      onBlur={() => finish(() => onCommit(v))}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === 'Enter') {
          e.preventDefault();
          finish(() => onCommit(v));
        } else if (e.key === 'Escape') {
          e.preventDefault();
          finish(onCancel);
        }
      }}
      aria-label="Correct this fact"
    />
  );
}
