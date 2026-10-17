// The iPhone as Daniel sees it: WebKit, iPhone 15, the OS in dark or light mode, the prototype theme left on
// auto or set explicitly, Safari's floating toolbar and the safe areas simulated (?sim=ios: a 34px home
// indicator inset and an 80px toolbar overlay that takes taps). On every route:
//   - exactly one theme: the one chosen, or the OS's when none is (never a mix of surfaces and text);
//   - nothing wider than the viewport (except inside a scroller that clips it);
//   - the tab bar's labels and every primary action are fully visible and not under the toolbar;
//   - rendered text against its rendered background is readable (≥ 3:1 everywhere; the share under 4.5:1
//     is reported).
// Runs only in the WebKit projects (WEBKIT=1; see run-webkit.sh). Screenshots land in OUT_DIR/iphone/.
const fs = require('fs');
const path = require('path');
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');

const SKIP = new Set(['#/signin', '#/enroll']); // full-screen gates without the app chrome; checked separately below

/** In-page audit: theme, overflow, overlap with the simulated toolbar, contrast. */
const audit = () => {
  const h = document.documentElement, iw = innerWidth, ov = document.getElementById('sim-toolbar');
  const ovTop = ov ? ov.getBoundingClientRect().top : innerHeight;
  const rgba = (c) => { const m = String(c).match(/rgba?\(([^)]+)\)/); if (!m) return null; const [r, g, b, a = 1] = m[1].split(/[ ,/]+/).filter(Boolean).map(Number); return { r, g, b, a }; };
  const lum = ({ r, g, b }) => { const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
  const over = (top, under) => ({ r: top.r * top.a + under.r * (1 - top.a), g: top.g * top.a + under.g * (1 - top.a), b: top.b * top.a + under.b * (1 - top.a), a: 1 });
  const bodyBg = rgba(getComputedStyle(document.body).backgroundColor);
  const bgOf = (el) => { const stack = []; for (let e = el; e && e.nodeType === 1; e = e.parentElement) { const c = rgba(getComputedStyle(e).backgroundColor); if (c && c.a > 0) { stack.push(c); if (c.a >= 1) break; } } let base = bodyBg && bodyBg.a ? bodyBg : { r: 255, g: 255, b: 255, a: 1 }; for (let i = stack.length - 1; i >= 0; i--) base = stack[i].a >= 1 ? stack[i] : over(stack[i], base); return base; };
  const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
  const visible = (el) => { const r = el.getBoundingClientRect(), cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' && +cs.opacity > 0.05; };
  const label = (el) => `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : ''} "${(el.textContent || '').trim().slice(0, 40)}"`;
  // overflow: anything past the right edge that no clipping ancestor contains
  const wide = [];
  document.querySelectorAll('body *').forEach((el) => { if (el.closest('#sim-toolbar, .tooltip') || (el.ownerSVGElement)) return; const r = el.getBoundingClientRect(); if (r.width === 0 || r.right <= iw + 1) return; for (let a = el.parentElement; a; a = a.parentElement) { if (getComputedStyle(a).overflowX !== 'visible' && a.getBoundingClientRect().right <= iw + 1) return; } if (visible(el)) wide.push(label(el) + ` right=${Math.round(r.right)}`); });
  // the tab bar and primary actions: fully above the toolbar, and the topmost thing at their centre
  const hidden = [];
  const check = (el, what) => { const r = el.getBoundingClientRect(); if (r.bottom > ovTop + 0.5) { hidden.push(`${what} ${label(el)} bottom=${Math.round(r.bottom)} > toolbar ${Math.round(ovTop)}`); return; } const t = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); if (!t || !(el === t || el.contains(t) || t.contains(el))) hidden.push(`${what} ${label(el)} covered by ${t ? label(t) : 'nothing'}`); };
  document.querySelectorAll('#tabbar a').forEach((a) => check(a, 'tab'));
  // contrast of rendered text
  const low = [], weak = []; let n = 0;
  const seen = new Set();
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let t = walker.nextNode(); t; t = walker.nextNode()) { if (!t.textContent.trim()) continue; const el = t.parentElement; if (!el || seen.has(el) || el.closest('#sim-toolbar, svg, .tooltip, [aria-hidden="true"], .sr-only, option')) continue; seen.add(el); if (!visible(el)) continue; const r = el.getBoundingClientRect(); if (r.bottom < 0 || r.top > innerHeight) continue; const fg = rgba(getComputedStyle(el).color); if (!fg || fg.a < 0.05) continue; const bg = bgOf(el), c = ratio(fg.a < 1 ? over(fg, bg) : fg, bg); n++; if (c < 3) low.push(`${label(el)} ${c.toFixed(2)}:1`); else if (c < 4.5) weak.push(c); }
  return { theme: h.dataset.theme, src: h.dataset.themeSource, ink: getComputedStyle(h).getPropertyValue('--ink').trim(), bg: getComputedStyle(h).getPropertyValue('--bg').trim(), wide, hidden, low, weakShare: n ? weak.length / n : 0, n, sw: h.scrollWidth, iw };
};

/** Scroll each primary action into view inside the app's scroller, then check it isn't covered. */
const primaries = () => { const ov = document.getElementById('sim-toolbar'), ovTop = ov ? ov.getBoundingClientRect().top : innerHeight, out = [];
  document.querySelectorAll('.page-head .actions .btn, .btn.primary, .wizard-foot .btn, .sticky-decide .btn').forEach((el) => { const r0 = el.getBoundingClientRect(); if (!r0.width || el.closest('.modal, .cdrawer, #pop-menu')) return; el.scrollIntoView({ block: 'center', behavior: 'instant' }); const r = el.getBoundingClientRect(); const t = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); if (r.bottom > ovTop + 0.5 || !(t && (el === t || el.contains(t)))) out.push(`${el.textContent.trim().slice(0, 30)} at ${Math.round(r.top)}–${Math.round(r.bottom)} covered by ${t ? t.tagName + (t.id ? '#' + t.id : '') + '.' + String(t.className).split(' ')[0] : 'nothing'}`); });
  return out; };

test.describe('iPhone (WebKit): one theme, nothing too wide, nothing under the toolbar, readable text', () => {
  for (const route of ROUTES.filter((r) => !SKIP.has(r))) {
    test(route, async ({ page, protoTheme, colorScheme }, info) => {
      await go(page, route, { fresh: true });
      await page.waitForTimeout(250);
      const want = protoTheme === 'auto' ? colorScheme : protoTheme;
      const a = await page.evaluate(audit);
      const dir = path.join(info.project.outputDir, 'iphone', info.project.name); fs.mkdirSync(dir, { recursive: true });
      await page.screenshot({ path: path.join(dir, route.replace(/[#/%]+/g, '_').replace(/^_+/, '') + '.png') });
      expect(a.theme, 'the applied theme').toBe(want);
      expect(a.src).toBe(protoTheme === 'auto' ? 'os' : 'explicit');
      expect(a.wide, `wider than the viewport on ${route}`).toEqual([]);
      expect(a.hidden, `tab bar under the toolbar on ${route}`).toEqual([]);
      expect(a.low, `text under 3:1 on ${route}`).toEqual([]);
      test.info().annotations.push({ type: 'contrast', description: `${route}: ${a.n} text runs, ${(a.weakShare * 100).toFixed(0)}% between 3:1 and 4.5:1` });
      expect(await page.evaluate(primaries), `primary actions covered on ${route}`).toEqual([]);
    });
  }
  test('the strip collapses to the status dot and the needs-you count; the rest is one tap away', async ({ page }) => {
    await go(page, '#/overview', { fresh: true });
    const shown = await page.locator('.strip .cell').evaluateAll((els) => els.filter((e) => getComputedStyle(e).display !== 'none').map((e) => e.className));
    expect(shown.map((c) => c.split(' ').pop())).toEqual(['status-btn', 'alert']);
    await page.locator('.strip .status-btn').tap();
    await expect(page.locator('#pop-menu')).toContainText('Sandbox enforcing');
    await page.locator('#pop-menu [data-href="#/safety"]').tap();
    await expect(page).toHaveURL(/#\/safety/);
  });
  test('the theme toggle is an explicit choice that beats the OS, and stays after a reload', async ({ page, colorScheme }) => {
    await go(page, '#/more', { fresh: true });
    const other = colorScheme === 'dark' ? 'light' : 'dark';
    await page.evaluate((t) => UI.theme(t), other);
    await go(page, '#/overview');
    expect(await page.evaluate(() => [document.documentElement.dataset.theme, document.documentElement.dataset.themeSource])).toEqual([other, 'explicit']);
    const a = await page.evaluate(audit);
    expect(a.low).toEqual([]);
    await page.evaluate(() => UI.theme('auto'));
    expect(await page.evaluate(() => document.documentElement.dataset.theme)).toBe(colorScheme);
  });
  test('sign-in and enrolment gates fit and read well', async ({ page }) => {
    for (const r of ['#/signin', '#/enroll']) { await go(page, r, { fresh: true }); const a = await page.evaluate(audit); expect(a.wide, r).toEqual([]); expect(a.low, r).toEqual([]); }
  });
});
