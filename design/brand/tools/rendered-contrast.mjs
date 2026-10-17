// Rendered contrast: measures what a reader actually sees, not what the tokens promise.
// The token-pair check (tools/tokens.mjs) passed while a page was unreadable on an iPhone,
// so this one loads every page in every engine and OS colour scheme and, for each visible
// text element, composites its computed colour (including ancestor opacity) over its computed
// background stack, then checks WCAG contrast (4.5, or 3 for large text).
//
//   node tools/rendered-contrast.mjs [engine,engine] [page-filter]
//
// Elements inside [data-contrast="exempt"] are skipped (deliberate demos: disabled states,
// misuse examples, verbatim "before" screenshots); every exemption is counted in the report.
// Writes $REPORT_DIR/rendered-contrast.md (default ~/.cache/conductor-design/reports) and exits 1 if anything fails.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { available, context, closeAll } from './browsers.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..');
const BASE = process.env.BRAND_BASE || 'http://localhost:8768';
const [onlyEngines, filter] = process.argv.slice(2);

const PAGES = [
  'index.html', 'directions-v2.html', 'logo-review.html', 'explore/status-column.html',
  'system.html', 'system.html?theme=light', 'system.html?theme=dark', 'system.html?theme=dark&density=compact',
];
for (const d of ['daylight', 'broadsheet', 'score']) for (const s of ['overview', 'run', 'approval', 'history', 'builder']) for (const t of ['light', 'dark']) PAGES.push(`v2/mock.html?d=${d}&s=${s}&t=${t}`);

function measure() {
  const parse = (s) => { const m = s.match(/rgba?\(([^)]+)\)/); if (!m) return null; const p = m[1].split(/[\s,/]+/).filter(Boolean).map(Number); return [p[0], p[1], p[2], p[3] ?? 1]; };
  const lum = ([r, g, b]) => { const f = (c) => { c /= 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
  const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
  const over = (top, bot) => { const a = top[3]; return [0, 1, 2].map((i) => top[i] * a + bot[i] * (1 - a)).concat(1); };
  const canvasBg = () => { const b = parse(getComputedStyle(document.body).backgroundColor); const h = parse(getComputedStyle(document.documentElement).backgroundColor); return b && b[3] > 0 ? b : h && h[3] > 0 ? h : [255, 255, 255, 1]; };
  const out = { scheme: getComputedStyle(document.documentElement).colorScheme, checked: 0, exempt: 0, unknown: 0, fails: [] };
  const seen = new Set();
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  while (walker.nextNode()) {
    const node = walker.currentNode; const el = node.parentElement;
    if (!el || seen.has(el) || !node.textContent.trim()) continue;
    seen.add(el);
    if (el.closest('svg, script, style, noscript, [hidden], details:not([open]) > :not(summary)')) continue;
    const r = el.getBoundingClientRect(); if (r.width < 1 || r.height < 1) continue;
    const cs = getComputedStyle(el);
    if (cs.visibility !== 'visible' || cs.display === 'none') continue;
    if (el.closest('[data-contrast="exempt"], :disabled, .is-disabled')) { out.exempt++; continue; }
    // opacity chain
    let op = 1; for (let n = el; n; n = n.parentElement) op *= Number(getComputedStyle(n).opacity);
    if (op < 0.05) continue;
    // background stack up to the first opaque layer
    const layers = []; let image = false;
    for (let n = el; n; n = n.parentElement) {
      const s = getComputedStyle(n), c = parse(s.backgroundColor);
      if (s.backgroundImage !== 'none' && !(c && c[3] >= 1) && n !== document.body && n !== document.documentElement) image = true;
      if (c && c[3] > 0) { layers.push(c); if (c[3] >= 1) break; }
    }
    if (image) { out.unknown++; continue; }
    let bg = canvasBg(); for (let i = layers.length - 1; i >= 0; i--) bg = over(layers[i], bg);
    let fg = parse(cs.color); fg = [fg[0], fg[1], fg[2], fg[3] * op];
    if (fg[3] < 0.05) continue; // fully transparent text is hidden by design, not low contrast
    const seenFg = over(fg, bg);
    const size = parseFloat(cs.fontSize), bold = Number(cs.fontWeight) >= 700;
    const need = size >= 24 || (size >= 18.66 && bold) ? 3 : 4.5;
    const cr = ratio(seenFg, bg);
    out.checked++;
    if (cr < need) out.fails.push({ text: node.textContent.trim().slice(0, 48), ratio: +cr.toFixed(2), need, fg: seenFg.slice(0, 3).map(Math.round).join(','), bg: bg.slice(0, 3).map(Math.round).join(','), where: (el.className && String(el.className).slice(0, 40)) || el.tagName.toLowerCase() });
  }
  return out;
}

const rows = []; let failing = 0;
for (const engine of available(onlyEngines)) {
  for (const os of engine.osModes ?? ['light', 'dark']) {
    const ctx = await context(engine, os);
    const page = await ctx.newPage();
    for (const p of PAGES) {
      if (filter && !p.includes(filter)) continue;
      const res = await page.goto(`${BASE}/${p}`).catch(() => null);
      if (!res || res.status() >= 400) continue;
      await page.waitForTimeout(700);
      const m = await page.evaluate(measure);
      const schemeFail = m.scheme === 'normal';
      if (m.fails.length || schemeFail) failing++;
      rows.push({ engine: engine.id, os, page: p, ...m, schemeFail });
    }
    await ctx.close();
    console.log(`${engine.id} · OS ${os}: ${rows.filter((r) => r.engine === engine.id && r.os === os).reduce((a, r) => a + r.checked, 0)} text elements`);
  }
}
await closeAll();

const total = rows.reduce((a, r) => a + r.checked, 0), bad = rows.reduce((a, r) => a + r.fails.length, 0);
const md = [
  '# Rendered contrast report', '',
  `GENERATED by \`tools/rendered-contrast.mjs\`: ${rows.length} page renders, ${total} text elements measured from computed styles (colour × opacity over the composited background), ${bad} below WCAG AA, ${rows.filter((r) => r.schemeFail).length} pages without a declared colour scheme.`, '',
  'Engines: ' + [...new Set(rows.map((r) => r.engine))].join(', ') + '. Each page is rendered with the OS in light and in dark mode.', '',
  '| Engine | OS | Page | Text checked | Exempt | Image background | Below AA | Scheme |', '|---|---|---|---|---|---|---|---|',
  ...rows.map((r) => `| ${r.engine} | ${r.os} | \`${r.page}\` | ${r.checked} | ${r.exempt} | ${r.unknown} | ${r.fails.length ? '**' + r.fails.length + '**' : 0} | ${r.schemeFail ? '**none**' : r.scheme} |`),
  '', ...rows.filter((r) => r.fails.length).flatMap((r) => [`## ${r.engine} · OS ${r.os} · \`${r.page}\``, '', ...r.fails.slice(0, 12).map((f) => `- ${f.ratio} < ${f.need}: “${f.text}” (${f.where}, rgb ${f.fg} on ${f.bg})`), '']),
].join('\n');
// Test output stays off the repo (and off network mounts): REPORT_DIR, default ~/.cache/conductor-design/reports.
const reportDir = process.env.REPORT_DIR || path.join(process.env.HOME, '.cache', 'conductor-design', 'reports');
fs.mkdirSync(reportDir, { recursive: true });
fs.writeFileSync(path.join(reportDir, 'rendered-contrast.md'), md + '\n');
if (process.env.DUMP) fs.writeFileSync(process.env.DUMP, JSON.stringify(rows.filter((r) => r.fails.length), null, 1));
console.log(`${rows.length} renders · ${total} text elements · ${bad} below AA · ${failing} pages failing`);
for (const r of rows.filter((x) => x.fails.length || x.schemeFail).slice(0, 25)) console.log(`  ${r.engine} ${r.os} ${r.page}: ${r.fails.length} below AA${r.schemeFail ? ', no color-scheme' : ''}${r.fails[0] ? ` (e.g. ${r.fails[0].ratio}: “${r.fails[0].text}”)` : ''}`);
process.exit(failing ? 1 : 0);
