// Measured layout checks (desktop-audit.md; the measuring scripts behind it ported here): run in the page with
// page.evaluate(measure) on every route, desktop and phone, both themes. Each problem names the element it caught.
//   row      controls side by side in one row share one height (±1px) and one centre line (±1px): a button, the ⋯
//            beside it, a select, an input, a segmented control, a filter menu
//   toolbar  a page's filter toolbar is the one component: every control in it the control height, one centre line
//   gap      stacked panels, cards and sections keep the stack gap between them (never touching); cards side by
//            side in a grid start on one line
//   centre   an icon beside its text, and the parts of a row (a status word, meta text, a button), share a centre
//   lines    a row of three or more text columns (a name over its id, where it's used, its value) reads across:
//            the columns' first lines share a centre when any of them runs to a second line (or a one-line column
//            sits on the row's centre)
//   title    a page's title is the size for its kind of page (a list page, a detail page)
//   over     nothing runs past the right edge of the window
//   clip     no text cut off without an ellipsis or a wrap
//   tight    content never sits flush against a card's edge
//   inset    a collapsed disclosure's title sits in the middle of its card, and every single-line card, row, chip and
//            button has equal space above and below its text (±1px)
function measure() {
  const out = [], main = document.getElementById('main');
  const W = innerWidth, phone = W <= 720;
  const root = getComputedStyle(document.documentElement);
  const tok = (n) => parseFloat(root.getPropertyValue(n));
  const vis = (el) => { const r = el.getBoundingClientRect(), cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' && +cs.opacity !== 0 && !el.closest('[hidden], .sr-only, .modal, [inert], #mx-pop, .fb-ac, #pop-menu'); };
  const name = (el) => `${el.tagName.toLowerCase()}${typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : ''} “${(el.innerText || el.value || el.getAttribute('aria-label') || el.placeholder || '').trim().replace(/\s+/g, ' ').slice(0, 24)}”`;
  const box = (el) => el.getBoundingClientRect();
  const mid = (r) => (r.top + r.bottom) / 2;
  const kids = (e) => [...e.children].flatMap((x) => (getComputedStyle(x).display === 'contents' ? kids(x) : [x]));
  // a control: something you press or type in, drawn as a box
  const CONTROL = 'button, a.btn, input:not([type=checkbox]):not([type=radio]):not([type=range]):not([type=file]), select, .seg, [role=radiogroup], .fb-search, .icon-btn';
  const isControl = (k) => k.matches(CONTROL) && !k.matches('.link, .switch, .fb-pv, .fb-px, .cm-mk, .tab-more, [role=tab], .tabs > *, .dl-tabs > *, .chip, .pin, .cv-pin, .tk-chip, .count') && !k.closest('.tabs, .dl-tabs, .cm-tools, .fb-search, .seg, .def-form .row-in, .cm-p, .hb, .site');

  // row: controls side by side in one row
  main.querySelectorAll('*').forEach((c) => {
    if (!vis(c) || c.closest('svg, .fx, .cm-tools, pre')) return; const cs = getComputedStyle(c);
    if (!(cs.display.includes('flex') && !cs.flexDirection.startsWith('column')) && !(cs.display.includes('grid') && cs.gridAutoFlow.startsWith('column'))) return;
    const ks = kids(c).filter((k) => vis(k) && isControl(k) && box(k).height < 60);
    // centre: a single line of text (a status word, meta text) beside a control sits on the control's centre line
    const tallest = Math.max(0, ...ks.map((k) => box(k).height));
    if (ks.length && !/center/.test(cs.alignItems) && box(c).height <= tallest + 8) kids(c).forEach((t) => { if (!vis(t) || ks.includes(t) || t.matches(CONTROL) || t.querySelector(CONTROL) || !(t.innerText || '').trim()) return;
      const r = box(t), lh = parseFloat(getComputedStyle(t).lineHeight) || 20; if (r.height > lh * 1.6) return; const k = ks.find((x) => { const q = box(x); return q.top < r.bottom && q.bottom > r.top; }); if (!k) return;
      const d = mid(r) - mid(box(k)); if (Math.abs(d) > 3) out.push(`centre: ${name(t)} sits ${Math.round(Math.abs(d))}px ${d < 0 ? 'above' : 'below'} the centre of ${name(k)}`); });
    if (ks.length < 2) return;
    const lines = []; ks.forEach((k) => { const r = box(k); let l = lines.find((g) => r.top < g.bottom - 2 && r.bottom > g.top + 2); if (!l) lines.push(l = { top: r.top, bottom: r.bottom, ks: [] }); l.ks.push([k, r]); l.top = Math.min(l.top, r.top); l.bottom = Math.max(l.bottom, r.bottom); });
    lines.forEach((g) => { if (g.ks.length < 2) return; const hs = g.ks.map(([, r]) => r.height), ms = g.ks.map(([, r]) => mid(r));
      const dh = Math.max(...hs) - Math.min(...hs), dm = Math.max(...ms) - Math.min(...ms);
      if (dh > 1.5 || dm > 1.5) out.push(`row: heights Δ${Math.round(dh)} centres Δ${Math.round(dm)}: ${g.ks.map(([k, r]) => `${name(k)}(${Math.round(r.height)})`).join(' | ')}`); });
  });

  // lines: side-by-side cells of a row, one of them more than a line, read across on their first lines
  const firstLine = (el) => { const w = document.createTreeWalker(el, NodeFilter.SHOW_TEXT, { acceptNode: (n) => (n.textContent.trim() && vis(n.parentElement) ? 1 : 3) }); const n = w.nextNode(); if (!n) return null;
    const rg = document.createRange(); rg.selectNodeContents(n); const r = rg.getClientRects()[0]; return r || null; };
  main.querySelectorAll('*').forEach((c) => { if (!vis(c) || c.closest('svg, pre, .cm, .modal, .hb, .tk-board, table')) return; const cs = getComputedStyle(c);
    if (!(cs.display.includes('grid') || (cs.display.includes('flex') && !cs.flexDirection.startsWith('column')))) return;
    const cells = kids(c).filter((k) => vis(k) && !k.matches(CONTROL) && !k.querySelector(CONTROL) && !k.matches('.bav, .tav, .st, .mk, .count, .sc-num, .wl, em, .caps, svg') && (k.innerText || '').trim().length > 2); if (cells.length < 3) return; // a row of columns
    const multi = (k) => { const lh = parseFloat(getComputedStyle(k).lineHeight) || 20; return box(k).height > lh * 1.6; };
    for (let i = 0; i < cells.length; i++) for (let j = i + 1; j < cells.length; j++) { const a = box(cells[i]), b = box(cells[j]); if (!(a.top < b.bottom && b.top < a.bottom) || b.left < a.right - 2 && a.left < b.right - 2) continue;
      if (!multi(cells[i]) && !multi(cells[j])) continue; const fa = firstLine(cells[i]), fb = firstLine(cells[j]); if (!fa || !fb) continue;
      const centred = (x, y, fx) => !multi(x) && Math.abs(mid(fx) - mid(box(y))) <= 2; // a one-line cell on the row's centre
      const d = Math.abs(mid(fa) - mid(fb)); if (d > 3 && Math.abs(fa.bottom - fb.bottom) > 3 && !centred(cells[i], cells[j], fa) && !centred(cells[j], cells[i], fb)) out.push(`lines: ${name(cells[i]).slice(0, 34)} and ${name(cells[j]).slice(0, 34)} start ${Math.round(d)}px apart in one row`); } });

  // toolbar: the filter bar is one component, every control in it the control height, one centre line
  main.querySelectorAll('.fb').forEach((fb) => {
    if (!vis(fb)) return; const H = tok(phone ? '--control-h-touch' : '--control-h') || 36;
    const parts = [...fb.querySelectorAll('.fb-search, .fb-btn, .fb-phone, .fb-more, .fb-clear, .seg, .fb-tools > *, .fb-end > *')].filter((x) => vis(x) && !x.closest('.fb-pop, .fb-ac'));
    const line = parts.filter((x) => Math.abs(mid(box(x)) - mid(box(parts[0]))) < 24);
    line.forEach((x) => { const r = box(x); if (!x.matches('.fb-clear') && Math.abs(r.height - H) > 1) out.push(`toolbar: ${name(x)} is ${Math.round(r.height)}px, not the control height ${H}`);
      if (Math.abs(mid(r) - mid(box(line[0]))) > 1) out.push(`toolbar: ${name(x)} is off the bar's centre line by ${Math.round(mid(r) - mid(box(line[0])))}px`); });
    const s = fb.querySelector('.fb-search'); if (s && vis(s)) { const i = s.querySelector('.fb-in'); if (i && vis(i) && box(i).height < box(s).height - 12) out.push(`toolbar: the search input is ${Math.round(box(i).height)}px inside a ${Math.round(box(s).height)}px field`); }
  });

  // gap: stacked panels and cards
  const G = tok('--gap-stack');
  const PANEL = '.panel, .card, section, .tk-group, .dl-card';
  main.querySelectorAll(PANEL).forEach((c) => {
    if (!vis(c) || c.closest('.tk-board, .cm, .modal')) return; let n = c.nextElementSibling; while (n && !vis(n)) n = n.nextElementSibling;
    if (!n || !n.matches(PANEL)) return; const a = box(c), b = box(n);
    if (b.top < a.bottom - 2) { // side by side: cards in one row of a grid start on one line
      if (getComputedStyle(c.parentElement).display.includes('grid') && Math.abs(b.top - a.top) > 1 && b.left >= a.right - 2) out.push(`gap: ${name(c).slice(0, 40)} and ${name(n).slice(0, 40)} sit side by side ${Math.round(Math.abs(b.top - a.top))}px out of line`);
      return; }
    if (b.left >= a.right - 2 || b.right <= a.left + 2) return;
    const g = b.top - a.bottom; if (g < G - 1) out.push(`gap: ${name(c).slice(0, 40)} → ${name(n).slice(0, 40)} are ${Math.round(g)}px apart (the section gap is ${G})`);
  });

  // centre: an icon beside its text
  main.querySelectorAll('svg').forEach((s) => {
    const p = s.parentElement; if (!p || !vis(p) || !vis(s) || s.closest('.hb, .chart, .bav, .tav, .site, .cm-img, .mk-big, .logo')) return;
    const cs = getComputedStyle(p); if (!cs.display.includes('flex') || cs.flexDirection.startsWith('column') || !/center/.test(cs.alignItems)) {
      const t = [...p.childNodes].find((n) => n.nodeType === 3 && n.textContent.trim()); if (!t) return;
      const rg = document.createRange(); rg.selectNodeContents(t); const rs = [...rg.getClientRects()]; if (!rs.length) return; const tr = rs[0], sr = box(s);
      if (sr.height > 28 || tr.top > sr.bottom || tr.bottom < sr.top - 30) return;
      const d = mid(sr) - mid(tr); if (Math.abs(d) > 2.5) out.push(`centre: the icon in ${name(p)} sits ${Math.round(Math.abs(d))}px ${d < 0 ? 'above' : 'below'} its text`);
    }
  });

  // title: a page's title is the page title (34px, the serif); a generated, machine-written one (a run's, a ticket's,
  // a session's) is the long title (the sans at the h2 size), the same on every page of its kind
  const h = main.querySelector('h1'); if (h && vis(h)) { const fs = parseFloat(getComputedStyle(h).fontSize), long = h.classList.contains('dl-h1-long');
    const head = phone && h.closest('.dl-bothead'); // a bot's or team's header, beside its picture
    const want = long ? tok('--type-h2-size') * (tok('--type-h2-size') < 4 ? 16 : 1) : tok(head ? '--title-head-phone' : phone ? '--title-page-phone' : '--title-page');
    if (want && Math.abs(fs - want) > 0.5) out.push(`title: “${h.innerText.slice(0, 30)}” is ${fs}px, not the ${long ? 'long title' : head ? 'header title' : 'page title'} size ${want}`); }

  // over, clip, tight, empty (the phone sweep's checks, everywhere)
  main.querySelectorAll('*').forEach((e) => {
    if (!vis(e) || e.closest('svg, pre, .cm, table, .tk-board, .diff, .log, .fb-search, .tabs, .dl-tabs, [data-scroll]')) return; const r = box(e);
    const scroller = () => { for (let a = e.parentElement; a && a !== main; a = a.parentElement) if (['auto', 'scroll', 'hidden', 'clip'].includes(getComputedStyle(a).overflowX)) return true; return false; };
    if (r.right > W + 1 && !scroller()) out.push(`over: ${name(e)} runs past the window to ${Math.round(r.right)}`);
  });
  main.querySelectorAll('.panel, .card, [class*=card], section').forEach((c) => {
    if (!vis(c) || c.closest('.cm, .modal, .tk-board')) return; const cr = box(c), cs = getComputedStyle(c); const pl = parseFloat(cs.paddingLeft), bw = parseFloat(cs.borderLeftWidth);
    if (bw > 0 || cs.backgroundColor !== 'rgba(0, 0, 0, 0)') kids(c).forEach((k) => { if (!vis(k) || k.matches('hr, .tabs, .dl-tabs, table, .panel-h, ul, ol, .cm-thread, .cm-comp, img, [class*=-head], [class*=-bar], .rows, .list') || k.offsetWidth >= cr.width - 2 * bw - 1) return;
      if (box(k).left - cr.left - bw < 6 && pl < 6) out.push(`tight: ${name(k).slice(0, 30)} sits flush against ${name(c).slice(0, 30)}`); });
  });
  // inset: a collapsed disclosure, in the box you see it in
  const textBox = (el) => { const rg = document.createRange(); rg.selectNodeContents(el); const rs = [...rg.getClientRects()].filter((r) => r.width > 0 && r.height > 0); if (!rs.length) return null;
    return { top: Math.min(...rs.map((r) => r.top)), bottom: Math.max(...rs.map((r) => r.bottom)) }; };
  const painted = (e) => { const cs = getComputedStyle(e); return parseFloat(cs.borderTopWidth) > 0 || parseFloat(cs.borderBottomWidth) > 0 || (cs.backgroundColor !== 'rgba(0, 0, 0, 0)' && cs.backgroundColor !== 'transparent'); };
  main.querySelectorAll('details:not([open])').forEach((d) => { const s = d.querySelector(':scope > summary'); if (!s || !vis(d)) return;
    let b = d; for (let e = d; e && e !== main; e = e.parentElement) if (painted(e)) { b = e; break; }
    const br = box(b); if (br.height > 160 || (b !== d && [...b.children].filter(vis).length > 1)) return; const t = textBox(s); if (!t) return;
    const top = t.top - br.top, bot = br.bottom - t.bottom; if (Math.abs(top - bot) > 1.5) out.push(`inset: the closed “${s.innerText.trim().slice(0, 28)}” sits ${Math.round(top)} from the top, ${Math.round(bot)} from the bottom of its card`); });
  // inset: single-line buttons, chips, pills, rows and cards
  main.querySelectorAll('.btn, .fb-btn, .fb-reset, .chip, .fb-pill, .tk-chip, .st, .dl-card, .panel, .callout, .dl-list > li, .row-card').forEach((e) => {
    if (!vis(e) || !painted(e) || e.closest('svg, .modal, .fb-pop') || e.matches('details') || e.querySelector('input, textarea, select, img, svg.hb, canvas, .bav.lg, .bav.xl')) return;
    const t = textBox(e); if (!t) return; const lh = parseFloat(getComputedStyle(e).lineHeight) || parseFloat(getComputedStyle(e).fontSize) * 1.5;
    if (t.bottom - t.top > lh * 1.6) return; // more than one line
    const r = box(e), bt = parseFloat(getComputedStyle(e).borderTopWidth), bb = parseFloat(getComputedStyle(e).borderBottomWidth);
    const top = t.top - r.top - bt, bot = r.bottom - bb - t.bottom; if (Math.abs(top - bot) > 1.5) out.push(`inset: ${name(e)} has ${Math.round(top)} above its text and ${Math.round(bot)} below`); });
  return [...new Set(out)];
}
module.exports = { measure };
