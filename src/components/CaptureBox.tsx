import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useActions } from '../state/store';
import type { SignalKind } from '../api/types';

const URL_ONLY = /^\s*https?:\/\/\S+\s*$/i;
const EMAIL_CUES = /^(from|subject|to|cc):\s/im;
const MEETING_CUES = /\b(attendees?|agenda|minutes|action items?|invitees?|participants|meeting notes?)\b/i;

/**
 * What the paste box will file the text as. The pill is a reading, not a
 * question: a bare URL is a link, mail headers make it an email, attendee/
 * agenda cues make it a meeting, anything else is a note.
 */
export function detectKind(text: string): SignalKind {
  if (!text.trim()) return 'note';
  if (URL_ONLY.test(text)) return 'link';
  if (EMAIL_CUES.test(text)) return 'email';
  if (MEETING_CUES.test(text)) return 'meeting';
  return 'note';
}

/**
 * An email's title is its subject, not its first header line. The server
 * falls back to the first line of the text when no title is sent, which for
 * a pasted mail is "From: …" — so pull the Subject: out here (a leading
 * Re:/Fwd: is kept; it is part of how the thread reads in the inbox).
 */
export function subjectOf(text: string): string | undefined {
  const m = /^\s*subject:\s*(.+)$/im.exec(text);
  const s = m?.[1].trim();
  return s ? s.slice(0, 120) : undefined;
}

const KIND_LABEL: Record<SignalKind, string> = {
  meeting: 'meeting',
  email: 'email',
  note: 'note',
  doc: 'doc',
  chat: 'chat',
  link: 'link',
  text: 'text',
};

/**
 * The Inbox's single capture field: one textarea that grows with what is
 * pasted into it. Cmd/Ctrl+Enter captures; Escape hands focus back to the
 * list so the one-key dispositions work again. Nothing else is asked here —
 * scope is a promotion-time question, not a capture-time one.
 */
export function CaptureBox({ onCaptured }: { onCaptured?: (id: string) => void }) {
  const { captureSignal } = useActions();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const ref = useRef<HTMLTextAreaElement>(null);
  const kind = detectKind(text);

  // grow to fit, never scroll inside: the box is the page's one text field
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = '0px';
    el.style.height = `${Math.min(el.scrollHeight, 320)}px`;
  }, [text]);

  // a global "/" jumps into the box when nothing else has the keyboard
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      if (t && t.closest('input, textarea, select, [contenteditable="true"]')) return;
      e.preventDefault();
      ref.current?.focus();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const submit = async () => {
    const clean = text.trim();
    if (!clean || busy) return;
    setBusy(true);
    const links = kind === 'link' ? [{ label: clean, url: clean }] : undefined;
    const title = kind === 'email' ? subjectOf(clean) : undefined;
    const sig = await captureSignal({ kind, text: clean, links, title });
    setBusy(false);
    if (sig) {
      setText('');
      onCaptured?.(sig.id);
    } else {
      ref.current?.focus(); // the capture failed: the text is still here, and so is the cursor
    }
  };

  // While a capture is in flight the field goes readOnly rather than disabled:
  // a disabled field loses focus to <body>, and on failure there is nothing to
  // move focus back to. readOnly keeps the caret exactly where it was.
  return (
    <div className={'capture' + (text ? ' has-text' : '') + (busy ? ' is-busy' : '')}>
      <textarea
        ref={ref}
        className="capture__field"
        rows={1}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          // Enter captures. This box does one thing, so the primary key does it —
          // Shift+Enter (and Cmd/Ctrl+Enter, for the muscle memory) adds a line
          // instead. Requiring a modifier to capture made the box look broken.
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            void submit();
          } else if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            ref.current?.blur();
          }
        }}
        placeholder="Paste a meeting note, an email, a link…"
        aria-label="Capture"
        readOnly={busy}
        aria-busy={busy || undefined}
      />
      <div className="capture__foot">
        <span className={'capture__kind capture__kind--' + kind} aria-live="polite">
          {text.trim() ? KIND_LABEL[kind] : 'note'}
        </span>
        <span className="capture__hint">
          <kbd className="qc__key">↵</kbd> capture
          <span className="capture__hint-sep" aria-hidden>
            ·
          </span>
          <kbd className="qc__key">⇧↵</kbd> new line
        </span>
        <button type="button" className="capture__go" onClick={() => void submit()} disabled={!text.trim() || busy}>
          Capture
        </button>
      </div>
    </div>
  );
}
