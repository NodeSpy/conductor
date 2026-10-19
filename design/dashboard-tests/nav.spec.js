// One way back from every drill-in (ledger item 21): the page's breadcrumb carries its whole path, each step a link,
// the page itself last; no "‹ Back to…" link under the tabs or anywhere; a tab's content starts the standard gap
// below the tab bar and never overlaps it.
const { test, expect, go } = require('./fixtures');

const DRILL = [
  ['#/bots/platform-sre/proposals/p12', ['Bots', 'platform', 'Atlas', 'Approvals'], 'Replace the production web server'],
  ['#/bots/platform-sre/tickets/c29', ['Bots', 'platform', 'Atlas', 'Tickets'], 'SEV2 api p99 latency > 2s'],
  ['#/bots/platform-sre', ['Bots', 'platform'], 'Atlas'],
  ['#/teams/platform/routing', ['Bots'], 'platform'],
  ['#/tickets/c29', ['Tickets'], 'SEV2 api p99 latency > 2s'],
  ['#/tickets/new', ['Tickets'], 'New ticket'],
  ['#/runs/r-7ef0', ['Activity'], null],
  ['#/runs/r-7ef0/compare/r-7e40', ['Activity', null], 'Compare'],
  ['#/extensions/pack/kit', ['Packs', 'kit'], 'Customize'],
  ['#/extensions/source/add', ['Packs', 'Browse'], 'A pack from a source'],
  ['#/browser/b-02', ['Browsers'], 'Approve sign-in · App'],
  ['#/browser/logins', ['Browsers'], 'Logins'],
  ['#/workflows/ci-fixer', ['Workflows'], 'ci-fixer'],
  ['#/config/raw', ['Config'], 'The config file'],
];

for (const [route, links, here] of DRILL) {
  test(`one breadcrumb with the whole path · ${route}`, async ({ page }) => {
    await go(page, route, { fresh: true });
    const crumbs = page.locator('#main .dl-crumbs');
    await expect(crumbs).toHaveCount(1);
    const as = crumbs.locator('a');
    await expect(as).toHaveCount(links.length);
    for (let i = 0; i < links.length; i++) if (links[i]) await expect(as.nth(i)).toHaveText(links[i]);
    await expect(as.first()).toHaveAttribute('href', /^#\/\w/);
    const cur = crumbs.locator('[aria-current="page"]');
    await expect(cur).toHaveCount(1);
    if (here) await expect(cur).toHaveText(here);
    // no second way back
    await expect(page.locator('#main a', { hasText: /^\s*(‹|Back to)/ })).toHaveCount(0);
  });
}

test('a tab drilled into keeps its tab selected; the content starts the standard gap below the tabs', async ({ page }) => {
  for (const [route, tab] of [['#/bots/platform-sre/tickets/c29', 'Tickets'], ['#/bots/platform-sre/tickets', 'Tickets'], ['#/bots/platform-sre/chat', 'Chat'], ['#/teams/platform/routing', 'Routing']]) {
    await go(page, route, { fresh: true });
    const tabs = page.locator('#main .dl-tabs:not(.dl-subnav), #main .tabs').first();
    await expect(tabs.locator('[aria-current="page"], .on').first()).toContainText(tab);
    const g = await page.evaluate(() => { const t = document.querySelector('#main .dl-tabs:not(.dl-subnav), #main .tabs'), body = t.nextElementSibling, first = [...body.children].find((x) => x.offsetHeight);
      return { gap: Math.round(first.getBoundingClientRect().top - t.getBoundingClientRect().bottom), token: parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--gap-stack')) }; });
    expect(g.gap, `${route}: gap below the tabs`).toBeGreaterThanOrEqual(g.token - 1);
    expect(g.gap, `${route}: gap below the tabs`).toBeLessThanOrEqual(g.token + 1);
  }
});
