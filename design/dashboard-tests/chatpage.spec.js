// Chat as a main destination (items 20, 25: direction 1, accent A): bots as people and teams as group chats, in the
// nav on desktop and phone; a team's lead answers, or everyone, or the bot you @mention; every reply says who; the
// bot's identity (its face in its colour, its name) on its messages.
const { test, expect, go } = require('./fixtures');
const isPhone = (page) => page.viewportSize().width <= 720;

test('Chat is in the nav; bots are people and teams are groups', async ({ page }) => {
  await go(page, '#/overview', { fresh: true });
  const nav = isPhone(page) ? page.locator('#tabbar [data-nav="chat"]') : page.locator('#rail-nav [data-nav="chat"]');
  await expect(nav).toBeVisible();
  await nav.click();
  await expect(page).toHaveURL(/#\/chat/);
  const list = page.locator('.cv-list');
  await expect(list.locator('.cv-c', { hasText: 'Atlas' }).first().locator('.bav svg')).toBeVisible(); // a bot is a face and a name
  await expect(list.locator('.cv-c', { hasText: 'team platform' }).locator('.cv-stack .bav')).toHaveCount(3); // a team is its members' faces
  await expect(list.locator('.cv-c', { hasText: 'Scout' }).locator('.cv-un')).toHaveText('1');
  await page.fill('#cv-q', 'ledg');
  await expect(list.locator('.cv-c', { hasText: 'Ledger' }).first()).toBeVisible();
  await expect(list.locator('.cv-c', { hasText: 'Scout' })).toHaveCount(0);
});

test('a team chat: the lead answers by default; Everyone asks every bot; @name asks one; each reply says who', async ({ page }) => {
  await go(page, '#/chat/team/platform', { fresh: true });
  await expect(page.locator('.cv-th')).toContainText('Atlas leads');
  await expect(page.locator('#msgs .cm-bot', { hasText: 'Back to you, Atlas' }).locator('.cm-meta')).toContainText('Scout');
  await expect(page.locator('#msgs .cm-card')).toContainText('Replace the production web server'); // an approval in the thread
  const bots = () => page.locator('#msgs .cm-bot').count();
  let n = await bots();
  await page.fill('#say', 'Is the rollback safe right now?'); await page.locator('#say').press('Enter');
  await expect(page.locator('#msgs .cm-op').last()).toContainText('Delivered');
  await expect.poll(bots).toBe(n + 1);
  await expect(page.locator('#msgs .cm-bot').last().locator('.cm-meta')).toContainText('Atlas');
  n = await bots();
  await page.locator('[data-act="cv-to"][data-v="all"]').click();
  await page.fill('#say', 'Everyone: anything else changed today?'); await page.locator('#say').press('Enter');
  await expect.poll(bots).toBe(n + 3);
  n = await bots();
  await page.fill('#say', '@Delta are the pipelines behind?'); await page.locator('#say').press('Enter');
  await expect.poll(bots).toBe(n + 1);
  await expect(page.locator('#msgs .cm-bot').last().locator('.cm-meta')).toContainText('Delta');
});

test('opening a conversation clears its unread; a bot chat is the same conversation as its Chat tab', async ({ page }) => {
  await go(page, '#/chat/bot/web-triage', { fresh: true });
  if (!isPhone(page)) await expect(page.locator('.cv-c', { hasText: 'Scout' }).locator('.cv-un')).toHaveCount(0);
  await page.fill('#say', 'Ping from Chat'); await page.locator('#say').press('Enter');
  await expect(page.locator('#msgs .cm-op').last()).toContainText('Ping from Chat');
  await go(page, '#/bots/web-triage/chat');
  await expect(page.locator('#msgs .cm-op').last()).toContainText('Ping from Chat');
  await expect(page.locator('.chat a.btn', { hasText: 'Open in Chat' })).toHaveAttribute('href', '#/chat/bot/web-triage');
});

test('phone: the list, then the conversation on its own screen with Chat in its breadcrumb', async ({ page }) => {
  test.skip(!isPhone(page), 'phone only');
  await go(page, '#/chat', { fresh: true });
  await expect(page.locator('.cv-main')).toHaveCount(0);
  await page.locator('.cv-c', { hasText: 'team platform' }).click();
  await expect(page.locator('.cv-list')).toHaveCount(0);
  await expect(page.locator('.dl-crumbs')).toContainText('Chat');
  await expect(page.locator('#say')).toBeVisible();
});
