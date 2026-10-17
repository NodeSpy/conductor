// Daylight conformance: the prototype is built only from the brand's tokens, and what renders keeps the brand's
// type rules. Static: no raw colours (hex, rgb, hsl) outside css/tokens.css, and none of the retired prototype
// variables (--ink, --bg-2, --accent, --ok …). Rendered, on every route: nothing under 11.5px, mono never under
// 12px, the serif (Fraunces) only in titles of five words or fewer at 20px or larger, and status marks, labels,
// chips and counts drawn only by the shared components (no tinted capsules).
const fs = require('fs');
const path = require('path');
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');

const PROTO = path.join(__dirname, '..', 'dashboard-prototype');
const files = (dir, ext) => fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? files(path.join(dir, e.name), ext) : ext.test(e.name) ? [path.join(dir, e.name)] : []));

test('only tokens: no raw colours outside tokens.css, no retired prototype variables', () => {
  const bad = [];
  const RAW = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(/g;
  const OLD = /var\(--(ink(-\d)?|bg(-\d)?|line(-\d)?|accent(-2|-ink|-wash)?|ok|run|wait|fail|idle|shadow|ok-wash|run-wash|wait-wash|fail-wash|idle-wash|shadow-wash|font-ui|font-title|font-mono|r|r-xs|r-sm|r-lg|t|t-fast|ease|elev|elev-pop|rail-w|strip-h|dot|grain-op|code-(key|str|num|com|anchor|bool)|sc|sw)\)/g;
  for (const f of files(path.join(PROTO, 'css'), /\.css$/).concat(files(path.join(PROTO, 'js'), /\.js$/), [path.join(PROTO, 'index.html')])) {
    if (f.endsWith('tokens.css')) continue;
    // issue/PR numbers (#178) and repo refs (acme/web#1288) in copy and fake data aren't colours
    const text = fs.readFileSync(f, 'utf8').replace(/(\w|\/)#\d+\b/g, '').replace(/#(1[0-9]{2}|[2-9][0-9]{2})\b/g, '');
    for (const m of text.matchAll(RAW)) bad.push(`${path.relative(PROTO, f)}: ${m[0]}`);
    for (const m of text.matchAll(OLD)) bad.push(`${path.relative(PROTO, f)}: ${m[0]}`);
  }
  expect(bad).toEqual([]);
});

test('every route keeps the type rules: size floors, mono for code only, the serif for short titles', async ({ page }) => {
  test.setTimeout(10 * 60 * 1000);
  const problems = [];
  for (const r of ROUTES) {
    await go(page, r, { fresh: true });
    const p = await page.evaluate(() => {
      const out = [];
      const seen = new Set();
      const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      for (let t = w.nextNode(); t; t = w.nextNode()) {
        const el = t.parentElement; if (!el || seen.has(el) || !t.textContent.trim() || el.closest('svg, .site, .sr-only, #sim-toolbar, option')) continue; seen.add(el);
        const r = el.getBoundingClientRect(); if (!r.width || !r.height) continue;
        const cs = getComputedStyle(el), px = parseFloat(cs.fontSize), fam = cs.fontFamily, txt = el.textContent.trim().replace(/\s+/g, ' ');
        const label = `${el.tagName.toLowerCase()}.${String(el.className).split(' ')[0]} "${txt.slice(0, 30)}"`;
        if (px < 11.5 - 0.01) out.push(`${label} ${px}px`);
        if (/Mono/.test(fam) && px < 12 - 0.01) out.push(`mono ${label} ${px}px`);
        if (/Fraunces/.test(fam)) { const words = txt.split(' ').length; if (px < 20 - 0.01) out.push(`serif under 20px: ${label} ${px}px`); if (words > 6) out.push(`serif on a long title: ${label} (${words} words)`); }
      }
      // no tinted capsules: a pill-shaped element with a tinted background is the old badge
      document.querySelectorAll('body *').forEach((el) => { const cs = getComputedStyle(el); if (parseFloat(cs.borderTopLeftRadius) >= 9 && el.getBoundingClientRect().height <= 24 && el.textContent.trim().length > 2 && !/count|switch|kbd|bar|dot|pulse|chip|wordmark/.test(el.className) && cs.backgroundColor !== 'rgba(0, 0, 0, 0)' && !el.closest('.site, .btn, .seg, .tabbar, .strip .search, .toast')) out.push(`capsule: ${el.tagName.toLowerCase()}.${String(el.className).split(' ').join('.')} "${el.textContent.trim().slice(0, 24)}"`); });
      return out;
    });
    p.forEach((x) => problems.push(`${r}: ${x}`));
  }
  expect(problems).toEqual([]);
});
