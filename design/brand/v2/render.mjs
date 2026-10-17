// Renders every v2 mock to PNG: v2/shots/{direction}-{screen}-{desktop|phone}-{light|dark}.png
// (viewport-sized, like the current-prototype shots beside them). Serve design/brand over
// HTTP first; set V2_BASE to its URL. Usage: node v2/render.mjs [direction] [screen]
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_PATH || 'playwright');
const here = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.V2_BASE || 'http://localhost:8768';
const [onlyD, onlyS] = process.argv.slice(2);
const b = await chromium.launch();
for (const d of ['daylight', 'broadsheet', 'score']) {
  if (onlyD && onlyD !== d) continue;
  for (const [vp, w, h] of [['desktop', 1440, 900], ['phone', 390, 844]]) {
    const p = await b.newPage({ viewport: { width: w, height: h } });
    const errs = []; p.on('pageerror', (e) => errs.push(e.message));
    for (const t of ['light', 'dark']) for (const s of ['overview', 'run', 'approval', 'history', 'builder']) {
      if (onlyS && onlyS !== s) continue;
      await p.goto(`${BASE}/v2/mock.html?d=${d}&s=${s}&t=${t}`); await p.waitForTimeout(500);
      await p.evaluate(() => document.fonts.ready);
      await p.screenshot({ path: path.join(here, 'shots', `${d}-${s}-${vp}-${t}.png`) });
    }
    console.log(d, vp, errs.length ? 'ERR ' + errs.join(' | ') : 'ok');
    await p.close();
  }
}
await b.close();
