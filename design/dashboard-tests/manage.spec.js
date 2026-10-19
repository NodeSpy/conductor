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
  await expect(plat.locator('.m-team-h .fx-i', { hasText: /^\+\d+$/ })).toHaveAttribute('title', /approved by/); // the rest of its rules, on one line as +N
  await expect(plat.locator('[data-act="bv-team-add"]')).toBeVisible();
  await expect(plat.locator('a', { hasText: 'Shape the team' })).toHaveAttribute('href', '#/teams/platform/defaults');
  await expect(plat.locator('.m-row[data-bot="platform-sre"] .fx-i[title="Leads its team"]')).toBeVisible();
  // no Solo group: bots not on a team are plain rows (no header); Make a team is a bulk action on the bots you pick
  await expect(page.locator('.m-team[data-team="solo"] .m-team-h')).toHaveCount(0);
  await expect(page.locator('.m-team[data-team="solo"] .m-row[data-bot="release-scribe"]')).toBeVisible();
  if (page.viewportSize().width > 720) { // selecting bots is a desktop feature
    await page.locator('.m-row[data-bot="release-scribe"] .m-cbx').check();
    await expect(page.locator('.m-bulk [data-act="bv-bulk-newteam"]')).toBeVisible();
    await page.locator('[data-act="bv-sel-clear"]').click();
  }
  // a team's header: its name, then the summary, then its rules on one line, '+N' for the rest
  if (page.viewportSize().width > 720) expect(await plat.locator('.m-team-h .fx').evaluate((e) => e.getBoundingClientRect().height < 30)).toBe(true); // a phone lets them wrap
  // no needs-you / working-now sections: status is a quiet mark that links there
  await expect(page.locator('#main h2', { hasText: /need you|working now/i })).toHaveCount(0);
  await expect(plat.locator('.m-row[data-bot="platform-sre"] .q-st')).toHaveAttribute('href', '#/attention');
  await groupBy(page, 'bots', 'None');
  await expect(page.locator('.m-flat .m-row')).toHaveCount(await page.evaluate(() => S.view().bots.length));
  // with None, each row says its team (or solo) as a fact
  await expect(page.locator('.m-row[data-bot="platform-sre"] .fx-i[title="Team"]')).toHaveText('platform');
  await expect(page.locator('.m-row[data-bot="homelab"] .fx-i[title="Not on a team"]')).toHaveText('solo');
});

/** Group by is a view control in the filter bar: at the far right on a desktop, a row in the Filter sheet on a phone. */
async function groupBy(page, id, label) {
  if (!isPhone(page)) {
    const btn = page.locator(`.fb[data-fb="${id}"] [data-fb-open="group"]`);
    const [b, bar] = await Promise.all([btn.boundingBox(), page.locator(`.fb[data-fb="${id}"] .fb-bar`).boundingBox()]);
    expect(Math.round(b.x + b.width)).toBeGreaterThan(Math.round(bar.x + bar.width) - 4); // at the far right
    await btn.click();
  } else { await page.locator(`.fb[data-fb="${id}"] [data-fb-sheet]`).click(); await page.locator('.fb-sheet [data-fb-row="group"]').click(); }
  await page.locator('.fb-menu .fb-opt', { hasText: label }).click();
  if (isPhone(page) && await page.locator('.fb-sheet').count()) await page.locator('.fb-sheet [data-fb-done]').click();
  // it filters nothing: the search field holds no qualifier for it
  await expect(page.locator(`.fb[data-fb="${id}"] .fb-in`)).not.toHaveValue(/group:/);
}

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
  // a bot's generated workflow isn't on Workflows at all (only what you can change): no card, no row, no filter option
  await expect(page.locator('.m-team[data-src="bot"]')).toHaveCount(0);
  await expect(page.locator('.m-row[data-wf^="bot:"]')).toHaveCount(0);
  if (page.viewportSize().width > 720) { await page.locator('.fb[data-fb="wl"] [data-fb-open="source"]').click(); await expect(page.locator('.fb-pop, .fb-menu').last()).not.toContainText(/bot/i); await page.keyboard.press('Escape'); }
  await expect(page.locator('.m-row[data-wf="dep-bump"] .fx-i[title="What starts it"]')).toHaveText('Mondays at 06:00');
  // with Group by None, each row says where it came from
  await groupBy(page, 'wl', 'None');
  await expect(page.locator('.m-row[data-wf="pr-review"] .fx-i[title="From a pack"]')).toHaveText('review 1.4.2');
  await expect(page.locator('.m-row[data-wf="ci-fixer"] .fx-i[title="In your config"]')).toHaveText('yours');
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

test('a workflow shows its recent health, not "Running": runs this week and the share that failed, a link to them, a Health filter, Most failing first', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  const ci = page.locator('.m-row[data-wf="ci-fixer"] .wf-health');
  await expect(ci).toContainText(/\d+ runs this week · \d+% failed/);
  await expect(ci).toHaveClass(/fail/); // 20% or more failed: the failure tone
  await expect(ci).toHaveAttribute('href', /#\/activity\?wf=ci-fixer&since=7d/);
  await expect(page.locator('.m-row[data-wf="dep-bump"] .wf-health')).not.toHaveClass(/fail/);
  await expect(page.locator('.m-row .q-st', { hasText: /^Running$/ })).toHaveCount(0);
  // descriptions are sentences, never "It runs its steps"
  for (const t of await page.locator('.m-row[data-wf] .dl-why').allTextContents()) expect(t).not.toMatch(/^(It runs its steps|An agent works on it|Runs a command)/);
  if (page.viewportSize().width > 720) {
    await page.locator('.fb[data-fb="wl"] [data-fb-open="sort"]').click(); await page.locator('.fb-opt', { hasText: 'Most failing first' }).click();
    await expect(page.locator('.m-team[data-src="config"] .m-row').first()).toHaveAttribute('data-wf', 'ci-fixer');
    await page.locator('.fb[data-fb="wl"] [data-fb-open="health"]').click(); await page.locator('.fb-opt', { hasText: 'Failing' }).click(); await page.keyboard.press('Escape');
    const n = await page.locator('.m-row[data-wf]').count(); expect(n).toBeGreaterThan(0);
    await expect(page.locator('.m-row[data-wf] .wf-health.fail')).toHaveCount(n); // only the failing ones
  }
});
