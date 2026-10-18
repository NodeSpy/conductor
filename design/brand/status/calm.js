// Calm by default: the four components that keep a screen quiet until something needs you.
//   summaryLine  normal work collapses into one quiet line ("10 finished · 9 passed · 1 parked"), expandable
//   runStrip     activity over time as one aggregate: runs per hour, passed in a soft green with the
//                failures stacked on top in red and marked
//   byWorkflow   the exceptions, grouped by workflow; healthy workflows collapse to a count
//                (together: the aggregate-plus-exceptions pattern. It scales to any number of
//                workflows and runs, unlike one lane per workflow, which doesn't.)
//   quietState   the first-class empty state: one sentence, one supporting line
//   atAGlance    the overview's few key counts (needs you, failing, running, went fine, spent), each a link
//   exception    the one loud thing: large, at the top, one plain sentence, one action
// Emphasis levels (tokens: --color-emphasis-loud/normal/quiet/ghost): only "loud" items get colour.
import { mark, chartMark, STATES } from './marks.js';

export const summaryLine = ({ lead, parts = [], body = '', open = false, label = 'Show' }) =>
  `<details class="calm-summary"${open ? ' open' : ''}><summary><span class="cs-lead">${lead}</span>${parts.length ? `<span class="cs-parts">${parts.join(' · ')}</span>` : ''}<span class="cs-more">${label}</span></summary><div class="cs-body">${body}</div></details>`;

/** The quiet state: one plain sentence and one supporting line. No illustration (and never the logo). */
export const quietState = ({ title, sub = '' }) =>
  `<div class="calm-quiet" data-glance data-glance-answer="${title.replace(/"/g, '&quot;')}"><h2>${title}</h2>${sub ? `<p>${sub}</p>` : ''}</div>`;

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

/** Runs over time, aggregated: one quiet column per bucket (height = runs), the failed mark above any
 *  bucket with failures (with its count when more than one). No lanes, no legend: the marks are
 *  labelled in words beside the strip. `hours`: [[fine, failed], …], oldest first. */
export function runStrip(hours, { axis = ['12 h ago', 'now'] } = {}) {
  const svg = (width, height, size) => {
    const n = hours.length, top = 18, max = Math.max(...hours.map(([a, b]) => a + b), 1), slot = width / n, bw = Math.max(slot - 3, 2);
    let g = '';
    hours.forEach(([ok, bad], i) => {
      // Passed runs in the soft success fill; failures stacked on top in red, with a 2px gap between.
      const unit = (height - top) / max, h = (ok + bad) * unit, hOk = ok * unit, x = i * slot + (slot - bw) / 2;
      const tip = `<title>${ok + bad} runs: ${ok} passed${bad ? `, ${bad} failed` : ''}</title>`;
      if (ok) g += `<rect x="${x.toFixed(1)}" y="${(height - hOk).toFixed(1)}" width="${bw.toFixed(1)}" height="${Math.max(hOk, 1).toFixed(1)}" rx="2" class="rs-bar">${tip}</rect>`;
      // A non-zero failure count is never thinner than 4 units, so one red run among many still shows.
      const hBad = Math.max(bad * unit - (ok ? 2 : 0), 4);
      if (bad) g += `<rect x="${x.toFixed(1)}" y="${(height - hOk - (ok ? 2 : 0) - hBad).toFixed(1)}" width="${bw.toFixed(1)}" height="${hBad.toFixed(1)}" rx="2" class="rs-fail">${tip}</rect>`;
      if (bad) {
        const cx = x + bw / 2, cy = height - hOk - (ok ? 2 : 0) - Math.max(bad * unit - (ok ? 2 : 0), 4) - 9;
        g += chartMark('failed', cx, cy, size, `${bad} failed`).replace('<svg ', '<svg data-labelled="1" ');
        if (bad > 1) g += `<text x="${(cx + 8).toFixed(1)}" y="${(cy + 4).toFixed(1)}" class="rs-n">${bad}</text>`;
      }
    });
    return g;
  };
  const total = hours.reduce((a, [x, y]) => a + x + y, 0), failed = hours.reduce((a, [, y]) => a + y, 0);
  const label = `${total} runs over the last ${hours.length} hours, ${failed} failed`;
  // Two drawings of the same data, so marks stay legible: a wide one, and a narrow one for phones.
  return `<figure class="run-strip" data-item><svg class="rs-d" viewBox="0 0 860 64" role="img" aria-label="${label}">${svg(860, 64, 12)}</svg><svg class="rs-p" viewBox="0 0 340 60" role="img" aria-label="${label}">${svg(340, 60, 11)}</svg>
    <figcaption><span>${axis[0]}</span><b>${total} runs · ${mark('failed')} ${failed} failed</b><span>${axis[1]}</span></figcaption></figure>`;
}

/** Exceptions grouped by workflow. `groups`: [{ workflow, runs, items: [excRow args…] }]; `healthy`:
 *  [[workflow, runs], …], collapsed into one summary line that opens to the names and counts. */
export const byWorkflow = (groups, healthy = []) => `<div class="by-wf">${groups.map((g) => `<section class="bw-g"><h4><b>${g.workflow}</b><span>${g.items.length} of ${g.runs} runs</span></h4>${excRows(g.items)}</section>`).join('')}
  ${healthy.length ? summaryLine({ lead: `${healthy.length} workflows healthy`, parts: [`${healthy.reduce((a, [, r]) => a + r, 0)} runs, all fine`], label: 'Show', body: `<ul class="bw-ok">${healthy.map(([w, r]) => `<li><b>${w}</b><span>${r}</span></li>`).join('')}</ul>` }) : ''}</div>`;

/** At a glance: a handful of key counts for the overview, each linking to its list. Ink only; the
 *  list each count opens carries the colour. `items`: [[value, label, sub?], …]. */
export const atAGlance = (items) => `<dl class="at-glance">${items.map(([v, l, sub]) => `<div><dt>${l}</dt><dd><b>${v}</b>${sub ? `<span>${sub}</span>` : ''}</dd></div>`).join('')}</dl>`;

export const CALM_CSS = `
.at-glance{display:grid;grid-template-columns:repeat(auto-fit,minmax(118px,1fr));gap:0;margin:0;border-top:1px solid var(--color-border-subtle);border-bottom:1px solid var(--color-border-subtle)}
.at-glance>div{padding:12px 14px 12px 0}.at-glance>div+div{padding-left:14px;border-left:1px solid var(--color-border-subtle)}
.at-glance dt{font-size:13px;font-weight:600;color:var(--color-text-secondary)}
.at-glance dd{margin:2px 0 0;display:flex;align-items:baseline;gap:6px}
.at-glance dd b{font-family:var(--font-family-serif);font-weight:600;font-variation-settings:'SOFT' 100;font-size:26px;line-height:1.1;color:var(--color-text-primary)}
.at-glance dd span{font-size:13px;color:var(--color-text-tertiary)}
@media (max-width:600px){.at-glance{grid-template-columns:repeat(3,1fr)}.at-glance>div:nth-child(4){padding-left:0;border-left:0}.at-glance>div:nth-child(n+4){border-top:1px solid var(--color-border-subtle)}}
.run-strip{margin:0}.run-strip>svg{display:block;width:100%;height:auto;overflow:visible}.run-strip>.rs-p{display:none}@media (max-width:600px){.run-strip>.rs-d{display:none}.run-strip>.rs-p{display:block}}
.run-strip .rs-bar{fill:var(--run-strip-bar,var(--color-status-success-chart))}.run-strip .rs-fail{fill:var(--run-strip-failed,var(--color-status-danger-solid))}
.run-strip .rs-n{font-size:11.5px;font-weight:700;fill:var(--color-status-danger-fg)}
.run-strip figcaption{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-top:6px;font-size:12px;color:var(--color-text-tertiary)}
.run-strip figcaption b{display:inline-flex;align-items:center;gap:6px;font-size:13px;font-weight:600;color:var(--color-text-secondary)}
.by-wf .bw-g{margin-top:18px}.by-wf h4{display:flex;align-items:baseline;gap:10px;margin:0 0 6px;font-size:15px}
.by-wf h4 b{font-weight:700;color:var(--color-text-primary)}.by-wf h4 span{font-size:13px;font-weight:400;color:var(--color-text-tertiary)}
.by-wf > .calm-summary{margin-top:18px}
.bw-ok{list-style:none;margin:0;padding:0;display:flex;flex-wrap:wrap;gap:6px 18px}.bw-ok li{display:inline-flex;gap:6px;font-size:14px;color:var(--color-text-secondary)}.bw-ok b{font-weight:600;color:var(--color-text-primary)}
.calm-summary{border-top:1px solid var(--color-border-subtle);}
.calm-summary>summary{list-style:none;cursor:pointer;display:flex;align-items:baseline;gap:10px;min-height:var(--summary-line-height,48px);align-items:center;color:var(--color-text-secondary);font-size:var(--type-body-m-size,15px)}
.calm-summary>summary::-webkit-details-marker{display:none}
.calm-summary .cs-lead{color:var(--color-text-primary);font-weight:600}
.calm-summary .cs-parts{color:var(--color-text-secondary);min-width:0;flex:0 1 auto;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.calm-summary .cs-more{margin-left:auto;font-size:13px;font-weight:600;color:var(--color-text-tertiary)}
.calm-summary[open] .cs-more{visibility:hidden}
.calm-quiet{padding:var(--quiet-state-pad,64px) 0 40px;max-width:40rem}
.calm-quiet h2{font-family:var(--font-family-serif);font-weight:600;font-variation-settings:'SOFT' 100;font-size:clamp(30px,5vw,44px);line-height:1.1;letter-spacing:-.015em;margin:0;color:var(--color-text-primary)}
.calm-quiet p{margin:10px 0 0;font-size:17px;color:var(--color-text-secondary)}
.calm-exc{position:relative;background:var(--exception-bg,var(--color-bg-surface));border-radius:var(--exception-radius,14px);box-shadow:var(--exception-shadow,var(--shadow-2)),inset var(--exception-edge,4px) 0 0 var(--s-solid);padding:var(--exception-pad,22px) var(--exception-pad,22px) var(--exception-pad,22px) calc(var(--exception-pad,22px) + 4px)}
.calm-exc .ce-k{display:flex;align-items:center;gap:8px;font-size:13.5px;font-weight:700;color:var(--s-fg)}
.calm-exc h3{margin:8px 0 6px;font-family:var(--font-family-serif);font-weight:600;font-variation-settings:'SOFT' 100;font-size:clamp(22px,3vw,28px);line-height:1.15;letter-spacing:-.01em;color:var(--color-text-primary)}
.calm-exc p{margin:0;font-size:16px;line-height:1.5;color:var(--color-text-secondary);max-width:40em}
.calm-exc .ce-a{display:flex;align-items:center;gap:14px;margin-top:16px;flex-wrap:wrap}
.calm-exc .ce-m{font-size:13px;color:var(--color-text-tertiary)}
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
