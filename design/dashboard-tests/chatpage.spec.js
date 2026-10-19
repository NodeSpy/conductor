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
  const team = list.locator('.cv-c', { hasText: 'team platform' });
  await expect(team.locator('.cv-stack .bav')).toHaveCount(1); // a team is its lead's face, large enough to know,
  await expect(team.locator('.cv-more')).toHaveText('+2'); // and how many more are on it
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

test('@mention: a list as you type, picked by keys or a click; a token Backspace takes whole; the To row follows', async ({ page }) => {
  await go(page, '#/chat/team/platform', { fresh: true });
  const say = page.locator('#say');
  await say.click(); await page.keyboard.type('Can @');
  const pop = page.locator('#mx-pop');
  await expect(pop.locator('.mx-o').first()).toContainText('@everyone');
  await expect(pop).toContainText('Atlas'); await expect(pop).toContainText('lead');
  await page.keyboard.type('sc');
  await expect(pop.locator('.mx-o')).toHaveCount(1);
  await page.keyboard.press('Enter'); // picks, doesn't send
  await expect(say).toHaveValue('Can @Scout ');
  await expect(page.locator('.cm-mirror .mx-tok')).toHaveText('@Scout');
  await expect(page.locator('.cv-to .cv-named')).toContainText('Scout');
  await page.keyboard.press('Backspace');
  await expect(say).toHaveValue('Can ');
  await expect(page.locator('.cv-to .cv-named')).toHaveCount(0);
  await expect(page.locator('.cv-to [data-v="lead"]')).toHaveAttribute('aria-pressed', 'true');
  // a click, and arrows, and Esc
  await page.keyboard.type('@'); await pop.locator('.mx-o', { hasText: '@everyone' }).click();
  await expect(say).toHaveValue('Can @everyone ');
  await expect(page.locator('.cv-to [data-v="all"]')).toHaveAttribute('aria-pressed', 'true');
  await page.keyboard.type('@'); await page.keyboard.press('ArrowDown'); await expect(pop.locator('.mx-o').nth(1)).toHaveClass(/on/);
  await page.keyboard.press('Escape'); await expect(pop).toHaveCount(0);
  await page.keyboard.type('Delta please'); await page.keyboard.press('Enter');
  await expect(page.locator('#msgs .cm-op').last().locator('.mx-tok').first()).toHaveText('@everyone');
});

test('pin and unpin a conversation; the Pinned heading only when something is pinned; it stays pinned', async ({ page }) => {
  const phone = page.viewportSize().width <= 720;
  await go(page, phone ? '#/chat' : '#/chat/bot/web-triage', { fresh: true });
  const pinned = page.locator('.cv-list .cv-g', { hasText: 'Pinned' });
  await page.locator('.cv-li[data-key="platform-sre"] .cv-pin').click({ force: true }); // unpin Atlas, the only pin
  await expect(pinned).toHaveCount(0);
  if (phone) { await page.locator('.cv-li[data-key="web-triage"] .cv-pin').click(); }
  else { await page.locator('.cv-th [data-act="cv-more"]').click(); await page.locator('#pop-menu [data-act="cv-pin"]').click(); } // pin Scout from the conversation's ⋯
  await expect(pinned).toHaveCount(1);
  await expect(page.locator('.cv-l').first().locator('.cv-li')).toHaveCount(1);
  await expect(page.locator('.cv-l').first()).toContainText('Scout');
  await page.reload(); await page.waitForSelector('.cv-list');
  await expect(page.locator('.cv-li[data-key="web-triage"]')).toHaveClass(/pinned/);
  // mark as unread from the conversation's ⋯ brings the count back
  await go(page, '#/chat/bot/web-triage');
  await page.locator('.cv-th [data-act="cv-more"]').click();
  await page.locator('#pop-menu [data-act="cv-unread"]').click();
  await expect(page.locator('.cv-li[data-key="web-triage"] .count.hot')).toHaveText('1');
});

test('one count badge: new or waiting is the orange badge everywhere (nav, tab bar, chat list, tabs); totals are plain text', async ({ page }) => {
  for (const route of ['#/chat', '#/bots/platform-sre/proposals', '#/teams/platform/routing', '#/overview']) {
    await go(page, route, { fresh: true });
    const r = await page.evaluate(() => { const bad = [], acc = getComputedStyle(document.querySelector('.count.hot') || document.body).backgroundColor;
      // anything drawn as a count pill must be the shared .count
      document.querySelectorAll('#main em, .nav em, #tabbar em, .cv-un:not(.count)').forEach((e) => { if (/^\d+$/.test(e.textContent.trim())) bad.push(`not the shared badge: ${e.outerHTML.slice(0, 60)}`); });
      document.querySelectorAll('.count').forEach((c) => { if (!c.offsetParent) return; const bg = getComputedStyle(c).backgroundColor; const hot = c.classList.contains('hot');
        if (hot && bg !== acc) bad.push(`a hot count in another colour: ${c.outerHTML.slice(0, 60)}`); if (!hot && bg !== 'rgba(0, 0, 0, 0)' && bg !== 'transparent') bad.push(`a total drawn as a badge: ${c.outerHTML.slice(0, 60)}`); });
      return bad; });
    expect(r, route).toEqual([]);
  }
  await go(page, '#/chat', { fresh: true });
  const nav = page.viewportSize().width <= 720 ? page.locator('#tabbar [data-nav="chat"] .count') : page.locator('#rail-nav [data-nav="chat"] .count');
  await expect(nav).toHaveClass(/hot/);
});

test('Chat reads as a page: the title like any page, the list and the conversation two panels, the search in the list', async ({ page }) => {
  test.skip(page.viewportSize().width <= 720, 'desktop layout');
  await go(page, '#/chat/team/platform', { fresh: true });
  const h = await page.evaluate(() => { const a = getComputedStyle(document.querySelector('.cv-page .dl-phead h1')); return [a.fontSize, a.fontFamily]; });
  await go(page, '#/activity');
  const ref = await page.evaluate(() => { const a = getComputedStyle(document.querySelector('#main h1')); return [a.fontSize, a.fontFamily]; });
  expect(h).toEqual(ref);
  await go(page, '#/chat/team/platform');
  for (const sel of ['.cv-list', '.cv-main']) expect(await page.locator(sel).evaluate((e) => getComputedStyle(e).borderTopWidth)).not.toBe('0px');
  await expect(page.locator('.cv-list #cv-q')).toBeVisible();
  // the pictures are big enough to know: 40 in the list, 36 on messages, 44 in the header
  expect(Math.round((await page.locator('.cv-li .bav').first().boundingBox()).width)).toBe(40);
  expect(Math.round((await page.locator('#msgs .cm-bot .bav').first().boundingBox()).width)).toBe(36);
  expect(Math.round((await page.locator('.cv-th .bav').first().boundingBox()).width)).toBe(44);
});
