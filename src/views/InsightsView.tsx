import '../styles/insights.css';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useApp, useActions } from '../state/store';
import { KINDS } from '../lib/energy';
import { Eyebrow } from '../components/primitives';
import { Heatmap } from '../components/Heatmap';
import type { DoneItem } from '../state/selectors';
import type { Output, SignalKind } from '../api/types';

/**
 * Insights answers one question: **how is the work going?**
 *
 * It leads with the work itself — what got finished (and what it produced),
 * who we are waiting on and for how long, what came in and from where, and
 * which projects and clients are moving or going quiet. The rhythm section
 * sits underneath as an *observation* drawn from real completions; it never
 * promises to schedule anything (scheduling lives inside Work now).
 */

const TIER_LABEL: Record<string, string> = { a: 'A', b: 'B', c: 'C' };

const SIGNAL_KIND_LABEL: Record<SignalKind, string> = {
  meeting: 'meetings',
  email: 'emails',
  note: 'notes',
  doc: 'documents',
  chat: 'chat',
  link: 'links',
  text: 'captured text',
};

/* --------------------------------------------------------------- formatting */

const DAY_MS = 86_400_000;

function startOfDayMs(ms: number): number {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

/** Whole calendar days between an instant and now — the same counting Signals use. */
function daysSince(iso: string, nowMs: number): number {
  return Math.max(0, Math.round((startOfDayMs(nowMs) - startOfDayMs(Date.parse(iso))) / DAY_MS));
}

function whenLabel(iso: string, nowMs: number): string {
  const n = daysSince(iso, nowMs);
  if (n === 0) return 'today';
  if (n === 1) return 'yesterday';
  if (n < 7) return `${n} days ago`;
  return new Date(iso).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' });
}

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

/* -------------------------------------------------------------------- view */

export function InsightsView() {
  const { insights, signals, recentDone, clientHealth, allTasks, projects, people, inbox, outputsByTask } = useApp();
  const { openEditor, setReflection, loadOutputs } = useActions();
  const { peak, dip, locations, heroDay, adminDay, bestWhen, topPlace, profile } = insights;
  const nowMs = Date.now();

  // Proof lives on its own endpoint, per item. Ask once for each completion we
  // show — the cache is kept current by output.* events after that.
  const asked = useRef<Set<string>>(new Set());
  useEffect(() => {
    for (const d of recentDone) {
      if (asked.current.has(d.id)) continue;
      asked.current.add(d.id);
      void loadOutputs(d.id);
    }
  }, [recentDone, loadOutputs]);

  const openItems = useMemo(() => allTasks.filter((t) => t.stage !== 'done'), [allTasks]);
  const doneCount = allTasks.length - openItems.length;

  // waiting: longest wait first; an item with no `since` stamp sorts last
  const waiting = useMemo(() => {
    const since = (v: string | null) => (v ? Date.parse(v) : Number.MAX_SAFE_INTEGER);
    return openItems
      .filter((t) => t.stage === 'waiting' || t.waitingOnPersonId != null || t.waitingOnReason.trim() !== '')
      .sort((a, b) => since(a.waitingOnSince) - since(b.waitingOnSince));
  }, [openItems]);

  const personById = useMemo(() => new Map(people.map((p) => [p.userId, p])), [people]);
  const originsById = useMemo(() => new Map(allTasks.map((t) => [t.id, t.originCount ?? 0])), [allTasks]);

  // what is sitting in the inbox, by the kind of thing it is
  const incoming = useMemo(() => {
    const by = new Map<SignalKind, number>();
    for (const s of inbox.items) by.set(s.kind, (by.get(s.kind) ?? 0) + 1);
    return [...by.entries()].map(([kind, n]) => ({ kind, n })).sort((a, b) => b.n - a.n);
  }, [inbox.items]);

  /**
   * Project movement. Totals come from every task; "just finished" is counted
   * across the recent-completions window only, so the flag means exactly what
   * the section's sub-line says it does.
   */
  const projectRows = useMemo(() => {
    const totals = new Map<string, { done: number; total: number }>();
    for (const t of allTasks) {
      if (!t.projectId) continue;
      const e = totals.get(t.projectId) ?? { done: 0, total: 0 };
      e.total++;
      if (t.stage === 'done') e.done++;
      totals.set(t.projectId, e);
    }
    // DoneItem carries the project's name, not its id — match on that
    const recent = new Map<string, number>();
    for (const d of recentDone) if (d.project) recent.set(d.project.name, (recent.get(d.project.name) ?? 0) + 1);
    return projects
      .map((p) => {
        const e = totals.get(p.id) ?? { done: 0, total: 0 };
        return { ...p, done: e.done, total: e.total, open: e.total - e.done, recent: recent.get(p.name) ?? 0 };
      })
      .filter((p) => p.total > 0)
      .sort((a, b) => b.recent - a.recent || b.open - a.open || a.name.localeCompare(b.name))
      .slice(0, 8);
  }, [projects, allTasks, recentDone]);

  const nothingDone = doneCount === 0;
  const hasAnything = allTasks.length > 0 || inbox.count > 0;

  return (
    <div className="viewbody insights">
      <div className="insights__topline">
        <Eyebrow>How the work is going</Eyebrow>
        <span className="insights__since">
          {nothingDone ? 'Nothing finished yet' : `${doneCount} finished · ${openItems.length} still open`}
        </span>
      </div>

      <h1 className="insights__title serif">
        {nothingDone ? (
          'Nothing has been finished yet.'
        ) : (
          <>
            You’ve finished{' '}
            <em>
              {doneCount} {plural(doneCount, 'thing', 'things')}
            </em>
            {recentDone[0] ? <> — the last one {whenLabel(recentDone[0].doneAt, nowMs)}.</> : '.'}
          </>
        )}
      </h1>
      {nothingDone && (
        <p className="insights__emptynote">
          This fills in as work gets finished: <em>what you shipped</em> and what it advanced, who you’re waiting on and
          for how long, what came in and from where, and which projects and clients are going quiet. Every number here is
          read off your own work — nothing is made up.
        </p>
      )}

      {hasAnything && (
        <div className="pulse" role="list" aria-label="At a glance">
          <Pulse n={doneCount} label="finished" />
          <Pulse n={openItems.length} label="still open" />
          <Pulse n={waiting.length} label="waiting on someone" warm={waiting.length > 0} />
          <Pulse n={inbox.count} label="in the inbox" />
        </div>
      )}

      {/* ------------------------------------------------------ what got done */}
      {recentDone.length > 0 && (
        <section className="shipped" aria-label="Recently finished">
          <div className="sec__head">
            <span className="sec__title">Finished</span>
            <span className="sec__sub">
              the last {recentDone.length} — with what each produced, and what it advanced
            </span>
          </div>
          <div className="shipped__list">
            {recentDone.map((d) => (
              <ShippedRow
                key={d.id}
                item={d}
                when={whenLabel(d.doneAt, nowMs)}
                outputs={outputsByTask[d.id]}
                originCount={originsById.get(d.id) ?? 0}
                onOpen={() => openEditor(d.id)}
                onReflect={(text) => setReflection(d.id, text)}
              />
            ))}
          </div>
        </section>
      )}

      {/* --------------------------------------------------- waiting on people */}
      {waiting.length > 0 && (
        <section className="waiting" aria-label="Waiting on someone">
          <div className="sec__head">
            <span className="sec__title">Waiting on someone</span>
            <span className="sec__sub">longest wait first</span>
          </div>
          <div className="waiting__list">
            {waiting.slice(0, 6).map((t) => {
              const who = t.waitingOnPersonId ? personById.get(t.waitingOnPersonId) : undefined;
              const days = t.waitingOnSince ? daysSince(t.waitingOnSince, nowMs) : null;
              return (
                <button key={t.id} className="wait" onClick={() => openEditor(t.id)}>
                  <span className="wait__who" style={{ background: who?.color ?? 'var(--line-strong)' }}>
                    {who?.initial ?? '?'}
                  </span>
                  <span className="wait__body">
                    <span className="wait__name">{t.title}</span>
                    <span className="wait__why">
                      {t.waitingOnReason || (t.project ? `on ${t.project.name}` : 'no reason recorded')}
                    </span>
                  </span>
                  <span className={'wait__age' + (days != null && days >= 7 ? ' is-long' : '')}>
                    {days == null ? 'no date' : days === 0 ? 'today' : `${days}d`}
                  </span>
                </button>
              );
            })}
          </div>
          {waiting.length > 6 && <div className="sec__more">and {waiting.length - 6} more</div>}
        </section>
      )}

      {/* ------------------------------------------------------- what came in */}
      {incoming.length > 0 && (
        <section className="incoming" aria-label="What came in">
          <div className="sec__head">
            <span className="sec__title">What came in</span>
            <span className="sec__sub">sitting in the inbox now, by the kind of thing it is</span>
          </div>
          <div className="incoming__list">
            {incoming.map((i) => (
              <span key={i.kind} className="src">
                <b>{i.n}</b> {SIGNAL_KIND_LABEL[i.kind] ?? i.kind}
              </span>
            ))}
            {inbox.remainder > 0 && <span className="src src--muted">+{inbox.remainder} older, not loaded</span>}
          </div>
        </section>
      )}

      {/* -------------------------------------------------- needs attention */}
      {signals.length > 0 && (
        <section className="signals" aria-label="Needs attention">
          <div className="sec__head">
            <span className="sec__title">Needs attention</span>
            <span className="sec__sub">work that will quietly rot if it is left alone</span>
          </div>
          <div className="signals__list">
            {signals.map((s) =>
              s.taskId ? (
                <button key={s.id} className={'signal signal--' + s.kind + ' signal--click'} onClick={() => openEditor(s.taskId!)}>
                  <span className="signal__title">{s.title}</span>
                  <span className="signal__detail">{s.detail}</span>
                </button>
              ) : (
                <div key={s.id} className={'signal signal--' + s.kind}>
                  <span className="signal__title">{s.title}</span>
                  <span className="signal__detail">{s.detail}</span>
                </div>
              ),
            )}
          </div>
        </section>
      )}

      {/* ------------------------------------------- moving and going quiet */}
      {(projectRows.length > 0 || clientHealth.length > 0) && (
        <section className="moving" aria-label="Moving and going quiet">
          <div className="sec__head">
            <span className="sec__title">Moving and going quiet</span>
            <span className="sec__sub">
              {recentDone.length
                ? `movement counted across your last ${recentDone.length} completions`
                : 'nothing finished yet, so nothing is moving'}
            </span>
          </div>

          {projectRows.length > 0 && (
            <div className="proj__list">
              {projectRows.map((p) => (
                <div key={p.id} className="proj">
                  <span className="dot" style={{ width: 7, height: 7, background: p.color }} />
                  <span className="proj__name">{p.name}</span>
                  <span className="proj__bar" aria-hidden>
                    <span style={{ width: `${Math.round((p.done / p.total) * 100)}%`, background: p.color }} />
                  </span>
                  <span className="proj__meta">
                    {p.done}/{p.total} done
                  </span>
                  <span className={'proj__flag' + (p.recent ? ' is-moving' : p.open ? ' is-quiet' : '')}>
                    {p.recent ? `${p.recent} just finished` : p.open ? 'quiet' : 'all done'}
                  </span>
                </div>
              ))}
            </div>
          )}

          {clientHealth.length > 0 && (
            <div className="clients__grid">
              {clientHealth.map((c) => (
                <div key={c.id} className={'chealth' + (c.underserved ? ' chealth--underserved' : '')}>
                  <div className="chealth__top">
                    <span className="chealth__tier" style={{ background: c.color }}>
                      {TIER_LABEL[c.tier] ?? c.tier}
                    </span>
                    <span className="chealth__name">{c.name}</span>
                    {c.kind === 'internal' && <span className="chealth__kind">internal</span>}
                  </div>
                  <div className="chealth__meta">
                    {c.projectCount} project{c.projectCount === 1 ? '' : 's'} · {c.openCount} open · {c.doneCount} finished
                  </div>
                  <div className={'chealth__touch' + (c.underserved ? ' is-warn' : '')}>
                    {c.underserved && <span className="chealth__flag">Going quiet</span>}
                    {c.daysSince == null
                      ? 'No completed work yet'
                      : c.daysSince === 0
                        ? 'Touched today'
                        : `Last touched ${c.daysSince} day${c.daysSince === 1 ? '' : 's'} ago`}
                    {c.expectedTouchDays != null && ` · aim every ${c.expectedTouchDays}d`}
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>
      )}

      {/* -------------------------------------------------------- the rhythm */}
      <section className="rhythm" aria-label="Your rhythm">
        <div className="sec__head">
          <span className="sec__title">Your rhythm</span>
          <span className="sec__sub">an observation, read off the hours you actually finish work</span>
        </div>

        {insights.total === 0 ? (
          <div className="card rhythm__empty">
            Once a handful of tasks are finished, this shows the hours and days you actually get work done — and where you
            were when you did it. It is drawn from completions only, so it stays empty until there are some.
          </div>
        ) : (
          <>
            {/* The cards below say outright when the profile is still the
                default; the lead must not contradict them by stating that
                default as a settled fact about this person. */}
            <p className="rhythm__lead">
              {profile.learned ? <>You finish most of your work on </> : <>Too little finished to know your rhythm — the starting assumption is </>}
              <em>{bestWhen}</em>
              {locations.length > 0 && (
                <>
                  , usually at <em>{topPlace}</em>
                </>
              )}
              .
            </p>

            <div className="insights__grid">
              <section className="card heat">
                <div className="card__title">When work gets finished</div>
                <div className="card__sub">Completed work, by hour and day</div>
                <div className="heat__chart">
                  <Heatmap grid={insights.grid} hours={insights.hours} />
                </div>
                <div className="heat__legend">
                  less
                  <span className="heat__sw" style={{ background: '#ECE6DA' }} />
                  <span className="heat__sw" style={{ background: '#E0C09C' }} />
                  <span className="heat__sw" style={{ background: '#D0935C' }} />
                  <span className="heat__sw" style={{ background: '#C2743D' }} />
                  more
                </div>
              </section>

              <div className="insights__col">
                <div className="insights__pair">
                  <section className="card stat">
                    <div className="stat__eyebrow" style={{ color: 'var(--accent)' }}>
                      WHEN YOU FINISH MOST
                    </div>
                    <div className="stat__big serif">{peak.label}</div>
                    <p className="stat__body">
                      {profile.learned
                        ? `${peak.share}% of your finished work lands in this window.`
                        : `A starting assumption, not yet your own — ${profile.samples} weekday ${plural(profile.samples, 'completion', 'completions')} recorded so far.`}
                    </p>
                  </section>
                  <section className="card stat">
                    <div className="stat__eyebrow" style={{ color: 'var(--slate)' }}>
                      QUIETEST STRETCH
                    </div>
                    <div className="stat__big serif">{dip.label}</div>
                    <p className="stat__body">
                      {profile.learned
                        ? 'Less of your finished work lands here than anywhere else in the day.'
                        : 'A starting assumption, until more weekday completions land.'}
                    </p>
                  </section>
                </div>

                {locations.length > 0 && (
                  <section className="card where">
                    <div className="where__head">
                      <div className="card__title">Where work gets done</div>
                      <div className="card__sub" style={{ margin: 0 }}>
                        all time
                      </div>
                    </div>
                    <div className="where__bar">
                      {locations.map((l) => (
                        <span key={l.key} style={{ width: `${l.pct}%`, background: l.color }} />
                      ))}
                    </div>
                    <div className="where__legend">
                      {locations.map((l) => (
                        <span key={l.key} className="where__item">
                          <span className="sq" style={{ background: l.color }} />
                          {l.label} <b>{l.pct}%</b>
                        </span>
                      ))}
                    </div>
                  </section>
                )}

                <section className="card byday">
                  <div className="card__title" style={{ marginBottom: 4 }}>
                    By the day
                  </div>
                  <p className="byday__body">
                    {heroDay === adminDay ? (
                      <>
                        <strong>{heroDay}s</strong> carry both your deepest work and most of your admin.
                      </>
                    ) : (
                      <>
                        <strong>{heroDay}s</strong> are when you finish the most deep work; <strong>{adminDay}s</strong>{' '}
                        run admin-heavy.
                      </>
                    )}
                  </p>
                </section>
              </div>
            </div>
          </>
        )}
      </section>
    </div>
  );
}

/* --------------------------------------------------------------- fragments */

function Pulse({ n, label, warm = false }: { n: number; label: string; warm?: boolean }) {
  return (
    <div className={'pulse__tile' + (warm ? ' is-warm' : '')} role="listitem">
      <span className="pulse__n serif">{n}</span>
      <span className="pulse__label">{label}</span>
    </div>
  );
}

function ShippedRow({
  item,
  when,
  outputs,
  originCount,
  onOpen,
  onReflect,
}: {
  item: DoneItem;
  when: string;
  outputs: Output[] | undefined;
  originCount: number;
  onOpen: () => void;
  onReflect: (text: string) => void;
}) {
  const [text, setText] = useState(item.reflection);
  // don't reset an in-progress edit: only pull in external changes while the field is untouched
  const dirty = useRef(false);
  useEffect(() => {
    if (!dirty.current) setText(item.reflection);
  }, [item.reflection]);
  const commit = () => {
    dirty.current = false;
    if (text.trim() !== item.reflection.trim()) onReflect(text.trim());
  };
  const proof = outputs ?? [];

  return (
    <div className="shipped__row">
      <div className="shipped__line">
        <span className="dot" style={{ width: 7, height: 7, background: KINDS[item.kind].dot }} />
        <button className="shipped__name" onClick={onOpen} title={item.title}>
          {item.title}
        </button>
        {item.project && (
          <span className="shipped__proj" style={{ color: item.project.color }}>
            <span className="dot" style={{ width: 5, height: 5, background: item.project.color }} />
            {item.project.name}
          </span>
        )}
        {originCount > 0 && (
          <span className="shipped__from">
            from {originCount} {plural(originCount, 'signal', 'signals')}
          </span>
        )}
        <span className="shipped__when">{when}</span>
      </div>
      <div className="shipped__line shipped__line--proof">
        {proof.length > 0 && (
          <span className="shipped__proof">
            {proof.slice(0, 2).map((o) =>
              o.url ? (
                <a key={o.id} className="proof" href={o.url} target="_blank" rel="noreferrer">
                  {o.title || o.url}
                </a>
              ) : (
                <span key={o.id} className="proof proof--flat">
                  {o.title || o.kind}
                </span>
              ),
            )}
            {proof.length > 2 && <span className="proof proof--flat">+{proof.length - 2}</span>}
          </span>
        )}
        <input
          className="shipped__reflect"
          value={text}
          onChange={(e) => {
            dirty.current = true;
            setText(e.target.value);
          }}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
          }}
          placeholder="what did this advance?"
          aria-label={`What “${item.title}” advanced`}
        />
      </div>
    </div>
  );
}
