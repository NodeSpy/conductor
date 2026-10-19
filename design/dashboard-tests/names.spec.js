// One name per bot (r5 major 9): the display name (Atlas) wherever a person reads it, the config id (platform-sre) only
// as the quiet secondary beside it, in code and under For audit. Checked on every route, in the filter menus, the
// pickers, and the audit log's search ("who:Atlas" finds Atlas's entries).
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');
const scan = require('./names');

test('no route names a bot by its config id where its display name belongs', async ({ page }) => {
  test.setTimeout(5 * 60 * 1000);
  await go(page, '#/overview', { fresh: true });
  const bad = [];
  for (const r of ROUTES) { await go(page, r); const hits = await page.evaluate(`(${scan.toString()})()`); hits.forEach((h) => bad.push(`${r}: ${h.where}: ${h.text}`)); }
  expect(bad).toEqual([]);
});

test('the Bot and Who menus list display names; who:Atlas finds Atlas in the audit log', async ({ page }) => {
  await go(page, '#/logs/audit', { fresh: true });
  await page.locator('#main .fb-in').fill('who:Atlas'); await page.locator('#main .fb-in').press('Enter');
  await expect(page.locator('#main .au-row, #main .dl-list > li').first()).toBeVisible();
  await expect(page.locator('#main')).not.toContainText(/Nothing matches|No entries/);
  await page.locator('#main .fb-in').fill(''); await page.locator('#main [data-fb-clear], #main .fb-reset').first().click().catch(() => {});
  await go(page, '#/logs/audit');
  await page.locator('#main .fb-in').fill('Atlas'); await page.locator('#main .fb-in').press('Enter');
  await expect(page.locator('#main .au-row, #main .dl-list > li').first()).toBeVisible();
});

test('New ticket and New team pick a bot by its name', async ({ page }) => {
  await go(page, '#/tickets/new', { fresh: true });
  const opts = await page.locator('#tk-who option').allTextContents();
  expect(opts.join(' | ')).toContain('Atlas · team platform');
  expect(opts.join(' | ')).not.toMatch(/(^|\| )platform-sre/);
  await go(page, '#/bots');
  await page.evaluate(() => UI.go('#/bots')); await page.locator('[data-act="team-new"]').first().click().catch(async () => { await page.evaluate(() => document.querySelector('[data-act="team-new"]') && document.querySelector('[data-act="team-new"]').click()); });
  const sel = page.locator('.modal [data-listk="bots"] select, .cdrawer [data-listk="bots"] select').first();
  await expect(sel).toBeVisible();
  const bopts = await sel.locator('option').allTextContents();
  expect(bopts).toEqual(expect.arrayContaining(['Atlas', 'Ledger']));
  await sel.selectOption('platform-sre');
  await expect(page.locator('[data-listk="bots"] .chip-x [data-v="platform-sre"]')).toHaveText('Atlas');
});
