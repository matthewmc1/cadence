import { catmullRom, type Point } from '../lib/curve';

/**
 * The vertical energy curve behind Today's timeline. Points and gradients are
 * ported verbatim from the design canvas so the silhouette — strong morning
 * peak, midday trough, afternoon rebound — matches exactly.
 */
const PTS: Point[] = [
  [42, 8],
  [60, 46],
  [150, 104],
  [170, 140],
  [150, 198],
  [96, 252],
  [72, 306],
  [118, 380],
  [152, 426],
  [122, 494],
  [84, 560],
  [62, 624],
  [50, 696],
];

const CURVE = catmullRom(PTS);
const AREA = CURVE + ' L30,700 L30,8 Z';

export function EnergyTimeline({ height = 700 }: { height?: number }) {
  const width = Math.round((200 * height) / 700);
  return (
    <svg
      className="energy-curve"
      width={width}
      height={height}
      viewBox="0 0 200 700"
      preserveAspectRatio="xMidYMid meet"
      aria-hidden
    >
      <defs>
        <linearGradient id="energy" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#8FA0AE" />
          <stop offset="14%" stopColor="#C2743D" />
          <stop offset="27%" stopColor="#C9803F" />
          <stop offset="40%" stopColor="#9AA6AE" />
          <stop offset="55%" stopColor="#C28F5A" />
          <stop offset="72%" stopColor="#9AA6AE" />
          <stop offset="100%" stopColor="#94A1AC" />
        </linearGradient>
        <linearGradient id="energyFill" x1="0" y1="0" x2="1" y2="0">
          <stop offset="0%" stopColor="#C2743D" stopOpacity="0.1" />
          <stop offset="100%" stopColor="#C2743D" stopOpacity="0" />
        </linearGradient>
      </defs>
      <line x1="30" y1="4" x2="30" y2="700" stroke="var(--line-spine)" strokeWidth="1" />
      <path d={AREA} fill="url(#energyFill)" />
      <path d={CURVE} fill="none" stroke="url(#energy)" strokeWidth="3" strokeLinecap="round" />
    </svg>
  );
}
