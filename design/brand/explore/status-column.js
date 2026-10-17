// explore/status-column.html: alternatives to the stock "tinted capsule + tinted row" status
// column, on the same 16 runs, in Daylight light and dark. Everything is drawn from tokens.css.
import { HISTORY } from '../v2/data.js';
import { LUCIDE } from '../icons/icons.js';
import { mark, status, legend, injectMarkCSS } from '../status/marks.js';
import { calmLanes, injectCalmCSS } from '../status/calm.js';
import { LANES } from '../v2/data.js';
injectMarkCSS(); injectCalmCSS();

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

// Run states → the shared vocabulary (status/marks.js).
const STATE = { ok: 'passed', failed: 'failed', running: 'running', waiting: 'needs', parked: 'parked' };
const shape = (st) => mark(STATE[st] ?? st);
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


// CHOSEN, calm by default: the same grouping, but only the rows that need someone are drawn. Each is one
// line (mark and word, the run, the reason, when, one action). Running and Finished are each one
// summary line that opens in place; took, cost and ids live inside, never in the default view.
const CALM_REASON = { 'r-7ef0': '2 tests fail after its change', 'r-7ed1': 'Approve, revise or discard its draft review', 'r-7e64': 'Stopped at its $0.05 budget' };
function calm() {
  const attn = ROWS.filter(ATTN), live = ROWS.filter((r) => r.st === 'running'), rest = ROWS.filter((r) => !ATTN(r) && r.st !== 'running');
  const passed = rest.filter((r) => r.st === 'ok').length, parked = rest.length - passed;
  const row = (r) => `<tr class="${r.st} attn" data-s="${S[r.st]}"><td class="st">${status(STATE[r.st])}</td>${runCell(r)}<td class="why-c">${CALM_REASON[r.id] ?? r.note}</td><td class="sub">${r.when}</td><td class="act"><button class="c-btn ${r.st === 'waiting' ? 'primary' : 'secondary'} sm" tabindex="-1">${r.st === 'waiting' ? 'Answer' : 'Open'}</button></td></tr>`;
  const quiet = (r) => `<tr class="${r.st}" hidden><td class="st">${status(STATE[r.st])}</td>${runCell(r)}<td class="sub">${[r.took, r.cost, `<code>${r.id}</code>`].filter(Boolean).join(' · ')}</td><td class="sub">${r.when}</td><td class="act"></td></tr>`;
  const sum = (lead, parts, rows, cls) => `<tbody class="${cls}"><tr class="sum"><td colspan="5"><b>${lead}</b><span class="parts">${parts}</span><span class="more">Show</span></td></tr>${rows.map(quiet).join('')}</tbody>`;
  return `<table class="tX calm">${head([['Status'], ['Run'], [''], ['When'], ['', 'act']])}
    <tbody class="g-attn"><tr class="gh"><td colspan="5"><b>Needs attention</b><span>${attn.length}</span></td></tr>${attn.map(row).join('')}</tbody>
    ${sum(`${live.length} running`, live.map((r) => r.wf).join(', '), live, 'g-live')}${sum(`${rest.length} finished`, `${passed} passed · ${parked} parked`, rest, 'g-done')}</table>`;
}
const lanesCalm = () => `<div class="lanes-calm">${calmLanes(LANES, { width: 860 })}</div>`;

// BEFORE CALM (kept for the record): D's grouping + A's margin marks, every row drawn. Rows that need someone first (a thin leading edge, the
// reason in words, one action), then what is running, then what finished in quiet ink. Status is a
// mark plus a word from the one vocabulary. No step trace in lists: step detail is on the run page.
function chosen() {
  const attn = ROWS.filter(ATTN), live = ROWS.filter((r) => r.st === 'running'), rest = ROWS.filter((r) => !ATTN(r) && r.st !== 'running');
  const row = (r, rich) => `<tr class="${r.st}${rich ? ' attn' : ''}" data-s="${S[r.st]}">${runCell(r, rich ? `<span class="why">${OUTCOME[r.st](r)}</span>` : r.st === 'running' ? `<span class="sub-l">${OUTCOME.running(r).replace('Running · ', '')}</span>` : '')}<td class="st">${status(STATE[r.st])}</td>${tail(r)}<td class="act">${rich ? `<button class="c-btn ${r.st === 'waiting' ? 'primary' : 'secondary'} sm" tabindex="-1">${r.st === 'waiting' ? 'Answer' : 'Open'}</button>` : ''}</td></tr>`;
  const grp = (title, n, rows, rich, cls) => `<tbody class="${cls}"><tr class="gh"><td colspan="7"><b>${title}</b><span>${n}</span></td></tr>${rows.map((r) => row(r, rich)).join('')}</tbody>`;
  return `<table class="tX">${head([['Run'], ['Status'], ['When'], ['Took', 'num'], ['Cost', 'num'], ['ID'], ['', 'act']])}
    ${grp('Needs attention', attn.length, attn, true, 'g-attn')}${grp('Running now', live.length, live, false, 'g-live')}${grp('Finished', rest.length, rest, false, 'g-done')}</table>`;
}

// The run detail page carries the step detail lists no longer show: a collapsible tree. A step that
// calls another workflow is one node with its own status; it opens to that workflow's steps.
const TREE = [
  ['passed', 'Get the failing logs', '3 failing checks found', '2 s'],
  ['passed', 'Decide what failed', 'A test failure (91% sure)', '8 s'],
  ['passed', 'Fix it', 'Calls workflow fix-and-verify', '7 min 40 s', [
    ['passed', 'Reproduce locally', 'TestRefund fails on main', '40 s'],
    ['passed', 'Patch applyFee', 'Rounds half-even now', '6 min 12 s'],
    ['passed', 'Lint', 'No issues', '48 s'],
  ], 'fix-and-verify'],
  ['failed', 'Run the tests', 'Calls workflow test-suite: 2 of 148 tests fail', '26 s', [
    ['failed', 'Unit tests', 'TestCheckout, TestRefund fail', '21 s'],
    ['passed', 'Lint', 'No issues', '3 s'],
    ['skipped', 'End-to-end tests', 'Not reached', ''],
  ], 'test-suite'],
  ['skipped', 'Push the fix', 'Not reached', ''],
  ['skipped', 'Comment on the PR', 'Not reached', ''],
];
function treeNode([st, title, sub, took, kids, wf], i, depth = 0) {
  const n = depth ? '' : `<span class="tn">${i + 1}</span>`;
  const line = `<span class="tl">${n}${mark(st, { label: true })}<span class="tt"><b>${title}</b><span>${sub}</span></span>${wf ? `<span class="wf">${LUCIDE.workflow ? `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE.workflow}</svg>` : ''}${wf} · ${kids.length} steps</span>` : ''}<em>${took}</em></span>`;
  if (!kids) return `<li class="leaf ${st}" data-state="${st}">${line}</li>`;
  return `<li class="${st}" data-state="${st}"><details ${st === 'failed' ? 'open' : ''}><summary>${line}</summary><ol class="kids">${kids.map((k, j) => treeNode(k, j, depth + 1)).join('')}</ol></details></li>`;
}
const tree = () => `<div class="run-tree"><div class="rt-h"><b>ci-fixer</b> acme/web#1288 ${status('failed', 'Failed at step 4')}<span class="sub-l">12 min · 52 min ago</span></div><ol class="tree">${TREE.map((t, i) => treeNode(t, i)).join('')}</ol>
  <p class="tree-note">Nested workflow calls collapse to a single step with their own status. The failing path opens by itself; everything else stays closed.</p></div>`;

const VARIANTS = [
  ['chosen', 'Chosen: calm by default', 'The decision (D’s grouping with A’s margin marks), made calm. Only the runs that need someone are drawn: one line each, with a 3px edge, the mark and word, the reason and one action. <b>Running</b> and <b>Finished</b> are each one summary line (“10 finished · 9 passed · 1 parked”) that opens in place, and took, cost and ids live inside it. Passed is a quiet sage dot and parked is slate pause bars, so they never look alike when opened. See <a href="../system.html#scannable">Scannable by default (it replaces Calm by default)</a>.', calm],
  ['lanes', 'Where lanes appear: the density band', 'On the Activity page and anywhere runs are drawn over time, passing and parked runs are faint ticks in a band, and only failures, things that need you and running work are marks. Each lane says in words what its marks are, so no legend is needed.', lanesCalm],
  ['tree', 'Run detail: the step tree', 'Workflows can have many steps and can call other workflows, so the number of steps is unpredictable and has no place in a list. On the run page they are a collapsible tree: one line per step with its mark, what it did and how long it took. A nested workflow call is one node with its own status, and the failing path opens on its own.', tree],
  ['grouped-all', 'Explored · Chosen before calm: every row drawn', 'The first version of the decision: grouped rows with margin marks, but every run drawn at equal weight. The review: “all of it just looks so busy… I have to read everything all the time.” It became the calm version above.', chosen],
  ['before', 'Explored · Before: tinted capsule and tinted row', 'The stock pattern (the prototype’s, and round two’s mocks too): every row carries a coloured pill with an outline icon, and failing rows are washed in red. Five colours compete, the passing majority is as loud as the failure, and the tint muddies text contrast.', before],
  ['margin', 'Explored · A: margin mark', 'The capsule goes. Passing rows show a small ink dot and the word, at the same weight as the rest of the row. A row that needs attention gets a 4px edge in its colour, its status word in that colour, and its reason in words under the run name. There is no fill anywhere.', margin],
  ['outcome', 'Explored · B: outcome sentence', 'The status column becomes “what happened”, in a sentence: “Passed in 7 min”, “Failed at step 4, ‘Run the tests’”, “Waiting for your answer on a draft”. Only the sentences that need you take colour. It is the most readable for a newcomer, but it needs a wide column.', outcome],
  ['trace', 'Explored · C: step trace', 'The column shows each run’s steps as a short row of marks: done steps as ink ticks, the failing step as a red cross, a waiting step as an ember diamond, a running step as an arc, and unreached steps hollow. You see not just that a run failed but where, and how far the others got. It is the densest and most conductor-specific.', traced],
  ['grouped', 'Explored · D: grouped, with the step trace', 'The grouping was kept, but the trace was dropped: workflows can have many steps and can call other workflows, so the number of marks per row is unpredictable.', grouped],
];

document.getElementById('variants').innerHTML = VARIANTS.map(([id, title, why, fn]) => `<section class="var" id="${id}">
  <h2>${title}</h2><p class="why-p">${why}</p>
  <div class="pair">${['light', 'dark'].map((t) => `<div class="panel v-${id}" data-theme="${t}"><span class="tag">${t}</span><div class="scroll">${fn()}</div></div>`).join('')}</div></section>`).join('');
document.getElementById('legend').innerHTML = legend(['passed', 'running', 'failed', 'needs', 'parked', 'skipped']);
// Summary lines open in place.
document.addEventListener('click', (e) => {
  const sum = e.target.closest('tr.sum'); if (!sum) return;
  const open = sum.classList.toggle('open');
  for (const r of sum.parentElement.querySelectorAll('tr:not(.sum)')) r.hidden = !open;
});
