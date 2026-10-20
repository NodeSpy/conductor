// A bot's Memory tab, laid out for writing: the head is a title, a quiet repo line and a New… button that never
// collide; the entry takes the wide column (What it knows and History stack beside it); the markdown is written in the
// plain GitHub-style editor; the footer says what saving does in words.
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

// The markdown editor is reset to the GitHub comment-box pattern (superseding 69d–69h's hand-rolled split and gutter):
// Write | Preview tabs, a plain monospace textarea with no line numbers, a small toolbar, Side by side only as an
// option, and the same quiet focus ring as every other field, never the accent. One editor wherever markdown is
// written.
for (const route of ['#/bots/platform-sre/memory', '#/teams/platform/knowledge']) {
  test(`the markdown editor is the plain comment box: Write | Preview, a textarea, no line numbers, a field's focus: ${route}`, async ({ page }) => {
    await go(page, route);
    const ed = page.locator('.md-ed').first(), ta = ed.locator('textarea.md-ta');
    await expect(ed.locator('.md-tab[aria-selected="true"]')).toHaveText('Write');
    await expect(ed.locator('.md-tab')).toHaveText(['Write', 'Preview']);
    await expect(ta).toBeVisible();
    await expect(ed.locator('.ced, .ln, .ced-hl')).toHaveCount(0); // a plain textarea: no overlay, no line numbers
    expect(await ta.evaluate((t) => /mono/i.test(getComputedStyle(t).fontFamily))).toBe(true);
    for (const f of ['bold', 'italic', 'list', 'link', 'code']) await expect(ed.locator(`.md-tool[data-act="md-fmt"][data-f="${f}"]`)).toHaveCount(1);
    await expect(ed.locator('.md-tool[data-act="md-attach"]')).toHaveCount(1);
    await expect(ed.locator('.md-tool[aria-label="Side by side"]')).toHaveAttribute('aria-pressed', 'false'); // an option, not the default
    // focus: the box takes the same ring as any other field, and it is not the accent
    const inp = page.locator('#ke-form .input').first(); await inp.focus();
    const settled = (l) => expect.poll(() => l.evaluate((e) => getComputedStyle(e).borderTopColor), { intervals: [100, 100, 200, 300] }); // fields ease their border colour
    let field = ''; await expect.poll(async () => { const a = await inp.evaluate((e) => getComputedStyle(e).borderTopColor); const same = a === field; field = a; return same; }, { intervals: [150, 150, 300] }).toBe(true);
    await ta.focus();
    await settled(ed).toBe(field);
    const c = await ed.evaluate((e) => ({ border: getComputedStyle(e).borderTopColor, accent: getComputedStyle(document.querySelector('.btn.primary') || document.body).backgroundColor, outline: getComputedStyle(e.querySelector('.md-ta')).outlineStyle }));
    expect(c.border).not.toBe(c.accent);
    expect(c.outline).toBe('none');
    // Preview shows the rendered markdown in place of the textarea; Write brings it back
    await ed.locator('.md-tab[data-v="preview"]').click();
    await expect(ta).toBeHidden(); await expect(ed.locator('.md-prev')).toBeVisible();
    await expect(ed.locator('.md-tools [data-act="md-fmt"]').first()).toBeHidden(); // nothing to format in a preview
    await ed.locator('.md-tab[data-v="write"]').click();
    await expect(ta).toBeVisible(); await expect(ed.locator('.md-prev')).toBeHidden();
  });
}

test('the markdown editor: the toolbar and its keys, [[ suggestions, files by choosing, Side by side, sized to its text', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory', { fresh: true });
  const ed = page.locator('.md-ed'), ta = page.locator('#rp-md');
  const h0 = (await box(ta)).h;
  expect(await ta.evaluate((t) => t.scrollHeight <= t.clientHeight + 2)).toBe(true); // as tall as its text
  await ta.fill('one two');
  await ta.evaluate((t) => t.setSelectionRange(4, 7));
  await ed.locator('[data-f="bold"]').click();
  await expect(ta).toHaveValue('one **two**');
  await ta.evaluate((t) => { t.value = 'plain'; t.setSelectionRange(0, 5); });
  await ta.press(process.platform === 'darwin' ? 'Meta+i' : 'Control+i');
  await expect(ta).toHaveValue('_plain_');
  await ta.evaluate((t) => { t.value = 'a\nb'; t.setSelectionRange(0, 3); });
  await ed.locator('[data-f="list"]').click();
  await expect(ta).toHaveValue('- a\n- b');
  expect((await box(ta)).h).toBeLessThan(h0 + 1); // a short text is a short box
  // [[ suggests the other entries: narrow, then Enter puts the path in
  await ta.press('End'); await ta.type('\nSee [[lat');
  await expect(ed.locator('.md-ac .md-ac-i').first()).toContainText('Latency');
  await ta.press('Enter');
  await expect(ta).toHaveValue(/See \[\[runbooks\/latency\]\]$/);
  await expect(ed.locator('.md-ac')).toHaveCount(0);
  // files through the chat's attach rules: an image goes in as markdown and shows in the preview; a type it can't take is refused in words
  await page.setInputFiles('#rp-md-file', [{ name: 'graph.png', mimeType: 'image/png', buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64') }, { name: 'x.exe', mimeType: 'application/x-msdownload', buffer: Buffer.from('MZ') }]);
  await expect(ta).toHaveValue(/!\[graph\.png\]\(attachment:att_[0-9a-f]+\)\n$/);
  await expect(ed.locator('.md-err')).toContainText('x.exe wasn’t attached');
  // Side by side: both at once, preview kept up to date
  await ed.locator('.md-tool[aria-label="Side by side"]').click();
  await expect(ed).toHaveClass(/mode-split/);
  await expect(ta).toBeVisible(); await expect(ed.locator('.md-prev img.md-img')).toBeVisible();
  await ta.press('End'); await ta.type('\n**fresh**');
  await expect(ed.locator('.md-prev')).toContainText('fresh');
  // it grows as you type and scrolls inside itself past its cap
  await ta.evaluate((t) => { t.value += '\nline'.repeat(80); t.dispatchEvent(new Event('input', { bubbles: true })); });
  const m = await ta.evaluate((t) => ({ ch: t.clientHeight, sh: t.scrollHeight }));
  expect(m.sh).toBeGreaterThan(m.ch); expect(m.ch).toBeLessThanOrEqual(561);
});

test('Memory: the save footer says what saving does in words, no trailer jargon', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
  const sign = page.locator('.ed-sign');
  await expect(sign).toHaveText('Saving makes one commit in your name, marked as edited here. Your own edits need no approval.');
  await expect(page.locator('.repo-ed')).not.toContainText(/Authored-By|Edited-Via|provenance|operator:ops/);
});

// The code editors (YAML) type into a transparent textarea under a highlighted copy: both layers use one font and start
// their text at the same place, so the caret and a selection sit on the letters (they drifted by a pixel a line, 4px across).
test('every code editor: the highlighted text and the textarea line up', async ({ page }) => {
  for (const r of ['#/config/raw']) {
    await go(page, r);
    const m = await page.locator('.ced').first().evaluate((c) => { const ta = c.querySelector('.ced-ta'), w = document.createTreeWalker(c.querySelector('.ln'), NodeFilter.SHOW_TEXT), t = w.nextNode(), rg = document.createRange(); rg.setStart(t, 0); rg.setEnd(t, 1);
      const a = ta.getBoundingClientRect(), hs = getComputedStyle(c.querySelector('.ced-hl')), ts = getComputedStyle(ta);
      return { dx: rg.getBoundingClientRect().left - a.left - parseFloat(ts.paddingLeft), font: [hs.fontSize, hs.lineHeight, hs.fontFamily].join() === [ts.fontSize, ts.lineHeight, ts.fontFamily].join() }; });
    expect(m.font, r).toBe(true);
    expect(Math.abs(m.dx), r).toBeLessThan(1);
  }
});

// 69b + 69c: a bot's memory fits its content and never manufactures height. A short entry is a short card with the
// Save bar right under it and nothing to scroll; a long one scrolls the page with the Save bar stuck to the bottom of
// the screen, landing right under the card at the end. The rail is capped at the screen and scrolls by itself. On a
// phone it is the list or the entry.
const geom = (page) => page.evaluate(() => { const main = document.getElementById('main'), card = document.querySelector('.repo-ed'), bar = document.querySelector('.repo-ed > .ed-bar'), rail = document.querySelector('.rp-rail'), mr = main.getBoundingClientRect();
  const r = (e) => e.getBoundingClientRect(); const cb = r(card).bottom + main.scrollTop - mr.top, rb = rail && rail.offsetParent ? r(rail).bottom + main.scrollTop - mr.top : 0;
  return { sh: main.scrollHeight, ch: main.clientHeight, mainBottom: mr.bottom, cardBottom: r(card).bottom, barBottom: r(bar).bottom, barTop: r(bar).top, contentEnd: Math.max(cb, rb) }; });
for (const [w, h] of [[1440, 900], [1280, 720], [390, 844]]) {
  test(`Memory at ${w}×${h}: a long entry scrolls the page with Save stuck to the screen's bottom, and no made-up height`, async ({ page }) => {
    test.skip((page.viewportSize().width <= 720) !== (w <= 720), 'sized per project');
    await page.setViewportSize({ width: w, height: h });
    await go(page, '#/bots/platform-sre/memory');
    await page.waitForTimeout(100);
    let g = await geom(page);
    expect(g.cardBottom).toBeGreaterThan(g.mainBottom); // db-prod-1's form is taller than this screen
    expect(g.sh).toBeGreaterThan(g.ch); // so the page scrolls
    expect(g.sh - g.contentEnd).toBeLessThanOrEqual(64); // by no more than its content (plus the page's own padding)
    expect(g.barBottom).toBeLessThanOrEqual(g.mainBottom + 1); expect(g.barBottom).toBeGreaterThan(g.mainBottom - 3); // stuck to the bottom of the screen
    await page.evaluate(() => { const m = document.getElementById('main'); m.scrollTo({ top: m.scrollHeight, behavior: 'instant' }); });
    await page.waitForTimeout(100);
    g = await geom(page);
    expect(Math.abs(g.barBottom - g.cardBottom)).toBeLessThanOrEqual(2); // at the end it is the card's own foot
    expect(g.barBottom).toBeLessThanOrEqual(g.mainBottom + 1);
    if (w > 720) {
      const rail = page.locator('.rp-rail');
      expect(await rail.evaluate((e) => { const c = getComputedStyle(e); return c.overflowY === 'auto' && c.position === 'sticky' && e.getBoundingClientRect().height <= document.getElementById('main').clientHeight; })).toBe(true);
      await expect(page.locator('.rp-rail .repo-hist')).toHaveCount(1);
      await expect(page.locator('[data-act="rp-list"]')).toBeHidden();
    } else {
      await expect(page.locator('.rp-rail')).toBeHidden();
      await page.locator('[data-act="rp-list"]').click();
      await expect(page.locator('.rp-rail')).toBeVisible();
      await expect(page.locator('.repo-ed')).toBeHidden();
      await page.locator('.kind-entries [data-act="rp-open"]', { hasText: 'ALB' }).click();
      await expect(page.locator('.repo-ed .ed-head')).toContainText('ALB');
      await expect(page.locator('.rp-rail')).toBeHidden();
      g = await geom(page);
      expect(g.barBottom).toBeLessThanOrEqual(g.mainBottom + 1);
    }
  });
}

test('Memory: a short entry on a tall screen is a short card, Save right under it, nothing to scroll', async ({ page }) => {
  test.skip(page.viewportSize().width <= 720, 'desktop');
  await page.setViewportSize({ width: 1440, height: 1300 });
  await go(page, '#/bots/platform-sre/memory');
  await page.locator('.kind-entries [data-act="rp-open"]', { hasText: 'ALB' }).click();
  await page.waitForTimeout(100);
  const g = await geom(page);
  expect(g.cardBottom).toBeLessThan(g.mainBottom - 40); // a short card, ending well above the bottom
  expect(Math.abs(g.barBottom - g.cardBottom)).toBeLessThanOrEqual(2); // its Save bar is its foot, not floating below
  expect(g.sh).toBeLessThanOrEqual(g.ch + 1); // and the page has nothing to scroll
  // the three-line markdown is a few lines tall, not a box
  const ta = await page.locator('#rp-md').evaluate((t) => ({ h: t.clientHeight, sh: t.scrollHeight }));
  expect(ta.sh).toBeLessThanOrEqual(ta.h + 2); expect(ta.h).toBeLessThanOrEqual(160);
});

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
  expect(pb.b).toBeLessThanOrEqual((await box(page.locator('.repo-ed > .ed-bar'))).y + 1); // and ends above the Save bar
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
