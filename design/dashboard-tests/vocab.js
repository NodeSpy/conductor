// The words people read (r5 major 10): practice mode (not shadow), ticket (not case), approval (not proposal),
// connection (not connector), the plain risk words (not commit_money, observe, reversible as codes), no "teams:" or
// "bots:" config kickers, no "task cNN"/"case cNN". Visible text outside code, ids and For audit. Returns [{ word, text, where }].
module.exports = () => {
  const BAD = [['shadow', /\bshadow\b/i], ['case', /\b(?:a|the|this|its|each|every|child|parent|new|open|one|your)\s+cases?\b|\bcases?\s+c\d+|\bcase\b(?=[:·])/i], ['proposal', /\bproposals?\b/i], ['connector', /\bconnectors?\b/i],
    ['risk code', /\b(?:commit_money|observe|reversible)\b(?![-_])/], ['config kicker', /^(?:teams|bots|connectors|triggers):\s*$/i], ['task id', /\btask c\d+/i]];
  const QUIET = '.mono, code, pre, kbd, .digest, .dl-audit-d, details.tech, .dl-tech-d, .tech, .ced, .diff, .yaml, textarea, input, select, option, script, style, svg, .proto-note, .raw-side, .log, .bav-id, .can-id, .item-id, [data-vocab-ok], .glossary, .gl-term, .gl-cfg';
  const out = [], seen = new Set(), main = document.querySelector('#main') || document.body;
  const vis = (e) => { const r = e.getBoundingClientRect(); if (!(r.width > 0 && r.height > 0)) return false; for (let x = e; x && x !== document.body; x = x.parentElement) { const cs = getComputedStyle(x); if (cs.display === 'none' || cs.visibility === 'hidden') return false; } return true; };
  const w = document.createTreeWalker(main, NodeFilter.SHOW_TEXT);
  for (let n = w.nextNode(); n; n = w.nextNode()) {
    const el = n.parentElement; if (!el || el.closest(QUIET)) continue;
    const d = el.closest('details'); if (d && !d.open && !el.closest('summary')) continue;
    const t = n.textContent; const hit = BAD.find(([, re]) => re.test(t)); if (!hit || !vis(el)) continue;
    const key = hit[0] + '|' + t.trim().slice(0, 60); if (seen.has(key)) continue; seen.add(key);
    out.push({ word: hit[0], text: t.trim().slice(0, 100), where: el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className ? '.' + el.className.split(' ')[0] : '') });
  }
  return out;
};
