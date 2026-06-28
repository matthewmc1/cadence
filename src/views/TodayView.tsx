import '../styles/today.css';
import { useApp, useActions } from '../state/store';
import { KINDS } from '../lib/energy';
import { clock } from '../state/selectors';
import { Eyebrow, Chip } from '../components/primitives';
import { EnergyTimeline } from '../components/EnergyTimeline';

const START = 7;
const HOUR_PX = 50;
const GUTTER_HOURS = [
  { h: 7, label: '7 AM' },
  { h: 9, label: '9' },
  { h: 11, label: '11' },
  { h: 13, label: '1 PM' },
  { h: 15, label: '3' },
  { h: 17, label: '5' },
  { h: 19, label: '7' },
];
const SPINE_H = (19 - START) * HOUR_PX; // 600

export function TodayView() {
  const { today, now } = useApp();
  const { toggleToday, openCreate } = useActions();

  const past = today.filter((t) => t.status === 'past');
  const hero = today.find((t) => t.hero);
  const upcoming = today.filter((t) => t.status === 'upcoming');

  return (
    <div className="viewbody today">
      <div className="today__head">
        <div>
          <Eyebrow>Today</Eyebrow>
          <h1 className="today__title serif">Arranged around your energy</h1>
        </div>
        <span className="today__loc">
          <span className="now-pill__ring" />
          {now.navLabel} · {now.dayLabel}
        </span>
      </div>

        <div className="today__grid">
          {/* energy spine */}
          <div className="today__spine" style={{ height: SPINE_H }} aria-hidden>
            <div className="today__gutter">
              {GUTTER_HOURS.map((g) => (
                <span key={g.h} className="today__hour tnum" style={{ top: (g.h - START) * HOUR_PX - 6 }}>
                  {g.label}
                </span>
              ))}
            </div>
            <EnergyTimeline height={SPINE_H + 24} />
          </div>

          {/* the day's stream */}
          <div className="today__stream">
            {past.map((t) => (
              <button
                key={t.id}
                className="today__past"
                onClick={() => toggleToday(t.id)}
                title="Mark as not done"
                aria-pressed={t.done ?? true}
              >
                <span className="check check--done" aria-hidden>
                  ✓
                </span>
                <span className="today__past-title">{t.title}</span>
                <span className="today__past-time tnum">{t.timeLabel}</span>
              </button>
            ))}

            <div className="today__now">
              <span className="today__now-pill tnum">NOW · {now.time}</span>
              <span className="today__now-rule" />
            </div>

            {today.length === 0 && (
              <div className="today__empty">
                <p className="today__empty-title serif">Your day is open.</p>
                <p className="today__empty-sub">Add a task and Cadence places it in the right window for your energy.</p>
                <button className="btn-primary" onClick={openCreate}>
                  + New task
                </button>
              </div>
            )}

            {hero && (
              <article className={'hero' + (hero.done ? ' hero--done' : '')}>
                <div className="hero__top">
                  <span className="hero__window">
                    {hero.timeLabel}
                    {hero.end != null ? ` – ${clock(hero.end)}` : ''} · {KINDS[hero.kind].label.toUpperCase()} WINDOW
                  </span>
                  <span className="hero__protected">
                    <span className="dot" style={{ width: 6, height: 6, background: 'var(--sage)' }} />
                    Protected
                  </span>
                </div>
                <h2 className="hero__title serif">{hero.title}</h2>
                <div className="hero__chips">
                  <Chip variant="warm" strong>
                    {KINDS[hero.kind].label}
                  </Chip>
                  {hero.effortLabel && <Chip variant="neutral">{hero.effortLabel}</Chip>}
                  {hero.place && <Chip variant="neutral">{hero.place} · at your desk</Chip>}
                </div>
                <div className="hero__note">
                  Your strongest window of the day. Cadence is holding notifications until{' '}
                  <strong>{hero.end != null ? clock(hero.end) : '11:00'}</strong>.
                </div>
                <div className="hero__actions">
                  <button
                    className={'hero__start' + (hero.done ? ' is-done' : '')}
                    onClick={() => toggleToday(hero.id)}
                  >
                    <span className="check" aria-hidden>
                      {hero.done ? '✓' : ''}
                    </span>
                    {hero.done ? 'Completed' : 'Start focus'}
                  </button>
                </div>
              </article>
            )}

            <div className="today__later">Later today</div>
            <ul className="today__list">
              {upcoming.map((t) => {
                const dot = t.dot ?? KINDS[t.kind].dot;
                return (
                  <li key={t.id}>
                    <button
                      className={'upcard' + (t.ghost ? ' upcard--ghost' : '') + (t.done ? ' upcard--done' : '')}
                      onClick={() => toggleToday(t.id)}
                      title="Mark done"
                      aria-pressed={t.done ?? false}
                    >
                      <span className="dot" style={{ width: 8, height: 8, background: dot }} />
                      <span className="upcard__body">
                        <span className="upcard__title">{t.title}</span>
                        {t.rationale && <span className="upcard__sub">{t.rationale}</span>}
                      </span>
                      <span className="upcard__time tnum">{t.timeLabel}</span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </div>

          {/* rationale rail */}
          {today.length > 0 && (
            <aside className="today__rail">
              <p className="today__quote serif">
                “Cadence arranges your day around your energy — deep work in your peak window,
                lighter work in the dips.”
              </p>
              <p className="today__quote-by">— how Cadence plans</p>
            </aside>
          )}
        </div>
    </div>
  );
}
