// The four brand directions. Geometry is hand-tuned:
//   mark — drawn on a 32-unit grid (box = content bounds [x, y, w, h])
//   fav  — a separate cut on a 16-unit grid, snapped for 16px tabs
// Ink shapes take the foreground colour, `acc` shapes take the accent.

export const DIRS = [
  {
    id: 'fermata',
    name: 'Fermata',
    num: 'I',
    line: 'Hold, then release.',
    rationale: [
      'A fermata is the one mark in a score that only the conductor resolves: the ensemble holds until they release it. That is conductor’s job too: runs wait behind an approval, a bot pauses on “needs you”, and nothing moves until you sign off.',
      'The arch also reads as a signal arc over its source, so the mark says “conductor” without a top hat or a baton. It is calm and symmetrical, and at 16px it is still just an arch and a dot.',
    ],
    mark: {
      ink: '<path d="M3 21a13 14 0 0 1 26 0h-3.2a9.8 10 0 0 0-19.6 0z"/>',
      acc: '<circle cx="16" cy="21" r="4"/>',
      box: [3, 7, 26, 18],
    },
    fav: {
      ink: '<path d="M1 11a7 8 0 0 1 14 0h-2a5 6 0 0 0-10 0z"/>',
      acc: '<circle cx="8" cy="11" r="2.1"/>',
    },
    word: { font: 'instrument-serif-latin-400-italic.woff', track: -0.01, label: 'Instrument Serif Italic' },
    lock: { h: 0.96, align: 'baseline', dy: 0.02, gap: 0.3 },
    stack: { markW: 0.34, gap: 0.32 },
    pal: {
      name: 'Podium',
      dark: { bg: '#0f100d', surface: '#181914', line: '#2a2b23', ink: '#ece8dc', ink2: '#b6b1a2', ink3: '#807c6f', acc: '#ff6a2b' },
      light: { bg: '#efeadf', surface: '#f9f6ef', line: '#ddd6c5', ink: '#1b1a15', ink2: '#514d43', ink3: '#847f71', acc: '#cc4a0e' },
      swatches: [
        ['Graphite', '#0f100d'], ['Graphite 2', '#181914'], ['Paper', '#efeadf'], ['Ink', '#1b1a15'],
        ['Bone', '#ece8dc'], ['Ember', '#ff6a2b'], ['Ember (light)', '#cc4a0e'], ['Stone', '#807c6f'],
      ],
    },
    ui: 'Instrument Sans', mono: 'Martian Mono',
  },
  {
    id: 'conduit',
    name: 'Conduit',
    num: 'II',
    line: 'The signal closes the loop.',
    rationale: [
      'A conductor in the electrical sense is the thing a signal travels through. The C is a closed circuit with one segment missing, and the accent dot is the event passing through the gap: events come in, the loop closes, and work goes out.',
      'This is the boldest and most legible cut: a heavy annulus with radial ends, in the tradition of infrastructure marks. The copper palette names the material and drops the generic “AI orange”.',
    ],
    mark: {
      ink: '<path d="M24.19 6.81a13 13 0 1 0 0 18.38l-3.53-3.53a8 8 0 1 1 0-11.32z"/>',
      acc: '<circle cx="25.8" cy="16" r="3.8"/>',
      box: [2.4, 3, 27.2, 26],
    },
    fav: {
      ink: '<path d="M12.1 3.4a6.5 6.5 0 1 0 0 9.2l-1.77-1.77a4 4 0 1 1 0-5.66z"/>',
      acc: '<circle cx="13" cy="8" r="2"/>',
    },
    word: { font: 'instrument-sans-latin-600-normal.woff', track: -0.025, label: 'Instrument Sans SemiBold' },
    lock: { h: 1.02, align: 'center', dy: 0, gap: 0.3 },
    stack: { markW: 0.24, gap: 0.3 },
    pal: {
      name: 'Copper',
      dark: { bg: '#0d1012', surface: '#151a1d', line: '#242b30', ink: '#e4e7e4', ink2: '#a9b0b0', ink3: '#717b7d', acc: '#e07a3f' },
      light: { bg: '#f1f1ed', surface: '#fafaf7', line: '#d9dad3', ink: '#14181b', ink2: '#454d52', ink3: '#788084', acc: '#b4561f' },
      swatches: [
        ['Slate', '#0d1012'], ['Slate 2', '#151a1d'], ['Fog', '#f1f1ed'], ['Ink', '#14181b'],
        ['Mist', '#e4e7e4'], ['Copper', '#e07a3f'], ['Copper (light)', '#b4561f'], ['Verdigris', '#3f9a86'],
      ],
    },
    ui: 'Instrument Sans', mono: 'Martian Mono',
  },
  {
    id: 'downbeat',
    name: 'Downbeat',
    num: 'III',
    line: 'The gesture before the sound.',
    rationale: [
      'This is the dashboard’s current baton, rebuilt rather than replaced. A single heavy stroke rises toward the dot where the beat lands. The cartoon grip and the hairline weight are gone, so it survives at 16px and in one colour.',
      'It has the lowest switching cost, since anyone who has seen the prototype already knows it. The trade-off is that a diagonal and a dot is a common shape, and here it carries the least meaning of the four.',
    ],
    mark: {
      ink: '<path d="M4.37 24.37l12.5-12.5a2.3 2.3 0 0 1 3.26 3.26l-12.5 12.5a2.3 2.3 0 0 1-3.26-3.26z"/>',
      acc: '<circle cx="24.1" cy="7.9" r="4.2"/>',
      box: [3.7, 3.7, 24.6, 24.6],
    },
    fav: {
      ink: '<path d="M1.88 12.28l5.4-5.4a1.3 1.3 0 0 1 1.84 1.84l-5.4 5.4a1.3 1.3 0 0 1-1.84-1.84z"/>',
      acc: '<circle cx="12" cy="4" r="2.5"/>',
    },
    word: { font: 'martian-mono-latin-500-normal.woff', track: -0.03, label: 'Martian Mono Medium' },
    lock: { h: 0.92, align: 'baseline', dy: 0.0, gap: 0.26 },
    stack: { markW: 0.2, gap: 0.3 },
    pal: {
      name: 'Signal',
      dark: { bg: '#111111', surface: '#1a1a1a', line: '#2b2b2b', ink: '#f4f2ed', ink2: '#b9b6ae', ink3: '#82807a', acc: '#ff5533' },
      light: { bg: '#fafaf7', surface: '#ffffff', line: '#e2e1dc', ink: '#111111', ink2: '#4a4946', ink3: '#86847e', acc: '#d93a1a' },
      swatches: [
        ['Black', '#111111'], ['Carbon', '#1a1a1a'], ['White', '#fafaf7'], ['Ink', '#111111'],
        ['Chalk', '#f4f2ed'], ['Vermilion', '#ff5533'], ['Vermilion (light)', '#d93a1a'], ['Grey', '#82807a'],
      ],
    },
    ui: 'Instrument Sans', mono: 'Martian Mono',
  },
  {
    id: 'signature',
    name: 'Signature',
    num: 'IV',
    line: 'The name is the mark.',
    rationale: [
      'This direction leads with the wordmark. “conductor.” is set in Instrument Serif roman, and the full stop is the downbeat. The monogram is the same c and dot, with a heavier cut for favicons.',
      'It is quiet, editorial and the easiest to apply anywhere, much as Stripe and Vercel lean on type. The downside is that a letter with a dot is a familiar move, and the mark has no idea of its own beyond the name.',
    ],
    mark: { glyph: { font: 'instrument-serif-latin-400-normal.woff', ch: 'c', size: 39, x: 4.95, base: 26 }, acc: '<circle cx="22.75" cy="22.9" r="3.4"/>', box: [5.85, 5.9, 20.3, 20.45] },
    fav: { glyph: { size: 22, x: 1.1, base: 13.4, bold: 0.9 }, acc: '<circle cx="12.9" cy="11.3" r="2.1"/>' },
    word: { font: 'instrument-serif-latin-400-normal.woff', track: -0.005, label: 'Instrument Serif', period: true },
    lock: { h: 0.82, align: 'baseline', dy: 0, gap: 0.3 },
    stack: { markW: 0.2, gap: 0.3 },
    pal: {
      name: 'Brass',
      dark: { bg: '#16140f', surface: '#1f1c15', line: '#332e23', ink: '#f1ead9', ink2: '#bdb39c', ink3: '#8a8170', acc: '#e0b25a' },
      light: { bg: '#f4efe4', surface: '#fbf8f1', line: '#e0d7c4', ink: '#1d1b16', ink2: '#544e42', ink3: '#8a8170', acc: '#9a6b12' },
      swatches: [
        ['Umber', '#16140f'], ['Umber 2', '#1f1c15'], ['Vellum', '#f4efe4'], ['Ink', '#1d1b16'],
        ['Ivory', '#f1ead9'], ['Brass', '#e0b25a'], ['Brass (light)', '#9a6b12'], ['Taupe', '#8a8170'],
      ],
    },
    ui: 'Instrument Sans', mono: 'Martian Mono',
  },
];

export const RECOMMENDED = 'fermata';
