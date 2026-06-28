import '../styles/login.css';
import { useState } from 'react';
import { useApp, useActions } from '../state/store';

export function LoginView() {
  const { linkSent, devLink } = useApp();
  const { requestMagicLink } = useActions();
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!email.trim() || busy) return;
    setBusy(true);
    await requestMagicLink(email.trim());
    setBusy(false);
  };

  return (
    <div className="login">
      <div className="login__panel">
        <div className="login__brand">
          <span className="wordmark__name" style={{ fontSize: 30 }}>
            Cadence
          </span>
          <span className="wordmark__dot" aria-hidden />
        </div>

        {linkSent ? (
          <div className="login__sent">
            <p className="login__lead serif">Check your inbox.</p>
            <p className="login__sub">
              We sent a magic link to <strong>{linkSent}</strong>. Click it to sign in — no password needed.
            </p>
            {devLink && (
              <a className="login__devlink" href={devLink}>
                Open magic link →
              </a>
            )}
            <button className="login__again" onClick={() => requestMagicLink(linkSent)}>
              Resend
            </button>
          </div>
        ) : (
          <form className="login__form" onSubmit={submit}>
            <p className="login__lead serif">Sign in to arrange your day.</p>
            <p className="login__sub">Enter your email and we'll send you a magic link.</p>
            <input
              type="email"
              className="login__input"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@example.com"
              autoFocus
              autoComplete="email"
              aria-label="Email address"
            />
            <button className="login__btn" type="submit" disabled={!email.trim() || busy}>
              {busy ? 'Sending…' : 'Send magic link'}
            </button>
            <p className="login__fine">No account yet? Signing in creates your private workspace.</p>
          </form>
        )}
      </div>
      <p className="login__foot serif">An energy-aware way to plan your work.</p>
    </div>
  );
}
