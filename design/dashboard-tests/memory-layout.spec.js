// A bot's Memory tab, laid out for writing: the head is a title, a quiet repo line and a New… button that never
// collide; the entry takes the wide column (What it knows and History stack beside it); the markdown opens in Edit,
// fills the screen and wraps its lines; the footer says what saving does in words.
const { test, expect, go } = require('./fixtures');

const box = (l) => l.evaluate((e) => { const r = e.getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height, r: r.right, b: r.bottom }; });
const overlap = (a, b) => a.x < b.r - 0.5 && b.x < a.r - 0.5 && a.y < b.b - 0.5 && b.y < a.b - 0.5;

test('Memory: the sidebar head is a title, a quiet repo line and New…, none overlapping or cut', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
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
  const ta = page.locator('#rp-ed .ced-ta'), vh = page.viewportSize().height;
  expect((await box(ta)).h).toBeGreaterThan(vh * 0.55);
  // a long line wraps: nothing runs off the pane, the textarea never scrolls inside, the highlight keeps up
  await ta.click(); await page.keyboard.press('Control+End');
  await page.keyboard.type(`${'a long line that keeps going '.repeat(12)}\n`);
  const m = await ta.evaluate((t) => ({ sw: t.scrollWidth, cw: t.clientWidth, sh: t.scrollHeight, ch: t.clientHeight, hl: t.previousElementSibling.scrollHeight }));
  expect(m.sw).toBeLessThanOrEqual(m.cw + 1);
  expect(m.sh).toBeLessThanOrEqual(m.ch + 1);
  expect(Math.abs(m.hl - m.ch)).toBeLessThanOrEqual(2);
  await page.locator('[data-act="rp-mode"][data-v="split"]').click();
  await expect(page.locator('#rp-prev')).toBeVisible();
  const src = await box(page.locator('.ed-src')), prev = await box(page.locator('#rp-prev'));
  expect(Math.min(src.w, prev.w)).toBeGreaterThan(page.viewportSize().width <= 720 ? 280 : 300);
  const sw = await page.locator('#rp-ed .ced-ta').evaluate((t) => t.scrollWidth - t.clientWidth);
  expect(sw).toBeLessThanOrEqual(1);
  // the choice holds for the next entry
  await page.locator('.kind-entries [data-act="rp-open"]', { hasText: 'ALB' }).click();
  const discard = page.locator('.modal [data-act], .modal button', { hasText: 'Discard' });
  if (await discard.count()) await discard.first().click();
  await expect(page.locator('.ed-panes')).toHaveClass(/mode-split/);
});

test('Memory: the save footer says what saving does in words, no trailer jargon', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
  const sign = page.locator('.ed-sign');
  await expect(sign).toHaveText('Saving makes one commit in your name, marked as edited in the dashboard. Your own edits go straight in: they need no approval.');
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
