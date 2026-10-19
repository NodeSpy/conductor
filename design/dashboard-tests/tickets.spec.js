// Tickets (ledger item 22, with 22b–d): tasks are tickets in conductor's own system (no outside ticketing). A ticket
// moves draft → ready → in progress → waiting on you → done, or is declined; a draft is an idea you and the bots shape,
// and nobody starts work on it until an operator marks it ready with an assignee. A list or a board by status, New
// ticket, a ticket's page with its comments (the chat component), a bot's and a team's Tickets tabs, a ticket made
// from a chat message.
const { test, expect, go } = require('./fixtures');
// on a phone the board shows one column at a time: open the one a ticket is in
const reveal = async (page, title) => { if (page.viewportSize().width > 720) return; const k = await page.locator('.tk-card', { hasText: title }).first().evaluate((e) => e.closest('.tk-col').dataset.col); await page.locator(`.tk-colpick [data-v="${k}"]`).click(); };

test('Tickets is in the nav; the Board first, drafts its first column; cards in words with the facts line, no ids', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  await expect(page.locator('#main h1')).toContainText('Tickets');
  if (page.viewportSize().width > 720) await expect(page.locator('#rail-nav [data-nav="tickets"]')).toHaveClass(/on/);
  await expect(page.locator('[data-act="tk-view"][data-v="board"]')).toHaveAttribute('aria-pressed', 'true');
  const drafts = page.locator('.tk-col[data-col="draft"]');
  await expect(drafts.locator('h2')).toHaveText('Draft');
  await expect(drafts.locator('.tk-card')).toHaveCount(4);
  await expect(drafts.locator('.tk-card', { hasText: 'Idea: move the nightly backups' })).toContainText('Drafted by Hearth');
  const urgent = page.locator('.tk-card', { hasText: 'SEV2 api p99 latency' });
  await expect(urgent.locator('.fx-i.hi')).toContainText('Urgent');
  await expect(urgent.locator('.fx')).toContainText('Atlas');
  for (const t of await page.locator('.tk-card').allTextContents()) expect(t).not.toMatch(/\bc\d{2}\b/);
});

test('nothing about outside ticket systems', async ({ page }) => {
  for (const route of ['#/tickets', '#/tickets/c29', '#/tickets/c33', '#/tickets/new']) {
    await go(page, route, { fresh: true });
    await expect(page.locator('#main')).not.toContainText(/GitHub issue|mirrored|Sync now|Outside conductor|Linked to/i);
  }
});

test('the filter bar narrows by status and label, and says what it hides', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  const all = await page.locator('.tk-card').count();
  await page.fill('#main .fb-in', 'status:draft');
  await page.keyboard.press('Enter');
  await expect(page.locator('.tk-card')).toHaveCount(4);
  await expect(page.locator('.tk-showing')).toContainText('Showing 4 of');
  await page.locator('[data-act="tk-clear"]').click();
  await expect(page.locator('.tk-card')).toHaveCount(all);
  await page.fill('#main .fb-in', 'label:incident');
  await page.keyboard.press('Enter');
  for (const t of await page.locator('.tk-card .fx').allTextContents()) expect(t).toContain('incident');
});

test('the board: one column per status, drafts first, each scrolling inside itself; on a phone one column at a time', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  await expect(page.locator('.tk-col h2')).toHaveText(['Draft', 'Ready', 'In progress', 'Waiting on you', 'Done']);
  await expect(page.locator('.tk-col[data-col="draft"] .tk-card')).toHaveCount(4);
  expect(await page.evaluate(() => { const m = document.getElementById('main'); return m.scrollWidth <= m.clientWidth + 1; })).toBe(true);
  if (page.viewportSize().width <= 720) {
    await expect(page.locator('.tk-col:visible')).toHaveCount(1); // one column at a time, picked above it
    await page.locator('.tk-colpick [data-v="waiting"]').click();
    await expect(page.locator('.tk-col[data-col="waiting"]')).toBeVisible();
    await expect(page.locator('.tk-col[data-col="draft"]')).toBeHidden();
  } else {
    await expect(page.locator('.tk-col[data-col="waiting"] .tk-card', { hasText: 'SEV2 api p99 latency' })).toBeVisible();
    for (const k of ['draft', 'ready', 'done']) expect(await page.locator(`.tk-col[data-col="${k}"] .tk-cards`).evaluate((e) => getComputedStyle(e).overflowY), k).toBe('auto');
  }
});

test('Done holds the last 7 days, Show older for the rest; the List is one flat list, paged, its status a fact; the choice is remembered', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  const phone = page.viewportSize().width <= 720;
  if (phone) await page.locator('.tk-colpick [data-v="done"]').click();
  const done = page.locator('.tk-col[data-col="done"]');
  await expect(done.locator('.tk-card', { hasText: 'Retire the old deploy keys' })).toHaveCount(0);
  await done.locator('[data-act="tk-older"]').click();
  await expect(done.locator('.tk-card', { hasText: 'Retire the old deploy keys' })).toHaveCount(1);
  await page.locator('[data-act="tk-view"][data-v="list"]').click();
  await expect(page.locator('.tk-flat .tk-row').first()).toBeVisible();
  await expect(page.locator('.tk-sec h2')).toHaveCount(0); // no grouping
  await expect(page.locator('.tk-flat .tk-row', { hasText: 'SEV2 api p99 latency' }).locator('.fx-i[title="Status"]')).toHaveText('Waiting on you');
  expect(await page.locator('.tk-flat .tk-row').count()).toBeLessThanOrEqual(20);
  await go(page, '#/tickets');
  await expect(page.locator('[data-act="tk-view"][data-v="list"]')).toHaveAttribute('aria-pressed', 'true'); // remembered
});

test('drag a card to change its status, within the rules', async ({ page }) => {
  test.skip(page.viewportSize().width <= 720, 'dragging is a desktop gesture; a phone moves a ticket from its page');
  await go(page, '#/tickets', { fresh: true });
  const drag = async (title, to) => page.locator('.tk-card', { hasText: title }).dragTo(page.locator(`.tk-col[data-col="${to}"]`));
  await drag('Rotate the staging TLS certificate', 'done');
  await expect(page.locator('.tk-col[data-col="done"] .tk-card', { hasText: 'Rotate the staging TLS certificate' })).toBeVisible();
  await drag('Rotate the staging TLS certificate', 'ready'); // reopened
  await expect(page.locator('.tk-col[data-col="ready"] .tk-card', { hasText: 'Rotate the staging TLS certificate' })).toBeVisible();
  await drag('Rotate the staging TLS certificate', 'progress'); // only conductor moves a ticket there
  await expect(page.locator('.toast').last()).toContainText('conductor moves it there');
  await expect(page.locator('.tk-col[data-col="ready"] .tk-card', { hasText: 'Rotate the staging TLS certificate' })).toBeVisible();
  await drag('Rotate the staging TLS certificate', 'draft');
  await expect(page.locator('.tk-col[data-col="draft"] .tk-card', { hasText: 'Rotate the staging TLS certificate' })).toBeVisible();
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
  await reveal(page, 'Try a cheaper instance type');
  await expect(page.locator('.tk-card', { hasText: 'Try a cheaper instance type' })).toBeVisible();
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
  await expect(page.locator('.tk-head .fx')).toContainText('Scout');
  await expect(page.locator('.tk-hist')).toContainText('Assigned');
  await go(page, '#/bots/web-triage/cases');
  await expect(page.locator('.dl-tabs a[aria-current="page"]')).toContainText('Tickets');
  await reveal(page, 'SEV2 api p99 latency');
  await expect(page.locator('.tk-card', { hasText: 'SEV2 api p99 latency' })).toBeVisible();
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

test('Ready has its own mark (not parked); a card opens, and a button only for a real next action; titles read as people write them', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  const col = (k) => page.locator(`.tk-col[data-col="${k}"]`);
  const show = async (k) => { if (page.viewportSize().width <= 720) await page.locator(`.tk-colpick [data-v="${k}"]`).click(); };
  await expect(col('ready').locator('.tk-colh .st')).toHaveAttribute('data-s', 'ready');
  await expect(page.locator('.tk-board .st[data-s="idle"]')).toHaveCount(0); // ‖ means parked, nowhere here
  // a draft's action marks it ready; a ready card has none; waiting on you opens what it waits for
  await show('draft'); await expect(col('draft').locator('.tk-card').first().locator('[data-act="tk-ready"]')).toHaveText('Mark ready…');
  await show('ready'); await expect(col('ready').locator('.tk-card .btn')).toHaveCount(0);
  await show('waiting'); await expect(col('waiting').locator('.tk-card', { hasText: 'SEV2 api p99 latency' }).locator('.btn')).toHaveAttribute('data-fx-href', /#\/bots\/[\w-]+\/proposals\/p\d+/);
  for (const t of await page.locator('.tk-card b').allTextContents()) expect(t, 'a machine prefix in a title').not.toMatch(/^(FINDING|[a-z]{2,4}:)\s/);
  // Mark ready from the card, without opening the ticket
  await show('draft'); await col('draft').locator('.tk-card', { hasText: 'Write a runbook' }).locator('[data-act="tk-ready"]').click();
  await expect(page).toHaveURL(/#\/tickets$/);
  await show('ready'); await expect(col('ready').locator('.tk-card', { hasText: 'Write a runbook' })).toBeVisible();
});

// In progress is a state, not activity (59): the brand's static part-filled capsule, never the spinner; a row says
// "a worker is running" (with the spinner) only while one executes now, else its last activity when that says more
// than its age, flagged once nothing has happened for a day
test('In progress: a static mark; the spinner only while a worker of it is running; a stalled one shows its last activity', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  const head = page.locator('.tk-col[data-col="progress"] .tk-colh [data-s="progress"]');
  await expect(head).toHaveCount(1);
  expect(await head.locator('rect').count(), 'the capsule').toBe(2);
  expect(await head.evaluate((e) => getComputedStyle(e.querySelector('svg')).animationName)).toBe('none');
  expect(await page.locator('#main [data-s="run"]').count(), 'no spinner on Tickets while nothing executes').toBe(0);
  const id = await page.evaluate(() => D.cases.find((c) => UI.ticketStatus(c).k === 'progress').id);
  await page.evaluate((cid) => { D.workers.push({ id: 'w-t59', case: cid, status: 'run', brief: 'a check', started: new Date().toISOString() }); UI.rerender(); }, id);
  const card = page.locator(`.tk-card[data-t="${id}"]`);
  await expect(card).toContainText('a worker is running');
  await expect(card.locator('[data-s="run"]')).toHaveCount(1);
  await page.evaluate((cid) => { D.workers = D.workers.filter((w) => w.id !== 'w-t59'); const c = D.cases.find((x) => x.id === cid); c.opened = new Date(Date.now() - 3 * 864e5).toISOString(); delete c.updated; (D.delegations || []).forEach((d) => { if (d.parent === cid) d.started = c.opened; }); UI.rerender(); }, id);
  if (await page.evaluate((cid) => UI.ticketStatus(D.cases.find((x) => x.id === cid)).k, id) === 'progress') {
    await expect(card).toContainText(/last activity \d+d ago/);
    await expect(card.locator('[data-s="run"]')).toHaveCount(0);
  }
});
