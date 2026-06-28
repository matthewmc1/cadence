/**
 * Geometry helpers for Cadence's energy visualisations.
 *
 * `catmullRom` and `lerpHex` are ported verbatim (behaviour-for-behaviour)
 * from the Cadence.dc.html design canvas so the rendered curves and heatmap
 * are pixel-faithful to the source.
 */

export type Point = readonly [number, number];

/**
 * Catmull-Rom spline through `pts`, emitted as an SVG path `d` string.
 * Mirrors the design canvas's `cr()` — same control-point math, same
 * 1-decimal rounding — so the shape matches exactly.
 */
export function catmullRom(pts: readonly Point[]): string {
  if (pts.length < 2) return '';
  let d = `M${pts[0][0]},${pts[0][1]}`;
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[i - 1] ?? pts[i];
    const p1 = pts[i];
    const p2 = pts[i + 1];
    const p3 = pts[i + 2] ?? pts[i + 1];
    const c1x = p1[0] + (p2[0] - p0[0]) / 6;
    const c1y = p1[1] + (p2[1] - p0[1]) / 6;
    const c2x = p2[0] - (p3[0] - p1[0]) / 6;
    const c2y = p2[1] - (p3[1] - p1[1]) / 6;
    d += ` C${c1x.toFixed(1)},${c1y.toFixed(1)} ${c2x.toFixed(1)},${c2y.toFixed(1)} ${p2[0]},${p2[1]}`;
  }
  return d;
}

/** Linear interpolation between two `#rrggbb` colours. Ported from `lerp()`. */
export function lerpHex(a: string, b: string, t: number): string {
  const h = (s: string): number[] => [
    parseInt(s.slice(1, 3), 16),
    parseInt(s.slice(3, 5), 16),
    parseInt(s.slice(5, 7), 16),
  ];
  const pa = h(a);
  const pb = h(b);
  const c = pa.map((v, i) => Math.round(v + (pb[i] - v) * t));
  return '#' + c.map((v) => v.toString(16).padStart(2, '0')).join('');
}

/** Clamp helper. */
export const clamp = (v: number, lo = 0, hi = 1): number => Math.max(lo, Math.min(hi, v));
