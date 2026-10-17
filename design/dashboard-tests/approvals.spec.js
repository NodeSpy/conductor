// Approvals show the whole signed action and say why and what happens, in
// words; non-operator requesters are flagged; the seal modal repeats it; a
// denial can carry a reason; the authenticator used is recorded; on the
// phone, Approve/Deny stay in thumb reach. (Sam M1/M2/P1/P3/m8/m9, Riley B4/M14)
const { test, expect, go, isPhone } = require('./fixtures');

test('a chat-requested Slack post: full message, why, impact, untrusted-requester banner', async ({ page }) => {
  await go(page, '#/bots/billing-sre/proposals/p4', { fresh: true });
  await expect(page.locator('.dl-ptitle')).toHaveText('Post the monthly forecast to #incidents');
  await expect(page.locator('.dl-origin')).toContainText('isn’t one of your operators');
  await expect(page.locator('.dl-pwhy').first()).toContainText('Why');
  await expect(page.locator('.dl-proposal')).toContainText('unusual for a forecast');
  const text = await page.evaluate(() => D.proposals.find((p) => p.id === 'p4').options.text);
  await expect(page.locator('.payload .pl-long')).toHaveText(text); // untruncated
  await expect(page.locator('details.dl-tech-d')).not.toHaveAttribute('open', '');
});

test('the seal modal says what you approve, shows the payload, and records the authenticator', async ({ page }) => {
  await go(page, '#/bots/billing-sre/proposals/p4', { fresh: true });
  await page.locator('[data-act="p-approve"][data-d="approve"]').first().click();
  await expect(page.locator('.seal-words')).toContainText("You're approving Post the monthly forecast to #incidents");
  await expect(page.locator('.modal .pl-long')).toContainText('October forecast');
  await page.locator('#seal-go').click();
  await expect(page.locator('.dl-decided')).toContainText('Approved');
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
  await expect(page.locator('.dl-decided')).toContainText('post it to #finance');
  expect(await page.evaluate(() => D.chats['billing-sre'].slice(-1)[0].text)).toContain('post it to #finance');
});

test('terraform: the plan decides it, so Overview | Technical details; the colorized plan full width; the decision pinned on both tabs', async ({ page }) => {
  await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
  await expect(page.locator('.dl-ptitle')).toHaveText('Replace the production web server');
  await expect(page.locator('.dl-origin')).toContainText('Started by an event');
  await expect(page.locator('.dl-changes')).toContainText('Replaces');
  await expect(page.locator('.prop-tabs [data-v]')).toHaveText(['Overview', 'Technical details']);
  const bar = page.locator('.dl-proposal .prop-decide');
  await expect(bar.locator('[data-d="approve"]')).toBeVisible();
  expect(await bar.evaluate((el) => getComputedStyle(el).position)).toBe('sticky');
  await page.locator('[data-act="prop-tab"][data-v="tech"]').click();
  await expect(page.locator('.prop-tech-facts')).toContainText('Plan sha256');
  await expect(page.locator('.prop-tech-facts')).toContainText('terraform apply');
  // the plan in Terraform's conventions: the replace line strongest, updates amber, symbols kept
  await expect(page.locator('.prop-plan .pl-replace', { hasText: /[-+]\/[-+]/ }).first()).toBeVisible();
  await expect(page.locator('.prop-plan .pl-update').first()).toContainText('~');
  await expect(page.locator('.prop-plan .pl-kaa').first()).toHaveText('(known after apply)');
  await expect(page.locator('.plan-jump')).toContainText('Replaced');
  await expect(page.locator('.dl-cols.prop-wide')).toHaveCount(1); // the details take the full width
  await expect(bar.locator('[data-d="approve"]')).toBeVisible(); // and the decision is still there
});

test('a simple action: what runs is on the Overview; the rest is a collapsed disclosure, no tabs; inline ids flow in the sentence', async ({ page }) => {
  await go(page, '#/bots/billing-sre/proposals/p4', { fresh: true });
  await expect(page.locator('.prop-tabs')).toHaveCount(0);
  await expect(page.locator('.dl-proposal .payload')).toBeVisible();
  await expect(page.locator('details.dl-tech-d')).not.toHaveAttribute('open', '');
  await expect(page.locator('.dl-proposal .prop-decide [data-d="approve"]')).toBeVisible();
  // the requester sentence is one line of text, the inline id inside it
  expect(await page.locator('.dl-origin').evaluate((el) => getComputedStyle(el).display)).toBe('block');
});

test('phone: opened from Approvals, Approve is in thumb reach and the tab stays highlighted', async ({ page }) => {
  test.skip(!isPhone(page), 'phone only');
  await go(page, '#/attention', { fresh: true });
  await page.locator('a[href="#/bots/platform-sre/proposals/p12"]').first().click();
  await expect(page.locator('.dl-ptitle')).toBeVisible();
  const box = await page.locator('.prop-decide [data-d="approve"]').boundingBox();
  expect(box.y + box.height).toBeLessThanOrEqual(844);
  await expect(page.locator('#tabbar [data-nav="attention"]')).toHaveClass(/on/);
  await expect(page.locator('.dl-crumbs a').last()).toBeVisible();
});
