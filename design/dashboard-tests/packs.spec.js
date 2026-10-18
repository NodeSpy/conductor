// Packs: Browse → Add opens the install wizard (roles bound, triggers armed with repos as consent, the grants
// shown loudly, the lockfile change), arming a github trigger without repos is refused like the loader does,
// and trust badges are right (a local pack is yours). (Riley M4, M12)
const { test, expect, go } = require('./fixtures');

test('Browse → Add → the install wizard: roles, arming with repos, loud grants, lockfile hunk, passkey', async ({ page }) => {
  await go(page, '#/extensions/browse', { fresh: true });
  await page.locator('[data-cat="pack:pr-autopilot"] a', { hasText: 'Add' }).click();
  await expect(page).toHaveURL(/#\/extensions\/install\/pack\/pr-autopilot$/);
  // one github connection: bound automatically, nothing written for it (Packs.md); the grants as the bound connection's verbs
  await expect(page.locator('#iw-form')).toContainText('Bound automatically to gh');
  await expect(page.locator('.grants-loud')).toContainText('gh.merge');
  await expect(page.locator('.grants-loud')).toContainText('With nothing armed, installing widens nothing');
  await page.locator('#iw-arm-on_check_failed').check();
  await expect(page.locator('.grants-loud')).toContainText('widening, so applying needs your passkey');
  await page.locator('[data-act="iw-review"]').click();
  await expect(page.locator('#iw-err')).toContainText('needs the repos');
  await page.fill('#iw-repos-on_check_failed', 'acme/api');
  await page.locator('[data-act="iw-review"]').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  await expect(page.locator('.diff').first()).toContainText('pr-autopilot');
  await expect(page.locator('.lock-diff')).toContainText('source: conductor-packs/pr-autopilot');
  await expect(page.locator('.lock-diff')).toContainText('version: 1.3.0');
  await expect(page.locator('#chk-state')).toContainText('passed');
  await expect(page.locator('#pk-why')).toContainText('Needs your passkey');
});

test('where each pack comes from, on its row: the official catalog, your own, or a third party (named)', async ({ page }) => {
  await go(page, '#/extensions/packs', { fresh: true });
  const row = (n) => page.locator(`.pack-row`, { hasText: n }).first();
  await expect(row('review')).toContainText('official');
  await expect(row('house-style')).toContainText('your own');
  await expect(row('kit')).toContainText('third party · acme/conductor-packs/kit');
  await expect(page.locator('.pack-row[data-tip]')).toHaveCount(0); // nothing repeats the row in a tooltip
});
