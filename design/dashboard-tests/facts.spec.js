// The facts line (UI.facts, the chosen option 1: icon and value): every list row says what a person scans for as
// facts, never a mono machine line; no ids or hashes; production marked; a fact's link opens its own page, not the
// row's; on a phone the low-value facts drop first.
const { test, expect, go } = require('./fixtures');

const LISTS = [['#/attention', '.dl-needs .dl-need'], ['#/activity', '#act-tbl tbody tr[data-href], #act-phone li'], ['#/bots', '.m-team .m-row'], ['#/workflows', '.m-team .m-row'], ['#/bots/platform-sre/proposals', '.proposal-row']];

test('list rows carry a facts line, never a mono machine line, ids or hashes', async ({ page }) => {
  for (const [route, rows] of LISTS) {
    await go(page, route, { fresh: true });
    const vis = page.locator(rows).filter({ has: page.locator('.fx') });
    expect(await vis.count(), route).toBeGreaterThan(0);
    await expect(page.locator('#main .dl-tech.mono'), route).toHaveCount(0);
    const texts = await page.locator('#main .fx').allTextContents();
    for (const t of texts) expect(t, route).not.toMatch(/\b(p|c|r-)[0-9a-f]{2,}\b|\b[0-9a-f]{8,}\b/);
  }
});

test('production is marked; a fact opens its own page, not the row’s', async ({ page }) => {
  await go(page, '#/attention', { fresh: true });
  const row = page.locator('.dl-needs .dl-need', { hasText: 'Replace the production web server' });
  await expect(row.locator('.fx-i.prod')).toContainText('production');
  await expect(row.locator('.fx-i.t-warn')).toContainText('Changes infrastructure'); // graded: infrastructure is amber, like production
  await row.locator('.fx-i.ln', { hasText: 'platform-sre' }).click({ force: true }); // force: on a phone the fact is plain and the tap goes to the row
  // on a desktop the fact opens its own page; on a phone the row is one tap target and opens the item
  if (page.viewportSize().width > 720) await expect(page).toHaveURL(/#\/bots\/platform-sre$/);
  else await expect(page).toHaveURL(/#\/bots\/platform-sre\/proposals\/p12$/);
});

test('on a phone the low-value facts drop first', async ({ page }) => {
  test.skip(page.viewportSize().width > 720, 'phone only');
  await go(page, '#/attention', { fresh: true });
  const low = page.locator('#main .fx-i[data-pri="3"]');
  for (const el of await low.all()) await expect(el).toBeHidden();
});
