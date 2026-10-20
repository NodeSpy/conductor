// A bot's Memory tab, laid out for writing: the head is a title, a quiet repo line and a New… button that never
// collide; the entry takes the wide column (What it knows and History stack beside it); the markdown opens in Split,
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

// 69d supersedes 69's Edit default: the markdown opens in Split, two readable panes.
test('Memory: the markdown opens in Split (two readable panes), is as tall as its text, grows, wraps and caps', async ({ page }) => {
  await go(page, '#/bots/platform-sre/memory');
  await expect(page.locator('[data-act="rp-mode"][data-v="split"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.ed-panes')).toHaveClass(/mode-split/);
  await expect(page.locator('#rp-prev')).toBeVisible();
  const src = await box(page.locator('.ed-src')), prev = await box(page.locator('#rp-prev'));
  expect(Math.min(src.w, prev.w)).toBeGreaterThan(page.viewportSize().width <= 720 ? 280 : 300);
  expect(await page.locator('#rp-ed .ced-ta').evaluate((t) => t.scrollWidth - t.clientWidth)).toBeLessThanOrEqual(1);
  await page.locator('[data-act="rp-mode"][data-v="edit"]').click();
  await expect(page.locator('#rp-prev')).toBeHidden();
  // the editor is as tall as its text: a few lines at least, growing as you type
  const ta = page.locator('#rp-ed .ced-ta'), h0 = (await box(ta)).h;
  const text = await ta.evaluate((t) => [...t.previousElementSibling.querySelectorAll('.ln')].reduce((n, l) => n + l.getBoundingClientRect().height, 0)); // its lines as wrapped
  expect(h0).toBeGreaterThan(90); expect(h0).toBeLessThanOrEqual(Math.max(5 * 21, text) + 24);
  await ta.click(); await page.keyboard.press('Control+End'); await page.keyboard.type('one more\ntwo more\nthree more\n');
  expect((await box(ta)).h).toBeGreaterThan(h0 + 40);
  // a long line wraps: nothing runs off the pane; past its cap the editor scrolls inside itself, the highlight with it
  await ta.click(); await page.keyboard.press('Control+End');
  await page.keyboard.type(`${'a long line that keeps going '.repeat(90)}\n`);
  const m = await ta.evaluate((t) => { const hl = t.previousElementSibling; return { sw: t.scrollWidth, cw: t.clientWidth, sh: t.scrollHeight, hsh: hl.scrollHeight, top: t.scrollTop, htop: hl.scrollTop, hcw: hl.clientWidth }; });
  expect(m.sw).toBeLessThanOrEqual(m.cw + 1);
  expect(m.hcw).toBe(m.cw); // both layers wrap at the same width (the scrollbar's gutter is kept in both)
  expect(Math.abs(m.hsh - m.sh)).toBeLessThanOrEqual(2);
  expect(m.top).toBeGreaterThan(0); expect(Math.abs(m.htop - m.top)).toBeLessThanOrEqual(1);
  expect(await ta.evaluate((t) => t.clientHeight)).toBeLessThanOrEqual(561); // capped
  // the choice holds for the next entry
  const back = page.locator('[data-act="rp-list"]'); if (await back.isVisible()) await back.click();
  await page.locator('.kind-entries [data-act="rp-open"]', { hasText: 'ALB' }).click();
  const discard = page.locator('.modal [data-act], .modal button', { hasText: 'Discard' });
  if (await discard.count()) await discard.first().click();
  await expect(page.locator('.ed-panes')).toHaveClass(/mode-edit/);
});

// 69d: the editor and its preview read as one unit, set apart from the form fields above: a bordered container with its
// own bar (holding the Edit/Split/Preview switch), a code-tinted Markdown pane, a divider, and the Preview on the reading
// surface; each pane labelled. 69f (over 69e): the line numbers stay, in a gutter of its own with a full-height hairline
// rule before the text, muted and right-aligned, each on its line's first row; an easy line height. 69g: the gutter is
// only as wide as its numbers (two digits' width up to four), the numbers close against the rule and the text close
// after it. Every markdown body goes through it (a team's knowledge too).
for (const route of ['#/bots/platform-sre/memory', '#/teams/platform/knowledge']) {
  test(`the markdown editor is one distinct unit, its line numbers in a ruled gutter: ${route}`, async ({ page }) => {
    await go(page, route);
    const unit = page.locator('.md-unit').first();
    await expect(unit).toBeVisible();
    await expect(unit.locator('.md-bar [data-act="rp-mode"]')).toHaveCount(3); // the switch lives in the unit
    await expect(unit.locator('.ed-src .md-pl')).toHaveText('Markdown');
    await expect(unit.locator('.ed-pv .md-pl')).toHaveText('Preview');
    // the bar's help reads as help: plain words, the syntax in a code chip, never a raw token on its own
    await expect(unit.locator('.md-hint')).toHaveText('Link another entry with [[name]]');
    await expect(unit.locator('.md-hint code.md-chip')).toHaveText('[[name]]');
    const c = await unit.evaluate((u) => { const cs = (e, p) => getComputedStyle(e, p), src = u.querySelector('.ed-src'), pv = u.querySelector('.ed-pv'), input = document.querySelector('#ke-form .input'), ced = u.querySelector('.ced'), ta = ced.querySelector('.ced-ta'), ln = ced.querySelector('.ced-hl .ln');
      // the pane's background: gutter colour to 44px, the hairline at 44–45px, then the code tint (the stops of one gradient)
      const bg = cs(src).backgroundImage, stops = bg.match(/(rgba?|color)\([^)]*\)/g) || [];
      const num = cs(ln, '::before'), w = document.createTreeWalker(ln, NodeFilter.SHOW_TEXT), t = w.nextNode(), rg = document.createRange(); rg.setStart(t, 0); rg.setEnd(t, 1);
      const left = ced.getBoundingClientRect().left;
      const gw = parseFloat(cs(src).getPropertyValue('--gw')), cx = document.createElement('canvas').getContext('2d'); cx.font = `${cs(ta).fontSize} ${cs(ta).fontFamily}`;
      return { gw, ch: cx.measureText('0').width, lines: ta.value.split('\n').length, border: cs(u).borderTopStyle, radius: parseFloat(cs(u).borderTopLeftRadius), stops, rule: bg.includes(`${gw}px, `) && bg.includes(`${gw + 1}px`), fullHeight: Math.abs(src.getBoundingClientRect().height - src.clientHeight - 0) < 3,
        preview: cs(pv).backgroundColor, input: input ? cs(input).backgroundColor : null, divider: cs(src).borderRightStyle, stacked: pv.getBoundingClientRect().top >= src.getBoundingClientRect().bottom - 1, pvTop: cs(pv).borderTopStyle,
        numContent: num.content, numAlign: num.textAlign, numColor: num.color, textColor: cs(ln).color, numTop: num.top, numBox: num.boxSizing, numEnd: parseFloat(num.width) - parseFloat(num.paddingRight), textX: rg.getBoundingClientRect().left - left, taX: parseFloat(cs(ta).paddingLeft),
        lh: parseFloat(cs(ta).lineHeight) / parseFloat(cs(ta).fontSize), hlLh: cs(ta.previousElementSibling).lineHeight, taLh: cs(ta).lineHeight }; });
    expect(c.border).toBe('solid'); expect(c.radius).toBeGreaterThan(0);
    // gutter, hairline, code (each two-position stop computes as two): the rule runs the pane's full height
    const [gutter, rule, code] = [c.stops[0], c.stops[2], c.stops[c.stops.length - 1]];
    expect(c.rule).toBe(true);
    expect(gutter).not.toBe(code); expect(rule).not.toBe(gutter); expect(rule).not.toBe(code); // a gutter of its own, a visible rule
    expect(code).not.toBe(c.preview); // the markdown pane is code-tinted; the preview is the reading surface
    expect(code).not.toBe(c.input); // and both read differently from the form's own fields
    expect(c.stacked ? c.pvTop : c.divider).toBe('solid'); // a divider between the panes (on top of the preview when stacked)
    expect(c.numContent).not.toBe('none'); expect(c.numAlign).toBe('right'); expect(c.numColor).not.toBe(c.textColor); // numbers kept, right-aligned, muted
    expect(c.numTop).toBe('0px'); // on its line's first row, however far the line wraps
    expect(c.lines).toBeLessThan(100);
    expect(c.gw).toBeLessThanOrEqual(2 * c.ch + 20); // two digits' width and room either side, no wide empty band
    // clear air: number · gap · rule · gap · text (the number's box is border-box, so its padding is inside the gutter)
    expect(c.numBox).toBe('border-box');
    expect(c.gw - c.numEnd).toBeGreaterThanOrEqual(8); expect(c.gw - c.numEnd).toBeLessThanOrEqual(12);
    expect(c.textX - c.gw - 1).toBeGreaterThanOrEqual(12); expect(c.textX - c.gw - 1).toBeLessThanOrEqual(16);
    expect(Math.abs(c.textX - c.taX)).toBeLessThan(1); // the highlighted copy and the textarea start together
    expect(c.lh).toBeGreaterThanOrEqual(1.7); expect(c.hlLh).toBe(c.taLh);
    // the gutter grows with the numbers (three digits past 99 lines) and stops at four digits' width
    const ta = unit.locator('.ced-ta'), gw = () => unit.locator('.ed-src').evaluate((e) => parseFloat(getComputedStyle(e).getPropertyValue('--gw')));
    const put = (v) => ta.evaluate((t, v) => { t.value = v; t.dispatchEvent(new Event('input', { bubbles: true })); }, v); // as typing would, without typing 12,000 lines
    await put('line\n'.repeat(120)); const g3 = await gw();
    expect(g3).toBeGreaterThan(c.gw + c.ch - 1); expect(g3).toBeLessThanOrEqual(3 * c.ch + 20);
    await put('x\n'.repeat(12000)); expect(await gw()).toBeLessThanOrEqual(4 * c.ch + 20);
  });
}

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
  const ta = await page.locator('#rp-ed .ced-ta').evaluate((t) => ({ h: t.clientHeight, text: [...t.previousElementSibling.querySelectorAll('.ln')].reduce((n, l) => n + l.getBoundingClientRect().height, 0) }));
  expect(ta.h).toBeLessThanOrEqual(Math.max(5 * 21, ta.text) + 24);
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
