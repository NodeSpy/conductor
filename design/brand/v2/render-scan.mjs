// Renders the scannable proposal's comparisons: scannable/shots/{screen}-{before|calm|scan}-{desktop|phone}-{light|dark}.png
// before = the dashboard prototype itself (serve a copy of design/dashboard-prototype; PROTO_BASE=url),
// calm = the calm-by-default mocks, scan = the proposal. Brand pages: V2_BASE=url (design/brand served).
// Usage: node v2/render-scan.mjs [screen] [outDir]
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright');
const here = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.V2_BASE || process.env.BRAND_BASE || 'http://localhost:8768';
const PROTO = process.env.PROTO_BASE || 'http://localhost:8791';
const [only, outArg] = process.argv.slice(2);
const out = outArg || path.join(here, '..', 'scannable', 'shots');
fs.mkdirSync(out, { recursive: true });
// [screen, prototype route (+ optional click), calm mock screen or null]
export const SCREENS = [
  ['overview', '#/', 'overview'],
  ['history', '#/runs', 'history'],
  ['approval', '#/bots/platform-sre/proposals/p12', 'approval'],
  ['perms', '#/bots/platform-sre/access', 'bot'],
  ['picker', '#/workflows/ci-fixer', null, '[data-act="wf-add"]'],
];
const b = await chromium.launch();
for (const [vp, w, h] of [['desktop', 1440, 900], ['phone', 390, 844]]) {
  const p = await b.newPage({ viewport: { width: w, height: h }, deviceScaleFactor: vp === 'phone' ? 2 : 1 });
  const errs = []; p.on('pageerror', (e) => errs.push(e.message));
  for (const [s, route, calmS, click] of SCREENS) for (const t of ['light', 'dark']) {
    if (only && only !== s) continue;
    const shot = async (v) => { await p.waitForTimeout(500); await p.evaluate(() => document.fonts.ready); await p.screenshot({ path: path.join(out, `${s}-${v}-${vp}-${t}.png`) }); };
    await p.goto(`${PROTO}/index.html?theme=${t}${route}`); await p.waitForTimeout(700);
    if (click) { const bs = await p.$$(click); if (bs[1] ?? bs[0]) await (bs[1] ?? bs[0]).click(); }
    await shot('before');
    if (calmS) { await p.goto(`${BASE}/v2/mock.html?d=daylight&s=${calmS}&t=${t}`); await shot('calm'); }
    await p.goto(`${BASE}/v2/mock.html?d=daylight&v=scan&s=${s}&t=${t}`); await shot('scan');
  }
  console.log(vp, errs.length ? 'ERR ' + [...new Set(errs)].join(' | ') : 'ok');
  await p.close();
}
await b.close();
