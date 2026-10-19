// One name per bot (r5 major 9): visible text that names a bot by its config id where it has a display name, outside
// the quiet places an id belongs (the id beside the name, code and paths, For audit, a field that edits the id).
// Returns [{ id, text, where }].
module.exports = () => {
  const ids = Object.keys(window.D.identity || {}).filter((b) => (window.D.identity[b] || {}).name && window.D.identity[b].name !== b);
  if (!ids.length) return [];
  const re = new RegExp(`(^|[^\\w./:@-])(${ids.map((x) => x.replace(/[-]/g, '\\-')).join('|')})(?![\\w/@-]|\\.[a-z])`);
  const QUIET = '.bav-id, .can-id, .mono, code, pre, kbd, .digest, .dl-audit-d, details.tech, .dl-tech-d, .tech, .ced, .diff, .yaml, textarea, input, select, option, script, style, [data-id-ok], .crumbs a[href*="/settings"], .item-id, .fx-id, .cm-code, .run-id, .wf-yaml, svg, .proto-note, .toast, .pv-yaml, .config-id, .ced-hl, .raw-side, .log, .l .m';
  const out = [], seen = new Set(), main = document.querySelector('#main') || document.body;
  const vis = (e) => { for (let x = e; x && x !== document.body; x = x.parentElement) { const cs = getComputedStyle(x); if (cs.display === 'none' || cs.visibility === 'hidden') return false; if (x.tagName === 'DETAILS' && !x.open && x !== e.closest('details')) return false; } const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
  const walker = document.createTreeWalker(main, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const el = n.parentElement; if (!el || el.closest(QUIET)) continue;
    const d = el.closest('details'); if (d && !d.open && !el.closest('summary')) continue;
    const m = n.textContent.match(re); if (!m || !vis(el)) continue;
    const key = m[2] + '|' + n.textContent.trim().slice(0, 60); if (seen.has(key)) continue; seen.add(key);
    out.push({ id: m[2], text: n.textContent.trim().slice(0, 90), where: (el.className && typeof el.className === 'string' ? el.tagName.toLowerCase() + '.' + el.className.split(' ')[0] : el.tagName.toLowerCase()) });
  }
  return out;
};
