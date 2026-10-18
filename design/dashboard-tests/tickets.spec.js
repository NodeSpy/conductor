// Tickets (ledger item 22): tasks are tickets. A ticket is for a team or assigned to a bot, or waits in triage; the list
// has the shared filter bar; New ticket; a ticket's page with its comments (the chat component), details, the work it
// links to and its mirror outside conductor; a bot's and a team's Tickets tabs; a ticket made from a chat message.
const { test, expect, go } = require('./fixtures');

test('Tickets is in the nav; triage first; rows in words with the facts line', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  await expect(page.locator('#main h1')).toContainText('Tickets');
  if (page.viewportSize().width > 720) await expect(page.locator('#rail-nav [data-nav="tickets"]')).toHaveClass(/on/);
  const triage = page.locator('#tk-triage');
  await expect(triage.locator('h2')).toHaveText('Not assigned yet');
  await expect(triage.locator('.tk-row')).toHaveCount(2);
  await expect(triage.locator('.tk-row', { hasText: 'Add a public status page' }).locator('.fx')).toContainText('acme/api#486');
  await expect(triage.locator('.tk-row').first().locator('.dl-na .btn')).toHaveText('Assign');
  const urgent = page.locator('.tk-row', { hasText: 'SEV2 api p99 latency' });
  await expect(urgent.locator('.fx-i.hi')).toContainText('Urgent');
  await expect(urgent.locator('.fx')).toContainText('platform-sre');
  // no ticket ids in the rows
  for (const t of await page.locator('.tk-row .dl-nt').allTextContents()) expect(t).not.toMatch(/\bc\d{2}\b/);
});

test('the filter bar narrows by status, assignee and label, and says what it hides', async ({ page }) => {
  await go(page, '#/tickets', { fresh: true });
  const all = await page.locator('.tk-row').count();
  await page.fill('#main .fb-in', 'status:new');
  await page.keyboard.press('Enter');
  await expect(page.locator('.tk-row')).toHaveCount(2);
  await expect(page.locator('.tk-showing')).toContainText(`Showing 2 of ${all}`);
  await page.locator('[data-act="tk-clear"]').click();
  await expect(page.locator('.tk-row')).toHaveCount(all);
  await page.fill('#main .fb-in', 'label:incident');
  await page.keyboard.press('Enter');
  for (const t of await page.locator('.tk-row .fx').allTextContents()) expect(t).toContain('incident');
});

test('New ticket for a team: it lands with the team, and a comment reaches it', async ({ page }) => {
  await go(page, '#/tickets/new?team=platform', { fresh: true });
  await expect(page.locator('#tk-who')).toHaveValue('team:platform');
  await page.fill('#tk-title', 'Renew the api.example.com certificate');
  await page.fill('#tk-desc', 'Expires in 12 days.');
  await page.selectOption('#tk-pri', 'high');
  await page.fill('#tk-labels', 'security, api');
  await page.setInputFiles('#tk-file', [{ name: 'cert.txt', mimeType: 'text/plain', buffer: Buffer.from('notAfter=…') }, { name: 'clip.mov', mimeType: 'video/quicktime', buffer: Buffer.alloc(4) }]);
  await expect(page.locator('#tk-pend .cm-p')).toHaveCount(1);
  await expect(page.locator('#tk-err')).toContainText('clip.mov wasn’t attached');
  await page.locator('#tk-gh').check();
  await page.locator('[data-act="tk-create"]').click();
  await expect(page).toHaveURL(/#\/tickets\/c\d+$/);
  await expect(page.locator('.tk-head h1')).toHaveText('Renew the api.example.com certificate');
  await expect(page.locator('.tk-head .fx')).toContainText('team platform');
  await expect(page.locator('.tk-head .fx-i.hi, .tk-head .fx')).toContainText('High');
  await expect(page.locator('.tk-ext')).toContainText('GitHub issue');
  await expect(page.locator('.tk-chat .cm-file')).toContainText('cert.txt');
  await page.fill('#say', 'The renewal window opens on Monday.'); await page.locator('#say').press('Enter');
  await expect(page.locator('.tk-chat .cm-op').last()).toContainText('The renewal window opens on Monday.');
  await go(page, '#/teams/platform/tickets');
  await expect(page.locator('.tk-row', { hasText: 'Renew the api.example.com certificate' })).toBeVisible();
});

test('assign from triage; the ticket moves to its bot’s Tickets tab', async ({ page }) => {
  await go(page, '#/tickets/c42', { fresh: true });
  await expect(page.locator('.case-route')).toContainText('Not assigned yet');
  await page.locator('.tk-head [data-act="tk-set"][data-k="who"]').click();
  await page.selectOption('#f-who', 'web-triage');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.tk-head .fx')).toContainText('web-triage');
  await expect(page.locator('.tk-hist')).toContainText('Assigned');
  await go(page, '#/bots/web-triage/cases');
  await expect(page.locator('.dl-tabs a[aria-current="page"]')).toContainText('Tickets');
  await expect(page.locator('.tk-row', { hasText: 'Checkout end-to-end test times out' })).toBeVisible();
  await go(page, '#/tickets');
  await expect(page.locator('#tk-triage .tk-row')).toHaveCount(1);
});

test('a ticket shows its mirror outside conductor, its linked work, and its details change in place', async ({ page }) => {
  await go(page, '#/tickets/c29', { fresh: true });
  await expect(page.locator('.tk-ext')).toContainText('PagerDuty incident');
  await expect(page.locator('.tk-ext a', { hasText: 'Open in PagerDuty' })).toHaveAttribute('target', '_blank');
  await expect(page.locator('.tk-side')).toContainText('Replace the production web server');
  await page.locator('[data-act="tk-set"][data-k="priority"]').click();
  await page.selectOption('#f-priority', 'low');
  await page.locator('#gen-submit').click();
  await expect(page.locator('[data-act="tk-set"][data-k="priority"]')).toContainText('Low');
  await page.locator('[data-act="tk-unlink"]').click(); await page.locator('#cf-ok').click();
  await expect(page.locator('.tk-ext')).toContainText('Not linked');
  await page.locator('[data-act="tk-link"]').click();
  await page.selectOption('#f-repo', 'acme/infra');
  await page.locator('#gen-submit').click();
  await expect(page.locator('.tk-ext')).toContainText('acme/infra#');
});

test('make a ticket from a chat message; old task links open the ticket', async ({ page }) => {
  await go(page, '#/bots/platform-sre/chat', { fresh: true });
  await page.locator('#msgs .cm-op').first().hover();
  await page.locator('#msgs .cm-op').first().locator('[data-act="chat-ticket"]').click();
  await expect(page).toHaveURL(/#\/tickets\/new\?bot=platform-sre/);
  await expect(page.locator('#tk-who')).toHaveValue('platform-sre');
  await expect(page.locator('#tk-desc')).toHaveValue(/what the dashboard showed/);
  await expect(page.locator('#tk-title')).not.toHaveValue('');
  await page.locator('[data-act="tk-create"]').click();
  await expect(page.locator('.tk-chat')).toContainText('Made from a message in platform-sre’s chat');
  await go(page, '#/bots/platform-sre/cases/c29');
  await expect(page).toHaveURL(/#\/tickets\/c29$/);
});
