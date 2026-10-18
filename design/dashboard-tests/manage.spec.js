// Bots and Workflows are management pages (the chosen direction 1): teams (sources) are the page. No needs-you or
// working-now sections; a row's status is one quiet mark that links to Needs you or Activity. Changes stage into the
// one diff. Folders don't exist in conductor, so none appear anywhere in the dashboard.
const { test, expect, go, isPhone } = require('./fixtures');
const { ROUTES } = require('./routes');

test('Bots: each team is a section that says its rules and builds the team; solo bots last; Group by None is one list', async ({ page }) => {
  await go(page, '#/bots', { fresh: true });
  const plat = page.locator('.m-team[data-team="platform"]');
  await expect(plat.locator('h2')).toHaveText('platform');
  await expect(plat.locator('.m-team-h')).toContainText('led by platform-sre');
  await expect(plat.locator('.m-team-h .fx-i[title="The most its bots may do"]')).toBeVisible();
  await expect(plat.locator('.m-team-h .fx-i[title="Who approves"]')).toContainText('approved by');
  await expect(plat.locator('[data-act="bv-team-add"]')).toBeVisible();
  await expect(plat.locator('a', { hasText: 'Shape the team' })).toHaveAttribute('href', '#/teams/platform/defaults');
  await expect(plat.locator('.m-row[data-bot="platform-sre"] .fx-i[title="Leads its team"]')).toBeVisible();
  await expect(page.locator('.m-team[data-team="solo"]')).toContainText('not on a team');
  // no needs-you / working-now sections: status is a quiet mark that links there
  await expect(page.locator('#main h2', { hasText: /need you|working now/i })).toHaveCount(0);
  await expect(plat.locator('.m-row[data-bot="platform-sre"] .q-st')).toHaveAttribute('href', '#/attention');
  await page.locator('[data-act="bv-group"][data-v="none"]').click();
  await expect(page.locator('.m-flat .m-row')).toHaveCount(await page.evaluate(() => S.view().bots.length));
});

test('Bots: add a bot to a team, clone one, archive one, each staged into the diff', async ({ page }) => {
  await go(page, '#/bots', { fresh: true });
  await page.locator('.m-team[data-team="finance"] [data-act="bv-team-add"]').click();
  await page.locator('#f-bot').selectOption('release-scribe');
  await page.locator('#gen-submit').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  await expect(page.locator('.diff').first()).toContainText('team: finance');
  await go(page, '#/bots');
  await page.locator('[data-act="bv-more"][data-m="homelab"]').click();
  await page.locator('#pop-menu [data-act="bv-clone"]').click();
  await page.fill('#f-name', 'homelab-2');
  await page.locator('#gen-submit').click();
  expect(await page.evaluate(() => S.getPath(D.pending.find((c) => c.key === 'homelab-2').after, 'autonomy.shadow'))).toBe(true);
  await go(page, '#/bots');
  await page.locator('[data-act="bv-more"][data-m="homelab"]').click();
  await page.locator('#pop-menu [data-act="bv-archive"]').click();
  await page.locator('#cf-ok').click();
  expect(await page.evaluate(() => D.pending.find((c) => c.key === 'homelab').after.disabled)).toBe(true);
});

test('Bots: select two, move both to a team in one diff (desktop)', async ({ page }) => {
  test.skip(isPhone(page), 'bulk selection is desktop-only');
  await go(page, '#/bots', { fresh: true });
  await page.locator('input[data-act="bv-sel"][data-m="homelab"]').check();
  await page.locator('input[data-act="bv-sel"][data-m="release-scribe"]').check();
  await expect(page.locator('.m-bulk')).toContainText('2 selected');
  await page.locator('[data-act="bv-bulk-team"]').click();
  await page.locator('#f-team').selectOption('platform');
  await page.locator('#gen-submit').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  expect(await page.evaluate(() => D.pending.filter((c) => c.coll === 'bots').map((c) => c.key).sort())).toEqual(['homelab', 'release-scribe']);
});

test('Workflows: yours, each pack with its update and Customize, made-for-bots as one line; switch one off', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  await expect(page.locator('.m-team[data-src="config"] h2')).toHaveText('Yours');
  const review = page.locator('.m-team[data-src="pack:review"]');
  await expect(review.locator('h2')).toHaveText('From review');
  await expect(review.locator('[data-act="pack-plan"]')).toContainText('Update to 1.5.0');
  await expect(review.locator('a', { hasText: 'Customize' })).toBeVisible();
  await expect(page.locator('.m-team[data-src="bot"]')).toContainText('platform-sre');
  await expect(page.locator('.m-row[data-wf="dep-bump"] .fx-i[title="What starts it"]')).toHaveText('Mondays at 06:00');
  await page.locator('.m-row[data-wf="ci-fixer"] [data-act="wl-toggle"]').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  expect(await page.evaluate(() => D.pending.find((c) => c.key === 'ci-fixer').after)).toContain('enabled: false');
});

test('no folders anywhere: no folder menu, button, crumb or word on any page; old folder links land on Bots', async ({ page }) => {
  test.setTimeout(6 * 60 * 1000);
  const bad = [];
  for (const r of ROUTES) {
    await go(page, r, { fresh: true });
    const t = await page.evaluate(() => { const m = document.querySelector('#main'); const c = m.cloneNode(true); c.querySelectorAll('textarea, pre, .yaml, .cm, .code').forEach((x) => x.remove()); return c.textContent; });
    if (/\bfolders?\b/i.test(t)) bad.push(`${r}: "${t.match(/.{0,40}\bfolders?\b.{0,30}/i)[0].trim()}"`);
    if (await page.locator('[data-fb-open="folder"], [data-act="f-new"], [data-act^="f-"]').count()) bad.push(`${r}: a folder control`);
  }
  expect(bad).toEqual([]);
  await go(page, '#/folders/work');
  await expect(page).toHaveURL(/#\/bots$/);
});
