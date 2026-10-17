// ⌘K finds everything (actions, approvals, bots, runs, verbs, config paths, terms), runs actions for real,
// never lists a thing twice, and says so when nothing matches. Single keys: ?, /, g-chords, j/k/Enter.
// (Sam M11, Riley M10)
const { test, expect, go, isPhone } = require('./fixtures');

const open = async (page) => { await page.keyboard.press(process.platform === 'darwin' ? 'Meta+k' : 'Control+k'); await expect(page.locator('.palette input')).toBeFocused(); };
const type = async (page, q) => { await page.fill('.palette input', q); };

test.beforeEach(async ({ page }) => { test.skip(isPhone(page), 'keyboard shortcuts are a desktop feature'); });

test('finds actions, approvals, verbs, config paths and terms; no duplicates; an empty result says what to try', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await open(page);
  await type(page, 'terraform');
  await expect(page.locator('.palette .res')).toContainText('Approve or deny p12');
  await type(page, 'tf-apply.apply');
  await expect(page.locator('.palette .res a', { hasText: 'tf-apply.apply' }).first()).toContainText('verb');
  await type(page, 'policy.budget');
  await expect(page.locator('.palette .res')).toContainText('triggers[ci-fixer].policy.budget');
  await type(page, 'platform-sre');
  const labels = await page.locator('.palette .res a .grow').allTextContents();
  expect(labels.filter((l) => l === 'platform-sre')).toHaveLength(1);
  await type(page, 'provenance');
  await expect(page.locator('.palette .res')).toContainText('Provenance: what it means');
  await type(page, 'zzqqx');
  await expect(page.locator('.pal-empty')).toContainText('No results for “zzqqx”');
  await page.locator('.pal-empty [data-q="new bot"]').click();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/bots\/new$/);
});

test('actions execute: Pause dispatch pauses (after a confirm)', async ({ page }) => {
  await go(page, '#/runs', { fresh: true });
  await open(page);
  await type(page, 'pause dispatch');
  await page.keyboard.press('Enter');
  await expect(page.locator('#cf-cancel')).toBeFocused();
  await page.locator('#cf-ok').click();
  expect(await page.evaluate(() => D.daemon.paused)).toBe(true);
  await expect(page.locator('#strip-cells')).toContainText('paused');
});

test('? shows the shortcuts; g r goes to Activity; / focuses the filter; j/k pick a row and Enter opens it', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await page.keyboard.press('?');
  await expect(page.locator('.modal')).toContainText('Keyboard shortcuts');
  await page.keyboard.press('Escape');
  await page.keyboard.press('g'); await page.keyboard.press('r');
  await expect(page).toHaveURL(/#\/activity$/);
  await page.locator('#main h1').click();
  await page.keyboard.press('/');
  await expect(page.locator('#main .dl-filters input').first()).toBeFocused();
  await page.locator('#main h1').click();
  await page.keyboard.press('j'); await page.keyboard.press('j');
  await expect(page.locator('#main .kbd-sel')).toHaveCount(1);
  const href = await page.locator('#main .kbd-sel').getAttribute('data-href');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(new RegExp(href.replace(/[#/]/g, (c) => '\\' + c) + '$'));
});
