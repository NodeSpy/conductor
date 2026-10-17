// A newcomer's first minutes: the overview says what conductor is and offers
// four plain first steps with ticks; New bot asks what it should do, what it
// watches, where it tells you and what it may do, and writes real config; a
// bot that watches nothing says so. (Sam B1, B2, m2; Riley M9 tiles/score)
const { test, expect, go, isPhone } = require('./fixtures');

test('overview: one-line explanation, Get started with derived ticks, hide and bring back', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  await expect(page.locator('.page-head')).toContainText('anything risky waits for your passkey');
  const gs = page.locator('.panel.gs');
  await expect(gs.locator('.gs-card')).toHaveCount(4);
  await expect(gs.locator('[data-gs="connect"]')).toHaveClass(/done/); // connectors exist in the demo
  await expect(gs.locator('[data-gs="safety"]')).not.toHaveClass(/done/);
  await gs.locator('[data-gs="safety"]').click();
  await expect(page).toHaveURL(/#\/safety/);
  await go(page, '#/overview');
  await expect(page.locator('[data-gs="safety"]')).toHaveClass(/done/);
  await page.locator('[data-act="gs-hide"]').click();
  await expect(page.locator('.panel.gs')).toHaveCount(0);
  await page.locator('[data-act="gs-show"]').click();
  await expect(page.locator('.panel.gs')).toBeVisible();
});

test('overview: "Runs, last 12h" (not "The score"), busiest rows first, filtered by folder; System is collapsed', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const runs = page.locator('.panel.score');
  await expect(runs.locator('h3')).toHaveText('Runs, last 12h');
  await expect(page.locator('text=The score')).toHaveCount(0);
  await expect(runs.locator('.lane-name').first()).toHaveText('pr-review');
  await runs.locator('[data-on-change="folder-scope"]').selectOption({ index: 1 });
  const n = await runs.locator('.lane').count();
  expect(n).toBeLessThan(6);
  const sys = page.locator('details.sys-card');
  await expect(sys).not.toHaveAttribute('open', '');
  await expect(sys.locator('summary')).toContainText('System');
  await sys.locator('summary').click();
  await expect(sys.locator('.list')).toContainText('conductor');
  // the Bots tile reads "6 bots", with the live count in words
  await expect(page.locator('.stat.hot .v')).toContainText('bots');
  await expect(page.locator('.stat.hot .d')).toContainText('in a live session');
});

test('New bot: purpose → watches → tells you where → what it may do → review, staged as a bot plus a trigger', async ({ page }) => {
  await go(page, '#/bots', { fresh: true });
  await page.locator('.page-head a', { hasText: 'New bot' }).click();
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
  await expect(page.locator('.can-col.own')).toContainText('slack-ops.post'); // it tells you on its own
  await expect(page.locator('.can-col.never')).toContainText('gh.comment');
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
