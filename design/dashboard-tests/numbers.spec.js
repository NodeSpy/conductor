// Copy never contradicts the data: counts on the overview, the rail, tabs and tiles match the state they
// describe. (Visual QA bar: the copy check)
const { test, expect, go } = require('./fixtures');

test('overview, rail and tab counts agree with the data', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const d = await page.evaluate(() => ({ need: D.attention.length, bots: D.bots.length, live: D.bots.filter((b) => b.home === 'live').length, pending: D.proposals.filter((p) => p.status === 'pending' && UI.live(p)).length, running: D.runs.filter((r) => r.status === 'run').length, logins: D.access.credentials.length + D.access.logins.length }));
  await expect(page.locator('.nav a[data-nav="attention"] .count')).toHaveText(String(d.need));
  await expect(page.locator('.dl-needs-card .dl-ch .count')).toHaveText(String(d.need));
  const tile = (l) => page.locator('.dl-tile', { has: page.locator(`.dl-lbl:text-is("${l}")`) });
  await expect(tile('Bots').locator('b')).toHaveText(String(d.bots));
  await expect(tile('Bots').locator('.d')).toContainText(`${d.live} working`);
  await expect(tile('Approvals').locator('b')).toHaveText(String(d.pending));
  await expect(tile('Running').locator('b')).toHaveText(String(d.running));
  await go(page, '#/browser/logins');
  await expect(page.locator('.tabs a[href="#/browser/logins"] .count')).toHaveText(String(d.logins));
  await go(page, '#/browser');
  await expect(page.locator('.tabs a[href="#/browser/logins"] .count')).toHaveText(String(d.logins));
});
