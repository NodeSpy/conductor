// Regression test for theme scoping: an explicit data-theme must always beat the OS
// preference and any UA auto-darkening, for surfaces and text, at every nesting level.
//
//   node tools/theme-test.mjs [engine,engine]   (serve design/brand over HTTP; BRAND_BASE=url)
//
// For each engine × OS colour scheme × root theme (auto, light, dark) it loads
// tokens/test/theme-fixture.html and checks every probe twice:
//   1. computed styles: background and text equal the expected theme's token values;
//   2. painted pixels: the screenshot under each probe equals that background, which catches
//      browsers that repaint colours after the cascade (force-dark, in-app WebViews).
// Exits 1 on any mismatch.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { available, context, closeAll } from './browsers.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const T = JSON.parse(fs.readFileSync(path.join(here, '..', 'tokens', 'tokens.json'), 'utf8'));
const BASE = process.env.BRAND_BASE || 'http://localhost:8768';

// Expected values straight from tokens.json, so the test follows the tokens.
const get = (p) => p.split('.').reduce((o, k) => o[k], T);
function hex(pathStr, theme) {
  let v = get(pathStr);
  v = v.$extensions?.mode?.[theme] ?? v.$value;
  while (typeof v === 'string' && v.startsWith('{')) { const t = get(v.slice(1, -1)); v = t.$extensions?.mode?.[theme] ?? t.$value; }
  return v.hex;
}
const EXPECT = (theme) => ({ bg: hex('semantic.color.bg.surface', theme), fg: hex('semantic.color.text.primary', theme), canvas: hex('semantic.color.bg.canvas', theme) });
const rgb = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const close = (a, b, tol = 10) => a.every((x, i) => Math.abs(x - b[i]) <= tol);
const parse = (s) => (s.match(/[\d.]+/g) || []).slice(0, 3).map(Number);

const PROBES = { inherit: null, light: 'light', darkInLight: 'dark', dark: 'dark', lightInDark: 'light' };
let fails = 0, checks = 0;
for (const engine of available(process.argv[2])) {
  for (const os of engine.osModes ?? ['light', 'dark']) {
    const ctx = await context(engine, os);
    const page = await ctx.newPage();
    for (const root of ['', 'light', 'dark']) {
      await page.goto(`${BASE}/tokens/test/theme-fixture.html${root ? '?root=' + root : ''}`);
      await page.waitForTimeout(150);
      const shot = await page.screenshot({ fullPage: true });
      const pageTheme = root || os;
      const facts = await page.evaluate(async ({ ids, b64 }) => {
        const img = new Image(); img.src = 'data:image/png;base64,' + b64; await img.decode();
        const c = document.createElement('canvas'); c.width = img.width; c.height = img.height;
        const x = c.getContext('2d'); x.drawImage(img, 0, 0);
        const dpr = img.width / document.documentElement.scrollWidth;
        const px = (cx, cy) => [...x.getImageData(Math.round(cx * dpr), Math.round(cy * dpr), 1, 1).data.slice(0, 3)];
        const out = { scheme: getComputedStyle(document.documentElement).colorScheme, body: px(4, 4) };
        for (const id of ids) {
          const el = document.getElementById(id), r = el.getBoundingClientRect(), cs = getComputedStyle(el);
          out[id] = { bg: cs.backgroundColor, fg: cs.color, pixel: px(r.left + 4, r.top + scrollY + 4) };
        }
        return out;
      }, { ids: Object.keys(PROBES), b64: shot.toString('base64') });
      const where = `${engine.id} · OS ${os} · root ${root || 'auto'}`;
      checks++;
      if (facts.scheme === 'normal') { fails++; console.log(`FAIL ${where}: <html> declares no color-scheme, so browsers may auto-darken it`); }
      checks++;
      if (!close(facts.body, rgb(EXPECT(pageTheme).canvas))) { fails++; console.log(`FAIL ${where}: page painted ${facts.body} but canvas should be ${EXPECT(pageTheme).canvas}`); }
      for (const [id, forced] of Object.entries(PROBES)) {
        const want = EXPECT(forced || pageTheme), got = facts[id];
        checks += 3;
        if (!close(parse(got.bg), rgb(want.bg), 1)) { fails++; console.log(`FAIL ${where} #${id}: computed background ${got.bg}, want ${want.bg}`); }
        if (!close(parse(got.fg), rgb(want.fg), 1)) { fails++; console.log(`FAIL ${where} #${id}: computed text ${got.fg}, want ${want.fg}`); }
        if (!close(got.pixel, rgb(want.bg))) { fails++; console.log(`FAIL ${where} #${id}: painted ${got.pixel}, want ${want.bg} (the browser recoloured it)`); }
      }
    }
    await ctx.close();
  }
  console.log(`${engine.id}: done`);
}
await closeAll();
console.log(`${checks} checks, ${fails} failing`);
process.exit(fails ? 1 : 0);
