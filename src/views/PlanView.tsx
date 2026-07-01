import '../styles/plan.css';
import { useApp, useActions } from '../state/store';
import { KINDS, hourToY, bestSlot, fmtTime } from '../lib/energy';
import { DayCurve } from '../components/DayCurve';
import { AiSchedule } from '../components/AiSchedule';
import type { PlacedTask } from '../state/types';

const GUTTER = [
  { label: '8a', top: 37 },
  { label: '10a', top: 124 },
  { label: '12p', top: 211 },
  { label: '2p', top: 297 },
  { label: '4p', top: 384 },
  { label: '6p', top: 471 },
];
const DOW = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

function Avatars({ people }: { people?: { initial: string; color: string }[] }) {
  if (!people || people.length === 0) return null;
  return (
    <span className="plan__avatars">
      {people.slice(0, 3).map((p, i) => (
        <span key={i} className="plan__avatar" style={{ background: p.color }}>
          {p.initial}
        </span>
      ))}
    </span>
  );
}

export function PlanView() {
  const { planMode, planLabel, isThisWeek, week, month, backlog, backlogTotal, selectedBacklogId, highlightTaskId, energyProfile } = useApp();
  const { selectBacklog, placeBacklog, openEditor, planPrev, planNext, planToday, setPlanMode } = useActions();

  const selected = backlog.find((b) => b.id === selectedBacklogId) ?? null;
  const others = backlog.filter((b) => b.id !== selectedBacklogId);
  const hiddenCount = Math.max(0, backlogTotal - backlog.length);

  const primary = selected ? bestSlot(selected.kind, 0, energyProfile) : null;
  const altDay = primary && primary.dayIndex !== 4 ? 4 : 1;

  return (
    <div className="viewbody plan-shell">
      <div className="plan__subhead">
        <div className="plan__subhead-title">
          <span className="eyebrow">Plan</span>
          <h1 className="plan__title serif">Arrange the weeks ahead</h1>
        </div>
        <div className="plan__controls">
          <div className="plan__modes">
            <button className={'plan__mode' + (planMode === 'week' ? ' is-on' : '')} onClick={() => setPlanMode('week')}>
              Week
            </button>
            <button className={'plan__mode' + (planMode === 'month' ? ' is-on' : '')} onClick={() => setPlanMode('month')}>
              Month
            </button>
          </div>
          <div className="plan__weeknav">
            <button className="plan__arrow" aria-label="Previous" onClick={planPrev}>
              ‹
            </button>
            <span className="plan__weeklabel">{planLabel}</span>
            <button className="plan__arrow" aria-label="Next" onClick={planNext}>
              ›
            </button>
          </div>
          {!isThisWeek && (
            <button className="plan__todaybtn" onClick={planToday}>
              Today
            </button>
          )}
        </div>
      </div>

      <div className="plan">
        {/* backlog rail */}
        <aside className="plan__rail">
          <div className="rail__head">
            <div className="rail__title serif">Backlog</div>
            <div className="rail__count">{backlogTotal} to place</div>
          </div>
          <p className="rail__hint">Pick a task — Cadence lights the slot that fits its energy. Place it on any week.</p>

          {selected && (
            <div className="rail__selected">
              <div className="rail__sel-title">{selected.title}</div>
              <div className="rail__sel-chips">
                <span className="chip" style={{ color: '#A85F2C', background: 'var(--fill-warm)', fontWeight: 600 }}>
                  {selected.tag}
                </span>
                <span className="chip" style={{ color: 'var(--ink-2)', background: 'var(--fill)' }}>
                  ≈ {selected.effortHrs % 1 === 0 ? selected.effortHrs : selected.effortHrs.toFixed(1)} hrs
                </span>
              </div>
              {primary && (
                <div className="rail__sel-fit">
                  <span className="dot" style={{ width: 5, height: 5, background: 'var(--sage)' }} />
                  Best fit → {primary.dayName} {fmtTime(primary.hour)}
                </div>
              )}
            </div>
          )}

          <div className="rail__list">
            {others.map((b) => (
              <button key={b.id} className="rail__item" onClick={() => selectBacklog(b.id)}>
                <span className="rail__item-row">
                  <span className="dot" style={{ width: 7, height: 7, background: KINDS[b.kind].dot }} />
                  <span className="rail__item-title">{b.title}</span>
                  <span className={'rail__item-tag' + (b.urgent ? ' rail__item-tag--urgent' : b.important ? ' rail__item-tag--important' : '')}>{b.tag}</span>
                </span>
                {b.project && (
                  <span className="rail__item-proj" style={{ color: b.project.color }}>
                    <span className="dot" style={{ background: b.project.color }} />
                    {b.project.name}
                  </span>
                )}
              </button>
            ))}
            {hiddenCount > 0 && <div className="rail__more">+ {hiddenCount} more</div>}
            {backlog.length === 0 && <div className="rail__empty">Nothing waiting — your backlog is clear.</div>}
          </div>

          <AiSchedule />
        </aside>

        {/* main area */}
        {planMode === 'week' ? (
          <div className="plan__gridwrap">
            <div className="plan__grid">
              <div className="plan__gutter">
                <div className="plan__gutter-spacer" />
                <div className="plan__gutter-rail">
                  {GUTTER.map((g) => (
                    <span key={g.label} className="plan__gutter-h tnum" style={{ top: g.top }}>
                      {g.label}
                    </span>
                  ))}
                </div>
              </div>

              {week.map((day, di) => {
                const isPrimary = primary?.dayIndex === di;
                const isAlt = !!selected && altDay === di && !isPrimary;
                return (
                  <div key={day.short} className="plan__day">
                    <div className={'plan__day-head' + (day.isToday ? ' plan__day-head--today' : '')}>
                      <div className="plan__day-name">
                        <span className="serif">{day.short}</span>
                        <span className="plan__day-date">{day.date}</span>
                      </div>
                      {day.load && (
                        <span className="plan__day-load" style={{ color: day.loadColor }}>
                          {day.load}
                        </span>
                      )}
                    </div>

                    <div className="plan__col">
                      <DayCurve scale={day.scale} weekend={day.weekend} profile={energyProfile} />

                      {isPrimary && primary && selected && (
                        <button
                          className="cand cand--primary"
                          style={{ top: hourToY(primary.hour, 520) - 6 }}
                          onClick={() => placeBacklog(selected.id, di, primary.hour)}
                          title={`Place "${selected.title}" here`}
                        >
                          <div className="cand__eyebrow">BEST FIT · {fmtTime(primary.hour).replace(' AM', '').replace(' PM', '')}</div>
                          <div className="cand__title">{selected.title}</div>
                        </button>
                      )}
                      {isAlt && selected && (
                        <button
                          className="cand cand--alt"
                          style={{ top: hourToY(9, 520) - 6 }}
                          onClick={() => placeBacklog(selected.id, di, 9)}
                          title={`Place "${selected.title}" here instead`}
                        >
                          <span className="cand__alt-ring" />
                          <span className="cand__alt-label">alt · 9:00</span>
                        </button>
                      )}

                      {day.tasks.map((t, ti) => (
                        <WeekChip key={t.id + ':' + ti} t={t} weekend={day.weekend} hot={highlightTaskId === t.id} onClick={() => openEditor(t.id)} />
                      ))}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        ) : (
          <div className="plan__monthwrap">
            <div className="plan__month-head">
              {DOW.map((d) => (
                <div key={d} className="plan__month-dow">
                  {d}
                </div>
              ))}
            </div>
            <div className="plan__month">
              {month.weeks.map((row, wi) => (
                <div key={wi} className="plan__month-row">
                  {row.map((day) => (
                    <div
                      key={day.date}
                      className={
                        'plan__mday' +
                        (day.inMonth ? '' : ' plan__mday--out') +
                        (day.isToday ? ' plan__mday--today' : '') +
                        (day.weekend ? ' plan__mday--weekend' : '')
                      }
                    >
                      <div className="plan__mday-num">{day.dayNum}</div>
                      <div className="plan__mday-tasks">
                        {day.tasks.slice(0, 4).map((t, ti) => (
                          <button
                            key={t.id + ':' + ti}
                            className={'plan__mchip' + (highlightTaskId === t.id ? ' is-highlight' : '')}
                            onClick={() => openEditor(t.id)}
                            title={t.title}
                          >
                            <span className="dot" style={{ width: 6, height: 6, background: KINDS[t.kind].dot }} />
                            <span className="plan__mchip-title">{t.title}</span>
                            {t.recurring && <span className="plan__recur" aria-label="repeats">↻</span>}
                          </button>
                        ))}
                        {day.tasks.length > 4 && <div className="plan__mmore">+ {day.tasks.length - 4} more</div>}
                      </div>
                    </div>
                  ))}
                </div>
              ))}
            </div>
          </div>
        )}
      </div>

      {planMode === 'week' && (
        <div className="plan__footer">
          <span>
            <b>{week.reduce((n, d) => n + d.tasks.length, 0)}</b> on this week
          </span>
          <span className="plan__dot">·</span>
          <span style={{ color: 'var(--accent)', fontWeight: 600 }}>{backlogTotal} in backlog</span>
          <span className="plan__dot">·</span>
          <span>↻ repeats expand across every week</span>
        </div>
      )}
    </div>
  );
}

function WeekChip({ t, weekend, hot, onClick }: { t: PlacedTask; weekend: boolean; hot: boolean; onClick: () => void }) {
  const tone = KINDS[t.kind];
  return (
    <button
      className={'plan__task plan__task--click' + (hot ? ' is-highlight' : '')}
      onClick={onClick}
      style={{
        top: hourToY(t.hour, 520) - 13,
        background: weekend ? '#F3F0E9' : tone.blockBg,
        borderColor: weekend ? '#E6E0D3' : tone.blockBd,
        opacity: weekend ? 0.7 : 1,
      }}
    >
      <span className="dot" style={{ width: 6, height: 6, background: tone.dot }} />
      <span className="plan__task-title">{t.title}</span>
      {t.recurring && <span className="plan__recur" aria-label="repeats">↻</span>}
      <Avatars people={t.assignees} />
    </button>
  );
}
