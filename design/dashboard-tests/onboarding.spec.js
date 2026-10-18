// A newcomer's first minutes: Home offers the first steps that are left, the next one as the primary button; New bot asks what it should do, what it
// watches, where it tells you and what it may do, and writes real config; a
// bot that watches nothing says so. (Sam B1, B2, m2; Riley M9 tiles/score)
const { test, expect, go, isPhone } = require('./fixtures');

test('Home: Get started lists what is left, derived from the setup; done steps sit in one quiet line; hide', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const gs = page.locator('.dl-gs');
  await expect(gs.locator('[data-gs="connect"]')).toHaveCount(0); // connectors exist in the demo: done
  await expect(gs.locator('.dl-gs-done')).toContainText('Connect a service');
  await expect(gs.locator('[data-gs="safety"]')).toBeVisible();
  await gs.locator('[data-gs="safety"] .btn').click();
  await expect(page).toHaveURL(/#\/safety/);
  await expect(page.locator('#main h1').first()).toHaveText(/Safety/); // rendered (visiting it is what ticks the step)
  await go(page, '#/overview');
  await expect(page.locator('.dl-gs [data-gs="safety"]')).toHaveCount(0);
  await expect(page.locator('.dl-gs .dl-gs-done')).toContainText('Check what your bots can do');
  await page.locator('[data-act="gs-hide"]').click();
  await expect(page.locator('.dl-gs')).toHaveCount(0);
});

test('New bot: purpose → watches → tells you where → what it may do → review, staged as a bot plus a trigger', async ({ page }) => {
  await go(page, '#/bots', { fresh: true });
  await page.locator('#main .dl-phead a', { hasText: 'New bot' }).click();
  await expect(page).toHaveURL(/#\/bots\/new$/);
  await expect(page.locator('[data-act="bw-next"]')).toHaveCount(0); // Next is disabled until a purpose is picked
  await page.locator('[data-act="bw-purpose"][data-p="incidents"]').click();
  await expect(page.locator('#bw-name')).toHaveValue('incident-bot');
  await page.locator('[data-act="bw-next"]').click();
  await expect(page.locator('[data-on-change="bw-ev"][data-k="pagerduty.incident"]')).toBeChecked();
  await page.locator('[data-on-change="bw-ev"][data-k="github.issue_opened"]').check();
  await page.fill('#bw-repos', 'acme/api');
  await page.locator('[data-act="bw-next"]').click();
  await page.locator('[data-act="bw-tell"][data-v="slack"]').click();
  await page.fill('#bw-channel', '#incidents');
  await page.locator('[data-act="bw-next"]').click();
  await page.locator('[data-act="bw-preset"][data-v="read"]').click();
  await expect(page.locator('.dl-can .cang[data-s="ok"]')).toContainText('Post a message'); // it tells you on its own
  await expect(page.locator('.dl-can .cang[data-s="fail"]')).toContainText('Comment on an issue or PR');
  await expect(page.locator('details.adv')).not.toHaveAttribute('open', '');
  await page.locator('[data-act="bw-next"]').click();
  await expect(page.locator('.bw-body')).toContainText('in #incidents via slack-ops');
  await page.locator('[data-act="bw-create"]').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  await expect(page.locator('.diff').first()).toContainText('incident-bot:');
  const staged = await page.evaluate(() => D.pending.map((c) => `${c.coll}:${c.key}`));
  expect(staged).toEqual(expect.arrayContaining(['bots:incident-bot', 'workflows:incident-bot-incident', 'workflows:incident-bot-issue-opened']));
  const y = await page.evaluate(() => D.pending.find((c) => c.key === 'incident-bot-issue-opened').after);
  expect(y).toContain('bot: { name: incident-bot, title: "{{.title}}" }');
  expect(y).toContain('filter: { repo: [acme/api] }');
  const cfg = await page.evaluate(() => D.pending.find((c) => c.key === 'incident-bot').after);
  expect(cfg.autonomy).toMatchObject({ shadow: true });
  expect(cfg.escalation).toContain('#incidents');
});

test('a bot that watches nothing says so and offers to add what it watches', async ({ page }) => {
  await go(page, '#/bots/release-scribe', { fresh: true });
  const note = page.locator('.watch-empty');
  await expect(note).toContainText("isn't watching anything yet");
  await note.locator('[data-act="bot-watch-add"]').click();
  await page.locator('#f-on').selectOption('gh.release');
  await page.locator('#gen-submit').click();
  await expect(page).toHaveURL(/#\/config\/apply/);
  expect(await page.evaluate(() => D.pending.find((c) => c.key === 'release-scribe-release').after)).toContain('bot: { name: release-scribe');
});

test('watching nothing in the wizard is allowed but explained', async ({ page }) => {
  test.skip(isPhone(page), 'same flow on the phone; desktop covers it');
  await go(page, '#/bots/new', { fresh: true });
  await page.locator('[data-act="bw-purpose"][data-p="custom"]').click();
  await page.fill('#bw-name', 'scratch-bot');
  await page.locator('[data-act="bw-next"]').click();
  await expect(page.locator('.bw-body .callout')).toContainText("won't do anything until it watches something");
});

test('terms explain themselves (hover or keyboard focus); the glossary lists them; the top strip says what each chip means', async ({ page }) => {
  await go(page, '#/workflows', { fresh: true });
  await expect(page.locator('#main .dl-phead')).toContainText('fixed recipe');
  const t = page.locator('#main .dl-phead .term[data-term="workflow"]');
  await t.focus();
  await expect(page.locator('.tooltip')).toContainText('A fixed recipe');
  await go(page, '#/bots');
  // calm: a team says what needs you there directly, so there is no legend to decode
  await expect(page.locator('.m-team[data-team="finance"] .m-team-h')).toContainText('needs you');
  await go(page, '#/glossary');
  await expect(page.locator('#g-provenance')).toContainText('no more trust');
  await expect(page.locator('#g-tier')).toContainText('observe');
  await expect(page.locator('#strip-cells')).not.toContainText('paseo');
  // an available update is said quietly on the machine card (the rail's foot; More on a phone), never in the top bar
  await expect(page.locator('#strip-cells a[href="#/updates"]')).toHaveCount(0);
  if (page.viewportSize().width > 720) await expect(page.locator('.rail .host')).toContainText('installs within 10 min');
});
