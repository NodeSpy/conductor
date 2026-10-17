// Copy never contradicts the data: counts on the overview, the rail, tabs and tiles match the state they
// describe. (Visual QA bar: the copy check)
const { test, expect, go } = require('./fixtures');

test('overview, rail and tab counts agree with the data', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const d = await page.evaluate(() => ({ need: D.attention.length, bots: D.bots.length, live: D.bots.filter((b) => b.home === 'live').length, pending: D.proposals.filter((p) => p.status === 'pending' && UI.live(p)).length, running: D.runs.filter((r) => r.status === 'run').length, logins: D.access.credentials.length + D.access.logins.length }));
  await expect(page.locator('#strip-cells .cell.alert .count')).toHaveText(String(d.need));
  await expect(page.locator('.panel.attn .panel-head')).toContainText(`${d.need} items`);
  await expect(page.locator('.stat.hot .v')).toContainText(String(d.bots));
  await expect(page.locator('.stat.hot .d')).toContainText(`${d.live} in a live session`);
  await expect(page.locator('.stat', { hasText: 'Approvals' }).locator('.v')).toContainText(String(d.pending));
  await expect(page.locator('.stat', { hasText: 'Running' }).locator('.v')).toHaveText(String(d.running));
  await go(page, '#/browser/logins');
  await expect(page.locator('.tabs a[href="#/browser/logins"] .count')).toHaveText(String(d.logins));
  await go(page, '#/browser');
  await expect(page.locator('.tabs a[href="#/browser/logins"] .count')).toHaveText(String(d.logins));
});
