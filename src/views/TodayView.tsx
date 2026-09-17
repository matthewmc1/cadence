/**
 * PARKED — not routed anywhere; its only remaining caller is the parked
 * PlanView.
 *
 * Today stopped being a tab some time ago and rode at the top of Plan's week
 * mode. With Plan collapsed from the navigation, its job moved into Work:
 * what is on today is the strip at the top of WorkView (`work__today*`), the
 * item in flight is Work's Now section, and every tick there is the same
 * store completion path, so the proof strip still rides under the row.
 *
 * Kept intact (with styles/today.css) so the decision is reversible: re-route
 * PlanView and this renders exactly as it did.
 */
import '../styles/today.css';
import { useApp, useActions } from '../state/store';
import { KINDS } from '../lib/energy';
import { clock } from '../state/selectors';
import { Chip } from '../components/primitives';
import { ProofStrip } from '../components/ProofStrip';
import { StartStrip } from '../components/StartStrip';

/**
 * The day's stream — what's done, the NOW rule, the protected hero block and
 * what's later today. Today is no longer a tab: Plan renders this at the top
 * of its week mode (`compact` tightens the cards so the calendar stays close).
 * Every tick here is the store's one completion path, so the proof strip
 * rides under whichever row was just ticked; the hero's Start is the one
 * start path, with its strip under the card.
 */
export function TodayStream({ compact = false }: { compact?: boolean }) {
  const { today, now, proofFor, startFor } = useApp();
  const { toggleToday, completeTask, startTask, openCreate } = useActions();

  const past = today.filter((t) => t.status === 'past');
  const hero = today.find((t) => t.hero);
  const upcoming = today.filter((t) => t.status === 'upcoming');

  // a recurring item can appear more than once in the day; its strip shows once
  let stripShown = false;
  const stripsFor = (id: string) => {
    if (stripShown) return null;
    const proof = proofFor?.id === id ? <ProofStrip task={proofFor} /> : null;
    const start = startFor?.id === id ? <StartStrip task={startFor} /> : null;
    if (proof || start) stripShown = true;
    return (
      <>
        {start}
        {proof}
      </>
    );
  };

  return (
    <div className={'today__stream' + (compact ? ' today__stream--compact' : '')}>
      {past.map((t, i) => (
        <div key={t.id + ':' + i}>
          <button
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
          {stripsFor(t.id)}
        </div>
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
            + New
          </button>
        </div>
      )}

      {hero && (
        <>
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
              {hero.done ? (
                <button className="hero__start is-done" onClick={() => toggleToday(hero.id)} title="Mark as not done">
                  <span className="check" aria-hidden>
                    ✓
                  </span>
                  Completed
                </button>
              ) : (
                <>
                  {/* the hero is the item in focus: Start opens the start strip, Done goes through the proof strip */}
                  <button className={'hero__start' + (startFor?.id === hero.id ? ' is-on' : '')} onClick={() => void startTask(hero.id)}>
                    <span className="check" aria-hidden />
                    Start focus
                  </button>
                  <button className="hero__done" onClick={() => void completeTask(hero.id)} title="Mark done">
                    Mark done
                  </button>
                </>
              )}
            </div>
          </article>
          {stripsFor(hero.id)}
        </>
      )}

      <div className="today__later">Later today</div>
      <ul className="today__list">
        {upcoming.map((t, i) => {
          const dot = t.dot ?? KINDS[t.kind].dot;
          return (
            <li key={t.id + ':' + i}>
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
              {stripsFor(t.id)}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
