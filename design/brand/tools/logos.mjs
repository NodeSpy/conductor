// Logo candidates for the Daylight system (round three of the mark). Same shape as the
// directions in directions.mjs, so build.mjs and render.mjs can produce a complete,
// self-contained kit for each in logo-kits/<id>/. Geometry is hand-tuned:
//   mark — 32-unit grid, box = content bounds [x, y, w, h]
//   fav  — a separate cut on a 16-unit grid, snapped for 16px tabs
// The wordmark is Fraunces SemiBold, Daylight's heading face.

// Daylight's palette, with one deliberate change for the logo: the ink is deeper than
// body text (#1c1712 vs #2a231c) so the mark holds its own on cream at small sizes.
const PAL = {
  name: 'Daylight',
  light: { bg: '#f7f3ec', surface: '#fffdf9', line: '#ddd3c2', ink: '#1c1712', ink2: '#584e43', ink3: '#6e6457', acc: '#cc4a0e' },
  dark: { bg: '#1d1915', surface: '#26211c', line: '#463e35', ink: '#f4eee4', ink2: '#d3c9ba', ink3: '#ada291', acc: '#ff7a3d' },
};
const WORD = { font: 'fraunces-latin-600-normal.woff', track: -0.012, label: 'Fraunces SemiBold' };

export const LOGOS = [
  {
    id: 'fermata-bold',
    name: 'Fermata, strengthened',
    line: 'The same idea, built to hold on light.',
    why: 'The hold-until-released arch, redrawn heavier for cream. The crown is 5.5 units (was 4) and the feet 5 (was 3.2), the dot is 25% bigger and sits higher under the arch, and the ink is deeper. The wordmark moves to Fraunces SemiBold, Daylight’s heading face, which carries real weight beside the mark. The old italic was spindly on light. The 16px cut is redrawn with 3px feet.',
    mark: {
      ink: '<path d="M2.5 21a13.5 15 0 0 1 27 0h-5a8.5 9.5 0 0 0-17 0z"/>',
      acc: '<circle cx="16" cy="21" r="5"/>',
      box: [2.5, 6, 27, 20],
    },
    fav: {
      ink: '<path d="M1 11a7 8 0 0 1 14 0h-3a4 4.8 0 0 0-8 0z"/>',
      acc: '<circle cx="8" cy="11" r="2.6"/>',
    },
    word: WORD,
    lock: { h: 1.0, align: 'baseline', dy: 0.04, gap: 0.3 },
    stack: { markW: 0.3, gap: 0.3 },
    pal: PAL,
  },
  {
    id: 'gate',
    name: 'Gate',
    line: 'A doorway with you standing in it.',
    why: 'The fermata becomes architecture: a heavy doorway arch whose legs reach the ground, with the ember dot standing in the opening. It still means “nothing passes until released” (approvals, the needs-you queue, the sandbox), and it also reads as a gate or a keyhole, which suits a tool you trust near production. It is a closed, heavy silhouette, so it is the strongest of the line-based marks on light and at 16px.',
    mark: {
      ink: '<path d="M5 27V15a11 11 0 0 1 22 0v12h-5.5V15a5.5 5.5 0 0 0-11 0v12z"/>',
      acc: '<circle cx="16" cy="22.2" r="3.7"/>',
      box: [5, 4, 22, 23],
    },
    fav: {
      ink: '<path d="M2 14V7.5a6 6 0 0 1 12 0V14h-3V7.5a3 3 0 0 0-6 0V14z"/>',
      acc: '<circle cx="8" cy="11.5" r="2"/>',
    },
    whyNot: 'The clearest favicon of the set, but beside the lowercase wordmark its arch-with-legs reads as a bold “n” (“nconductor”).',
    word: WORD,
    lock: { h: 0.98, align: 'baseline', dy: 0.0, gap: 0.3 },
    stack: { markW: 0.24, gap: 0.3 },
    pal: PAL,
  },
  {
    id: 'cue',
    name: 'Cue',
    line: 'The moment conductor hands it to you.',
    why: 'Two solid shapes: the system (a large ink disc) and you (the ember dot), with the disc giving way where the dot sits. It is the cue, the instant a conductor turns to one player, which is exactly what an approval or a needs-you item is. It has no lines at all, so it is the boldest silhouette on any surface and the most legible at 16px. The trade-off is that a disc and a dot is an abstract, familiar shape that needs the name beside it to say “conductor”.',
    mark: {
      ink: '<path d="M17.42 7.52A11.5 11.5 0 1 0 24.82 14.6A7.6 7.6 0 0 1 17.42 7.52Z"/>',
      acc: '<circle cx="25" cy="7" r="4.5"/>',
      box: [2.5, 2.5, 27, 27.5],
    },
    fav: {
      ink: '<path d="M8.3 3.44A6 6 0 1 0 12.78 7.69A4.2 4.2 0 0 1 8.3 3.44Z"/>',
      acc: '<circle cx="12.5" cy="3.5" r="2.5"/>',
    },
    whyNot: 'The boldest silhouette, but a disc and a dot is an abstract, familiar shape that says nothing about conductor without the name.',
    word: WORD,
    lock: { h: 1.0, align: 'baseline', dy: 0.02, gap: 0.28 },
    stack: { markW: 0.24, gap: 0.3 },
    pal: PAL,
  },
  {
    id: 'conduit-daylight',
    name: 'Conduit',
    line: 'Round one’s runner-up, re-set for Daylight.',
    why: 'The C as a circuit with the signal passing through its gap, now with the Fraunces wordmark and the Daylight palette, for a fair comparison. It is a heavy annulus, very legible at 16px and strong on light. But a C with a dot is a crowded genre, and the mark carries less of conductor’s own idea than the others.',
    mark: {
      ink: '<path d="M24.19 6.81a13 13 0 1 0 0 18.38l-3.53-3.53a8 8 0 1 1 0-11.32z"/>',
      acc: '<circle cx="25.8" cy="16" r="3.8"/>',
      box: [2.4, 3, 27.2, 26],
    },
    fav: {
      ink: '<path d="M12.1 3.4a6.5 6.5 0 1 0 0 9.2l-1.77-1.77a4 4 0 1 1 0-5.66z"/>',
      acc: '<circle cx="13" cy="8" r="2"/>',
    },
    whyNot: 'Clear and strong on light, but a C with a dot is a crowded genre and it carries the least of conductor’s own idea.',
    word: WORD,
    lock: { h: 1.0, align: 'center', dy: 0, gap: 0.28 },
    stack: { markW: 0.24, gap: 0.3 },
    pal: PAL,
  },
];

// Daniel picked Fermata, strengthened (Oct 2026). The official kit is generated into
// design/brand/assets/ (the one canonical location); the explored marks stay in logo-kits/.
export const OFFICIAL_LOGO = 'fermata-bold';
export const RECOMMENDED_LOGO = OFFICIAL_LOGO;
export const kitDir = (id) => (id === OFFICIAL_LOGO ? 'assets' : `logo-kits/${id}`);
