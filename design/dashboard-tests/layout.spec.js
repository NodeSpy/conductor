// Desktop layout and contrast sweep, both themes: on every route nothing is wider than the window (outside a
// scroller that clips it), the page renders exactly the theme asked for, and rendered text meets the brand's
// minimums (4.5:1, or 3:1 for large text) against its rendered background. (The iPhone version, with the Safari toolbar simulated, is iphone.spec.js.)
const { test, expect, go, isPhone } = require('./fixtures');
const { ROUTES } = require('./routes');
const { audit } = require('./audit');

for (const theme of ['light', 'dark']) {
  test(`every route, ${theme} theme: fits the window, one theme, readable text`, async ({ page }) => {
    test.skip(isPhone(page), 'the phone layout is swept in WebKit by iphone.spec.js');
    test.setTimeout(10 * 60 * 1000);
    const problems = [];
    for (const r of ROUTES) {
      await page.evaluate((t) => localStorage.setItem('cd-theme', t), theme);
      await go(page, r, { fresh: true });
      const a = await page.evaluate(audit);
      if (a.theme !== theme) problems.push(`${r}: theme ${a.theme}`);
      a.wide.forEach((w) => problems.push(`${r}: too wide: ${w}`));
      a.low.forEach((l) => problems.push(`${r}: low contrast: ${l}`));
    }
    expect(problems).toEqual([]);
  });
}
