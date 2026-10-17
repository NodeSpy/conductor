// Calm by default: the four components that keep a screen quiet until something needs you.
//   summaryLine  normal work collapses into one quiet line ("10 finished · 9 passed · 1 parked"), expandable
//   calmLanes    a timeline where passing runs are a faint density band of ticks, and only exceptions
//                (failed, needs you, running) are marks, each labelled directly instead of via a legend
//   quietState   the first-class empty state: one sentence, one supporting line
//   exception    the one loud thing: large, at the top, one plain sentence, one action
// Emphasis levels (tokens: --color-emphasis-loud/normal/quiet/ghost): only "loud" items get colour.
import { mark, chartMark, STATES } from './marks.js';

export const summaryLine = ({ lead, parts = [], body = '', open = false, label = 'Show' }) =>
  `<details class="calm-summary"${open ? ' open' : ''}><summary><span class="cs-lead">${lead}</span>${parts.length ? `<span class="cs-parts">${parts.join(' · ')}</span>` : ''}<span class="cs-more">${label}</span></summary><div class="cs-body">${body}</div></details>`;

export const quietState = ({ title, sub = '', art = true }) =>
  `<div class="calm-quiet" data-glance data-glance-answer="${title.replace(/"/g, '&quot;')}">${art ? `<svg class="cq-art" viewBox="0 0 160 92" aria-hidden="true"><path d="M46 70a34 36 0 0 1 68 0h-9a25 26 0 0 0-50 0z" fill="var(--color-border-default)"/><circle cx="80" cy="70" r="10" fill="var(--color-border-strong, var(--color-border-default))"/><path d="M8 76h144" stroke="var(--color-border-default)" stroke-width="2" stroke-linecap="round"/></svg>` : ''}<h2>${title}</h2>${sub ? `<p>${sub}</p>` : ''}</div>`;

/** The one loud thing. `state` from the vocabulary; `action` is the caller's button HTML. */
export const exception = ({ state, kicker, title, sentence, action = '', meta = '', glance = false }) =>
  `<article class="calm-exc" data-item data-s="${{ failed: 'danger', needs: 'accent', waiting: 'warning' }[state] ?? 'neutral'}"${glance ? ` data-glance data-glance-answer="${title.replace(/"/g, '&quot;')}"` : ''}>
    <div class="ce-k">${mark(state, { size: 14 })}<span>${kicker}</span></div><h3>${title}</h3><p>${sentence}</p>${action || meta ? `<div class="ce-a">${action}<span class="ce-m">${meta}</span></div>` : ''}</article>`;

/** A compact exception row: one line, mark + what + one action; detail is the sentence, which
 *  truncates (and hides on a phone). Edge colour follows the mark, as in the status column. */
const EDGE_S = { failed: 'danger', needs: 'accent', waiting: 'warning', running: 'running' };
export const excRow = ({ state, title, sentence = '', age = '', action = '' }) =>
  `<li class="calm-row attn" data-item data-s="${EDGE_S[state] ?? 'neutral'}">${mark(state, { size: 14, label: true })}<span class="cr-t"><b>${title}</b>${sentence ? `<span>${sentence}</span>` : ''}</span><span class="cr-age">${age}</span>${action}</li>`;
/** `terse`: the titles are already sentences, so on a phone the detail line is dropped. */
export const excRows = (rows, { terse = false } = {}) => `<ul class="calm-rows${terse ? ' terse' : ''}">${rows.map(excRow).join('')}</ul>`;

/** Lanes: [name, [[minutesAgo, state], …]] over `span` minutes. Passing (and parked) runs become
 *  faint ticks in a density band; only exceptions are marks. Each lane says in words what its marks
 *  are, on the same row ("failed 52 min ago · running"), so there is no legend to decode and labels
 *  never collide. Lanes with exceptions come first, worst first; lanes where everything went fine
 *  fold into one "N more" row. `stacked` puts name and words above the band (phones). */
const SEVERITY = { failed: 0, needs: 1, waiting: 2, running: 3, shadow: 4, skipped: 5 };
export function calmLanes(lanes, { span = 720, width = 880, labelW = 120, wordsW = 210, stacked = false, fold = true, axis = ['12 h ago', 'now'] } = {}) {
  const H = stacked ? 46 : 40, by = stacked ? 33 : H / 2, ty = stacked ? 15 : H / 2 + 5;
  const x0 = stacked ? 6 : labelW + 8, x1 = stacked ? width - 6 : width - wordsW - 16, x = (m) => x0 + (1 - m / span) * (x1 - x0);
  const isFine = (s) => s === 'passed' || s === 'ok' || s === 'parked';
  const norm = (s) => (s === 'approval' || s === 'waiting' ? 'needs' : s === 'ok' ? 'passed' : s);
  let rows = lanes.map(([name, notes]) => [name, notes.filter(([m]) => m <= span).map(([m, s]) => [m, norm(s)])]);
  const worst = (n) => Math.min(9, ...n.filter(([, s]) => !isFine(s)).map(([, s]) => SEVERITY[s]));
  const loud = rows.filter(([, n]) => worst(n) < 9).sort((a, b) => worst(a[1]) - worst(b[1])), calm = rows.filter(([, n]) => worst(n) === 9);
  rows = fold && calm.length > 1 ? [...loud, [`${calm.length} more`, calm.flatMap(([, n]) => n), calm.map(([n]) => n).join(', ')]] : [...loud, ...calm];
  const ago = (m) => (m < 60 ? `${m} min ago` : `${Math.round(m / 60)} h ago`);
  const wordOf = ([m, s]) => (s === 'running' ? 'running' : s === 'needs' ? 'needs you' : `${STATES[s].word.toLowerCase()} ${ago(m)}`);
  const svgRow = ([name, notes, names]) => {
    const exc = notes.filter(([, s]) => !isFine(s)).sort((a, b) => SEVERITY[a[1]] - SEVERITY[b[1]] || a[0] - b[0]);
    const quiet = exc.length === 0, fine = notes.length - exc.length;
    let g = `<text x="0" y="${ty}" class="cl-name${quiet ? ' q' : ''}">${name}${names ? `<title>${names}</title>` : ''}</text>`;
    g += `<rect x="${x0}" y="${by - 5}" width="${x1 - x0}" height="10" rx="5" class="cl-track"/>`;
    for (const [m] of notes.filter(([, s]) => isFine(s))) g += `<rect x="${(x(m) - 1).toFixed(1)}" y="${by - 4}" width="2" height="8" rx="1" class="cl-tick"/>`;
    // Marks closer than 16 units are nudged apart (in time order), so two never sit on top of each other.
    const pos = exc.map(([m, s]) => ({ m, s, x: x(m) })).sort((a, b) => a.x - b.x);
    for (let i = 1; i < pos.length; i++) pos[i].x = Math.max(pos[i].x, pos[i - 1].x + 16);
    const over = (pos.at(-1)?.x ?? 0) - (x1 + 2);
    if (over > 0) for (let i = pos.length - 1; i >= 0; i--) pos[i].x = Math.min(pos[i].x, x1 + 2 - (pos.length - 1 - i) * 16);
    for (const p of pos.sort((a, b) => SEVERITY[b.s] - SEVERITY[a.s])) g += chartMark(p.s, p.x, by, 15, `${name}: ${wordOf([p.m, p.s])}`).replace('<svg ', '<svg data-labelled="1" ');
    const words = quiet ? `${fine} fine` : exc.map((e, i) => `${i ? ' · ' : ''}<tspan data-state="${e[1]}">${wordOf(e)}</tspan>`).join('');
    g += `<text x="${width}" y="${ty}" text-anchor="end" class="cl-lab${quiet ? ' q' : ''}">${words}</text>`;
    return `<svg viewBox="0 0 ${width} ${H}" class="cl-row${quiet ? ' quiet' : ''}"${quiet ? '' : ' data-item'} role="img" aria-label="${name}: ${quiet ? `all ${fine} fine` : exc.map(wordOf).join(', ')}">${g}</svg>`;
  };
  const total = lanes.reduce((a, [, n]) => a + n.filter(([m]) => m <= span).length, 0);
  const fine = rows.reduce((a, [, n]) => a + n.filter(([, s]) => isFine(s)).length, 0);
  return `<div class="calm-lanes${stacked ? ' stacked' : ''}" style="--cl-l:${((x0 / width) * 100).toFixed(2)}%;--cl-r:${(((width - x1) / width) * 100).toFixed(2)}%">${rows.map(svgRow).join('')}
    <div class="cl-axis"><span>${axis[0]}</span><span>${axis[1]}</span></div>
    <p class="cl-note">Each faint tick is a run that went fine: ${fine} of ${total}. Only what needs attention, or is running, is drawn.</p></div>`;
}

export const CALM_CSS = `
.calm-summary{border-top:1px solid var(--color-border-subtle);}
.calm-summary>summary{list-style:none;cursor:pointer;display:flex;align-items:baseline;gap:10px;min-height:var(--summary-line-height,48px);align-items:center;color:var(--color-text-secondary);font-size:var(--type-body-m-size,15px)}
.calm-summary>summary::-webkit-details-marker{display:none}
.calm-summary .cs-lead{color:var(--color-text-primary);font-weight:600}
.calm-summary .cs-parts{color:var(--color-text-secondary);min-width:0;flex:0 1 auto;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.calm-summary .cs-more{margin-left:auto;font-size:13px;font-weight:600;color:var(--color-text-tertiary)}
.calm-summary[open] .cs-more{visibility:hidden}
.calm-quiet{padding:var(--quiet-state-pad,64px) 0 40px;max-width:40rem}
.calm-quiet .cq-art{width:var(--quiet-state-art-size,96px);height:auto;display:block;margin-bottom:18px}
.calm-quiet h2{font-family:var(--font-family-serif);font-weight:600;font-variation-settings:'SOFT' 100;font-size:clamp(30px,5vw,44px);line-height:1.1;letter-spacing:-.015em;margin:0;color:var(--color-text-primary)}
.calm-quiet p{margin:10px 0 0;font-size:17px;color:var(--color-text-secondary)}
.calm-exc{position:relative;background:var(--exception-bg,var(--color-bg-surface));border-radius:var(--exception-radius,14px);box-shadow:var(--exception-shadow,var(--shadow-2)),inset var(--exception-edge,4px) 0 0 var(--s-solid);padding:var(--exception-pad,22px) var(--exception-pad,22px) var(--exception-pad,22px) calc(var(--exception-pad,22px) + 4px)}
.calm-exc .ce-k{display:flex;align-items:center;gap:8px;font-size:13.5px;font-weight:700;color:var(--s-fg)}
.calm-exc h3{margin:8px 0 6px;font-family:var(--font-family-serif);font-weight:600;font-variation-settings:'SOFT' 100;font-size:clamp(22px,3vw,28px);line-height:1.15;letter-spacing:-.01em;color:var(--color-text-primary)}
.calm-exc p{margin:0;font-size:16px;line-height:1.5;color:var(--color-text-secondary);max-width:40em}
.calm-exc .ce-a{display:flex;align-items:center;gap:14px;margin-top:16px;flex-wrap:wrap}
.calm-exc .ce-m{font-size:13px;color:var(--color-text-tertiary)}
.calm-lanes .cl-row{display:block;width:100%;height:auto;overflow:visible}
.calm-lanes .cl-name{font-size:14px;font-weight:600;fill:var(--color-text-primary)}
.calm-lanes .cl-name.q{font-weight:400;fill:var(--color-text-tertiary)}
.calm-lanes .cl-track{fill:var(--density-band-track,var(--color-bg-sunken))}
.calm-lanes .cl-tick{fill:var(--density-band-tick,var(--color-border-strong,var(--color-border-default)))}
.calm-lanes .cl-lab{font-size:13.5px;font-weight:700}.calm-lanes .cl-lab.q{font-weight:400;fill:var(--color-text-tertiary)}
.calm-lanes .cl-row+.cl-row{margin-top:4px}
.calm-lanes .cl-lab{fill:var(--color-text-secondary)}.calm-lanes .cl-lab [data-state="failed"]{fill:var(--color-status-danger-fg)}.calm-lanes .cl-lab [data-state="needs"]{fill:var(--color-text-accent)}.calm-lanes .cl-lab [data-state="running"]{fill:var(--color-text-secondary);font-weight:400}
.calm-lanes .cl-axis{display:flex;justify-content:space-between;padding:0 var(--cl-r) 0 var(--cl-l);font-size:12px;color:var(--color-text-tertiary);margin-top:4px}
.calm-lanes .cl-note{font-size:13px;color:var(--color-text-tertiary);margin:10px 0 0}
`;
export const CALM_CSS_ROWS = `
.calm-rows{list-style:none;margin:0;padding:0}
.calm-row{display:flex;align-items:center;gap:14px;min-height:var(--size-row,52px);padding:8px 4px 8px 16px;border-top:1px solid var(--color-border-subtle);box-shadow:inset 3px 0 0 var(--s-solid)}
.calm-row:last-child{border-bottom:1px solid var(--color-border-subtle)}
.calm-row .cr-t{flex:1;min-width:0;display:flex;gap:10px;align-items:baseline;white-space:nowrap;overflow:hidden}
.calm-row .cr-t b{font-weight:600;color:var(--color-text-primary);flex:none}
.calm-row .cr-t>span{color:var(--color-text-tertiary);overflow:hidden;text-overflow:ellipsis}
.calm-row .cr-age{font-size:13px;color:var(--color-text-tertiary);white-space:nowrap}
.calm-stack{display:grid;gap:var(--space-8,32px)}
.calm-stack>*,.calm-rest>*{min-width:0}
.calm-rest{display:grid}
.calm-rest>.calm-summary:last-child{border-bottom:1px solid var(--color-border-subtle)}
.calm-summary .cs-body{padding:4px 0 18px}
@media (max-width:600px){
  .calm-exc{padding:18px 18px 18px 22px}
  .calm-row{gap:10px;padding-left:14px}.calm-row .cr-age{display:none}.calm-row .cr-t{flex-direction:column;align-items:flex-start;gap:2px}.calm-row .cr-t>b{white-space:normal}.calm-row .cr-t>span{max-width:100%;font-size:14px}.calm-rows.terse .cr-t>span{display:none}
  .calm-summary>summary{flex-wrap:wrap;gap:2px 10px;padding:10px 0}.calm-summary .cs-parts{order:3;flex-basis:100%;font-size:14px}.calm-summary .cs-lead{flex:1}
  .calm-quiet{padding:40px 0 24px}
}`;
export const injectCalmCSS = () => { if (document.getElementById('calm-css')) return; const s = document.createElement('style'); s.id = 'calm-css'; s.textContent = CALM_CSS + CALM_CSS_ROWS; document.head.append(s); };
