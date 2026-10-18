// Icon legibility at 16px for the scannable proposal. Renders every semantic icon (kinds, actions,
// step kinds), the environment glyphs and the status marks at 16 CSS px on a 1x
// screen, in black on white, and measures the real pixels:
//   ink        share of the 16×16 cell that is inked (too little vanishes, too much is a blob);
//   alike      for every pair, how much their inked 16px pixels overlap (intersection over union of the
//              masks, best of every shift up to 1px, so a nudge doesn't make two icons “different”).
//              Pairs above 0.6 are too alike to tell apart at a glance, and fail if they share a family;
// and writes a contact sheet of the actual 16px pixels, enlarged 6× with no smoothing, so a person can
// judge them too.
//
//   node tools/icon-check.mjs      (serve design/brand over HTTP; BRAND_BASE=url)
// Writes scannable/ICON-CHECK.md, scannable/icon-check.json and scannable/icon-16px-sheet.png.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright');
const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.join(here, '..', 'scannable');
const BASE = process.env.BRAND_BASE || 'http://localhost:8768';
const LIMITS = { inkMin: 0.07, inkMax: 0.5, alike: 0.6 };

const b = await chromium.launch();
const p = await b.newPage({ deviceScaleFactor: 1 });
await p.goto(`${BASE}/v2/mock.html?d=daylight&v=scan`);
const res = await p.evaluate(async ({ base, LIMITS }) => {
  const S = await import(`${base}/status/rows.js`);
  const M = await import(`${base}/status/marks.js`);
  const I = await import(`${base}/icons/icons.js`);
  const luc = (n) => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="#000" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">${I.LUCIDE[n]}</svg>`;
  const fix = (svg) => svg.replace('<svg ', '<svg xmlns="http://www.w3.org/2000/svg" ').replace(/currentColor/g, '#000').replace(/ class="[^"]*"/g, '').replace(/ style="[^"]*"/g, '');
  const set = [];
  for (const [k, v] of Object.entries(S.KINDS)) set.push(['kind', v.word, v.icon, luc(v.icon)]);
  for (const [k, v] of Object.entries(S.ACTIONS)) set.push(['action', v.word, v.icon, luc(v.icon)]);
  for (const [k, v] of Object.entries(S.STEPS)) set.push(['step', v.word, v.icon, luc(v.icon)]);
  for (const e of Object.keys(S.ENV)) set.push(['env', S.ENV[e].word, `env ${e}`, fix(S.env(e).match(/<svg[\s\S]*?<\/svg>/)[0].replace('width="10" height="10"', 'width="16" height="16"'))]);
  for (const st of Object.keys(M.STATES)) set.push(['status', M.STATES[st].word, `mark ${st}`, fix(M.mark(st, { size: 16 }))]);
  const draw = (svg) => new Promise((ok) => { const im = new Image(); im.onload = () => { const c = document.createElement('canvas'); c.width = c.height = 16; const x = c.getContext('2d'); x.fillStyle = '#fff'; x.fillRect(0, 0, 16, 16); x.drawImage(im, 0, 0, 16, 16); ok(x.getImageData(0, 0, 16, 16).data); }; im.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg); });
  const imgs = [];
  for (const it of set) { const d = await draw(it[3]); const g = new Float32Array(256); for (let i = 0; i < 256; i++) g[i] = 1 - (d[i * 4] * 0.2126 + d[i * 4 + 1] * 0.7152 + d[i * 4 + 2] * 0.0722) / 255; imgs.push(g); }
  const blur = (g) => { const o = new Float32Array(256); for (let y = 0; y < 16; y++) for (let x = 0; x < 16; x++) { let s = 0, n = 0; for (let dy = -1; dy <= 1; dy++) for (let dx = -1; dx <= 1; dx++) { const X = x + dx, Y = y + dy; if (X >= 0 && X < 16 && Y >= 0 && Y < 16) { s += g[Y * 16 + X] * (dx || dy ? 0.5 : 1); n += dx || dy ? 0.5 : 1; } } o[y * 16 + x] = s / n; } return o; };
  const masks = imgs.map((g) => g.map((v) => (v > 0.35 ? 1 : 0)));
  const iou = (a, b2) => { let best = 0; for (let dy = -1; dy <= 1; dy++) for (let dx = -1; dx <= 1; dx++) { let I = 0, U = 0; for (let y = 0; y < 16; y++) for (let x = 0; x < 16; x++) { const A = a[y * 16 + x], X = x + dx, Y = y + dy, B = X >= 0 && X < 16 && Y >= 0 && Y < 16 ? b2[Y * 16 + X] : 0; I += A & B; U += A | B; } best = Math.max(best, U ? I / U : 0); } return best; };
  const rows = set.map(([fam, word, src], i) => ({ fam, word, src, ink: imgs[i].reduce((a, v) => a + (v > 0.35 ? 1 : 0), 0) / 256 }));
  const pairs = [];
  for (let i = 0; i < set.length; i++) for (let j = i + 1; j < set.length; j++) { const s2 = iou(masks[i], masks[j]); if (s2 > LIMITS.alike - 0.2) pairs.push({ a: set[i][1], b: set[j][1], fa: set[i][0], fb: set[j][0], sim: +s2.toFixed(3) }); }
  // contact sheet: the real 16px pixels, ×6, nearest neighbour
  const cols = 9, cell = 16 * 6 + 10, sheet = document.createElement('canvas'); sheet.width = cols * cell; sheet.height = Math.ceil(set.length / cols) * (cell + 14);
  const sx = sheet.getContext('2d'); sx.fillStyle = '#fff'; sx.fillRect(0, 0, sheet.width, sheet.height); sx.imageSmoothingEnabled = false;
  for (let i = 0; i < set.length; i++) { const c = document.createElement('canvas'); c.width = c.height = 16; const cx = c.getContext('2d'); const id = cx.createImageData(16, 16); for (let k = 0; k < 256; k++) { const v = 255 - imgs[i][k] * 255; id.data.set([v, v, v, 255], k * 4); } cx.putImageData(id, 0, 0); const X = (i % cols) * cell + 5, Y = Math.floor(i / cols) * (cell + 14) + 4; sx.drawImage(c, X, Y, 96, 96); sx.fillStyle = '#333'; sx.font = '10px sans-serif'; sx.fillText(set[i][1].slice(0, 17), X, Y + 108); }
  return { rows, pairs, sheet: sheet.toDataURL('image/png') };
}, { base: BASE, LIMITS });
await b.close();

fs.writeFileSync(path.join(out, 'icon-16px-sheet.png'), Buffer.from(res.sheet.split(',')[1], 'base64'));
const inkBad = res.rows.filter((r) => r.fam !== 'env' && (r.ink < LIMITS.inkMin || r.ink > LIMITS.inkMax));
const SAME = (p2) => p2.fa === p2.fb || ['kind', 'action', 'step'].includes(p2.fa) && ['kind', 'action', 'step'].includes(p2.fb);
const alikeBad = res.pairs.filter((p2) => p2.sim > LIMITS.alike && SAME(p2));
const md = ['# Icon check: legibility at 16px', '', 'GENERATED by `tools/icon-check.mjs`. Every icon is rendered at 16 CSS px on a 1× screen, black on white, and measured on its real pixels. See the enlarged pixels in [`icon-16px-sheet.png`](icon-16px-sheet.png).', '',
  `- **Ink**: the share of the 16×16 cell that is inked; between ${LIMITS.inkMin * 100}% and ${LIMITS.inkMax * 100}% for icons (environment glyphs are solid by design).`,
  `- **Alike**: how much the inked pixels of two icons overlap at 16px (intersection over union, best of every 1px shift). Above ${LIMITS.alike} within the semantic icons (kinds, actions, step kinds), or within one family, fails.`, '',
  `Result: ${res.rows.length} glyphs; ${inkBad.length} outside the ink range; ${alikeBad.length} pairs too alike.`, '',
  '| Family | Glyph | Source | Ink at 16px |', '|---|---|---|---|',
  ...res.rows.map((r) => `| ${r.fam} | ${r.word} | ${r.src.startsWith('env') || r.src.startsWith('mark') ? `ours (${r.src})` : `Lucide \`${r.src}\``} | ${(r.ink * 100).toFixed(0)}%${inkBad.includes(r) ? ' **out of range**' : ''} |`),
  '', '## Closest pairs', '', '| A | B | Overlap | Verdict |', '|---|---|---|---|',
  ...res.pairs.sort((x, y) => y.sim - x.sim).slice(0, 12).map((p2) => `| ${p2.a} (${p2.fa}) | ${p2.b} (${p2.fb}) | ${p2.sim} | ${p2.sim > LIMITS.alike ? (SAME(p2) ? '**too alike**' : 'alike, but never in the same slot') : 'distinct'} |`), ''];
fs.writeFileSync(path.join(out, 'ICON-CHECK.md'), md.join('\n'));
fs.writeFileSync(path.join(out, 'icon-check.json'), JSON.stringify({ rows: res.rows, pairs: res.pairs, inkBad: inkBad.length, alikeBad: alikeBad.length }, null, 1) + '\n');
console.log(`${res.rows.length} glyphs · ${inkBad.length} outside ink range · ${alikeBad.length} pairs too alike`);
for (const r of inkBad) console.log('  ink', r.word, (r.ink * 100).toFixed(0) + '%');
for (const p2 of alikeBad) console.log('  alike', p2.a, '~', p2.b, p2.sim);
process.exit(inkBad.length || alikeBad.length ? 1 : 0);
