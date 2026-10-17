// Loudness check for "calm by default". Loads each before/after mock pair and measures only the
// first viewport, which is what a three-second glance sees:
//   coloured   things drawn in a saturated colour (OKLCH chroma ≥ 0.05: text, SVG marks and bars,
//              backgrounds, edges), counted once per row, item, link or button they belong to;
//   heavy      rows (li, tr, summary lines) with more than 110 characters or 3+ lines of text;
//   glance     the page's [data-glance] answer must be the largest text in the viewport (the wordmark
//              aside), the first large text in reading order, and at least 7:1 against what's behind it.
// Budget, from the page's declared exceptions (main[data-exceptions], the things that need a person):
//   ≤ 3 exceptions: coloured ≤ exceptions + 2, heavy ≤ 2;  more: coloured ≤ 5, heavy ≤ 3.
// Calm versions must pass; the busy "before" versions are measured too, to show what changed.
//
//   node tools/calm-check.mjs [engine,engine]   (serve design/brand over HTTP; BRAND_BASE=url)
// Writes tokens/CALM-CHECK.md and tokens/calm-check.json. Exits 1 if any calm page fails.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { available, context, closeAll } from './browsers.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.BRAND_BASE || 'http://localhost:8768';
const SCREENS = [['overview', 'overview', ''], ['quiet overview', 'overview', '&day=quiet'], ['run history', 'history', ''], ['lanes (activity)', 'activity', ''], ['bot page', 'bot', ''], ['approval header', 'approval', '']];
const ENGINES = process.argv[2] || 'chromium-desktop,webkit-iphone';
export const BUDGET = { chroma: 0.05, heavyChars: 110, heavyLines: 3, glanceContrast: 7, large: 20 };

function measure(B) {
  const vw = innerWidth, vh = innerHeight;
  const lin = (c) => { c /= 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
  const parse = (s) => { const m = s && s.match(/rgba?\(([^)]+)\)/); if (!m) return null; const p = m[1].split(/[ ,/]+/).filter(Boolean).map(Number); return { r: p[0], g: p[1], b: p[2], a: p[3] ?? 1 }; };
  const chroma = ({ r, g, b }) => {
    const [R, G, Bl] = [lin(r), lin(g), lin(b)];
    const l = Math.cbrt(0.4122214708 * R + 0.5363325363 * G + 0.0514459929 * Bl), m = Math.cbrt(0.2119034982 * R + 0.6806995451 * G + 0.1073969566 * Bl), s = Math.cbrt(0.0883024619 * R + 0.2817188376 * G + 0.6299787005 * Bl);
    const A = 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s, Bb = 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s;
    return Math.hypot(A, Bb);
  };
  const lum = ({ r, g, b }) => 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
  const contrast = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
  const inView = (r) => r.width > 0 && r.height > 0 && r.bottom > 0 && r.right > 0 && r.top < vh && r.left < vw;
  const shown = (el) => { if (el.checkVisibility && !el.checkVisibility({ opacityProperty: true, visibilityProperty: true })) return false; for (let e = el; e && e.nodeType === 1; e = e.parentElement) { const cs = getComputedStyle(e); if (cs.visibility === 'hidden' || cs.display === 'none' || +cs.opacity < 0.3) return false; } return true; };
  const ownText = (el) => [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
  const groupOf = (el) => el.closest('[data-item]') ?? el.closest('li, tr, a, button, summary, .badge, .count-pill') ?? el;
  const label = (el) => { const t = (el.innerText || el.textContent || el.getAttribute('aria-label') || '').replace(/\s+/g, ' ').trim(); return `${el.tagName.toLowerCase()}${el.className && typeof el.className === 'string' ? '.' + el.className.split(' ')[0] : ''} “${t.slice(0, 40)}”`; };

  const coloured = new Map();
  for (const el of document.querySelectorAll('body *')) {
    if (el.closest('.logo, .brand, title, [data-calm-ignore]')) continue;
    const r = el.getBoundingClientRect();
    if (!inView(r)) continue;
    const cs = getComputedStyle(el), hits = [];
    if (ownText(el)) hits.push(cs.color);
    if (el instanceof SVGElement && ['path', 'circle', 'rect', 'line', 'polyline', 'polygon', 'ellipse', 'text', 'tspan'].includes(el.tagName)) {
      if (cs.fill !== 'none' && +cs.fillOpacity >= 0.3) hits.push(cs.fill);
      if (cs.stroke !== 'none' && +cs.strokeOpacity >= 0.3 && parseFloat(cs.strokeWidth) > 0) hits.push(cs.stroke);
    }
    if (r.width >= 6 && r.height >= 6) hits.push(cs.backgroundColor);
    if (parseFloat(cs.borderLeftWidth) >= 2) hits.push(cs.borderLeftColor);
    if (cs.boxShadow.includes('inset')) hits.push(cs.boxShadow);
    if (!hits.some((h) => { const c = parse(h); return c && c.a >= 0.3 && chroma(c) >= B.chroma; })) continue;
    if (!shown(el)) continue;
    const g = groupOf(el);
    if (!coloured.has(g)) coloured.set(g, label(g));
  }

  const heavy = [];
  for (const row of document.querySelectorAll('main li, main tr, main summary')) {
    const r = row.getBoundingClientRect();
    if (!inView(r) || !shown(row) || row.closest('li, tr, summary') !== row && row.parentElement.closest('li, tr')) continue;
    const text = (row.innerText || '').replace(/\s+/g, ' ').trim();
    // Lines of text: rects of the row's visible text nodes, merged where they overlap vertically.
    const rects = [];
    const walk = document.createTreeWalker(row, NodeFilter.SHOW_TEXT);
    for (let n = walk.nextNode(); n; n = walk.nextNode()) {
      if (!n.textContent.trim() || !shown(n.parentElement) || n.parentElement.closest('button, .cs-more')) continue; // actions aren't reading
      const range = document.createRange(); range.selectNodeContents(n);
      rects.push(...[...range.getClientRects()].filter((q) => q.width > 1 && q.height > 4));
    }
    const lines = [];
    for (const q of rects.sort((a, b) => a.top - b.top)) {
      const l = lines.find((x) => Math.min(x.bottom, q.bottom) - Math.max(x.top, q.top) > 0.5 * Math.min(x.bottom - x.top, q.height));
      if (l) { l.top = Math.min(l.top, q.top); l.bottom = Math.max(l.bottom, q.bottom); } else lines.push({ top: q.top, bottom: q.bottom });
    }
    if (text.length > B.heavyChars || lines.length >= B.heavyLines) heavy.push(`${text.slice(0, 50)}… (${text.length} chars, ${lines.length} lines)`);
  }

  // The glance answer: largest, first large text, high contrast.
  const glance = document.querySelector('[data-glance]');
  let glanceOk = false, glanceWhy = 'no [data-glance] element';
  if (glance) {
    const textEls = [...document.querySelectorAll('body *')].filter((el) => ownText(el) && !el.closest('.logo, .brand, title') && inView(el.getBoundingClientRect()) && shown(el));
    const size = (el) => parseFloat(getComputedStyle(el).fontSize);
    const gEls = textEls.filter((el) => glance.contains(el)), gSize = Math.max(...gEls.map(size));
    const bigger = textEls.filter((el) => !glance.contains(el) && size(el) > gSize);
    const firstLarge = textEls.find((el) => size(el) >= B.large);
    const gText = gEls.find((el) => size(el) === gSize);
    let bg = null;
    for (let e = gText; e && !bg; e = e.parentElement) { const c = parse(getComputedStyle(e).backgroundColor); if (c && c.a > 0.5) bg = c; }
    const ratio = contrast(parse(getComputedStyle(gText).color), bg ?? { r: 255, g: 255, b: 255 });
    const problems = [];
    if (bigger.length) problems.push(`larger text elsewhere: ${label(bigger[0])}`);
    if (firstLarge && !glance.contains(firstLarge)) problems.push(`first large text is ${label(firstLarge)}`);
    if (ratio < B.glanceContrast) problems.push(`contrast ${ratio.toFixed(1)}`);
    glanceOk = !problems.length; glanceWhy = problems.join('; ') || `${gSize}px, ${ratio.toFixed(1)}:1`;
  }
  const exceptions = +(document.querySelector('main')?.dataset.exceptions ?? 0);
  return { exceptions, coloured: [...coloured.values()], heavy, glance: glance?.dataset.glanceAnswer ?? null, glanceOk, glanceWhy };
}

const budgetOf = (ex) => (ex <= 3 ? { coloured: ex + 2, heavy: 2 } : { coloured: 5, heavy: 3 });
const results = [];
for (const engine of available(ENGINES)) {
  const ctx = await context(engine, 'light'); const page = await ctx.newPage();
  for (const [name, s, extra] of SCREENS) for (const t of ['light', 'dark']) for (const v of ['before', 'calm']) {
    if (extra && v === 'before') continue;
    await page.goto(`${BASE}/v2/mock.html?d=daylight&s=${s}&t=${t}${extra}${v === 'before' ? '&v=before' : ''}`);
    await page.waitForTimeout(500); await page.evaluate(() => document.fonts.ready);
    const m = await page.evaluate(measure, BUDGET);
    const b = budgetOf(m.exceptions);
    const fails = [];
    if (m.coloured.length > b.coloured) fails.push(`${m.coloured.length} coloured (budget ${b.coloured})`);
    if (m.heavy.length > b.heavy) fails.push(`${m.heavy.length} text-heavy rows (budget ${b.heavy})`);
    if (!m.glanceOk) fails.push(`glance: ${m.glanceWhy}`);
    results.push({ engine: engine.id, screen: name, theme: t, version: v, ...m, budget: b, pass: !fails.length, fails });
    console.log(`${fails.length ? (v === 'calm' ? 'FAIL' : 'busy') : 'pass'} ${engine.id} · ${name} · ${v} · ${t}: ${m.coloured.length} coloured, ${m.heavy.length} heavy${fails.length ? ' — ' + fails.join('; ') : ''}`);
    if (process.env.CALM_VERBOSE) console.log('   coloured:', m.coloured.join(' | '), '\n   heavy:', m.heavy.join(' | '));
  }
  await ctx.close();
}
await closeAll();

const calmFails = results.filter((r) => r.version === 'calm' && !r.pass);
const out = ['# Calm check: loudness of the first viewport', '',
  'GENERATED by `tools/calm-check.mjs`. Every before/after pair of mocks (`v2/mock.html?d=daylight`, `&v=before` for the busy version) is loaded in each engine and theme, and only the **first viewport** is measured.', '',
  `- **Coloured**: things drawn in a saturated colour (OKLCH chroma ≥ ${BUDGET.chroma}), counted once per row, item, link or button.`,
  `- **Text-heavy rows**: rows (\`li\`, \`tr\`, summary lines) over ${BUDGET.heavyChars} characters or ${BUDGET.heavyLines}+ lines.`,
  `- **Glance**: the page’s \`[data-glance]\` answer must be the largest text, the first large text in reading order, and ≥ ${BUDGET.glanceContrast}:1.`,
  '- **Budget**: with ≤ 3 exceptions, coloured ≤ exceptions + 2 and text-heavy ≤ 2; with more, coloured ≤ 5 and text-heavy ≤ 3.', '',
  `Result: ${results.filter((r) => r.version === 'calm').length - calmFails.length} of ${results.filter((r) => r.version === 'calm').length} calm renders pass; ${results.filter((r) => r.version === 'before' && !r.pass).length} of ${results.filter((r) => r.version === 'before').length} busy renders fail the same budget.`, '',
  '| Engine | Screen | Theme | Version | Needs a person | Coloured | Text-heavy | Glance answer | Result |', '|---|---|---|---|---|---|---|---|---|'];
for (const r of results) out.push(`| ${r.engine} | ${r.screen} | ${r.theme} | ${r.version} | ${r.exceptions} | ${r.coloured.length} / ${r.budget.coloured} | ${r.heavy.length} / ${r.budget.heavy} | ${r.glance ? `${r.glanceOk ? '✓' : '✗'} ${r.glance}` : '✗ none'} | ${r.pass ? 'pass' : r.version === 'calm' ? '**FAIL**' : 'too loud'} |`);
fs.writeFileSync(path.join(here, '..', 'tokens', 'CALM-CHECK.md'), out.join('\n') + '\n');
fs.writeFileSync(path.join(here, '..', 'tokens', 'calm-check.json'), JSON.stringify(results.map(({ engine, screen, theme, version, exceptions, coloured, heavy, glance, glanceOk, pass }) => ({ engine, screen, theme, version, exceptions, coloured: coloured.length, heavy: heavy.length, glance, glanceOk, pass })), null, 1) + '\n');
console.log(`${calmFails.length ? `${calmFails.length} calm renders FAIL` : 'all calm renders pass'} · ${results.filter((r) => r.version === 'before' && !r.pass).length} busy renders over budget`);
process.exit(calmFails.length ? 1 : 0);
