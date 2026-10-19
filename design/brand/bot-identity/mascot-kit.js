// Bot mascots, round two: a mix-and-match kit. Any body shape + any eye set + any identity colour.
// One solid silhouette in the bot's colour and a pair of eyes (or one) in the theme's ink, nothing else.
// Drawn on a 48-unit grid. Never derived from the logo: no open arch, and never a single eye under a
// round top (a dot under a curve reads as the mark), so the single eye only goes on angular shapes.
//
// The eye rule: expression comes from eye shape, size, spacing, gaze and simple lids, never from
// narrowing or slanting. No eye is a squint or a slit, none is tilted, and none may evoke an ethnicity,
// a culture or a body type. Every eye set below carries its review against that rule.
//
//   kitSVG('hex', 'dots', { fill, ink, rim, size })

const TAU = Math.PI * 2;
const f = (n) => +n.toFixed(2);
const pathOf = (pts) => `M${pts.map(([x, y]) => `${f(x)} ${f(y)}`).join('L')}Z`;
const poly = (pts, round) => `<polygon points="${pts}" fill="FACE" stroke="FACE" stroke-width="${round}" stroke-linejoin="round"/>`;
const shape = (d, round = 0) => `<path d="${d}" fill="FACE"${round ? ` stroke="FACE" stroke-width="${round}" stroke-linejoin="round"` : ''}/>`;
function notched(cx, cy, r, bx, by, br) {
  const d = Math.hypot(bx - cx, by - cy), a = (r * r - br * br + d * d) / (2 * d), h = Math.sqrt(r * r - a * a);
  const ux = (bx - cx) / d, uy = (by - cy) / d, mx = cx + a * ux, my = cy + a * uy;
  const p1 = [mx - h * uy, my + h * ux], p2 = [mx + h * uy, my - h * ux];
  return `M${f(p1[0])} ${f(p1[1])}A${r} ${r} 0 1 1 ${f(p2[0])} ${f(p2[1])}A${br} ${br} 0 0 0 ${f(p1[0])} ${f(p1[1])}Z`;
}
const radial = (cx, cy, rf) => pathOf(Array.from({ length: 160 }, (_, i) => { const t = (i / 160) * TAU, r = rf(t); return [cx + r * Math.sin(t), cy - r * Math.cos(t)]; }));
const squircle = (cx, cy, rx, ry, rot, n = 4) => pathOf(Array.from({ length: 144 }, (_, i) => { const t = (i / 144) * TAU, c = Math.cos(t), s = Math.sin(t); const x = rx * Math.sign(c) * Math.abs(c) ** (2 / n), y = ry * Math.sign(s) * Math.abs(s) ** (2 / n); const a = (rot * Math.PI) / 180; return [cx + x * Math.cos(a) - y * Math.sin(a), cy + x * Math.sin(a) + y * Math.cos(a)]; }));
const egg = (cx, cy, rx, ry) => pathOf(Array.from({ length: 144 }, (_, i) => { const t = (i / 144) * TAU; return [cx + rx * Math.sin(t) * (1 - 0.16 * Math.cos(t)), cy - ry * Math.cos(t)]; }));

// [id, name, from, silhouette, [eye x, eye y, usable half-width], roundTop]
export const SHAPES = [
  ['hex', 'Hex', 'Facet', poly('14.5,8.5 33.5,8.5 42.5,24 33.5,39.5 14.5,39.5 5.5,24', 7), [24, 23.5, 14], false],
  ['lozenge', 'Lozenge', 'Facet', poly('24,6 41.5,24 24,42 6.5,24', 9), [24, 24, 12.5], false],
  ['capsule', 'Capsule', 'Facet', `<rect x="10" y="4" width="28" height="40" rx="14" fill="FACE"/>`, [24, 20, 11.5], true],
  ['quarter', 'Quarter', 'Facet', shape('M8 42V8A34 34 0 0 1 42 42Z', 5), [21, 27, 12.5], false],
  ['notch', 'Notch', 'Facet', shape(notched(23, 25, 19, 39.5, 8.5, 9), 2), [21.5, 26, 13], true],
  ['shield', 'Shield', 'Facet', shape('M10 9.5H38V24C38 33 31.5 38.5 24 41.5C16.5 38.5 10 33 10 24Z', 6), [24, 22, 12], false],
  ['bean', 'Bean', 'Pebble', shape('M9 21C8 11 17 6.5 25 9.5C29.5 11.2 32 9.8 36 10.5C43 12 45 22 42 30C38.5 40 27 43 18 40.5C11 38.5 9.6 30 9 21Z'), [25, 24, 13.5], true],
  ['twin', 'Twin', 'Pebble', `<circle cx="15" cy="25" r="11.5" fill="FACE"/><circle cx="33" cy="25" r="11.5" fill="FACE"/>`, [24, 24.5, 14], true],
  ['petal', 'Petal', 'Pebble', shape(radial(24, 24, (t) => 17.5 + 2.6 * Math.cos(6 * t))), [24, 24, 12], true],
  ['tilt', 'Tilt', 'Pebble', shape(squircle(24, 24.5, 18, 16, -10)), [24, 25, 14], false],
  ['egg', 'Egg', 'new', shape(egg(24, 25.5, 15.5, 19)), [24, 27, 11.5], true],
  ['cushion', 'Cushion', 'new', shape(radial(24, 24, (t) => 19 * (1 - 0.085 * Math.cos(4 * t)))), [24, 24, 13], false],
  ['octa', 'Octa', 'new', poly('17,6.5 31,6.5 41.5,17 41.5,31 31,41.5 17,41.5 6.5,31 6.5,17', 6), [24, 24, 13.5], false],
];

// Eye sets: [id, name, reads as, review, draw(x, y, side) per eye or single]. All level; none tilted.
const dot = (x, y, r) => `<circle cx="${f(x)}" cy="${f(y)}" r="${r}" fill="INK"/>`;
export const EYES = [
  ['dots', 'Round dots', 'calm, attentive', 'Round, level, equal. Passes.', (x, y) => dot(x, y, 3.2)],
  ['ovals', 'Tall ovals', 'alert, curious', 'Taller than wide (1.7 : 1), level. Passes.', (x, y) => `<ellipse cx="${f(x)}" cy="${f(y)}" rx="2.6" ry="4.4" fill="INK"/>`],
  ['rings', 'Rings', 'open, surprised', 'Round, open, level. Passes.', (x, y) => `<circle cx="${f(x)}" cy="${f(y)}" r="3.3" fill="none" stroke="INK" stroke-width="2.3"/>`],
  ['single', 'Single eye', 'focused, watchful', 'One round eye, centred. Passes. Only on angular shapes: under a round top it would read as the logo’s dot under an arch.', 'single'],
  ['wide', 'Wide-set dots', 'easy-going, friendly', 'Round, level, set wide apart. Passes; spacing alone carries it.', 'wide'],
  ['up', 'Looking up', 'thinking, hopeful', 'A round ring with its pupil at the top: gaze, not narrowing. Passes.', (x, y) => `<circle cx="${f(x)}" cy="${f(y)}" r="3.8" fill="none" stroke="INK" stroke-width="1.9"/>${dot(x, y - 1.7, 1.9)}`],
  ['happy', 'Happy arcs', 'delighted', 'Upward curves, level and symmetrical: closed, smiling eyes, not a slit. Passes.', (x, y) => `<path d="M${f(x - 3.8)} ${f(y + 1.8)}q3.8-5.6 7.6 0" fill="none" stroke="INK" stroke-width="2.7" stroke-linecap="round"/>`],
  ['sleepy', 'Heavy lids', 'sleepy, unhurried', 'A round eye with a level lid across its top fifth: about three-quarters of the eye shows (0.7 as tall as wide), round below the lid, never slanted or narrowed to a line. Passes.', (x, y) => `<path d="M${f(x - 3.43)} ${f(y - 1.4)}A3.7 3.7 0 1 0 ${f(x + 3.43)} ${f(y - 1.4)}z" fill="INK"/><path d="M${f(x - 4.4)} ${f(y - 1.7)}h8.8" stroke="INK" stroke-width="1.9" stroke-linecap="round"/>`],
];
// Withdrawn in round two (never to return): slanted or tilted bars, narrow horizontal bars, flattened or
// tilted ovals, and the stacked pebble (its knob over a wide body read as a topknot).
export const WITHDRAWN = ['Slanted or tilted bars (Facet focused, delighted, sceptical)', 'Narrow horizontal bars (Facet calm and sleepy on the capsule and quarter-round)', 'Flattened or tilted ovals (Pebble focused, calm, sleepy, sceptical)', 'The stacked pebble shape: its small knob over a wide round body read as a topknot', 'A short, wide capsule (tried in this round): two dots on it read as a snout', 'The twin-lobe with calm eyes and the lozenge with focused eyes (dropped combinations; both eye sets are withdrawn)'];

/** Is this combination allowed? The single eye never goes on a round-topped shape. */
export const allowed = (shapeId, eyeId) => !(eyeId === 'single' && SHAPES.find((s) => s[0] === shapeId)[5]);

export function kitSVG(shapeId, eyeId, { fill, ink, rim = '', size = 48, title = '' } = {}) {
  const [, name, , body, [ax, ay, hw]] = SHAPES.find((s) => s[0] === shapeId);
  const [, eyeName, , , draw] = EYES.find((e) => e[0] === eyeId);
  const s = Math.min(6.5, hw - 4.5), w = Math.min(10.5, hw - 3.2);
  const eyes = draw === 'single' ? dot(ax, ay, 4.6) : draw === 'wide' ? dot(ax - w, ay, 2.9) + dot(ax + w, ay, 2.9) : draw(ax - s, ay) + draw(ax + s, ay);
  const rimBody = (c) => body.replace(/<(path|polygon|rect|ellipse|circle)\b[^>]*>/g, (tag) => tag.includes('stroke="FACE"')
    ? tag.replace(/stroke-width="([\d.]+)"/, (_, n) => `stroke-width="${+n + 3}"`).replaceAll('FACE', c)
    : tag.replace('fill="FACE"', `fill="${c}" stroke="${c}" stroke-width="3" stroke-linejoin="round"`));
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" width="${size}" height="${size}" role="img" aria-label="${title || `${name} with ${eyeName.toLowerCase()}`}">${rim ? rimBody(rim) : ''}${body.replaceAll('FACE', fill)}${eyes.replaceAll('INK', ink)}</svg>`;
}
