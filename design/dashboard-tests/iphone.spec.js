// The iPhone as the operator sees it: WebKit, iPhone 15, the OS in dark or light mode, the prototype theme left on
// auto or set explicitly, Safari's floating toolbar and the safe areas simulated (?sim=ios: a 34px home
// indicator inset and an 80px toolbar overlay that takes taps). On every route:
//   - exactly one theme: the one chosen, or the OS's when none is (never a mix of surfaces and text);
//   - nothing wider than the viewport (except inside a scroller that clips it);
//   - the tab bar's labels and every primary action are fully visible and not under the toolbar;
//   - rendered text against its rendered background meets the brand's minimums (4.5:1, 3:1 for large text;
//     disabled controls exempt).
// Runs only in the WebKit projects (WEBKIT=1; see run-webkit.sh). Screenshots land in OUT_DIR/iphone/.
const fs = require('fs');
const path = require('path');
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');

const SKIP = new Set(['#/signin', '#/enroll']); // full-screen gates without the app chrome; checked separately below

const { audit, primaries } = require('./audit');
const { walkWizards, stepperAudit } = require('./wizards');

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
      expect(a.low, `text under the brand's contrast minimums on ${route}`).toEqual([]);
      expect(await page.evaluate(stepperAudit), `wizard header on ${route}`).toEqual([]);
      test.info().annotations.push({ type: 'contrast', description: `${route}: ${a.n} text runs, ${(a.weakShare * 100).toFixed(0)}% between 3:1 and 4.5:1` });
      expect(await page.evaluate(primaries), `primary actions covered on ${route}`).toEqual([]);
    });
  }
  test('the header is the page and your menu; the machine and its update are on More', async ({ page }) => {
    await go(page, '#/overview', { fresh: true });
    await expect(page.locator('.strip .logo-mark path').first()).toBeAttached(); // the logo mark renders (from assets/logo/mark.svg)
    const shown = await page.locator('.strip .cell').evaluateAll((els) => els.filter((e) => getComputedStyle(e).display !== 'none').map((e) => e.className));
    expect(shown.map((c) => c.split(' ').pop())).toEqual(['op-btn']); // no status menu: what needs you is in Needs you
    await page.locator('.tabbar a[data-nav="more"]').tap();
    await expect(page.locator('.more-host .host')).toContainText('installs within 10 min');
    await page.locator('.more-host .host').tap();
    await expect(page).toHaveURL(/#\/updates/);
  });
  test('wizard headers fit in every state: no overlap, wrap or clipping', async ({ page }) => {
    test.setTimeout(3 * 60 * 1000);
    expect(await walkWizards(page)).toEqual([]);
  });
  test('the logo: the mark renders and holds contrast in this theme; the 16px favicon cut draws', async ({ page }) => {
    await go(page, '#/overview', { fresh: true });
    const r = await page.evaluate(async () => {
      const lum = (c) => { const v = String(c).match(/\d+(\.\d+)?/g).slice(0, 3).map(Number).map((x) => { x /= 255; return x <= 0.03928 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4); }); return 0.2126 * v[0] + 0.7152 * v[1] + 0.0722 * v[2]; };
      const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
      const path = document.querySelector('.strip .logo-mark path'), bar = getComputedStyle(document.querySelector('.strip')).backgroundColor;
      const ink = getComputedStyle(path).fill, dot = getComputedStyle(document.querySelector('.strip .logo-mark circle')).fill;
      const box = document.querySelector('.strip .logo-mark').getBoundingClientRect();
      // the favicon's 16px cut, drawn at 16×16: it must put real ink on the canvas
      const img = new Image(16, 16); img.src = 'assets/logo/favicon.svg'; await img.decode();
      const cv = document.createElement('canvas'); cv.width = cv.height = 16; const cx = cv.getContext('2d'); cx.drawImage(img, 0, 0, 16, 16);
      const px = cx.getImageData(0, 0, 16, 16).data; let ink16 = 0; for (let i = 3; i < px.length; i += 4) if (px[i] > 128) ink16++;
      return { arch: ratio(ink, bar), dot: ratio(dot, bar), h: box.height, ink16 };
    });
    expect(r.h, 'the mark is drawn at its chrome size').toBeGreaterThanOrEqual(16);
    expect(r.arch, 'the arch against the header').toBeGreaterThanOrEqual(4.5);
    expect(r.dot, 'the dot against the header').toBeGreaterThanOrEqual(3);
    expect(r.ink16, 'the 16px favicon cut has ink').toBeGreaterThan(20);
    const dir = require('path').join(test.info().project.outputDir, 'iphone', test.info().project.name); require('fs').mkdirSync(dir, { recursive: true });
    await page.locator('.strip .brand').screenshot({ path: require('path').join(dir, 'logo-strip.png') });
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
