// The New bot wizard: leaving is obvious and safe. A visible Close button at the top, Esc closes, and a confirm only
// once something has been entered.
const { test, expect, go } = require('./fixtures');

test('New bot: Close and Esc leave at once when nothing was entered, and ask first when something was', async ({ page }) => {
  await go(page, '#/bots/new', { fresh: true });
  const close = page.locator('.bw-close');
  await expect(close).toBeVisible();
  await expect(close).toHaveText(/Close/);
  await close.click();
  await expect(page).toHaveURL(/#\/bots$/);
  await go(page, '#/bots/new');
  await page.keyboard.press('Escape');
  await expect(page).toHaveURL(/#\/bots$/);
  // something entered: Esc asks first; Keep editing stays, Discard leaves
  await go(page, '#/bots/new');
  await page.locator('[data-act="bw-purpose"]').first().click();
  await page.keyboard.press('Escape');
  await expect(page.locator('.modal')).toContainText('Discard this new bot?');
  await page.locator('#cf-cancel').click();
  await expect(page).toHaveURL(/#\/bots\/new$/);
  await close.click();
  await page.locator('#cf-ok').click();
  await expect(page).toHaveURL(/#\/bots$/);
});
