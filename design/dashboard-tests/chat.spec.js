// The bot chat (ledger item 19): a normal chat scroller with no gaps, tool calls folded into one quiet line, a full
// composer (attach, paste, drop, link cards, limits, Shift+Enter, queued until idle), attachments in the thread as
// data you gave it, and side cards in plain words.
const { test, expect, go } = require('./fixtures');

/** Every gap between consecutive rows of the thread, and the thread's own spacing token. */
const gaps = (page) => page.evaluate(() => { const box = document.querySelector('#msgs'), rows = [...box.children].filter((e) => e.offsetParent);
  const tok = parseFloat(getComputedStyle(box).rowGap) || 0, out = [];
  for (let i = 1; i < rows.length; i++) out.push(Math.round(rows[i].getBoundingClientRect().top - rows[i - 1].getBoundingClientRect().bottom));
  const last = rows[rows.length - 1].getBoundingClientRect(), b = box.getBoundingClientRect();
  return { tok, out, n: rows.length, tail: Math.round(b.bottom - last.bottom - parseFloat(getComputedStyle(box).paddingBottom)) }; });

test('no gap between messages at any count; the newest sits against the composer', async ({ page }) => {
  for (const bot of ['platform-sre', 'billing-sre', 'web-triage']) {
    await go(page, `#/bots/${bot}/chat`, { fresh: true });
    for (let k = 0; k < 3; k++) {
      const g = await gaps(page);
      for (const x of g.out) expect(x, `${bot}: gap between messages`).toBeLessThanOrEqual(g.tok + 1);
      if (!(await page.locator('#msgs .cm-empty').count())) expect(Math.abs(g.tail), `${bot}: the newest message is at the bottom`).toBeLessThanOrEqual(2);
      await page.fill('#say', `Message ${k + 1}`); await page.locator('#say').press('Enter');
      await expect(page.locator('#msgs .cm-bot').last()).toContainText(/Got it/);
    }
  }
});

test('consecutive tool calls are one quiet line that opens to each command and its output', async ({ page }) => {
  await go(page, '#/bots/platform-sre/chat', { fresh: true });
  const g = page.locator('.cm-tools');
  await expect(g).toHaveCount(2);
  await expect(g.first().locator('summary')).toHaveText('Started 2 read-only workers');
  await expect(g.nth(1).locator('summary')).toHaveText('Ran 2 commands');
  await expect(page.locator('.cm-cmd').first()).toBeHidden();
  await g.first().locator('summary').click();
  await expect(g.first().locator('.cm-cmd')).toHaveCount(2);
  await expect(g.first().locator('.cm-out').first()).toBeVisible();
  // the output scrolls in its own box, never widening the thread
  expect(await page.evaluate(() => { const b = document.querySelector('#msgs'); return b.scrollWidth <= b.clientWidth + 1; })).toBe(true);
});

test('attach, refuse with the reason, a link card, remove; send is queued until the bot is idle, then delivered', async ({ page }) => {
  await go(page, '#/bots/platform-sre/chat', { fresh: true });
  await expect(page.locator('[data-act="chat-attach"]')).toHaveAttribute('title', /up to 10 files, 20 MB each/);
  await page.setInputFiles('#cm-file', [{ name: 'trace.log', mimeType: 'text/plain', buffer: Buffer.from('GET /checkout 2.31s\n') }, { name: 'clip.mov', mimeType: 'video/quicktime', buffer: Buffer.alloc(8) }]);
  await expect(page.locator('#cm-pend .cm-p')).toHaveCount(1);
  await expect(page.locator('#cm-err')).toContainText('clip.mov wasn’t attached: only images, PDF and text files');
  await page.fill('#say', 'See https://grafana.example.com/d/api for the hour');
  await expect(page.locator('#cm-pend .cm-p', { hasText: 'grafana.example.com' })).toBeVisible();
  // Shift+Enter is a new line, not a send
  await page.locator('#say').press('End'); await page.locator('#say').press('Shift+Enter'); await page.locator('#say').type('and the log');
  await expect(page.locator('#say')).toHaveValue(/hour\nand the log/);
  // paste an image from the clipboard
  await page.evaluate(() => { const c = document.createElement('canvas'); c.width = 4; c.height = 4; return new Promise((r) => c.toBlob((b) => { const dt = new DataTransfer(); dt.items.add(new File([b], 'image.png', { type: 'image/png' }));
    document.querySelector('#say').dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })); r(); })); });
  await expect(page.locator('#cm-pend .cm-p', { hasText: 'Pasted image' })).toBeVisible();
  // remove the pasted image
  await page.locator('#cm-pend .cm-p', { hasText: 'Pasted image' }).locator('[data-act="chat-unattach"]').click();
  await expect(page.locator('#cm-pend .cm-p')).toHaveCount(2);
  await expect(page.locator('#cm-pend .cm-p', { hasText: 'trace.log' })).not.toHaveClass(/up/);
  await page.locator('[data-act="chat-send"]').click();
  const mine = page.locator('#msgs .cm-op').last();
  await expect(mine.locator('.cm-q')).toContainText('Queued: delivered when Atlas is idle');
  await expect(mine).toContainText('Delivered');
  await expect(mine.locator('.cm-given')).toHaveText('Data you gave it');
  await expect(mine.locator('.cm-file')).toContainText('trace.log');
  await expect(mine.locator('.cm-link')).toContainText('grafana.example.com');
  await expect(page.locator('#cm-pend .cm-p')).toHaveCount(0);
});

test('dropped files attach; an image in the thread opens larger', async ({ page }) => {
  await go(page, '#/bots/platform-sre/chat', { fresh: true });
  await page.evaluate(() => { const dt = new DataTransfer(); dt.items.add(new File(['a,b\n1,2\n'], 'costs.csv', { type: 'text/csv' }));
    const z = document.querySelector('#msgs'); z.dispatchEvent(new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true })); z.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true })); });
  await expect(page.locator('#cm-pend .cm-p', { hasText: 'costs.csv' })).toBeVisible();
  await page.locator('.cm-img').first().click();
  await expect(page.locator('.modal')).toContainText('p99-by-route.png');
  await expect(page.locator('.modal img.cm-big')).toBeVisible();
  await expect(page.locator('.modal')).toContainText('Data you gave platform-sre');
});

test('side cards and thread speak in words: no ids, no plan jargon, the approval inline', async ({ page }) => {
  await go(page, '#/bots/platform-sre/chat', { fresh: true });
  const side = await page.locator('.cm-side').evaluate((e) => e.textContent); // on a phone it's folded into a line of links (65); its words still hold
  const ids = await page.evaluate(() => [...D.proposals, ...D.cases].map((x) => x.id));
  for (const id of ids) expect(side, 'no ids').not.toMatch(new RegExp(`\\b${id}\\b`));
  expect(side).not.toMatch(/REPLACES|\+\d+ ~\d+ -\d+|operator:|event:/);
  if (page.viewportSize().width > 1100) await expect(page.locator('.cm-side .fx').first()).toBeVisible(); else await expect(page.locator('.cm-side-line')).toContainText('waiting for you');
  await expect(page.locator('.cm-card', { hasText: 'Replace the production web server' })).toHaveAttribute('href', '#/bots/platform-sre/proposals/p12');
  // the bot's identity on its messages (option A): its face and its name, and a rule in its colour
  await expect(page.locator('.cm-bot .bav svg').first()).toBeVisible();
  await expect(page.locator('.cm-bot .cm-meta').first()).toContainText('Atlas');
  await expect(page.locator('.cm-bot[data-bc="teal"]').first()).toBeVisible();
  // an incoming event is a solid card: source, a plain headline, severity on its edge, facts and links; raw text folded
  const ev = page.locator('.cm-event .cm-evc');
  await expect(ev).toHaveAttribute('data-sev', 'high');
  await expect(ev.locator('.cm-evh')).toContainText('PagerDuty incident');
  await expect(ev.locator('.cm-evt')).toHaveText('api p99 latency above 2s for 10 minutes');
  await expect(ev.locator('.cm-evt')).not.toContainText('pd.incident');
  await expect(ev.locator('.fx')).toContainText('SEV2'); await expect(ev.locator('.fx')).toContainText('acme/api');
  await expect(ev.locator('.fx-i.ln', { hasText: 'SEV2 api p99 latency' })).toHaveAttribute('data-fx-href', '#/tickets/c29');
  await expect(ev.locator('.cm-evnote')).toContainText('Data from PagerDuty: Atlas reads it, never takes orders from it');
  expect(parseFloat(await ev.locator('.cm-evnote').evaluate((e) => getComputedStyle(e).fontSize))).toBeGreaterThanOrEqual(13);
  await expect(ev.locator('.cm-evraw code')).toBeHidden();
  await page.locator('[data-act="chat-paseo"]').click();
  await expect(page.locator('.modal')).toContainText('paseo shows all of it');
  await expect(page.locator('.modal a.btn', { hasText: 'Open paseo' })).toHaveAttribute('target', '_blank');
});

test('phone: a full-height chat with the composer pinned in view', async ({ page }) => {
  test.skip(page.viewportSize().width > 720, 'phone only');
  await go(page, '#/bots/platform-sre/chat', { fresh: true });
  await page.evaluate(() => document.querySelector('.chat').scrollIntoView({ block: 'start', behavior: 'instant' }));
  await page.waitForTimeout(100);
  const r = await page.evaluate(() => { const c = document.querySelector('.chat').getBoundingClientRect(), k = document.querySelector('.cm-comp').getBoundingClientRect(), bar = document.querySelector('.tabbar, .mobile-nav, nav.bottom, #tabbar');
    return { h: c.height, vh: innerHeight, compBottom: k.bottom, barTop: bar ? bar.getBoundingClientRect().top : innerHeight }; });
  expect(r.h).toBeGreaterThan(r.vh * 0.5); // it fits what's left below the bot's head and tabs (65)
  expect(r.compBottom).toBeLessThanOrEqual(r.barTop + 1);
});

test('the composer is pinned to the bottom of a full-height panel: it stays put while you type, on empty, short and long threads', async ({ page }) => {
  for (const route of ['#/chat/bot/web-triage', '#/bots/billing-sre/chat', '#/chat/team/platform']) {
    await go(page, route, { fresh: true });
    await page.locator('#say').click(); await page.waitForTimeout(150); // focusing brings the panel into view on a phone
    const at = () => page.evaluate(() => { const c = document.querySelector('.cm-comp').getBoundingClientRect(), p = document.querySelector('.chat').getBoundingClientRect(), t = document.querySelector('#msgs').getBoundingClientRect(); return { comp: Math.round(c.bottom), panel: Math.round(p.height), pBottom: Math.round(p.bottom), thread: Math.round(t.height) }; });
    const a = await at();
    expect(Math.abs(a.comp - a.pBottom), `${route}: the composer is at the panel's bottom`).toBeLessThanOrEqual(2);
    await page.keyboard.type('one'); for (const l of ['two', 'three', 'four']) { await page.keyboard.press('Shift+Enter'); await page.keyboard.type(l); }
    const b = await at();
    expect(Math.abs(b.comp - a.comp), `${route}: the composer's bottom edge moved while typing`).toBeLessThanOrEqual(1);
    expect(b.panel, `${route}: the panel's height`).toBe(a.panel);
    expect(b.thread, `${route}: the thread shrinks as the composer grows`).toBeLessThan(a.thread);
  }
  // an empty thread shows its note in the middle
  await go(page, '#/chat/bot/web-triage', { fresh: true });
  const e = await page.evaluate(() => { const t = document.querySelector('#msgs').getBoundingClientRect(), n = document.querySelector('.cm-empty').getBoundingClientRect(); return Math.abs((n.top + n.bottom) / 2 - (t.top + t.bottom) / 2); });
  expect(e).toBeLessThan(4);
});

test('the composer: the send button is the field’s height and radius, on its bottom edge; the paperclip opens what you can attach', async ({ page }) => {
  for (const route of ['#/bots/platform-sre/chat', '#/tickets/c29']) {
    await go(page, route, { fresh: true });
    const [f, b] = await Promise.all([page.locator('.cm-field').first().boundingBox(), page.locator('.cm-send').first().boundingBox()]);
    expect(Math.abs(f.height - b.height), `${route}: same height`).toBeLessThanOrEqual(1);
    expect(Math.abs((f.y + f.height) - (b.y + b.height)), `${route}: same bottom edge`).toBeLessThanOrEqual(1);
    const [rf, rb] = await Promise.all([page.locator('.cm-field').first().evaluate((e) => getComputedStyle(e).borderTopLeftRadius), page.locator('.cm-send').first().evaluate((e) => getComputedStyle(e).borderTopLeftRadius)]);
    expect(rb, `${route}: same radius`).toBe(rf);
  }
  await go(page, '#/bots/platform-sre/chat', { fresh: true });
  await page.locator('[data-act="chat-attach"]').click();
  await expect(page.locator('#pop-menu [role="menuitem"]')).toHaveText(['Upload a file', 'Photo or image', 'Paste a link', 'From a vault item', 'Attach a run or ticket']);
  if (page.viewportSize().width <= 720) await expect(page.locator('#pop-menu')).toHaveClass(/as-sheet/);
  await page.locator('#pop-menu [data-act="cm-att-obj"]').click();
  await page.selectOption('#f-o', { index: 1 });
  await page.locator('#gen-submit').click();
  await expect(page.locator('#cm-pend .cm-p[data-kind="obj"]')).toContainText('a ticket');
  await page.locator('[data-act="chat-attach"]').click();
  await page.locator('#pop-menu [data-act="cm-att-ref"]').click();
  await page.selectOption('#f-ref', { index: 1 });
  await page.locator('#gen-submit').click();
  await expect(page.locator('#cm-pend .cm-p[data-kind="ref"]')).toContainText('its value is never shown');
});

// Item 65: every chat surface fits the screen: only the thread scrolls, the composer is always on it, and a resize refits
const fits = (page) => page.evaluate(() => {
  const main = document.getElementById('main'), comp = main.querySelector('.cm-comp'), box = main.querySelector('#msgs'), bar = document.getElementById('tabbar');
  const bottom = Math.min(innerHeight, bar && bar.offsetParent ? bar.getBoundingClientRect().top : innerHeight), r = comp.getBoundingClientRect();
  return { compBottom: Math.round(r.bottom), compTop: Math.round(r.top), bottom: Math.round(bottom), pageScrolls: main.scrollHeight - main.clientHeight > 1, threadScrolls: box ? box.scrollHeight > box.clientHeight : false };
});
for (const route of ['#/chat/bot/platform-sre', '#/chat/team/platform', '#/bots/platform-sre/chat', '#/tickets/c29']) {
  test(`fits the screen at three sizes, and refits on resize · ${route}`, async ({ page }) => {
    const phone = page.viewportSize().width <= 720, sizes = phone ? [[390, 844], [375, 667], [414, 740]] : [[1440, 900], [1280, 720], [1024, 640]];
    const ticket = route.startsWith('#/tickets');
    for (const [w, h] of sizes) {
      await page.setViewportSize({ width: w, height: h });
      await go(page, route, { fresh: true });
      if (ticket) await page.locator('.tk-chat .cm-comp').scrollIntoViewIfNeeded(); // a ticket's comments are part of its page
      const f = await fits(page);
      expect(f.compBottom, `${route} at ${w}×${h}: the composer is on screen`).toBeLessThanOrEqual(f.bottom + 1);
      expect(f.compTop, `${route} at ${w}×${h}: the composer is on screen`).toBeGreaterThanOrEqual(0);
      if (!ticket) expect(f.pageScrolls, `${route} at ${w}×${h}: only the thread scrolls`).toBe(false);
    }
    if (!ticket) { // a resize in place refits it
      const [w, h] = sizes[0]; await page.setViewportSize({ width: w, height: h - 140 }); await page.waitForTimeout(150);
      const f = await fits(page);
      expect(f.compBottom, `${route}: refits after a resize`).toBeLessThanOrEqual(f.bottom + 1);
      expect(f.pageScrolls).toBe(false);
    }
  });
}
