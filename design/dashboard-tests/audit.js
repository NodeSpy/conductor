// In-page audits shared by the layout sweeps (iphone.spec.js, layout.spec.js). Each is a function the
// page evaluates: page.evaluate(audit), page.evaluate(primaries).
/** In-page audit: theme, overflow, overlap with the simulated toolbar, contrast. */
const audit = () => {
  const h = document.documentElement, iw = innerWidth, ov = document.getElementById('sim-toolbar');
  const ovTop = ov ? ov.getBoundingClientRect().top : innerHeight;
  const rgba = (c) => { const m = String(c).match(/rgba?\(([^)]+)\)/); if (!m) return null; const [r, g, b, a = 1] = m[1].split(/[ ,/]+/).filter(Boolean).map(Number); return { r, g, b, a }; };
  const lum = ({ r, g, b }) => { const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
  const over = (top, under) => ({ r: top.r * top.a + under.r * (1 - top.a), g: top.g * top.a + under.g * (1 - top.a), b: top.b * top.a + under.b * (1 - top.a), a: 1 });
  const bodyBg = rgba(getComputedStyle(document.body).backgroundColor);
  const bgOf = (el) => { const stack = []; for (let e = el; e && e.nodeType === 1; e = e.parentElement) { const c = rgba(getComputedStyle(e).backgroundColor); if (c && c.a > 0) { stack.push(c); if (c.a >= 1) break; } } let base = bodyBg && bodyBg.a ? bodyBg : { r: 255, g: 255, b: 255, a: 1 }; for (let i = stack.length - 1; i >= 0; i--) base = stack[i].a >= 1 ? stack[i] : over(stack[i], base); return base; };
  const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
  const visible = (el) => { const r = el.getBoundingClientRect(), cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' && +cs.opacity > 0.05; };
  const label = (el) => `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : ''} "${(el.textContent || '').trim().slice(0, 40)}"`;
  // overflow: anything past the right edge that no clipping ancestor contains
  const wide = [];
  document.querySelectorAll('body *').forEach((el) => { if (el.closest('#sim-toolbar, .tooltip') || (el.ownerSVGElement)) return; const r = el.getBoundingClientRect(); if (r.width === 0 || r.right <= iw + 1) return; for (let a = el.parentElement; a; a = a.parentElement) { if (getComputedStyle(a).overflowX !== 'visible' && a.getBoundingClientRect().right <= iw + 1) return; } if (visible(el)) wide.push(label(el) + ` right=${Math.round(r.right)}`); });
  // the tab bar and primary actions: fully above the toolbar, and the topmost thing at their centre
  const hidden = [];
  const check = (el, what) => { const r = el.getBoundingClientRect(); if (r.bottom > ovTop + 0.5) { hidden.push(`${what} ${label(el)} bottom=${Math.round(r.bottom)} > toolbar ${Math.round(ovTop)}`); return; } const t = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); if (!t || !(el === t || el.contains(t) || t.contains(el))) hidden.push(`${what} ${label(el)} covered by ${t ? label(t) : 'nothing'}`); };
  document.querySelectorAll('#tabbar a').forEach((a) => check(a, 'tab'));
  // contrast of rendered text
  const low = [], weak = []; let n = 0;
  const seen = new Set();
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let t = walker.nextNode(); t; t = walker.nextNode()) { if (!t.textContent.trim()) continue; const el = t.parentElement; if (!el || seen.has(el) || el.closest('#sim-toolbar, svg, .tooltip, [aria-hidden="true"], .sr-only, option')) continue; seen.add(el); if (!visible(el)) continue; const r = el.getBoundingClientRect(); if (r.bottom < 0 || r.top > innerHeight) continue; const fg = rgba(getComputedStyle(el).color); if (!fg || fg.a < 0.05) continue; const bg = bgOf(el), c = ratio(fg.a < 1 ? over(fg, bg) : fg, bg); n++; if (c < 3) low.push(`${label(el)} ${c.toFixed(2)}:1`); else if (c < 4.5) weak.push(c); }
  return { theme: h.dataset.theme, src: h.dataset.themeSource, ink: getComputedStyle(h).getPropertyValue('--ink').trim(), bg: getComputedStyle(h).getPropertyValue('--bg').trim(), wide, hidden, low, weakShare: n ? weak.length / n : 0, n, sw: h.scrollWidth, iw };
};

/** Scroll each primary action into view inside the app's scroller, then check it isn't covered. */
const primaries = () => { const ov = document.getElementById('sim-toolbar'), ovTop = ov ? ov.getBoundingClientRect().top : innerHeight, out = [];
  document.querySelectorAll('.page-head .actions .btn, .btn.primary, .wizard-foot .btn, .sticky-decide .btn').forEach((el) => { const r0 = el.getBoundingClientRect(); if (!r0.width || el.closest('.modal, .cdrawer, #pop-menu')) return; el.scrollIntoView({ block: 'center', behavior: 'instant' }); const r = el.getBoundingClientRect(); const t = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); if (r.bottom > ovTop + 0.5 || !(t && (el === t || el.contains(t)))) out.push(`${el.textContent.trim().slice(0, 30)} at ${Math.round(r.top)}–${Math.round(r.bottom)} covered by ${t ? t.tagName + (t.id ? '#' + t.id : '') + '.' + String(t.className).split(' ')[0] : 'nothing'}`); });
  return out; };

module.exports = { audit, primaries };
