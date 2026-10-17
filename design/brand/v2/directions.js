// The three v2 system directions. Each is a value set for the SAME semantic token names
// as tokens/tokens.json (--color-bg-*, --color-text-*, --color-status-*, --font-family-*,
// --radius-*, --shadow-*), plus a few "personality" tokens (--d-*). The mocks are written
// once against those names; a direction is only values. That is the point: whichever
// direction ships, the token architecture, generator and contrast tooling stay.

const STATUS = (o) => Object.fromEntries(Object.entries(o).flatMap(([k, [fg, bg, solid]]) => [
  [`--color-status-${k}-fg`, fg], [`--color-status-${k}-bg`, bg], [`--color-status-${k}-solid`, solid],
]));

export const DIRS = {
  daylight: {
    name: 'Daylight',
    tag: 'Warm, light-first, written like a good status note.',
    pov: 'conductor as a calm colleague who tells you how things are going in a sentence before showing you a table. Warm paper surfaces and a legibility-first humanist face (Atkinson Hyperlegible, made by the Braille Institute for low-vision readers) carry most of the weight. A soft, friendly serif (Fraunces) gives the brand its voice in headings. Colour is reserved for things that need you. Cards are rounded and roomy, and every list opens with a plain-English summary. The fermata arch becomes a small sunrise motif for empty states and onboarding. This is the newcomer-friendly end; compact density keeps it workable at scale.',
    fonts: {
      display: ['Fraunces', 'SIL OFL 1.1', 'Headings, 600, soft axis on, upright, never italic'],
      sans: ['Atkinson Hyperlegible Next', 'SIL OFL 1.1', 'All UI and body text; 400 / 600 / 700'],
      mono: ['Atkinson Hyperlegible Mono', 'SIL OFL 1.1', 'Only code, ids, YAML, logs'],
    },
    shape: 'Rounded: 14px cards, 10px controls, pill badges and soft warm shadows.',
    space: 'Generous: 8px base, 20–24px card padding, 48px rows (36 compact).',
    icons: 'Lucide at 1.75 stroke with rounded joins, plus the sunrise arch as an illustration motif.',
    data: 'Sentence first (“18 of 20 runs went fine”), then one simple chart with direct labels. Timelines read top to bottom, like a story.',
    light: {
      '--color-bg-canvas': '#f7f3ec', '--color-bg-sunken': '#efe9df', '--color-bg-surface': '#fffdf9', '--color-bg-raised': '#ffffff', '--color-bg-overlay': '#ffffff', '--color-bg-rail': '#f1ebe1',
      '--color-border-subtle': '#ece5d8', '--color-border-default': '#ddd3c2', '--color-border-control': '#8f8577',
      '--color-text-primary': '#2a231c', '--color-text-secondary': '#584e43', '--color-text-tertiary': '#6e6457', '--color-text-disabled': '#b4aa9b',
      '--color-accent-default': '#cc4a0e', '--color-accent-hover': '#a33901', '--color-text-on-accent': '#ffffff', '--color-text-accent': '#a33901', '--color-accent-subtle': '#fbe8dd',
      ...STATUS({ success: ['#2c7148', '#e5f1e7', '#3f9a62'], warning: ['#855600', '#f9eccd', '#ad770a'], danger: ['#b02c37', '#fbe5e4', '#d4434e'], running: ['#2a6699', '#e2edf7', '#3f86c4'], info: ['#6648ab', '#eee8f8', '#8466c9'], neutral: ['#6e6457', '#efeae2', '#8f8577'] }),
      '--color-code-key': '#8a4b00', '--color-code-string': '#2c7148', '--color-code-number': '#2a6699', '--color-code-comment': '#6e6457',
      '--shadow-1': '0 1px 2px rgb(60 40 20 / .06), 0 6px 18px -10px rgb(60 40 20 / .16)', '--shadow-2': '0 2px 4px rgb(60 40 20 / .06), 0 18px 40px -18px rgb(60 40 20 / .28)',
    },
    dark: {
      '--color-bg-canvas': '#1d1915', '--color-bg-sunken': '#16130f', '--color-bg-surface': '#26211c', '--color-bg-raised': '#2e2822', '--color-bg-overlay': '#36302a', '--color-bg-rail': '#211c18',
      '--color-border-subtle': '#352e27', '--color-border-default': '#463e35', '--color-border-control': '#8d8274',
      '--color-text-primary': '#f4eee4', '--color-text-secondary': '#d3c9ba', '--color-text-tertiary': '#ada291', '--color-text-disabled': '#6b6155',
      '--color-accent-default': '#ff7a3d', '--color-accent-hover': '#ff9563', '--color-text-on-accent': '#2a1206', '--color-text-accent': '#ff9563', '--color-accent-subtle': '#3d2416',
      ...STATUS({ success: ['#7fcf9b', '#1f3326', '#5fb57f'], warning: ['#f0c065', '#3a2e15', '#d9a63c'], danger: ['#ff8f8f', '#3d1f1f', '#ef6767'], running: ['#8cc2ee', '#1c2c3b', '#5ea5df'], info: ['#bfa8f5', '#2b2440', '#9b82e3'], neutral: ['#ada291', '#2e2822', '#7f7466'] }),
      '--color-code-key': '#f0c065', '--color-code-string': '#7fcf9b', '--color-code-number': '#8cc2ee', '--color-code-comment': '#ada291',
      '--shadow-1': '0 1px 2px rgb(0 0 0 / .3)', '--shadow-2': '0 18px 40px -18px rgb(0 0 0 / .6)',
    },
    vars: {
      '--font-family-display': "'Fraunces', Georgia, serif", '--font-family-sans': "'Atkinson Hyperlegible Next', system-ui, sans-serif", '--font-family-mono': "'Atkinson Hyperlegible Mono', ui-monospace, monospace",
      '--d-title-weight': '600', '--d-title-style': 'normal', '--d-title-tracking': '-0.015em', '--d-title-variation': "'SOFT' 100, 'opsz' 72", '--d-h1': '34px', '--d-h2': '21px',
      '--d-body': '15px', '--d-small': '13px', '--d-label-transform': 'none', '--d-label-tracking': '0', '--d-label-size': '13px', '--d-label-weight': '600',
      '--radius-card': '14px', '--radius-control': '10px', '--radius-badge': '999px', '--d-box': '10px', '--d-card-border': '1px solid var(--color-border-subtle)', '--d-pad': '22px', '--d-row': '52px', '--d-gap': '20px',
      '--d-section-rule': '0', '--d-rail-w': '236px', '--d-icon-stroke': '1.75',
    },
  },

  broadsheet: {
    name: 'Broadsheet',
    tag: 'Editorial information design: the front page of your infrastructure.',
    pov: 'conductor as the morning paper for everything your bots and workflows did. Headlines say what happened, a one-line “dek” in a reading serif says why it matters, and dense, ruled tables and small multiples carry the detail with newspaper discipline. Archivo (a grotesk drawn for news and high-volume text) gives real weight contrast, from 800 headlines to 400 body. Colour is almost absent: ink on newsprint, so the one ember signal and the rare red failure jump off the page. Corners are square, there are no shadows and rules do the grouping instead of boxes. Its dark mode is an inky “night edition”, not a terminal. It is the most information-dense, and the power user’s favourite.',
    fonts: {
      display: ['Archivo', 'SIL OFL 1.1', 'Headlines at 800 with tight tracking; section heads at 800 caps'],
      sans: ['Archivo', 'SIL OFL 1.1', 'UI and body at 400 / 500 / 600'],
      serif: ['Newsreader Italic', 'SIL OFL 1.1', 'One-line summaries (“deks”) and pull quotes only'],
      mono: ['JetBrains Mono', 'SIL OFL 1.1', 'Only code, ids, YAML, logs'],
    },
    shape: 'Square: 2px radii, hairline and 3px ink rules, no shadows and no cards.',
    space: 'Tight and rhythmic: a 4px baseline, 40px rows (30 compact) and a visible column grid.',
    icons: 'Few icons, 1.5px, only where they carry state. Data graphics (sparklines, small multiples) are the illustration.',
    data: 'Front page first: headline numbers with sparklines, a “since you last looked” digest, then tables. It is the strictest about tabular alignment.',
    light: {
      '--color-bg-canvas': '#fbfaf7', '--color-bg-sunken': '#f1efe9', '--color-bg-surface': '#fbfaf7', '--color-bg-raised': '#ffffff', '--color-bg-overlay': '#ffffff', '--color-bg-rail': '#fbfaf7',
      '--color-border-subtle': '#e6e3da', '--color-border-default': '#cfcbbf', '--color-border-control': '#858175', '--d-rule-strong': '#111111',
      '--color-text-primary': '#121212', '--color-text-secondary': '#45433e', '--color-text-tertiary': '#66625a', '--color-text-disabled': '#b0aca2',
      '--color-accent-default': '#cc4a0e', '--color-accent-hover': '#a33901', '--color-text-on-accent': '#ffffff', '--color-text-accent': '#a33901', '--color-accent-subtle': '#fbeae1',
      ...STATUS({ success: ['#1f6b43', '#e8f1eb', '#2f8a58'], warning: ['#855600', '#f7eed8', '#b88213'], danger: ['#ad2230', '#f8e5e5', '#cc3442'], running: ['#1d5e99', '#e5eef7', '#2f78bd'], info: ['#5b4aa6', '#eeebf7', '#7b67c4'], neutral: ['#66625a', '#efede8', '#8a867b'] }),
      '--color-code-key': '#7a4a00', '--color-code-string': '#1f6b43', '--color-code-number': '#1d5e99', '--color-code-comment': '#66625a',
      '--shadow-1': 'none', '--shadow-2': '0 12px 32px -16px rgb(0 0 0 / .25)',
    },
    dark: {
      '--color-bg-canvas': '#11141a', '--color-bg-sunken': '#0c0e13', '--color-bg-surface': '#11141a', '--color-bg-raised': '#181c24', '--color-bg-overlay': '#1e232d', '--color-bg-rail': '#11141a',
      '--color-border-subtle': '#232934', '--color-border-default': '#353d4a', '--color-border-control': '#7d8696', '--d-rule-strong': '#ebe8e1',
      '--color-text-primary': '#ebe8e1', '--color-text-secondary': '#c3c0b8', '--color-text-tertiary': '#9c9a92', '--color-text-disabled': '#5c616c',
      '--color-accent-default': '#ff6a2b', '--color-accent-hover': '#ff8253', '--color-text-on-accent': '#1d0c03', '--color-text-accent': '#ff8253', '--color-accent-subtle': '#33201a',
      ...STATUS({ success: ['#73c795', '#16271f', '#4fae78'], warning: ['#e9bb5e', '#2c2516', '#cf9c33'], danger: ['#ff8a92', '#33191d', '#ef5f6b'], running: ['#86b9ee', '#172333', '#5a9be0'], info: ['#b6a6f2', '#221f36', '#9282e0'], neutral: ['#9c9a92', '#1d2028', '#6f727b'] }),
      '--color-code-key': '#e9bb5e', '--color-code-string': '#73c795', '--color-code-number': '#86b9ee', '--color-code-comment': '#9c9a92',
      '--shadow-1': 'none', '--shadow-2': '0 12px 32px -16px rgb(0 0 0 / .7)',
    },
    vars: {
      '--font-family-display': "'Archivo', system-ui, sans-serif", '--font-family-sans': "'Archivo', system-ui, sans-serif", '--font-family-mono': "'JetBrains Mono', ui-monospace, monospace", '--font-family-serif': "'Newsreader', Georgia, serif",
      '--d-title-weight': '800', '--d-title-style': 'normal', '--d-title-tracking': '-0.025em', '--d-title-variation': 'normal', '--d-h1': '38px', '--d-h2': '19px',
      '--d-body': '14px', '--d-small': '12.5px', '--d-label-transform': 'uppercase', '--d-label-tracking': '0.07em', '--d-label-size': '11px', '--d-label-weight': '700',
      '--radius-card': '2px', '--radius-control': '3px', '--radius-badge': '2px', '--d-box': '2px', '--d-card-border': '0', '--d-pad': '0px', '--d-row': '40px', '--d-gap': '28px',
      '--d-section-rule': '3px', '--d-rail-w': '0px', '--d-icon-stroke': '1.5',
    },
  },

  score: {
    name: 'Score',
    tag: 'Work as a score: lanes of activity over time, with personality.',
    pov: 'conductor shows its work the way a conductor reads it: every workflow and bot is a lane, time runs left to right, and each run is a note. Pattern-finding is spatial: a failure is a red cross on a lane, and a waiting approval is an ember diamond that sits on “now”. A characterful grotesk (Bricolage Grotesque) gives the brand a voice, and a friendly geometric face (Figtree) carries the UI. Surfaces are tinted (pale lilac by day, deep indigo “concert hall” by night) rather than grey, and the shapes are round. Lanes get calm categorical colours for identity; status is shape plus colour. This is the most distinctive and visual of the three.',
    fonts: {
      display: ['Bricolage Grotesque', 'SIL OFL 1.1', 'Headings at 700, tight tracking'],
      sans: ['Figtree', 'SIL OFL 1.1', 'UI and body at 400 / 500 / 700'],
      mono: ['Geist Mono', 'SIL OFL 1.1', 'Only code, ids, YAML, logs'],
    },
    shape: 'Round: 12px cards, pill controls and badges, notes as circles and diamonds.',
    space: 'Medium: 8px base, 28px lanes and 44px rows (32 compact).',
    icons: 'Lucide at 2px, plus the staff (five hairlines) as a background motif and lane notes as the illustration.',
    data: 'Timeline first: the score on the overview, a Gantt for runs, and duration lanes in history. Categorical colour encodes which workflow a run belongs to; status is encoded by shape.',
    light: {
      '--color-bg-canvas': '#f3f2f8', '--color-bg-sunken': '#e9e7f2', '--color-bg-surface': '#ffffff', '--color-bg-raised': '#ffffff', '--color-bg-overlay': '#ffffff', '--color-bg-rail': '#1c1a33',
      '--color-border-subtle': '#e5e2ef', '--color-border-default': '#d2cee2', '--color-border-control': '#7f799e',
      '--color-text-primary': '#1c1a33', '--color-text-secondary': '#46435f', '--color-text-tertiary': '#625e82', '--color-text-disabled': '#b2aec8',
      '--color-accent-default': '#cc4a0e', '--color-accent-hover': '#a33901', '--color-text-on-accent': '#ffffff', '--color-text-accent': '#a33901', '--color-accent-subtle': '#fbe9df',
      ...STATUS({ success: ['#1c7350', '#e3f3ec', '#2f9a6d'], warning: ['#855600', '#f8eed6', '#ad7a0e'], danger: ['#b8203f', '#fbe5ea', '#dc3c5c'], running: ['#2b56c0', '#e6ecfb', '#4a74de'], info: ['#6243c0', '#efeafb', '#8466dd'], neutral: ['#625e82', '#eeedf4', '#8682a6'] }),
      '--color-code-key': '#8a4b00', '--color-code-string': '#1c7350', '--color-code-number': '#2b56c0', '--color-code-comment': '#625e82',
      '--shadow-1': '0 1px 2px rgb(28 26 51 / .06), 0 8px 24px -14px rgb(28 26 51 / .2)', '--shadow-2': '0 20px 44px -20px rgb(28 26 51 / .35)',
      '--d-rail-ink': '#eeedf8', '--d-rail-ink-2': '#a9a5cf', '--d-rail-on': '#2c2a4d',
    },
    dark: {
      '--color-bg-canvas': '#15142a', '--color-bg-sunken': '#100f21', '--color-bg-surface': '#1d1c36', '--color-bg-raised': '#252342', '--color-bg-overlay': '#2c2a4d', '--color-bg-rail': '#100f21',
      '--color-border-subtle': '#2a2849', '--color-border-default': '#3b385f', '--color-border-control': '#8a86b5',
      '--color-text-primary': '#eeedf8', '--color-text-secondary': '#c9c6e3', '--color-text-tertiary': '#a29fc6', '--color-text-disabled': '#5d5a86',
      '--color-accent-default': '#ff7438', '--color-accent-hover': '#ff9461', '--color-text-on-accent': '#24100a', '--color-text-accent': '#ff9461', '--color-accent-subtle': '#3a2433',
      ...STATUS({ success: ['#6fd3a4', '#17302f', '#3fb985'], warning: ['#f1c368', '#33291f', '#d9a53f'], danger: ['#ff8aa0', '#3a1c33', '#f05a79'], running: ['#94b2ff', '#1f2752', '#6a8df0'], info: ['#c1a9ff', '#2a2350', '#9c80f2'], neutral: ['#a29fc6', '#252342', '#6f6b98'] }),
      '--color-code-key': '#f1c368', '--color-code-string': '#6fd3a4', '--color-code-number': '#94b2ff', '--color-code-comment': '#a29fc6',
      '--shadow-1': '0 1px 2px rgb(0 0 0 / .3)', '--shadow-2': '0 20px 44px -20px rgb(0 0 0 / .65)',
      '--d-rail-ink': '#eeedf8', '--d-rail-ink-2': '#a29fc6', '--d-rail-on': '#252342',
    },
    vars: {
      '--font-family-display': "'Bricolage Grotesque', system-ui, sans-serif", '--font-family-sans': "'Figtree', system-ui, sans-serif", '--font-family-mono': "'Geist Mono', ui-monospace, monospace",
      '--d-title-weight': '700', '--d-title-style': 'normal', '--d-title-tracking': '-0.03em', '--d-title-variation': 'normal', '--d-h1': '34px', '--d-h2': '19px',
      '--d-body': '14.5px', '--d-small': '13px', '--d-label-transform': 'none', '--d-label-tracking': '0', '--d-label-size': '12.5px', '--d-label-weight': '700',
      '--radius-card': '14px', '--radius-control': '999px', '--radius-badge': '999px', '--d-box': '12px', '--d-card-border': '1px solid var(--color-border-subtle)', '--d-pad': '20px', '--d-row': '46px', '--d-gap': '18px',
      '--d-section-rule': '0', '--d-rail-w': '220px', '--d-icon-stroke': '2',
    },
  },
};

