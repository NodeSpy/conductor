// The measured layout checks (desktop-audit.md, the measuring scripts behind it ported into measure.js), on every
// route, desktop and phone, in both themes: one control height in a row, the filter toolbar as one component, the
// stack gap between panels, row and icon centring, a title's size for its kind of page, equal space above and below
// a single line (a collapsed disclosure, a button, a chip, a row), nothing past the window, no text cut off.
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');
const { measure } = require('./measure');

for (const theme of ['dark', 'light']) {
  test(`every route, ${theme} theme: the measured layout checks pass`, async ({ page }) => {
    test.setTimeout(10 * 60 * 1000);
    const problems = [];
    for (const r of ROUTES) {
      await page.evaluate((t) => localStorage.setItem('cd-theme', t), theme);
      await go(page, r, { fresh: true });
      (await page.evaluate(measure)).forEach((p) => problems.push(`${r}: ${p}`));
    }
    expect(problems).toEqual([]);
  });
}

test('the filter toolbar is one component: the same parts on every page, one height, one centre line', async ({ page }) => {
  const phone = page.viewportSize().width <= 720;
  for (const r of ['#/tickets', '#/workflows', '#/bots', '#/activity', '#/attention', '#/secrets', '#/extensions/browse', '#/logs/audit']) {
    await go(page, r, { fresh: true });
    const fb = page.locator('#main .fb').first();
    await expect(fb, r).toBeVisible();
    await expect(fb.locator('.fb-search .fb-in'), r).toBeVisible();
    if (!phone) await expect(fb.locator('.fb-by'), `${r}: Filter by… in the field`).toBeVisible();
    const parts = await fb.evaluate((el) => [...el.querySelectorAll('.fb-search, .fb-btn, .fb-phone, .seg')].filter((x) => x.offsetParent && getComputedStyle(x).display !== 'none').map((x) => { const b = x.getBoundingClientRect(); return { h: Math.round(b.height), mid: (b.top + b.bottom) / 2, t: (x.innerText || '').trim().split('\n')[0] }; }));
    const want = phone ? 44 : 36;
    for (const p of parts) { expect(p.h, `${r}: ${p.t} height`).toBe(want); expect(Math.abs(p.mid - parts[0].mid), `${r}: ${p.t} off the bar's centre line`).toBeLessThanOrEqual(1); }
  }
  // the view toggle is the toolbar's last part, after Group by, on the same line
  await go(page, '#/tickets', { fresh: true });
  const seg = page.locator('#main .fb .fb-bar .seg.tk-view');
  await expect(seg).toBeVisible();
  if (!phone) { // on the bar's centre line, after the menus
    const [g, s] = await Promise.all([page.locator('#main .fb .fb-btn').first().boundingBox(), seg.boundingBox()]);
    expect(Math.abs((g.y + g.height / 2) - (s.y + s.height / 2))).toBeLessThanOrEqual(1);
    expect(s.x).toBeGreaterThan(g.x);
  }
});
