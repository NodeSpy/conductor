// Connector concepts stay in connectors. The fixed filter menus are conductor's own (Kind, Status, Bot, Workflow,
// Time, Folder); what a connector's items carry (a repository, a channel, a service) is a search qualifier the
// connector declares in its schema (js/connattrs.js), namespaced by connector (github.repo:acme/web) with a short
// alias when it's unambiguous (repo:). Screen code that lists, filters, searches or describes items never names a
// connector's field.
const fs = require('fs');
const path = require('path');
const { test, expect, go } = require('./fixtures');

const P = path.join(__dirname, '..', 'dashboard-prototype', 'js');
// the list, filter, search and facts surfaces: Activity, Needs you and a run (runs.js), Home, Bots (folders.js), ⌘K,
// the filter bar itself, and the options frames that preview Bots and Workflows
const SURFACES = ['screens/runs.js', 'screens/overview.js', 'screens/folders.js', 'palette.js', 'filter.js'];
const FIELD = /\.(repo|repos|branch|channel|urgency|check_name|number)\b|['"`](repo|branch|channel|urgency|check_name)['"`]|\b(repo|branch|channel|urgency|check_name):\s/;
const strip = (src) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '').replace(/([;,{(])\s*\/\/ .*$/gm, '$1');

test('screen code names no connector field: they come from the connector’s declared attributes', async () => {
  const bad = [];
  for (const f of SURFACES) strip(fs.readFileSync(path.join(P, f), 'utf8')).split('\n').forEach((l, i) => { const m = l.match(FIELD); if (m) bad.push(`${f}:${i + 1}: ${m[0]} · ${l.trim().slice(0, 90)}`); });
  expect(bad).toEqual([]);
});

test('Activity: the filters GitHub publishes are menus (Repo ▾, PR ▾) and qualifiers (repo:, github.repo:), offered by connector', async ({ page }) => {
  await go(page, '#/activity', { fresh: true });
  const bar = page.locator('.fb[data-fb="act"]');
  if (page.viewportSize().width > 720) {
    await expect(bar.locator('[data-fb-open="github.repo"]')).toHaveText(/Repo/);
    await expect(bar.locator('[data-fb-open="github.pr"]')).toHaveText(/PR/);
    // nothing in view came from PagerDuty's services or Slack's channels: they aren't offered
    await expect(bar.locator('[data-fb-open="pagerduty.service"], [data-fb-open="slack.channel"]')).toHaveCount(0);
  }
  const input = bar.locator('.fb-in');
  await input.fill('');
  await input.focus();
  await expect(bar.locator('.fb-ac')).toBeVisible();
  await expect(bar.locator('.fb-ac .fb-ach', { hasText: /github/i })).toHaveCount(1);
  await expect(bar.locator('.fb-ac')).not.toContainText('linear');
  // the short alias and the namespaced key are the same filter
  const keep = async () => page.evaluate(() => [...document.querySelectorAll('#act-tbl tr[data-href], #act-phone a[href^="#/runs/"]')].filter((x) => x.getClientRects().length).map((x) => x.getAttribute('data-href') || x.getAttribute('href')));
  await input.fill('repo:acme/web');
  const a = await keep();
  expect(a.length).toBeGreaterThan(0);
  expect(await page.evaluate((hs) => hs.every((h) => { const r = D.runs.find((x) => '#/runs/' + x.id === h); return r && UI.itemAttrs(r)['github.repo'] === 'acme/web'; }), a)).toBe(true);
  await input.fill('github.repo:acme/web');
  expect(await keep()).toEqual(a);
  // a value autocompletes from the runs in view
  await input.fill('repo:acme/a');
  await expect(bar.locator('.fb-ac li[data-i]').first()).toContainText('acme/api');
});

test('only active connectors publish: with no GitHub connection there is no Repo menu or repo: qualifier', async ({ page }) => {
  await go(page, '#/activity', { fresh: true });
  await page.evaluate(() => { D.connectors = D.connectors.filter((c) => (c.cfg && c.cfg.use || c.use) !== 'github'); S.save(); });
  await go(page, '#/activity');
  const bar = page.locator('.fb[data-fb="act"]');
  await expect(bar.locator('[data-fb-open="github.repo"]')).toHaveCount(0);
  await bar.locator('.fb-in').fill('re');
  await expect(bar.locator('.fb-ac')).not.toContainText('repo:');
});

test('Needs you: the same qualifiers, and "Showing N of M" whenever one hides anything', async ({ page }) => {
  await go(page, '#/attention', { fresh: true });
  const bar = page.locator('.fb[data-fb="need"]');
  await bar.locator('.fb-in').fill('repo:acme/web');
  await expect(page.locator('.need-shown')).toContainText(/Showing \d+ of \d+/);
  await expect(page.locator('#need-body .dl-need').first()).toContainText('acme/web');
  await bar.locator('.fb-in').press('Escape'); // the suggestions close, as when a person moves on
  await page.locator('.need-shown [data-act="need-clear"]').click();
  await expect(page.locator('.need-shown')).toHaveCount(0);
});
