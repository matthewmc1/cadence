import '../styles/insights.css';
import { useEffect, useRef, useState } from 'react';
import { useApp, useActions } from '../state/store';
import { KINDS } from '../lib/energy';
import { Eyebrow } from '../components/primitives';
import { Heatmap } from '../components/Heatmap';
import type { DoneItem } from '../state/selectors';

export function InsightsView() {
  const { insights, signals, recentDone } = useApp();
  const { openEditor, setReflection } = useActions();
  const { peak, dip, locations, heroDay, adminDay, bestWhen, topPlace } = insights;

  return (
    <div className="viewbody insights">
      <div className="insights__topline">
        <Eyebrow>What Cadence learned</Eyebrow>
        <span className="insights__since">{insights.total ? `From ${insights.total} completed tasks` : 'No completed tasks yet'}</span>
      </div>

      {signals.length > 0 && (
        <section className="signals" aria-label="Needs attention">
          <div className="signals__head">Needs attention</div>
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
      {insights.total === 0 ? (
        <h1 className="insights__title serif">Your patterns will appear as you finish work.</h1>
      ) : (
        <h1 className="insights__title serif">
          You do your best thinking on <em>{bestWhen}</em> — almost always at {topPlace}.
        </h1>
      )}
      {insights.total === 0 && (
        <p className="insights__emptynote">
          Complete a few tasks and Cadence will start showing <em>when</em> and <em>where</em> you do your best work — this
          chart fills in from your real completions, not made-up data.
        </p>
      )}

      <div className="insights__grid">
        {/* heatmap */}
        <section className="card heat">
          <div className="card__title">When focus happens</div>
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

        {/* right column */}
        <div className="insights__col">
          <div className="insights__pair">
            <section className="card stat">
              <div className="stat__eyebrow" style={{ color: 'var(--accent)' }}>
                PEAK WINDOW
              </div>
              <div className="stat__big serif">{peak.label}</div>
              <p className="stat__body">
                {peak.share}% of your finished work lands here. Cadence guards it.
              </p>
            </section>
            <section className="card stat">
              <div className="stat__eyebrow" style={{ color: 'var(--slate)' }}>
                THE DIP
              </div>
              <div className="stat__big serif">{dip.label}</div>
              <p className="stat__body">
                Energy bottoms out. Only light &amp; admin tasks get placed here.
              </p>
            </section>
          </div>

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

          <section className="card byday">
            <div className="card__title" style={{ marginBottom: 4 }}>
              By the day
            </div>
            <p className="byday__body">
              <strong>{adminDay}s</strong> run admin-heavy — Cadence front-loads email &amp; planning.{' '}
              <strong>{heroDay}s</strong> you ship the most, so deep work is stacked early.
            </p>
          </section>
        </div>
      </div>

      {recentDone.length > 0 && (
        <section className="shipped">
          <div className="shipped__head">
            <span className="shipped__title">Recently shipped</span>
            <span className="shipped__sub">what you finished — note what each advanced</span>
          </div>
          <div className="shipped__list">
            {recentDone.map((d) => (
              <ShippedRow key={d.id} item={d} onReflect={(text) => setReflection(d.id, text)} />
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function ShippedRow({ item, onReflect }: { item: DoneItem; onReflect: (text: string) => void }) {
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
  return (
    <div className="shipped__row">
      <span className="dot" style={{ width: 7, height: 7, background: KINDS[item.kind].dot }} />
      <span className="shipped__name">{item.title}</span>
      {item.project && (
        <span className="shipped__proj" style={{ color: item.project.color }}>
          <span className="dot" style={{ width: 5, height: 5, background: item.project.color }} />
          {item.project.name}
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
  );
}
