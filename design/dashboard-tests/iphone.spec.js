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

const { audit, primaries } = require('./audit');

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
