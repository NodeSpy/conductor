// A bot's Memory tab, laid out for writing: the head is a title, a quiet repo line and a New… button that never
// collide; the entry takes the wide column (What it knows and History stack beside it); the markdown opens in Edit,
// fills the screen and wraps its lines; the footer says what saving does in words.
const { test, expect, go } = require('./fixtures');

const box = (l) => l.evaluate((e) => { const r = e.getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height, r: r.right, b: r.bottom }; });
const overlap = (a, b) => a.x < b.r - 0.5 && b.x < a.r - 0.5 && a.y < b.b - 0.5 && b.y < a.b - 0.5;

test('Memory: the sidebar head is a title, a quiet repo line and New…, none overlapping or cut', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
  const back = page.locator('[data-act="rp-list"]'); if (await back.isVisible()) await back.click(); // a phone shows the list or the entry
  const head = page.locator('.repo-side .rp-side-head');
  const t = head.locator('h3'), n = head.locator('[data-act="rp-new"]'), repo = head.locator('.rp-side-repo');
  await expect(t).toHaveText('What it knows');
  await expect(n).toHaveText('New…');
  await expect(n).not.toHaveClass(/primary/);
  await expect(repo).toHaveText(/^\d+ entries · its own repo$/);
  const [bt, bn, br] = [await box(t), await box(n), await box(repo)];
  expect(overlap(bt, bn) || overlap(bt, br) || overlap(bn, br)).toBe(false);
  for (const l of [t, repo]) expect(await l.evaluate((e) => e.scrollWidth <= e.clientWidth + 1)).toBe(true);
  // Import lives in the New… sheet, not as a loose icon in the head
  await expect(head.locator('[data-act="rp-upload"]')).toHaveCount(0);
  await n.click();
  await page.locator('.modal [data-act="rp-upload"]').click();
  await expect(page.locator('.modal h2')).toHaveText('Import markdown');
});

test('Memory: the entry gets the wide column; History is not wider than the entry', async ({ page }) => {
  test.skip(page.viewportSize().width <= 720, 'desktop');
  await go(page, '#/bots/platform-sre/memory');
  const [s, e, h] = [await box(page.locator('.repo-side')), await box(page.locator('.repo-ed')), await box(page.locator('.repo-hist'))];
  expect(e.w).toBeGreaterThan(2 * s.w);
  expect(e.w).toBeGreaterThan(2 * h.w);
  expect(Math.abs(h.x - s.x)).toBeLessThan(1); // History sits under What it knows
  expect(h.y).toBeGreaterThan(s.b - 1);
});

test('Memory: the markdown opens in Edit, fills the screen and wraps; Split shows two readable panes', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
  await expect(page.locator('[data-act="rp-mode"][data-v="edit"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.ed-panes')).toHaveClass(/mode-edit/);
  await expect(page.locator('#rp-prev')).toBeHidden();
  // the editor is the height of the entry's scrolling body (less its label), so scrolled to, it fills the view
  const ta = page.locator('#rp-ed .ced-ta'), body = await box(page.locator('.repo-ed > .ed-body'));
  expect((await box(ta)).h).toBeGreaterThan(Math.max(150, body.h - 90));
  // a long line wraps: nothing runs off the pane; the editor scrolls inside itself and the highlight scrolls with it
  await ta.click(); await page.keyboard.press('Control+End');
  await page.keyboard.type(`${'a long line that keeps going '.repeat(40)}\n`);
  const m = await ta.evaluate((t) => { const hl = t.previousElementSibling; return { sw: t.scrollWidth, cw: t.clientWidth, sh: t.scrollHeight, hsh: hl.scrollHeight, top: t.scrollTop, htop: hl.scrollTop, hcw: hl.clientWidth }; });
  expect(m.sw).toBeLessThanOrEqual(m.cw + 1);
  expect(m.hcw).toBe(m.cw); // both layers wrap at the same width (the scrollbar's gutter is kept in both)
  expect(Math.abs(m.hsh - m.sh)).toBeLessThanOrEqual(2);
  expect(m.top).toBeGreaterThan(0); expect(Math.abs(m.htop - m.top)).toBeLessThanOrEqual(1);
  await page.locator('[data-act="rp-mode"][data-v="split"]').click();
  await expect(page.locator('#rp-prev')).toBeVisible();
  const src = await box(page.locator('.ed-src')), prev = await box(page.locator('#rp-prev'));
  expect(Math.min(src.w, prev.w)).toBeGreaterThan(page.viewportSize().width <= 720 ? 280 : 300);
  const sw = await page.locator('#rp-ed .ced-ta').evaluate((t) => t.scrollWidth - t.clientWidth);
  expect(sw).toBeLessThanOrEqual(1);
  // the choice holds for the next entry
  const back = page.locator('[data-act="rp-list"]'); if (await back.isVisible()) await back.click();
  await page.locator('.kind-entries [data-act="rp-open"]', { hasText: 'ALB' }).click();
  const discard = page.locator('.modal [data-act], .modal button', { hasText: 'Discard' });
  if (await discard.count()) await discard.first().click();
  await expect(page.locator('.ed-panes')).toHaveClass(/mode-split/);
});

test('Memory: the save footer says what saving does in words, no trailer jargon', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
  const sign = page.locator('.ed-sign');
  await expect(sign).toHaveText('Saving makes one commit in your name, marked as edited here. Your own edits need no approval.');
  await expect(page.locator('.repo-ed')).not.toContainText(/Authored-By|Edited-Via|provenance|operator:ops/);
});

// The editor types into a transparent textarea under a highlighted copy: both layers use one font and start their text
// at the same place, so the caret and a selection sit on the letters (they drifted by a pixel a line, 4px across).
test('every code editor: the highlighted text and the textarea line up', async ({ page }) => {
  for (const r of ['#/bots/platform-sre/memory', '#/config/raw']) {
    await go(page, r);
    const m = await page.locator('.ced').first().evaluate((c) => { const ta = c.querySelector('.ced-ta'), w = document.createTreeWalker(c.querySelector('.ln'), NodeFilter.SHOW_TEXT), t = w.nextNode(), rg = document.createRange(); rg.setStart(t, 0); rg.setEnd(t, 1);
      const a = ta.getBoundingClientRect(), hs = getComputedStyle(c.querySelector('.ced-hl')), ts = getComputedStyle(ta);
      return { dx: rg.getBoundingClientRect().left - a.left - parseFloat(ts.paddingLeft), font: [hs.fontSize, hs.lineHeight, hs.fontFamily].join() === [ts.fontSize, ts.lineHeight, ts.fontFamily].join() }; });
    expect(m.font, r).toBe(true);
    expect(Math.abs(m.dx), r).toBeLessThan(1);
  }
});

// 69b: the page fits the screen like a chat (65): it never scrolls, the Save bar is always on screen, and the rail and
// the entry's body scroll inside themselves. On a phone it is the list or the entry, each filling the screen.
for (const [w, h] of [[1440, 900], [1280, 720], [390, 844]]) {
  test(`Memory fits the viewport at ${w}×${h}: the page never scrolls, Save is on screen, the columns scroll themselves`, async ({ page }) => {
    test.skip((page.viewportSize().width <= 720) !== (w <= 720), 'sized per project');
    await page.setViewportSize({ width: w, height: h });
    await go(page, '#/bots/platform-sre/memory');
    await page.waitForTimeout(100);
    const fits = () => page.evaluate(() => { const main = document.getElementById('main'), se = document.scrollingElement; return main.scrollHeight <= main.clientHeight + 1 && se.scrollHeight <= se.clientHeight + 1; });
    const onScreen = async (sel) => { const b = await box(page.locator(sel)); return b.h > 0 && b.y >= 0 && b.b <= h; };
    expect(await fits()).toBe(true);
    expect(await onScreen('[data-act="rp-save"]')).toBe(true);
    expect(await onScreen('.ed-sign')).toBe(true);
    const body = page.locator('.repo-ed > .ed-body');
    expect(await body.evaluate((e) => e.scrollHeight > e.clientHeight && getComputedStyle(e).overflowY === 'auto')).toBe(true);
    await body.evaluate((e) => { e.scrollTop = e.scrollHeight; });
    expect(await onScreen('[data-act="rp-save"]')).toBe(true);
    expect(await fits()).toBe(true);
    // a redraw (Split) keeps the body where it was scrolled to
    const split = page.locator('[data-act="rp-mode"][data-v="split"]');
    await split.scrollIntoViewIfNeeded(); // where the click would scroll it to anyway
    const before = await body.evaluate((e) => e.scrollTop);
    expect(before).toBeGreaterThan(40);
    await split.click();
    await expect.poll(async () => Math.abs((await page.locator('.repo-ed > .ed-body').evaluate((e) => e.scrollTop)) - before)).toBeLessThan(40);
    if (w > 720) {
      const rail = page.locator('.rp-rail');
      expect(await rail.evaluate((e) => getComputedStyle(e).overflowY === 'auto' && e.clientHeight > 200)).toBe(true);
      await expect(page.locator('.rp-rail .repo-hist')).toHaveCount(1);
      await expect(page.locator('[data-act="rp-list"]')).toBeHidden();
    } else {
      await expect(page.locator('.rp-rail')).toBeHidden();
      await page.locator('[data-act="rp-list"]').click();
      await expect(page.locator('.rp-rail')).toBeVisible();
      await expect(page.locator('.repo-ed')).toBeHidden();
      expect(await fits()).toBe(true);
      await page.locator('.kind-entries [data-act="rp-open"]', { hasText: 'ALB' }).click();
      await expect(page.locator('.repo-ed .ed-head')).toContainText('ALB');
      await expect(page.locator('.rp-rail')).toBeHidden();
      expect(await onScreen('[data-act="rp-save"]')).toBe(true);
    }
  });
}

// 70: Who wrote this opened in place of the whole editor, with no clear way back. It is a popover under its button: the
// entry stays put, the button reads as pressed, and ✕, Esc and a click elsewhere each close it.
test('Who wrote this is a popover over the entry, never in its place, and closes three ways', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
  const btn = page.locator('[data-act="rp-blame"]'), pop = page.locator('#rp-blame-pop');
  const open = async () => { await btn.click(); await expect(pop).toBeVisible(); await expect(btn).toHaveAttribute('aria-pressed', 'true'); await expect(btn).toHaveClass(/\bon\b/); };
  await open();
  await expect(pop.locator('.bl-group').first()).toContainText('Atlas');
  await expect(page.locator('#ke-form')).toBeVisible(); // the entry is still there, under it
  await expect(page.locator('[data-act="rp-save"]')).toBeVisible();
  const pb = await box(pop), bb = await box(btn);
  expect(pb.y).toBeGreaterThanOrEqual(bb.b - 1); expect(pb.y - bb.b).toBeLessThan(24); // anchored just under its button
  expect(pb.b).toBeLessThanOrEqual((await box(page.locator('.repo-ed > .ed-foot'))).y); // and ends above the Save bar
  await page.keyboard.press('Escape');
  await expect(pop).toHaveCount(0); await expect(btn).toHaveAttribute('aria-pressed', 'false'); await expect(btn).toBeFocused();
  await open();
  await pop.locator('[data-act="rp-blame-x"]').click();
  await expect(pop).toHaveCount(0); await expect(btn).toHaveAttribute('aria-pressed', 'false');
  await open();
  await page.locator('.repo-ed .ed-head b').click();
  await expect(pop).toHaveCount(0); await expect(btn).not.toHaveClass(/\bon\b/);
  await open();
  await btn.click(); // the button toggles it too
  await expect(pop).toHaveCount(0);
});
