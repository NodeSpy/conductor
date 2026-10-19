// Every card on every page, at phone width: its content keeps the card's padding (no text flush against its edge), no
// card holds a large empty block (an empty state is said in words), and a name and its time never touch. A card is a
// .dl-card or a .panel. Ledger item 22's fix-up: the ticket's comments looked unstyled on a phone.
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');

/** Measure every visible card: text that sits inside the padding, the biggest empty band, cramped name/time pairs. */
const measure = (page) => page.evaluate(() => {
  const pad = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--pad-card')) || 16;
  const out = [];
  const name = (el) => (el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : el.tagName.toLowerCase());
  const path = (el, card) => { const xs = []; for (let e = el; e && e !== card; e = e.parentElement) xs.unshift(name(e)); return xs.slice(-3).join(' > '); };
  const visible = (el) => { const r = el.getBoundingClientRect(), cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none'; };
  for (const card of document.querySelectorAll('#main .dl-card, #main .panel')) {
    if (!visible(card) || card.closest('.modal, [inert]')) continue;
    if (card.parentElement.closest('.dl-card, .panel')) continue; // a card inside a card is measured as part of its parent
    const c = card.getBoundingClientRect(), label = name(card) + ' “' + (card.querySelector('h1,h2,h3,summary') || card).textContent.trim().slice(0, 40) + '”';
    // 1. text inside the padding: every text line keeps the card's padding from its left and right edges
    const tw = document.createTreeWalker(card, NodeFilter.SHOW_TEXT, { acceptNode: (n) => (n.textContent.trim() ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT) });
    const content = [];
    for (let n = tw.nextNode(); n; n = tw.nextNode()) {
      const el = n.parentElement; if (!visible(el) || el.closest('.sr-only, [hidden], .cm-drop')) continue;
      // a block that draws its own frame (code, a log, a plan, a mock of a web page) is content, measured as a whole
      if (el.closest('pre, .plan, .ced, .cm-out, svg, .log, .site')) { content.push(el.closest('pre, .plan, .ced, .cm-out, svg, .log, .site').getBoundingClientRect()); continue; }
      // text in a horizontal scroller inside the card (a wide table) may sit out of view: only what's in view counts
      // and text clipped by its box (an ellipsis) is measured where it's clipped
      let sc = null; for (let e = el; e && e !== card; e = e.parentElement) { const ox = getComputedStyle(e).overflowX; if (ox === 'auto' || ox === 'scroll') { sc = e.getBoundingClientRect(); break; } }
      let clip = null; for (let e = el; e && e !== card; e = e.parentElement) { const ox = getComputedStyle(e).overflowX; if (ox === 'hidden' || ox === 'clip') { clip = e.getBoundingClientRect(); break; } }
      const rg = document.createRange(); rg.selectNodeContents(n);
      for (const r0 of rg.getClientRects()) { if (r0.width < 1) continue; content.push(r0); if (sc && (r0.left < sc.left - 1 || r0.right > sc.right + 1)) continue;
        const r = clip ? { left: Math.max(r0.left, clip.left), right: Math.min(r0.right, clip.right) } : r0; if (r.right <= r.left) continue;
        if (r.left < c.left + pad - 3 || r.right > c.right - pad + 3) out.push(`${label}: text “${n.textContent.trim().slice(0, 30)}” in ${path(el, card)} is ${Math.round(Math.min(r.left - c.left, c.right - r.right))}px from the card edge (padding ${pad}px)`); }
    }
    card.querySelectorAll('img, svg, canvas, input:not([type=hidden]), textarea, select, button, .cm-ta').forEach((el) => { if (visible(el) && !el.closest('[hidden]')) content.push(el.getBoundingClientRect()); });
    // 2. no large empty band (unless the card says it's empty)
    // a chat panel is full height by design (item 33): the space above a short thread is the thread's
    if (c.height > 160 && !card.querySelector('.empty, .cm-empty, .dl-empty-row') && !card.matches('.chat, .cv-main')) {
      const ys = content.map((r) => [Math.max(r.top, c.top), Math.min(r.bottom, c.bottom)]).filter(([a, b]) => b > a).sort((a, b) => a[0] - b[0]);
      let last = c.top, gap = 0; for (const [a, b] of ys) { gap = Math.max(gap, a - last); last = Math.max(last, b); } gap = Math.max(gap, c.bottom - last);
      if (gap > c.height * 0.5) out.push(`${label}: an empty band of ${Math.round(gap)}px in a ${Math.round(c.height)}px card`);
    }
    // 3. a name and its time never touch
    card.querySelectorAll('.cm-meta, .meta').forEach((m) => { const kids = [...m.children].filter(visible); for (let i = 1; i < kids.length; i++) { const a = kids[i - 1].getBoundingClientRect(), b = kids[i].getBoundingClientRect(); if (Math.abs(a.top - b.top) < 4 && b.left - a.right < 4) out.push(`${label}: “${kids[i - 1].textContent.trim()}” and “${kids[i].textContent.trim()}” touch`); } });
  }
  return out;
});

for (const route of ROUTES) {
  test(`cards keep their padding and hug their content · ${route}`, async ({ page }) => {
    test.skip(page.viewportSize().width > 720, 'phone width');
    await go(page, route, { fresh: true });
    expect(await measure(page)).toEqual([]);
  });
}
