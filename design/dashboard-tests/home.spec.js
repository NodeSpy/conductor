// Home, as approved in review (and recorded in design/CONFORMANCE.md), less the greeting and its summary sentence
// (they restated the figures and the list below, and claimed "everything else is running normally" beside failures): four At a glance
// tiles (Running links to Activity filtered to running and lists no names); Needs you as plain-sentence items with one
// action each; Running now capped at three, longest-running first, then a quiet "+N more running" link, hidden when
// nothing runs; the last 12 hours as runs per hour with the failures named; For your information; Get started showing
// only what's left. No sunrise, no "Since you last looked", no lanes.
const { test, expect, go } = require('./fixtures');

const day = async (page, demo) => { await page.evaluate((d) => { localStorage.setItem('cd-demo', d); localStorage.removeItem('cd-proto-state-v6'); }, demo); };

test('Home: no greeting or summary line; At a glance, Needs you with one action each, the last 12 hours, For your information', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await expect(page.locator('.dl-hello')).toHaveCount(0);
  await expect(page.locator('#main')).not.toContainText(/Good (morning|afternoon|evening)|running normally/);
  // the page opens on the figures, and Needs you is the first card
  await expect(page.locator('.dl-page > :not(.sr-only)').first()).toHaveClass(/dl-glance/);
  const tiles = page.locator('.dl-glance .dl-tile');
  await expect(tiles).toHaveCount(4);
  await expect(tiles.locator('.dl-lbl')).toHaveText(['Needs you', 'Running', 'Runs, last 12 h', 'Bots']);
  // Needs you agrees with the badge, broken down by kind
  const badge = await page.locator('#rail-nav [data-nav="attention"] .count').textContent();
  await expect(tiles.first().locator('b')).toHaveText(badge.trim());
  await expect(tiles.nth(1)).toHaveAttribute('href', '#/activity?status=run');
  await expect(tiles.nth(1)).not.toContainText('ci-fixer'); // no names under the count
  await expect(tiles.locator('text=/passkey/')).toHaveCount(0);
  // Needs you: every item is one plain title with its reason and exactly one action
  const needs = page.locator('.dl-needs-card .dl-need');
  expect(await needs.count()).toBeGreaterThan(0);
  for (let i = 0; i < await needs.count(); i++) await expect(needs.nth(i).locator('.dl-na .btn')).toHaveCount(1);
  // rows are peers: every row's action is the same secondary button, the same width, with the verb for its kind
  await expect(page.locator('.dl-needs-card .dl-need').first().locator('.btn')).toHaveText('Review');
  await expect(page.locator('.dl-needs-card .dl-need .btn.primary')).toHaveCount(0);
  if (page.viewportSize().width > 720) expect(new Set(await page.locator('.dl-needs-card .dl-need .dl-na .btn').evaluateAll((bs) => bs.map((b) => Math.round(b.getBoundingClientRect().width)))).size).toBe(1);
  await expect(page.locator('.dl-needs-card .dl-need', { hasText: 'Approve a sign-in' }).locator('.btn')).toHaveText('Approve');
  await expect(page.locator('.dl-needs-card .dl-need', { hasText: 'couldn’t fix' }).locator('.btn')).toHaveText('See why');
  // the last 12 hours: one strip for every workflow, its failures named
  await expect(page.locator('svg.dl-hours')).toHaveCount(1);
  await expect(page.locator('.dl-card', { hasText: 'Last 12 hours' }).locator('.dl-foot-note')).toContainText(/failed: .+ at \d\d:\d\d/);
  // a routine update isn't news: it's on the machine card, not under For your information
  await expect(page.locator('.dl-card', { hasText: 'For your information' })).not.toContainText('v0.72.0');
  // retired: the sunrise, the digest, the lanes
  await expect(page.locator('.sunrise, svg.sunrise')).toHaveCount(0);
  await expect(page.getByText(/since you last looked/i)).toHaveCount(0);
  await expect(page.locator('.lanes, .lane')).toHaveCount(0);
});

test('Running now shows at most three, longest-running first, then a quiet link; hidden when nothing runs', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await page.evaluate(() => { const base = D.runs.find((r) => r.status === 'run'); for (let i = 0; i < 5; i++) D.runs.push(Object.assign({}, base, { id: `r-x${i}`, started: new Date(Date.now() - (i + 1) * 60000).toISOString() })); S.save(); });
  await go(page, '#/overview');
  const card = page.locator('.dl-running');
  await expect(card.locator('li')).toHaveCount(3);
  const running = await page.evaluate(() => D.runs.filter((r) => r.status === 'run').length);
  await expect(card.locator('a.dl-more')).toHaveText(`+${running - 3} more running →`);
  await expect(card.locator('a.dl-more')).toHaveAttribute('href', '#/activity?status=run');
  const ages = await card.locator('li .dl-age').allTextContents();
  expect(ages[0]).toMatch(/min|h/); // the longest-running comes first
  await day(page, 'quiet');
  await page.evaluate(() => { localStorage.setItem('cd-demo', 'quiet'); });
  await go(page, '#/overview', { fresh: true });
  await page.evaluate(() => { D.runs.forEach((r) => { if (r.status === 'run') { r.status = 'ok'; r.finished = new Date().toISOString(); } }); S.save(); });
  await go(page, '#/overview');
  await expect(page.locator('.dl-running')).toHaveCount(0);
});

test('Get started shows only what is left, its steps as peers, what is done in one quiet line', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const gs = page.locator('.dl-gs');
  await expect(gs).toBeVisible();
  await expect(gs.locator('.dl-gs-step .btn.primary')).toHaveCount(0);
  await expect(gs.locator('.dl-gs-done')).toContainText('Done:');
  expect(await gs.evaluate((el) => [...el.querySelectorAll('*')].some((x) => getComputedStyle(x).textDecorationLine.includes('line-through')))).toBe(false);
  await gs.locator('[data-act="gs-hide"]').click();
  await expect(page.locator('.dl-gs')).toHaveCount(0);
});

test('the Running tile opens Activity filtered to running', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await page.locator('.dl-glance .dl-tile[href="#/activity?status=run"]').click();
  await expect(page).toHaveURL(/#\/activity\?status=run/);
  await expect(page.locator('.fb[data-fb="act"] .fb-pill')).toContainText(/status: Running/i);
  await expect(page.locator('#act-tbl tbody.g-attn')).toHaveCount(0);
});

// a header sentence earns its place: list pages open on their rows, not on a count of them
test('no header sentence restates the page below it', async ({ page }) => {
  for (const r of ['#/activity', '#/attention', '#/workflows', '#/bots', '#/browser', '#/browser/logins', '#/extensions', '#/connectors', '#/logs', '#/updates']) {
    await go(page, r, { fresh: true });
    await expect(page.locator('#main h1').first()).toBeVisible();
    await expect(page.locator('#main .dl-lead, #main .page-head .lede'), r).toHaveCount(0);
  }
});
