import { lerpHex, clamp } from '../lib/curve';

/**
 * "When focus happens" — completed work by hour and day, rendered from a
 * normalized intensity grid computed from real completions (see computeInsights).
 */
const DAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const HOUR_LABEL: Record<number, string> = { 7: '7a', 9: '9a', 11: '11a', 13: '1p', 15: '3p', 17: '5p', 19: '7p' };

const CW = 46, CHH = 20, GX = 6, GY = 6, PAD_L = 36, PAD_T = 22;
const LO = '#ECE6DA', HI = '#C2743D';

export function Heatmap({ grid, hours }: { grid: number[][]; hours: number[] }) {
  const W = PAD_L + 7 * CW + 6 * GX;
  const H = PAD_T + hours.length * CHH + (hours.length - 1) * GY;

  return (
    <svg
      className="heatmap"
      width="100%"
      viewBox={`0 0 ${W} ${H}`}
      role="img"
      aria-label="Completed work by hour and day. Densest on weekday late mornings."
    >
      {DAYS.map((d, ci) => (
        <text
          key={`d${ci}`}
          x={PAD_L + ci * (CW + GX) + CW / 2}
          y={14}
          textAnchor="middle"
          fontFamily="var(--sans)"
          fontSize={10.5}
          fontWeight={600}
          fill="#8A8173"
        >
          {d}
        </text>
      ))}
      {hours.map((h, ri) =>
        HOUR_LABEL[h] ? (
          <text
            key={`h${ri}`}
            x={28}
            y={PAD_T + ri * (CHH + GY) + CHH / 2 + 4}
            textAnchor="end"
            fontFamily="var(--sans)"
            fontSize={10}
            fontWeight={600}
            fill="#948A77"
          >
            {HOUR_LABEL[h]}
          </text>
        ) : null,
      )}
      {hours.map((h, ri) =>
        DAYS.map((d, ci) => {
          const it = clamp(grid[ri]?.[ci] ?? 0);
          const fill = it < 0.06 ? '#EFEAE1' : lerpHex(LO, HI, it);
          return (
            <rect key={`c${ri}-${ci}`} x={PAD_L + ci * (CW + GX)} y={PAD_T + ri * (CHH + GY)} width={CW} height={CHH} rx={4} fill={fill}>
              <title>{`${d} ${HOUR_LABEL[h] ?? h + ':00'} · ${Math.round(it * 100)}% of peak`}</title>
            </rect>
          );
        }),
      )}
    </svg>
  );
}
