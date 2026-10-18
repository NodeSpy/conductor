// Bot mascots: two style directions for review, before the full set is drawn in the chosen one.
// One solid silhouette per mascot in the bot's identity colour, and a pair of eyes in the theme's
// ink: nothing else (direction B adds one restrained highlight). Expression comes only from the
// eyes' size, tilt and spacing, in one eye geometry per direction. Drawn on a 48-unit grid.
// Never derived from the logo: no open arch, and no single dot under a curve.
//
//   mascotSVG('facet', 'hex', 'curious', { fill, ink, rim, hl, size })

const TAU = Math.PI * 2;
const f = (n) => +n.toFixed(2);
// A closed path through points, for computed outlines (flower, squircle).
const pathOf = (pts) => `M${pts.map(([x, y]) => `${f(x)} ${f(y)}`).join('L')}Z`;
// A rounded polygon: filled, with a fat round-joined stroke in the same colour to soften the corners.
const poly = (pts, round) => `<polygon points="${pts}" fill="FACE" stroke="FACE" stroke-width="${round}" stroke-linejoin="round"/>`;
const shape = (d, round = 0) => `<path d="${d}" fill="FACE"${round ? ` stroke="FACE" stroke-width="${round}" stroke-linejoin="round"` : ''}/>`;

// A circle with a round bite out of it: the big circle's long arc, then the bite's arc, inverted.
function notched(cx, cy, r, bx, by, br) {
  const d = Math.hypot(bx - cx, by - cy), a = (r * r - br * br + d * d) / (2 * d), h = Math.sqrt(r * r - a * a);
  const ux = (bx - cx) / d, uy = (by - cy) / d, mx = cx + a * ux, my = cy + a * uy;
  const p1 = [mx - h * uy, my + h * ux], p2 = [mx + h * uy, my - h * ux];
  return `M${f(p1[0])} ${f(p1[1])}A${r} ${r} 0 1 1 ${f(p2[0])} ${f(p2[1])}A${br} ${br} 0 0 0 ${f(p1[0])} ${f(p1[1])}Z`;
}
const flower = (cx, cy, r, amp, lobes) => pathOf(Array.from({ length: 144 }, (_, i) => { const t = (i / 144) * TAU; const rr = r + amp * Math.cos(lobes * t); return [cx + rr * Math.sin(t), cy - rr * Math.cos(t)]; }));
const squircle = (cx, cy, rx, ry, rot, n = 4) => pathOf(Array.from({ length: 144 }, (_, i) => { const t = (i / 144) * TAU, c = Math.cos(t), s = Math.sin(t); const x = rx * Math.sign(c) * Math.abs(c) ** (2 / n), y = ry * Math.sign(s) * Math.abs(s) ** (2 / n); const a = (rot * Math.PI) / 180; return [cx + x * Math.cos(a) - y * Math.sin(a), cy + x * Math.sin(a) + y * Math.cos(a)]; }));

// Eyes: [dx, dy, w, h, rotate] for the left and right eye, relative to the shape's eye anchor.
// Rotation is in degrees, clockwise; the right eye's numbers are its own (not mirrored).
const BAR_EYES = {
  curious: [[-6.5, -1, 4.6, 10.5, 0], [6.5, 1, 4.6, 6.5, 0]],
  focused: [[-6.5, 0, 8.5, 4.2, 12], [6.5, 0, 8.5, 4.2, -12]],
  calm: [[-7, 1, 7.5, 3.8, 0], [7, 1, 7.5, 3.8, 0]],
  sleepy: [[-6.5, 3, 8, 2.8, -8], [6.5, 3, 8, 2.8, 8]],
  sceptical: [[-6.5, -0.5, 4.6, 9.5, 0], [6.5, 1, 8.5, 3.8, -10]],
  delighted: [[-6, -0.5, 4.6, 10, 16], [6, -0.5, 4.6, 10, -16]],
};
const OVAL_EYES = {
  curious: [[-6.5, -1, 7.2, 10.4, 0], [6.5, 1, 5.2, 7.2, 0]],
  focused: [[-6.5, 0, 8.6, 5, 12], [6.5, 0, 8.6, 5, -12]],
  calm: [[-7, 1, 7.6, 5.6, 0], [7, 1, 7.6, 5.6, 0]],
  sleepy: [[-6.5, 2.5, 8.4, 3.6, -6], [6.5, 2.5, 8.4, 3.6, 6]],
  sceptical: [[-6.5, -0.5, 6.4, 9.2, 0], [6.5, 1, 8.4, 4, -10]],
  delighted: 'crescent',
};
const bar = ([x, y, w, h, r]) => `<rect x="${f(x - w / 2)}" y="${f(y - h / 2)}" width="${w}" height="${h}" rx="${Math.min(w, h) / 2}" fill="INK"${r ? ` transform="rotate(${r} ${f(x)} ${f(y)})"` : ''}/>`;
const oval = ([x, y, w, h, r]) => `<ellipse cx="${f(x)}" cy="${f(y)}" rx="${w / 2}" ry="${h / 2}" fill="INK"${r ? ` transform="rotate(${r} ${f(x)} ${f(y)})"` : ''}/>`;
// Delighted, in ovals: the top halves of two ovals (smiling eyes), corners softened.
const crescent = (x, y) => `<path d="M${f(x - 4)} ${f(y + 2)}a4 5 0 0 1 8 0z" fill="INK" stroke="INK" stroke-width="1.6" stroke-linejoin="round"/>`;

// Each mascot: [id, name, silhouette (in FACE), eye anchor [x, y, half-spacing], highlight [cx, cy, rx, ry, rot]].
export const MASCOT_DIRECTIONS = [
  {
    id: 'facet', name: 'Facet', eyes: 'bars',
    note: 'Soft geometry from the system’s own radii: a rounded hexagon, a lozenge, an upright capsule, a quarter-round, a notched circle and a soft shield. Flat, one solid colour. The eyes are rounded bars of one width; tilt, length and spacing make the expression. Crisp and technical; the closer of the two to the product’s icons.',
    mascots: [
      ['hex', 'Hex', poly('14.5,8.5 33.5,8.5 42.5,24 33.5,39.5 14.5,39.5 5.5,24', 7), [24, 23.5, 6.5]],
      ['lozenge', 'Lozenge', poly('24,6 41.5,24 24,42 6.5,24', 9), [24, 24, 6.5]],
      ['capsule', 'Capsule', `<rect x="10" y="4" width="28" height="40" rx="14" fill="FACE"/>`, [24, 20, 6.5]],
      ['quarter', 'Quarter', shape('M8 42V8A34 34 0 0 1 42 42Z', 5), [21, 26.5, 6.5]],
      ['notch', 'Notch', shape(notched(23, 25, 19, 39.5, 8.5, 9), 2), [21.5, 26, 6.5]],
      ['shield', 'Shield', shape('M10 9.5H38V24C38 33 31.5 38.5 24 41.5C16.5 38.5 10 33 10 24Z', 6), [24, 22, 6.5]],
    ],
  },
  {
    id: 'pebble', name: 'Pebble', eyes: 'ovals',
    note: 'Soft organic forms: a bean, a stacked pebble, a twin-lobe, a six-petal flower, a lens and a tilted squircle, each with one restrained highlight in the top-left. The eyes are ovals; their squash, tilt and size make the expression. Warmer and more characterful; the highlight drops out cleanly at 24px.',
    mascots: [
      ['bean', 'Bean', shape('M9 21C8 11 17 6.5 25 9.5C29.5 11.2 32 9.8 36 10.5C43 12 45 22 42 30C38.5 40 27 43 18 40.5C11 38.5 9.6 30 9 21Z'), [25, 24, 6.5], [16, 15, 3.6, 2, -38]],
      ['pebble', 'Pebble', `<ellipse cx="28.5" cy="12" rx="9" ry="6.5" fill="FACE"/><ellipse cx="24" cy="30" rx="19" ry="13.5" fill="FACE"/>`, [24, 30, 6.5], [12.5, 24, 3.4, 1.8, -32]],
      ['twin', 'Twin', `<circle cx="15" cy="25" r="11.5" fill="FACE"/><circle cx="33" cy="25" r="11.5" fill="FACE"/>`, [24, 24.5, 7.5], [9.5, 18.5, 3, 1.7, -40]],
      ['petal', 'Petal', shape(flower(24, 24, 17.5, 2.6, 6)), [24, 24, 6.5], [15, 13.5, 3.2, 1.8, -36]],
      ['lens', 'Lens', `<g transform="rotate(-14 24 24)">${shape('M5 24Q24 4 43 24Q24 44 5 24Z', 6)}</g>`, [24, 24.5, 6.5], [14, 18.5, 3.2, 1.7, -36]],
      ['tilt', 'Tilt', shape(squircle(24, 24.5, 18, 16, -10)), [24, 25, 6.5], [13, 15.5, 3.4, 1.8, -44]],
    ],
  },
];
export const MASCOT_EXPRESSIONS = ['curious', 'focused', 'calm', 'sleepy', 'sceptical', 'delighted'];

export function mascotSVG(dir, id, expr, { fill, ink, rim = '', hl = '', size = 48, title = '' } = {}) {
  const D = MASCOT_DIRECTIONS.find((d) => d.id === dir);
  const [, name, body, [ax, ay, s], h] = D.mascots.find((m) => m[0] === id);
  const set = (D.eyes === 'bars' ? BAR_EYES : OVAL_EYES)[expr];
  const at = ([dx, dy, w, hh, r]) => [ax + Math.sign(dx) * (Math.abs(dx) - 6.5 + s), ay + dy, w, hh, r];
  const eyes = set === 'crescent' ? crescent(ax - s, ay) + crescent(ax + s, ay)
    : set.map((e) => (D.eyes === 'bars' ? bar(at(e)) : oval(at(e)))).join('');
  // The rim: the silhouette again, underneath, in the rim colour and 3 units fatter (for deep fills on a dark page).
  const rimBody = (c) => body.replace(/<(path|polygon|rect|ellipse|circle)\b[^>]*>/g, (tag) => tag.includes('stroke="FACE"')
    ? tag.replace(/stroke-width="([\d.]+)"/, (_, n) => `stroke-width="${+n + 3}"`).replaceAll('FACE', c)
    : tag.replace('fill="FACE"', `fill="${c}" stroke="${c}" stroke-width="3" stroke-linejoin="round"`));
  const shine = h && hl ? `<ellipse cx="${h[0]}" cy="${h[1]}" rx="${h[2]}" ry="${h[3]}" transform="rotate(${h[4]} ${h[0]} ${h[1]})" fill="${hl}"/>` : '';
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" width="${size}" height="${size}" role="img" aria-label="${title || `${name}, ${expr}`}">${rim ? rimBody(rim) : ''}${body.replaceAll('FACE', fill)}${shine}${eyes.replaceAll('INK', ink)}</svg>`;
}
