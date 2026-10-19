// Approvals show the whole signed action and say why and what happens, in
// words; non-operator requesters are flagged; the seal modal repeats it; a
// denial can carry a reason; the authenticator used is recorded; on the
// phone, Approve/Deny stay in thumb reach. (Sam M1/M2/P1/P3/m8/m9, Riley B4/M14)
const { test, expect, go, isPhone } = require('./fixtures');

test('a chat-requested Slack post: full message, why, impact, untrusted-requester banner', async ({ page }) => {
  await go(page, '#/bots/billing-sre/proposals/p4', { fresh: true });
  await expect(page.locator('.dl-ptitle')).toHaveText('Post the monthly forecast to #incidents');
  await expect(page.locator('.dl-origin')).toContainText('isn’t one of your operators');
  await expect(page.locator('.dl-pwhy').nth(1)).toContainText('Why'); // after exactly what it sends
  await expect(page.locator('.dl-proposal')).toContainText('unusual for a forecast');
  const text = await page.evaluate(() => D.proposals.find((p) => p.id === 'p4').options.text);
  await expect(page.locator('.payload .pl-long')).toHaveText(text); // untruncated
  await expect(page.locator('details.dl-audit-d')).not.toHaveAttribute('open', '');
});

test('the seal modal says what you approve, shows the payload, and records the authenticator', async ({ page }) => {
  await go(page, '#/bots/billing-sre/proposals/p4', { fresh: true });
  await page.locator('[data-act="p-approve"][data-d="approve"]').first().click();
  await expect(page.locator('.seal-words')).toContainText("You're approving Post the monthly forecast to #incidents");
  await expect(page.locator('.modal .pl-long')).toContainText('October forecast');
  await page.locator('#seal-go').click();
  await expect(page.locator('.prop-decided')).toContainText('Approved');
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
  await expect(page.locator('.prop-decided')).toContainText('post it to #finance');
  expect(await page.evaluate(() => D.chats['billing-sre'].slice(-1)[0].text)).toContain('post it to #finance');
});

test('terraform: one column in the order the decision needs, the plan and the audit data folded, never a tab (from review)', async ({ page }) => {
  await go(page, '#/bots/platform-sre/proposals/p12', { fresh: true });
  await expect(page.locator('.dl-ptitle')).toHaveText('Replace the production web server');
  await expect(page.locator('.prop-tabs')).toHaveCount(0);
  // the order: what will change, why, where/effect/undo, who asked
  const order = await page.locator('.prop-body > *').evaluateAll((xs) => xs.map((x) => ['prop-what', 'dl-pwhy', 'dl-where', 'dl-origin', 'prop-plan-d', 'dl-audit-d'].find((c) => x.classList.contains(c))).filter(Boolean));
  expect(order.slice(0, 4)).toEqual(['prop-what', 'dl-pwhy', 'dl-where', 'dl-origin']);
  await expect(page.locator('.prop-what')).toContainText('Replaces');
  await expect(page.locator('.prop-what .plan-jump')).toContainText('aws_instance.web');
  // the integrity data is folded and quiet; opening it hides nothing
  await expect(page.locator('.dl-audit-d')).not.toHaveAttribute('open', '');
  await page.locator('.dl-audit-d summary').click();
  await expect(page.locator('.dl-audit-d')).toContainText('Plan sha256');
  expect(await page.locator('.dl-audit-d .dl-facts').evaluate((el) => parseFloat(getComputedStyle(el).fontSize))).toBeLessThan(14);
  // a resource in the summary opens the full plan at its block, colourised in Terraform's conventions
  await page.locator('.plan-jump a', { hasText: 'aws_instance.web' }).click();
  await expect(page.locator('.prop-plan-d')).toHaveAttribute('open', '');
  await expect(page.locator('.prop-plan .pl-replace', { hasText: /[-+]\/[-+]/ }).first()).toBeVisible();
  await expect(page.locator('.prop-plan .pl-kaa').first()).toHaveText('(known after apply)');
  for (const s of ['.prop-what', '.dl-pwhy', '.dl-where', '.dl-origin']) await expect(page.locator(s).first()).toBeVisible();
  const bar = page.locator('.dl-proposal .prop-decide');
  expect(await bar.evaluate((el) => getComputedStyle(el).position)).toBe('sticky');
  await expect(bar.locator('[data-d="approve"]')).toBeVisible();
});

test('a simple action: exactly what it sends comes first; the audit data folded; inline ids flow in the sentence', async ({ page }) => {
  await go(page, '#/bots/billing-sre/proposals/p4', { fresh: true });
  await expect(page.locator('.prop-tabs')).toHaveCount(0);
  await expect(page.locator('.prop-body > .dl-pwhy').first()).toContainText('Exactly what it sends');
  await expect(page.locator('details.dl-audit-d')).not.toHaveAttribute('open', '');
  await expect(page.locator('.dl-proposal .prop-decide [data-d="approve"]')).toBeVisible();
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
