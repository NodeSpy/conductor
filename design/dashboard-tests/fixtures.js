// Shared fixture: every test starts from fresh demo data, runs simulated waits
// fast, and fails on any console error or uncaught exception.
const base = require('@playwright/test');

const test = base.test.extend({
  // project options: the prototype theme ('auto' follows the OS colorScheme) and the iOS Safari simulation
  protoTheme: ['dark', { option: true }],
  simIos: [false, { option: true }],
  page: async ({ page, protoTheme, simIos }, use, info) => {
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error') errors.push(`console: ${m.text()}`); });
    page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
    await page.addInitScript(() => { window.__PROTO_FAST = true; });
    await page.goto(`/index.html${simIos ? '?sim=ios' : ''}#/overview`);
    await page.evaluate((t) => { localStorage.removeItem('cd-proto-state-v5'); if (t === 'auto') localStorage.removeItem('cd-theme'); else localStorage.setItem('cd-theme', t); }, protoTheme);
    await use(page);
    base.expect(errors, `console errors in ${info.title}`).toEqual([]);
  },
});

/** Navigate to a route with a reload, so module state resets too. */
async function go(page, route, { fresh = false } = {}) {
  if (fresh) await page.evaluate(() => localStorage.removeItem('cd-proto-state-v5'));
  await page.goto('/index.html?r=' + Date.now() + route);
  await page.waitForSelector('#main > *');
  // let the page's entrance animation settle, so drags and positions are measured on a still page
  await page.waitForFunction(() => document.getAnimations().filter((a) => a.playState === 'running' && a.effect && a.effect.target && a.effect.target.closest && a.effect.target.closest('.page')).every((a) => a.effect.getComputedTiming().iterations !== 1 || (a.currentTime || 0) >= 400), null, { timeout: 2000 }).catch(() => {});
  await page.waitForTimeout(40);
}
const isPhone = (page) => page.viewportSize().width <= 720;
/** Complete the staged-change pipeline: wait for checks, ack widening, sign if it widens, wait for commit. */
async function applyStaged(page, { expectWidening = false } = {}) {
  await page.waitForURL(/#\/config\/apply/);
  const go2 = page.locator('[data-act="apply-go"]');
  await base.expect(page.locator('#chk-state')).not.toContainText('running');
  await base.expect(page.locator('#chk-state')).toContainText('passed');
  const ack = page.locator('.ack input[type=checkbox]');
  if (expectWidening) { await base.expect(go2).toHaveAttribute('aria-disabled', 'true'); await ack.check(); }
  else if (await ack.count()) await ack.check();
  await base.expect(go2).not.toHaveAttribute('aria-disabled', 'true');
  const needsPk = (await page.locator('#pk-why.needs').count()) > 0;
  await go2.click();
  if (needsPk) await page.locator('#su-go').click(); // only a widening change asks for the passkey
  await base.expect(page.locator('.page-head h1')).toHaveText(/Applied/);
  await base.expect(page.locator('.callout.ember')).toContainText('Applied.', { timeout: 8000 });
}
async function stagedCount(page) { return page.evaluate(() => window.D.pending.length); }

/** Click a page-head (or bot/team header) action; on a phone, actions beyond the first two live in the ⋯ menu. */
async function headAction(page, selector) {
  const el = page.locator(selector).first();
  if (await el.isVisible()) return el.click();
  const label = (await el.textContent()).trim();
  await page.locator('.page-head .ph-more, .bot-hero .ph-more').first().click();
  await page.locator('#pop-menu button', { hasText: label }).click();
}
module.exports = { test, expect: base.expect, go, isPhone, applyStaged, stagedCount, headAction };
