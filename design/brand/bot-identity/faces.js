// Bot faces: the precanned pictures for a bot's identity. Twelve friendly robot faces, each with its
// own head shape and expression, drawn on a 48-unit grid with bold features so they read at 24px and
// hold up at 96px. The head takes the bot's identity colour; the features are drawn in the theme's
// ink, so any face works with any colour in light and dark. They are Daylight's own drawings and are
// never derived from the logo: no arches, and no dot under an arch.
//
// The ids are the config's `avatar.preset` values: short, lowercase and stable. Never rename one;
// add a new face instead.
//
//   faceSVG('beam', { fill: '#cce1a7', ink: '#2a231c', size: 96 })      → an <svg> string
//   In CSS: the files in faces/ use var(--bot-face), var(--bot-ink) and var(--bot-rim) with fallbacks.

const S = 'stroke-linecap="round" stroke-linejoin="round" fill="none"';
const eye = (x, y, r = 3.2) => `<circle cx="${x}" cy="${y}" r="${r}" fill="INK"/>`;
const line = (d, w = 3) => `<path d="${d}" stroke="INK" stroke-width="${w}" ${S}/>`;
const antenna = (x1, y1, x2, y2, knob = 2.6) => `<path d="M${x1} ${y1}L${x2} ${y2}" stroke="INK" stroke-width="2.4" ${S}/><circle cx="${x2}" cy="${y2}" r="${knob}" fill="INK"/>`;
// A rounded polygon: filled, with a fat round-joined stroke in the same colour to soften the corners.
const poly = (pts, round = 6) => `<polygon points="${pts}" fill="FACE" stroke="FACE" stroke-width="${round}" stroke-linejoin="round"/>`;

// [id, name, expression, head (in FACE), features (in INK)]
export const FACES = [
  ['beam', 'Beam', 'cheerful',
    `<circle cx="24" cy="27" r="17" fill="FACE"/>`,
    antenna(24, 10, 24, 4.5) + line('M14.5 25.5q3.5-5 7 0') + line('M26.5 25.5q3.5-5 7 0') + line('M17 32q7 7.5 14 0')],
  ['focus', 'Focus', 'focused',
    `<rect x="8" y="11" width="32" height="31" rx="7" fill="FACE"/><rect x="3.5" y="21" width="5" height="10" rx="2" fill="INK"/><rect x="39.5" y="21" width="5" height="10" rx="2" fill="INK"/>`,
    line('M14 20.5l7 1.6') + line('M34 20.5l-7 1.6') + `<rect x="14" y="24.5" width="7.5" height="3.6" rx="1.8" fill="INK"/><rect x="26.5" y="24.5" width="7.5" height="3.6" rx="1.8" fill="INK"/>` + line('M19.5 34.5h9')],
  ['curio', 'Curio', 'curious',
    `<rect x="11" y="7" width="26" height="37" rx="13" fill="FACE"/>`,
    line('M13.5 17q4.5-3.5 9-0.5') + eye(18, 24, 4.4) + eye(30.5, 25, 2.9) + line('M21 35.5l7-2')],
  ['doze', 'Doze', 'sleepy',
    `<rect x="4" y="15" width="40" height="26" rx="13" fill="FACE"/><rect x="21" y="10" width="6" height="6" rx="2" fill="FACE"/>`,
    line('M11.5 26q4 3.5 8 0') + line('M28.5 26q4 3.5 8 0') + line('M21 34h6') + `<path d="M38 8h5l-5 5h5" stroke="INK" stroke-width="2" ${S}/>`],
  ['grit', 'Grit', 'determined',
    poly('24,8 38.5,16 38.5,34 24,42 9.5,34 9.5,16', 7),
    line('M13.5 19.5l8 3.2') + line('M34.5 19.5l-8 3.2') + eye(17.5, 26.5, 2.9) + eye(30.5, 26.5, 2.9) + line('M18 34.5h12')],
  ['imp', 'Imp', 'mischievous',
    `<rect x="8" y="13" width="32" height="29" rx="9" fill="FACE"/>` + poly('9,15 12,5 19,13', 4) + poly('39,15 36,5 29,13', 4),
    eye(17, 25.5, 3.1) + line('M27 25.5h7') + line('M16.5 33.5q8 4.5 14-3')],
  ['calm', 'Calm', 'calm',
    `<rect x="7" y="8" width="34" height="35" rx="15" fill="FACE"/>`,
    line('M13 23.5h8', 2.6) + line('M27 23.5h8', 2.6) + `<path d="M14 24.5a3 3 0 0 0 6 0z" fill="INK"/><path d="M28 24.5a3 3 0 0 0 6 0z" fill="INK"/>` + line('M19 33q5 3.5 10 0')],
  ['wow', 'Wow', 'surprised',
    poly('17,9 31,9 40,18 40,32 31,41 17,41 8,32 8,18', 6),
    antenna(24, 8, 24, 3, 2.3) + line('M13 16.5q4-3 8 0', 2.6) + line('M27 16.5q4-3 8 0', 2.6) + eye(17, 24, 4) + eye(31, 24, 4) + `<circle cx="24" cy="34" r="3.6" stroke="INK" stroke-width="2.8" fill="none"/>`],
  ['wink', 'Wink', 'playful',
    `<rect x="6" y="14" width="36" height="28" rx="7" fill="FACE"/>`,
    antenna(21, 14, 15, 5) + antenna(27, 14, 33, 5) + line('M13.5 26q3.5-4 7 0') + eye(31, 25.5, 3.2) + line('M18 32.5q6 5 12 0')],
  ['shy', 'Shy', 'bashful',
    poly('9,12 39,12 24,42', 12),
    eye(18.5, 21, 2.6) + eye(29.5, 21, 2.6) + line('M12 26.5l3-1.4', 2.2) + line('M33 25.1l3 1.4', 2.2) + line('M21.5 28q2.5 1.8 5 0', 2.6)],
  ['chirp', 'Chirp', 'excited',
    `<path d="M9 9h30v17a15 15 0 0 1-30 0z" fill="FACE"/>`,
    antenna(14, 9, 11, 3.5, 2.3) + eye(17.5, 21, 3.3) + eye(30.5, 21, 3.3) + `<path d="M17 28.5h14a7 7 0 0 1-14 0z" fill="INK"/>`],
  ['steady', 'Steady', 'steady',
    `<rect x="8" y="12" width="32" height="31" rx="3.5" fill="FACE"/>`,
    `<path d="M24 12V6" stroke="INK" stroke-width="2.4" ${S}/><rect x="20.5" y="2.5" width="7" height="4.5" rx="1.2" fill="INK"/><rect x="14" y="22" width="6.5" height="6.5" rx="1.6" fill="INK"/><rect x="27.5" y="22" width="6.5" height="6.5" rx="1.6" fill="INK"/>` + line('M18 34.5h12')],
];

export const PRESETS = FACES.map(([id]) => id);
const byId = Object.fromEntries(FACES.map((f) => [f[0], f]));

/** An <svg> string for a face. `fill` and `ink` are colours or var() strings; `rim` (optional) draws a
 *  1.5-unit edge round the head, for deep fills on a dark page. `asFile: true` emits the swappable
 *  file form instead: classes and a <style> reading --bot-face, --bot-ink and --bot-rim. */
export function faceSVG(id, { fill = '#c4bcb4', ink = '#2a231c', rim = '', size = 48, title = '', asFile = false } = {}) {
  const [, name, expr, head, feat] = byId[id] ?? byId.steady;
  // The rim: the head again, underneath, in the rim colour and 3 units fatter.
  const rimHead = (c) => head.replace(/<[^>]+>/g, (tag) => tag.includes('stroke="FACE"')
    ? tag.replace(/stroke-width="(\d+)"/, (_, n) => `stroke-width="${+n + 3}"`).replaceAll('FACE', c)
    : tag.replace('fill="FACE"', `fill="${c}" stroke="${c}" stroke-width="3" stroke-linejoin="round"`));
  const label = `role="img" aria-label="${title || `${name}, ${expr}`}"`;
  if (asFile) {
    const cls = (tag) => { const c = []; if (tag.includes('fill="FACE"')) c.push('f'); if (tag.includes('stroke="FACE"')) c.push('fs'); if (tag.includes('fill="INK"')) c.push('i'); if (tag.includes('stroke="INK"')) c.push('is'); if (tag.includes('fill="RIM"')) c.push('r'); if (!c.length) return tag; return tag.replace(/ (fill|stroke)="(FACE|INK|RIM)"/g, '').replace(/^<(\w+)/, `<$1 class="${c.join(' ')}"`); };
    const body = (rimHead('RIM') + head + feat).replace(/<[^>]+>/g, cls).replaceAll('stroke="RIM"', '');
    return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" width="${size}" height="${size}" ${label} data-face="${id}"><style>.f{fill:var(--bot-face,#c4bcb4)}.fs{stroke:var(--bot-face,#c4bcb4)}.i{fill:var(--bot-ink,#2a231c)}.is{stroke:var(--bot-ink,#2a231c)}.r{fill:var(--bot-rim,none);stroke:var(--bot-rim,none)}</style>${body}</svg>`;
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" width="${size}" height="${size}" ${label} data-face="${id}">${rim ? rimHead(rim) : ''}${head.replaceAll('FACE', fill)}${feat.replaceAll('INK', ink)}</svg>`;
}
export const faceInfo = (id) => { const f = byId[id]; return f && { id: f[0], name: f[1], expression: f[2] }; };
