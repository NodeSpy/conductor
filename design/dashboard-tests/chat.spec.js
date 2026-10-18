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
      expect(Math.abs(g.tail), `${bot}: the newest message is at the bottom`).toBeLessThanOrEqual(2);
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
  await expect(page.locator('.cm-hint')).toContainText('Up to 10 files, 20 MB each');
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
  await expect(mine.locator('.cm-q')).toContainText('Queued: delivered when platform-sre is idle');
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
  const side = await page.locator('.cm-side').innerText();
  const ids = await page.evaluate(() => [...D.proposals, ...D.cases].map((x) => x.id));
  for (const id of ids) expect(side, 'no ids').not.toMatch(new RegExp(`\\b${id}\\b`));
  expect(side).not.toMatch(/REPLACES|\+\d+ ~\d+ -\d+|operator:|event:/);
  await expect(page.locator('.cm-side .fx').first()).toBeVisible();
  await expect(page.locator('.cm-card', { hasText: 'Replace the production web server' })).toHaveAttribute('href', '#/bots/platform-sre/proposals/p12');
  await expect(page.locator('.cm-bot .dl-bot-av').first()).toHaveText('ps'); // the bot's identity on its messages
  await expect(page.locator('.cm-event .cm-note')).toContainText('Untrusted data');
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
  expect(r.h).toBeGreaterThan(r.vh * 0.6);
  expect(r.compBottom).toBeLessThanOrEqual(r.barTop + 1);
});
