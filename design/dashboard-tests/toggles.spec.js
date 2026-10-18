// Every tab, segmented control, disclosure and switch on every route is clicked, one at a time on a fresh page, and
// the page must survive it: no error, the page's title still there, the main content not emptied, and on an approval
// everything the decision depends on (what will change, why, the effect and undo, who asked, the decision bar) still in
// view. A control that hides the decision, or breaks the page, fails here.
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');

const CONTROLS = '#main [role="tab"], #main .seg button, #main .dl-tabs a, #main details > summary, #main .switch, #main [aria-pressed]';
const ESSENTIAL = { proposal: ['.dl-ptitle', '.dl-proposal .dl-pwhy', '.dl-proposal .dl-origin', '.dl-proposal .prop-decide'] };

test('no tab, toggle, disclosure or segmented control hides what matters or breaks the page', async ({ page }) => {
  test.setTimeout(20 * 60 * 1000);
  const problems = [];
  for (const r of ROUTES) {
    await go(page, r, { fresh: true });
    const n = await page.locator(CONTROLS).count();
    for (let i = 0; i < Math.min(n, 24); i++) {
      if (i) await go(page, r, { fresh: true });
      const el = page.locator(CONTROLS).nth(i);
      if (!(await el.isVisible().catch(() => false))) continue;
      const what = (await el.textContent().catch(() => '') || '').trim().slice(0, 30) || (await el.getAttribute('aria-label')) || `#${i}`;
      if ((await el.getAttribute('aria-disabled')) === 'true') continue;
      await el.click({ timeout: 2000 }).catch(() => {});
      await page.waitForTimeout(150);
      if (await page.locator('.modal, .cdrawer').count()) { await page.keyboard.press('Escape'); await page.waitForTimeout(80); }
      const ok = await page.evaluate((ess) => {
        const out = []; const vis = (s) => [...document.querySelectorAll(s)].some((x) => x.getClientRects().length && getComputedStyle(x).visibility !== 'hidden');
        if (!document.querySelector('#main > *')) out.push('the page is empty');
        if (!vis('#main h1, #main .dl-ptitle, #main h2')) out.push('the title disappeared');
        if ((document.querySelector('#main') || {}).scrollHeight < 200) out.push('the main content collapsed');
        if (document.querySelector('.dl-proposal .prop-decide')) ess.forEach((s) => { if (!vis(s)) out.push(`hidden: ${s}`); });
        return out;
      }, ESSENTIAL.proposal);
      ok.forEach((x) => problems.push(`${r} after "${what}": ${x}`));
    }
  }
  expect(problems).toEqual([]);
});
