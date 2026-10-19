// The filter bar (items 35, 36): a connector's filters are qualifiers in the search field only, never menus; a
// qualifier applies on a pick or Enter, never half-typed; the filters that are on are pills in the field (click to
// edit, ✕ to remove, Backspace to take the last), one state with the menus.
const { test, expect, go } = require('./fixtures');

test('connector filters are qualifiers only: no Repo or PR menu, in the row, More filters or the phone sheet', async ({ page }) => {
  await go(page, '#/activity', { fresh: true });
  const bar = page.locator('.fb[data-fb="act"]');
  await expect(bar.locator('[data-fb-open*="."]')).toHaveCount(0);
  if (page.viewportSize().width <= 720) { await bar.locator('[data-fb-sheet]').click(); await expect(page.locator('.fb-rows')).not.toContainText(/Repo|Pull request|PR/); await expect(page.locator('.fb-rows .fb-gh')).toHaveCount(0); }
  else if (await bar.locator('[data-fb-more]').count()) { await bar.locator('[data-fb-more]').click(); await expect(page.locator('.fb-pop, .fb-menu').last()).not.toContainText(/Repo|PR/); }
});

test('a qualifier applies on Enter or a pick, never half-typed; it becomes a pill; menus and pills are one state', async ({ page }) => {
  await go(page, '#/activity', { fresh: true });
  const bar = page.locator('.fb[data-fb="act"]'), input = bar.locator('.fb-in');
  const state = () => page.evaluate(() => JSON.stringify({ s: FB.reg.act.state.status, r: FB.reg.act.state['github.repo'] }));
  const before = await state();
  await input.click(); await input.type('status:fai');
  expect(await state()).toBe(before);
  await bar.locator('.fb-ac li[data-i]').first().dispatchEvent('mousedown'); // a pick
  await expect(bar.locator('.fb-pill')).toContainText('status: Failed');
  await expect(input).toHaveValue('');
  await input.type('repo:acme/web'); await input.press('Enter');
  await expect(bar.locator('.fb-pill')).toHaveCount(2);
  // the Status menu shows what the pill says, and a menu pick updates the pill
  if (page.viewportSize().width > 720) {
    await expect(bar.locator('[data-fb-open="status"]')).toContainText('Failed');
    await bar.locator('[data-fb-open="status"]').click();
    await page.locator('.fb-opt', { hasText: 'Running' }).first().click();
    await page.keyboard.press('Escape');
    await expect(bar.locator('.fb-pill', { hasText: 'status:' })).toContainText(/Failed, Running|Running, Failed/);
  }
  // free text stays text after the pills
  await input.type('checkout');
  await expect(input).toHaveValue('checkout');
});

test('a pill edits in place, ✕ removes it, Backspace at the start takes the last one', async ({ page }) => {
  await go(page, '#/activity', { fresh: true });
  const bar = page.locator('.fb[data-fb="act"]'), input = bar.locator('.fb-in');
  await input.click(); await input.type('status:failed'); await input.press('Enter');
  await input.type('repo:acme/web'); await input.press('Enter');
  await expect(bar.locator('.fb-pill')).toHaveCount(2);
  // edit: it comes back as its qualifier, with the value suggestions open
  await bar.locator('.fb-pill', { hasText: 'repo:' }).locator('.fb-pv').click();
  await expect(input).toHaveValue(/^repo:acme\/web/);
  await expect(bar.locator('.fb-pill')).toHaveCount(1);
  await input.press('Enter');
  await expect(bar.locator('.fb-pill')).toHaveCount(2);
  // ✕
  await bar.locator('.fb-pill', { hasText: 'status:' }).locator('.fb-px').click();
  await expect(bar.locator('.fb-pill')).toHaveCount(1);
  // Backspace at the start: the first selects the last pill, the second removes it
  await input.focus(); await input.press('Home');
  await input.press('Backspace');
  await expect(bar.locator('.fb-pill.sel')).toHaveCount(1);
  await input.press('Backspace');
  await expect(bar.locator('.fb-pill')).toHaveCount(0);
});

test('one time range control, everywhere a time range appears: a clock, "Last … ▾", at the far right of its row, the same choices', async ({ page }) => {
  const phone = page.viewportSize().width <= 720;
  const check = async (where, scope) => {
    const t = page.locator(`${scope} .fb-time`);
    if (phone && !(await t.isVisible())) return; // a phone keeps it in the Filter sheet, except where it's the bar's only control
    await expect(t, where).toHaveCount(1);
    await expect(t.locator('svg').first(), where).toHaveAttribute('data-ico', 'clock');
    await expect(t, where).toContainText(/^(Last|All)/);
    const [r, bar] = await Promise.all([t.boundingBox(), t.locator('xpath=ancestor::div[contains(@class,"fb ")or@class="fb"][1]').boundingBox()]);
    expect(Math.abs(r.x + r.width - (bar.x + bar.width)), `${where}: at the far right`).toBeLessThanOrEqual(2);
  };
  await go(page, '#/activity', { fresh: true }); await check('Activity', '#main');
  await go(page, '#/logs/audit'); await check('Audit', '#main');
  await go(page, '#/logs/daemon'); await check('Daemon log', '#main');
  await go(page, '#/overview'); await check('Home chart', '.ov-runs');
  await go(page, '#/workflows/ci-fixer'); await page.locator('.dl-tabs a', { hasText: 'Runs' }).first().click(); await check('a workflow\'s runs', '#main');
  // the same choices everywhere
  await go(page, '#/activity');
  if (!phone) { await page.locator('.fb-time').click(); await expect(page.locator('.fb-opt')).toContainText(['Last hour', 'Last 12 hours', 'Last 24 hours', 'Last 7 days', 'Last 30 days', 'All time']); await page.keyboard.press('Escape'); }
});
