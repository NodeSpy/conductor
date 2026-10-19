// The mobile quality pass (ledger step M, mobile-audit.md): on every route at phone width, the classes of problem the
// audit found never come back. Each check names the element it caught.
//   tabs     no tab label cut at the edge of its bar (tabs that don't fit are under More)
//   ⋯        no "More" button alone on a line
//   clip     no text cut off without an ellipsis or a wrap; the page never scrolls sideways
//   pills    no pill filter outside the shared filter bar
//   header   a bot's or team's header takes at most 140px before its tabs
//   orphan   an action row never leaves one button alone on a line
//   tap      buttons are at least 44px tall
//   bottom   the page's end clears the bottom bar
//   type     no text below the brand's smallest size (the caption, 12.5px; code 12px)
const { test, expect, go } = require('./fixtures');
const { ROUTES } = require('./routes');

const audit = (page) => page.evaluate(() => {
  const out = [], main = document.getElementById('main');
  const vis = (el) => { const r = el.getBoundingClientRect(), cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' && !el.closest('[hidden], .sr-only, .modal, [inert]'); };
  const name = (el) => `${el.tagName.toLowerCase()}${el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : ''} “${(el.textContent || el.getAttribute('aria-label') || '').trim().replace(/\s+/g, ' ').slice(0, 30)}”`;
  // tabs
  main.querySelectorAll('.tabs, .dl-tabs').forEach((t) => { if (!vis(t)) return; const b = t.getBoundingClientRect();
    t.querySelectorAll(':scope > a, :scope > button').forEach((a) => { if (!vis(a)) return; const r = a.getBoundingClientRect(); if (r.left < b.left - 1 || r.right > b.right + 1) out.push(`tabs: ${name(a)} is cut at the bar's edge`); }); });
  // ⋯ alone on a line
  main.querySelectorAll('.icon-btn, .ph-more').forEach((m) => { if (!vis(m) || !/more/i.test(m.getAttribute('aria-label') || m.dataset.act || '')) return; let p = m.parentElement; while (getComputedStyle(p).display === 'contents') p = p.parentElement; const r = m.getBoundingClientRect();
    const kids = (e) => [...e.children].flatMap((x) => (getComputedStyle(x).display === 'contents' ? kids(x) : [x])); const sibs = kids(p).filter((x) => x !== m && vis(x)); if (!sibs.length) return;
    if (!sibs.some((x) => { const q = x.getBoundingClientRect(); return q.top < r.bottom - 4 && q.bottom > r.top + 4; })) out.push(`⋯: ${name(m)} in ${name(p).slice(0, 40)} is alone on its line`); });
  // clipped text; sideways scrolling
  if (main.scrollWidth > main.clientWidth + 1) out.push(`clip: the page scrolls sideways (${main.scrollWidth} > ${main.clientWidth})`);
  main.querySelectorAll('*').forEach((el) => { if (!vis(el) || el.closest('svg, .tabs, .dl-tabs, .site, .thumb, .cm-img, pre, .log, .plan, .ced, .diff, .cm-out, .code, .tk-board')) return; const cs = getComputedStyle(el);
    if ((cs.overflowX === 'hidden' || cs.overflowX === 'clip') && cs.textOverflow !== 'ellipsis' && el.scrollWidth > el.clientWidth + 2 && [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())) out.push(`clip: ${name(el)} cuts its text`); });
  // pill filters
  main.querySelectorAll('.chip').forEach((c) => { if (vis(c) && !c.closest('.fb')) out.push(`pills: ${name(c)} is a filter outside the filter bar`); });
  // header height
  const head = main.querySelector('.dl-bothead'), tabs = main.querySelector('.dl-tabs:not(.dl-subnav), .tabs');
  if (head && tabs && vis(head) && vis(tabs)) { const h = tabs.getBoundingClientRect().top - head.getBoundingClientRect().top; if (h > 140) out.push(`header: ${Math.round(h)}px from the header to its tabs`); }
  // orphaned buttons
  main.querySelectorAll('.dl-actions, .dl-na, .prop-decide, .kp-acts, .row').forEach((box) => { if (!vis(box)) return; const bs = [...box.children].filter((x) => vis(x) && x.matches('.btn, a.btn, button.btn'));
    if (bs.length < 3) return; const rows = new Map(); bs.forEach((b) => { const t = Math.round(b.getBoundingClientRect().top / 4); rows.set(t, (rows.get(t) || 0) + 1); });
    const counts = [...rows.values()]; if (counts.length > 1 && counts[counts.length - 1] === 1 && counts[0] > 1) out.push(`orphan: ${name(bs[bs.length - 1])} alone under ${counts[0]} buttons`); });
  // tap targets
  main.querySelectorAll('button, a.btn, .icon-btn, [role="tab"]').forEach((b) => { if (!vis(b) || b.closest('.cm-tools, .tk-pick, .cm-mk, .fx, .cm-img, .cm-file, .seg, .tabs, .dl-tabs')) return; if (b.matches('.link, .cm-mk, .switch, summary *, .fb-px')) return; // a pill's ✕ draws small over a 44px target
    const r = b.getBoundingClientRect(); if (r.height < 43) out.push(`tap: ${name(b)} is ${Math.round(r.height)}px tall`); });
  // the bottom bar
  const bar = document.getElementById('tabbar'); if (bar && vis(bar)) { main.scrollTo({ top: main.scrollHeight, behavior: 'instant' }); const last = [...main.querySelectorAll('.page > *')].filter(vis).pop();
    if (last && last.getBoundingClientRect().bottom > bar.getBoundingClientRect().top + 1) out.push(`bottom: ${name(last)} ends under the bottom bar`); main.scrollTo({ top: 0, behavior: 'instant' }); }
  // type sizes
  const tw = document.createTreeWalker(main, NodeFilter.SHOW_TEXT, { acceptNode: (n) => (n.textContent.trim() ? 1 : 2) });
  for (let n = tw.nextNode(); n; n = tw.nextNode()) { const el = n.parentElement; if (!vis(el) || el.closest('svg, .site, .thumb')) continue; const fs = parseFloat(getComputedStyle(el).fontSize), mono = /mono|code/i.test(getComputedStyle(el).fontFamily) || !!el.closest('.mono, code');
    if (fs < (mono ? 11.9 : 12.4)) { out.push(`type: ${name(el)} is ${fs}px`); } }
  return [...new Set(out)];
});

for (const route of ROUTES) {
  test(`mobile quality · ${route}`, async ({ page }) => {
    test.skip(page.viewportSize().width > 720, 'phone width');
    await go(page, route, { fresh: true });
    expect(await audit(page)).toEqual([]);
  });
}
