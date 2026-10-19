// Connect with Slack: the manifest flow creates the app, the returned tokens go to the vault (a signed write),
// and the connector is staged with references; it then says what bots could do with it. The manual form stays
// for people who already have tokens. (Sam M3)
const { test, expect, go } = require('./fixtures');

test('Connect with Slack: manifest, install, tokens into the vault, connector staged with references', async ({ page }) => {
  await go(page, '#/connectors/connectors', { fresh: true });
  await page.locator('[data-act="conn-add"]').first().click();
  await page.locator('[data-act="cat-pick"][data-kind="connector"][data-name="slack"]').click();
  // two ways in, side by side: Connect with Slack (recommended), or fill it in yourself
  await expect(page.locator('.conn-paths .conn-rec')).toContainText('Connect with Slack');
  await expect(page.locator('.vs[data-vs="bot_token"]')).toBeHidden();
  await page.locator('[data-act="conn-manual"]').click();
  await expect(page.locator('.vs[data-vs="bot_token"]')).toBeVisible(); // the manual form, once you choose it
  await page.locator('[data-act="conn-manifest"]').click();
  await page.fill('#cm-inst', 'slack-team');
  await page.locator('.modal details summary').click();
  await expect(page.locator('.modal')).toContainText('socket_mode_enabled: true');
  await page.locator('[data-act="cm-go"]').click();
  await expect(page.locator('.conn-can')).toContainText('slack-team.post');
  await page.locator('[data-act="cm-store"]').click();
  await page.locator('#su-go').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  const n = await page.evaluate(() => D.pending.find((c) => c.key === 'slack-team').after);
  expect(n.bot_token).toBe('{{ vault "house" "slack-team-bot-token" }}');
  expect(await page.evaluate(() => D.secrets.some((s) => s.ref === 'house/slack-team-app-token'))).toBe(true);
});
