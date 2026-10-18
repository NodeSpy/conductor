// Generates CSS and a contrast report from tokens/tokens.json (the source of truth).
//
//   node tools/tokens.mjs          writes tokens/tokens.css, tokens/prototype-compat.css,
//                                  tokens/contrast.json, tokens/CONTRAST.md and tokens/migration.json
//   node tools/tokens.mjs --check  only verifies contrast; exits 1 if any declared pair fails
//
// Every token that is used as text or as a control edge declares where it sits in
// $extensions["org.conductor.contrast"] = { on: [token paths], min, aaaOn? }. A path like
// "a@b" means "a composited over b" (for translucent washes). Each pair is checked in
// both themes; anything under its minimum fails the run.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { contrast } from './oklch.mjs';
import { privacyCheck } from './privacy-check.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const dir = path.resolve(here, '..', 'tokens');
const J = JSON.parse(fs.readFileSync(path.join(dir, 'tokens.json'), 'utf8'));
const checkOnly = process.argv.includes('--check');

// ---------- flatten ----------
const tokens = new Map();
(function walk(node, p, inheritedType) {
  const type = node.$type ?? inheritedType;
  if ('$value' in node) {
    tokens.set(p.join('.'), { path: p.join('.'), type, value: node.$value, modes: node.$extensions?.mode, ext: node.$extensions ?? {}, desc: node.$description });
    return;
  }
  for (const [k, v] of Object.entries(node)) if (!k.startsWith('$') && v && typeof v === 'object') walk(v, [...p, k], type);
})(J, [], undefined);

const FAMILY = { dark: 'theme', light: 'theme', comfortable: 'density', compact: 'density', standard: 'motion', reduced: 'motion' };
const familyOf = (t) => (t.modes ? FAMILY[Object.keys(t.modes)[0]] : null);
const isAlias = (v) => typeof v === 'string' && /^\{[^}]+\}$/.test(v);
const target = (v) => v.slice(1, -1);
const DEFAULT_CTX = { theme: 'dark', density: 'comfortable', motion: 'standard' };

function raw(t, ctx) {
  const fam = familyOf(t);
  return fam ? t.modes[ctx[fam]] : t.value;
}
function resolve(p, ctx = DEFAULT_CTX, seen = []) {
  const t = tokens.get(p);
  if (!t) throw new Error(`unknown token ${p} (from ${seen.join(' → ')})`);
  if (seen.includes(p)) throw new Error(`alias cycle: ${[...seen, p].join(' → ')}`);
  const v = raw(t, ctx);
  if (isAlias(v)) return resolve(target(v), ctx, [...seen, p]);
  return { type: t.type, value: deepResolve(v, ctx, [...seen, p]) };
}
function deepResolve(v, ctx, seen) {
  if (isAlias(v)) return resolve(target(v), ctx, seen).value;
  if (Array.isArray(v)) return v.map((x) => deepResolve(x, ctx, seen));
  if (v && typeof v === 'object') return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, deepResolve(x, ctx, seen)]));
  return v;
}

// ---------- CSS names and values ----------
function cssName(p) {
  const [tier, ...rest] = p.split('.');
  if (tier === 'primitive' && rest[0] === 'color') return '--palette-' + rest.slice(1).join('-');
  return '--' + rest.join('-');
}
const hexOf = (c) => c.hex ?? '#' + c.components.map((x) => Math.round(x * 255).toString(16).padStart(2, '0')).join('');
function cssColor(c) {
  if (c.alpha == null || c.alpha >= 1) return hexOf(c);
  const [r, g, b] = c.components.map((x) => Math.round(x * 255));
  return `rgb(${r} ${g} ${b} / ${c.alpha})`;
}
const dim = (d) => (d.value === 0 ? '0' : `${d.value}${d.unit}`);
const fam = (f) => (Array.isArray(f) ? f : [f]).map((x) => (/[\s\d]/.test(x) && !x.includes('-') ? `'${x}'` : /\s/.test(x) ? `'${x}'` : x)).join(', ');
function cssValue(type, v) {
  if (isAlias(v)) return `var(${cssName(target(v))})`;
  switch (type) {
    case 'color': return cssColor(v);
    case 'dimension': case 'duration': return dim(v);
    case 'cubicBezier': return `cubic-bezier(${v.join(', ')})`;
    case 'fontFamily': return fam(v);
    case 'shadow': {
      const list = Array.isArray(v) ? v : [v];
      if (!list.length) return 'none';
      return list.map((s) => `${s.inset ? 'inset ' : ''}${dim(s.offsetX)} ${dim(s.offsetY)} ${dim(s.blur)} ${dim(s.spread)} ${isAlias(s.color) ? cssValue('color', s.color) : cssColor(s.color)}`).join(', ');
    }
    default: return String(v);
  }
}
// Typography composites become one property per facet plus a `font` shorthand.
function typeDecls(t) {
  const n = cssName(t.path), v = t.value, x = t.ext['org.conductor.type'] ?? {};
  const out = [
    [`${n}-family`, cssValue('fontFamily', v.fontFamily)],
    [`${n}-size`, cssValue('dimension', v.fontSize)],
    [`${n}-weight`, cssValue('fontWeight', v.fontWeight)],
    [`${n}-line`, String(v.lineHeight)],
    [`${n}-tracking`, `${v.letterSpacing.value}${v.letterSpacing.unit}`],
    [`${n}-style`, x.fontStyle ?? 'normal'],
    [`${n}-transform`, x.textTransform ?? 'none'],
    [`${n}-variation`, x.fontVariation ?? 'normal'],
    [n, `var(${n}-style) var(${n}-weight) var(${n}-size)/var(${n}-line) var(${n}-family)`],
  ];
  return out;
}
function decls(t, value) {
  if (t.type === 'typography') return typeDecls(t);
  return [[cssName(t.path), cssValue(t.type, value)]];
}

// ---------- emit tokens.css ----------
const blocks = { root: [], alias: [], dark: [], light: [], comfortable: [], compact: [], standard: [], reduced: [] };
for (const t of tokens.values()) {
  const f = familyOf(t);
  if (f) {
    for (const [m, v] of Object.entries(t.modes)) blocks[m].push(...decls(t, v));
  } else if (t.path.startsWith('primitive.') || !JSON.stringify(t.value).includes('{')) {
    blocks.root.push(...decls(t, t.value));
  } else {
    // An alias outside the primitive tier may depend on a mode, so it is re-declared on
    // every element that switches theme or density (custom properties resolve where declared).
    blocks.alias.push(...decls(t, t.value));
  }
}
const body = (list) => list.map(([k, v]) => `  ${k}: ${v};`).join('\n');
const now = new Date().toISOString().slice(0, 10);
const css = `/* conductor design tokens. GENERATED by design/brand/tools/tokens.mjs from tokens/tokens.json: do not edit.
 *
 * Themes:   follows prefers-color-scheme; force with data-theme="dark" | "light" on any element
 *           (an explicit data-theme always beats the OS). Pages should also carry
 *           <meta name="color-scheme" content="light dark"> so first paint is right.
 * Density:  comfortable by default; data-density="compact" on any element.
 * Motion:   follows prefers-reduced-motion; force with data-motion="reduced" | "standard".
 * Fonts:    load design/brand/fonts/fonts.css (or your own @font-face) alongside this file.
 */

:root {
${body(blocks.root)}
}

/* ---------- themes ----------
 * Order and color-scheme matter (see BRAND.md § Theming robustness):
 *  - With no data-theme, the page follows the OS and says so (light dark). Declaring a
 *    color-scheme also stops browsers that auto-darken undeclared pages (Chromium force-dark,
 *    some in-app WebViews) from repainting our colours.
 *  - An explicit data-theme, on <html> or any element, always wins: its values are declared
 *    on the element itself, after the media query, and \`only\` forbids the UA from adjusting it. */
:root {
  color-scheme: light dark;
}
:root, [data-theme="light"] {
${body(blocks.light)}
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
${body(blocks.dark).replace(/^/gm, '  ')}
  }
}
[data-theme="light"] {
  color-scheme: only light;
}
[data-theme="dark"] {
  color-scheme: only dark;
${body(blocks.dark)}
}

/* ---------- density ---------- */
:root, [data-density="comfortable"] {
${body(blocks.comfortable)}
}
[data-density="compact"] {
${body(blocks.compact)}
}

/* ---------- motion ---------- */
:root, [data-motion="standard"] {
${body(blocks.standard)}
}
@media (prefers-reduced-motion: reduce) {
  :root:not([data-motion="standard"]) {
${body(blocks.reduced).replace(/^/gm, '  ')}
  }
}
[data-motion="reduced"] {
${body(blocks.reduced)}
}

/* ---------- aliases (re-resolved wherever theme, density or motion changes) ---------- */
:root, [data-theme], [data-density], [data-motion] {
${body(blocks.alias)}
}
`;

// ---------- prototype compatibility shim ----------
// Old dashboard-prototype variable → new token. Lets the prototype adopt tokens.css first
// and migrate selector by selector. Notes flag where the meaning (not just the name) changed.
export const MIGRATION = [
  ['--font-ui', '--font-family-sans', 'Instrument Sans → Atkinson Hyperlegible Next. Body goes 13.5 → 15px (--type-body-m).'],
  ['--font-title', '--font-family-serif', 'Instrument Serif italic → Fraunces SemiBold, upright. Use the --type-h1 / --type-h2 shorthands plus their -tracking and -variation. Long or generated titles move to --type-h3 (sans).'],
  ['--font-mono', '--font-family-mono', 'Martian Mono → Atkinson Hyperlegible Mono, and only for literal code, ids, YAML and logs. Strip cells, crumbs, counts, times and costs move to the sans with tabular-nums.'],
  ['--r-xs', '--radius-badge', '3 → 3: tags only. Nothing is a pill.'],
  ['--r-sm', '--radius-control', '5 → 10.'],
  ['--r', '--radius-card', '8 → 14.'],
  ['--r-lg', '--radius-overlay', '12 → 18.'],
  ['--rail-w', '--size-rail', '224 → 236 (212 compact).'],
  ['--strip-h', '--size-strip', '34 → 60 (48 compact): the mono status strip becomes a quiet top bar (search, health, avatar).'],
  ['--ease', '--motion-easing-out', ''],
  ['--t-fast', '--motion-duration-fast', 'Drops to 0 under reduced motion.'],
  ['--t', '--motion-duration-base', '220 → 200ms.'],
  ['--bg', '--color-bg-canvas', 'Dark #0f100d → cocoa #1d1915; light #efeadf → cream #f7f3ec. Daylight is light-first.'],
  ['--bg-1', '--color-bg-rail', 'Dark #13140f → #211c18; light #f4f0e6 → #f1ebe1.'],
  ['--bg-2', '--color-bg-surface', 'Dark #181914 → #26211c; light #f9f6ef → #fffdf9.'],
  ['--bg-3', '--color-bg-raised', 'Dark #1f201a → #2e2822; light #fffdf8 → #ffffff.'],
  ['--bg-4', '--color-bg-overlay', 'Dark #272821 → #36302a; light #ffffff.'],
  ['--line', '--color-border-subtle', 'Dark #2a2b23 → #352e27; light #ddd6c5 → #ece5d8.'],
  ['--line-2', '--color-border-default', 'Inputs and checkboxes use --color-border-control (3:1) instead.'],
  ['--line-3', '--color-border-strong', ''],
  ['--ink', '--color-text-primary', 'Dark #ece8dc → #f4eee4; light #1b1a15 → #2a231c.'],
  ['--ink-2', '--color-text-secondary', 'Dark #b6b1a2 → #ddd3c2; light #514d43 → #584e43.'],
  ['--ink-3', '--color-text-tertiary', 'Changed for AA: dark #807c6f → #ada291, light #847f71 → #6e6457. The old values failed 4.5:1 on cards.'],
  ['--ink-4', '--color-text-disabled', 'Disabled controls only.'],
  ['--accent', '--color-accent-default', 'Fills only (buttons, counts, bars, the logo dot). Where it colours text (links, .cell.alert, crumbs:hover; 43 places), use --color-text-accent.'],
  ['--accent-2', '--color-accent-hover', ''],
  ['--accent-ink', '--color-text-on-accent', ''],
  ['--accent-wash', '--color-accent-subtle', ''],
  ['--ok', '--color-status-success-fg', 'Dots and bars: --color-status-success-solid. Passing rows stay ink: colour is for what needs attention.'],
  ['--run', '--color-status-running-fg', 'Dots and bars: --color-status-running-solid.'],
  ['--wait', '--color-status-warning-fg', 'Dots and bars: --color-status-warning-solid.'],
  ['--fail', '--color-status-danger-fg', 'Dots and bars: --color-status-danger-solid.'],
  ['--idle', '--color-status-neutral-fg', ''],
  ['--shadow', '--color-status-info-fg', 'Shadow mode is shown as info (observes, never acts) with its own glyph.'],
  ['--ok-wash', '--color-status-success-bg', ''],
  ['--run-wash', '--color-status-running-bg', ''],
  ['--wait-wash', '--color-status-warning-bg', ''],
  ['--fail-wash', '--color-status-danger-bg', ''],
  ['--idle-wash', '--color-status-neutral-bg', ''],
  ['--shadow-wash', '--color-status-info-bg', ''],
  ['--code-key', '--color-code-key', ''],
  ['--code-str', '--color-code-string', ''],
  ['--code-num', '--color-code-number', ''],
  ['--code-com', '--color-code-comment', 'Light #9a9482 → #6e6457 (comments were 2.9:1).'],
  ['--code-anchor', '--color-code-anchor', ''],
  ['--code-bool', '--color-code-bool', ''],
  ['--dot', '--color-decor-dot', 'Daylight drops the dot grid on app surfaces; keep it for brand pages only.'],
  ['--elev', '--shadow-2', 'Warm, softer shadows in light.'],
  ['--elev-pop', '--shadow-3', 'The old value included a 1px ring; add border: 1px solid var(--color-border-default).'],
  ['--grain-op', '--opacity-grain', 'Daylight drops the film grain on app surfaces.'],
];
const compat = `/* dashboard-prototype → conductor tokens. GENERATED by design/brand/tools/tokens.mjs.
 * Load after tokens.css and instead of the theme blocks in css/app.css; then replace the
 * old names selector by selector and delete this file. See BRAND.md § Migrating the dashboard. */
:root, [data-theme], [data-density] {
${MIGRATION.map(([o, n]) => `  ${o}: ${o === '--elev-pop' ? 'var(--shadow-3), 0 0 0 1px var(--color-border-default)' : `var(${n})`};`).join('\n')}
}
`;

// ---------- contrast ----------
function composite(fg, bg) {
  const a = fg.alpha ?? 1;
  const c = fg.components.map((x, i) => x * a + bg.components[i] * (1 - a));
  return { colorSpace: 'srgb', components: c, hex: '#' + c.map((x) => Math.round(x * 255).toString(16).padStart(2, '0')).join('') };
}
function colorAt(spec, ctx) {
  if (spec.includes('@')) {
    const [over, base] = spec.split('@');
    return composite(resolve(over, ctx).value, colorAt(base, ctx));
  }
  const c = resolve(spec, ctx).value;
  if (c.alpha != null && c.alpha < 1) throw new Error(`${spec} is translucent: declare it as ${spec}@<backdrop>`);
  return c;
}
const results = [];
for (const t of tokens.values()) {
  const spec = t.ext['org.conductor.contrast'];
  if (!spec) continue;
  for (const theme of ['dark', 'light']) {
    const ctx = { ...DEFAULT_CTX, theme };
    const fg = colorAt(t.path, ctx);
    for (const on of spec.on) {
      const bg = colorAt(on, ctx);
      const ratio = contrast(hexOf(fg), hexOf(bg));
      const aaa = (spec.aaaOn ?? []).includes(on) || spec.min >= 7;
      const need = aaa ? Math.max(7, spec.min) : spec.min;
      let level = ratio >= 7 ? 'AAA' : ratio >= 4.5 ? 'AA' : ratio >= 3 ? 'AA large / UI' : 'below 3';
      const pass = spec.min === 0 || ratio >= need;
      if (spec.min === 0) level = 'exempt';
      results.push({ token: t.path, css: cssName(t.path), theme, on, fg: hexOf(fg), bg: hexOf(bg), ratio: +ratio.toFixed(2), min: need, level, pass });
    }
  }
}
const fails = results.filter((r) => !r.pass);
const short = (p) => p.replace(/^semantic\.color\./, '').replace(/^component\./, 'component.');
const md = [
  '# Contrast report',
  '',
  `GENERATED by \`tools/tokens.mjs\` from the contrast intents declared in \`tokens.json\` (${results.length} pairs, ${now}).`,
  'Minimums: 7 (AAA) for primary text and anywhere marked `aaaOn`; 4.5 (AA) for all other text; 3 for control edges, focus rings, icons and data marks (WCAG 1.4.11). `a@b` means a translucent `a` composited over `b`.',
  '',
  fails.length ? `**${fails.length} FAILING pairs.**` : '**Every declared pair passes.**',
  '',
  '| Token | On | Dark | Light | Min |',
  '|---|---|---|---|---|',
  ...[...new Set(results.map((r) => r.token + '|' + r.on))].map((key) => {
    const [tok, on] = key.split('|');
    const d = results.find((r) => r.token === tok && r.on === on && r.theme === 'dark');
    const l = results.find((r) => r.token === tok && r.on === on && r.theme === 'light');
    const cell = (r) => `${r.pass ? '' : '**FAIL** '}${r.ratio.toFixed(2)} ${r.level}`;
    return `| \`${short(tok)}\` | \`${short(on)}\` | ${cell(d)} | ${cell(l)} | ${d.level === 'exempt' ? 'exempt' : d.min} |`;
  }),
  '',
].join('\n');

if (!checkOnly) {
  fs.writeFileSync(path.join(dir, 'tokens.css'), css);
  fs.writeFileSync(path.join(dir, 'prototype-compat.css'), compat);
  fs.writeFileSync(path.join(dir, 'contrast.json'), JSON.stringify(results, null, 1) + '\n');
  fs.writeFileSync(path.join(dir, 'migration.json'), JSON.stringify(MIGRATION.map(([from, to, note]) => ({ from, to, note })), null, 1) + '\n');
  fs.writeFileSync(path.join(dir, 'CONTRAST.md'), md);
}
console.log(`${tokens.size} tokens · ${results.length} contrast pairs · ${fails.length} failing`);
for (const f of fails) console.log(`  FAIL ${f.theme.padEnd(5)} ${short(f.token)} on ${short(f.on)}: ${f.ratio} < ${f.min}`);
// The branch is public: no real names, home paths or private hosts anywhere under design/.
const privateHits = privacyCheck();
for (const h of privateHits) console.log(`  PRIVATE ${h.slice(0, 160)}`);
console.log(`privacy: ${privateHits.length} hits under design/`);
process.exit(fails.length || privateHits.length ? 1 : 0);
