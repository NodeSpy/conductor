// The words people read (r5 major 10): practice mode, ticket, approval, connection and the plain risk words on every
// route (shadow, case, proposal, connector, commit_money/observe/reversible as codes, config kickers and "task cNN" only
// in code, config notes and For audit); a staged change and its toast say what changed in words.
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');
const scan = require('./vocab');

test('no route uses the retired words where people read', async ({ page }) => {
  test.setTimeout(5 * 60 * 1000);
  await go(page, '#/overview', { fresh: true });
  const bad = [];
  for (const r of ROUTES) { await go(page, r); (await page.evaluate(`(${scan.toString()})()`)).forEach((h) => bad.push(`${r}: ${h.word} · ${h.where}: ${h.text}`)); }
  expect(bad).toEqual([]);
});

test('a staged permission change says what changed in words, in its toast and in the review', async ({ page }) => {
  await go(page, '#/bots/platform-sre/access', { fresh: true });
  await page.locator('.cang[data-s="ok"] .can-item').first().locator('[data-act="can-more"]').click();
  await page.locator('#pop-menu [data-act]').filter({ hasText: 'Ask me first' }).first().click();
  await expect(page.locator('.toast').last()).toContainText('Staged: Atlas: what it may do');
  await expect(page.locator('.toast').last()).not.toContainText(/bots\.|autonomy\./);
  await page.goto(page.url().replace(/#.*/, '#/config/apply'));
  await expect(page.locator('#main')).toContainText('Atlas: what it may do');
});
