// Renders the conformance pairs: each approved Daylight mock beside the prototype on the same screen, desktop (1440×900)
// and phone (390×844), light and dark, as JPEGs into OUT (default: next to this file). Serve design/brand from the brand
// branch at BRAND (default http://127.0.0.1:8798) and the prototype at PROTO (default http://127.0.0.1:8799).
// Usage: node render.js [out-dir]
const { chromium } = require('@playwright/test'); const fs = require('fs'); const path = require('path');
const OUT = process.argv[2] || __dirname, BRAND = process.env.BRAND || 'http://127.0.0.1:8798', PROTO = process.env.PROTO || 'http://127.0.0.1:8799';
const PAIRS = [['overview', 'overview', '#/overview'], ['activity', 'history', '#/activity'], ['run', 'run', '#/runs/r-7ef0'], ['approval', 'approval', '#/bots/platform-sre/proposals/p12'], ['builder', 'builder', '#/workflows/ci-fixer']];
const SOLO = [['needs', '#/attention'], ['personal-details', '#/browser/details/home'], ['payment-cards', '#/browser/cards'], ['purchase-approval', '#/bots/homelab/proposals/p14'], ['bots', '#/bots'], ['workflows', '#/workflows'], ['connections', '#/connectors/connectors'], ['secrets', '#/secrets'], ['packs', '#/extensions/packs'], ['pack-overrides', '#/extensions/pack/kit'], ['safety', '#/safety'], ['config', '#/config'], ['updates', '#/updates'], ['access', '#/security'], ['logins', '#/browser/logins']];
(async () => { fs.mkdirSync(OUT, { recursive: true }); const b = await chromium.launch();
  const proto = async (p, route, t) => { await p.goto(`${PROTO}/index.html#/overview`); await p.evaluate((th) => { localStorage.clear(); localStorage.setItem('cd-demo', 'full'); localStorage.setItem('cd-theme', th); }, t); await p.goto(`${PROTO}/index.html?x=${Date.now()}${route}`); await p.waitForTimeout(900); await p.evaluate(() => document.fonts.ready); };
  for (const [dev, w, h] of [['desktop', 1440, 900], ['phone', 390, 844]]) {
    const p = await b.newPage({ viewport: { width: w, height: h }, deviceScaleFactor: 1 });
    for (const t of ['light', 'dark']) {
      for (const [k, s, route] of PAIRS) {
        await p.goto(`${BRAND}/v2/mock.html?d=daylight&s=${s}&t=${t}&v=before`); await p.waitForTimeout(500); await p.evaluate(() => document.fonts.ready);
        await p.screenshot({ path: path.join(OUT, `${k}-${dev}-${t}-approved.jpg`), type: 'jpeg', quality: 72 });
        await proto(p, route, t); await p.screenshot({ path: path.join(OUT, `${k}-${dev}-${t}-prototype.jpg`), type: 'jpeg', quality: 72 });
      }
      if (dev === 'desktop' && t === 'light') for (const [k, route] of SOLO) { await proto(p, route, t); await p.screenshot({ path: path.join(OUT, `${k}-${dev}-${t}-prototype.jpg`), type: 'jpeg', quality: 72 }); }
    }
    await p.close(); }
  await b.close(); })();
