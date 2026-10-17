// Builds every brand SVG from directions.mjs.
//   design/brand/directions/<id>/*.svg   one set per direction (for the presentation)
//   design/brand/assets/*.svg            the ready-to-use set for the recommended direction
// Wordmarks are converted to outlines, so no SVG depends on a font being installed.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import opentype from 'opentype.js';
import { optimize } from 'svgo';
import { DIRS, RECOMMENDED } from './directions.mjs';
import { LOGOS } from './logos.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..');
const r2 = (n) => Math.round(n * 100) / 100;

const fonts = {};
function font(file) {
  if (!fonts[file]) {
    const b = fs.readFileSync(path.join(here, 'fonts', file));
    fonts[file] = opentype.parse(b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength));
  }
  return fonts[file];
}

// opentype.js 2.0's toPathData() emits NaN for some coordinates; serialise the commands ourselves.
const pathData = (p) =>
  p.commands
    .map((c) =>
      c.type === 'Z' ? 'Z'
      : c.type === 'Q' ? `Q${r2(c.x1)} ${r2(c.y1)} ${r2(c.x)} ${r2(c.y)}`
      : c.type === 'C' ? `C${r2(c.x1)} ${r2(c.y1)} ${r2(c.x2)} ${r2(c.y2)} ${r2(c.x)} ${r2(c.y)}`
      : `${c.type}${r2(c.x)} ${r2(c.y)}`)
    .join('');

// Lay out text glyph by glyph (no shaping: lowercase latin needs none) with kerning and tracking.
function text(file, str, size, trackEm = 0) {
  const f = font(file);
  const glyphs = [...str].map((c) => f.charToGlyph(c));
  const all = new opentype.Path();
  const ds = [];
  let x = 0;
  glyphs.forEach((g, i) => {
    const p = g.getPath(x, 0, size);
    ds.push(pathData(p));
    all.extend(p);
    x += (g.advanceWidth * size) / f.unitsPerEm;
    if (i < glyphs.length - 1) x += ((f.getKerningValue(g, glyphs[i + 1]) || 0) * size) / f.unitsPerEm + trackEm * size;
  });
  const bb = all.getBoundingBox();
  return { d: ds.join(''), x1: bb.x1, y1: bb.y1, x2: bb.x2, y2: bb.y2, advance: x };
}

// The mark as {ink, acc, bold} element strings on its grid.
function markParts(dir, cut = 'mark') {
  const m = dir[cut];
  if (m.glyph) {
    const g = { ...dir.mark.glyph, ...m.glyph };
    const t = text(g.font, g.ch, g.size);
    return { ink: `<path transform="translate(${g.x} ${g.base})" d="${t.d}"/>`, acc: m.acc, bold: m.glyph.bold };
  }
  return { ink: m.ink, acc: m.acc };
}

function paint(parts, ink, acc) {
  const stroke = parts.bold ? ` stroke="${ink}" stroke-width="${parts.bold}" stroke-linejoin="round"` : '';
  return `<g fill="${ink}"${stroke}>${parts.ink}</g><g fill="${acc}">${parts.acc}</g>`;
}

function svg(viewBox, body, extra = '') {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${viewBox}" role="img" aria-label="conductor"${extra}><title>conductor</title>${body}</svg>`;
}

const svgoOpts = {
  multipass: true,
  floatPrecision: 2,
  plugins: [{ name: 'preset-default', params: { overrides: { inlineStyles: false, minifyStyles: false } } }],
};
// svgo drops role="img"; put it back so inlined SVGs announce as one image with their <title>.
const opt = (s) => (process.env.RAW ? s : optimize(s, svgoOpts).data.replace('<svg ', '<svg role="img" '));

const VARIANTS = (dir) => ({
  light: [dir.pal.light.ink, dir.pal.light.acc],
  dark: [dir.pal.dark.ink, dir.pal.dark.acc],
  mono: ['currentColor', 'currentColor'],
});

// Horizontal and stacked lockups. Units: wordmark set at 100.
function lockups(dir) {
  const W = dir.word;
  const t = text(W.font, 'conductor', 100, W.track);
  const per = W.period ? text(W.font, '.', 100) : null;
  const perX = t.advance + 0.5;
  const wordX2 = per ? perX + per.x2 : t.x2;
  const asc = -t.y1; // top of the ascenders (d, t)
  const xh = (font(W.font).tables.os2.sxHeight * 100) / font(W.font).unitsPerEm;
  const [bx, by, bw, bh] = dir.mark.box;
  const parts = markParts(dir);

  const wordEls = (ink, acc, dx, dy) =>
    `<path fill="${ink}" transform="translate(${r2(dx)} ${r2(dy)})" d="${t.d}"/>` +
    (per ? `<path fill="${acc}" transform="translate(${r2(dx + perX)} ${r2(dy)})" d="${per.d}"/>` : '');
  const markEls = (ink, acc, x, y, s) =>
    `<g transform="translate(${r2(x - bx * s)} ${r2(y - by * s)}) scale(${r2(s * 1000) / 1000})">${paint(parts, ink, acc)}</g>`;

  // horizontal
  const mh = dir.lock.h * asc;
  const s = mh / bh;
  const mw = bw * s;
  const markTop = dir.lock.align === 'center' ? -xh / 2 - mh / 2 : -mh + dir.lock.dy * asc;
  const tx = mw + dir.lock.gap * mh - t.x1;
  const top = Math.min(markTop, t.y1);
  const bot = Math.max(markTop + mh, t.y2);
  const H = { vb: `0 ${r2(top)} ${r2(tx + wordX2)} ${r2(bot - top)}`, w: tx + wordX2, h: bot - top };

  // stacked
  const ww = wordX2 - t.x1;
  const s2 = (dir.stack.markW * ww) / bw;
  const mh2 = bh * s2;
  const gap2 = dir.stack.gap * mh2;
  const S = { w: ww, h: mh2 + gap2 + asc + t.y2 };

  const out = {};
  const wordVB = `${r2(t.x1)} ${r2(t.y1)} ${r2(wordX2 - t.x1)} ${r2(t.y2 - t.y1)}`;
  const plainW = t.x2 - t.x1; // wordmark width without the full stop
  for (const [v, [ink, acc]] of Object.entries(VARIANTS(dir))) {
    out[`wordmark-${v}`] = svg(wordVB, wordEls(ink, acc, 0, 0));
    if (W.wordOnly) {
      out[`logo-horizontal-${v}`] = out[`wordmark-${v}`];
      out[`logo-stacked-${v}`] = svg(
        `0 0 ${r2(plainW)} ${r2(S.h)}`,
        markEls(ink, acc, (plainW - bw * s2) / 2, 0, s2) +
          `<path fill="${ink}" transform="translate(${r2(-t.x1)} ${r2(mh2 + gap2 + asc)})" d="${t.d}"/>`,
      );
      continue;
    }
    out[`logo-horizontal-${v}`] = svg(H.vb, markEls(ink, acc, 0, markTop, s) + wordEls(ink, acc, tx, 0));
    out[`logo-stacked-${v}`] = svg(
      `0 0 ${r2(S.w)} ${r2(S.h)}`,
      markEls(ink, acc, (ww - bw * s2) / 2, 0, s2) + wordEls(ink, acc, -t.x1, mh2 + gap2 + asc),
    );
  }
  return out;
}

function marks(dir) {
  const out = {};
  const parts = markParts(dir);
  const fav = markParts(dir, 'fav');
  for (const [v, [ink, acc]] of Object.entries(VARIANTS(dir))) {
    out[`logo-mark-${v}`] = svg('0 0 32 32', paint(parts, ink, acc));
    out[`favicon16-${v}`] = svg('0 0 16 16', paint(fav, ink, acc));
  }
  // favicon.svg: the 16px cut, following the browser's colour scheme
  const L = dir.pal.light, D = dir.pal.dark;
  const st = (c) => (fav.bold ? `fill:${c};stroke:${c}` : `fill:${c}`);
  const style = `<style>.i{${st(L.ink)}}.a{fill:${L.acc}}@media (prefers-color-scheme:dark){.i{${st(D.ink)}}.a{fill:${D.acc}}}</style>`;
  const strokeW = fav.bold ? ` stroke-width="${fav.bold}" stroke-linejoin="round"` : '';
  out['favicon'] = svg('0 0 16 16', `${style}<g class="i"${strokeW}>${fav.ink}</g><g class="a">${fav.acc}</g>`);
  // app icon: 512 rounded square, mark at ~58% of the width, optically centred
  const [bx, by, bw, bh] = dir.mark.box;
  for (const [v, P] of [['dark', D], ['light', L]]) {
    const s = (512 * 0.58) / Math.max(bw, bh * 1.25);
    const x = 256 - (bw * s) / 2 - bx * s;
    const y = 256 - (bh * s) / 2 - by * s;
    out[`app-icon-${v}`] = svg(
      '0 0 512 512',
      `<rect width="512" height="512" rx="114" fill="${P.bg}"/><g transform="translate(${r2(x)} ${r2(y)}) scale(${r2(s * 1000) / 1000})">${paint(parts, P.ink, P.acc)}</g>`,
    );
  }
  // full-bleed square for apple-touch-icon (iOS applies its own mask)
  {
    const s = (180 * 0.58) / Math.max(bw, bh * 1.25);
    out['apple-touch-icon'] = svg(
      '0 0 180 180',
      `<rect width="180" height="180" fill="${D.bg}"/><g transform="translate(${r2(90 - (bw * s) / 2 - bx * s)} ${r2(90 - (bh * s) / 2 - by * s)}) scale(${r2(s * 1000) / 1000})">${paint(parts, D.ink, D.acc)}</g>`,
    );
  }
  return out;
}

const meta = [];
for (const dir of DIRS) {
  const outDir = path.join(root, 'directions', dir.id);
  fs.mkdirSync(outDir, { recursive: true });
  const files = { ...marks(dir), ...lockups(dir) };
  for (const [name, s] of Object.entries(files)) fs.writeFileSync(path.join(outDir, name + '.svg'), opt(s) + '\n');
  meta.push({ id: dir.id, files: Object.keys(files).length });
}

// Ready-to-use set for the recommended direction, with the public file names.
{
  const dir = DIRS.find((d) => d.id === RECOMMENDED);
  const src = path.join(root, 'directions', dir.id);
  const dst = path.join(root, 'assets');
  fs.mkdirSync(dst, { recursive: true });
  const map = {
    'logo-mark.svg': 'logo-mark-light.svg',
    'logo-mark-dark.svg': 'logo-mark-dark.svg',
    'logo-mark-mono.svg': 'logo-mark-mono.svg',
    'logo-horizontal.svg': 'logo-horizontal-light.svg',
    'logo-horizontal-dark.svg': 'logo-horizontal-dark.svg',
    'logo-horizontal-mono.svg': 'logo-horizontal-mono.svg',
    'logo-stacked.svg': 'logo-stacked-light.svg',
    'logo-stacked-dark.svg': 'logo-stacked-dark.svg',
    'logo-stacked-mono.svg': 'logo-stacked-mono.svg',
    'wordmark.svg': 'wordmark-light.svg',
    'wordmark-dark.svg': 'wordmark-dark.svg',
    'wordmark-mono.svg': 'wordmark-mono.svg',
    'favicon.svg': 'favicon.svg',
    'app-icon.svg': 'app-icon-dark.svg',
    'app-icon-light.svg': 'app-icon-light.svg',
  };
  for (const [to, from] of Object.entries(map)) fs.copyFileSync(path.join(src, from), path.join(dst, to));
}
// Logo kits: one self-contained, drop-in folder per logo candidate (tools/logos.mjs).
// Public file names: no suffix = for light backgrounds, -dark, -mono (currentColor).
const KIT_NAMES = { 'logo-mark-light': 'logo-mark', 'logo-horizontal-light': 'logo-horizontal', 'logo-stacked-light': 'logo-stacked', 'wordmark-light': 'wordmark', 'app-icon-dark': 'app-icon' };
for (const dir of LOGOS) {
  const kit = path.join(root, 'logo-kits', dir.id);
  fs.mkdirSync(kit, { recursive: true });
  const files = { ...marks(dir), ...lockups(dir) };
  for (const [name, s] of Object.entries(files)) fs.writeFileSync(path.join(kit, (KIT_NAMES[name] ?? name) + '.svg'), opt(s) + '\n');
  // The mark as inline SVG themed by tokens.css, for app chrome (sidebar, top bar).
  const parts = markParts(dir);
  const inline = `<svg class="logo-mark" viewBox="${dir.mark.box.join(' ')}" role="img" aria-label="conductor"><g fill="var(--color-logo-ink, var(--color-text-primary))">${parts.ink}</g><g fill="var(--color-accent-default)">${parts.acc}</g></svg>`;
  fs.writeFileSync(path.join(kit, 'mark-inline.html'), `<!-- ${dir.name}: inline mark for app chrome. Size it by height; the viewBox is the mark's content box. Colours come from tokens.css. -->\n${inline}\n`);
  fs.writeFileSync(path.join(kit, 'README.md'), [
    `# conductor logo kit: ${dir.name}`, '', dir.line, '',
    'GENERATED by `design/brand/tools/build.mjs` and `render.mjs` from `tools/logos.mjs`. Edit the geometry there, not these files.', '',
    '| File | Use |', '|---|---|',
    '| `logo-horizontal.svg` · `-dark` · `-mono` | Default lockup: README, docs, sign-in. |',
    '| `logo-stacked.svg` · `-dark` · `-mono` | Square spaces. |',
    '| `logo-mark.svg` · `-dark` · `-mono` | Mark alone, on a 32-unit square. `-mono` uses `currentColor`. |',
    '| `wordmark.svg` · `-dark` · `-mono` | Name alone. |',
    '| `mark-inline.html` | Inline SVG for the dashboard sidebar and top bar, coloured by `tokens.css` (`--color-text-primary`, `--color-accent-default`). |',
    '| `favicon.svg` | The 16px cut; follows the browser colour scheme. |',
    '| `favicon-16/32/48.png` (+ `-dark`) | PNG fallbacks. |',
    '| `apple-touch-icon.png` · `icon-192.png` · `icon-512.png` · `app-icon.svg` · `app-icon-light.svg` | Home screen and PWA. |', '',
    'Files without a suffix are for light backgrounds; `-dark` files are for dark backgrounds.', '',
    '## Dropping it into the dashboard prototype', '',
    '1. Copy this folder to `design/dashboard-prototype/brand/`.',
    '2. `index.html`: replace the inline favicon with:',
    '   ```html', '   <link rel="icon" href="brand/favicon.svg" type="image/svg+xml">', '   <link rel="icon" href="brand/favicon-32.png" sizes="32x32">', '   <link rel="apple-touch-icon" href="brand/apple-touch-icon.png">', '   ```',
    '3. `js/ui.js` (the `.baton` svg in the strip) and `js/screens/access.js` (the gate mark): replace the svg with the contents of `mark-inline.html`.',
    '4. CSS: `.logo-mark { height: 20px; width: auto; flex: none; }` beside the live-text wordmark (Fraunces SemiBold, `--type-brand`), or use `logo-horizontal(-dark).svg` as an `<img>`.', '',
  ].join('\n') + '\n');
  meta.push({ id: 'logo-kits/' + dir.id, files: Object.keys(files).length });
}
console.log(meta.map((m) => `${m.id}: ${m.files} svgs`).join('\n'));
