// logo-review.html: every logo candidate in real chrome, both themes, real sizes.
import { LOGOS, OFFICIAL_LOGO as RECOMMENDED_LOGO, kitDir } from '../tools/logos.mjs';
import { DIRS } from '../v2/directions.js';
import { LUCIDE } from '../icons/icons.js';

const ic = (n) => `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE[n]}</svg>`;
const D = DIRS.daylight;
// A self-contained Daylight scope: every token, the logo ink, and its own colour scheme, so
// neither the OS preference nor a browser's auto-darkening can leak into it.
const scope = (t, logo) => Object.entries({ ...D.vars, ...D[t], '--color-logo-ink': logo.pal[t].ink }).map(([k, v]) => `${k}:${v}`).join(';') + `;color-scheme:only ${t}`;
const mark = (L, cls = 'lm') => `<svg class="${cls}" viewBox="${L.mark.box.join(' ')}" role="img" aria-label="conductor"><g fill="var(--color-logo-ink)">${L.mark.ink}</g><g fill="var(--color-accent-default)">${L.mark.acc}</g></svg>`;
const kit = (L, f) => `${kitDir(L.id)}/${f}`;
const sfx = (t) => (t === 'dark' ? '-dark' : '');

function dash(L, t) {
  const nav = [['layout-grid', 'Home', 1], ['bell', 'Needs you', 0, 5], ['history', 'Runs'], ['workflow', 'Workflows'], ['bot', 'Bots']];
  return `<div class="ctx dash" style="${scope(t, L)}"><aside><div class="brand">${mark(L)}<b class="wm">conductor</b></div>
    <nav>${nav.map(([i, l, on, n]) => `<a class="${on ? 'on' : ''}">${ic(i)}<span>${l}</span>${n ? `<em>${n}</em>` : ''}</a>`).join('')}</nav></aside>
    <div class="main"><div class="top"><span class="search">${ic('search')}Search runs, bots and settings</span></div><h3>Good afternoon, ops.</h3><p><b>5 things need you.</b> Everything else is running normally.</p></div></div>`;
}
const strip = (L, t) => `<div class="ctx strip" style="${scope(t, L)}"><div class="brand">${mark(L)}<b class="wm">conductor</b></div><span class="cell"><i class="dot"></i>Healthy</span><span class="cell"><b>3</b> running</span><span class="cell acc">5 need you</span></div>`;
const phone = (L, t) => `<div class="ctx phone" style="${scope(t, L)}"><div class="ph">${mark(L)}<b>Home</b><span class="av">op</span></div><div class="pb"><h4>Good afternoon, ops.</h4><p>5 things need you.</p></div></div>`;
const tab = (L, t) => `<div class="ctx tabs ${t}" style="color-scheme:only ${t}"><div class="tab on"><img src="${kit(L, `favicon-16${sfx(t)}.png`)}" width="16" height="16" alt=""><span>Home · conductor</span><i>×</i></div><div class="tab"><span class="g"></span><span>acme/web#1288</span><i>×</i></div></div>`;
const readme = (L, t) => `<div class="ctx gh ${t}" style="color-scheme:only ${t}"><div class="gh-h">README</div><div class="gh-b"><img src="${kit(L, `logo-horizontal${sfx(t)}.svg`)}" alt="conductor"><p>Event-driven orchestration for AI coding agents — self-hosted, config-as-code.</p></div></div>`;
const sizes = (L, t) => `<div class="ctx sizes" style="${scope(t, L)}">${[16, 24, 32, 48, 96].map((s) => `<figure><img src="${kit(L, `logo-mark${sfx(t)}.svg`)}" width="${s}" height="${s}" alt=""><figcaption>${s}</figcaption></figure>`).join('')}<figure><img class="px" src="${kit(L, `favicon-16${sfx(t)}.png`)}" width="64" height="64" alt=""><figcaption>16 ×4</figcaption></figure></div>`;

function candidate(L, i) {
  const col = (t) => `<div class="col"><span class="tag">${t === 'light' ? 'Light' : 'Dark'}</span>${strip(L, t)}${dash(L, t)}${phone(L, t)}${tab(L, t)}${readme(L, t)}${sizes(L, t)}</div>`;
  return `<section class="sec cand" id="${L.id}"><div class="ch"><span class="n">${i + 1}</span><div><h2>${L.name}${L.id === RECOMMENDED_LOGO ? ' <span class="rec-badge">Chosen</span>' : ''}</h2><p class="line">${L.line}</p></div>
    <div class="hero" style="${scope('light', L)}"><img src="${kit(L, 'logo-horizontal.svg')}" alt="${L.name} lockup"></div></div>
    <p class="sub">${L.why}</p><div class="cols">${col('light')}${col('dark')}</div>
    <p class="kit">${L.id === RECOMMENDED_LOGO ? 'Official kit' : 'Kit (explored)'}: <a href="${kitDir(L.id)}/">${kitDir(L.id)}/</a> · <a href="${kitDir(L.id)}/README.md">README</a>${L.whyNot ? ` · <b>Why not:</b> ${L.whyNot}` : ''}</p></section>`;
}

// before / after: the round-one mark (directions/fermata/) against the official, strengthened one
const baRow = (t) => {
  const old = (f) => `directions/fermata/${f.replace(/^(logo-(?:horizontal|mark))\.svg$/, '$1-light.svg').replace('favicon-16-dark.png', 'favicon-16.png')}`, now = (f) => kit(LOGOS[0], f);
  const cell = (src, label) => `<div class="ba-cell"><span class="tag">${label}</span><img class="hl" src="${src(`logo-horizontal${sfx(t)}.svg`)}" alt=""><div class="ba-sizes">${[96, 48, 32, 16].map((s) => `<img src="${src(`logo-mark${sfx(t)}.svg`)}" width="${s}" height="${s}" alt="">`).join('')}<img class="px" src="${src(`favicon-16${t === 'dark' ? '-dark' : ''}.png`)}" width="64" height="64" alt=""></div></div>`;
  return `<div class="ctx ba-row" style="${scope(t, LOGOS[0])}">${cell(old, `Before · ${t}`)}${cell(now, `After · ${t}`)}</div>`;
};
document.getElementById('ba').innerHTML = baRow('light') + baRow('dark');
document.getElementById('candidates').innerHTML = LOGOS.map(candidate).join('');
document.getElementById('jump').innerHTML = `<a href="#before-after">Before / after</a>${LOGOS.map((L) => `<a href="#${L.id}">${L.name.replace(', strengthened', '')}</a>`).join('')}<a href="#recommendation">Recommendation</a>`;

const R = LOGOS.find((L) => L.id === RECOMMENDED_LOGO);
document.getElementById('recommendation').innerHTML = `<h2>Chosen: ${R.name}</h2><div class="rec-body">
  <div class="hero big" style="${scope('light', R)}"><img src="${kit(R, 'logo-horizontal.svg')}" alt=""></div><div class="hero big" style="${scope('dark', R)}"><img src="${kit(R, 'logo-horizontal-dark.svg')}" alt=""></div></div>
  <div id="rec-copy"></div>`;
const copy = document.getElementById('rec-copy');
fetch('logo-review/recommendation.html').then((r) => (r.ok ? r.text() : '')).then((h) => { copy.innerHTML = h; });
