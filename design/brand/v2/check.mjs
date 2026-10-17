// Contrast check for the v2 direction palettes (same rules as tools/tokens.mjs:
// 7 for primary text, 4.5 for other text, 3 for control edges and solid marks).
import { DIRS } from './directions.js';
import { contrast } from '../tools/oklch.mjs';
const BGS = ['canvas', 'sunken', 'surface', 'raised', 'overlay'];
let fails = 0, n = 0;
const chk = (d, t, fg, bg, min, label) => { n++; const r = contrast(fg, bg); if (r < min) { fails++; console.log(`FAIL ${d} ${t} ${label}: ${r.toFixed(2)} < ${min} (${fg} on ${bg})`); } };
for (const [id, D] of Object.entries(DIRS)) for (const t of ['light', 'dark']) {
  const P = D[t], v = (k) => P['--color-' + k];
  for (const b of BGS) {
    chk(id, t, v('text-primary'), v('bg-' + b), 7, 'text.primary/' + b);
    chk(id, t, v('text-secondary'), v('bg-' + b), 4.5, 'text.secondary/' + b);
    chk(id, t, v('text-tertiary'), v('bg-' + b), 4.5, 'text.tertiary/' + b);
    chk(id, t, v('text-accent'), v('bg-' + b), 4.5, 'text.accent/' + b);
    chk(id, t, v('border-control'), v('bg-' + b), 3, 'border.control/' + b);
    for (const s of ['success', 'warning', 'danger', 'running', 'info', 'neutral']) {
      chk(id, t, v(`status-${s}-fg`), v('bg-' + b), 4.5, `status.${s}.fg/${b}`);
      if (b === 'surface') { chk(id, t, v(`status-${s}-fg`), v(`status-${s}-bg`), 4.5, `status.${s}.fg/own bg`); chk(id, t, v(`status-${s}-solid`), v('bg-' + b), 3, `status.${s}.solid/${b}`); }
    }
  }
  chk(id, t, v('text-on-accent'), v('accent-default'), 4.5, 'on-accent/accent');
  chk(id, t, v('text-on-accent'), v('accent-hover'), 4.5, 'on-accent/accent-hover');
  chk(id, t, v('text-accent'), v('accent-subtle'), 4.5, 'text.accent/accent-subtle');
  chk(id, t, v('text-primary'), v('accent-subtle'), 7, 'text.primary/accent-subtle');
  for (const k of ['key', 'string', 'number', 'comment']) chk(id, t, v('code-' + k), v('bg-sunken'), 4.5, 'code.' + k + '/sunken');
  if (P['--d-rail-ink']) { chk(id, t, P['--d-rail-ink'], v('bg-rail'), 7, 'rail ink'); chk(id, t, P['--d-rail-ink-2'], v('bg-rail'), 4.5, 'rail ink 2'); chk(id, t, P['--d-rail-ink'], P['--d-rail-on'], 7, 'rail on'); }
}
console.log(`${n} pairs, ${fails} failing`);
process.exit(fails ? 1 : 0);
