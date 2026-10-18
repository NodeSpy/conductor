// Every modal, drawer and sheet has a visible way out and closes on Esc; full-page flows leave with the
// browser's Back; destructive confirmations put focus on Cancel. (Visual QA bar)
const { test, expect, go, isPhone, headAction } = require('./fixtures');

const OPENERS = [
  ['#/workflows', '[data-act="wf-new"]', 'new workflow'],
  ['#/bots', '[data-act="team-new"]', 'new team'],
  ['#/secrets', '[data-act="secret-add"]', 'add a secret'],
  ['#/secrets/settings', '#env [data-act="env-add"]', 'add an env var'],
  ['#/connectors/connectors', '[data-act="conn-add"]', 'add a connector'],
  ['#/runs/r-7ef0', '[data-act="retry"]', 'retry a run'],
  ['#/bots/platform-sre/proposals/p12', '[data-act="p-approve"][data-d="approve"]', 'approve (seal)'],
  ['#/teams/platform/routing', '[data-act="rule-edit"]', 'routing rule'],
  ['#/config/collections', '[data-act="coll-new"]', 'new collection'],
  ['#/bots/release-scribe', '[data-act="bot-watch-add"]', 'add what it watches'],
  ['#/extensions/packs', '[data-act="pack-plan"][data-update="1"]', 'pack update plan'],
];
const open = () => page => page.locator('.scrim, .drawer-scrim, .cdrawer, .modal').count();

for (const [route, sel, name] of OPENERS) {
  test(`${name}: a visible way out, and Esc closes it`, async ({ page }) => {
    await go(page, route, { fresh: true });
    await headAction(page, sel);
    const box = page.locator('.modal, .cdrawer').last();
    await expect(box).toBeVisible();
    await expect(box.locator('[data-close], [data-act="cf-cancel"], #cf-cancel, .btn.ghost').first()).toBeVisible();
    await page.keyboard.press('Escape');
    await expect.poll(() => open()(page)).toBe(0);
  });
}

test('⌘K closes on Esc', async ({ page }) => {
  test.skip(isPhone(page), 'keyboard');
  await go(page, '#/overview', { fresh: true });
  await page.keyboard.press('Control+k');
  await expect(page.locator('.palette')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('.palette')).toHaveCount(0);
});

for (const [from, to, how] of [['#/bots', '#/bots/new', 'New bot wizard'], ['#/browser/logins', '#/browser/add', 'Add a login'], ['#/extensions/browse', '#/extensions/install/pack/ci-unsticker', 'pack install wizard'], ['#/security', '#/enroll', 'enrol a passkey']]) {
  test(`${how}: the browser's Back returns to where you were`, async ({ page }) => {
    await go(page, from, { fresh: true });
    await page.evaluate((h) => { location.hash = h; }, to);
    await expect(page).toHaveURL(new RegExp(to.replace(/[/#]/g, '\\$&') + '$'));
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(from.replace(/[/#]/g, '\\$&') + '$'));
    await expect(page.locator('#main > *').first()).toBeVisible();
  });
}

test('destructive confirmations focus Cancel', async ({ page }) => {
  await go(page, '#/secrets/settings', { fresh: true });
  await page.locator('[data-act="env-more"]').first().click();
  await page.locator('#pop-menu [data-act="env-del"]').click();
  await expect(page.locator('#cf-cancel')).toBeFocused();
  await page.keyboard.press('Escape');
  await go(page, '#/config/collections');
  await go(page, '#/workflows');
  await page.locator('[data-act="wl-more"][data-name="ci-fixer"]').click();
  await page.locator('#pop-menu [data-act="wf-del"]').click();
  await expect(page.locator('#cf-cancel')).toBeFocused();
});
