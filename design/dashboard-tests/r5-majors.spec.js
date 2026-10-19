// Round 5 majors 11–15: the reviewers' repros.
const { test, expect, go, applyStaged } = require('./fixtures');

// r5-11: "Asks before anything else" with Slack chosen showed "Does on its own: Post a message". The channel it tells
// you in is the one thing it does on its own besides reading, and both the preset and the list say so.
test('New bot, Asks before anything else, telling you in Slack: the preset and the list agree', async ({ page }) => {
  await go(page, '#/bots/new', { fresh: true });
  await page.locator('[data-act="bw-purpose"][data-p="incidents"]').click();
  await page.locator('[data-act="bw-next"]').click(); await page.locator('[data-act="bw-next"]').click();
  await page.locator('[data-act="bw-tell"][data-v="slack"]').click(); await page.fill('#bw-channel', '#incidents');
  await page.locator('[data-act="bw-next"]').click();
  await page.locator('[data-act="bw-preset"][data-v="ask"]').click();
  await expect(page.locator('[data-act="bw-preset"][data-v="ask"]')).toContainText('tells you where you chose, on its own');
  const own = page.locator('.cang[data-s="ok"]');
  await expect(own).toContainText('Tell you in #incidents');
  await expect(own).not.toContainText('Post a message');
  await expect(page.locator('.cang[data-s="ember"]')).toContainText(/Add a note|Comment|note/i);
  await expect(page.locator('.bw-body')).not.toContainText(/shadow|observe\b/);
});
