// Approvals show the whole signed action and say why and what happens, in
// words; non-operator requesters are flagged; the seal modal repeats it; a
// denial can carry a reason; the authenticator used is recorded; on the
// phone, Approve/Deny stay in thumb reach. (Sam M1/M2/P1/P3/m8/m9, Riley B4/M14)
const { test, expect, go, isPhone } = require('./fixtures');

test('a chat-requested Slack post: full message, why, impact, untrusted-requester banner', async ({ page }) => {
  await go(page, '#/bots/billing-sre/proposals/p4', { fresh: true });
  await expect(page.locator('.prop-title')).toHaveText('Post the monthly forecast to #incidents');
  await expect(page.locator('.req-banner.untrusted')).toContainText("isn't one of your operators");
  await expect(page.locator('.why-impact')).toContainText('Why');
  await expect(page.locator('.why-impact')).toContainText('unusual for a forecast');
  const text = await page.evaluate(() => D.proposals.find((p) => p.id === 'p4').options.text);
  await expect(page.locator('.payload .pl-long')).toHaveText(text); // untruncated
  await expect(page.locator('details.tech')).not.toHaveAttribute('open', '');
});

test('the seal modal says what you approve, shows the payload, and records the authenticator', async ({ page }) => {
  await go(page, '#/bots/billing-sre/proposals/p4', { fresh: true });
  await page.locator('[data-act="p-approve"][data-d="approve"]').first().click();
  await expect(page.locator('.seal-words')).toContainText("You're approving Post the monthly forecast to #incidents");
  await expect(page.locator('.modal .pl-long')).toContainText('October forecast');
  await page.locator('#seal-go').click();
  await expect(page.locator('.callout.ok')).toContainText('Approved');
  const who = await page.evaluate(() => D.proposals.find((p) => p.id === 'p4').approver);
  expect(who).toContain(isPhone(page) ? 'phone' : 'laptop');
  // the page never fades to blank after the decision
  expect(await page.evaluate(() => getComputedStyle(document.querySelector('.page > *')).opacity)).toBe('1');
});

test('deny with a reason: the reason reaches the bot', async ({ page }) => {
  await go(page, '#/bots/billing-sre/proposals/p4', { fresh: true });
  await page.locator('[data-act="p-approve"][data-d="deny"]').first().click();
  await page.fill('#seal-reason', 'Not in #incidents; post it to #finance.');
  await page.locator('#seal-go').click();
  await expect(page.locator('.callout.danger')).toContainText('post it to #finance');
  expect(await page.evaluate(() => D.chats['billing-sre'].slice(-1)[0].text)).toContain('post it to #finance');
});

test('terraform: plain-language summary first, hashes under a disclosure; event requester explained', async ({ page }) => {
  await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
  await expect(page.locator('.prop-title')).toHaveText('Replace the production web server');
  await expect(page.locator('.req-banner.event')).toContainText('Started by an event');
  await expect(page.locator('.tf-tiles')).toContainText('replaces');
  await expect(page.locator('details.tech').last()).toContainText('plan sha256');
  await expect(page.locator('details.tech').last().locator('.hash').first()).toBeHidden();
});

test('phone: opened from Approvals, Approve is in thumb reach and the tab stays highlighted', async ({ page }) => {
  test.skip(!isPhone(page), 'phone only');
  await go(page, '#/attention', { fresh: true });
  await page.locator('a[href="#/bots/platform-sre/proposals/p12"]').first().click();
  await expect(page.locator('.prop-title')).toBeVisible();
  const box = await page.locator('.sticky-decide [data-d="approve"]').boundingBox();
  expect(box.y + box.height).toBeLessThanOrEqual(844);
  await expect(page.locator('#tabbar [data-nav="attention"]')).toHaveClass(/on/);
  await expect(page.locator('a.back')).toHaveAttribute('href', '#/attention');
});
