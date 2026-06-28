import { catmullRom, type Point } from '../lib/curve';
import { HOUR_ENERGY, hourToY } from '../lib/energy';

/**
 * The faint energy curve that sits behind each day column on Plan.
 * Ported from the canvas's `dayCurve(scale, weekend)`: weekends are damped,
 * weekdays get a peak marker at ~9:30.
 */
const TIMES = [7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19];
const SPINE_X = 8;
const MAX_B = 44;

export function DayCurve({ scale, weekend }: { scale: number; weekend: boolean }) {
  const wf = weekend ? 0.4 : 1;
  const pts: Point[] = TIMES.map((h) => {
    let en = HOUR_ENERGY[h] * scale * wf;
    if (weekend) en = Math.min(en, 0.3);
    return [Number((SPINE_X + en * MAX_B).toFixed(1)), Number(hourToY(h, 520).toFixed(1))];
  });
  const path = catmullRom(pts);
  const area = `${path} L${SPINE_X},520 L${SPINE_X},0 Z`;

  const peakX = SPINE_X + HOUR_ENERGY[10] * scale * MAX_B;
  const peakY = hourToY(9.5, 520);

  return (
    <svg
      className="day-curve"
      width="64"
      height="520"
      viewBox="0 0 64 520"
      aria-hidden
    >
      <line x1={SPINE_X} y1={0} x2={SPINE_X} y2={520} stroke="#E3DCCD" strokeWidth={1} />
      <path d={area} fill="#C2743D" fillOpacity={0.06} />
      <path
        d={path}
        fill="none"
        stroke={weekend ? '#BBB4A4' : '#CBA277'}
        strokeWidth={2}
        strokeLinecap="round"
      />
      {!weekend && <circle cx={Number(peakX.toFixed(1))} cy={Number(peakY.toFixed(1))} r={3.5} fill="#C2743D" />}
    </svg>
  );
}
