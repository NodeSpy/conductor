// Teams: define and edit through the pipeline, assign bots, narrow-only
// defaults, routing rules and routed work with provenance, the knowledge
// repo's promotion approvals, links, and the peers graph's team boundaries.
const { test, expect, go, applyStaged, headAction } = require('./fixtures');

test('define a team with bots and defaults, through the diff', async ({ page }) => {
  await go(page, '#/bots');
  await headAction(page, '[data-act="team-new"]');
  await page.fill('#f-name', 'docs');
  await page.fill('#f-description', 'Release notes and API docs');
  await page.locator('[data-listk="bots"] select').selectOption('release-scribe');
  await page.locator('[data-listk="operators"] select').selectOption('ops');
  await page.fill('#f-pool_runs', '4');
  await page.fill('#f-repo', 'team-knowledge/docs');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.diff')).toContainText('docs:');
  await expect(page.locator('.diff')).toContainText('team: docs');
  await applyStaged(page);
  await go(page, '#/teams/docs');
  await expect(page.locator('.panel', { hasText: 'peers of each other by default' })).toContainText('release-scribe');
  await go(page, '#/bots');
  await expect(page.locator('svg.peers-graph rect.pg-team')).toHaveCount(3);
});

test('assign a bot from its definition; the team ceiling and team tiers are enforced in the controls and refused on validation', async ({ page }) => {
  await go(page, '#/bots/release-scribe/settings');
  await page.locator('#f-df_team').selectOption('platform');
  await expect(page.locator('.inh-hint').first()).toContainText('team platform');
  await page.locator('[data-act="df-review"]').click();
  await applyStaged(page);
  await go(page, '#/bots/release-scribe');
  await expect(page.locator('.dl-crumbs')).toContainText('team platform');
  // finance's ceiling is observe: no bot on it may run anything without asking
  await go(page, '#/bots/billing-sre/access');
  await page.selectOption('select[data-v="slack-ops.post"]', 'reversible');
  await go(page, '#/bots/billing-sre/access');
  await expect(page.locator('[data-act="acc-promote"][data-v="slack-ops.post"]')).toHaveAttribute('data-reason', /ceiling is observe/);
  // platform classifies gh.delete_* as never: looser tiers are disabled for its bots
  await go(page, '#/bots/platform-sre/access');
  await expect(page.locator('select[data-v="gh.delete_*"] option', { hasText: 'propose' })).toBeDisabled();
  // and validation refuses a promotion above the ceiling even if it's written by hand
  await page.evaluate(() => { const c = JSON.parse(JSON.stringify(S.staged('bots', 'billing-sre'))); c.autonomy.promote = ['slack-ops.post']; S.stage('bots', 'billing-sre', c); });
  await go(page, '#/config/apply');
  await expect(page.locator('#chk-state')).toContainText('failed');
  await expect(page.locator('.callout.danger').first()).toContainText("team finance's ceiling is observe");
  await expect(page.locator('#checks')).toContainText('Team ceilings');
  await expect(page.locator('[data-act="apply-go"]')).toHaveAttribute('aria-disabled', 'true');
});

test('the ceilings tab shows inherits / narrows / same as team; editing the team stages a change', async ({ page }) => {
  await go(page, '#/teams/platform/defaults');
  await expect(page.locator('.tier-ladder')).toHaveCount(0); // no risk ladder (not approved)
  await expect(page.locator('.inherit-tbl')).toContainText('inherits');
  await expect(page.locator('.inherit-tbl')).toContainText('same as team'); // web-triage's approvers equal the team's: not "narrows"
  await page.locator('[data-act="team-edit"]').first().click();
  await page.fill('#f-pool_runs', '5');
  await page.locator('#gen-submit').click();
  // platform-sre's 6 sweep runs no longer fit the pool
  await expect(page.locator('.callout.danger').first()).toContainText("doesn't fit team platform's proactive pool");
});

test('routing: add a rule, try it on a past event, route work with provenance kept', async ({ page }) => {
  await go(page, '#/teams/platform/routing');
  await page.locator('[data-act="rule-edit"][data-i="-1"]').click();
  await page.fill('#f-name', 'web-alerts');
  await page.selectOption('#f-on', 'gh.issue_opened');
  await page.locator('[data-mapk="conds"] [data-mk=k]').first().fill('repo');
  await page.locator('[data-mapk="conds"] [data-mk=v]').first().fill('acme/api');
  await page.selectOption('#f-to', 'web-triage');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.list')).toContainText('web-alerts');
  await page.selectOption('[data-on-change="route-ev"]', 'ev-pd');
  await expect(page.locator('.callout.ok')).toContainText('pd-api');
  await page.locator('a', { hasText: 'Review staged change' }).click();
  await expect(page.locator('.diff').first()).toContainText('match: { on: gh.issue_opened, repo: acme/api }');
  await applyStaged(page);
  await go(page, '#/teams/platform');
  await headAction(page, '[data-act="team-deliver"]');
  await page.selectOption('#f-event', 'ev-pd');
  await page.locator('#gen-submit').click();
  await expect(page).toHaveURL(/#\/bots\/platform-sre\/cases\/c\d+/);
  await expect(page.locator('.case-head')).toContainText('routed to platform by rule pd-api');
  await expect(page.locator('.case-head')).toContainText('→ platform-sre');
  await expect(page.locator('.case-head')).toContainText('event:pd.incident');
  // an operator reassignment is recorded too, requester still kept
  await go(page, '#/bots/web-triage/cases/c33');
  await page.locator('[data-act="case-more"]').click(); await page.locator('#pop-menu [data-act="case-reassign"][data-to="platform-sre"]').click();
  await expect(page.locator('.case-head')).toContainText('reassigned by operator ops to platform-sre');
  await expect(page.locator('.case-head')).toContainText('event:gh.issue_opened');
});

test('knowledge: approve a promotion with a passkey; a bot proposes one from memory', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge');
  await page.locator('[data-act="kp-approve"][data-id="k4"]').click();
  await page.locator('#su-go').click();
  await expect(page.locator('#rp-ed .ced-ta')).toHaveValue(/30s/);
  await go(page, '#/bots/web-triage/memory');
  await page.locator('[data-act="rp-promote"]').click();
  await expect(page).toHaveURL(/#\/teams\/platform\/knowledge/);
  await expect(page.locator('.item', { hasText: 'Promote knowledge entry “Checkout margin fallback” from web-triage' })).toContainText('Approve');
});

test('links are pairs of bots (bot_links), never whole teams; a lead change is a routing edit', async ({ page }) => {
  await go(page, '#/teams/platform');
  await expect(page.locator('.panel:has(h3:text-is("Links"))')).toContainText('platform-sre ↔ billing-sre');
  await page.locator('[data-act="botlink-add"]').click();
  await page.selectOption('#f-a', 'web-triage'); await page.selectOption('#f-b', 'homelab');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.diff').first()).toContainText('bot_links:');
  await expect(page.locator('ul.widen-list')).toContainText('web-triage and homelab may message each other');
  await page.goto(page.url().replace(/#.*/, '#/teams/platform'));
  await page.locator('[data-act="team-lead"][data-m="web-triage"]').click();
  await expect(page.locator('.diff').first()).toContainText('lead: web-triage');
});

/* ---------- bots: solo or on a team; delegation between teammates ---------- */

test('old member links land on the bots pages', async ({ page }) => {
  await go(page, '#/members/platform-sre/cases');
  await expect(page).toHaveURL(/#\/bots\/platform-sre\/cases/);
  await expect(page.locator('.page-head, .dl-bothead').first()).toContainText('platform-sre');
  await go(page, '#/bots');
  await expect(page.locator('.page-head h1')).toContainText('Bots');
  if (page.viewportSize().width > 720) await expect(page.locator('.frow.bot', { hasText: 'release-scribe' })).toContainText('solo'); // a solo bot
});

test('a case shows its delegation edges with status and the reported result', async ({ page }) => {
  await go(page, '#/bots/platform-sre/cases/c29');
  const d1 = page.locator('.item.deleg[data-deleg="d1"]');
  await expect(d1).toContainText('delegated by platform-sre → platform-data');
  await expect(d1).toContainText('done');
  await expect(d1.locator('.deleg-result')).toContainText('p99 regression starts 14:03');
  await expect(page.locator('.item.deleg[data-deleg="d2"]')).toContainText('running');
  // the recipient's child case carries the original requester
  await page.locator('.item.deleg[data-deleg="d1"] a', { hasText: 'platform-data' }).click();
  await expect(page).toHaveURL(/#\/bots\/platform-data\/cases\/c35/);
  await expect(page.locator('.case-head')).toContainText('delegated by platform-sre → platform-data from case c29');
  await expect(page.locator('.case-head')).toContainText('event:pd.incident');
});

test('closing a delegated case reports its result back to the delegating case', async ({ page }) => {
  await go(page, '#/bots/web-triage/cases/c36');
  await expect(page.locator('.item.deleg.inbound')).toContainText('running');
  await page.fill('#case-note', 'Canary shows the same p99 step at 14:03.');
  await page.locator('[data-act="case-note"]').click();
  await page.locator('[data-act="case-close"]').click();
  await expect(page.locator('.item.deleg.inbound')).toContainText('done');
  await go(page, '#/bots/platform-sre/cases/c29');
  await expect(page.locator('.item.deleg[data-deleg="d2"] .deleg-result')).toContainText('Canary shows the same p99 step');
  await expect(page.locator('.panel', { hasText: 'Notes' })).toContainText('↩ result of d2');
});

test('have a bot delegate to a teammate; the child case works under the original requester', async ({ page }) => {
  await go(page, '#/bots/platform-sre/cases/c29');
  await page.locator('[data-act="case-delegate"]').click();
  await page.selectOption('#f-to', 'platform-data');
  await page.fill('#f-task', 'Split p99 by AMI for the incident window and report the worst route.');
  await page.locator('#gen-submit').click();
  const edge = page.locator('.item.deleg', { hasText: 'Split p99 by AMI' });
  await expect(edge).toContainText('delegated by platform-sre → platform-data');
  await expect(edge).toContainText('running');
  await edge.locator('a', { hasText: 'platform-data' }).click();
  await expect(page.locator('.case-head')).toContainText('event:pd.incident');
  await go(page, '#/teams/platform');
  await expect(page.locator('.panel', { hasText: 'Delegations' }).first()).toContainText('Split p99 by AMI');
});

test('across teams, delegation exists only between linked bots; a bot with no link has no one to delegate to', async ({ page }) => {
  // billing-sre and platform-sre list each other as peers: linked
  await go(page, '#/bots/billing-sre/cases/c7');
  await page.locator('[data-act="case-delegate"]').click();
  await page.selectOption('#f-to', 'platform-sre');
  await page.fill('#f-task', 'Which NAT gateways does platform own?');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.item.deleg', { hasText: 'Which NAT gateways' })).toContainText('linked bots');
  // web-triage is on another team and not linked to billing-sre
  await page.locator('[data-act="case-delegate"]').click();
  await page.selectOption('#f-to', 'web-triage');
  await page.fill('#f-task', 'Again?');
  await page.locator('#gen-submit').click();
  await expect(page.locator('[data-field="to"] .ferr')).toContainText("aren't linked");
  await page.locator('.modal [data-close]').first().click();
  // a solo bot with no links can't delegate at all
  await go(page, '#/bots/release-scribe/cases');
  await headAction(page, '[data-act="m-deliver"]');
  await page.fill('#f-title', 'Draft notes for v2.14'); await page.locator('#gen-submit').click();
  await expect(page.locator('button', { hasText: 'Delegate a task' })).toHaveAttribute('aria-disabled', 'true');
  await expect(page.locator('button', { hasText: 'Delegate a task' })).toHaveAttribute('data-reason', /no teammate or linked bot/);
});

test('the team page lists delegations and each bot\'s delegation reach (the activity graph is gone: it overlapped and isn\'t in the mocks)', async ({ page }) => {
  await go(page, '#/teams/platform');
  await expect(page.locator('svg.act-graph')).toHaveCount(0);
  await expect(page.locator('.panel', { hasText: 'Delegations' }).first()).toBeVisible();
  await expect(page.locator('.panel:has(h3:text-is("Links"))')).toContainText('delegation reach');
});

test('builder: hand a trigger\'s work to a team and test the routing', async ({ page }) => {
  await go(page, '#/workflows/incident-intake');
  await expect(page.locator('[data-card="page"]')).toContainText('to team platform');
  await page.locator('[data-on-change="wf-ev"]').selectOption('ev-pd');
  if (!(await page.locator('[data-card="page"]').getAttribute('class')).includes('open')) await page.locator('[data-act="wf-toggle"][data-id="page"]').click();
  await page.locator('[data-act="wf-test"][data-id="page"]').click();
  await expect(page.locator('[data-card="page"] .results')).toContainText('platform-sre');
  await expect(page.locator('[data-card="page"] .results')).toContainText('rule pd-api');
});
