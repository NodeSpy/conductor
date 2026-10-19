// Renders one hi-fi mock: mock.html?d=daylight|broadsheet|score&s=overview|run|approval|history|builder|activity|bot&t=light|dark
// Daylight is calm by default (status/calm.js); &v=before renders the earlier, busy version of the
// same data for comparison, and &day=quiet renders the overview on a day when nothing needs you.
// The markup is shared; directions differ by token values (directions.js) and, where the
// direction's data presentation differs, by the small per-direction variants below.
import { DIRS } from './directions.js';
import * as C from './data.js';
import { LUCIDE, STATUS } from '../icons/icons.js';
import { STATES, GLYPH_STATE, mark, chartMark, status, legend, injectMarkCSS } from '../status/marks.js';
import { summaryLine, quietState, exception, excRows, runStrip, byWorkflow, atAGlance, injectCalmCSS } from '../status/calm.js';
import { injectScanCSS } from '../status/rows.js';
import * as SCAN from '../scannable/screens.js';
import * as CUR from './current.js';
injectMarkCSS(); injectCalmCSS();

const q = new URLSearchParams(location.search);
const d = q.get('d') || 'daylight', s = q.get('s') || 'overview', theme = q.get('t') || 'light';
const D = DIRS[d];
// Daylight renders the current system (v2/current.js) by default. Archived drafts, kept for the record
// and listed on archive/index.html: &v=before (the first busy version), &v=calm (the calm round) and
// &v=scan (the scannable proposal).
const V = q.get('v') ?? '';
const scan = d === 'daylight' && V === 'scan';
const calm = d === 'daylight' && V === 'calm', quietDay = q.get('day') === 'quiet';
const current = d === 'daylight' && !V;
if (current) { injectScanCSS(); const st = document.createElement('style'); st.textContent = CUR.CURRENT_CSS + SCAN.SCAN_SCREEN_CSS; document.head.append(st); }
if (scan) { injectScanCSS(); const st = document.createElement('style'); st.textContent = SCAN.SCAN_SCREEN_CSS; document.head.append(st); }
const root = document.documentElement;
root.dataset.d = d; root.dataset.theme = theme;
root.style.colorScheme = `only ${theme}`; // the forced theme always wins over the OS and UA auto-darkening
// Daylight takes its colours from tokens.css (the shipped system); the other directions keep their own values.
for (const [k, v] of Object.entries({ ...D.vars, ...(d === 'daylight' ? {} : D[theme]) })) root.style.setProperty(k, v);
if (calm) root.dataset.calm = '';

// ---------- helpers ----------
const ic = (n, cls = '') => `<svg class="ic ${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="var(--d-icon-stroke)" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE[n] ?? ''}</svg>`;
const glLegacy = (n, cls = '') => `<svg class="gl ${cls}" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${(STATUS[n] ?? STATUS.info).svg}</svg>`;
// Status glyphs come from the one vocabulary (status/marks.js).
const gl = (n, cls = '') => (GLYPH_STATE[n] ? mark(GLYPH_STATE[n], { size: 16 }) : glLegacy(n, cls));
// run state → vocabulary state
const ST = { ok: 'passed', failed: 'failed', running: 'running', waiting: 'needs', parked: 'parked', skipped: 'skipped', approval: 'needs' };
const SG = { ok: 'success', failed: 'danger', running: 'running', waiting: 'warning', parked: 'neutral', skipped: 'skipped', approval: 'needs-you' };
const SS = { ok: 'ok', failed: 'danger', running: 'running', waiting: 'warning', parked: 'neutral', skipped: 'neutral', approval: 'accent' };
const SL = { ok: 'Passed', failed: 'Failed', running: 'Running', waiting: 'Waiting on you', parked: 'Parked', skipped: 'Not reached' };
const badge = (st, label = SL[st]) => status(ST[st], label);
const logoMark = `<svg class="logo" viewBox="3 7 26 18" aria-hidden="true"><path fill="currentColor" d="M3 21a13 14 0 0 1 26 0h-3.2a9.8 10 0 0 0-19.6 0z"/><circle cx="16" cy="21" r="4" fill="var(--color-accent-default)"/></svg>`;
const spark = (vals, w = 96, h = 26, cls = '') => {
  const mx = Math.max(...vals), mn = Math.min(...vals, 0);
  const pts = vals.map((v, i) => `${(i / (vals.length - 1)) * w},${h - 2 - ((v - mn) / (mx - mn || 1)) * (h - 4)}`).join(' ');
  return `<svg class="spark ${cls}" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" aria-hidden="true"><polyline points="${pts}" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/><circle cx="${w}" cy="${pts.split(' ').pop().split(',')[1]}" r="2.2" fill="currentColor"/></svg>`;
};

// ---------- shell ----------
const NAV = [['layout-grid', 'Overview', 'overview'], ['bell', 'Needs you', 'needs', quietDay ? 0 : current || scan ? 6 : 5], ['activity', 'Activity', 'activity'], ['workflow', 'Workflows', 'builder'], ['bot', 'Bots', 'approval'], ['globe', 'Browsers', 'browsers', '']];
// One System area: configuration, credentials, extensions, updates, the audit log and access.
const NAV2 = [['settings-2', 'Config'], ['key-round', 'Secrets'], ['package', 'Packs'], ['plug', 'Plugins'], ['refresh-cw', 'Updates', 1], ['scroll-text', 'Logs & audit'], ['shield-check', 'Access']];
const activeOf = { overview: 'overview', run: 'activity', history: 'activity', approval: 'approval', builder: 'builder', activity: 'activity', bot: 'approval', perms: 'approval', picker: 'builder' };
function shell(body, title) {
  const act = activeOf[s];
  const navItem = ([i, l, k, n]) => `<a class="${k === act ? 'on' : ''}">${ic(i)}<span>${l}</span>${n ? `<em class="${k === 'needs' ? 'hot' : ''}">${n}</em>` : ''}</a>`;
  if (d === 'broadsheet') {
    return `<div class="app">
      <header class="masthead"><div class="mh-top"><span class="brand">${logoMark}<b>conductor</b></span><span class="dateline">${C.NOW} · ops-box · <span class="okline">all systems normal</span></span><span class="mh-search">${ic('search')}Search runs, bots, settings <kbd>⌘K</kbd></span><span class="avatar">op</span></div>
      <nav class="sections">${NAV.map(navItem).join('')}<span class="sp"></span>${NAV2.slice(0, 3).map(([i, l]) => `<a>${l}</a>`).join('')}<a>Settings</a></nav></header>
      <main class="page">${body}</main>${tabbar(act)}</div>`;
  }
  return `<div class="app">
    <aside class="rail"><div class="brand">${logoMark}<b>conductor</b></div>
      <nav>${NAV.map(navItem).join('')}<span class="grp">System</span>${NAV2.map(([i, l, n]) => navItem([i, l, l, n])).join('')}</nav>
      <div class="host">${calm ? '' : '<span class="dot" data-s="ok"></span>'}<div><b>ops-box</b><span>Healthy · up 3 days</span></div></div></aside>
    <div class="main"><header class="topbar"><span class="ph-title">${logoMark}<b>${title}</b></span><span class="search">${ic('search')}Search runs, bots and settings<kbd>⌘K</kbd></span><span class="sp"></span>${calm ? '' : `<span class="health">${gl('success')}All systems normal</span>`}<span class="avatar">op</span></header>
    <main class="page">${body}</main></div>${tabbar(act)}</div>`;
}
const tabbar = (act) => `<nav class="tabbar">${[['layout-grid', 'Overview', 'overview'], ['bell', 'Needs you', 'needs', quietDay ? 0 : current || scan ? 6 : 5], ['activity', 'Activity', 'activity'], ['bot', 'Bots', 'approval'], ['ellipsis', 'More', 'more']].map(([i, l, k, n]) => `<a class="${k === act ? 'on' : ''}">${ic(i)}${n ? `<em>${n}</em>` : ''}<span>${l}</span></a>`).join('')}</nav>`;

// ---------- pieces ----------
const needItem = (n, i) => `<li class="need" data-s="${n.s}">
  <span class="ng">${gl(n.glyph)}</span>
  <div class="nt"><b>${n.title}</b><span class="why">${n.why}</span><span class="tech mono">${n.tech}</span></div>
  <span class="age">${n.age}</span>
  <span class="na"><button class="btn ${n.s === 'accent' ? 'primary' : 'secondary'} sm">${n.act}</button></span></li>`;
const needsCard = (limit = 5) => `<section class="card needs"><header class="ch"><h2>Needs you</h2><span class="count-pill">${C.NEEDS.length}</span><span class="sp"></span><a class="link">See all</a></header>
  <ul class="list">${C.NEEDS.slice(0, limit).map(needItem).join('')}</ul></section>`;
const runningCard = () => `<section class="card"><header class="ch"><h2>Running now</h2><span class="sub">${C.RUNNING.length}</span></header>
  <ul class="list running">${C.RUNNING.map((r) => `<li><span class="ng" data-s="running">${gl('running', 'spin')}</span><div class="nt"><b>${r.doing}</b><span class="why">${r.wf} · ${r.target}</span></div><div class="prog"><span>Step ${r.step} of ${r.of}</span><i><b style="width:${(r.step / r.of) * 100}%"></b></i></div></li>`).join('')}</ul></section>`;
const fyiCard = () => `<section class="card fyi"><header class="ch"><h2>For your information</h2></header><ul class="list">${C.FYI.map((f) => `<li><span class="ng" data-s="${f.s}">${f.glyph === 'shield' ? ic('shield-check') : f.glyph === 'update' ? ic('refresh-cw') : gl(f.glyph)}</span><div class="nt"><b>${f.title}</b><span class="why">${f.why}</span></div></li>`).join('')}</ul></section>`;
function hourBars(w = 300, h = 96) {
  const n = C.HOURLY.length, bw = w / n - 6, mx = 4;
  const bars = C.HOURLY.map(([ok, f], i) => { const x = i * (w / n) + 3; const ho = (ok / mx) * (h - 22), hf = (f / mx) * (h - 22); return `<rect x="${x}" y="${h - 18 - ho}" width="${bw}" height="${Math.max(ho - (hf ? 2 : 0), 0)}" rx="2" fill="var(--color-border-default)"/>${f ? `<rect x="${x}" y="${h - 18 - ho - hf}" width="${bw}" height="${hf - 1}" rx="2" fill="var(--color-status-danger-solid)"/>` : ''}`; }).join('');
  return `<svg class="hours" viewBox="0 0 ${w} ${h}" role="img" aria-label="Runs per hour, last 12 hours: 2 failures">${bars}<line x1="0" x2="${w}" y1="${h - 17}" y2="${h - 17}" stroke="var(--color-border-default)"/><text x="0" y="${h - 3}" class="ax">12 h ago</text><text x="${w}" y="${h - 3}" class="ax" text-anchor="end">now</text></svg>`;
}
function scoreSVG(w = 980, hrs = 12, compact = false) {
  const lh = compact ? 30 : 36, top = 8, left = compact ? 92 : 120, H = top + C.LANES.length * lh + 26, span = hrs * 60;
  const x = (min) => left + (1 - min / span) * (w - left - 16);
  let g = '';
  for (let hh = 0; hh <= hrs; hh += (compact ? 3 : 2)) { const xx = x(hh * 60); g += `<line x1="${xx}" x2="${xx}" y1="${top}" y2="${H - 22}" class="gridl"/><text x="${xx}" y="${H - 6}" class="ax" text-anchor="middle">${hh === 0 ? 'now' : hh + ' h'}</text>`; }
  C.LANES.forEach(([name, notes], i) => {
    const y = top + i * lh + lh / 2;
    g += `<text x="0" y="${y + 4}" class="lane-l">${name}</text>${[0, 1, 2, 3, 4].map((k) => `<line x1="${left}" x2="${w - 16}" y1="${y - 8 + k * 4}" y2="${y - 8 + k * 4}" class="staff"/>`).join('')}`;
    for (const [min, st] of notes) { if (min <= span) g += chartMark(ST[st], x(min), y, st === 'ok' ? 10 : 14, `${name}: ${STATES[ST[st]].word.toLowerCase()}`); }
  });
  g += `<line x1="${x(0)}" x2="${x(0)}" y1="${top - 4}" y2="${H - 22}" stroke="var(--color-accent-default)" stroke-width="1.5"/>`;
  return `<svg class="score-svg" viewBox="0 0 ${w} ${H}" role="img" aria-label="Runs per workflow over the last ${hrs} hours">${g}</svg>`;
}
const scoreLegend = legend(['passed', 'running', 'failed', 'needs', 'parked']);

// ---------- screens ----------
function overview() {
  const sumLine = `<b>5 things need you.</b> Everything else is running normally: ${C.STATS.fine} of ${C.STATS.runs} runs in the last 12 hours went fine.`;
  if (d === 'daylight') return `
    <section class="hello"><div><h1>Good afternoon, ops.</h1><p class="lead">${sumLine}</p></div></section>
    <div class="cols"><div class="col-main">${needsCard()}</div>
    <div class="col-side">${runningCard()}<section class="card"><header class="ch"><h2>Last 12 hours</h2></header><div class="stat-line"><b>${C.STATS.fine}</b><span>of ${C.STATS.runs} runs went fine</span><b class="r">${C.STATS.cost}</b><span>spent</span></div>${hourBars()}<p class="foot-note">${gl('danger')}2 failed: ci-fixer at 17:03, release-notes at 10:55</p></section>${fyiCard()}</div></div>`;
  if (d === 'broadsheet') return `
    <section class="front"><div class="kicker">Front page · last 12 hours</div><h1>5 things need you</h1><p class="dek">Two approvals, a fix that didn’t work and two questions. Everything else ran normally: 18 of 20 runs went fine.</p></section>
    <section class="statrow">
      <div><span class="lbl">Runs</span><b>20</b>${spark(C.HOURLY.map(([a, b]) => a + b))}</div>
      <div><span class="lbl">Went fine</span><b>90%</b><span class="d">18 of 20</span></div>
      <div class="alert"><span class="lbl">Failed</span><b>2</b><span class="d">ci-fixer · release-notes</span></div>
      <div><span class="lbl">Running</span><b>3</b><span class="d">ci-fixer · pr-review · platform-sre</span></div>
      <div><span class="lbl">Spent, 14 days</span><b>$8.40</b>${spark(C.STATS.costTrend)}<span class="d">today</span></div>
    </section>
    <div class="cols"><div class="col-main">${needsCard()}</div><div class="col-side">
      
      ${runningCard()}</div></div>`;
  return `
    <section class="ov-head"><h1>Overview</h1><p class="lead">${sumLine}</p></section>
    <section class="card score" data-legend-scope><header class="ch"><h2>The last 12 hours</h2><span class="sub">every run, one lane per workflow</span><span class="sp"></span>${scoreLegend}</header>${scoreSVG(1060, 12)}<div class="score-phone">${scoreSVG(360, 6, true)}</div></section>
    <div class="cols"><div class="col-main">${needsCard()}</div><div class="col-side">${runningCard()}${fyiCard()}</div></div>`;
}

function runPage() {
  const R = C.RUN;
  const head = `<div class="crumbs"><a>Activity</a>${ic('chevron-right')}<a>ci-fixer</a></div>
    <div class="phead"><div><h1 class="h1-long">${R.title}</h1><p class="lead">${R.summary}</p></div>${badge('failed')}</div>
    <div class="actions"><button class="btn primary">${ic('refresh-cw')}Retry “Fix it” with a note</button><button class="btn secondary">${ic('git-pull-request')}Open the pull request</button><button class="btn ghost">Dismiss</button></div>`;
  const total = 460;
  const dur = { '2 s': 2, '8 s': 8, '7 min 40 s': 460 - 36, '26 s': 26 };
  let off = 0;
  const steps = d === 'daylight' ? runTreeDaylight() : d === 'score'
    ? `<section class="card"><header class="ch"><h2>What happened</h2><span class="sub">12 minutes, step by step</span></header><div class="gantt">${R.steps.map(([n, st, t, note]) => { const w = st === 'skipped' ? 0 : Math.max(((dur[t] || 1) / total) * 100, 1.2); const l = off; off += w; return `<div class="g-row" data-s="${SS[st]}"><span class="g-l">${n}</span><span class="g-track">${st === 'skipped' ? `<em class="g-skip">not reached</em>` : `<b style="left:${l}%;width:${w}%"></b>`}</span><span class="g-n">${t ? `<b>${t}</b>` : ''}${note}</span></div>`; }).join('')}<div class="g-axis"><span>17:03</span><span>17:15</span></div></div></section>`
    : d === 'broadsheet'
      ? `<section class="card"><header class="ch"><h2>What happened</h2><span class="sub">6 steps · 12 minutes</span></header><table class="tbl"><thead><tr><th>Step</th><th>Result</th><th class="num">Took</th><th class="bar-h"></th><th>Note</th></tr></thead><tbody>${R.steps.map(([n, st, t, note]) => `<tr class="${st}"><td><b>${n}</b></td><td>${badge(st)}</td><td class="num">${t || '—'}</td><td class="bar-c">${st === 'skipped' ? '' : `<i style="width:${Math.max(((dur[t] || 1) / total) * 100, 2)}%" data-s="${SS[st]}"></i>`}</td><td class="sub">${note}</td></tr>`).join('')}</tbody></table></section>`
      : `<section class="card"><header class="ch"><h2>What happened</h2><span class="sub">12 minutes</span></header><ol class="story">${R.steps.map(([n, st, t, note]) => `<li data-s="${SS[st]}" class="${st}"><span class="sg">${gl(SG[st])}</span><div><b>${n}</b><span>${note}</span></div><span class="t">${t}</span></li>`).join('')}</ol></section>`;
  const out = `<section class="card"><header class="ch"><h2>Why the tests failed</h2><span class="sub">2 of 148 tests</span></header>
    <ul class="fails">${R.failures.map(([t, f, m]) => `<li>${gl('danger')}<div><b>${t}</b><span>${m}</span></div><span class="mono src">${f}</span></li>`).join('')}</ul>
    <details class="log" open><summary>${ic('chevron-right')}Test output <span class="sub">7 lines · stdout</span></summary><pre class="code">${R.log.map(([c, a, p, v, cls], i) => `<span class="ln ${c}"><i>${i + 1}</i>${p ? `<span class="k">${a}</span>${p}<span class="${cls}">${v}</span>` : a}</span>`).join('')}</pre></details></section>`;
  const facts = [['Started', `${R.started} <span class="sub">(${R.at})</span>`], ['Took', R.took], ['Cost', R.cost], ['Triggered by', R.trigger], ['Run', `<span class="mono">${R.id}</span>`], ['Record', `${gl('success')} Signature verified`]];
  const side = `<section class="card facts"><header class="ch"><h2>Details</h2></header><dl>${facts.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('')}</dl></section>`;
  return `${head}<div class="cols"><div class="col-main">${steps}${out}</div><div class="col-side">${side}</div></div>`;
}

function approvalPage() {
  const P = C.PROPOSAL;
  const head = `<div class="crumbs"><a>Bots</a>${ic('chevron-right')}<a>work / platform</a></div>
    <div class="bothead"><span class="bot-av">ps</span><div><h1>${P.bot}</h1><p class="lead">${P.role}</p></div><span class="badge" data-s="running">${gl('running', 'spin')}${P.status}</span></div>
    <nav class="tabs"><a class="on">Approvals <em class="hot">2</em></a><a>Chat</a><a>Cases <em>4</em></a><a>Memory</a><a>What it can do</a><a>Settings</a></nav>`;
  const card = `<article class="card proposal"><div class="kick">${gl('needs-you')}Needs you · asked 8 minutes ago · ${P.expires.toLowerCase()}</div>
    <h2 class="ptitle">${P.title}</h2><p class="ask">${P.ask}</p>
    <div class="pwhy"><span class="lbl">Why</span><p>${P.why}</p></div>
    <div class="changes">${P.changes.map(([l, n, st]) => `<div data-s="${n ? st : 'neutral'}"><b>${n}</b><span>${l}</span></div>`).join('')}<p class="chg-note">${gl('warning')}<span><b>Replaces 1 server</b> (the web server). Stateful things such as databases are never replaced without a separate rule.</span></p></div>
    <dl class="risk">${P.risk.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('')}</dl>
    <p class="origin">${ic('zap')}${P.origin}</p>
    <div class="decide"><button class="btn primary lg">${ic('fingerprint')}Approve with passkey</button><button class="btn secondary lg">Deny…</button><span class="hint">Only operators on the platform team can approve.</span></div>
    <details class="tech"><summary>${ic('chevron-right')}Technical details <span class="sub">plan, hashes, exact command</span></summary><dl>${P.tech.map(([k, v]) => `<dt>${k}</dt><dd class="mono">${v}</dd>`).join('')}</dl></details></article>`;
  const can = `<section class="card can"><header class="ch"><h2>What ${P.bot} can do</h2></header>
    ${[['Does on its own', 'success', P.can.own], ['Asks you first', 'accent', P.can.asks], ['Never', 'danger', P.can.never]].map(([t, st, xs]) => `<div class="cang" data-s="${st}"><h3>${gl(st === 'success' ? 'success' : st === 'accent' ? 'needs-you' : 'danger')}${t}</h3><ul>${xs.map((x) => `<li>${x}</li>`).join('')}</ul></div>`).join('')}</section>`;
  const tl = `<section class="card"><header class="ch"><h2>Timeline</h2></header><ol class="story mini"><li data-s="neutral"><span class="sg">${gl('success')}</span><div><b>Proposed by platform-sre</b><span>while handling the PagerDuty incident</span></div><span class="t">8 min</span></li><li data-s="accent"><span class="sg">${gl('needs-you')}</span><div><b>Waiting for your decision</b><span>expires in 23 h 52 min</span></div><span class="t">now</span></li></ol></section>`;
  return `${head}<div class="cols"><div class="col-main">${card}</div><div class="col-side">${can}${tl}</div></div>`;
}

function historyPage() {
  const rows = C.HISTORY[0][1];
  if (d === 'daylight') return historyDaylight(`<div class="phead"><div><h1>Runs</h1><p class="lead"><b>Last 12 hours: 16 runs.</b> 3 need attention, 3 are running and 10 finished.</p></div><button class="btn secondary">${ic('external-link')}Export</button></div><div class="filters"><span class="field">${ic('search')}Search repo, PR or run</span><span class="chip">${ic('workflow')}Any workflow</span><span class="chip">${ic('clock')}Last 12 hours</span></div>`);
  const head = `<div class="phead"><div><h1>Runs</h1><p class="lead"><b>Last 12 hours: 16 runs.</b> 11 went fine, 2 failed, 3 are running and 1 is waiting on you.</p></div><button class="btn secondary">${ic('external-link')}Export</button></div>
    <div class="filters"><div class="seg"><span class="on">All <em>16</em></span><span>Needs attention <em>3</em></span><span>Failed <em>2</em></span><span>Running <em>3</em></span></div><span class="field">${ic('search')}Search repo, PR or run</span><span class="chip">${ic('workflow')}Any workflow</span><span class="chip">${ic('clock')}Last 12 hours</span></div>`;
  const span = 12 * 60;
  const mins = (t) => { const m = t.match(/(\d+) (min|h)/); return m ? (+m[1]) * (m[2] === 'h' ? 60 : 1) : 0; };
  const tookMin = (t) => { const m = t.match(/(\d+) min/); return m ? +m[1] : 2; };
  const lane = (r) => { const st = mins(r[4]); const l = (1 - st / span) * 100; const w = r[0] === 'running' ? (st / span) * 100 : (tookMin(r[5]) / span) * 100; return `<span class="mini-lane" data-s="${SS[r[0]]}"><b style="left:${l}%;width:${Math.max(w, 1.4)}%"></b></span>`; };
  const table = `<section class="card flush"><table class="tbl runs"><thead><tr><th class="w-st"></th><th>Run</th><th>Started by</th><th>When</th>${d === 'score' ? '<th class="lane-h">12 h ago → now</th>' : ''}<th class="num">Took</th><th class="num">Cost</th><th>Note</th><th class="id-h">ID</th></tr></thead><tbody>
    ${d === 'broadsheet' ? `<tr class="day"><td colspan="8"><b>Today</b> · 16 runs · 2 failed ${spark(C.HOURLY.map(([a, b]) => a + b), 80, 16)}</td></tr>` : ''}
    ${rows.map((r) => `<tr class="${r[0]}"><td class="w-st" data-s="${SS[r[0]]}">${gl(SG[r[0]], r[0] === 'running' ? 'spin' : '')}<span class="sr">${SL[r[0]]}</span></td><td><b>${r[1]}</b> <span class="tgt">${r[2]}</span></td><td class="sub">${r[3]}</td><td class="sub nowrap">${r[4]}</td>${d === 'score' ? `<td>${lane(r)}</td>` : ''}<td class="num">${r[5] || '<span class="sub">…</span>'}</td><td class="num">${r[6]}</td><td class="note">${r[8] ? `<span data-s="${SS[r[0]]}" class="note-t">${r[8]}</span>` : ''}</td><td class="mono id">${r[7]}</td></tr>`).join('')}
  </tbody></table></section>
  <ul class="list runs-phone">${rows.map((r) => `<li class="${r[0]}"><span class="ng" data-s="${SS[r[0]]}">${gl(SG[r[0]], r[0] === 'running' ? 'spin' : '')}</span><div class="nt"><b>${r[1]} <span class="tgt">${r[2]}</span></b><span class="why">${r[8] || r[3]}</span></div><span class="age">${r[4].replace(' ago', '')}<br><span class="num">${r[5] || ''}</span></span></li>`).join('')}</ul>`;
  return head + table;
}

function builderPage() {
  const W = C.WORKFLOW;
  const TYPE = { action: ['plug', 'Action'], decide: ['filter', 'AI decision'], agent: ['bot', 'AI agent'], code: ['terminal', 'Command'] };
  const head = `<div class="crumbs"><a>Workflows</a>${ic('chevron-right')}<a>${W.folder}</a></div>
    <div class="phead"><div><h1>${W.name}</h1><p class="lead">${W.desc}</p></div><div class="actions tight"><button class="btn ghost">${ic('undo-2')}Undo</button><button class="btn secondary">${ic('play')}Test</button><button class="btn primary">Save and apply</button></div></div>
    <nav class="tabs"><a class="on">Recipe</a><a>Config file</a><a>Runs <em>9</em></a><a>Settings</a></nav>`;
  const when = `<div class="when"><span class="wl">${ic('zap')}When</span><div><b>${W.when}</b> <span>${W.where},</span> <span>${W.unless}</span></div><button class="btn ghost sm">Edit</button></div>`;
  const steps = W.steps.map(([id, t, sub, ty, cond], i) => `${cond ? `<div class="cond">${ic('filter')}${cond}</div>` : ''}<div class="step" data-t="${ty}"><span class="sn">${i + 1}</span><div class="st"><b>${t}</b><span>${sub}</span></div><span class="ty">${ic(TYPE[ty][0])}${TYPE[ty][1]}</span><span class="mono sid">${id}</span>${ic('chevron-right', 'chev')}</div>`).join('');
  const recipe = `<section class="card recipe">${when}<div class="flow">${steps}<button class="add">${ic('plus')}Add a step</button></div></section>`;
  const side = `<section class="card"><header class="ch"><h2>Try it</h2></header><p class="sub">Run every step against a recent event. Anything that would change something is a dry run.</p><div class="field sel">${ic('git-pull-request')}${W.testEvent}</div><button class="btn secondary wide">${ic('play')}Run a test</button></section>
    <section class="card facts"><header class="ch"><h2>At a glance</h2></header><dl><dt>Budget</dt><dd>${W.budget}</dd><dt>Last 24 h</dt><dd>${W.recent}</dd><dt>Owner</dt><dd>your config</dd></dl></section>
    <section class="card"><header class="ch"><h2>Config file</h2><span class="sp"></span><a class="link">Open</a></header><pre class="code small">${W.yaml.map(([k, v], i) => `<span class="ln"><i>${i + 1}</i><span class="k">${k}</span>:${v ? ' ' + `<span class="s">${v}</span>` : ''}</span>`).join('')}</pre></section>`;
  return `${head}<div class="cols"><div class="col-main">${recipe}</div><div class="col-side">${side}</div></div>`;
}


// ---------- Daylight: the chosen status column (grouped rows + margin marks) ----------
// Edge and reason colour follow the mark's state (needs you is ember, never the amber of “waiting”).
const EDGE = { failed: 'danger', waiting: 'accent' };
function historyDaylight(head) {
  const rows = C.HISTORY[0][1];
  const attn = rows.filter((r) => r[0] === 'failed' || r[0] === 'waiting'), live = rows.filter((r) => r[0] === 'running'), done = rows.filter((r) => !attn.includes(r) && !live.includes(r));
  const REASON = { 'r-7ef0': 'Failed at step 4, “Run the tests”: 2 tests fail after its change.', 'r-7ed1': 'Waiting for your answer: approve, revise or discard its draft review.', 'r-7e64': 'Stopped at step 2: it hit its $0.05 budget.' };
  const row = (r, rich) => `<tr class="${r[0]}${rich ? ' attn' : ''}" data-s="${EDGE[r[0]] ?? SS[r[0]]}"><td class="run"><b>${r[1]}</b> <span class="tgt">${r[2]}</span>${rich ? `<span class="reason">${REASON[r[7]] ?? r[8]}</span>` : r[8] ? `<span class="sub-l">${r[8]}</span>` : ''}</td><td class="st">${status(ST[r[0]])}</td><td class="sub nowrap">${r[4]}</td><td class="num">${r[5] || '<span class="sub">…</span>'}</td><td class="num">${r[6]}</td><td class="mono id">${r[7]}</td><td class="act">${rich ? `<button class="btn ${r[0] === 'waiting' ? 'primary' : 'secondary'} sm">${r[0] === 'waiting' ? 'Answer' : 'Open'}</button>` : ''}</td></tr>`;
  const grp = (title, list, rich, cls) => `<tbody class="${cls}"><tr class="gh"><td colspan="7"><b>${title}</b><span>${list.length}</span></td></tr>${list.map((r) => row(r, rich)).join('')}</tbody>`;
  const table = `<section class="card flush"><table class="tbl runs grouped"><thead><tr><th>Run</th><th>Status</th><th>When</th><th class="num">Took</th><th class="num">Cost</th><th class="id-h">ID</th><th></th></tr></thead>
    ${grp('Needs attention', attn, true, 'g-attn')}${grp('Running now', live, false, 'g-live')}${grp('Finished', done, false, 'g-done')}</table></section>`;
  const phone = (title, list, rich) => `<h3 class="ph-g">${title} <span>${list.length}</span></h3><ul class="list runs-phone">${list.map((r) => `<li class="${r[0]}${rich ? ' attn' : ''}" data-s="${EDGE[r[0]] ?? SS[r[0]]}"><div class="nt"><b>${r[1]} <span class="tgt">${r[2]}</span></b>${rich ? `<span class="reason">${REASON[r[7]] ?? r[8]}</span>` : ''}${status(ST[r[0]])}</div><span class="age">${r[4].replace(' ago', '')}</span></li>`).join('')}</ul>`;
  return head + table + `<div class="phone-groups">${phone('Needs attention', attn, true)}${phone('Running now', live)}${phone('Finished', done)}</div>`;
}
// The run page carries the step detail: a collapsible tree; nested workflow calls are one node.
function runTreeDaylight() {
  const T = [
    ['passed', 'Get the failing logs', '3 failing checks found', '2 s'],
    ['passed', 'Decide what failed', 'A test failure (91% sure, 6% flaky)', '8 s'],
    ['passed', 'Fix it', 'Calls workflow fix-and-verify', '7 min 40 s', [['passed', 'Reproduce locally', 'TestRefund fails on main', '40 s'], ['passed', 'Patch applyFee', 'Rounds half-even now', '6 min 12 s'], ['passed', 'Lint', 'No issues', '48 s']], 'fix-and-verify'],
    ['failed', 'Run the tests', 'Calls workflow test-suite: 2 of 148 tests fail', '26 s', [['failed', 'Unit tests', 'TestCheckout, TestRefund fail', '21 s'], ['passed', 'Lint', 'No issues', '3 s'], ['skipped', 'End-to-end tests', 'Not reached', '']], 'test-suite'],
    ['skipped', 'Push the fix', 'Not reached', ''],
    ['skipped', 'Comment on the PR', 'Not reached', ''],
  ];
  const node = ([st, title, sub, took, kids, wf], i, depth = 0) => {
    const line = `<span class="tl">${depth ? '' : `<span class="tn">${i + 1}</span>`}${mark(st, { label: true, size: 14 })}<span class="tt"><b>${title}</b><span>${sub}</span></span>${wf ? `<span class="wf">${ic('workflow')}${wf} · ${kids.length} steps</span>` : ''}<em>${took}</em></span>`;
    return kids ? `<li class="${st}"><details ${st === 'failed' ? 'open' : ''}><summary>${line}</summary><ol class="kids">${kids.map((k, j) => node(k, j, depth + 1)).join('')}</ol></details></li>` : `<li class="${st}">${line}</li>`;
  };
  return `<section class="card"><header class="ch"><h2>What happened</h2><span class="sub">6 steps · 12 minutes</span></header><ol class="tree">${T.map((t, i) => node(t, i)).join('')}</ol></section>`;
}

// ---------- the busy “before” versions of the two screens added for the calm comparison ----------
function activityBefore() {
  const rows = C.HISTORY[0][1];
  return `<div class="phead"><div><h1>Activity</h1><p class="lead"><b>Last 12 hours: ${rows.length} runs.</b> Every run, newest first.</p></div><div class="filters"><span class="chip">${ic('clock')}Last 12 hours</span></div></div>
    <div class="cols"><div class="col-main"><section class="card"><header class="ch"><h2>Runs per hour</h2></header>${hourBars(560, 110)}</section>
    <section class="card"><header class="ch"><h2>Every run</h2><span class="sub">${rows.length}</span></header><ul class="list">${rows.map((r) => `<li><span class="ng" data-s="${SS[r[0]]}">${gl(SG[r[0]], r[0] === 'running' ? 'spin' : '')}</span><div class="nt"><b>${r[1]} <span class="tgt">${r[2]}</span></b><span class="why">${r[8] || r[3]}</span><span class="tech mono">${r[7]} · ${r[6]}</span></div><span class="age">${r[4]}</span></li>`).join('')}</ul></section></div>
    <div class="col-side">${runningCard()}</div></div>`;
}
function botBefore() {
  const B = C.BOT;
  const head = `<div class="crumbs"><a>Bots</a>${ic('chevron-right')}<a>${B.folder}</a></div>
    <div class="bothead"><span class="bot-av">ps</span><div><h1>${B.name}</h1><p class="lead">${B.role}</p></div><span class="badge" data-s="running">${gl('running', 'spin')}${B.status}</span></div>
    <nav class="tabs"><a class="on">Overview</a><a>Approvals <em class="hot">1</em></a><a>Chat</a><a>Cases <em>4</em></a><a>Memory <em>${B.memory}</em></a><a>What it can do</a><a>Settings</a></nav>`;
  const stats = `<section class="statrow bot-stats">${B.stats.map(([l, v, sub], i) => `<div class="${i === 2 ? 'alert acc' : ''}"><span class="lbl">${l}</span><b>${v}</b><span class="d">${sub}</span>${i === 0 ? spark(B.week, 80, 22) : ''}</div>`).join('')}</section>`;
  const pend = `<section class="card needs"><header class="ch"><h2>Waiting on you</h2><span class="count-pill">1</span></header><ul class="list">${B.pending.map((p) => `<li class="need" data-s="accent"><span class="ng">${gl('needs-you')}</span><div class="nt"><b>${p.title}</b><span class="why">${p.why}</span><span class="tech mono">p12 · infra@3f2a9c1d</span></div><span class="age">${p.age}</span><span class="na"><button class="btn primary sm">Review</button></span></li>`).join('')}</ul></section>`;
  const runs = `<section class="card flush"><header class="ch pad"><h2>Recent runs</h2><span class="sp"></span><a class="link">All runs</a></header><table class="tbl runs"><thead><tr><th>Status</th><th>Run</th><th>When</th><th class="num">Took</th><th class="num">Cost</th><th class="id-h">ID</th></tr></thead><tbody>${B.runs.map((r) => `<tr><td>${badge(r[0])}</td><td><b>${r[1]}</b> <span class="tgt">${r[2]}</span></td><td class="sub nowrap">${r[3]}</td><td class="num">${r[4] || '…'}</td><td class="num">${r[5]}</td><td class="mono id">${r[6]}</td></tr>`).join('')}</tbody></table></section>`;
  const cases = `<section class="card"><header class="ch"><h2>Cases</h2><span class="sub">4</span></header><ul class="list">${B.cases.map(([st, id, t, w]) => `<li><span class="ng" data-s="${SS[st]}">${gl(SG[st])}</span><div class="nt"><b>${t}</b><span class="why"><span class="mono">${id}</span> · ${w}</span></div></li>`).join('')}</ul></section>`;
  const conns = `<section class="card"><header class="ch"><h2>Connections</h2></header><ul class="list">${B.connections.map(([n, a]) => `<li><span class="ng" data-s="ok">${gl('success')}</span><div class="nt"><b>${n}</b><span class="why">${a} · healthy</span></div></li>`).join('')}</ul></section>`;
  const P = C.PROPOSAL;
  const can = `<section class="card can"><header class="ch"><h2>What it can do</h2></header>${[['Does on its own', 'success', P.can.own], ['Asks you first', 'accent', P.can.asks], ['Never', 'danger', P.can.never]].map(([t, st, xs]) => `<div class="cang" data-s="${st}"><h3>${gl(st === 'success' ? 'success' : st === 'accent' ? 'needs-you' : 'danger')}${t}</h3><ul>${xs.map((x) => `<li>${x}</li>`).join('')}</ul></div>`).join('')}</section>`;
  return `${head}${stats}<div class="cols"><div class="col-main">${pend}${runs}${cases}</div><div class="col-side">${conns}${can}</div></div>`;
}

// ---------- Daylight, calm by default ----------
// Each calm screen leads with its glance answer (data-glance): the largest, first text on the page,
// a sentence a person gets in three seconds. One loud thing; everything normal is one quiet line.
const glanceHead = ({ over = '', title, answer = title, lead = '' }) =>
  `<header class="calm-head">${over ? `<p class="overline">${over}</p>` : ''}<h1 data-glance data-glance-answer="${answer.replace(/"/g, '&quot;')}">${title}</h1>${lead ? `<p class="lead">${lead}</p>` : ''}</header>`;
const NEED_STATE = { approval: 'needs', failed: 'failed', question: 'needs', signin: 'needs' };
const KICK = { approval: 'Needs your approval', failed: 'Failed', question: 'Needs your answer', signin: 'Needs your sign-in' };
const needRow = (n) => ({ state: NEED_STATE[n.kind], title: n.title, sentence: n.short ?? n.why, age: n.age, action: `<button class="btn secondary sm">${n.act}</button>` });
const runningLine = (list) => summaryLine({ lead: `${list.length} running`, parts: [list.map((r) => r.wf).join(', ')], label: 'Show',
  body: `<ul class="list running">${list.map((r) => `<li><span class="ng" data-s="running">${gl('running', 'spin')}</span><div class="nt"><b>${r.doing}</b><span class="why">${r.wf} · ${r.target}</span></div><div class="prog"><span>Step ${r.step} of ${r.of}</span><i><b style="width:${(r.step / r.of) * 100}%"></b></i></div></li>`).join('')}</ul>` });
// Runs per hour, [fine, failed], from [workflow, [[minutesAgo, state]]] data.
const hourly = (lanes) => { const h = Array.from({ length: 12 }, () => [0, 0]); for (const [, n] of lanes) for (const [m, st] of n) if (m < 720) h[11 - Math.floor(m / 60)][st === 'failed' ? 1 : 0]++; return h; };
const healthLine = () => summaryLine({ lead: 'All healthy', parts: ['ops-box', '6 connections', 'up 3 days'], label: 'Show',
  body: `<ul class="plain">${['ops-box: up 3 days, 41% disk', 'GitHub, Slack, Datadog, Terraform Cloud, PagerDuty, OpenAI: connected'].map((x) => `<li>${x}</li>`).join('')}</ul>` });
const fyiLine = () => summaryLine({ lead: `${C.FYI.length} notes`, parts: ['update v0.72.0 is ready', 'sandbox blocked pastebin.com'], label: 'Show',
  body: `<ul class="plain">${C.FYI.map((f) => `<li><b>${f.title}.</b> ${f.why}</li>`).join('')}</ul>` });

function overviewCalm() {
  if (quietDay) {
    return `${quietState({ title: 'All quiet. Nothing needs you.', sub: '46 runs went fine in the last 12 hours. 2 are running now.' })}
      ${atAGlance([[0, 'Need you'], [0, 'Failed', '12 h'], [2, 'Running'], ['46 of 46', 'Went fine', '12 h'], ['$6.90', 'Spent', 'today']])}
      <div class="calm-rest">${summaryLine({ lead: 'Last 12 hours', parts: ['46 runs, all fine', '$6.90 spent'], body: runStrip(hourly(C.QUIET_LANES)), label: 'Show' })}
      ${runningLine(C.RUNNING.slice(0, 2))}${healthLine()}</div>`;
  }
  const [top, ...rest] = C.NEEDS;
  const loud = exception({ state: NEED_STATE[top.kind], kicker: `${KICK[top.kind]} · ${top.age}`, title: top.title, sentence: 'platform-sre wants to run terraform apply on production to fix the SEV2 latency incident.', action: `<button class="btn primary">${top.act}</button>` });
  return `${glanceHead({ title: `${C.NEEDS.length} things need you`, answer: `${C.NEEDS.length} things need you, starting with the production web server`, lead: 'Start with the first. Everything else is running normally.' })}
    ${atAGlance([[C.NEEDS.length, 'Need you'], [C.STATS.failed, 'Failed', '12 h'], [C.RUNNING.length, 'Running'], [`${C.STATS.fine} of ${C.STATS.runs}`, 'Went fine', '12 h'], [C.STATS.cost, 'Spent', 'today']])}
    <div class="calm-stack">${loud}
    <div>${excRows(rest.slice(0, 2).map(needRow), { terse: true })}${summaryLine({ lead: `${rest.length - 2} more need you`, parts: rest.slice(2).map((n) => n.title.replace(/^./, (c) => c.toLowerCase())), label: 'Show', body: excRows(rest.slice(2).map(needRow), { terse: true }) })}</div>
    <div class="calm-rest">${summaryLine({ lead: `${C.STATS.fine} of ${C.STATS.runs} runs went fine`, parts: ['last 12 hours', `${C.STATS.cost} spent`], body: runStrip(C.HOURLY), label: 'Show' })}
      ${runningLine(C.RUNNING)}${healthLine()}${fyiLine()}</div></div>`;
}

const REASON = { 'r-7ef0': '2 tests fail after its change; nothing was pushed', 'r-7ed1': 'Approve, revise or discard its draft review', 'r-7e64': 'Stopped at its $0.05 budget' };
function historyCalm() {
  const rows = C.HISTORY[0][1];
  const attn = rows.filter((r) => r[0] === 'failed' || r[0] === 'waiting'), live = rows.filter((r) => r[0] === 'running'), done = rows.filter((r) => !attn.includes(r) && !live.includes(r));
  const passed = done.filter((r) => r[0] === 'ok').length, parked = done.length - passed;
  const failed = attn.filter((r) => r[0] === 'failed').length;
  const doneTable = `<table class="tbl runs compact"><thead><tr><th>Status</th><th>Run</th><th>When</th><th class="num">Took</th><th class="num">Cost</th><th class="id-h">ID</th></tr></thead><tbody>${done.map((r) => `<tr><td>${status(ST[r[0]])}</td><td><b>${r[1]}</b> <span class="tgt">${r[2]}</span>${r[8] ? `<span class="sub-l">${r[8]}</span>` : ''}</td><td class="sub nowrap">${r[4]}</td><td class="num">${r[5]}</td><td class="num">${r[6]}</td><td class="mono id">${r[7]}</td></tr>`).join('')}</tbody></table>`;
  return `${glanceHead({ over: 'Runs · last 12 hours', title: `${attn.length} runs need attention`, answer: `${attn.length} runs need attention: ${failed} failed and ${attn.length - failed} waits for you`, lead: `${failed} failed and ${attn.length - failed} waits for you. The other ${rows.length - attn.length} are fine.` })}
    <div class="calm-stack"><div>${excRows(attn.map((r) => ({ state: ST[r[0]], title: `${r[1]} <span class="tgt">${r[2]}</span>`, sentence: REASON[r[7]] ?? r[8], age: r[4], action: `<button class="btn ${r[0] === 'waiting' ? 'primary' : 'secondary'} sm">${r[0] === 'waiting' ? 'Answer' : 'Open'}</button>` })))}</div>
    <div class="calm-rest">${runningLine(C.RUNNING)}${summaryLine({ lead: `${done.length} finished`, parts: [`${passed} passed`, `${parked} parked`], label: 'Show', body: doneTable })}</div></div>`;
}

function activityCalm() {
  const A = C.ACTIVITY, items = A.exceptions.flatMap((g) => g.items);
  const f = items.filter((x) => x.state === 'failed').length, w = items.length - f, total = A.hours.reduce((a, [x, y]) => a + x + y, 0);
  return `${glanceHead({ over: 'Activity · last 12 hours', title: `${f} failed, ${w} wait for you`, answer: `In 12 hours ${f} runs failed and ${w} wait for you; everything else went fine`, lead: `Everything else went fine: ${total - items.length} of ${total} runs across ${A.exceptions.length + A.healthy.length} workflows. ${A.running} are running now.` })}
    <section class="card calm-card">${runStrip(A.hours)}${byWorkflow(A.exceptions, A.healthy)}</section>`;
}

function approvalCalm() {
  const P = C.PROPOSAL;
  const can = `<div class="calm-can">${[['Does on its own', P.can.own], ['Asks you first', P.can.asks], ['Never', P.can.never]].map(([t, xs]) => `<div><h3>${t}</h3><ul>${xs.map((x) => `<li>${x}</li>`).join('')}</ul></div>`).join('')}</div>`;
  return `<div class="crumbs"><a>Bots</a>${ic('chevron-right')}<a>${P.bot}</a></div>
    <header class="calm-decision" data-item data-s="accent"><p class="cd-k">${mark('needs', { size: 14, label: true })}<span>${P.bot} asks you · ${P.expires.toLowerCase()}</span></p>
      <h1 data-glance data-glance-answer="${P.bot} wants to replace the production web server: approve or deny">${P.title}?</h1>
      <p class="lead">It rolls out the new image to fix the SEV2 latency incident. About 2 minutes of drain; you can undo it for 7 days.</p>
      <div class="decide"><button class="btn primary lg">${ic('fingerprint')}Approve with passkey</button><button class="btn secondary lg">Deny…</button></div></header>
    <div class="calm-rest">
      ${summaryLine({ lead: 'What changes', parts: ['1 added', '2 changed', '1 replaced', 'nothing destroyed'], label: 'Show', body: `<dl class="risk">${P.risk.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('')}</dl><p class="sub">Stateful things such as databases are never replaced without a separate rule.</p>` })}
      ${summaryLine({ lead: 'Why it’s asking', parts: ['the SEV2 incident “api p99 latency”'], label: 'Show', body: `<p>${P.why}</p><p class="sub">${P.origin} Only operators on the platform team can approve.</p>` })}
      ${summaryLine({ lead: `What ${P.bot} can do`, parts: [`${P.can.own.length} on its own`, `${P.can.asks.length} ask you first`, `${P.can.never.length} never`], label: 'Show', body: can })}
      ${summaryLine({ lead: 'Technical details', parts: ['plan, hashes, exact command'], label: 'Show', body: `<dl class="risk">${P.tech.map(([k, v]) => `<dt>${k}</dt><dd class="mono">${v}</dd>`).join('')}</dl>` })}</div>`;
}

function botCalm() {
  const B = C.BOT, p = B.pending[0];
  return `<div class="crumbs"><a>Bots</a>${ic('chevron-right')}<a>${B.folder}</a></div>
    ${glanceHead({ over: B.role, title: `${B.name} needs one decision`, answer: `${B.name} needs one decision from you; otherwise all is well`, lead: 'Otherwise all is well: its 14 runs this week went fine.' })}
    <div class="calm-stack">${exception({ state: 'needs', kicker: `Needs your approval · ${p.age}`, title: p.title, sentence: 'It wants to run terraform apply on production to fix the SEV2 latency incident.', action: '<button class="btn primary">Review</button>' })}
    <div class="calm-rest">
      ${summaryLine({ lead: 'Working on case c31', parts: ['db-prod-1 disk at 86%', 'for 11 min'], label: 'Show', body: `<ul class="plain">${B.cases.map(([, id, t, w]) => `<li><b>${t}</b> · ${w} <span class="mono sub">${id}</span></li>`).join('')}</ul>` })}
      ${summaryLine({ lead: '14 runs this week', parts: ['all went fine', '$3.10 of its $20 budget'], label: 'Show', body: `<ul class="plain">${B.runs.map((r) => `<li>${r[1]} · <span class="sub">${r[3]}</span></li>`).join('')}</ul>` })}
      ${summaryLine({ lead: 'Connections all healthy', parts: B.connections.map(([n]) => n), label: 'Show', body: `<ul class="plain">${B.connections.map(([n, a]) => `<li><b>${n}</b> · ${a}</li>`).join('')}</ul>` })}
      ${summaryLine({ lead: 'What it can do', parts: ['3 on its own', '3 ask you first', '2 never'], label: 'Show', body: `<div class="calm-can">${[['Does on its own', C.PROPOSAL.can.own], ['Asks you first', C.PROPOSAL.can.asks], ['Never', C.PROPOSAL.can.never]].map(([t, xs]) => `<div><h3>${t}</h3><ul>${xs.map((x) => `<li>${x}</li>`).join('')}</ul></div>`).join('')}</div>` })}
      ${summaryLine({ lead: 'Memory', parts: [`${B.memory} notes`, 'last added 2 days ago'], label: 'Show', body: '<p>Runbooks for db-prod, the SEV2 playbook and 10 notes from past cases.</p>' })}</div></div>`;
}

// exceptions = how many things on the screen genuinely need a person (the loudness budget's input)
const SCREENS = {
  overview: [overview, 'Overview', 5], run: [runPage, 'Activity', 1], approval: [approvalPage, 'Bots', 1], history: [historyPage, 'Activity', 3], builder: [builderPage, 'Workflows', 0],
  activity: [activityBefore, 'Activity', 5], bot: [botBefore, 'Bots', 1],
  perms: [SCAN.perms, 'Bots', 0], picker: [SCAN.picker, 'Workflows', 0],
};
const SCANS = { overview: SCAN.overview, history: SCAN.history, approval: SCAN.approval, perms: SCAN.perms, picker: SCAN.picker };
const CALM = { overview: overviewCalm, history: historyCalm, activity: activityCalm, approval: approvalCalm, bot: botCalm };
// The current system: history is part of Activity; perms and picker are the bot Access tab and the step picker.
const CURS = { overview: CUR.overview, activity: CUR.activity, history: CUR.activity, approval: () => CUR.approval(q.get('a') ?? 'terraform', q.get('tab') ?? 'overview'), bot: CUR.bot, perms: SCAN.perms, picker: SCAN.picker };
let [fn, title, exceptions] = SCREENS[s];
if (calm && CALM[s]) fn = CALM[s];
if (scan && SCANS[s]) fn = SCANS[s];
if (current && CURS[s]) fn = CURS[s];
if (quietDay && s === 'overview') exceptions = 0;
document.body.innerHTML = shell(fn(), title);
const main = document.querySelector('main');
main.dataset.exceptions = exceptions; main.dataset.version = current ? 'current' : scan ? 'scan' : calm && CALM[s] ? 'calm' : 'before';
document.title = `${D.name} · ${s}${current ? '' : scan ? ' · scannable proposal (archived)' : calm ? ' · calm round (archived)' : ' · before (archived)'}`;
// Archived drafts say so on the page.
if (d === 'daylight' && !current) main.insertAdjacentHTML('afterbegin', `<p class="archived">Archived draft: not the current system. <a href="?d=daylight&s=${s}&t=${theme}">See the current screen</a> · <a href="../archive/index.html">Archive</a></p>`);
