import type { CSSProperties, ReactNode } from 'react';
import { KINDS, type Kind } from '../lib/energy';

/** "Cadence" italic serif wordmark with the terracotta beat dot. */
export function Wordmark({ size = 23 }: { size?: number }) {
  return (
    <span className="wordmark" aria-label="Cadence">
      <span className="wordmark__name" style={{ fontSize: size }}>
        Cadence
      </span>
      <span className="wordmark__dot" aria-hidden />
    </span>
  );
}

export function Eyebrow({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return (
    <div className="eyebrow" style={style}>
      {children}
    </div>
  );
}

export function Dot({ color, size = 8 }: { color: string; size?: number }) {
  return (
    <span
      className="dot"
      style={{ width: size, height: size, background: color }}
      aria-hidden
    />
  );
}

type ChipVariant = 'warm' | 'neutral' | 'admin' | 'sage' | 'outline' | 'dashed';

const CHIP_STYLE: Record<ChipVariant, CSSProperties> = {
  warm: { color: '#A85F2C', background: 'var(--fill-warm)' },
  neutral: { color: 'var(--ink-2)', background: 'var(--fill)' },
  admin: { color: '#41566A', background: 'var(--fill-admin)' },
  sage: { color: '#4E5E45', background: 'var(--fill-sage)' },
  outline: { color: 'var(--ink-2)', background: 'var(--paper-2)', border: '1px solid var(--line)' },
  dashed: { color: 'var(--ink-4)', border: '1px dashed var(--line-dash)' },
};

export function Chip({
  children,
  variant = 'neutral',
  strong = false,
  style,
}: {
  children: ReactNode;
  variant?: ChipVariant;
  strong?: boolean;
  style?: CSSProperties;
}) {
  return (
    <span
      className="chip"
      style={{ ...CHIP_STYLE[variant], fontWeight: strong ? 600 : 500, ...style }}
    >
      {children}
    </span>
  );
}

/** The pill that shows a task's kind (dot + label), used on cards. */
export function KindChip({ kind }: { kind: Kind }) {
  const k = KINDS[kind];
  const variant: ChipVariant =
    kind === 'deep' ? 'warm' : kind === 'admin' || kind === 'meet' ? 'admin' : kind === 'personal' ? 'sage' : 'neutral';
  return <Chip variant={variant}>{k.label}</Chip>;
}

export function Avatar({
  initial,
  color = 'var(--ink)',
  fg = 'var(--surface)',
  size = 30,
  ring = false,
}: {
  initial: string;
  color?: string;
  fg?: string;
  size?: number;
  ring?: boolean;
}) {
  return (
    <span
      className="avatar"
      style={{
        width: size,
        height: size,
        background: color,
        color: fg,
        fontSize: size * 0.4,
        border: ring ? '2px solid var(--surface)' : 'none',
      }}
    >
      {initial}
    </span>
  );
}

/** Per-view contextual top bar. Left = page context, right = controls. */
export function TopBar({ left, right }: { left?: ReactNode; right?: ReactNode }) {
  return (
    <header className="topbar">
      <div className="topbar__left">{left ?? <Wordmark />}</div>
      <div className="topbar__right">{right}</div>
    </header>
  );
}
