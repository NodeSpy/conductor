// Bot faces: three style directions for review, before the full set is drawn in the chosen one.
// Every face shares one silhouette: the rounded square of the initials avatar (48-unit grid, corner
// radius 13.5, the same 0.28 ratio as the 32px initials tile). Identity comes from the face inside
// and the colour. Features are ink (or a tonal shade of the colour that only raises their contrast).
//
//   dirSVG('line', 'beam', { fill, ink, tone, rim, size })
//   tone: the colour the two-tone treatments mix toward (white in light, black in dark), so the
//         features keep more contrast on it than on the plain fill, never less.

const R = 13.5;
const S = 'stroke-linecap="round" stroke-linejoin="round" fill="none"';
const st = (d, w = 3.5) => `<path d="${d}" stroke="INK" stroke-width="${w}" ${S}/>`;
const dot = (x, y, r) => `<circle cx="${x}" cy="${y}" r="${r}" fill="INK"/>`;
const pill = (x, y, w, h) => `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="${Math.min(w, h) / 2}" fill="INK"/>`;
// A solid shape with its corners softened by a 2-unit stroke in the same ink.
const solid = (d) => `<path d="${d}" fill="INK" stroke="INK" stroke-width="2" stroke-linejoin="round"/>`;

export const DIRECTIONS = [
  {
    id: 'line', name: 'Line',
    note: 'Monoline. One stroke weight for every feature (3.5 on the 48 grid: 1.75px at 24px), dots the same width as the stroke, a flat tile. The quietest of the three; expression is the tilt and curve of a few lines.',
    faces: {
      beam: st('M12.5 24.5q4-5.5 8 0') + st('M27.5 24.5q4-5.5 8 0') + st('M19 32q5 3.8 10 0'),
      focus: st('M12.5 22.6L20 23.8') + st('M35.5 22.6L28 23.8') + st('M20.5 32.5h7'),
      curio: dot(16.5, 25, 2.9) + dot(31.5, 25, 2.9) + st('M27.5 17.6l7.5-2.2') + st('M21 32.8l6.5-1.6'),
    },
  },
  {
    id: 'solid', name: 'Solid',
    note: 'Eyes only, as solid geometry: domes, slanted bars, circles. No mouth; the eye shape carries the whole expression. Largest features of the three, so it holds best at 24px; the most graphic and the closest to a product mark.',
    faces: {
      beam: solid('M11.5 27a5.5 5.5 0 0 1 11 0z') + solid('M25.5 27a5.5 5.5 0 0 1 11 0z'),
      focus: solid('M11 22.5L20 24.5V28.5H11z') + solid('M37 22.5L28 24.5V28.5H37z'),
      curio: dot(16.5, 24, 5.6) + dot(32, 25.6, 3.6),
    },
  },
  {
    id: 'screen', name: 'Screen',
    note: 'Two-tone with depth. The tile has a soft diagonal shade and an inset screen in a tonal shade of the bot’s colour (paler in light, deeper in dark, so the features gain contrast). Pill eyes and a single line for the mouth. The most “device”; the most detail to hold at 24px.',
    tile: (u) => `<linearGradient id="g${u}" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="TONE" stop-opacity=".24"/><stop offset=".6" stop-color="TONE" stop-opacity="0"/></linearGradient><rect width="48" height="48" rx="${R}" fill="url(#g${u})"/><rect x="6" y="10.5" width="36" height="27" rx="8.5" fill="TONE" fill-opacity=".3"/>`,
    faces: {
      beam: st('M13 25q3.75-5 7.5 0') + st('M27.5 25q3.75-5 7.5 0') + st('M20 31.2q4 2.8 8 0', 3),
      focus: pill(12.5, 21.5, 8.5, 4) + pill(27, 21.5, 8.5, 4) + st('M21 31.2h6', 3),
      curio: pill(14, 16.5, 5, 9.5) + pill(29.5, 20.5, 5, 5.5) + st('M22 31.4l5.5-1.3', 3),
    },
  },
];
export const DIRECTION_FACES = [['beam', 'cheerful'], ['focus', 'focused'], ['curio', 'curious']];

let uid = 0;
export function dirSVG(dir, face, { fill, ink, tone = '#ffffff', rim = '', size = 48, title = '' } = {}) {
  const d = DIRECTIONS.find((x) => x.id === dir), u = `bd${++uid}`;
  const label = `role="img" aria-label="${title || `${d.name}: ${face}`}"`;
  const tile = `<rect width="48" height="48" rx="${R}" fill="${fill}"/>` + (d.tile ? d.tile(u).replaceAll('TONE', tone) : '')
    + (rim ? `<rect x=".75" y=".75" width="46.5" height="46.5" rx="${R - 0.75}" fill="none" stroke="${rim}" stroke-width="1.5"/>` : '');
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" width="${size}" height="${size}" ${label}>${tile}${d.faces[face].replaceAll('INK', ink)}</svg>`;
}
