// Derives the raw colour ramps from OKLCH parameters and prints them as JSON.
// This was run once to seed tokens/tokens.json. From then on, tokens.json is the
// source of truth: the hex values are frozen there, so a later tweak here does not
// silently move a shipped colour. Re-run it only on purpose, and diff the result.
import { toHex } from './oklch.mjs';

export const STEPS = ['50', '100', '200', '300', '400', '500', '600', '700', '800', '900', '950'];
const L = [0.975, 0.945, 0.89, 0.82, 0.74, 0.66, 0.575, 0.49, 0.405, 0.315, 0.235];
const SHAPE = [0.12, 0.25, 0.45, 0.7, 0.9, 1, 1, 0.92, 0.8, 0.65, 0.5];

const hue = (h, cmax, l = L) => Object.fromEntries(STEPS.map((s, i) => [s, toHex(l[i], cmax * SHAPE[i], h)]));

export function ramps() {
  // Neutral: warm, and slightly greener at the dark end, like the prototype's graphite.
  const nL = [0.975, 0.945, 0.885, 0.815, 0.73, 0.6, 0.495, 0.42, 0.345, 0.285, 0.215];
  const nC = [0.008, 0.013, 0.02, 0.026, 0.026, 0.021, 0.019, 0.017, 0.016, 0.014, 0.01];
  const nH = [90, 90, 89, 89, 90, 92, 92, 92, 108, 112, 100];
  const neutral = Object.fromEntries(STEPS.map((s, i) => [s, toHex(nL[i], nC[i], nH[i])]));
  return {
    neutral,
    // Surfaces need finer steps than a perceptual ramp gives, so each theme gets its own
    // six-step surface set (0 = sunken … 5 = overlay). Values are the prototype's, kept.
    graphite: { 0: '#0b0c09', 1: '#0f100d', 2: '#13140f', 3: '#181914', 4: '#1f201a', 5: '#272821' },
    paper: { 0: '#e8e2d5', 1: '#efeadf', 2: '#f4f0e6', 3: '#f9f6ef', 4: '#fffdf8', 5: '#ffffff' },
    ember: { ...hue(41, 0.2), 500: '#ff6a2b', 600: '#cc4a0e' },
    red: hue(14, 0.2),
    amber: hue(80, 0.16),
    // 700 nudged from L 0.49 to 0.475: success text must clear 4.5:1 on its own wash.
    green: { ...hue(152, 0.16), 700: toHex(0.475, 0.16 * 0.92, 152) },
    teal: hue(185, 0.12),
    sky: hue(232, 0.13),
    blue: hue(258, 0.17),
    violet: hue(292, 0.15),
    magenta: hue(345, 0.17),
  };
}

if (import.meta.url === `file://${process.argv[1]}`) console.log(JSON.stringify(ramps(), null, 1));
