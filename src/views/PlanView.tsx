/**
 * PARKED — not routed anywhere.
 *
 * Plan was collapsed from the navigation on the product owner's call: managing
 * the plan had grown more complex than doing the work, so the app is three tabs
 * now (Inbox · Work · Insights) and scheduling lives inside Work as a
 * lightweight "when" — today's strip at the top of Work, and a per-item
 * Today / Tomorrow / This week / Pick a date control on the row.
 *
 * The file (and styles/plan.css) is kept intact so the decision is reversible:
 * the week/month grid, the backlog rail and the drag-to-place interaction all
 * still work. To bring it back: add 'plan' to the View union in state/types.ts,
 * route it in App.tsx and put the tab back in components/AppHeader.tsx.
 */
import '../styles/plan.css';
import { useEffect, useRef, useState } from 'react';
import { useApp, useActions } from '../state/store';
import { KINDS, hourToY, bestSlot, fmtTime } from '../lib/energy';
import { clock } from '../state/selectors';
import { DayCurve } from '../components/DayCurve';
import { AiSchedule } from '../components/AiSchedule';
import { ProjectPicker } from '../components/ProjectPicker';
import { TodayStream } from './TodayView';
import type { PlacedTask } from '../state/types';

const DOW = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

/* ---- week-grid geometry (must match hourToY: (hour-7)/12 * height) ---- */
const COL_H = 520;
const H_START = 7; // 7am — top of the column
const H_END = 19; // 7pm — bottom
const H_SPAN = H_END - H_START;

const GUTTER = [7, 9, 11, 13, 15, 17, 19].map((h) => ({ h, top: hourToY(h, COL_H) }));

const clamp = (n: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, n));
/** Column-relative pixel y → decimal hour. */
const yToHour = (y: number) => H_START + (clamp(y, 0, COL_H) / COL_H) * H_SPAN;
/** Snap a start hour to 15 minutes, leaving room for a minimum block. */
const snapStart = (h: number) => clamp(Math.round(h * 4) / 4, H_START, H_END - 0.5);
/** Pixel height of a block of `min` minutes (capped so a huge task still fits). */
const blockH = (min: number) => Math.max(24, (Math.min(min, 240) / 60 / H_SPAN) * COL_H);
/** Clamp a block's height so it never spills past the bottom of the column. */
const capH = (top: number, h: number) => Math.max(20, Math.min(h, COL_H - top));

interface DragPreview {
  id: string;
  title: string;
  effortMin: number;
  di: number;
  hour: number;
}

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
  const { planMode, planLabel, isThisWeek, week, month, backlog, backlogTotal, selectedBacklogId, highlightTaskId, energyProfile, today, now } = useApp();
  const todayLeft = today.filter((t) => !t.done).length;
  const { selectBacklog, placeBacklog, moveTaskToSlot, openEditor, planPrev, planNext, planToday, setPlanMode } = useActions();

  const [drag, setDrag] = useState<DragPreview | null>(null);
  const [placeHover, setPlaceHover] = useState<{ di: number; hour: number } | null>(null);
  // true for the instant after a drag commits, so the browser's synthetic click
  // doesn't ALSO place the selected backlog task on the column it landed on.
  const suppressClick = useRef(false);
  // teardown for an in-flight drag, so a mid-drag unmount can't leak listeners.
  const dragCleanup = useRef<(() => void) | null>(null);
  useEffect(() => () => dragCleanup.current?.(), []);

  const selected = backlog.find((b) => b.id === selectedBacklogId) ?? null;
  const others = backlog.filter((b) => b.id !== selectedBacklogId);
  const hiddenCount = Math.max(0, backlogTotal - backlog.length);
  const selEffort = selected ? Math.max(15, Math.round(selected.effortHrs * 60)) : 40;

  const primary = selected ? bestSlot(selected.kind, 0, energyProfile) : null;

  // Begin a pointer drag on a placed block. Below a small threshold it's a
  // click (opens the editor); past it, it reschedules to wherever it's dropped.
  const startDrag = (e: React.PointerEvent, t: PlacedTask, onOpen: () => void) => {
    if (e.button !== 0) return;
    e.preventDefault();
    // preventDefault above cancels the browser's focus-on-mousedown, so the
    // block is focused by hand before the editor opens: TaskEditor returns
    // focus to whatever was active when it mounted, and that must be this
    // block — not the rail item (or <body>) the user clicked before it.
    const block = e.currentTarget as HTMLElement;
    const grabDy = e.clientY - block.getBoundingClientRect().top;
    const st = { moved: false, x: e.clientX, y: e.clientY, cur: null as null | { di: number; hour: number } };
    const move = (ev: PointerEvent) => {
      if (!st.moved && Math.hypot(ev.clientX - st.x, ev.clientY - st.y) < 5) return;
      st.moved = true;
      const col = (document.elementFromPoint(ev.clientX, ev.clientY) as HTMLElement | null)?.closest('.plan__col') as HTMLElement | null;
      if (!col || col.dataset.day == null) return;
      const di = Number(col.dataset.day);
      const hour = snapStart(yToHour(ev.clientY - col.getBoundingClientRect().top - grabDy));
      st.cur = { di, hour };
      setDrag({ id: t.id, title: t.title, effortMin: t.effortMinutes, di, hour });
    };
    const cleanup = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      window.removeEventListener('pointercancel', cancel);
      dragCleanup.current = null;
      setDrag(null);
    };
    const up = () => {
      cleanup();
      if (st.moved && st.cur) {
        // swallow the synthetic click that follows a real drag
        suppressClick.current = true;
        setTimeout(() => (suppressClick.current = false), 0);
        moveTaskToSlot(t.id, st.cur.di, st.cur.hour);
      } else {
        block.focus();
        onOpen();
      }
    };
    const cancel = () => cleanup(); // pointercancel: abandon without committing
    dragCleanup.current = cleanup;
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
    window.addEventListener('pointercancel', cancel);
  };

  const hourAt = (el: HTMLElement, clientY: number) => snapStart(yToHour(clientY - el.getBoundingClientRect().top));

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
          <p className="rail__hint">Pick a task, then click any time on the calendar to drop it there — or drag a placed block to reschedule it.</p>

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
              <div className="rail__sel-actions">
                <ProjectPicker taskId={selected.id} projectId={selected.projectId} />
                <button className="rail__sel-edit" onClick={() => openEditor(selected.id)}>
                  Edit details
                </button>
              </div>
            </div>
          )}

          <div className="rail__list">
            {others.map((b) => (
              <div key={b.id} className="rail__item">
                <button className="rail__item-main" onClick={() => selectBacklog(b.id)}>
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
                <button
                  className="rail__item-edit"
                  onClick={() => openEditor(b.id)}
                  title="Edit details"
                  aria-label={`Edit ${b.title}`}
                >
                  ✎
                </button>
              </div>
            ))}
            {hiddenCount > 0 && <div className="rail__more">+ {hiddenCount} more</div>}
            {backlog.length === 0 && <div className="rail__empty">Nothing waiting — your backlog is clear.</div>}
          </div>

          <AiSchedule />
        </aside>

        {/* main area */}
        {planMode === 'week' ? (
          <div className="plan__gridwrap">
            {/* Today first — the day's stream, compact — then the week under it */}
            <section className="plan__today" aria-label="Today">
              <div className="plan__today-head">
                <div className="plan__today-title">
                  <span className="eyebrow">Today</span>
                  <h2 className="plan__today-h serif">{now.dayLabel}</h2>
                </div>
                <span className="plan__today-meta">
                  <span className="now-pill__ring" />
                  {todayLeft === 0 ? 'Nothing left today' : `${todayLeft} left today`}
                </span>
              </div>
              <TodayStream compact />
            </section>

            <div className="plan__grid">
              <div className="plan__gutter">
                <div className="plan__gutter-spacer" />
                <div className="plan__gutter-rail">
                  {GUTTER.map((g) => (
                    <span key={g.h} className="plan__gutter-h tnum" style={{ top: g.top }}>
                      {/* the gutter wants only the hour: "09:00" → "09" */}
                      {fmtTime(g.h).replace(':00', '')}
                    </span>
                  ))}
                </div>
              </div>

              {week.map((day, di) => {
                const isPrimary = primary?.dayIndex === di;
                const showPlace = !!selected && !drag && placeHover?.di === di;
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

                    <div
                      className={'plan__col' + (selected ? ' plan__col--place' : '')}
                      data-day={di}
                      onMouseMove={(e) => {
                        if (!selected || drag) return;
                        // Over the Best-fit card: no free-placement ghost, so
                        // the card stays put and can actually be clicked.
                        if ((e.target as HTMLElement).closest('.cand')) {
                          setPlaceHover(null);
                          return;
                        }
                        setPlaceHover({ di, hour: hourAt(e.currentTarget, e.clientY) });
                      }}
                      onMouseLeave={() => setPlaceHover((ph) => (ph?.di === di ? null : ph))}
                      onClick={(e) => {
                        if (suppressClick.current) {
                          suppressClick.current = false;
                          return;
                        }
                        if (!selected) return;
                        if ((e.target as HTMLElement).closest('.plan__task, .cand, .plan__ghost')) return;
                        placeBacklog(selected.id, di, hourAt(e.currentTarget, e.clientY));
                        setPlaceHover(null);
                      }}
                    >
                      <DayCurve scale={day.scale} weekend={day.weekend} profile={energyProfile} />

                      {/* stays mounted while the pointer is in the column — it
                          is the one affordance that must survive being hovered */}
                      {isPrimary && primary && selected && !drag && (
                        <button
                          className="cand cand--primary"
                          style={{ top: hourToY(primary.hour, COL_H) - 6 }}
                          onClick={(e) => {
                            e.stopPropagation();
                            placeBacklog(selected.id, di, primary.hour);
                          }}
                          title={`Place "${selected.title}" here`}
                        >
                          <div className="cand__eyebrow">BEST FIT · {fmtTime(primary.hour)}</div>
                          <div className="cand__title">{selected.title}</div>
                        </button>
                      )}

                      {/* drag drop-preview */}
                      {drag?.di === di && (
                        <div className="plan__ghost" style={{ top: hourToY(drag.hour, COL_H), height: capH(hourToY(drag.hour, COL_H), blockH(drag.effortMin)) }}>
                          <span className="plan__ghost-time tnum">{clock(drag.hour)}</span>
                          <span className="plan__ghost-title">{drag.title}</span>
                        </div>
                      )}
                      {/* click-to-place preview */}
                      {showPlace && placeHover && (
                        <div className="plan__ghost plan__ghost--place" style={{ top: hourToY(placeHover.hour, COL_H), height: capH(hourToY(placeHover.hour, COL_H), blockH(selEffort)) }}>
                          <span className="plan__ghost-time tnum">{clock(placeHover.hour)}</span>
                          <span className="plan__ghost-title">Drop “{selected.title}”</span>
                        </div>
                      )}

                      {day.tasks.map((t, ti) => (
                        <WeekBlock
                          key={t.id + ':' + ti}
                          t={t}
                          weekend={day.weekend}
                          hot={highlightTaskId === t.id}
                          dim={drag?.id === t.id}
                          onDragStart={startDrag}
                          onOpen={openEditor}
                        />
                      ))}
                    </div>
                  </div>
                );
              })}
            </div>
            <p className="plan__hint-cal">Click an empty slot to place the selected task · drag a block to move it · click a block to edit.</p>
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

function WeekBlock({
  t,
  weekend,
  hot,
  dim,
  onDragStart,
  onOpen,
}: {
  t: PlacedTask;
  weekend: boolean;
  hot: boolean;
  dim: boolean;
  onDragStart: (e: React.PointerEvent, t: PlacedTask, onOpen: () => void) => void;
  onOpen: (id: string) => void;
}) {
  const tone = KINDS[t.kind];
  const top = hourToY(t.hour, COL_H);
  const h = capH(top, blockH(t.effortMinutes));
  const endHour = t.hour + t.effortMinutes / 60;
  return (
    <div
      className={'plan__task plan__task--block plan__task--click' + (hot ? ' is-highlight' : '') + (dim ? ' plan__task--dragging' : '')}
      style={{
        top,
        height: h,
        background: weekend ? '#F3F0E9' : tone.blockBg,
        borderColor: weekend ? '#E6E0D3' : tone.blockBd,
        opacity: dim ? 0.32 : weekend ? 0.72 : 1,
      }}
      role="button"
      tabIndex={0}
      title={`${t.title} · ${clock(t.hour)}–${clock(endHour)} — drag to move, click to edit`}
      onPointerDown={(e) => onDragStart(e, t, () => onOpen(t.id))}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          onOpen(t.id);
        }
      }}
    >
      <span className="plan__task-head">
        <span className="dot" style={{ width: 6, height: 6, background: tone.dot }} />
        <span className="plan__task-title">{t.title}</span>
        {t.recurring && <span className="plan__recur" aria-label="repeats">↻</span>}
      </span>
      <span className="plan__task-foot">
        <span className="plan__task-time tnum">
          {clock(t.hour)}
          {h > 40 ? `–${clock(endHour)}` : ''}
        </span>
        {h > 52 && <Avatars people={t.assignees} />}
      </span>
    </div>
  );
}
