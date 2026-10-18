// Wizard headers never overlap, wrap or clip (from the iPhone review: the New bot stepper ran its five labels into each other).
// Every wizard is walked through its states; at each one the stepper either shows its labelled steps with room to
// spare, or the compact "Step 2 of 5 · Watches" with a thin bar. Chromium at 1440 and 390 here; WebKit in iphone.spec.
const { test, expect } = require('./fixtures');
const { walkWizards } = require('./wizards');

test('every wizard header fits in every state', async ({ page }) => {
  test.setTimeout(3 * 60 * 1000);
  expect(await walkWizards(page)).toEqual([]);
});

test('New bot is a sheet over the Bots page (full screen on a phone) with a quiet progress line, not a stepper', async ({ page }) => {
  const { go } = require('./fixtures');
  await go(page, '#/bots/new', { fresh: true });
  await expect(page.locator('.nb-sheet[role="dialog"]')).toBeVisible();
  await expect(page.locator('.nb-under .bot-list')).toBeAttached(); // the Bots page is underneath
  await expect(page.locator('.nb-head .dl-sub')).toHaveText('Step 1 of 5 · Purpose');
  await expect(page.locator('.nb-prog[role="progressbar"]')).toHaveAttribute('aria-valuenow', '1');
  await expect(page.locator('#main .stepper')).toHaveCount(0);
  const r = await page.locator('.nb-sheet').boundingBox(), w = page.viewportSize().width;
  if (w <= 720) expect(Math.round(r.width)).toBe(w); else expect(r.width).toBeLessThan(w - 200);
});
