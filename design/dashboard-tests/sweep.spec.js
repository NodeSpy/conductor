// The visual sweep, as code: every route at desktop (1440), tablet (900) and phone (390) is checked for the layout
// regressions the earlier checks missed:
//   - text and controls overlapping inside a card (a button over a change summary, a label under a button);
//   - content spilling outside its card (a button past the card's right edge);
//   - extreme wrapping (a phrase squeezed to one word per line in a column far too narrow for it);
//   - list content centred by accident (a row's text starting mid-row) and rows blown up to many times their content;
//   - a dropped interpolation: an unknown element in the DOM (`<name>` read as a tag) or an empty gap before a full stop.
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');

const SIZES = [['desktop', 1440, 900], ['tablet', 900, 1000], ['phone', 390, 844]];

const audit = () => {
  const out = [];
  const vis = (el) => { const r = el.getBoundingClientRect(); if (!r.width || !r.height) return null; const cs = getComputedStyle(el); if (cs.visibility === 'hidden' || +cs.opacity < 0.05) return null; return r; };
  const label = (el) => `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}.${String(el.getAttribute('class') || '').split(' ').filter(Boolean).slice(0, 2).join('.')} "${(el.innerText || el.value || '').trim().replace(/\s+/g, ' ').slice(0, 40)}"`;
  const clipped = (el, stop) => { for (let p = el.parentElement; p && p !== stop.parentElement; p = p.parentElement) { const cs = getComputedStyle(p); if (/(auto|scroll|hidden|clip)/.test(cs.overflowX + cs.overflowY)) return true; } return false; };
  const ownText = (el) => [...el.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent).join('').trim();
  const cards = [...document.querySelectorAll('#main .dl-card, #main .panel, #main .modal')].filter((c) => vis(c) && !c.closest('.site'));
  for (const card of cards) {
    const cr = card.getBoundingClientRect();
    const leaves = [...card.querySelectorAll('button, a.btn, input:not([type=checkbox]):not([type=radio]), select, textarea, b, span, p, h2, h3, dd, dt, td, label, li')].filter((el) => {
      if (el.closest('.sr-only, svg, .dl-code, pre, .ced, .diff, .graph, details:not([open]) > :not(summary)')) return false;
      const r = vis(el); if (!r) return false;
      const isCtl = el.matches('button, a.btn, input, select, textarea');
      return isCtl || ownText(el).length > 1;
    });
    // spill: a leaf past its card's right edge (scrollable or clipped regions are allowed to hold more)
    for (const el of leaves) { const r = el.getBoundingClientRect(); if (r.right > cr.right + 2 && !clipped(el, card)) out.push(`spills out of its card: ${label(el)} (${Math.round(r.right - cr.right)}px)`); }
    // overlap: a control over text or another control it doesn't contain (and isn't inside)
    // a sticky or fixed bar (the phone's decision bar, a pinned selection bar) sits over content by design
    const pinned = (el) => { for (let q = el; q && q !== card; q = q.parentElement) { if (/sticky|fixed/.test(getComputedStyle(q).position)) return true; } return false; };
    const ctl = leaves.filter((el) => el.matches('button, a.btn, input, select, textarea') && !pinned(el));
    for (const a of ctl) { const ra = a.getBoundingClientRect();
      for (const b of leaves) { if (a === b || a.contains(b) || b.contains(a)) continue; const rb = b.getBoundingClientRect();
        const ix = Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left), iy = Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top);
        if (ix > 4 && iy > 4 && !clipped(b, card)) out.push(`overlaps: ${label(a)} over ${label(b)}`); } }
  }
  // extreme wrapping: a phrase of three or more words laid out one word per line (counted from the text's own line boxes)
  document.querySelectorAll('#main *, .modal *').forEach((el) => { const t = ownText(el), words = t.split(/\s+/).filter(Boolean).length; if (words < 3 || el.closest('svg, .sr-only, .site, pre, .dl-code, textarea')) return; if (!vis(el)) return;
    const tops = new Set(); [...el.childNodes].filter((n) => n.nodeType === 3 && n.textContent.trim()).forEach((n) => { const rg = document.createRange(); rg.selectNodeContents(n); [...rg.getClientRects()].forEach((q) => { if (q.width > 0) tops.add(Math.round(q.top)); }); });
    if (tops.size >= 3 && tops.size >= words - 1) out.push(`one word per line: ${label(el)} (${tops.size} lines for ${words} words)`); });
  // list rows: content centred by accident, or a row many times taller than its content needs
  document.querySelectorAll('#main .dl-list > li, #main .list > .item, #main .list > a.item').forEach((row) => { const r = vis(row); if (!r || row.closest('.site')) return;
    const first = [...row.querySelectorAll('b, span, a, div')].find((x) => vis(x) && (x.innerText || '').trim()); if (!first) return;
    const fr = first.getBoundingClientRect(); if (r.width > 320 && fr.left - r.left > r.width * 0.3 && getComputedStyle(row).textAlign === 'center') out.push(`centred row content: ${label(row)}`);
    if (r.width > 320 && fr.left - r.left > r.width * 0.4) out.push(`row content starts mid-row: ${label(row)}`);
    if (r.height > 220 && !row.querySelector('details[open], .open, textarea, .diff, pre, .hist-diff, .sec-detail')) out.push(`row height blown up: ${label(row)} (${Math.round(r.height)}px)`); });
  // dropped interpolations: an element the browser doesn't know (an unescaped <name>), or a gap before punctuation
  const known = /^(a|abbr|article|aside|b|blockquote|br|button|code|dd|del|details|dfn|div|dl|dt|em|fieldset|figure|footer|form|h[1-6]|header|hr|i|img|input|ins|kbd|label|legend|li|main|mark|nav|ol|optgroup|option|p|pre|s|section|select|small|span|strong|sub|summary|sup|table|tbody|td|textarea|tfoot|th|thead|time|tr|u|ul|var|svg|path|circle|rect|line|polyline|polygon|g|text|tspan|title|defs|use|ellipse|lineargradient|stop|clippath|mask|pattern|foreignobject|template|script|style|iframe|canvas|picture|source|video|audio|output|progress|meter|dialog|wbr|q|cite|data|address|caption|col|colgroup|map|area|noscript|search|datalist|marker)$/i;
  document.querySelectorAll('#main *, .modal *').forEach((el) => { if (!known.test(el.tagName)) out.push(`unknown element <${el.tagName.toLowerCase()}> (an unescaped placeholder?) in ${label(el.parentElement)}`); });
  document.querySelectorAll('#main .hint, #main .dl-sub, #main .help, #main p, .modal .hint, .modal p').forEach((el) => { if (/\s{2,}[.,;:]|\(\s*\)|,\s*,/.test(el.textContent)) out.push(`a gap where text was dropped: ${label(el)} → "${el.textContent.trim().slice(0, 80)}"`); });
  return [...new Set(out)];
};

for (const [name, w, h] of SIZES) {
  test(`visual sweep at ${name} (${w}px): no overlaps, spills, one-word lines, centred rows, blown-up rows or dropped text`, async ({ page }) => {
    test.setTimeout(15 * 60 * 1000);
    await page.setViewportSize({ width: w, height: h });
    const problems = [];
    for (const r of ROUTES) {
      await go(page, r, { fresh: true });
      await page.waitForTimeout(150);
      (await page.evaluate(audit)).forEach((p) => problems.push(`${r}: ${p}`));
    }
    expect(problems).toEqual([]);
  });
}
