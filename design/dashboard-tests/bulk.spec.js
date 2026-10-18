// Bulk: tick rows; a selection bar says how many and the actions that apply (Clear to drop it); config changes land as one change set in one
// diff (here non-widening, so a plain Apply); destructive actions confirm. (Riley M2)
const { test, expect, go, isPhone } = require('./fixtures');

test('workflows: select two, switch both off, one diff, applied without a passkey', async ({ page }) => {
  test.skip(isPhone(page), 'bulk selection is desktop-only');
  await go(page, '#/workflows', { fresh: true });
  await page.locator('input[data-act="wl-sel"][data-name="ci-fixer"]').check();
  await page.locator('input[data-act="wl-sel"][data-name="dep-bump"]').check();
  await expect(page.locator('.m-bulk')).toContainText('2 selected');
  await page.locator('[data-act="wl-bulk-enable"][data-v="false"]').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  const staged = await page.evaluate(() => D.pending.map((c) => c.key));
  expect(staged).toEqual(expect.arrayContaining(['ci-fixer', 'dep-bump']));
  expect(await page.evaluate(() => D.pending.find((c) => c.key === 'ci-fixer').after)).toContain('enabled: false');
  await expect(page.locator('#chk-state')).toContainText('passed');
});

test('phone: Activity shows what needs attention and what runs; finished runs are one line; no bulk selection', async ({ page }) => {
  test.skip(!isPhone(page), 'phone list');
  await go(page, '#/activity', { fresh: true });
  await expect(page.locator('#act-phone input.cbx')).toHaveCount(0);
  await expect(page.locator('#act-phone .ph-done')).toContainText(/\d+ finished/);
  await expect(page.locator('#act-phone .dl-runs-phone')).toHaveCount(2); // needs attention, running
  await page.locator('#act-phone .ph-done').click();
  await expect(page.locator('#act-phone .dl-runs-phone')).toHaveCount(3);
});

test('runs: the selection bar takes the header row; retry the failed ones in one go; stop running ones with a confirmation', async ({ page }) => {
  test.skip(isPhone(page), 'the desktop table (the phone list has its own test)');
  await go(page, '#/activity', { fresh: true });
  await page.locator('#act-tbl input[data-act="act-sel-row"][data-id="r-7ef0"]').click();
  await page.locator('#act-tbl input[data-act="act-sel-row"][data-id="r-7e64"]').click();
  const bar = page.locator('#act-tbl thead.selecting .dl-selbar');
  await expect(bar).toContainText('2 selected');
  await expect(bar).toContainText(/Select all \d+ matching/);
  await expect(page).toHaveURL(/#\/activity$/); // ticking doesn't open the row
  // pinned: it stays at the top of the list while you scroll
  expect(await bar.evaluate((el) => getComputedStyle(el.closest('th')).position)).toBe('sticky');
  await expect(page.locator('#act-tbl .dl-selbar button', { hasText: 'Stop' })).toHaveAttribute('aria-disabled', 'true'); // nothing selected is running
  await page.locator('#act-tbl [data-act="act-bulk-retry"]').click();
  await page.locator('#cf-ok').click();
  expect(await page.evaluate(() => D.runs.filter((r) => ['r-7ef0', 'r-7e64'].includes(r.retry_of)).length)).toBe(2);
  await expect(page.locator('.dl-selbar')).toHaveCount(0); // the header row is back
  await page.locator('#act-tbl input[data-act="act-sel-row"][data-id="r-7f2c"]').click();
  await page.locator('#act-tbl [data-act="act-bulk-stop"]').click();
  await expect(page.locator('#cf-cancel')).toBeFocused();
  await page.locator('#cf-ok').click();
  expect(await page.evaluate(() => D.runs.find((r) => r.id === 'r-7f2c').status)).toBe('stopped');
  // nothing exports: no export action anywhere on the page
  await expect(page.getByText(/export/i)).toHaveCount(0);
});
