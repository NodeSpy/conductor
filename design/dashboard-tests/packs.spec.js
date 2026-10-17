// Packs: Browse → Add opens the install wizard (roles bound, triggers armed with repos as consent, the grants
// shown loudly, the lockfile change), arming a github trigger without repos is refused like the loader does,
// and trust badges are right (a local pack is yours). (Riley M4, M12)
const { test, expect, go } = require('./fixtures');

test('Browse → Add → the install wizard: roles, arming with repos, loud grants, lockfile hunk, passkey', async ({ page }) => {
  await go(page, '#/extensions/browse', { fresh: true });
  await page.locator('[data-cat="pack:pr-autopilot"] a', { hasText: 'Add' }).click();
  await expect(page).toHaveURL(/#\/extensions\/install\/pack\/pr-autopilot$/);
  await expect(page.locator('#iw-role-github')).toHaveValue('gh');
  await expect(page.locator('.grants-loud')).toContainText('git.push');
  await page.locator('#iw-arm-on_check_failed').check();
  await page.locator('[data-act="iw-review"]').click();
  await expect(page.locator('#iw-err')).toContainText('needs the repos');
  await page.fill('#iw-repos-on_check_failed', 'acme/api');
  await page.locator('[data-act="iw-review"]').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  await expect(page.locator('.diff').first()).toContainText('pr-autopilot');
  await expect(page.locator('.lock-diff')).toContainText('NodeSpy/conductor-packs/pr-autopilot');
  await expect(page.locator('#chk-state')).toContainText('passed');
  await expect(page.locator('#pk-why')).toContainText('Needs your passkey');
});

test('trust badges: official, local (yours), third party', async ({ page }) => {
  await go(page, '#/extensions/packs', { fresh: true });
  const rows = await page.locator('.pack-row').allTextContents();
  expect(rows.some((t) => /official registry/.test(t))).toBe(true);
  expect(rows.some((t) => /local · yours/.test(t))).toBe(true);
  for (const t of rows.filter((x) => /\.\/packs\//.test(x))) expect(t).not.toContain('third party');
});
