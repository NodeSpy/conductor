// Renders the brand book from tokens/tokens.json, tokens/contrast.json and tokens/migration.json.
// Everything visual comes from tokens.css; this file only lays content out.
import { LUCIDE, STATUS } from '../icons/icons.js';

const [T, CON, MIG] = await Promise.all(['tokens/tokens.json', 'tokens/contrast.json', 'tokens/migration.json'].map((u) => fetch(u).then((r) => r.json())));

// ---------- helpers ----------
const lu = (n, cls = '') => `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE[n] ?? ''}</svg>`;
const sg = (n, cls = '') => `<svg class="${cls}" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${STATUS[n].svg}</svg>`;
const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const THEMES = ['dark', 'light'];

const flat = new Map();
(function walk(n, p, type) {
  const t = n.$type ?? type;
  if ('$value' in n) { flat.set(p.join('.'), { type: t, value: n.$value, modes: n.$extensions?.mode, desc: n.$description, ext: n.$extensions }); return; }
  for (const [k, v] of Object.entries(n)) if (!k.startsWith('$') && v && typeof v === 'object') walk(v, [...p, k], t);
})(T, [], null);
const group = (p) => p.split('.').reduce((o, k) => o[k], T);
const isAlias = (v) => typeof v === 'string' && v.startsWith('{');
function resolve(p, theme = 'dark', density = 'comfortable') {
  const t = flat.get(p);
  let v = t.modes ? (t.modes[theme] ?? t.modes[density] ?? t.modes.standard ?? t.value) : t.value;
  return isAlias(v) ? resolve(v.slice(1, -1), theme, density) : v;
}
const hexOf = (c) => (c.alpha != null && c.alpha < 1 ? `${c.hex} · ${Math.round(c.alpha * 100)}%` : c.hex);
function cssName(p) {
  const [tier, ...rest] = p.split('.');
  return tier === 'primitive' && rest[0] === 'color' ? '--palette-' + rest.slice(1).join('-') : '--' + rest.join('-');
}
const lumi = (h) => { const c = [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16) / 255).map((x) => (x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4)); return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]; };
const conFor = (token, on, theme) => CON.find((r) => r.token === token && r.on === on && r.theme === theme);
const pair = (fn, cls = '') => `<div class="pair ${cls}">${THEMES.map((t) => `<div class="panel" data-theme="${t}"><span class="tag">${t}</span>${fn(t)}</div>`).join('')}</div>`;
const stack = (fn) => `<div class="pair" style="grid-template-columns:1fr">${THEMES.map((t) => `<div class="panel" data-theme="${t}"><span class="tag">${t}</span>${fn(t)}</div>`).join('')}</div>`;
const why = (s) => `<p class="why">${s}</p>`;
const rules = (items) => `<ul class="rules">${items.map((i) => `<li>${i}</li>`).join('')}</ul>`;
const verdict = (ok) => `<div class="verdict">${sg(ok ? 'success' : 'danger')}${ok ? 'Do' : 'Don’t'}</div>`;

const chapters = [];
const ch = (id, title, blurb, subs, body) => chapters.push({ id, title, blurb, subs, body });
const sub = (id, title, lede = '') => `<h3 class="sub" id="${id}">${title}</h3>${lede ? `<p class="lede">${lede}</p>` : ''}`;

// =====================================================================
// 1. Colour
// =====================================================================
const RAMPS = Object.keys(T.primitive.color).filter((k) => !k.startsWith('$'));
const rampRow = (name) => {
  const steps = Object.entries(T.primitive.color[name]);
  const short = steps.length < 11;
  return `<div class="ramp ${short ? 'short' : ''}"><b>${name}</b>${steps.map(([s, t]) => {
    const h = t.$value.hex; const ink = lumi(h) > 0.32 ? '#1a1a14' : '#f0ede3';
    return `<i style="background:${h};color:${ink}" title="--palette-${name}-${s}: ${h}"><em>${s}</em>${h.slice(1)}</i>`;
  }).join('')}${short ? '<span></span>' : ''}</div>`;
};
const SURF = ['canvas', 'sunken', 'rail', 'surface', 'raised', 'overlay'];
const TEXTS = ['primary', 'secondary', 'tertiary', 'accent', 'disabled'];
const textMatrix = (theme) => `<div class="textscale">
  <div class="row hdr"><b>text ↓ / bg →</b>${SURF.map((s) => `<span>${s}</span>`).join('')}</div>
  ${TEXTS.map((tx) => `<div class="row"><b>text.${tx}</b>${SURF.map((s) => {
    const r = conFor(`semantic.color.text.${tx}`, `semantic.color.bg.${s}`, theme);
    return `<div class="cell" style="background:var(--color-bg-${s});color:var(--color-text-${tx})"><span>Needs you · 6</span><small>${r ? `${r.ratio.toFixed(2)} ${r.level === 'exempt' ? 'exempt' : r.level}` : ''}</small></div>`;
  }).join('')}</div>`).join('')}
</div>`;
const semGroups = [
  ['bg', ['canvas', 'sunken', 'rail', 'surface', 'raised', 'overlay', 'field', 'inverse']],
  ['text', ['primary', 'secondary', 'tertiary', 'disabled', 'inverse', 'accent', 'on-accent']],
  ['icon', ['default', 'muted']],
  ['border', ['subtle', 'default', 'strong', 'control']],
  ['accent', ['default', 'hover', 'active', 'subtle']],
  ['focus', ['ring']], ['selection', ['bg', 'text']], ['state', ['hover', 'pressed']], ['', ['scrim']],
];
const semSwatches = (theme) => `<div class="sem-grid">${semGroups.flatMap(([g, ks]) => ks.map((k) => {
  const p = `semantic.color.${g ? g + '.' : ''}${k}`; const v = resolve(p, theme);
  return `<div class="sw"><i style="--c:var(${cssName(p)})"></i><b>${cssName(p).slice(8)}</b><span>${hexOf(v)}</span></div>`;
})).join('')}</div>`;

const STAT = Object.keys(T.semantic.color.status).filter((k) => !k.startsWith('$'));
const STAT_GLYPH = { success: 'success', warning: 'warning', danger: 'danger', info: 'info', running: 'running', neutral: 'neutral' };
const STAT_EX = {
  success: ['Passed', 'pr-review finished · 31 checks', '2m'], warning: ['Waiting on you', 'hand-off awaiting answer', '1h'], danger: ['Failed', 'ci-fixer failed on acme/web#1288', '52m'],
  info: ['Update', 'v0.72.0 available · preflight passed', '4m'], running: ['Running', 'issue-triage · step classify', 'now'], neutral: ['Parked', 'dep-bump paused by you', '3d'],
};
const statusCards = (theme) => `<div class="statuses">${STAT.map((s) => `<div class="status-card" data-s="${s}">
  <div class="top-row">${sg(STAT_GLYPH[s], s === 'running' ? 'c-glyph-spin' : '')}<b>${s}</b><span class="c-dot ${s === 'running' ? 'live' : ''}"></span></div>
  <div class="chips"><span class="c-badge" data-s="${s}">${sg(STAT_GLYPH[s], s === 'running' ? 'c-glyph-spin' : '')}${STAT_EX[s][0]}</span><span class="c-badge" data-s="${s}">${STAT_EX[s][0].toLowerCase()}</span></div>
  <div class="row">${sg(STAT_GLYPH[s])}<span>${STAT_EX[s][1]}</span><em>${STAT_EX[s][2]}</em></div>
  <p>${esc(T.semantic.color.status[s].$description.split(' Always')[0])}<br><code>--color-status-${s}-{fg,bg,border,solid}</code> · fg ${resolve(`semantic.color.status.${s}.fg`, theme).hex}</p>
</div>`).join('')}</div>`;

// charts
const CAT = Array.from({ length: 8 }, (_, i) => i + 1);
const catNames = ['pr-review', 'ci-fixer', 'issue-triage', 'platform-sre', 'dep-bump', 'release-notes', 'billing-sre', 'Other'];
function barChart() {
  const days = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri'];
  const data = days.map((_, d) => CAT.map((c) => 2 + ((c * 7 + d * 5) % 9)));
  const W = 560, H = 210, pad = 30, bw = 64, max = 60;
  const x0 = (i) => pad + 16 + i * ((W - pad - 16) / days.length);
  let bars = '';
  data.forEach((col, i) => {
    let y = H - 22;
    col.forEach((v, j) => { const h = (v / max) * (H - 40); bars += `<rect x="${x0(i)}" y="${y - h}" width="${bw}" height="${Math.max(h - 2, 1)}" rx="1.5" fill="var(--color-dataviz-categorical-${j + 1})"><title>${catNames[j]} · ${days[i]}: ${v} runs</title></rect>`; y -= h; });
    bars += `<text class="ax" x="${x0(i) + bw / 2}" y="${H - 6}" text-anchor="middle">${days[i]}</text>`;
  });
  const grid = [0, 20, 40, 60].map((v) => { const y = H - 22 - (v / max) * (H - 40); return `<line class="${v ? 'gl' : 'base'}" x1="${pad}" x2="${W}" y1="${y}" y2="${y}"/><text class="ax" x="${pad - 6}" y="${y + 4}" text-anchor="end">${v}</text>`; }).join('');
  return `<div class="chart"><div class="legend">${catNames.map((n, j) => `<span><i style="background:var(--color-dataviz-categorical-${j + 1})"></i>${n}</span>`).join('')}</div>
    <svg viewBox="0 0 ${W} ${H}" role="img" aria-label="Runs per day by workflow, stacked">${grid}${bars}</svg></div>`;
}
function lineChart() {
  const W = 560, H = 170, n = 24;
  const series = [0, 1, 2].map((s) => Array.from({ length: n }, (_, i) => 40 + 28 * Math.sin((i + s * 5) / 3.2) + s * 12 + ((i * (s + 3)) % 7)));
  const x = (i) => 30 + (i / (n - 1)) * (W - 70), y = (v) => H - 20 - (v / 120) * (H - 30);
  // Direct labels at the line ends, pushed apart so they never overlap (min 13px apart).
  const ends = series.map((sr, s) => ({ s, y: y(sr[n - 1]) + 4 })).sort((a, b) => a.y - b.y);
  for (let i = 1; i < ends.length; i++) ends[i].y = Math.max(ends[i].y, ends[i - 1].y + 13);
  const labelY = Object.fromEntries(ends.map((e) => [e.s, e.y]));
  const lines = series.map((sr, s) => `<path d="${sr.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)} ${y(v).toFixed(1)}`).join('')}" fill="none" stroke="var(--color-dataviz-categorical-${s + 1})" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/><text class="dl" x="${x(n - 1) + 6}" y="${labelY[s]}">${catNames[s]}</text>`).join('');
  const nowX = x(n - 4);
  return `<div class="chart"><svg viewBox="0 0 ${W} ${H}" role="img" aria-label="p95 duration by workflow, last 24h">
    ${[0, 60, 120].map((v) => `<line class="${v ? 'gl' : 'base'}" x1="30" x2="${W - 40}" y1="${y(v)}" y2="${y(v)}"/><text class="ax" x="24" y="${y(v) + 4}" text-anchor="end">${v}s</text>`).join('')}
    ${lines}<line x1="${nowX}" x2="${nowX}" y1="6" y2="${H - 20}" stroke="var(--color-dataviz-now)" stroke-width="1.5"/><circle cx="${nowX}" cy="6" r="3" fill="var(--color-dataviz-now)"/><text class="ax" x="${nowX}" y="${H - 6}" text-anchor="middle" style="fill:var(--color-text-accent)">now</text>
  </svg></div>`;
}
const seqStrip = () => `<div class="seq">${Array.from({ length: 9 }, (_, i) => `<i style="background:var(--color-dataviz-sequential-${i})" title="sequential-${i}"></i>`).join('')}</div><div class="scale-lbl"><span>fewer runs</span><span>more runs</span></div>`;
const divStrip = () => `<div class="div">${['neg-4', 'neg-3', 'neg-2', 'neg-1', 'mid', 'pos-1', 'pos-2', 'pos-3', 'pos-4'].map((k) => `<i style="background:var(--color-dataviz-diverging-${k})" title="${k}"></i>`).join('')}</div><div class="scale-lbl"><span>cheaper than last week</span><span>same</span><span>dearer</span></div>`;

function contrastTable() {
  const by = (lvl) => CON.filter((r) => r.level === lvl).length;
  const fails = CON.filter((r) => !r.pass).length;
  return `<div class="summary">
    <span class="c-badge" data-s="${fails ? 'danger' : 'success'}">${sg(fails ? 'danger' : 'success')}${fails ? fails + ' failing' : 'All ' + CON.length + ' declared pairs pass'}</span>
    <span class="c-badge">${by('AAA')} AAA</span><span class="c-badge">${by('AA')} AA</span><span class="c-badge">${by('AA large / UI')} UI 3:1</span><span class="c-badge">${by('exempt')} exempt (disabled)</span>
  </div>
  <div class="cfilter"><div class="c-input" style="width:280px">${lu('search')}<input id="cq" placeholder="Filter by token, e.g. status or button" style="all:unset;flex:1;font:inherit"></div>
  <div class="c-seg" id="ct"><span class="is-active" data-v="">Both themes</span><span data-v="dark">Dark</span><span data-v="light">Light</span></div></div>
  <div class="ctable-wrap"><table class="ctable"><thead><tr><th>Sample</th><th>Token (text / mark)</th><th>On</th><th>Theme</th><th class="r">Ratio</th><th>Min</th><th>Level</th></tr></thead><tbody id="cbody"></tbody></table></div>`;
}
function fillContrast() {
  const q = document.getElementById('cq').value.trim().toLowerCase();
  const th = document.querySelector('#ct .is-active').dataset.v;
  const s = (p) => p.replace('semantic.color.', '').replace('component.', 'component.');
  document.getElementById('cbody').innerHTML = CON.filter((r) => (!th || r.theme === th) && (!q || (r.token + r.on).toLowerCase().includes(q))).map((r) => `<tr>
    <td><span class="pv" style="background:${r.bg};color:${r.fg}">Aa</span></td><td><code>${s(r.token)}</code></td><td><code>${s(r.on)}</code></td><td>${r.theme}</td>
    <td class="r">${r.ratio.toFixed(2)}</td><td>${r.level === 'exempt' ? '—' : r.min}</td>
    <td><span class="lvl ${r.level === 'AAA' ? 'AAA' : r.level === 'AA' ? 'AA' : r.level === 'exempt' ? 'ex' : 'ui'}">${r.pass ? '' : 'FAIL · '}${r.level}</span></td></tr>`).join('');
}

ch('colour', 'Colour', 'Warm graphite and paper neutrals, one ember accent that means “you”, and status hues that never travel without a glyph.',
  [['palettes', 'Raw palettes'], ['surfaces', 'Surfaces and text'], ['semantic', 'Semantic tokens'], ['status', 'Status'], ['dataviz', 'Data visualisation'], ['contrast', 'Contrast']],
  () => `
  ${sub('palettes', 'Raw palettes', 'Eleven-step OKLCH ramps (50 is the lightest, 950 the darkest), plus a six-step surface set per theme. Components never use these directly: semantic tokens map them to roles.')}
  <div class="ramp-head"><span></span>${['50', '100', '200', '300', '400', '500', '600', '700', '800', '900', '950'].map((s) => `<span>${s}</span>`).join('')}</div>
  <div class="ramps">${RAMPS.filter((r) => !['graphite', 'paper'].includes(r)).map(rampRow).join('')}</div>
  <h4 class="cap">Surface sets: 0 sunken · 1 canvas · 2 rail · 3 surface · 4 raised · 5 overlay</h4>
  <div class="ramps">${rampRow('graphite')}${rampRow('paper')}</div>
  ${why('<b>Why separate surface sets?</b> Dark interfaces need several surfaces within a narrow lightness band (L 0.17–0.27). A perceptual ramp steps 0.08 at a time, too coarse for that band, so graphite and paper keep the prototype’s hand-tuned values. The ramps are frozen hex values in tokens.json, so nothing drifts if the generator changes.')}

  ${sub('surfaces', 'Surfaces and text', 'Elevation is lightness: each surface sits one step above the one below. Every text role is checked on every surface it can land on, with the ratios shown below.')}
  ${pair((t) => `<div class="surfaces" style="background:var(--color-bg-canvas)">${SURF.map((s) => `<div style="background:var(--color-bg-${s})"><b>bg.${s}</b><span>${resolve('semantic.color.bg.' + s, t).hex}</span></div>`).join('')}</div>`)}
  <div style="height:12px"></div>
  ${stack((t) => textMatrix(t))}
  ${why('<b>Changed from the prototype:</b> tertiary text was <code>#807c6f</code> (dark) and <code>#847f71</code> (light), about 4.3:1 and 3.5:1 on cards. It is now <code>#aea796</code> and <code>#666256</code>, at AA or better on every surface. The light accent was 3.8:1 as text on paper. Text now uses the separate <code>text.accent</code> (ember 700, 5.2–6.7:1), and the bright ember is kept for fills.')}

  ${sub('semantic', 'Semantic tokens', 'The names components use. Each resolves to a different raw value per theme.')}
  ${stack((t) => semSwatches(t))}

  ${sub('status', 'Status', 'There are six states, each with fg (text and glyph), bg (a tinted wash), border and solid (dots and bars) tokens. Each is always shown with its glyph and a word. The accent is not a status: “needs you” uses the accent with a diamond.')}
  ${stack((t) => statusCards(t))}

  ${sub('dataviz', 'Data visualisation', 'The palettes were built with the dataviz method and checked with its validator against <code>bg.surface</code> in both themes. Categorical slots are assigned in this order and never cycled, and a ninth series folds into “Other”.')}
  ${pair(() => `${barChart()}<h4 class="cap">Three series: the most a scatter, map or small-multiple may carry</h4>${lineChart()}
    <h4 class="cap">Sequential (teal) · magnitude</h4>${seqStrip()}<h4 class="cap">Diverging (blue ↔ red) · delta from a baseline</h4>${divStrip()}`)}
  <div class="stat"><div><b>16.5</b><span>worst adjacent CVD ΔE, light (target ≥ 8)</span></div><div><b>12.8</b><span>worst adjacent CVD ΔE, dark</span></div><div><b>18.1</b><span>worst normal-vision ΔE, light (floor 15)</span></div><div><b>0</b><span>slots under 3:1 in either theme</span></div></div>
  ${rules([
    '<b>The accent never encodes data.</b> In a chart, ember appears only on the “now” cursor.',
    '<b>Status colours mean status.</b> Red and green are slots 5 and 7, so they appear only when there are many series. A pass/fail chart uses the status tokens instead, never both.',
    '<b>Labels use text tokens,</b> never the series colour. Bars and lines carry identity, and the legend and direct labels stay in ink.',
    '<b>One axis.</b> Two measures with different scales go in two charts.',
    '<b>Thin marks:</b> 2px lines, a 2px gap between stacked segments, a crosshair and tooltip on hover, and a table view for every chart.',
    'The full validator output is in <a href="tokens/dataviz-validation.txt">tokens/dataviz-validation.txt</a>.',
  ])}

  ${sub('contrast', 'Contrast', 'Every token that is used as text, as a control edge or as a data mark declares the backgrounds it sits on. <code>tools/tokens.mjs</code> checks each pair in both themes and fails the build below the minimum: 7 for primary text, 4.5 for all other text and 3 for UI marks.')}
  ${contrastTable()}`);

// =====================================================================
// 2. Type
// =====================================================================
const TYPES = Object.keys(T.semantic.type).filter((k) => !k.startsWith('$'));
const SAMPLE = {
  'display-xl': 'Hold, then release.', display: 'Calm under load.', h1: 'Needs you', h2: 'Runs, last 12 hours', h3: 'terraform apply on envs/prod: replace 1 server',
  h4: 'What it can do without asking', 'body-l': 'conductor runs AI bots and automations on your own machine. Anything risky waits for your passkey.',
  'body-m': 'platform-sre wants to replace the production web server to roll out a new image, in response to SEV2 “api p99 latency”.',
  'body-s': 'Folders only organise. They never grant access, except logins bound to a folder.', label: 'Approve and run', caption: 'Signed with your passkey · 2 minutes ago',
  overline: 'Running · 12h', 'numeric-l': '20', code: 'trigger: pagerduty.incident', 'code-s': 'r-7f2c · sha256:3f2a9c1d07e4',
};
const typeRow = (k) => {
  const t = T.semantic.type[k]; const v = t.$value; const x = t.$extensions['org.conductor.type'];
  const fam = v.fontFamily.split('.').pop().replace('}', '');
  const w = { '{primitive.font.weight.regular}': 400, '{primitive.font.weight.medium}': 500, '{primitive.font.weight.semibold}': 600 }[v.fontWeight];
  return `<div class="spec-row"><div class="nm">--type-${k}<small>${esc(t.$description)}</small></div>
    <div class="sample" style="font:var(--type-${k});letter-spacing:var(--type-${k}-tracking);text-transform:var(--type-${k}-transform)">${esc(SAMPLE[k])}</div>
    <div class="meta">${x.px}px · ${v.fontSize.value}rem / ${v.lineHeight}<br>${fam} ${w}${x.fontStyle === 'italic' ? ' italic' : ''} · ${v.letterSpacing.value ? (v.letterSpacing.value > 0 ? '+' : '') + v.letterSpacing.value + 'em' : '0'}${x.textTransform === 'uppercase' ? ' · caps' : ''}</div></div>`;
};
ch('type', 'Type', 'An italic serif for short human titles, a warm grotesque for everything people read, and a mono for what machines say.',
  [['families', 'Families'], ['scale', 'Scale'], ['weights', 'Weights and figures'], ['serif-rule', 'When to use the serif']],
  () => `
  ${sub('families', 'Families', 'All three are open-licensed and self-hosted as latin subsets with <code>font-display: swap</code>. Only the sans and the mono are preloaded, because the serif appears in titles alone.')}
  <div class="families">
    <div class="fam"><div class="glyph" style="font-family:var(--font-family-serif);font-style:italic">Aa</div><h4>Instrument Serif</h4><dl><dt>Role</dt><dd>Display: page and section titles, hero numbers, the wordmark</dd><dt>Styles</dt><dd>400 italic (UI), 400 roman (brand pages only)</dd><dt>Licence</dt><dd>SIL OFL 1.1 · Instrument</dd><dt>Fallback</dt><dd><code>ui-serif, Georgia, serif</code></dd><dt>File</dt><dd>22 KB woff2</dd></dl></div>
    <div class="fam"><div class="glyph" style="font-family:var(--font-family-sans);font-weight:500">Aa</div><h4>Instrument Sans</h4><dl><dt>Role</dt><dd>UI and body: everything people read and act on</dd><dt>Weights</dt><dd>400 · 500 · 600 (variable 400–700)</dd><dt>Licence</dt><dd>SIL OFL 1.1 · Instrument</dd><dt>Fallback</dt><dd><code>ui-sans-serif, system-ui, Segoe UI, Roboto</code></dd><dt>File</dt><dd>29 KB woff2, preloaded</dd></dl></div>
    <div class="fam"><div class="glyph" style="font-family:var(--font-family-mono);font-size:64px">Aa</div><h4>Martian Mono</h4><dl><dt>Role</dt><dd>Machine values: ids, digests, times, costs, YAML, logs</dd><dt>Weights</dt><dd>400 · 500, width 100% (comfortable) or 87.5% (compact)</dd><dt>Licence</dt><dd>SIL OFL 1.1 · Evil Martians</dd><dt>Fallback</dt><dd><code>ui-monospace, SF Mono, Menlo, Consolas</code></dd><dt>File</dt><dd>38 KB woff2 (weight and width axes), preloaded</dd></dl></div>
  </div>
  ${sub('scale', 'Scale', '1rem = 16px. The dashboard reads at body-m (14px), and docs and the README read at body-l (16px). Nothing in the product is smaller than 11px, and mono text is never smaller than 11.5px.')}
  <div class="specimen">${TYPES.map(typeRow).join('')}</div>
  ${why('<b>Use the shorthand, plus tracking.</b> <code>font: var(--type-h1)</code> sets style, weight, size, line height and family. CSS’s <code>font</code> shorthand can’t carry letter-spacing or text-transform, so add <code>letter-spacing: var(--type-h1-tracking)</code> (and <code>text-transform</code> for overline).')}
  ${sub('weights', 'Weights and figures')}
  <div class="weights">
    <div><b style="font-weight:400">Regular 400</b><span>Body, descriptions, table cells, code</span></div>
    <div><b style="font-weight:500">Medium 500</b><span>Buttons, tabs, nav, form labels, entity names in lists</span></div>
    <div><b style="font-weight:600">Semibold 600</b><span>h3/h4, overlines, counts. Nothing heavier: 700 is not used.</span></div>
  </div>
  <h4 class="cap">Numbers: tabular in columns, proportional in prose</h4>
  <div class="figs">
    <div class="panel surface"><table style="font-variant-numeric:proportional-nums"><tr><td>proportional (default)</td><td>1,111.10</td></tr><tr><td></td><td>$48.07</td></tr><tr><td></td><td>9,000.00</td></tr></table><p class="c-help" style="margin-top:8px">Digits wobble: wrong for tables.</p></div>
    <div class="panel surface"><table style="font-variant-numeric:tabular-nums"><tr><td>tabular-nums</td><td>1,111.10</td></tr><tr><td></td><td>$48.07</td></tr><tr><td></td><td>9,000.00</td></tr></table><p class="c-help" style="margin-top:8px">Use <code>font-variant-numeric: tabular-nums</code> in tables, timers, counters and axes.</p></div>
  </div>
  ${why('Instrument Serif has no tabular figures, so <b>serif numbers are for one hero value per card</b> and never appear in a column. Martian Mono is monospaced, so its digits always align.')}
  ${sub('serif-rule', 'When to use the serif', 'The audits found long italic titles hard to read. The serif is the product’s human voice, so it stays short and human.')}
  <div class="dodont">
    <div class="dd do">${verdict(true)}<div style="font:var(--type-h1);letter-spacing:var(--type-h1-tracking)">Needs you</div><div class="note">Page titles of one to four words, section titles, empty-state headlines and a single hero number.</div></div>
    <div class="dd dont">${verdict(false)}<div style="font:var(--type-h2);letter-spacing:var(--type-h2-tracking)">terraform apply of infra@3f2a9c1d07e4, dir envs/prod, workspace prod: +1 ~2 -0</div><div class="note">Generated or long titles, anything containing ids, paths or numbers, and anything under 20px.</div></div>
    <div class="dd do">${verdict(true)}<div><div style="font:var(--type-h3)">Replace the production web server</div><div class="c-mono" style="color:var(--color-text-tertiary);margin-top:4px">infra@3f2a9c1d · envs/prod · +1 ~2 −0</div></div><div class="note">Generated titles use h3 in the sans, with the machine details on a mono line below.</div></div>
    <div class="dd dont">${verdict(false)}<div><span class="c-btn primary" style="font-family:var(--font-family-serif);font-style:italic;font-size:17px">Approve</span></div><div class="note">Never in controls, labels, tables, badges or form fields.</div></div>
  </div>
  ${rules(['<b>Serif:</b> five words or fewer, written by a person, at 20px or larger, in a title role.', '<b>Sans:</b> everything people read or act on.', '<b>Mono:</b> only values a machine produced or will parse, such as ids, hashes, paths, times, costs and config. It is never used for prose or labels.', '<b>Sentence case</b> everywhere. Only the overline style is uppercase.'])}
  `);

// =====================================================================
// 3. Space & layout
// =====================================================================
const SPACES = Object.entries(T.primitive.space).filter(([k]) => !k.startsWith('$')).sort((a, b) => a[1].$value.value - b[1].$value.value);
const densityTokens = [...flat.entries()].filter(([, t]) => t.modes && 'compact' in t.modes);
const sampleTable = (extra = '') => `<div class="c-table-wrap"><table class="c-table"><thead><tr><th style="width:28px"></th><th>Run</th><th>Workflow</th><th>Status</th><th class="num">Duration</th><th class="num">Cost</th></tr></thead><tbody>
  ${[['r-7f2c', 'pr-review', 'success', 'Passed', '1m 12s', '$0.04'], ['r-7f2b', 'ci-fixer', 'danger', 'Failed', '6m 40s', '$0.31'], ['r-7f29', 'issue-triage', 'running', 'Running', '0m 22s', '$0.01'], ['r-7f27', 'platform-sre', 'warning', 'Waiting on you', '12m 03s', '$0.18'], ['r-7f22', 'dep-bump', 'neutral', 'Parked', '—', '—']]
    .map(([id, wf, s, l, d, c], i) => `<tr class="${i === 1 ? 'is-selected' : ''} ${extra && i === 2 ? extra : ''}"><td><span class="check">${i === 1 ? lu('check') : ''}</span></td><td><span class="c-mono">${id}</span></td><td>${wf}</td><td><span class="c-badge" data-s="${s}">${sg(s === 'neutral' ? 'neutral' : s, s === 'running' ? 'c-glyph-spin' : '')}${l}</span></td><td class="num">${d}</td><td class="num">${c}</td></tr>`).join('')}
</tbody></table></div>`;
ch('layout', 'Space and layout', 'A 4px base, a 12-column grid on desktop and two density modes for the same components.',
  [['space', 'Spacing'], ['grid', 'Grid and breakpoints'], ['containers', 'Content widths'], ['density', 'Density']],
  () => `
  ${sub('space', 'Spacing', 'Everything sits on a 4px grid. The 2px and 6px steps exist for optical nudges inside controls, never for layout.')}
  <div class="spaces">${SPACES.map(([k, t]) => `<div><span>--space-${k}</span><span>${t.$description}</span><i style="width:var(--space-${k})"></i></div>`).join('')}</div>
  ${sub('grid', 'Grid and breakpoints', 'Phone is designed at 390, tablet at 834, desktop at 1440 and wide at 1920. These are min-widths: CSS can’t read custom properties in media queries, so use the numbers literally.')}
  <div class="bps">
    ${[['Phone', '< 600', 4, '16', '12', 'Bottom tab bar, sheets instead of drawers, comfortable density only, 44px targets'], ['Tablet', '600–1023', 8, '24', '16', 'Rail collapses to icons, drawers at most 80% wide'], ['Desktop', '1024–1599', 12, '28 (16)', '16 (12)', 'Rail (224 / 200) + top strip + page'], ['Wide', '≥ 1600', 12, 'auto', '20', 'Page capped at 1480 and centred, side panels allowed']]
      .map(([n, r, c, m, g, note]) => `<div class="bp"><b>${n}</b><div class="cols" style="grid-template-columns:repeat(${c},1fr)">${'<i></i>'.repeat(c)}</div><dl><dt>Width</dt><dd>${r}</dd><dt>Columns</dt><dd>${c}</dd><dt>Margin</dt><dd>${m}</dd><dt>Gutter</dt><dd>${g}</dd></dl><span class="c-help">${note}</span></div>`).join('')}
  </div>
  ${sub('containers', 'Content widths')}
  <div class="containers">${Object.entries(T.semantic.layout.container).map(([k, t]) => `<div><i style="width:calc(${t.$value.value} * 1rem * .62)">${t.$value.value * 16}px</i><span>--layout-container-${k}</span><span class="c-help">${t.$description ?? ''}</span></div>`).join('')}</div>
  ${sub('density', 'Density', 'Comfortable is the default. Compact is for operators at scale: 30px rows, tighter padding, 13px tables and a narrower mono. It is a token set, not a separate design: set <code>data-density="compact"</code> on any element. Phones always use comfortable.')}
  <div class="densities">
    <div><h4 class="cap">Comfortable</h4><div data-density="comfortable">${sampleTable()}</div></div>
    <div><h4 class="cap">Compact</h4><div data-density="compact">${sampleTable()}</div></div>
  </div>
  <h4 class="cap">Density tokens</h4>
  <div class="dens-tbl"><span class="h">Token</span><span class="h">Comfortable</span><span class="h">Compact</span>
  ${densityTokens.map(([p, t]) => `<code>${cssName(p)}</code><span>${t.modes.comfortable.value ?? t.modes.comfortable}${t.modes.comfortable.unit ?? ''}</span><span>${t.modes.compact.value ?? t.modes.compact}${t.modes.compact.unit ?? ''}</span>`).join('')}</div>
  `);

// =====================================================================
// 4. Shape & depth
// =====================================================================
ch('shape', 'Shape and depth', 'Small radii, hairline borders, and depth that comes from surface lightness first and shadow second.',
  [['radii', 'Radii and borders'], ['elevation', 'Elevation'], ['layers', 'Layers']],
  () => `
  ${sub('radii', 'Radii and borders', 'Radii nest: a control (5) inside a card (8) inside an overlay (12). Pills are only for chips and counts.')}
  <div class="radii">${Object.entries(T.primitive.radius).filter(([k]) => !k.startsWith('$')).map(([k, t]) => `<div><i style="border-radius:min(var(--radius-${k}), 38px)"></i><span>--radius-${k}</span><small>${t.$value.value}px${{ xs: ' · badges', sm: ' · controls', md: ' · cards', lg: ' · overlays', xl: ' · sheets', pill: ' · chips' }[k] ?? ''}</small></div>`).join('')}</div>
  <h4 class="cap">Borders</h4>
  <div class="borders">
    <div><i style="border:var(--border-width) solid var(--color-border-default)"></i>hairline 1px · every edge</div>
    <div><i style="border:var(--border-width) solid var(--color-border-control)"></i>1px border.control · inputs (3:1)</div>
    <div><i style="outline:var(--border-focus) solid var(--color-focus-ring);outline-offset:var(--border-focus)"></i>2px focus ring, 2px offset</div>
    <div><i style="box-shadow:inset var(--border-indicator) 0 0 var(--color-accent-default)"></i>3px indicator · active nav, needs you</div>
  </div>
  ${sub('elevation', 'Elevation', 'Each level pairs a surface with a shadow. On dark, depth comes mostly from the lighter surface, and the shadow just separates. On light, the shadow does more of the work.')}
  ${stack(() => `<div class="elev">${[['0', 'canvas', 'Page'], ['1', 'surface', 'Cards, panels'], ['2', 'raised', 'Sticky headers, menus'], ['3', 'overlay', 'Dialogs, drawers'], ['4', 'overlay', 'Toasts, ⌘K']].map(([n, s, u]) => `<div style="background:var(--color-bg-${s});box-shadow:var(--shadow-${n})"><b>--shadow-${n}</b><span>bg.${s} · ${u}</span></div>`).join('')}</div>`)}
  ${sub('layers', 'Layers', 'Use z-index only through these tokens. Anything that floats belongs to exactly one layer.')}
  <div class="zs">${Object.entries(T.semantic.z).filter(([k]) => !k.startsWith('$')).map(([k, t]) => `<div><code>--z-${k}</code><span>${t.$value}</span><i style="width:${Math.min(100, 8 + Math.log10(t.$value + 1) * 24)}%"></i></div>`).join('')}</div>
  `);

// =====================================================================
// 5. Icons
// =====================================================================
const GLYPH_S = { success: 'success', warning: 'warning', danger: 'danger', info: 'info', running: 'running', neutral: 'neutral', skipped: 'neutral', shadow: 'info', 'needs-you': 'accent' };
ch('icons', 'Iconography', 'Lucide outline icons for objects and actions, plus nine status glyphs of our own drawn to match.',
  [['icon-style', 'Style'], ['icon-set', 'Set'], ['glyphs', 'Status glyphs']],
  () => `
  ${sub('icon-style', 'Style', 'Outline only, with round caps and joins, drawn on a 24-unit grid with a 2-unit live-area padding. At 16px a 2-unit stroke renders at 1.33px, which matches the type’s stem weight.')}
  <div class="icon-anatomy">
    <div class="big"><svg viewBox="0 0 24 24" aria-hidden="true"><g stroke="var(--color-border-default)" stroke-width=".05">${Array.from({ length: 25 }, (_, i) => `<line x1="${i}" y1="0" x2="${i}" y2="24"/><line x1="0" y1="${i}" x2="24" y2="${i}"/>`).join('')}</g><rect x="2" y="2" width="20" height="20" fill="none" stroke="var(--color-accent-default)" stroke-width=".08" stroke-dasharray=".4 .3"/><g fill="none" stroke="var(--color-text-primary)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${LUCIDE.workflow}</g></svg></div>
    <div>${rules([
      '<b>Set:</b> <a href="https://lucide.dev">Lucide</a> (ISC licence). The subset we use is vendored in <code>icons/lucide/</code>.',
      '<b>Sizes:</b> 16px beside body text, 14px in compact density and badges, 20px in empty states and pickers, 24px in phone tab bars.',
      '<b>Stroke:</b> keep Lucide’s 2 at 14–20px and use 1.5 at 24px, so the line stays about 1.3px.',
      '<b>Colour:</b> <code>icon.default</code> beside text and <code>icon.muted</code> for decoration (still 3:1). An icon takes the status colour only when it is a status glyph.',
      '<b>Never</b> show an icon alone without a label or tooltip (except a universal close ×), never fill outline icons, and never mix icon sets.',
      '<b>Drawing new icons:</b> 24 grid, 2px stroke, round caps and joins, 2px corner radius on rectangles, no fills, and the optical size of the Lucide neighbours.',
    ])}
    <div class="icon-sizes">${[14, 16, 20, 24].map((s) => `<div><span style="width:${s}px;height:${s}px;display:block;color:var(--color-icon-default)">${lu('bot').replace('<svg ', `<svg width="${s}" height="${s}" ${s === 24 ? 'stroke-width="1.5" ' : ''}`)}</span>${s}px</div>`).join('')}</div></div>
  </div>
  ${sub('icon-set', 'Set', 'This is the vocabulary the dashboard uses. Each concept gets exactly one icon.')}
  <div class="icon-grid">${Object.keys(LUCIDE).map((n) => `<div>${lu(n)}<span>${n}</span></div>`).join('')}</div>
  ${sub('glyphs', 'Status glyphs', 'Drawn on a 16-unit grid with a 1.5 stroke and round caps, so each is legible at 13px. Each state has a distinct shape, so they read in one colour and to colour-blind readers. Running is the only one that moves.')}
  ${pair(() => `<div class="glyphs" style="grid-template-columns:1fr 1fr">${Object.entries(STATUS).map(([k, g]) => `<div data-s="${GLYPH_S[k]}">${sg(k, k === 'running' ? 'c-glyph-spin' : '')}<b>${k}</b><span>${g.label}</span></div>`).join('')}</div>`)}
  `);

// =====================================================================
// 6. Motion
// =====================================================================
const DURS = [['fast', 'Hover, press, toggles'], ['base', 'Reveal, expand, tab content'], ['slow', 'Drawers, dialogs, sheets'], ['slower', 'Page-level reveals, chart draw-in']];
const EAS = [['out', 'Default: arriving, responding'], ['in', 'Leaving'], ['in-out', 'Moving across the screen'], ['linear', 'Loops only']];
const curve = (b) => { const [a, c, d, e] = b; return `<svg viewBox="-4 -4 108 108" aria-hidden="true"><rect x="0" y="0" width="100" height="100" fill="none" stroke="var(--color-border-subtle)"/><path d="M0 100 C${a * 100} ${100 - c * 100} ${d * 100} ${100 - e * 100} 100 0" fill="none" stroke="var(--color-accent-default)" stroke-width="2.5" stroke-linecap="round"/></svg>`; };
ch('motion', 'Motion', 'Motion explains a change. It never decorates, delays or celebrates.',
  [['durations', 'Durations and easing'], ['motion-rules', 'What moves, what never does']],
  () => `
  ${sub('durations', 'Durations and easing', 'Press <b>Play</b> to run each duration. Under reduced motion, durations collapse (shown live when Motion is set to Reduced above).')}
  <p><button class="c-btn secondary sm" id="play">${lu('play')}Play</button></p>
  <div class="durs" id="durs">${DURS.map(([k, u]) => `<div class="dur"><code>--motion-duration-${k}</code><span id="d-${k}"></span><div class="track"><i style="transition-duration:var(--motion-duration-${k});transition-timing-function:var(--motion-easing-out)"></i></div></div>`).join('')}</div>
  <div class="easings" id="eas" style="margin-top:var(--space-5)">${EAS.map(([k, u]) => { const b = T.primitive.easing[k].$value; return `<div class="easing" style="--e:var(--motion-easing-${k})">${curve(b)}<b>--motion-easing-${k}</b><span>${u} · cubic-bezier(${b.join(', ')})</span><div class="ball"><i></i></div></div>`; }).join('')}</div>
  ${sub('motion-rules', 'What moves, what never does')}
  <div class="dodont">
    <div class="dd do">${verdict(true)}${rules(['Hover and press colour changes (fast).', 'Drawers and sheets sliding in from their edge (slow, out), and leaving faster (base, in).', 'Dialogs fading and rising 8px (slow).', 'Toasts arriving 4px from below.', 'The running glyph turning (1.2s per turn, linear) and the live dot pulsing (2.4s).', 'Skeleton shimmer while content loads, never longer than the load.'])}</div>
    <div class="dd dont">${verdict(false)}${rules(['Anything after an approval or a deploy: no confetti, no checkmark bursts. Signing is serious.', 'Counting numbers up (cost, approvals, runs). Show the value.', 'The logo. Not as a spinner, and not on load.', 'Content moving under the pointer, or layout shifting after load.', 'Page transitions that delay content.', 'Blinking or flashing more than three times a second, ever.'])}</div>
  </div>
  ${why('<b>Reduced motion</b> (the OS setting, or <code>data-motion="reduced"</code>): travel distances become 0, the spin and pulse stop (the running glyph stays as a static arc and the word still says “Running”), and longer durations shrink to 120ms. Colour and opacity transitions remain, because they help orientation and don’t move anything.')}
  `);

// =====================================================================
// 7. Components
// =====================================================================
const STATES = ['Default', 'Hover', 'Focus', 'Active', 'Disabled', 'Loading'];
const stCls = { Default: '', Hover: 'is-hover', Focus: 'is-focus', Active: 'is-active', Disabled: 'is-disabled', Loading: 'is-loading' };
const spin = `<svg class="c-spin" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round">${STATUS.running.svg}</svg>`;
const btnSheet = () => `<div class="grid-sheet" style="grid-template-columns:90px repeat(6, minmax(0,1fr))">
  <span></span>${STATES.map((s) => `<span class="h">${s}</span>`).join('')}
  ${[['primary', 'Approve'], ['secondary', 'Review'], ['ghost', 'Cancel'], ['danger', 'Delete']].map(([v, l]) => `<span class="rl">${v}</span>${STATES.map((s) => `<div><button class="c-btn ${v} ${stCls[s]}" ${s === 'Disabled' ? 'disabled' : ''} tabindex="-1">${s === 'Loading' ? spin : ''}${l}</button></div>`).join('')}`).join('')}
  <span class="rl">sizes</span><div style="grid-column:span 6;display:flex;gap:12px;align-items:center"><button class="c-btn primary sm" tabindex="-1">Small · ${'sm'}</button><button class="c-btn primary" tabindex="-1">${lu('fingerprint')}Approve and run</button><button class="c-btn primary lg" tabindex="-1">Large · lg</button><button class="c-btn secondary" tabindex="-1">${lu('play')}Run a workflow</button><button class="c-btn ghost sm" tabindex="-1">${lu('ellipsis')}</button></div>
</div>`;
const fld = (label, cls, inner, help = '', opt = '') => `<div class="c-field"><div class="c-label">${label}${opt ? `<span class="opt">${opt}</span>` : ''}</div><div class="c-input ${cls}">${inner}</div>${help}</div>`;
const fieldSheet = () => `<div class="grid-sheet" style="grid-template-columns:repeat(4, minmax(0,1fr));align-items:start">
  ${fld('Name', '', '<span class="ph">e.g. alert-watcher</span>', '<div class="c-help">Lowercase, with dashes. Shown everywhere this bot is.</div>')}
  ${fld('Name', 'is-hover', '<span class="ph">e.g. alert-watcher</span>', '<div class="c-help">Hover</div>')}
  ${fld('Name', 'is-focus', 'alert-watch<span style="border-left:1.5px solid var(--color-text-primary);height:16px;margin-left:-6px"></span>', '<div class="c-help">Focus</div>')}
  ${fld('Name', '', 'alert-watcher', '<div class="c-help">Filled</div>')}
  ${fld('Channel', 'is-error is-focus', '<span class="ph">Pick a channel</span>', `<div class="c-error">${sg('danger')}Pick a channel to post to.</div>`)}
  ${fld('Workspace', 'is-disabled', 'prod', '<div class="c-help">Disabled: set by the pack</div>')}
  ${fld('Repository', '', `${lu('git-pull-request')}<span>acme/web</span>${spin}`, '<div class="c-help">Loading: checking access…</div>')}
  ${fld('Schedule', '', `${lu('calendar-clock')}<span>Mondays at 06:00</span>`, '<div class="c-help"><span class="c-mono">0 6 * * 1</span> · shown as words</div>', 'optional')}
</div>`;
const vsSheet = () => `<div style="display:grid;grid-template-columns:minmax(0,1.2fr) minmax(0,1fr);gap:var(--space-5);align-items:start">
  <div style="display:grid;gap:var(--space-4)">
    <div class="c-field"><div class="c-label">Slack bot token</div><div class="c-vs"><span class="src">${lu('lock')}Secret${lu('chevron-right', 'chev')}</span><span class="val"><span class="c-mono ref">slack/bot-token</span><span class="ok">${sg('success')}found</span></span></div><div class="c-help">Stored in conductor’s secret store. The value never comes back to the browser.</div></div>
    <div class="c-field"><div class="c-label">GitHub App key</div><div class="c-vs is-focus"><span class="src">${lu('key-round')}Vault item${lu('chevron-right', 'chev')}</span><span class="val"><span class="c-mono ref">op://ops/github-app/key</span></span></div><div class="c-help">Focus</div></div>
    <div class="c-field"><div class="c-label">Webhook secret</div><div class="c-vs" style="border-color:var(--input-border-error)"><span class="src">${lu('lock')}Secret${lu('chevron-right', 'chev')}</span><span class="val"><span class="c-mono ref">github/webhook</span></span></div><div class="c-error">${sg('danger')}No secret called github/webhook. Create it, or pick another.</div></div>
  </div>
  <div><div class="c-vs-menu">
    <div class="opt on">${lu('lock')}<b>Secret</b><span>Recommended. Stored encrypted by conductor.</span></div>
    <div class="opt">${lu('key-round')}<b>Vault item</b><span>1Password or Bitwarden, read when needed.</span></div>
    <div class="opt">${lu('terminal')}<b>Environment variable</b><span>Read from conductor’s environment.</span></div>
    <hr><div class="more">Other sources: typed value (not secret), setting</div>
  </div></div>
</div>`;
const choiceSheet = () => `<div style="display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:var(--space-3)">
  <div class="c-choice"><span class="radio"></span><b>Read only</b><p>Looks, never touches. Reads repos, alerts and dashboards.</p></div>
  <div class="c-choice is-hover"><span class="radio"></span><b>Can comment and post</b><p>Writes things you can delete: comments, messages.</p></div>
  <div class="c-choice is-selected"><span class="radio"></span><b>Asks before anything else</b><p>Every change waits for your passkey.</p><span class="c-badge tag" data-s="accent">Recommended</span></div>
  <div class="c-choice is-disabled"><span class="radio"></span><b>Acts on its own</b><p>Not allowed: the team’s ceiling is “asks first”.</p></div>
</div>`;
const tabsSheet = () => `<div style="display:grid;gap:var(--space-5)">
  <div class="c-tabs"><span class="c-tab is-active">Overview</span><span class="c-tab is-hover">Runs <span class="count">412</span></span><span class="c-tab">Access</span><span class="c-tab">Memory</span><span class="c-tab is-disabled">Billing</span></div>
  <div style="display:flex;gap:var(--space-4);flex-wrap:wrap;align-items:center"><div class="c-seg"><span>1h</span><span class="is-active">12h</span><span class="is-hover">24h</span><span>7d</span></div><div class="c-seg"><span class="is-active">Comfortable</span><span>Compact</span></div>
  <span class="c-help">Tabs switch views of one thing; a segmented control switches a setting. Neither ever navigates to another page.</span></div>
</div>`;
const listSheet = () => `${sampleTable('is-focus')}<div style="height:12px"></div><div class="c-table-wrap"><table class="c-table"><tbody>
  ${[['needs-you', 'accent', 'terraform apply · infra@3f2a9c1d', 'platform-sre · p12', '7m'], ['danger', 'danger', 'ci-fixer failed on acme/web#1288', 'step test · 2 failures', '52m'], ['warning', 'warning', 'hand-off awaiting answer · acme/infra#77', 'pr-review', '1h']].map(([g, s, t, m, a]) => `<tr><td style="width:28px;color:var(--s-fg)" data-s="${s}">${sg(g)}</td><td><div style="font-weight:500">${t}</div><div class="c-mono sub">${m}</div></td><td class="num sub">${a}</td><td style="width:1%">${s === 'accent' ? '<button class="c-btn primary sm" tabindex="-1">Review</button>' : lu('chevron-right')}</td></tr>`).join('')}
</tbody></table></div>`;
const badgeSheet = () => `<div style="display:grid;gap:var(--space-4)">
  <div style="display:flex;gap:var(--space-2);flex-wrap:wrap">${STAT.map((s) => `<span class="c-badge" data-s="${s}">${sg(s, s === 'running' ? 'c-glyph-spin' : '')}${STAT_EX[s][0]}</span>`).join('')}<span class="c-badge" data-s="accent">${sg('needs-you')}Needs you</span><span class="c-badge">${lu('package')}pack</span><span class="c-badge">read</span></div>
  <div style="display:flex;gap:var(--space-2);flex-wrap:wrap;align-items:center"><span class="c-chip">${lu('folder')}All folders</span><span class="c-chip is-hover">work</span><span class="c-chip is-selected">${lu('check')}personal</span><span class="c-count">6</span><span style="display:flex;gap:6px;align-items:center" data-s="success"><span class="c-dot live"></span><span class="c-help">live</span></span><span data-s="running"><span class="c-dot"></span></span><span data-s="danger"><span class="c-dot"></span></span></div>
  <span class="c-help">Badges label state (glyph + word). Chips filter (they toggle). The orange count appears only where a person is needed.</span>
</div>`;
const feedbackSheet = () => `<div style="display:grid;grid-template-columns:minmax(0,1.2fr) minmax(0,1fr);gap:var(--space-4);align-items:start">
  <div style="display:grid;gap:var(--space-3)">
    <div class="c-banner" data-s="warning">${sg('warning')}<div><b>Sandbox is off for this host</b><p>Bots on gpu-box can reach the whole network. Turn the sandbox on, or allow only the hosts they need.</p></div><button class="c-btn secondary sm" tabindex="-1">Fix</button></div>
    <div class="c-banner" data-s="danger">${sg('danger')}<div><b>Couldn’t post to #incidents</b><p>Slack says the bot isn’t in that channel. Invite @conductor to #incidents, then retry.</p></div><button class="c-btn secondary sm" tabindex="-1">Retry</button></div>
    <div class="c-banner" data-s="info">${sg('info')}<div><b>v0.72.0 is ready</b><p>Preflight passed. It installs within 10 minutes; running work finishes first.</p></div></div>
  </div>
  <div style="display:grid;gap:var(--space-3);justify-items:start">
    <div class="c-toast" data-s="success">${sg('success')}<div><b>Approved</b><p>conductor is applying the plan.</p></div><div class="act"><button class="c-btn ghost sm" tabindex="-1">View run</button></div></div>
    <div class="c-toast" data-s="danger">${sg('danger')}<div><b>Deploy failed at step 3</b><p>Nothing was changed: the plan was rolled back.</p></div><div class="act"><button class="c-btn ghost sm" tabindex="-1">Open</button></div></div>
    <div class="c-toast" data-s="neutral">${sg('neutral')}<div><b>Dispatch paused</b><p>New events queue until you resume.</p></div><div class="act"><button class="c-btn ghost sm" tabindex="-1">Undo</button></div></div>
  </div>
</div>`;
const overlaySheet = () => `<div style="display:grid;grid-template-columns:1.1fr 1.1fr .8fr;gap:var(--space-3)">
  <div class="c-stage" style="height:280px;display:grid;place-items:center"><div class="scrim"></div><div class="c-modal"><div class="hd"><h3>Delete the nightly-backup workflow?</h3></div><div class="bd">Its 412 past runs stay in history. Scheduled runs stop immediately. You can’t undo this.</div><div class="ft"><button class="c-btn ghost" tabindex="-1">Cancel</button><button class="c-btn danger" tabindex="-1">Delete workflow</button></div></div></div>
  <div class="c-stage" style="height:280px"><div class="scrim"></div><div class="c-drawer"><div class="hd">${lu('bot')}platform-sre</div><div class="bd"><span class="c-badge" data-s="running">${sg('running', 'c-glyph-spin')}Working on case c29</span><span class="c-help">Drawers show detail without leaving the list. Esc closes them, and focus returns to the row.</span><span class="c-skel" style="width:80%"></span><span class="c-skel" style="width:60%"></span></div></div></div>
  <div class="c-stage" style="height:280px"><div class="scrim"></div><div class="c-sheet"><div class="handle"></div><div style="font:var(--type-h3);margin-bottom:4px">Approve p12?</div><div class="c-help" style="margin-bottom:12px">Replace the production web server.</div><div style="display:grid;gap:8px"><button class="c-btn primary lg" tabindex="-1" style="width:100%">${lu('fingerprint')}Approve</button><button class="c-btn secondary lg" tabindex="-1" style="width:100%">Deny…</button></div></div></div>
</div>`;
const stepperSheet = () => `<div style="display:grid;gap:var(--space-4)">
  <div class="c-stepper"><span class="c-step done"><i>${lu('check')}</i>Purpose</span><span class="c-step-line done"></span><span class="c-step done"><i>${lu('check')}</i>Watches</span><span class="c-step-line done"></span><span class="c-step current"><i>3</i>Tells you where</span><span class="c-step-line"></span><span class="c-step"><i>4</i>Permissions</span><span class="c-step-line"></span><span class="c-step"><i>5</i>Review</span></div>
  <div class="c-stepper"><span class="c-step done"><i>${lu('check')}</i>Edit</span><span class="c-step-line done"></span><span class="c-step error"><i>!</i>Validate on a copy</span><span class="c-step-line"></span><span class="c-step"><i>3</i>Review the change</span><span class="c-step-line"></span><span class="c-step"><i>4</i>Apply</span></div>
  <span class="c-help">At most five steps, each named for what you decide, not for the file it edits. The review lives inside the wizard, and Back never loses input.</span>
</div>`;
const emptySheet = () => `<div style="display:grid;grid-template-columns:1fr 1fr;gap:var(--space-3)">
  <div class="panel surface" style="padding:0"><div class="c-empty"><div class="ic">${sg('success')}</div><h4>Nothing needs you</h4><p>When a bot asks for approval or a run fails, it shows up here. You can also get these on your phone.</p><button class="c-btn secondary" tabindex="-1">${lu('bell')}Set up notifications</button></div></div>
  <div class="panel surface" style="padding:0"><div class="c-empty"><div class="ic">${lu('eye')}</div><h4>Not watching anything yet</h4><p>alert-watcher has no triggers, so it won’t do anything on its own. Add an alert, a repository or a schedule.</p><button class="c-btn primary" tabindex="-1">${lu('plus')}Add what it watches</button></div></div>
</div>`;
const Y = (k, rest) => `<span class="k">${k}</span><span class="p">:</span>${rest}`;
const codeSheet = () => `<div style="display:grid;grid-template-columns:1fr 1fr;gap:var(--space-3)">
  <div class="c-code"><div class="hd">${lu('scroll-text')}config.yaml · workflows › alert-watcher</div><pre>${[
    ['1', '', `<span class="c"># Post incidents to Slack</span>`],
    ['2', '', Y('alert-watcher', '')],
    ['3', '', '  ' + Y('on', ' <span class="s">pagerduty.incident</span>')],
    ['4', '', '  ' + Y('filter', ' <span class="s">"urgency == \'high\'"</span>')],
    ['5', '', '  ' + Y('steps', '')],
    ['6', '', '    <span class="p">-</span> ' + Y('use', ' <span class="s">slack-ops.post</span>')],
    ['7', '', '      ' + Y('channel', ' <span class="s">"#incidents"</span>')],
    ['8', '', '      ' + Y('text', ' <span class="a">{{ .incident.title }}</span>')],
    ['9', '', '      ' + Y('retries', ' <span class="num">3</span>')],
    ['10', '', '      ' + Y('draft', ' <span class="b">false</span>')],
  ].map(([n, m, l]) => `<div class="ln"><span class="n">${n}</span><span class="m">${m}</span><span>${l}</span></div>`).join('')}</pre></div>
  <div class="c-code"><div class="hd">${lu('scroll-text')}Review the change · 1 added, 1 removed</div><pre>${[
    ['', 'add', '6', '+', '    <span class="p">-</span> ' + Y('use', ' slack-ops.post')],
    ['7', '', '7', '', '      ' + Y('channel', ' <span class="s">"#incidents"</span>')],
    ['8', 'del', '', '−', '      ' + Y('text', ' "{{ .kind }} on {{ .workflow }}"')],
    ['', 'add', '8', '+', '      ' + Y('text', ' "{{ .incident.title }}"')],
    ['9', '', '9', '', '      ' + Y('retries', ' <span class="num">3</span>')],
  ].map(([, c, n, m, l]) => `<div class="ln ${c}"><span class="n">${n}</span><span class="m">${m}</span><span>${l}</span></div>`).join('')}</pre></div>
</div>
${rules(['<b>No space before a colon</b>, ever: <code>channel: "#incidents"</code>, never <code>channel : …</code>. Keys and colons are separate spans with no whitespace between them.', '<b>Diffs</b> use a gutter mark (+ / −) and a 2px bar as well as the tint, so they still read in one colour.', '<b>Header</b> names the file and the enclosing key (<code>workflows › alert-watcher</code>), not just a line number.', 'Comments in light theme are <code>#666256</code> (4.7:1). The prototype’s were 2.9:1.'])}`;
const COMP = [
  ['buttons', 'Buttons', 'Four variants and three sizes. There is one primary per view: the thing a person came to do. Danger is only for destructive actions, and every destructive action is confirmed.', btnSheet, true],
  ['fields', 'Fields', 'The label sits above the field, with helper text below that swaps to the error message. Errors appear on blur or submit, never while a field is untouched (an empty new form is a to-do list, not a list of errors).', fieldSheet, true],
  ['value-source', 'Value source', 'This field takes a value from a source. Secret is the default and the source sits in a quiet menu, instead of a five-way segmented control in front of every field (from the newcomer audit).', vsSheet, true],
  ['choice', 'Choice cards', 'Use these for decisions that need a sentence each, such as permission presets, connector methods and wizard forks. Never use more than five; the rest go under “Advanced”.', choiceSheet, true],
  ['tabs', 'Tabs and segmented controls', null, tabsSheet, true],
  ['tables', 'Tables and dense lists', 'Text is left-aligned and numbers are right-aligned with tabular figures. The status column always has a glyph and a word, and ids are in mono. Rows are focusable (j/k to move, x to select, Enter to open). Selection uses the accent wash and a 3px bar.', listSheet, true],
  ['badges', 'Badges, chips and counts', null, badgeSheet, true],
  ['feedback', 'Banners and toasts', 'Banners are for conditions that persist (until fixed). Toasts are for things that just happened, last 6 seconds (errors stay until dismissed) and sit above the phone tab bar. Every message says what happened and what to do next.', feedbackSheet, true],
  ['overlays', 'Dialogs, drawers and sheets', 'Dialogs are for decisions, drawers for detail beside a list, and sheets for the phone. All of them trap focus, close on Esc and return focus to where you were.', overlaySheet, true],
  ['steppers', 'Steppers and wizards', null, stepperSheet, true],
  ['empty', 'Empty states', 'Say what will appear here, why it’s empty and the one next step. The headline is a short human sentence, so it can use the serif.', emptySheet, true],
  ['code', 'Code, YAML and diffs', null, codeSheet, true],
];
ch('components', 'Components', 'Guidance and token mapping, not a library. Each sheet shows every state, in both themes, at the density selected in the header.',
  COMP.map(([id, t]) => [id, t]),
  () => COMP.map(([id, t, lede, fn]) => `${sub(id, t, lede ?? '')}${stack(fn)}`).join('') + why('<b>Token mapping:</b> every value in these sheets is a component token (<code>--button-primary-bg</code>, <code>--input-border-focus</code>, <code>--table-row-h</code>…) that aliases a semantic token. The full map is in <code>tokens.json › component</code>, and <code>book/components.css</code> shows how each one is used.'));

// =====================================================================
// 8. Voice
// =====================================================================
const VOICE = [
  ['Plain words first', 'Then the technical term, if anyone needs it.', '“Asks you first” <span class="c-mono">(propose)</span>', '“tier: propose”'],
  ['Say what changes', 'Name what, where, how much, and whether it can be undone.', 'Replaces 1 production server. You can roll back by re-applying the previous image.', '+1 ~2 −0 · REPLACES aws_instance.web'],
  ['State the risk plainly', 'Don’t reassure. Describe.', 'homelab will be able to sign in to nas.home.example as you.', 'Binds credential to principal. Not a stateful type, so it isn’t refused.'],
  ['No internals', 'No issue numbers, codenames or raw ids in headings.', 'Only when you started the task', 'Provenance (recommended, as in #182)'],
  ['Define every noun once', 'Put a dotted underline and a one-line definition on first use.', 'A <u style="text-decoration-style:dotted">workflow</u> is a fixed recipe: when something happens, do these steps.', 'Workflows: triggers and the steps they run'],
  ['Verbs on buttons', 'The button says what happens. There is no “OK”, “Submit” or “Yes”.', '<span class="c-btn primary sm">Delete workflow</span>', '<span class="c-btn primary sm">OK</span>'],
  ['Humans read times', 'Use words for schedules and relative times, with the exact value on hover.', 'Mondays at 06:00 · 52 minutes ago', '0 6 * * 1 · 2026-10-16T22:11:04Z'],
  ['Calm, not cute', 'No exclamation marks, “oops”, “please” or “I’m thinking”. Only bots speak in the first person, and only in chat.', 'Couldn’t reach GitHub. Retrying in 30 seconds.', 'Oops! Something went wrong :('],
];
const NAMES = [
  ['bot', 'An AI teammate with memory. You can chat with it; it watches things and proposes actions.', 'lowercase-kebab: platform-sre. In prose: “the platform-sre bot”.'],
  ['team', 'Bots that share a lead and a permission ceiling.', 'lowercase: billing, platform.'],
  ['workflow', 'A fixed recipe: when a trigger fires, run these steps. No judgement involved.', 'lowercase-kebab verbs or nouns: pr-review, nightly-backup.'],
  ['trigger · step', 'What starts a workflow · one thing it does.', 'Sentence case labels from the picker: “Incident triggered”, “Post a message”.'],
  ['run', 'One execution of a workflow or a bot task.', 'id in mono: r-7f2c. Never in a heading.'],
  ['approval', 'A proposed action waiting for a person to approve it with a passkey.', 'Titled by the action in plain words, never by its hash.'],
  ['case', 'A bot’s investigation of one problem, with its reasoning.', '“case c29”, linked wherever an approval came from one.'],
  ['folder', 'Organisation only. It changes nothing, except logins bound to it.', 'lowercase paths: work/billing.'],
  ['pack · plugin', 'An installable bundle of workflows and bots · new steps and connectors.', 'Named by publisher/name: acme/oncall.'],
  ['connector · host', 'A connection to a service (Slack, GitHub) · a machine conductor runs work on.', 'The service’s own name and casing: GitHub, PagerDuty.'],
  ['login · secret', 'A website sign-in from your vault · an encrypted value conductor stores.', 'secret names are paths: slack/bot-token.'],
  ['conductor', 'The product.', 'Always lowercase, even at the start of a sentence (rewrite the sentence to avoid it there).'],
];
const microPatterns = () => `
  <div><h4 class="cap">Approval</h4><div class="approval"><div class="hd"><div class="k" data-s="accent" style="color:var(--color-text-accent)">${sg('needs-you')}Needs you · platform-sre · 7 minutes ago</div><h5>Replace the production web server</h5></div>
    <dl><dt>Why</dt><dd>Rolling out a new image in response to SEV2 “api p99 latency” (case c29).</dd><dt>Impact</dt><dd>Production · replaces 1 server · about 2 minutes of drain.</dd><dt>Undo</dt><dd>Re-apply the previous image. conductor keeps the old plan for 7 days.</dd></dl>
    <div class="det">${lu('chevron-right')}Technical details: plan, hashes and the exact command</div>
    <div class="ft"><button class="c-btn primary" tabindex="-1">${lu('fingerprint')}Approve and run</button><button class="c-btn secondary" tabindex="-1">Deny…</button><span class="hint">${lu('lock')}Signed with your passkey</span></div></div></div>
  <div><h4 class="cap">Error</h4><div class="c-banner" data-s="danger">${sg('danger')}<div><b>Couldn’t post to #incidents</b><p>Slack says the bot isn’t in that channel. Invite @conductor to #incidents, then retry. <span class="c-mono">channel_not_found</span></p></div><button class="c-btn secondary sm" tabindex="-1">Retry</button></div>
    <p class="c-help" style="margin-top:8px">What happened, why, and what to do, in that order. The raw error code goes last, in mono.</p>
    <h4 class="cap">Validation</h4><div class="c-error">${sg('danger')}Pick a channel to post to.</div><p class="c-help">Not “options.channel is required by slack-ops.post”.</p>
    <h4 class="cap">Toast</h4><div class="c-toast" data-s="success">${sg('success')}<div><b>Approved</b><p>conductor is applying the plan.</p></div><div class="act"><button class="c-btn ghost sm" tabindex="-1">View run</button></div></div></div>
  <div><h4 class="cap">Empty state</h4><div class="panel surface" style="padding:0"><div class="c-empty" style="padding:var(--space-6)"><h4>Nothing needs you</h4><p>When a bot asks for approval or a run fails, it shows up here.</p></div></div></div>
  <div><h4 class="cap">Confirmation that widens access</h4><div class="c-modal" style="width:100%"><div class="hd"><h3>Give homelab your NAS login?</h3></div><div class="bd">homelab will be able to sign in to <b>nas.home.example</b> as you, whenever its tasks need to. You can revoke this at any time under Logins.
    <label style="display:flex;gap:8px;margin-top:12px;align-items:flex-start;color:var(--color-text-primary)"><span class="check" style="width:16px;height:16px;border:1.5px solid var(--color-border-control);border-radius:3px;flex:none;margin-top:2px"></span>I understand homelab can sign in as me.</label></div><div class="ft"><button class="c-btn ghost" tabindex="-1">Cancel</button><button class="c-btn primary" tabindex="-1">${lu('fingerprint')}Grant access</button></div></div></div>`;
ch('voice', 'Voice and tone', 'Plain, specific and calm. The interface talks to someone nervous about giving an AI access to production, so it earns trust by being exact.',
  [['voice-principles', 'Principles'], ['naming', 'Names'], ['microcopy', 'Patterns']],
  () => `
  ${sub('voice-principles', 'Principles', 'The newcomer audit could not answer “what can my bots do without asking me?”, even though the safety machinery was excellent, because it was described in the builder’s vocabulary. These rules close that gap.')}
  <div class="voice-pairs">${VOICE.map(([t, s, good, bad]) => `<div class="vp"><b>${t}<small>${s}</small></b><div class="dd do">${verdict(true)}<p>${good}</p></div><div class="dd dont">${verdict(false)}<p>${bad}</p></div></div>`).join('')}</div>
  ${sub('naming', 'Names', 'Each concept has one name, used the same way everywhere: UI, docs, CLI output and the README.')}
  <table class="names"><thead><tr><th>Term</th><th>Means</th><th>Convention</th></tr></thead><tbody>${NAMES.map((r) => `<tr>${r.map((c) => `<td>${c}</td>`).join('')}</tr>`).join('')}</tbody></table>
  ${rules(['<b>Sentence case</b> for every title, button and label.', '<b>Numbers:</b> digits for counts and units (“3 bots”, “2 minutes”), with currency to the cent in tables and rounded in prose.', '<b>“You”</b> is the operator. conductor refers to itself as “conductor”, never “we”.', '<b>Risk words</b> are fixed: “Does on its own”, “Asks you first” and “Never”, with the tier name second.'])}
  ${sub('microcopy', 'Patterns')}
  ${pair(() => `<div class="micro">${microPatterns()}</div>`, 'stack1')}
  `);

// =====================================================================
// 9. Logo
// =====================================================================
const A = 'assets/';
// An image that follows this page's theme: light file by default, dark file on dark.
const th = (light, dark, attrs = '') => `<img src="${light}" data-l="${light}" data-d="${dark}" alt="" ${attrs}>`;
ch('logo', 'Logo', 'Fermata is the working mark: an arch over a point, the conductor’s hold. These rules apply whichever direction ships.',
  [['lockups', 'Lockups'], ['logo-space', 'Clear space and size'], ['misuse', 'Misuse'], ['cobrand', 'Co-branding'], ['logo-files', 'Favicon and social'], ['logo-directions', 'Per direction']],
  () => `
  ${sub('lockups', 'Lockups', 'Use the horizontal lockup by default. Use the stacked one for square spaces, and the mark alone where the name is already present (the app icon, the favicon, the dashboard on a phone).')}
  <div class="logo-tiles">
    <div class="ltile" style="background:#efeadf;color:#1b1a15"><img src="${A}logo-horizontal.svg" alt=""><span>horizontal · light</span></div>
    <div class="ltile" style="background:#0f100d;color:#ece8dc"><img src="${A}logo-horizontal-dark.svg" alt=""><span>horizontal · dark</span></div>
    <div class="ltile" style="background:#f9f6ef;color:#1b1a15"><img src="${A}logo-stacked.svg" alt="" style="max-height:62%"><span>stacked</span></div>
    <div class="ltile" style="background:#ffffff;color:#000"><img src="${A}logo-mark-mono.svg" alt="" style="height:64px"><span>mono · black</span></div>
    <div class="ltile" style="background:#ff6a2b;color:#1d0c03"><div style="width:120px;height:72px;background:#fff;-webkit-mask:url(${A}logo-horizontal-mono.svg) center/contain no-repeat;mask:url(${A}logo-horizontal-mono.svg) center/contain no-repeat"></div><span>one colour on ember</span></div>
    <div class="ltile" style="background:#181914;color:#ece8dc"><img src="${A}app-icon.svg" alt="" style="height:96px"><span>app icon</span></div>
  </div>
  ${sub('logo-space', 'Clear space and size', 'Keep one dot diameter clear on every side (the dashed line). Below 24px, use the 16px cut (favicon.svg), not a scaled master.')}
  <div style="display:flex;gap:var(--space-8);flex-wrap:wrap;align-items:center">
    <div class="clear">${th(A + 'logo-horizontal.svg', A + 'logo-horizontal-dark.svg')}</div>
    <div class="mins">
      <div><img src="${A}favicon.svg" width="16" height="16" alt="">mark 16px (16px cut)</div>
      <div>${th(A + 'logo-mark.svg', A + 'logo-mark-dark.svg', 'width="24" height="24"')}mark 24px (master)</div>
      <div>${th(A + 'logo-horizontal.svg', A + 'logo-horizontal-dark.svg', 'style="height:18px"')}horizontal 18px tall</div>
      <div>${th(A + 'logo-stacked.svg', A + 'logo-stacked-dark.svg', 'style="width:64px"')}stacked 64px wide</div>
    </div>
  </div>
  ${sub('misuse', 'Misuse')}
  <div class="logo-tiles">
    ${[['transform:scaleX(1.5)', 'stretched'], ['filter:hue-rotate(180deg)', 'recoloured'], ['transform:rotate(180deg)', 'rotated (it becomes a smile)'], ['filter:drop-shadow(0 6px 8px rgba(0,0,0,.5))', 'effects and shadows'], ['', 'on a busy image', 'background:repeating-linear-gradient(45deg,#cc4a0e 0 12px,#3f9a86 12px 24px)'], ['letter-spacing:.4em;font:italic 34px var(--font-family-serif)', 're-typed wordmark', '', 'conductor']]
      .map(([st, l, bg = '', txt = '']) => `<div class="ltile bad" style="background:${bg || '#efeadf'};color:#1b1a15">${txt ? `<span style="position:static;opacity:1;${st};text-transform:none">${txt}</span>` : `<img src="${A}logo-horizontal.svg" alt="" style="${st}">`}<span>${l}</span></div>`).join('')}
  </div>
  ${sub('cobrand', 'Co-branding', 'Partner logos sit to the right at equal optical height, separated by a hairline rule with two dot-widths of space on each side. conductor always comes first on our surfaces, and we never merge the marks.')}
  <div class="cobrand">${th(A + 'logo-horizontal.svg', A + 'logo-horizontal-dark.svg', 'style="height:40px"')}<span class="sep"></span><span class="partner"><i></i>Acme</span></div>
  ${sub('logo-files', 'Favicon and social', 'The favicon is the 16px cut. Its SVG follows the browser’s colour scheme, and the PNG fallbacks come in light and dark. The social card is 1280×640, dark, with the lockup, the one-line description and the repository URL. Its safe area keeps 64px clear on every side.')}
  <div style="display:grid;grid-template-columns:auto 1fr;gap:var(--space-6);align-items:center">
    <div style="display:flex;gap:var(--space-4);align-items:flex-end">${[16, 32, 48].map((s) => `<img src="${A}favicon-${s}.png" width="${s}" height="${s}" alt="" style="image-rendering:pixelated">`).join('')}<img src="${A}apple-touch-icon.png" width="60" height="60" alt="" style="border-radius:13px"></div>
    <img src="${A}social-card.png" alt="Social card" style="width:100%;max-width:640px;border-radius:var(--radius-card);box-shadow:var(--shadow-2)">
  </div>
  ${sub('logo-directions', 'What changes per direction', 'Tokens never name the logo. Switching direction swaps the logo files and, where a direction has its own accent, adds a ramp and re-points nine accent tokens. Every other token stays.')}
  <table class="perdir"><thead><tr><th>Direction</th><th>Logo files</th><th>Accent</th><th>Token changes</th><th>Wordmark</th></tr></thead><tbody>
    <tr class="cur"><td>${th('directions/fermata/logo-horizontal-light.svg', 'directions/fermata/logo-horizontal-dark.svg')}</td><td><code>directions/fermata/</code> (= <code>assets/</code>)</td><td><span class="chip"><i style="background:#ff6a2b"></i>ember</span></td><td>None: this system is built on it.</td><td>Instrument Serif Italic (the dashboard’s title face)</td></tr>
    <tr><td>${th('directions/conduit/logo-horizontal-light.svg', 'directions/conduit/logo-horizontal-dark.svg')}</td><td><code>directions/conduit/</code></td><td><span class="chip"><i style="background:#e07a3f"></i>copper</span></td><td>Add a <code>copper</code> ramp (hue ≈ 50, lower chroma) and re-point the accent set below. Re-run the contrast check.</td><td>Instrument Sans SemiBold, in the logo only. UI type is unchanged.</td></tr>
    <tr><td>${th('directions/downbeat/logo-horizontal-light.svg', 'directions/downbeat/logo-horizontal-dark.svg')}</td><td><code>directions/downbeat/</code></td><td><span class="chip"><i style="background:#ff5533"></i>vermilion</span></td><td>Add a <code>vermilion</code> ramp (hue ≈ 32). It sits closer to danger red, so check that the needs-you and failed badges still differ by more than colour (they do: diamond and cross).</td><td>Martian Mono Medium</td></tr>
    <tr><td>${th('directions/signature/logo-horizontal-light.svg', 'directions/signature/logo-horizontal-dark.svg')}</td><td><code>directions/signature/</code></td><td><span class="chip"><i style="background:#e0b25a"></i>brass</span></td><td>Brass collides with <b>status.warning</b> (amber, hue 80). Either keep ember as the UI accent and use brass in the logo only, or move warning to a more orange amber.</td><td>Instrument Serif roman</td></tr>
  </tbody></table>
  ${why('<b>The accent set</b> (re-point these, nothing else): <code>accent.default · hover · active · subtle</code>, <code>text.accent</code>, <code>text.on-accent</code>, <code>focus.ring</code>, <code>selection.bg · text</code>, <code>dataviz.now</code> and <code>code.anchor</code>.')}
  `);

// =====================================================================
// 10. Tokens
// =====================================================================
ch('tokens', 'Tokens', 'There is one file of truth and generated CSS. Raw values feed semantic roles, and semantic roles feed components.',
  [['token-files', 'Files'], ['token-use', 'Using them'], ['migration', 'Migrating the dashboard']],
  () => `
  ${sub('token-files', 'Files')}
  <div class="files-grid">
    <a class="file" href="tokens/tokens.json"><b>tokens/tokens.json</b><span>The source of truth, in W3C Design Tokens format (2025.10): primitive → semantic → component. Modes sit in <code>$extensions.mode</code>.</span></a>
    <a class="file" href="tokens/tokens.css"><b>tokens/tokens.css</b><span>Generated custom properties: prefers-color-scheme, plus [data-theme], [data-density] and [data-motion] overrides.</span></a>
    <a class="file" href="tokens/prototype-compat.css"><b>tokens/prototype-compat.css</b><span>Maps the prototype’s variable names onto tokens, so migration can go one selector at a time.</span></a>
    <a class="file" href="tokens/CONTRAST.md"><b>tokens/CONTRAST.md</b><span>Every declared text, background and mark pair, checked in both themes.</span></a>
    <a class="file" href="tokens/dataviz-validation.txt"><b>tokens/dataviz-validation.txt</b><span>Validator output for the categorical, sequential and ordinal palettes.</span></a>
    <a class="file" href="fonts/fonts.css"><b>fonts/fonts.css</b><span>Self-hosted @font-face rules and the loading strategy.</span></a>
  </div>
  ${sub('token-use', 'Using them', 'Run <code>node tools/tokens.mjs</code> after editing tokens.json. It regenerates the CSS and fails if any declared pair drops below its contrast minimum.')}
  <div class="c-code"><div class="hd">${lu('scroll-text')}any page</div><pre>${[
    '<span class="p">&lt;</span><span class="k">link</span> rel<span class="p">=</span><span class="s">"stylesheet"</span> href<span class="p">=</span><span class="s">"fonts/fonts.css"</span><span class="p">&gt;</span>',
    '<span class="p">&lt;</span><span class="k">link</span> rel<span class="p">=</span><span class="s">"stylesheet"</span> href<span class="p">=</span><span class="s">"tokens/tokens.css"</span><span class="p">&gt;</span>',
    '<span class="p">&lt;</span><span class="k">html</span> data-theme<span class="p">=</span><span class="s">"dark"</span> data-density<span class="p">=</span><span class="s">"compact"</span><span class="p">&gt;</span>   <span class="c">&lt;!-- both optional --&gt;</span>',
    '',
    '<span class="k">.card</span> <span class="p">{</span>',
    '  <span class="k">background</span><span class="p">:</span> var(--color-bg-surface)<span class="p">;</span>',
    '  <span class="k">border</span><span class="p">:</span> var(--border-width) solid var(--color-border-default)<span class="p">;</span>',
    '  <span class="k">border-radius</span><span class="p">:</span> var(--radius-card)<span class="p">;</span>',
    '  <span class="k">padding</span><span class="p">:</span> var(--pad-card)<span class="p">;</span>',
    '  <span class="k">box-shadow</span><span class="p">:</span> var(--shadow-1)<span class="p">;</span>',
    '<span class="p">}</span>',
    '<span class="k">h1</span> <span class="p">{</span> <span class="k">font</span><span class="p">:</span> var(--type-h1)<span class="p">;</span> <span class="k">letter-spacing</span><span class="p">:</span> var(--type-h1-tracking)<span class="p">;</span> <span class="p">}</span>',
  ].map((l, i) => `<div class="ln"><span class="n">${i + 1}</span><span class="m"></span><span>${l}</span></div>`).join('')}</pre></div>
  ${rules(['<b>Names:</b> <code>--palette-*</code> are raw (never use them in components). <code>--color-*</code>, <code>--shadow-*</code>, <code>--type-*</code>, <code>--size-*</code>, <code>--pad-*</code>, <code>--gap-*</code>, <code>--radius-*</code> and <code>--motion-*</code> are semantic. <code>--button-*</code>, <code>--input-*</code> and similar are component tokens.', '<b>Scoping:</b> <code>data-theme</code> and <code>data-density</code> work on any element, not just <code>&lt;html&gt;</code>. This page uses that to show both themes side by side.', '<b>Adding a token:</b> add it to tokens.json at the lowest tier that makes sense, declare its contrast intent if it colours text or marks, and re-run the generator.'])}
  ${sub('migration', 'Migrating the dashboard', 'Load tokens.css and prototype-compat.css, and delete the theme blocks in css/app.css. The prototype renders the same way, with the AA fixes applied. Then replace old names with these, selector by selector.')}
  <div class="ctable-wrap" style="max-height:440px"><table class="mig"><thead><tr><th>Prototype</th><th>Token</th><th>Note</th></tr></thead><tbody>${MIG.map((m) => `<tr><td><code>${m.from}</code></td><td><code>${m.to}</code></td><td>${m.note}</td></tr>`).join('')}</tbody></table></div>
  `);

// ---------- mount ----------
const root = document.getElementById('chapters');
root.innerHTML = chapters.map((c, i) => `<section class="ch" id="${c.id}"><div class="ch-head"><div><span class="num">${String(i + 1).padStart(2, '0')} · ${c.title}</span><h2>${c.title}</h2></div><p>${c.blurb}</p></div>${c.body()}</section>`).join('');
document.getElementById('toc').innerHTML = `<ol><li><a href="#principles"><span class="n">00</span>Principles</a></li>${chapters.map((c, i) => `<li><a href="#${c.id}"><span class="n">${String(i + 1).padStart(2, '0')}</span>${c.title}</a><ol>${c.subs.map(([id, t]) => `<li><a href="#${id}">${t}</a></li>`).join('')}</ol></li>`).join('')}</ol>`;

// theme-dependent logo images (in this page's own theme)
function syncLogos() {
  const dark = getComputedStyle(document.documentElement).colorScheme.includes('dark');
  for (const img of document.querySelectorAll('img[data-l]')) img.src = dark ? img.dataset.d : img.dataset.l;
}

// controls
function setCtl(k, v) {
  if (v) document.documentElement.dataset[k] = v; else delete document.documentElement.dataset[k];
  v ? localStorage.setItem('cds-' + k, v) : localStorage.removeItem('cds-' + k);
  for (const s of document.querySelectorAll(`[data-ctl="${k}"] span`)) s.classList.toggle('is-active', (s.dataset.v || '') === (v || ''));
  syncLogos(); showDurations();
}
for (const seg of document.querySelectorAll('[data-ctl]')) {
  const k = seg.dataset.ctl;
  seg.addEventListener('click', (e) => { const s = e.target.closest('span[data-v]'); if (s) setCtl(k, s.dataset.v); });
  const cur = document.documentElement.dataset[k] || '';
  for (const s of seg.querySelectorAll('span')) s.classList.toggle('is-active', (s.dataset.v || '') === cur || (!cur && k === 'density' && s.dataset.v === 'comfortable'));
}
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', syncLogos);
function showDurations() {
  for (const [k] of DURS) { const el = document.getElementById('d-' + k); if (el) el.textContent = getComputedStyle(document.documentElement).getPropertyValue('--motion-duration-' + k).trim(); }
}
document.getElementById('play').addEventListener('click', () => {
  const box = document.getElementById('book');
  box.classList.remove('motion-play'); void box.offsetWidth;
  requestAnimationFrame(() => box.classList.add('motion-play'));
  setTimeout(() => box.classList.remove('motion-play'), 2200);
});
document.getElementById('cq').addEventListener('input', fillContrast);
document.getElementById('ct').addEventListener('click', (e) => { const s = e.target.closest('span[data-v]'); if (!s) return; for (const x of document.querySelectorAll('#ct span')) x.classList.toggle('is-active', x === s); fillContrast(); });
fillContrast(); syncLogos(); showDurations();

// scroll-spy
const links = [...document.querySelectorAll('#toc > ol > li > a')];
const io = new IntersectionObserver((es) => { for (const e of es) if (e.isIntersecting) for (const a of links) a.classList.toggle('on', a.getAttribute('href') === '#' + e.target.id); }, { rootMargin: '-30% 0px -65% 0px' });
for (const s of document.querySelectorAll('section.ch, #principles')) io.observe(s);
if (location.hash) document.querySelector(location.hash)?.scrollIntoView();
