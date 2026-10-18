// Renders the calm-by-default before/after pairs: v2/shots/calm/{screen}-{before|calm}-{desktop|phone}-{light|dark}.png
// (first viewport only: what the 3-second glance sees). Serve design/brand over HTTP; V2_BASE=url.
// Usage: node v2/render-calm.mjs [screen] [outDir]
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright');
const here = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.V2_BASE || process.env.BRAND_BASE || 'http://localhost:8768';
const [only, outArg] = process.argv.slice(2);
const out = outArg || path.join(here, 'shots', 'calm');
fs.mkdirSync(out, { recursive: true });
export const PAIRS = [['overview', ''], ['quiet', '&day=quiet'], ['history', ''], ['activity', ''], ['bot', ''], ['approval', '']];
const b = await chromium.launch();
for (const [vp, w, h] of [['desktop', 1440, 900], ['phone', 390, 844]]) {
  const p = await b.newPage({ viewport: { width: w, height: h }, deviceScaleFactor: vp === 'phone' ? 2 : 1 });
  const errs = []; p.on('pageerror', (e) => errs.push(e.message));
  for (const [name, extra] of PAIRS) for (const v of ['before', 'calm']) for (const t of ['light', 'dark']) {
    if (only && only !== name) continue;
    if (name === 'quiet' && v === 'before') continue;
    const s = name === 'quiet' ? 'overview' : name;
    await p.goto(`${BASE}/v2/mock.html?d=daylight&s=${s}&t=${t}${extra}${v === 'before' ? '&v=before' : '&v=calm'}`); await p.waitForTimeout(400);
    await p.evaluate(() => document.fonts.ready);
    await p.screenshot({ path: path.join(out, `${name}-${v}-${vp}-${t}.png`) });
  }
  console.log(vp, errs.length ? 'ERR ' + [...new Set(errs)].join(' | ') : 'ok');
  await p.close();
}
await b.close();
