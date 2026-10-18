// Round-2 validation (newcomer N8, N11, N12, N17): a Slack channel must be a channel, an instance name can't be taken
// twice, watching nothing is allowed but never by accident, and a denial's reason has a length limit.
const { test, expect, go } = require('./fixtures');

test('New bot: a bad Slack channel blocks Next with the reason; watching nothing asks first', async ({ page }) => {
  await go(page, '#/bots/new', { fresh: true });
  await page.locator('[data-act="bw-purpose"][data-p="incidents"]').click();
  await page.locator('[data-act="bw-next"]').click();
  // nothing to watch: allowed, never by accident
  const checked = page.locator('[data-on-change="bw-ev"]:checked'); // each untick re-renders the step: ask again each time
  for (let i = 0; i < 10 && await checked.count(); i++) { await checked.first().click(); await page.waitForTimeout(80); }
  await page.locator('[data-act="bw-next"]').click();
  await expect(page.locator('.modal')).toContainText('Continue without anything to watch?');
  await page.locator('#cf-ok').click();
  await page.locator('[data-act="bw-tell"][data-v="slack"]').click();
  await page.fill('#bw-channel', 'Incidents Channel');
  const next = page.locator('.wizard-foot .btn.primary');
  await expect(next).toHaveAttribute('aria-disabled', 'true');
  await expect(next).toHaveAttribute('data-reason', /channel/i);
  await page.fill('#bw-channel', '#incidents');
  await page.locator('#bw-channel').blur();
  await expect(page.locator('[data-act="bw-next"]')).toHaveCount(1);
});

test('a connection’s instance name can’t be one that is already taken', async ({ page }) => {
  await go(page, '#/connectors', { fresh: true });
  await page.locator('[data-act="conn-add"]').click();
  await page.fill('.modal #cat-q', 'github');
  await page.locator('.modal [data-act="cat-pick"][data-name="github"]').click();
  await page.fill('#f-name', 'gh');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.modal')).toContainText(/taken|exists|already/i);
});

test('a denial’s reason is limited in length', async ({ page }) => {
  await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
  await page.locator('.prop-decide [data-act]', { hasText: /^Deny/ }).first().click();
  await expect(page.locator('#seal-reason')).toHaveAttribute('maxlength', '500');
});
