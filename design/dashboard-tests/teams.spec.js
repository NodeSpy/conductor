// Teams: define and edit through the pipeline, assign bots, narrow-only
// defaults, routing rules and routed work with provenance, the knowledge
// repo's promotion approvals, links, and the peers graph's team boundaries.
const { test, expect, go, applyStaged } = require('./fixtures');

test('define a team with bots and defaults, through the diff', async ({ page }) => {
  await go(page, '#/bots');
  await page.locator('[data-act="team-new"]').click();
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

test('assign a bot from its definition; a team ceiling refuses a wider bot', async ({ page }) => {
  await go(page, '#/bots/release-scribe/settings');
  await page.locator('#f-df_team').selectOption('platform');
  await expect(page.locator('.inh-hint').first()).toContainText('team platform');
  await page.locator('[data-act="df-review"]').click();
  await applyStaged(page);
  await go(page, '#/bots/release-scribe');
  await expect(page.locator('.bot-hero')).toContainText('team platform');
  // finance requires a container; release-scribe runs in a namespace → refused
  await go(page, '#/teams/finance');
  await page.locator('#team-add-sel').selectOption('release-scribe');
  await page.locator('[data-act="team-add"]').click();
  await expect(page.locator('#chk-state')).toContainText('failed');
  await expect(page.locator('.callout.danger')).toContainText('team finance requires container');
  await expect(page.locator('[data-act="apply-go"]')).toHaveAttribute('aria-disabled', 'true');
});

test('defaults tab shows what bots inherit and narrow; editing defaults stages a change', async ({ page }) => {
  await go(page, '#/teams/platform/defaults');
  await expect(page.locator('.inherit-tbl')).toContainText('inherits');
  await expect(page.locator('.inherit-tbl')).toContainText('narrows');
  await page.locator('[data-act="team-edit"]').first().click();
  await page.fill('#f-pool_runs', '5');
  await page.locator('#gen-submit').click();
  // platform-sre's 6 sweep runs no longer fit the pool
  await expect(page.locator('.callout.danger')).toContainText('over the team pool');
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
  await applyStaged(page);
  await go(page, '#/teams/platform');
  await page.locator('[data-act="team-deliver"]').click();
  await page.selectOption('#f-event', 'ev-pd');
  await page.locator('#gen-submit').click();
  await expect(page).toHaveURL(/#\/bots\/web-triage\/cases\/c\d+/);
  await expect(page.locator('.route-box')).toContainText('routed to platform by rule pd-api → assigned by lead platform-sre to web-triage');
  await expect(page.locator('.route-box')).toContainText('event:pd.incident');
  await go(page, '#/bots/web-triage/cases');
  await expect(page.locator('td.route-cell').first()).toContainText('routed to platform');
  // an operator reassignment is recorded too, requester still kept
  await go(page, '#/bots/web-triage/cases/c33');
  await page.locator('[data-act="case-reassign"]').click();
  await expect(page.locator('.route-box')).toContainText('reassigned by operator ops to platform-sre');
  await expect(page.locator('.route-box')).toContainText('event:gh.issue_opened');
});

test('knowledge: approve a promotion with a passkey; a bot proposes one from memory', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge');
  await page.locator('[data-act="kp-approve"][data-id="k4"]').click();
  await page.locator('#su-go').click();
  await expect(page.locator('#ke-statement')).toHaveValue(/30s/);
  await go(page, '#/bots/web-triage/memory');
  await page.locator('[data-act="rp-promote"]').click();
  await expect(page).toHaveURL(/#\/teams\/platform\/knowledge/);
  await expect(page.locator('.item', { hasText: 'Promote knowledge entry “Checkout margin fallback” from web-triage' })).toContainText('Approve');
});

test('links: unlinking teams is a staged config change; lead change too', async ({ page }) => {
  await go(page, '#/teams/platform');
  await page.locator('[data-act="team-link"][data-o="finance"]').click();
  await expect(page.locator('.facts')).toContainText('change staged');
  await page.locator('[data-act="team-lead"][data-m="web-triage"]').click();
  await expect(page.locator('.diff')).toContainText('lead: web-triage');
  // platform-sre peers billing-sre across teams: without the link that is refused
  await expect(page.locator('.callout.danger')).toContainText('isolated until linked');
});

/* ---------- bots: solo or on a team; delegation between teammates ---------- */

test('old member links land on the bots pages', async ({ page }) => {
  await go(page, '#/members/platform-sre/cases');
  await expect(page).toHaveURL(/#\/bots\/platform-sre\/cases/);
  await expect(page.locator('.page-head, .bot-hero').first()).toContainText('platform-sre');
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
  await expect(page.locator('.route-box')).toContainText('delegated by platform-sre → platform-data from case c29');
  await expect(page.locator('.route-box')).toContainText('event:pd.incident');
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
  await expect(page.locator('.route-box')).toContainText('event:pd.incident');
  await go(page, '#/teams/platform');
  await expect(page.locator('.panel', { hasText: 'Delegations' }).first()).toContainText('Split p99 by AMI');
});

test('cross-team delegation exists only where the teams are linked; solo bots have no one to delegate to', async ({ page }) => {
  // finance ↔ platform are linked: billing-sre may delegate to web-triage
  await go(page, '#/bots/billing-sre/cases/c7');
  await page.locator('[data-act="case-delegate"]').click();
  await page.selectOption('#f-to', 'web-triage');
  await page.fill('#f-task', 'Which web routes call the billing API?');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.item.deleg', { hasText: 'Which web routes' })).toContainText('cross-team · linked');
  // unlink the teams (both sides), drop the explicit cross-team peers that relied on the link, apply
  await go(page, '#/teams/platform');
  await page.locator('[data-act="team-link"][data-o="finance"]').click();
  await go(page, '#/teams/finance');
  await page.locator('[data-act="team-link"][data-o="platform"]').click();
  for (const b of ['platform-sre', 'billing-sre']) {
    await go(page, `#/bots/${b}/settings`);
    await page.locator('[data-listk="df.peers"] [data-act="list-del"]').first().click();
    await expect(page.locator('#df-yaml')).not.toContainText('peers:');
    await page.locator('[data-act="df-review"]').click();
  }
  await expect(page.locator('.list .item')).toHaveCount(4);
  await applyStaged(page);
  await go(page, '#/bots/billing-sre/cases/c7');
  await page.locator('[data-act="case-delegate"]').click();
  await page.selectOption('#f-to', 'web-triage');
  await page.fill('#f-task', 'Again?');
  await page.locator('#gen-submit').click();
  await expect(page.locator('[data-field="to"] .ferr')).toContainText('isolated');
  await page.locator('.modal [data-close]').first().click();
  // a solo bot can't delegate at all
  await go(page, '#/bots/release-scribe/cases');
  await page.locator('[data-act="m-deliver"]').first().click();
  await page.fill('#f-title', 'Draft notes for v2.14'); await page.locator('#gen-submit').click();
  await expect(page.locator('button', { hasText: 'Delegate a task' })).toHaveAttribute('aria-disabled', 'true');
  await expect(page.locator('button', { hasText: 'Delegate a task' })).toHaveAttribute('data-reason', /solo/);
});

test('the team page shows ask and delegate activity and each bot\'s delegation reach', async ({ page }) => {
  await go(page, '#/teams/platform');
  const g = page.locator('svg.act-graph');
  await expect(g.locator('line.ag-delegate')).not.toHaveCount(0);
  await expect(g.locator('line.ag-ask')).not.toHaveCount(0);
  await expect(g).toContainText('billing-sre · finance'); // a linked team's bot, outside the circle
  await expect(page.locator('.panel', { hasText: 'Links' })).toContainText('delegation reach');
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
