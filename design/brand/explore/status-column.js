// explore/status-column.html: alternatives to the stock "tinted capsule + tinted row" status
// column, on the same 16 runs, in Daylight light and dark. Everything is drawn from tokens.css.
import { HISTORY } from '../v2/data.js';
import { LUCIDE } from '../icons/icons.js';

const ROWS = HISTORY[0][1].map(([st, wf, target, by, when, took, cost, id, note]) => ({ st, wf, target, by, when, took, cost, id, note }));
// Where each run is in its workflow: [steps total, reached step (1-based), step names]
const TRACE = {
  'r-7f3a': [6, 3], 'r-7f2c': [3, 2], 'r-7f29': [1, 1], 'r-7ef0': [6, 4], 'r-7ed1': [3, 2], 'r-7ea0': [6, 1], 'r-7e64': [3, 2],
};
const STEPS = { 'ci-fixer': 6, 'pr-review': 3, 'issue-triage': 2, 'dep-bump': 3, 'platform-sre': 1, 'release-notes': 3 };
const OUTCOME = {
  ok: (r) => `Passed in ${r.took}`,
  failed: (r) => r.id === 'r-7ef0' ? 'Failed at step 4, “Run the tests”: 2 tests fail after its change. Nothing was pushed.' : 'Stopped at step 2, “Draft”: it hit its $0.05 budget.',
  running: (r) => ({ 'r-7f3a': 'Running · step 3 of 6, “Fix it”', 'r-7f2c': 'Running · step 2 of 3, reviewing', 'r-7f29': 'Running · investigating' })[r.id],
  waiting: () => 'Waiting for your answer: approve, revise or discard its draft review.',
  parked: () => 'Parked: the PR has the hold label',
};
const ATTN = (r) => r.st === 'failed' || r.st === 'waiting';
const lu = (n, cls = '') => `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE[n]}</svg>`;

// Filled status shapes (not outline icons in pills): distinct silhouettes that survive one colour.
const SHAPE = {
  ok: '<circle cx="6" cy="6" r="3.2" fill="currentColor"/>',
  failed: '<path d="M2.6 2.6l6.8 6.8M9.4 2.6l-6.8 6.8" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"/>',
  waiting: '<path d="M6 .8L11.2 6 6 11.2.8 6z" fill="currentColor"/>',
  running: '<circle cx="6" cy="6" r="4" fill="none" stroke="currentColor" stroke-width="2" stroke-dasharray="17 8" class="spin"/>',
  parked: '<path d="M4 2.5v7M8 2.5v7" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/>',
};
const shape = (st, cls = '') => `<svg class="sh ${cls}" viewBox="0 0 12 12" aria-hidden="true">${SHAPE[st]}</svg>`;
const WORD = { ok: 'Passed', failed: 'Failed', running: 'Running', waiting: 'Needs you', parked: 'Parked' };
const S = { ok: 'ok', failed: 'danger', running: 'running', waiting: 'accent', parked: 'neutral' };

// The run's step trace: one mark per step. Done steps are ink ticks, the failing step a red
// cross, a waiting step an ember diamond, the running step an arc, unreached steps hollow.
function trace(r) {
  const n = STEPS[r.wf] ?? 1;
  const [, at] = TRACE[r.id] ?? [n, n];
  const cells = Array.from({ length: n }, (_, i) => {
    const k = i + 1;
    let c = 'done';
    if (r.st !== 'ok') c = k < at ? 'done' : k === at ? r.st : 'todo';
    return `<i class="t ${c}" data-s="${S[c] ?? ''}">${c === 'done' || c === 'todo' ? '' : shape(c)}</i>`;
  }).join('');
  return `<span class="trace" role="img" aria-label="${r.st === 'ok' ? `all ${n} steps passed` : `step ${at} of ${n}: ${WORD[r.st].toLowerCase()}`}">${cells}</span>`;
}

const head = (cols) => `<thead><tr>${cols.map(([t, c = '']) => `<th class="${c}">${t}</th>`).join('')}</tr></thead>`;
const runCell = (r, extra = '') => `<td class="run"><b>${r.wf}</b> <span class="tgt">${r.target}</span>${extra}</td>`;
const tail = (r) => `<td class="sub">${r.when}</td><td class="num">${r.took || '…'}</td><td class="num">${r.cost}</td><td class="id"><code>${r.id}</code></td>`;

// 0. Before: the stock pattern (outline icon in a tinted capsule, plus a tinted row)
const before = () => `<table class="t0">${head([['Status'], ['Run'], ['When'], ['Took', 'num'], ['Cost', 'num'], ['ID']])}<tbody>${ROWS.map((r) => `<tr class="${r.st}">
  <td><span class="cap" data-s="${S[r.st] === 'accent' ? 'warning' : S[r.st]}">${lu({ ok: 'check', failed: 'x', running: 'refresh-cw', waiting: 'triangle-alert', parked: 'pause' }[r.st])}${{ ok: 'Passed', failed: 'Failed', running: 'Running', waiting: 'Waiting on you', parked: 'Parked' }[r.st]}</span></td>
  ${runCell(r)}${tail(r)}</tr>`).join('')}</tbody></table>`;

// A. Margin mark: no capsule; passing rows are silent; attention rows get an edge mark and their reason in words.
const margin = () => `<table class="tA">${head([['', 'edge'], ['Run'], ['Status'], ['When'], ['Took', 'num'], ['Cost', 'num'], ['ID']])}<tbody>${ROWS.map((r) => `<tr class="${r.st}" data-s="${S[r.st]}">
  <td class="edge"></td>${runCell(r, r.note && r.st !== 'parked' ? `<span class="why">${r.note}</span>` : '')}
  <td class="st"><span class="in">${shape(r.st)}<span>${WORD[r.st]}</span></span></td>${tail(r)}</tr>`).join('')}</tbody></table>`;

// B. Outcome sentence: the status column becomes what happened, in words. Colour only on the verb that needs you.
const outcome = () => `<table class="tB">${head([['Run'], ['Outcome'], ['When'], ['Cost', 'num'], ['ID']])}<tbody>${ROWS.map((r) => `<tr class="${r.st}" data-s="${S[r.st]}">
  ${runCell(r)}<td class="oc"><span class="in">${shape(r.st)}<span>${OUTCOME[r.st](r)}</span></span></td><td class="sub">${r.when}</td><td class="num">${r.cost}</td><td class="id"><code>${r.id}</code></td></tr>`).join('')}</tbody></table>`;

// C. Step trace: the status column shows where in the workflow each run is, and where it stopped.
const traced = () => `<table class="tC">${head([['Run'], ['Steps'], [''], ['When'], ['Took', 'num'], ['Cost', 'num'], ['ID']])}<tbody>${ROWS.map((r) => `<tr class="${r.st}" data-s="${S[r.st]}">
  ${runCell(r)}<td class="tr">${trace(r)}</td><td class="lbl">${r.st === 'ok' ? '' : `<span>${WORD[r.st]}</span>`}</td>${tail(r)}</tr>`).join('')}</tbody></table>`;

// D. Grouped + trace (recommended): runs that need a person are lifted into their own group with a
// plain sentence each; everything else sits below in quiet ink. Emphasis by position, not paint.
function grouped() {
  const attn = ROWS.filter(ATTN), live = ROWS.filter((r) => r.st === 'running'), rest = ROWS.filter((r) => !ATTN(r) && r.st !== 'running');
  const row = (r, rich) => `<tr class="${r.st}" data-s="${S[r.st]}">${runCell(r, rich ? `<span class="why">${OUTCOME[r.st](r)}</span>` : '')}<td class="tr">${trace(r)}</td>${tail(r)}${rich ? `<td class="act"><button class="c-btn ${r.st === 'waiting' ? 'primary' : 'secondary'} sm" tabindex="-1">${r.st === 'waiting' ? 'Answer' : 'Open'}</button></td>` : '<td class="act"></td>'}</tr>`;
  const grp = (title, n, rows, rich, cls) => `<tbody class="${cls}"><tr class="gh"><td colspan="7"><b>${title}</b><span>${n}</span></td></tr>${rows.map((r) => row(r, rich)).join('')}</tbody>`;
  return `<table class="tD">${head([['Run'], ['Steps'], ['When'], ['Took', 'num'], ['Cost', 'num'], ['ID'], ['', 'act']])}
    ${grp('Needs attention', attn.length, attn, true, 'g-attn')}${grp('Running now', live.length, live, false, 'g-live')}${grp('Finished', rest.length, rest, false, 'g-done')}</table>`;
}

const VARIANTS = [
  ['before', 'Before: tinted capsule and tinted row', 'The stock pattern (the prototype’s, and round two’s mocks too): every row carries a coloured pill with an outline icon, and failing rows are washed in red. Five colours compete, the passing majority is as loud as the failure, and the tint muddies text contrast.', before],
  ['margin', 'A · Margin mark', 'The capsule goes. Passing rows show a small ink dot and the word, at the same weight as the rest of the row. A row that needs attention gets a 4px edge in its colour, its status word in that colour, and its reason in words under the run name. There is no fill anywhere.', margin],
  ['outcome', 'B · Outcome sentence', 'The status column becomes “what happened”, in a sentence: “Passed in 7 min”, “Failed at step 4, ‘Run the tests’”, “Waiting for your answer on a draft”. Only the sentences that need you take colour. It is the most readable for a newcomer, but it needs a wide column.', outcome],
  ['trace', 'C · Step trace', 'The column shows each run’s steps as a short row of marks: done steps as ink ticks, the failing step as a red cross, a waiting step as an ember diamond, a running step as an arc, and unreached steps hollow. You see not just that a run failed but where, and how far the others got. It is the densest and most conductor-specific.', traced],
  ['grouped', 'D · Grouped, with the trace (recommended)', 'Runs that need a person are lifted out of the list into their own group, each with a plain sentence and one action. Running work follows, then everything that finished, in quiet ink. The step trace stays as the status column. Emphasis comes from position and words, not from tinting rows, so the table stays calm at 500 rows.', grouped],
];

document.getElementById('variants').innerHTML = VARIANTS.map(([id, title, why, fn]) => `<section class="var" id="${id}">
  <h2>${title}</h2><p class="why-p">${why}</p>
  <div class="pair">${['light', 'dark'].map((t) => `<div class="panel v-${id}" data-theme="${t}"><span class="tag">${t}</span><div class="scroll">${fn()}</div></div>`).join('')}</div></section>`).join('');
document.getElementById('legend').innerHTML = ['ok', 'failed', 'waiting', 'running', 'parked'].map((s) => `<span data-s="${S[s]}">${shape(s)}${WORD[s]}</span>`).join('');
