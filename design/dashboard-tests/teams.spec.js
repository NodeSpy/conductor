// Teams: define and edit through the pipeline, assign members, narrow-only
// defaults, routing rules and routed work with provenance, the knowledge
// repo's promotion approvals, links, and the peers graph's team boundaries.
const { test, expect, go, applyStaged } = require('./fixtures');

test('define a team with members and defaults, through the diff', async ({ page }) => {
  await go(page, '#/members');
  await page.locator('[data-act="team-new"]').click();
  await page.fill('#f-name', 'docs');
  await page.fill('#f-description', 'Release notes and API docs');
  await page.locator('[data-listk="members"] select').selectOption('release-scribe');
  await page.locator('[data-listk="operators"] select').selectOption('ops');
  await page.fill('#f-pool_runs', '4'); await page.fill('#f-pool_cost', '1.5');
  await page.fill('#f-repo', 'team-knowledge/docs');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.diff')).toContainText('docs:');
  await expect(page.locator('.diff')).toContainText('team: docs');
  await applyStaged(page);
  await go(page, '#/teams/docs');
  await expect(page.locator('.panel', { hasText: 'Members' })).toContainText('release-scribe');
  await go(page, '#/members');
  await expect(page.locator('svg.peers-graph rect.pg-team')).toHaveCount(3);
});

test('assign a member from its definition; a team ceiling refuses a wider member', async ({ page }) => {
  await go(page, '#/members/release-scribe/settings');
  await page.locator('#f-df_team').selectOption('platform');
  await expect(page.locator('.inh-hint').first()).toContainText('team platform');
  await page.locator('[data-act="df-review"]').click();
  await applyStaged(page);
  await go(page, '#/members/release-scribe');
  await expect(page.locator('.member-hero')).toContainText('team platform');
  // finance requires a container; release-scribe runs in a namespace → refused
  await go(page, '#/teams/finance');
  await page.locator('#team-add-sel').selectOption('release-scribe');
  await page.locator('[data-act="team-add"]').click();
  await expect(page.locator('#chk-state')).toContainText('failed');
  await expect(page.locator('.callout.danger')).toContainText('team finance requires container');
  await expect(page.locator('[data-act="apply-go"]')).toHaveAttribute('aria-disabled', 'true');
});

test('defaults tab shows what members inherit and narrow; editing defaults stages a change', async ({ page }) => {
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
  await expect(page).toHaveURL(/#\/members\/web-triage\/cases\/c\d+/);
  await expect(page.locator('.route-box')).toContainText('routed to platform by rule pd-api → assigned by lead platform-sre to web-triage');
  await expect(page.locator('.route-box')).toContainText('event:pd.incident');
  await go(page, '#/members/web-triage/cases');
  await expect(page.locator('td.route-cell').first()).toContainText('routed to platform');
  // an operator reassignment is recorded too, requester still kept
  await go(page, '#/members/web-triage/cases/c33');
  await page.locator('[data-act="case-reassign"]').click();
  await expect(page.locator('.route-box')).toContainText('reassigned by operator ops to platform-sre');
  await expect(page.locator('.route-box')).toContainText('event:gh.issue_opened');
});

test('knowledge: approve a promotion with a passkey; a member proposes one from memory', async ({ page }) => {
  await go(page, '#/teams/platform/knowledge');
  await page.locator('[data-act="kn-decide"][data-id="k4"][data-d="approve"]').click();
  await page.locator('#su-go').click();
  await expect(page.locator('.prose')).toContainText('30s');
  await go(page, '#/members/web-triage/memory');
  await page.locator('[data-act="mem-promote"]').click();
  await expect(page).toHaveURL(/#\/teams\/platform\/knowledge/);
  await expect(page.locator('.item', { hasText: 'Promote README.md from web-triage' })).toContainText('Approve');
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
