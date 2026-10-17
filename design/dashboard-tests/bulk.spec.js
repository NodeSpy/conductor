// Bulk: tick rows (shift-click for a range), a sticky bar acts on all of them, and config changes land as one
// change set in one diff (here non-widening, so a plain Apply). (Riley M2)
const { test, expect, go } = require('./fixtures');

test('workflows: select two, move both to a folder, one diff, applied without a passkey', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  await page.locator('input[data-act="wl-sel"][data-name="ci-fixer"]').click();
  await page.locator('input[data-act="wl-sel"][data-name="dep-bump"]').click();
  await expect(page.locator('.bulk-bar')).toContainText('2 workflows selected');
  await page.locator('[data-act="wl-bulk-move"]').click();
  await page.locator('#f-to').selectOption('personal');
  await page.locator('#gen-submit').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  const staged = await page.evaluate(() => D.pending.map((c) => c.key));
  expect(staged).toEqual(expect.arrayContaining(['ci-fixer', 'dep-bump']));
  await expect(page.locator('#chk-state')).toContainText('passed');
  await expect(page.locator('#pk-why')).toContainText('No passkey needed');
});

test('workflows: shift-click selects a range; disable stages enabled: false', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  const boxes = page.locator('#wl-body tr[data-row]:not(.hidden) input[data-act="wl-sel"]');
  await boxes.nth(0).click();
  await boxes.nth(2).click({ modifiers: ['Shift'] });
  await expect(page.locator('.bulk-bar')).toContainText('3 workflows selected');
  await page.locator('[data-act="wl-bulk-clear"]').click();
  await expect(page.locator('.bulk-bar')).toHaveCount(0);
  await page.locator('input[data-act="wl-sel"][data-name="ci-fixer"]').click();
  await page.locator('[data-act="wl-bulk-enable"][data-v="false"]').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  expect(await page.evaluate(() => D.pending.find((c) => c.key === 'ci-fixer').after)).toContain('enabled: false');
});

test('runs: retry the failed ones in one go; export the selection', async ({ page }) => {
  await go(page, '#/runs', { fresh: true });
  await page.locator('input[data-act="hist-sel-row"][data-id="r-7ef0"]').click();
  await page.locator('input[data-act="hist-sel-row"][data-id="r-7e64"]').click();
  await expect(page.locator('.bulk-bar')).toContainText('2 runs selected');
  await expect(page).toHaveURL(/#\/runs$/); // ticking doesn't open the row
  await page.locator('[data-act="hist-bulk-retry"]').click();
  await page.locator('#cf-ok').click();
  expect(await page.evaluate(() => D.runs.filter((r) => ['r-7ef0', 'r-7e64'].includes(r.retry_of)).length)).toBe(2);
});
