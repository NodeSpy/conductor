// YAML is an advanced feature (ledger item 24): a form takes the full width; its YAML is one click away (Edit as YAML,
// in the action bar and the bot's ⋯) in a sheet that validates and puts the edit back in the form; the action bar
// sticks while there are unsaved changes; "Show advanced" (off by default) puts the YAML within reach.
const { test, expect, go } = require('./fixtures');

test('bot Settings: the form at full width, no YAML column; Edit as YAML round-trips into the form', async ({ page }) => {
  await go(page, '#/bots/platform-sre/settings', { fresh: true });
  await expect(page.locator('.def-yaml, #df-yaml')).toHaveCount(0);
  const w = await page.evaluate(() => [document.querySelector('.def-form').getBoundingClientRect().width, document.querySelector('.dl-tab-body').getBoundingClientRect().width]);
  expect(w[0]).toBeGreaterThan(w[1] - 4);
  await expect(page.locator('.def-bar')).toContainText('No changes');
  await page.locator('[data-act="df-yaml"]').click();
  const ta = page.locator('#df-yaml-ed .ced-ta');
  await expect(ta).toHaveValue(/bots:\s*\n\s*platform-sre:/);
  // a broken edit is caught in the sheet
  await ta.fill('bots: [');
  await ta.dispatchEvent('input');
  await expect(page.locator('#df-yaml-err')).toContainText('Not valid YAML');
  await ta.fill('bots:\n  platform-sre:\n    description: Keeps the platform up\n    team: platform\n');
  await ta.dispatchEvent('input');
  await page.locator('[data-act="df-yaml-apply"]').click();
  await expect(page.locator('.modal')).toHaveCount(0);
  await expect(page.locator('[id="f-df_description"]')).toHaveValue('Keeps the platform up');
  await expect(page.locator('.def-bar')).toHaveClass(/dirty/);
  await expect(page.locator('.def-bar')).toContainText('Unsaved changes');
});

test('Edit as YAML is in the bot’s ⋯ too; Show advanced puts the YAML within reach on the form', async ({ page }) => {
  await go(page, '#/bots/platform-sre', { fresh: true });
  await page.locator('[data-act="bot-more"]').click();
  await page.locator('#pop-menu [data-act="bot-yaml"]').click();
  await expect(page.locator('#df-yaml-ed')).toBeVisible();
  await go(page, '#/preferences');
  await page.locator('[data-act="advanced-toggle"]').click();
  await go(page, '#/bots/platform-sre/settings');
  await expect(page.locator('.def-yaml-d')).toBeVisible();
  await expect(page.locator('.def-bar .def-yaml-btn')).toHaveClass(/btn/);
});
