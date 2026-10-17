// directions-v2.html: principles with before/after, direction specimens, the side-by-side grid.
import { DIRS } from './directions.js';
import { LUCIDE, STATUS } from '../icons/icons.js';

const gl = (n) => `<svg class="gl" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${STATUS[n].svg}</svg>`;
const ic = (n) => `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE[n]}</svg>`;
const varsOf = (id, t = 'light') => Object.entries({ ...DIRS[id].vars, ...DIRS[id][t] }).map(([k, v]) => `${k}:${v}`).join(';');
const AFTER = (html) => `<div class="after" style="${varsOf('daylight')}">${html}</div>`;
const BEFORE = (html) => `<div class="before">${html}</div>`;

// ---------- principles ----------
const P = [
  ['Summary first, detail on demand', 'Every screen opens by answering “does anything need me?” in one sentence. System internals, raw counts and health checks sit one click down, not across the top of every page.',
    BEFORE(`<div class="b-strip"><span>● up <b>3d 4h 12m</b></span><span>v0.71.2 → <i>v0.72.0</i></span><span>sandbox <b>enforcing</b></span><span>paseo <b>ok</b></span><span>● <b>3</b> running</span><span class="hot">◇ 6 need you</span></div><div class="b-h">The score <small>one staff per workflow</small></div><div class="b-mono">Health · daemon · paseo · kv boltdb · hoff · tf-apply · no bot verb reaches it</div>`),
    AFTER(`<p class="a-lead"><b>5 things need you.</b> Everything else is running normally: 18 of 20 runs in the last 12 hours went fine.</p><p class="a-more">${ic('chevron-right')}System health: all normal</p>`)],
  ['A plain headline, the technical detail beneath', 'The title says what will happen in words a newcomer understands. The tool, ids and diff stats follow on a quieter line, in mono, last.',
    BEFORE(`<div class="b-row"><span class="b-dia">◇</span><div><b class="b-t">terraform apply · infra@3f2a9c1d</b><span class="b-mono">platform-sre · p12 · +1 ~2 -0 · REPLACES aws_instance.web</span></div></div>`),
    AFTER(`<div class="a-item" data-s="accent"><span class="a-ng">${gl('needs-you')}</span><div><b>Replace the production web server</b><span>platform-sre wants to run terraform apply on production, for the SEV2 latency incident</span><code>p12 · infra@3f2a9c1d · +1 ~2 −0</code></div></div>`)],
  ['Three levels of type, and only three', 'Level 1 says <b>what</b> (heading or bold). Level 2 says <b>why it matters</b> (regular, secondary ink). Level 3 is the <b>machine detail</b> (mono, small, tertiary, at the end). Mono never carries prose, and the serif never carries a generated string.',
    BEFORE(`<div class="b-title">ci-fixer <small>acme/web#1288</small></div><div class="b-banner"><b>Failed at step test.</b> <span class="b-mono">go test ./...: 2 failures (TestCheckout, TestRefund)</span></div>`),
    AFTER(`<div class="a-levels"><div><span class="lv">1</span><h4>ci-fixer couldn’t fix the failing check on acme/web#1288</h4></div><div><span class="lv">2</span><p>Its change made 2 tests fail. It stopped before pushing anything, so the pull request is unchanged.</p></div><div><span class="lv">3</span><code>r-7ef0 · step test · TestCheckout, TestRefund</code></div></div>`)],
  ['Colour only for what needs attention', 'Passing runs are ink, not green. When eleven of sixteen rows are green, green means nothing, and the two red rows are harder to find. Colour is kept for failed, waiting, running and “needs you”, always with a glyph and a word.',
    BEFORE(`<table class="b-tbl">${[['ok', 'r-7f11', 'pr-review'], ['failed', 'r-7ef0', 'ci-fixer'], ['ok', 'r-7ee3', 'issue-triage'], ['waiting', 'r-7ed1', 'pr-review'], ['ok', 'r-7ec8', 'dep-bump'], ['ok', 'r-7eb2', 'bot:platform-sre']].map(([s, id, w]) => `<tr><td class="b-${s}">${s === 'ok' ? '✓ ok' : s === 'failed' ? '× failed' : '◎ waiting'}</td><td class="b-mono">${id}</td><td><b>${w}</b></td></tr>`).join('')}</table>`),
    AFTER(`<table class="a-tbl">${[['ok', 'pr-review', 'acme/api#480'], ['failed', 'ci-fixer', 'acme/web#1288', '2 tests fail after its change'], ['ok', 'issue-triage', 'acme/api#486'], ['waiting', 'pr-review', 'acme/infra#77', 'Waiting for your answer'], ['ok', 'dep-bump', 'acme/web'], ['ok', 'platform-sre', 'PagerDuty incident']].map(([s, w, t, n]) => `<tr class="${s}" data-s="${s === 'failed' ? 'danger' : s === 'waiting' ? 'warning' : 'ok'}"><td class="st">${gl(s === 'ok' ? 'success' : s === 'failed' ? 'danger' : 'warning')}</td><td><b>${w}</b> ${t}</td><td class="nt">${n ?? ''}</td></tr>`).join('')}</table>`)],
  ['Numbers line up', 'Durations, costs and counts are right-aligned in tabular figures, with units spelled out, so a column can be scanned and compared without reading every cell.',
    BEFORE(`<table class="b-tbl b-nums"><tr><th>TOOK</th><th>COST</th></tr>${[['7m', '0.41'], ['12m', '1.12'], ['1m', '0.06'], ['…', '0.38'], ['9m', '0.82'], ['14m', '0.54']].map(([a, b]) => `<tr><td class="b-mono">${a}</td><td class="b-mono">$${b}</td></tr>`).join('')}</table>`),
    AFTER(`<table class="a-tbl a-nums"><tr><th>Took</th><th>Cost</th></tr>${[['7 min', '$0.41'], ['12 min', '$1.12'], ['1 min', '$0.06'], ['…', '$0.38'], ['9 min', '$0.82'], ['14 min', '$0.54']].map(([a, b]) => `<tr><td>${a}</td><td>${b}</td></tr>`).join('')}</table>`)],
  ['Group and chunk', 'Things you must act on are separate from things that are merely true. An update that installs itself is not “needing you”. Each group gets a count and a short label, and the groups are separated by space, not boxes inside boxes.',
    BEFORE(`<div class="b-h">Needs you <small>6 items · approvals are signed with your passkey</small></div>${['terraform apply · infra@3f2a9c1d', 'slack-ops.post to #incidents', 'ci-fixer failed on acme/web#1288', 'hand-off awaiting answer · acme/infra#77', 'push approval requested by app.example.com', 'v0.72.0 available'].map((t) => `<div class="b-li">${t}</div>`).join('')}`),
    AFTER(`<h4 class="a-g">Needs you <em>5</em></h4><p class="a-li">2 approvals · 1 failed fix · 2 questions</p><h4 class="a-g muted">For your information <em>2</em></h4><p class="a-li">Update v0.72.0 installs within 10 minutes · Sandbox blocked pastebin.com</p>`)],
  ['Lead with what changed', 'Coming back after a while, you want the difference, not the full chronological feed. The digest counts what is new since your last visit and links each line to its detail.',
    BEFORE(`<div class="b-h">Activity <small>live</small></div>${['r-7f2c pr-review step <b>review</b> started', 'platform-sre wrote <b>knowledge/db-prod-1.md</b> (commit 8e1f0aa)', 'browser b-02 awaiting push approval on <b>app.example.com</b>', 'update available <b>v0.72.0</b> · preflight validate passed on a copy', 'r-7f3a ci-fixer started on <b>acme/api#482</b>'].map((t) => `<div class="b-li">${t}</div>`).join('')}`),
    AFTER(`<h4 class="a-g">Since you last looked <small>2 h ago</small></h4><ul class="a-dig"><li><b>14</b> runs passed, including 6 pull-request reviews</li><li class="bad"><b>1</b> new failure: ci-fixer on acme/web#1288</li><li class="acc"><b>1</b> new approval: replace the production web server</li><li><b>1</b> update ready: v0.72.0</li></ul>`)],
  ['Timelines tell the story', 'A run reads top to bottom as what it did and what came of each step, in sentences, with the duration on the right. The failing step says why it failed, and the steps after it say they weren’t reached, not just that they are grey.',
    BEFORE(`<div class="b-tl">${[['logs', 8, 1], ['triage', 14, 1], ['fix', 52, 1], ['test', 22, 0], ['push', 0, 2], ['comment', 0, 2]].map(([n, w, s]) => `<div><span class="b-mono">${n}</span><i class="${s === 0 ? 'f' : s === 2 ? 'n' : ''}" style="width:${w}%"></i><em>${s === 1 ? '✓' : s === 0 ? '×' : 'not reached'}</em></div>`).join('')}</div>`),
    AFTER(`<ol class="a-story">${[['ok', 'Get the failing logs', '3 failing checks found', '2 s'], ['ok', 'Fix it', 'Changed applyFee to round half-even', '7 min 40 s'], ['failed', 'Run the tests', '2 of 148 tests fail', '26 s'], ['skipped', 'Push the fix', 'Not reached', '']].map(([s, t, d, x]) => `<li class="${s}" data-s="${s === 'failed' ? 'danger' : 'ok'}">${gl(s === 'ok' ? 'success' : s === 'failed' ? 'danger' : 'skipped')}<div><b>${t}</b><span>${d}</span></div><em>${x}</em></li>`).join('')}</ol>`)],
  ['Times and schedules for humans', 'Use relative time with the exact time beside it, schedules in words and the cron string on hover. Ids and hashes are never the first thing on a line.',
    BEFORE(`<dl class="b-dl"><dt>trigger</dt><dd class="b-mono">schedule 0 6 * * 1</dd><dt>started</dt><dd class="b-mono">10/16/2026, 5:03:45 PM</dd><dt>id</dt><dd class="b-mono">r-7ef0</dd></dl>`),
    AFTER(`<dl class="a-dl"><dt>Runs</dt><dd>Mondays at 06:00</dd><dt>Started</dt><dd>52 min ago <span>(17:03)</span></dd><dt>Run</dt><dd><code>r-7ef0</code></dd></dl>`)],
  ['Whitespace that groups, density on request', 'Space does the grouping, so boxes and rules can be fewer. Comfortable is the default; compact is a token set for operators at scale, with the same content and the same order, only tighter.',
    BEFORE(`<div class="b-dense">${['ops-box · 100.88.12.40', 'NOW · Overview · Live 3 · Needs you 6', 'WORK · Run history · Workflows · Bots · Browsers 3', 'SYSTEM · Config · Secrets & vars · Packs & plugins · Connectors & hosts · Updates 1 · Logs & audit · Access'].map((t) => `<div class="b-mono">${t}</div>`).join('')}</div>`),
    AFTER(`<div class="a-dens"><div><small>Comfortable · 52px</small>${['pr-review acme/web#1291', 'ci-fixer acme/web#1288'].map((t, i) => `<p class="${i ? 'f' : ''}">${gl(i ? 'danger' : 'success')}<b>${t.split(' ')[0]}</b> ${t.split(' ')[1]}</p>`).join('')}</div><div class="cmp"><small>Compact · 34px</small>${['pr-review acme/web#1291', 'ci-fixer acme/web#1288', 'issue-triage acme/api#486'].map((t, i) => `<p class="${i === 1 ? 'f' : ''}">${gl(i === 1 ? 'danger' : 'success')}<b>${t.split(' ')[0]}</b> ${t.split(' ')[1]}</p>`).join('')}</div></div>`)],
];
document.getElementById('principle-list').innerHTML = P.map(([t, why, before, after], i) => `<article class="pr"><div class="pr-h"><span class="pn">${String(i + 1).padStart(2, '0')}</span><div><h3>${t}</h3><p>${why}</p></div></div>
  <div class="ba"><figure><figcaption class="mono">Before · prototype</figcaption>${before}</figure><figure><figcaption class="mono">After</figcaption>${after}</figure></div></article>`).join('');

// ---------- direction cards ----------
const sunrise = `<svg viewBox="0 0 160 92" width="140" aria-hidden="true"><g stroke="var(--color-border-default)" stroke-width="1.5" stroke-linecap="round">${[-62, -38, -14, 14, 38, 62].map((a) => { const r = Math.PI * (a - 90) / 180; return `<line x1="${80 + Math.cos(r) * 44}" y1="${70 + Math.sin(r) * 44}" x2="${80 + Math.cos(r) * 54}" y2="${70 + Math.sin(r) * 54}"/>`; }).join('')}</g><path d="M46 70a34 36 0 0 1 68 0h-9a25 26 0 0 0-50 0z" fill="var(--color-text-primary)"/><circle cx="80" cy="70" r="10" fill="var(--color-accent-default)"/><path d="M8 76h144" stroke="var(--color-text-primary)" stroke-width="2" stroke-linecap="round"/></svg>`;
const sparkSvg = `<svg viewBox="0 0 140 40" width="140" aria-hidden="true"><polyline points="0,30 12,22 24,26 36,14 48,28 60,32 72,12 84,8 96,20 108,22 120,16 132,4" fill="none" stroke="var(--color-text-primary)" stroke-width="1.6"/><circle cx="132" cy="4" r="2.5" fill="var(--color-text-primary)"/></svg>`;
const miniScore = `<svg viewBox="0 0 160 70" width="150" aria-hidden="true">${[0, 1, 2].map((r) => [0, 1, 2, 3, 4].map((k) => `<line x1="0" x2="160" y1="${10 + r * 22 + k * 3}" y2="${10 + r * 22 + k * 3}" stroke="var(--color-border-subtle)"/>`).join('')).join('')}<circle cx="24" cy="16" r="4" fill="var(--color-text-tertiary)"/><circle cx="70" cy="16" r="4" fill="var(--color-text-tertiary)"/><path d="M100 33l8 8M108 33l-8 8" stroke="var(--color-status-danger-solid)" stroke-width="2.4" stroke-linecap="round"/><circle cx="46" cy="60" r="4" fill="var(--color-text-tertiary)"/><path d="M146 52l7 7-7 7-7-7z" fill="var(--color-accent-default)"/><line x1="150" x2="150" y1="2" y2="70" stroke="var(--color-accent-default)" stroke-width="1.5"/></svg>`;
const ART = { daylight: sunrise, broadsheet: sparkSvg, score: miniScore };
const SW = ['bg-canvas', 'bg-surface', 'text-primary', 'text-secondary', 'accent-default'];
const ST = ['success', 'warning', 'danger', 'running', 'info', 'neutral'];
document.getElementById('dir-cards').innerHTML = Object.entries(DIRS).map(([id, D], i) => `<article class="dir-card" id="dir-${id}">
  <div class="dc-head"><span class="mono n">${['I', 'II', 'III'][i]}</span><h3>${D.name}</h3><p>${D.tag}</p>${id === 'daylight' ? '<span class="badge-rec mono">recommended</span>' : ''}</div>
  <p class="pov">${D.pov}</p>
  <div class="specs">${['light', 'dark'].map((t) => `<div class="dspec" style="${varsOf(id, t)}" data-d="${id}">
    <div class="sp-top"><span class="sp-t">${t === 'light' ? 'Light' : 'Dark'}</span>${ART[id]}</div>
    <div class="sp-disp">5 things need you</div>
    <p class="sp-ui"><b>Replace the production web server.</b> platform-sre wants to run terraform apply on production.</p>
    <code class="sp-mono">p12 · infra@3f2a9c1d · +1 ~2 −0</code>
    <div class="sp-row"><span class="sp-btn">Approve with passkey</span><span class="sp-btn sec">Deny…</span>${ST.slice(0, 3).map((s) => `<span class="sp-badge" style="color:var(--color-status-${s}-fg);background:var(--color-status-${s}-bg)">${gl({ success: 'success', warning: 'warning', danger: 'danger' }[s])}${{ success: 'Passed', warning: 'Waiting', danger: 'Failed' }[s]}</span>`).join('')}</div>
    <div class="sp-sw">${SW.map((k) => `<i style="background:var(--color-${k})" title="${k}: ${D[t]['--color-' + k]}"></i>`).join('')}<span class="gap"></span>${ST.map((s) => `<i class="dot" style="background:var(--color-status-${s}-solid)" title="${s}"></i>`).join('')}</div>
  </div>`).join('')}</div>
  <dl class="facets">
    <dt>Type</dt><dd>${Object.values(D.fonts).map(([f, l, u]) => `<b>${f}</b> <span class="lic">${l}</span> · ${u}`).join('<br>')}</dd>
    <dt>Colour</dt><dd>${id === 'daylight' ? 'Warm cream paper with espresso ink by day and warm cocoa by night. Ember only for “needs you”, and sage, honey, brick and sky only for state.' : id === 'broadsheet' ? 'Newsprint white with near-black ink, and an inky navy “night edition” (not graphite). Almost no colour: ember for needs-you, red for failures, everything else in ink.' : 'Pale lilac by day and deep indigo by night, with a dark indigo rail. Ember on indigo is a complementary pair. Notes are coloured by status only; lanes are named, not coloured.'}</dd>
    <dt>Space</dt><dd>${D.space}</dd><dt>Shape</dt><dd>${D.shape}</dd><dt>Icons, art</dt><dd>${D.icons}</dd><dt>Data</dt><dd>${D.data}</dd>
  </dl></article>`).join('');

// ---------- compare grid ----------
const SCREENS = [['overview', 'Overview', '#/overview'], ['run', 'A failed run', '#/runs/r-7ef0'], ['approval', 'Bot with the terraform approval', '#/bots/platform-sre/proposals/p12'], ['history', 'Run history', '#/runs'], ['builder', 'Workflow builder', '#/workflows/ci-fixer']];
const COLS = [['current', 'Current prototype'], ['daylight', 'Daylight'], ['broadsheet', 'Broadsheet'], ['score', 'Score']];
let vp = 'desktop', th = 'light';
function drawGrid() {
  document.getElementById('compare-grid').className = 'cgrid ' + vp;
  document.getElementById('compare-grid').innerHTML = SCREENS.map(([s, label, hash]) => `<div class="crow"><h3>${label}</h3><div class="ccols">${COLS.map(([k, name]) => {
    const src = `v2/shots/${k}-${s}-${vp}-${th}.png`;
    const live = k === 'current' ? `http://100.119.192.38:8767/index.html${th === 'light' ? '?theme=light' : ''}${hash}` : `v2/mock.html?d=${k}&s=${s}&t=${th}`;
    return `<figure class="${k === 'daylight' ? 'chosen' : ''}"><figcaption><b>${name}</b><a href="${live}" target="_blank" rel="noopener">Open live ↗</a></figcaption><img src="${src}" width="${vp === 'phone' ? 390 : 1440}" height="${vp === 'phone' ? 844 : 900}" alt="${name}: ${label}" data-cap="${name} · ${label} · ${vp} · ${th}"></figure>`;
  }).join('')}</div></div>`).join('');
}
for (const [id, set] of [['vp', (v) => (vp = v)], ['th', (v) => (th = v)]]) {
  document.getElementById(id).addEventListener('click', (e) => { const s = e.target.closest('span[data-v]'); if (!s) return; for (const x of document.querySelectorAll(`#${id} span`)) x.classList.toggle('on', x === s); set(s.dataset.v); drawGrid(); });
}
drawGrid();
const lb = document.getElementById('lb');
document.getElementById('compare-grid').addEventListener('click', (e) => { const img = e.target.closest('img'); if (!img) return; lb.querySelector('img').src = img.src; lb.querySelector('p').textContent = img.dataset.cap; lb.hidden = false; });
lb.addEventListener('click', () => { lb.hidden = true; });
addEventListener('keydown', (e) => { if (e.key === 'Escape') lb.hidden = true; });

// ---------- recommendation table ----------
const R = [['Approachable for a newcomer', [4, 3, 3]], ['Legible at a glance', [4, 4, 3]], ['Information density', [3, 4, 3]], ['Feels like a brand, not a tool', [4, 3, 4]], ['Less “engineery”', [4, 3, 3]], ['Dark mode quality', [3, 4, 4]], ['Effort to build and keep consistent', [4, 3, 2]]];
const dots = (n) => '●'.repeat(n) + `<span class="off">${'●'.repeat(4 - n)}</span>`;
document.getElementById('rec-table').innerHTML = `<thead><tr><th></th>${['Daylight', 'Broadsheet', 'Score'].map((n, i) => `<th class="${i ? '' : 'rec'}">${n}</th>`).join('')}</tr></thead><tbody>${R.map(([k, v]) => `<tr><td>${k}</td>${v.map((n, i) => `<td class="${i ? '' : 'rec'}">${dots(n)}</td>`).join('')}</tr>`).join('')}<tr class="tot"><td>Total</td>${[0, 1, 2].map((i) => `<td class="${i ? '' : 'rec'}">${R.reduce((a, [, v]) => a + v[i], 0)}<small>/28</small></td>`).join('')}</tr></tbody>`;
