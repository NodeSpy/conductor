// Tickets (ledger item 22, with 22b–d): tasks are tickets in conductor's own system (no outside ticketing). A ticket
// moves draft → ready → in progress → waiting on you → done, or is declined; a draft is an idea you and the bots shape,
// and nobody starts work on it until an operator marks it ready with an assignee. A list or a board by status, New
// ticket, a ticket's page with its comments (the chat component), a bot's and a team's Tickets tabs, a ticket made
// from a chat message.
const { test, expect, go } = require('./fixtures');

test('Tickets is in the nav; drafts first; rows in words with the facts line, no ids', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  await expect(page.locator('#main h1')).toContainText('Tickets');
  if (page.viewportSize().width > 720) await expect(page.locator('#rail-nav [data-nav="tickets"]')).toHaveClass(/on/);
  const drafts = page.locator('#tk-draft');
  await expect(drafts.locator('h2')).toHaveText('Draft');
  await expect(drafts).toContainText('Nobody starts work on a draft until someone marks it ready');
  await expect(drafts.locator('.tk-row')).toHaveCount(4);
  await expect(drafts.locator('.tk-row', { hasText: 'Idea: move the nightly backups' })).toContainText('Drafted by homelab');
  const urgent = page.locator('.tk-row', { hasText: 'SEV2 api p99 latency' });
  await expect(urgent.locator('.fx-i.hi')).toContainText('Urgent');
  await expect(urgent.locator('.fx')).toContainText('platform-sre');
  for (const t of await page.locator('.tk-row .dl-nt').allTextContents()) expect(t).not.toMatch(/\bc\d{2}\b/);
});

test('nothing about outside ticket systems', async ({ page }) => {
  for (const route of ['#/tickets', '#/tickets/c29', '#/tickets/c33', '#/tickets/new']) {
    await go(page, route, { fresh: true });
    await expect(page.locator('#main')).not.toContainText(/GitHub issue|mirrored|Sync now|Outside conductor|Linked to/i);
  }
});

test('the filter bar narrows by status and label, and says what it hides', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  const all = await page.locator('.tk-row').count();
  await page.fill('#main .fb-in', 'status:draft');
  await page.keyboard.press('Enter');
  await expect(page.locator('.tk-row')).toHaveCount(4);
  await expect(page.locator('.tk-showing')).toContainText('Showing 4 of');
  await page.locator('[data-act="tk-clear"]').click();
  await expect(page.locator('.tk-row')).toHaveCount(all);
  await page.fill('#main .fb-in', 'label:incident');
  await page.keyboard.press('Enter');
  for (const t of await page.locator('.tk-row .fx').allTextContents()) expect(t).toContain('incident');
});

test('the board: one column per status, drafts first; on a phone the columns stack', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  await page.locator('[data-act="tk-view"][data-v="board"]').click();
  await expect(page.locator('.tk-col h2')).toHaveText(['Draft', 'Ready', 'In progress', 'Waiting on you', 'Done']);
  await expect(page.locator('.tk-col[data-col="draft"] .tk-card')).toHaveCount(4);
  await expect(page.locator('.tk-col[data-col="waiting"] .tk-card', { hasText: 'SEV2 api p99 latency' })).toBeVisible();
  expect(await page.evaluate(() => { const m = document.getElementById('main'); return m.scrollWidth <= m.clientWidth + 1; })).toBe(true);
  if (page.viewportSize().width <= 720) { const xs = await page.locator('.tk-col').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().left))); expect(new Set(xs).size).toBe(1); }
});

test('a draft: no work starts; Mark ready asks who does it, then it is ready for them', async ({ page }) => {
  await go(page, '#/tickets/new', { fresh: true });
  await page.fill('#tk-title', 'Try a cheaper instance type for the CI runners');
  await page.fill('#tk-desc', 'Just an idea for now.');
  await page.locator('[data-act="tk-create"][data-stage="draft"]').click();
  await expect(page).toHaveURL(/#\/tickets\/c\d+$/);
  await expect(page.locator('.tk-head .case-state')).toContainText('Draft');
  await expect(page.locator('.tk-ready')).toContainText('until you mark it ready');
  await expect(page.locator('[data-act="case-delegate"]')).toHaveCount(0);
  await page.locator('[data-act="tk-ready"]').click();
  await page.selectOption('#f-who', 'team:platform');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.tk-head .case-state')).toContainText('Ready');
  await expect(page.locator('.tk-chat')).toContainText('Marked ready by you');
  await expect(page.locator('.tk-head .fx')).toContainText('team platform');
  await go(page, '#/teams/platform/tickets');
  await expect(page.locator('.tk-row', { hasText: 'Try a cheaper instance type' })).toBeVisible();
});

test('Create, ready to start needs someone to do it', async ({ page }) => {
  await go(page, '#/tickets/new', { fresh: true });
  await page.fill('#tk-title', 'Nobody to do it');
  await page.locator('[data-act="tk-create"][data-stage="ready"]').click();
  await expect(page).toHaveURL(/#\/tickets\/new/);
  await expect(page.locator('.toast').last()).toContainText('needs someone to do it');
});

test('New ticket for a team, ready: attachments, priority, labels; a comment reaches it', async ({ page }) => {
  await go(page, '#/tickets/new?team=platform', { fresh: true });
  await expect(page.locator('#tk-who')).toHaveValue('team:platform');
  await page.fill('#tk-title', 'Renew the api.example.com certificate');
  await page.selectOption('#tk-pri', 'high');
  await page.fill('#tk-labels', 'security, api');
  await page.setInputFiles('#tk-file', [{ name: 'cert.txt', mimeType: 'text/plain', buffer: Buffer.from('notAfter=…') }, { name: 'clip.mov', mimeType: 'video/quicktime', buffer: Buffer.alloc(4) }]);
  await expect(page.locator('#tk-pend .cm-p')).toHaveCount(1);
  await expect(page.locator('#tk-err')).toContainText('clip.mov wasn’t attached');
  await page.locator('[data-act="tk-create"][data-stage="ready"]').click();
  await expect(page.locator('.tk-head h1')).toHaveText('Renew the api.example.com certificate');
  await expect(page.locator('.tk-head .case-state')).toContainText('Ready');
  await expect(page.locator('.tk-head .fx')).toContainText('High');
  await expect(page.locator('.tk-chat .cm-file')).toContainText('cert.txt');
  await page.fill('#say', 'The renewal window opens on Monday.'); await page.locator('#say').press('Enter');
  await expect(page.locator('.tk-chat .cm-op').last()).toContainText('The renewal window opens on Monday.');
});

test('decline closes a ticket without doing it; reopening brings it back', async ({ page }) => {
  await go(page, '#/tickets/c27', { fresh: true });
  await page.locator('[data-act="tk-decline"]').click(); await page.locator('#cf-ok').click();
  await expect(page.locator('.tk-head .case-state')).toContainText('Declined');
  await page.locator('[data-act="case-close"]').click();
  await expect(page.locator('.tk-head .case-state')).not.toContainText('Declined');
});

test('details change in place; a ticket shows its linked work', async ({ page }) => {
  await go(page, '#/tickets/c29', { fresh: true });
  await expect(page.locator('.tk-side')).toContainText('Replace the production web server');
  await page.locator('[data-act="tk-set"][data-k="priority"]').click();
  await page.selectOption('#f-priority', 'low');
  await page.locator('#gen-submit').click();
  await expect(page.locator('[data-act="tk-set"][data-k="priority"]')).toContainText('Low');
  await page.locator('[data-act="tk-set"][data-k="who"]').click();
  await page.selectOption('#f-who', 'web-triage');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.tk-head .fx')).toContainText('web-triage');
  await expect(page.locator('.tk-hist')).toContainText('Assigned');
  await go(page, '#/bots/web-triage/cases');
  await expect(page.locator('.dl-tabs a[aria-current="page"]')).toContainText('Tickets');
  await expect(page.locator('.tk-row', { hasText: 'SEV2 api p99 latency' })).toBeVisible();
});

test('make a ticket from a chat message; old task links open the ticket', async ({ page }) => {
  await go(page, '#/bots/platform-sre/chat', { fresh: true });
  await page.locator('#msgs .cm-op').first().hover();
  await page.locator('#msgs .cm-op').first().locator('[data-act="chat-ticket"]').click();
  await expect(page).toHaveURL(/#\/tickets\/new\?bot=platform-sre/);
  await expect(page.locator('#tk-who')).toHaveValue('platform-sre');
  await expect(page.locator('#tk-desc')).toHaveValue(/what the dashboard showed/);
  await expect(page.locator('#tk-title')).not.toHaveValue('');
  await page.locator('[data-act="tk-create"][data-stage="ready"]').click();
  await expect(page.locator('.tk-chat')).toContainText('Made from a message in platform-sre’s chat');
  // a ticket opened from a bot's Tickets tab stays in the bot's page, the tab selected and the path in the breadcrumb
  await go(page, '#/bots/platform-sre/cases/c29');
  await expect(page.locator('.dl-crumbs')).toHaveText(/Bots\s*›\s*platform\s*›\s*Atlas\s*›\s*Tickets\s*›\s*SEV2 api p99 latency > 2s/);
  await expect(page.locator('.dl-tabs a[aria-current="page"]')).toContainText('Tickets');
  await expect(page.locator('.tk-head h1')).toHaveText('SEV2 api p99 latency > 2s');
});

test('Ready has its own mark (not parked); the row opens, and a button only for a real next action; titles read as people write them', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  const ready = page.locator('#tk-ready .tk-row').first();
  await expect(ready.locator('.st').first()).toHaveAttribute('data-s', 'ready');
  await expect(page.locator('.tk-row .st[data-s="idle"]')).toHaveCount(0); // ‖ means parked, nowhere here
  // a draft's action marks it ready; a ready or in-progress row has none; waiting on you opens what it waits for
  await expect(page.locator('#tk-draft .tk-row').first().locator('[data-act="tk-ready"]')).toHaveText('Mark ready…');
  await expect(page.locator('#tk-ready .tk-row .dl-na .btn')).toHaveCount(0);
  const wait = page.locator('#tk-waiting .tk-row', { hasText: 'SEV2 api p99 latency' });
  await expect(wait.locator('.dl-na .btn')).toHaveAttribute('data-fx-href', /#\/bots\/[\w-]+\/proposals\/p\d+/);
  for (const t of await page.locator('.tk-row .dl-nt > b').allTextContents()) expect(t, 'a machine prefix in a title').not.toMatch(/^(FINDING|[a-z]{2,4}:)\s/);
  // Mark ready from the row, without opening the ticket
  await page.locator('#tk-draft .tk-row', { hasText: 'Write a runbook' }).locator('[data-act="tk-ready"]').click();
  await expect(page).toHaveURL(/#\/tickets$/);
  await expect(page.locator('#tk-ready .tk-row', { hasText: 'Write a runbook' })).toBeVisible();
});
