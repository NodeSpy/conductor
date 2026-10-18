import { DIRS, RECOMMENDED } from './tools/directions.mjs';

const f = (id, name) => `directions/${id}/${name}`;
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;');

// ---------- small line icons for the dashboard mock (16px, stroke) ----------
const I = {
  grid: '<rect x="2.5" y="2.5" width="4.5" height="4.5" rx="1"/><rect x="9" y="2.5" width="4.5" height="4.5" rx="1"/><rect x="2.5" y="9" width="4.5" height="4.5" rx="1"/><rect x="9" y="9" width="4.5" height="4.5" rx="1"/>',
  pulse: '<path d="M1.5 8.5h3l2-5 3 9 2-4h3"/>',
  diamond: '<path d="M8 2l6 6-6 6-6-6z"/>',
  list: '<path d="M3 4h10M3 8h10M3 12h7"/>',
  flow: '<circle cx="4" cy="4" r="1.6"/><circle cx="12" cy="12" r="1.6"/><path d="M4 5.6v2.4a2 2 0 0 0 2 2h4.4"/>',
  bots: '<circle cx="6" cy="6" r="2.3"/><path d="M2 13.5c.6-2.3 2.1-3.5 4-3.5s3.4 1.2 4 3.5M10.5 4.2a2.3 2.3 0 0 1 0 4.1M12 10.3c1.2.5 2 1.6 2.3 3.2"/>',
  braces: '<path d="M6 2.5c-1.6 0-2 .8-2 2v1.6c0 1-.6 1.9-1.6 1.9 1 0 1.6.9 1.6 1.9v1.6c0 1.2.4 2 2 2M10 2.5c1.6 0 2 .8 2 2v1.6c0 1 .6 1.9 1.6 1.9-1 0-1.6.9-1.6 1.9v1.6c0 1.2-.4 2-2 2"/>',
  key: '<circle cx="5" cy="11" r="2.8"/><path d="M7 9l6.5-6.5M11 5l1.8 1.8"/>',
  pack: '<path d="M2.5 5L8 2.5 13.5 5v6L8 13.5 2.5 11z M2.5 5L8 7.5 13.5 5M8 7.5v6"/>',
  shield: '<path d="M8 1.8l5 2v4c0 3.2-2.2 5.4-5 6.4-2.8-1-5-3.2-5-6.4v-4z"/>',
  search: '<circle cx="7" cy="7" r="4.2"/><path d="M10.2 10.2L13.5 13.5"/>',
  play: '<path d="M5 3.5v9l7-4.5z"/>',
  pause: '<path d="M5.5 3.5v9M10.5 3.5v9"/>',
};
const icon = (n) => `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${I[n]}</svg>`;

// ---------- lineup ----------
document.getElementById('lineup').innerHTML = DIRS.map(
  (d) => `<a href="#${d.id}" class="pick${d.id === RECOMMENDED ? ' rec' : ''}" style="--a:${d.pal.light.acc}">
    <span class="mono n">${d.num}</span>
    <img src="${f(d.id, 'logo-mark-light.svg')}" width="88" height="88" alt="">
    <span class="nm">${d.name}</span><span class="ln">${d.line}</span>
    <img class="px16" src="${f(d.id, 'favicon-16.png')}" width="16" height="16" alt="" title="actual 16px">
    ${d.id === RECOMMENDED ? '<span class="badge mono">recommended</span>' : ''}</a>`,
).join('');

// ---------- per-direction sections ----------
const tok = (P, pre) => Object.entries(P).map(([k, v]) => `--${pre}-${k}:${v}`).join(';');

function constructionGrid() {
  let g = '';
  for (let i = 0; i <= 32; i++) {
    const c = i % 4 === 0 ? 'k' : '';
    g += `<line class="${c}" x1="${i}" y1="0" x2="${i}" y2="32"/><line class="${c}" x1="0" y1="${i}" x2="32" y2="${i}"/>`;
  }
  return g;
}

function construction(d, theme) {
  const [x, y, w, h] = d.mark.box;
  return `<figure class="tile big ${theme}">
    <img src="${f(d.id, `logo-mark-${theme}.svg`)}" alt="${d.name} mark">
    <svg class="grid" viewBox="0 0 32 32" aria-hidden="true">${constructionGrid()}
      <rect class="box" x="${x}" y="${y}" width="${w}" height="${h}"/></svg>
    <figcaption class="mono">${theme === 'light' ? '32-unit grid · content box' : 'on dark'}</figcaption></figure>`;
}

function github(d, theme) {
  const v = theme === 'dark' ? 'dark' : 'light';
  return `<div class="gh ${theme}">
    <div class="gh-top"><svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path fill="currentColor" d="M2 2.5A1.5 1.5 0 0 1 3.5 1h9A1.5 1.5 0 0 1 14 2.5v11a.5.5 0 0 1-.5.5h-10A1.5 1.5 0 0 1 2 12.5zM3.5 2a.5.5 0 0 0-.5.5v10c0 .28.22.5.5.5H13V2z"/></svg><span>your-org</span><span class="sl" data-contrast="exempt">/</span><b>conductor</b><span class="pill">Public</span></div>
    <div class="gh-tabs"><span class="on">Code</span><span>Issues</span><span>Pull requests</span><span>Actions</span></div>
    <div class="gh-readme">
      <div class="gh-rh">README</div>
      <div class="gh-body">
        <p class="c"><img src="${f(d.id, `logo-horizontal-${v}.svg`)}" alt="conductor"></p>
        <p class="c">Event-driven orchestration for AI coding agents — self-hosted, config-as-code.</p>
        <p>Connect to your services once (<b>connectors</b>), declare where agents run (<b>runtimes</b> + <b>agents</b>), and wire events to work (<b>triggers</b>). A GitHub review request can research in an agent, ask you on Slack, and submit the review…</p>
        <h2>Quick start</h2>
        <pre>conductor init &amp;&amp; conductor serve</pre>
      </div>
    </div></div>`;
}

// The recommended direction is shown exactly as BRAND.md proposes to ship it in the dashboard:
// an inline mark themed by the dashboard's tokens, beside the existing live-text wordmark.
const liveBrand = (d) =>
  `<svg class="mark" viewBox="${d.mark.box.join(' ')}" aria-hidden="true"><g fill="var(--p-ink)">${d.mark.ink}</g><g fill="var(--p-acc)">${d.mark.acc}</g></svg><span class="wordmark">conductor</span>`;

function dashboard(d, theme) {
  const P = d.pal[theme];
  const v = theme;
  const nav = (ic, label, extra = '', on = false) => `<a class="${on ? 'on' : ''}">${icon(ic)}<span>${label}</span>${extra}</a>`;
  return `<div class="dash ${theme}" style="${tok(P, 'p')}">
    <div class="strip">
      <div class="brand">${d.id === RECOMMENDED ? liveBrand(d) : `<img src="${f(d.id, `logo-horizontal-${v}.svg`)}" alt="conductor">`}</div>
      <div class="cell"><i class="dot ok"></i>up <b>3d 4h</b></div>
      <div class="cell">v0.71.2 → <span class="run">v0.72.0</span></div>
      <div class="cell">${icon('shield')}sandbox <b>enforcing</b></div>
      <div class="cell"><i class="dot run"></i><b>3</b> running</div>
      <div class="cell alert">${icon('diamond')}<b>6</b> need you</div>
      <div class="spacer"></div>
      <div class="cell search">${icon('search')}Search <kbd>⌘K</kbd></div>
    </div>
    <div class="body">
      <nav class="rail">
        <div class="host"><i class="dot ok"></i><span>ops-box · 192.0.2.40</span></div>
        <span class="grp">Now</span>
        ${nav('grid', 'Overview', '', true)}${nav('pulse', 'Live', '<em>3</em>')}${nav('diamond', 'Needs you', '<em class="hot">6</em>')}
        <span class="grp">Work</span>
        ${nav('list', 'Run history')}${nav('flow', 'Workflows')}${nav('bots', 'Bots')}
        <span class="grp">System</span>
        ${nav('braces', 'Config')}${nav('key', 'Secrets & vars')}${nav('pack', 'Packs & plugins')}
      </nav>
      <div class="page">
        <div class="crumbs">ops-box · ops-box.tail0000.ts.net</div>
        <div class="ph"><h4>Overview</h4><span class="btn">${icon('pause')}Pause dispatch</span><span class="btn">${icon('play')}Run a workflow</span></div>
        <div class="cards">
          <div class="card needs"><div class="ch">${icon('diamond')}<b>Needs you</b><span>6 items · approvals are signed with your passkey</span></div>
            <div class="li"><span>terraform apply · infra@3f2a9c1d</span><span class="rv">Review</span></div>
            <div class="li"><span>slack-ops.post to #incidents</span><span class="rv">Review</span></div>
            <div class="li m"><span>hand-off awaiting answer · acme/infra#77</span><span>1h</span></div></div>
          <div class="card"><span class="cap">Running</span><div class="n">3</div><span class="sm">ci-fixer · pr-review</span></div>
          <div class="card"><span class="cap">Runs · 12h</span><div class="n">20 <small>90% ok</small></div><span class="sm">2 failed · 1 waiting</span></div>
        </div>
      </div>
    </div></div>`;
}

function tabs(d, theme) {
  const v = theme === 'dark' ? 'dark' : 'light';
  return `<div class="tabs ${theme}">
    <div class="tab on"><img src="${f(d.id, `favicon16-${v}.svg`)}" width="16" height="16" alt=""><span>Overview · conductor</span><i data-contrast="exempt">×</i></div>
    <div class="tab"><svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6.5" fill="currentColor" opacity=".35"/></svg><span>Pull request #182</span><i data-contrast="exempt">×</i></div>
    <div class="tab"><svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><rect x="2" y="2" width="12" height="12" rx="3" fill="currentColor" opacity=".35"/></svg><span>Incidents · Slack</span><i data-contrast="exempt">×</i></div>
  </div>`;
}

const mono = (d, file, color, bg, cls = '') =>
  `<div class="mono-tile ${cls}" style="background:${bg}"><i style="--m:url(${f(d.id, file)});background:${color}" class="mask"></i></div>`;

function section(d, idx) {
  const L = d.pal.light, D = d.pal.dark;
  const rec = d.id === RECOMMENDED;
  return `<section class="dir" id="${d.id}" style="${tok(L, 'l')};${tok(D, 'd')}">
  <div class="sec-head"><span class="num">${d.num}</span><h2>${d.name} <span class="line">${d.line}</span></h2>${rec ? '<span class="badge mono">recommended</span>' : ''}</div>

  <div class="intro">
    <div class="rationale">${d.rationale.map((p) => `<p>${p}</p>`).join('')}
      <dl class="mono meta"><dt>Wordmark</dt><dd>${d.word.label}</dd><dt>Palette</dt><dd>${d.pal.name}</dd><dt>UI / mono</dt><dd>${d.ui} · ${d.mono}</dd></dl>
    </div>
    <div class="hero-tiles">${construction(d, 'light')}${construction(d, 'dark')}</div>
  </div>

  <div class="spec"><h3 class="mono"><b>${idx}.1</b> Lockups</h3><div class="lockups">
    <figure class="tile light wide"><img src="${f(d.id, 'logo-horizontal-light.svg')}" alt="" class="h"><figcaption class="mono">horizontal</figcaption></figure>
    <figure class="tile dark wide"><img src="${f(d.id, 'logo-horizontal-dark.svg')}" alt="" class="h"><figcaption class="mono">horizontal · dark</figcaption></figure>
    <figure class="tile light"><img src="${f(d.id, 'logo-stacked-light.svg')}" alt="" class="s"><figcaption class="mono">stacked</figcaption></figure>
    <figure class="tile dark"><img src="${f(d.id, 'logo-stacked-dark.svg')}" alt="" class="s"><figcaption class="mono">stacked · dark</figcaption></figure>
  </div></div>

  <div class="spec"><h3 class="mono"><b>${idx}.2</b> Palette · ${d.pal.name}</h3><div class="swatches">
    ${d.pal.swatches.map(([n, hx]) => `<div class="sw"><i style="background:${hx}"></i><b>${n}</b><span class="mono">${hx}</span></div>`).join('')}
  </div></div>

  <div class="spec"><h3 class="mono"><b>${idx}.3</b> README header</h3><div class="pair">${github(d, 'light')}${github(d, 'dark')}</div></div>

  <div class="spec"><h3 class="mono"><b>${idx}.4</b> Dashboard</h3><div class="stack-2">${dashboard(d, 'dark')}${dashboard(d, 'light')}</div></div>

  <div class="spec"><h3 class="mono"><b>${idx}.5</b> Favicon · 16 / 32 / 48</h3><div class="fav">
    <div class="tabsets">${tabs(d, 'light')}${tabs(d, 'dark')}</div>
    <div class="px">${[16, 32, 48].map((s) => `<figure><div class="zoom"><img src="${f(d.id, `favicon-${s}.png`)}" width="${s * (s === 16 ? 5 : s === 32 ? 3 : 2)}" height="${s * (s === 16 ? 5 : s === 32 ? 3 : 2)}" alt=""></div><figcaption class="mono"><img src="${f(d.id, `favicon-${s}.png`)}" width="${s}" height="${s}" alt=""> ${s}px</figcaption></figure>`).join('')}</div>
  </div></div>

  <div class="spec"><h3 class="mono"><b>${idx}.6</b> App icon · 512</h3><div class="app">
    <img src="${f(d.id, 'app-icon-512.png')}" width="232" height="232" alt="App icon, dark">
    <img src="${f(d.id, 'app-icon-light.svg')}" width="232" height="232" alt="App icon, light" class="shadowed">
    <div class="dock">${[96, 64, 48, 32].map((s) => `<img src="${f(d.id, 'app-icon-dark.svg')}" width="${s}" height="${s}" alt="">`).join('')}</div>
  </div></div>

  <div class="spec"><h3 class="mono"><b>${idx}.7</b> Monochrome</h3><div class="monos">
    ${mono(d, 'logo-mark-mono.svg', '#000', '#fff')}
    ${mono(d, 'logo-mark-mono.svg', '#fff', '#000')}
    ${mono(d, 'logo-mark-mono.svg', L.acc === '#9a6b12' ? '#16140f' : '#fff', D.acc)}
    ${mono(d, 'logo-horizontal-mono.svg', '#000', '#fff', 'wide')}
    ${mono(d, 'logo-horizontal-mono.svg', '#fff', '#000', 'wide')}
    ${mono(d, 'favicon16-mono.svg', '#000', '#fff', 'tiny')}
  </div></div>

  <div class="spec"><h3 class="mono"><b>${idx}.8</b> Social preview · 1280×640</h3><div class="social">
    <img src="${f(d.id, 'social-card.png')}" width="1280" height="640" alt="${d.name} social card">
  </div></div>
</section>`;
}

document.getElementById('directions').innerHTML = DIRS.map((d, i) => section(d, i + 1)).join('');

// ---------- scorecard ----------
const SCORE = [
  ['Legible at 16px', [3, 4, 3, 2]],
  ['Works in one colour', [4, 4, 4, 3]],
  ['Distinctive / ownable', [4, 2, 1, 2]],
  ['Says something true', [4, 3, 2, 1]],
  ['Fits the dashboard', [4, 3, 4, 3]],
  ['Low switching cost', [3, 2, 4, 3]],
];
const dots = (n) => `<span role="img" aria-label="${n} of 4">${'●'.repeat(n)}<span class="off" data-contrast="exempt">${'●'.repeat(4 - n)}</span></span>`;
document.getElementById('scoretable').innerHTML =
  `<thead><tr><th></th>${DIRS.map((d) => `<th class="${d.id === RECOMMENDED ? 'rec' : ''}"><img src="${f(d.id, 'logo-mark-light.svg')}" width="28" height="28" alt=""><span>${d.name}</span></th>`).join('')}</tr></thead><tbody>` +
  SCORE.map(([k, v]) => `<tr><td>${k}</td>${v.map((n, i) => `<td class="${DIRS[i].id === RECOMMENDED ? 'rec' : ''}">${dots(n)}</td>`).join('')}</tr>`).join('') +
  `<tr class="tot"><td>Total</td>${DIRS.map((d, i) => `<td class="${d.id === RECOMMENDED ? 'rec' : ''}">${SCORE.reduce((a, [, v]) => a + v[i], 0)}<small>/24</small></td>`).join('')}</tr></tbody>`;

// ---------- asset list ----------
const ASSETS = [
  ['logo-mark.svg', 'light'], ['logo-mark-dark.svg', 'dark'], ['logo-mark-mono.svg', 'light'],
  ['logo-horizontal.svg', 'light w'], ['logo-horizontal-dark.svg', 'dark w'], ['logo-horizontal-mono.svg', 'light w'],
  ['logo-stacked.svg', 'light'], ['logo-stacked-dark.svg', 'dark'], ['logo-stacked-mono.svg', 'light'],
  ['wordmark.svg', 'light w'], ['wordmark-dark.svg', 'dark w'], ['favicon.svg', 'light'],
  ['favicon-16.png', 'light pix'], ['favicon-32.png', 'light pix'], ['favicon-48.png', 'light pix'],
  ['apple-touch-icon.png', 'light'], ['icon-192.png', 'light'], ['icon-512.png', 'light'],
  ['app-icon.svg', 'light'], ['app-icon-light.svg', 'light'], ['social-card.png', 'dark w2'],
];
document.getElementById('assetgrid').innerHTML = ASSETS.map(
  ([n, c]) => `<a class="asset ${c}" href="assets/${n}" download><span class="prev"><img src="assets/${n}" alt=""></span><span class="mono">${esc(n)}</span></a>`,
).join('');
