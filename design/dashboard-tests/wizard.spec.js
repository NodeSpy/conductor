// Wizard headers never overlap, wrap or clip (from the iPhone review: the New bot stepper ran its five labels into each other).
// Every wizard is walked through its states; at each one the stepper either shows its labelled steps with room to
// spare, or the compact "Step 2 of 5 · Watches" with a thin bar. Chromium at 1440 and 390 here; WebKit in iphone.spec.
const { test, expect } = require('./fixtures');
const { walkWizards } = require('./wizards');

test('every wizard header fits in every state', async ({ page }) => {
  test.setTimeout(3 * 60 * 1000);
  expect(await walkWizards(page)).toEqual([]);
});

test('New bot on a phone: the stepper is compact; on a desktop the five labelled steps show', async ({ page }) => {
  const { go } = require('./fixtures');
  await go(page, '#/bots/new', { fresh: true });
  const compact = await page.locator('.stepper').evaluate((el) => el.classList.contains('compact'));
  if (page.viewportSize().width <= 720) { expect(compact).toBe(true); await expect(page.locator('.step-compact .sc-t')).toHaveText('Step 1 of 5 · Purpose'); }
  else { expect(compact).toBe(false); await expect(page.locator('.stepper .as')).toHaveCount(5); }
});
