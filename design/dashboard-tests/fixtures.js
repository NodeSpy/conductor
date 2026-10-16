// Shared fixture: every test starts from fresh demo data, runs simulated waits
// fast, and fails on any console error or uncaught exception.
const base = require('@playwright/test');

const test = base.test.extend({
  page: async ({ page }, use, info) => {
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error') errors.push(`console: ${m.text()}`); });
    page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
    await page.addInitScript(() => { window.__PROTO_FAST = true; });
    await page.goto('/index.html#/overview');
    await page.evaluate(() => { localStorage.removeItem('cd-proto-state-v3'); localStorage.setItem('cd-theme', 'dark'); });
    await use(page);
    base.expect(errors, `console errors in ${info.title}`).toEqual([]);
  },
});

/** Navigate to a route with a reload, so module state resets too. */
async function go(page, route, { fresh = false } = {}) {
  if (fresh) await page.evaluate(() => localStorage.removeItem('cd-proto-state-v3'));
  await page.goto('/index.html?r=' + Date.now() + route);
  await page.waitForSelector('#main > *');
  await page.waitForTimeout(80);
}
const isPhone = (page) => page.viewportSize().width <= 720;
/** Complete the staged-change pipeline: wait for checks, ack widening, sign, wait for commit. */
async function applyStaged(page, { expectWidening = false } = {}) {
  await page.waitForURL(/#\/config\/apply/);
  const go2 = page.locator('[data-act="apply-go"]');
  await base.expect(page.locator('#chk-state')).not.toContainText('running');
  await base.expect(page.locator('#chk-state')).toContainText('passed');
  const ack = page.locator('.ack input[type=checkbox]');
  if (expectWidening) { await base.expect(go2).toHaveAttribute('aria-disabled', 'true'); await ack.check(); }
  else if (await ack.count()) await ack.check();
  await base.expect(go2).not.toHaveAttribute('aria-disabled', 'true');
  await go2.click();
  await page.locator('#su-go').click();
  await base.expect(page.locator('.page-head h1')).toHaveText(/Applied/);
  await base.expect(page.locator('.callout.ember')).toContainText('Applied.', { timeout: 8000 });
}
async function stagedCount(page) { return page.evaluate(() => window.D.pending.length); }

module.exports = { test, expect: base.expect, go, isPhone, applyStaged, stagedCount };
