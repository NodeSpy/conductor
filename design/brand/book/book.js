// Renders the brand book from tokens/tokens.json, tokens/contrast.json and tokens/migration.json.
// Everything visual comes from tokens.css; this file only lays content out.
import { LUCIDE, STATUS } from '../icons/icons.js';
import { LOGOS, OFFICIAL_LOGO, kitDir } from '../tools/logos.mjs';
import { STATES as STATUS_STATES, CHANGE, GLYPH_STATE, mark, injectMarkCSS } from '../status/marks.js';
import { summaryLine, quietState, exception as calmException, excRows, calmLanes, injectCalmCSS } from '../status/calm.js';
import { risk as scanRisk, env as scanEnv, rows as scanRows, injectScanCSS } from '../scannable/scan.js';
import * as SCAN from '../scannable/data.js';
injectMarkCSS(); injectCalmCSS(); injectScanCSS();

const [T, CON, MIG, CALMCHK] = await Promise.all(['tokens/tokens.json', 'tokens/contrast.json', 'tokens/migration.json', 'tokens/calm-check.json'].map((u) => fetch(u).then((r) => r.json())));

// ---------- helpers ----------
const lu = (n, cls = '') => `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE[n] ?? ''}</svg>`;
const sgLegacy = (n, cls = '') => `<svg class="${cls}" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${STATUS[n].svg}</svg>`;
// Status glyphs come from the one vocabulary (status/marks.js); only the news “info” icon is drawn here.
const sg = (n, cls = '') => (GLYPH_STATE[n] ? mark(GLYPH_STATE[n], { size: 16 }) : sgLegacy(n, cls));
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
// Calm by default (exception-first information design)
// =====================================================================
// Scannable by default (a correction to Calm by default, which hid too much). Proposal under review.
const CALM_RULES = [
  ['Identify before reading', 'Every item shows what it is (an icon and a word), how risky it is (a marker on a fixed ladder), where it happens (an environment tag) and how it went (the status mark), in the same place on every screen.'],
  ['Keep the information', 'Nothing a person uses to understand or decide is hidden. Only ids, hashes and raw payloads go behind a disclosure or a hover.'],
  ['Order by attention', 'Needs you, then failing, then running, then finished: by position, weight and the status marks. The page heading still answers “what needs me?”.'],
  ['Danger is unmistakable, routine is quiet', 'Reads and reversible changes are quiet ink; production, money and destructive or access changes are solid bands. A production Terraform apply never looks like a bot posting a comment.'],
  ['One meaning per icon, always with a word', 'An icon stands for one kind of thing or action, everywhere, and is never decorative or alone.'],
  ['Dense, but structured', 'One row anatomy with fixed, aligned columns: what · title · where · risk · status · when · action.'],
];
const CALM_SCREENS = [['overview', 'Overview'], ['quiet', 'Overview, quiet day'], ['history', 'Run history'], ['activity', 'Lanes (Activity)'], ['bot', 'Bot page'], ['approval', 'Approval header']];
const CALM_CHECK_NAME = { overview: 'overview', quiet: 'quiet overview', history: 'run history', activity: 'lanes (activity)', bot: 'bot page', approval: 'approval header' };
const calmResult = (screen, version, vp, theme) => CALMCHK.find((r) => r.screen === CALM_CHECK_NAME[screen] && r.version === version && r.theme === theme && r.engine === (vp === 'phone' ? 'webkit-iphone' : 'chromium-desktop'));
const calmChip = (r) => (r ? `<span class="calm-chip ${r.pass ? 'ok' : 'loud'}">${r.coloured} coloured · ${r.heavy} text-heavy · ${r.pass ? 'within budget' : 'too loud'}</span>` : '');
const calmPair = (screen, vp = 'desktop', theme = 'light') => {
  const shot = (v) => `v2/shots/calm/${screen}-${v}-${vp}-${theme}.png`;
  const live = (v) => `v2/mock.html?d=daylight&s=${screen === 'quiet' ? 'overview' : screen}&t=${theme}${screen === 'quiet' ? '&day=quiet' : ''}${v === 'before' ? '&v=before' : ''}`;
  const fig = (v, label) => `<figure><figcaption><b>${label}</b>${calmChip(calmResult(screen, v, vp, theme))}<a href="${live(v)}">live</a></figcaption><img loading="lazy" src="${shot(v)}" alt="${label}: ${screen}, ${vp}, ${theme}"></figure>`;
  return `${screen === 'quiet' ? `<figure class="calm-none"><figcaption><b>Before</b></figcaption><p>The busy overview had no quiet state: on a calm day it still showed every card.</p></figure>` : fig('before', 'Before')}${fig('calm', 'Calm')}`;
};
const BOOK_LANES = [['pr-review', [[2, 'running'], [31, 'passed'], [95, 'needs'], [252, 'passed'], [503, 'passed'], [640, 'passed']]], ['ci-fixer', [[6, 'running'], [52, 'failed'], [230, 'parked'], [588, 'passed']]], ['issue-triage', [[79, 'passed'], [299, 'passed'], [410, 'passed']]], ['platform-sre', [[7, 'needs'], [176, 'passed'], [716, 'passed']]], ['dep-bump', [[131, 'passed']]], ['release-notes', [[418, 'failed']]]];
const calmLanesDemo = () => `<div class="calm-lanes-wrap">${calmLanes(BOOK_LANES, { width: 860 })}</div>`;
const CALM_TOKENS = ['semantic.color.emphasis', 'semantic.color.band', 'semantic.calm', 'component.summary-line', 'component.density-band', 'component.quiet-state', 'component.exception'];
const fmtTok = (v) => (v == null ? '' : v.hex ? hexOf(v) : v.value != null ? `${v.value}${v.unit ?? ''}` : typeof v === 'object' ? JSON.stringify(v) : String(v));
const calmTokenRows = () => [...flat.keys()].filter((p) => CALM_TOKENS.some((g) => p.startsWith(g + '.'))).map((p) => { const t = flat.get(p); const raw = t.modes?.dark ?? t.modes?.light ?? t.value; return `<code>${cssName(p)}</code><span>${isAlias(raw) ? `<code>${raw.slice(1, -1).replace(/^semantic\./, '').replace(/^primitive\.color\./, 'palette.')}</code>` : esc(fmtTok(raw))}</span><span class="c-help">${t.desc ?? ''}</span>`; }).join('');
const EMPH = [['loud', 'Replace the production web server', 'The one thing that needs you. The only level allowed colour (its mark, its edge, its action).'], ['normal', '18 of 20 runs went fine', 'What things are: summary-line leads, row titles.'], ['quiet', 'last 12 hours · $8.40 spent', 'Supporting facts and reasons.'], ['ghost', '7 min ago · Show', 'Times, disclosures and the band’s axis.']];
const emphasisSheet = () => `<div class="calm-emph">${EMPH.map(([k, sample, use]) => `<div class="em-${k}"><code>--color-emphasis-${k}</code><span class="em-sample" style="color:var(--color-emphasis-${k})">${k === 'loud' ? mark('needs', { size: 14 }) : ''}${sample}</span><span class="c-help">${use}</span></div>`).join('')}</div>`;

ch('scannable', 'Scannable by default', 'Identify before reading. Every item says what it is, how risky it is, where it happens and how it went, in fixed places, so you can tell at a glance what needs you and whether it is dangerous or routine. A correction to “Calm by default”, which hid too much. Proposal under review.',
  [['calm-rules', 'Principles'], ['scan-proposal', 'The proposal'], ['calm-pairs', 'For the record: the calm round'], ['calm-glance', 'The glance answer and the loudness check'], ['calm-emphasis', 'Emphasis levels'], ['calm-components', 'Components kept from the calm round'], ['calm-tokens', 'Tokens']],
  () => `
  ${sub('calm-rules', 'Principles', 'The first review of the Daylight mocks found them busy: “there is no way for me to just pick out what’s going on… I have to read everything all the time.” The calm round answered by showing less, and over-corrected. The real problem was identification: every item looked the same, so each had to be read to learn what it was and whether it mattered, and icons were missing or didn’t make sense. The fix keeps the information and adds a visual language that encodes meaning.')}
  <ol class="calm-rules">${CALM_RULES.map(([t, d]) => `<li><b>${t}</b><span>${d}</span></li>`).join('')}</ol>
  ${sub('scan-proposal', 'The proposal', 'Four parts, specified on <a href="scannable-review.html">the scannable review page</a> with the full icon set, the risk ladder mapped to the autonomy tiers, the icon audit of the prototype and before / calm / proposed on five screens. Not yet adopted into the tokens or the mocks.')}
  ${stack(() => `<div class="scan-demo"><div class="sd-ladder">${[1, 2, 3, 4, 5, 6].map((l) => scanRisk(l)).join('')}</div><div class="sd-env">${['production', 'staging', 'local'].map((e) => scanEnv(e)).join('')}</div>${scanRows([['needs', [SCAN.NEEDS[0], SCAN.NEEDS[3]]], ['running', [SCAN.RUNNING[2]]]])}</div>`)}
  ${why('The top three steps of the risk ladder are the only solid bands on a row; the bottom three are ink. Environments differ by fill, never by colour alone. Icons are Lucide (ISC), one meaning each, always with a word.')}
  ${sub('calm-pairs', 'For the record: the calm round', 'The busy version against the calm one, in the first viewport. The calm versions pass a loudness budget, but they hide what a person needs to decide; they are kept here as the over-correction the proposal replaces.')}
  <div class="calm-ctl"><div class="c-seg" id="calm-vp"><span data-v="desktop" class="is-active">Desktop</span><span data-v="phone">Phone</span></div><div class="c-seg" id="calm-th"><span data-v="light" class="is-active">Light</span><span data-v="dark">Dark</span></div></div>
  <div class="calm-pairs" data-vp="desktop">${CALM_SCREENS.map(([s, t]) => `<section class="calm-pair" data-screen="${s}"><h4>${t}</h4><p class="calm-answer">“${(calmResult(s, 'calm', 'desktop', 'light')?.glance ?? '').replace(/^./, (c) => c.toUpperCase())}.”</p><div class="calm-figs">${calmPair(s)}</div></section>`).join('')}</div>
  ${sub('calm-glance', 'The glance answer and the loudness check', 'Kept: the page heading is the glance answer. Retired with this correction (pending review): the loudness budget, which rewarded hiding information. For each calm screen, the one sentence a person should get at a glance, marked on the page as <code>[data-glance]</code>. <code>tools/calm-check.mjs</code> checks that the glance element is the most prominent thing on screen (largest text, first large text in reading order, at least 7:1), and counts loudness in the first viewport: things in a saturated colour (OKLCH chroma ≥ 0.05, once per row or item) and text-heavy rows (over 110 characters or 3+ lines). With 3 or fewer exceptions the budget is <b>coloured ≤ exceptions + 2</b> and <b>text-heavy ≤ 2</b>; with more, 5 and 3. Calm pages must pass; the busy versions are measured against the same budget.')}
  <div class="c-table-wrap"><table class="c-table calm-table"><thead><tr><th>Screen</th><th>Glance answer</th><th class="num">Needs a person</th><th class="num">Before</th><th class="num">Calm</th><th>Result</th></tr></thead><tbody>
  ${CALM_SCREENS.map(([s, t]) => { const n = (r) => (r ? `${r.coloured} · ${r.heavy}` : '—'); const c = calmResult(s, 'calm', 'desktop', 'light'); const all = CALMCHK.filter((r) => r.screen === CALM_CHECK_NAME[s] && r.version === 'calm'); return `<tr><td><b>${t}</b></td><td>${c?.glance ?? ''}</td><td class="num">${c?.exceptions ?? ''}</td><td class="num">${s === 'quiet' ? '—' : `${n(calmResult(s, 'before', 'desktop', 'light'))} / ${n(calmResult(s, 'before', 'phone', 'light'))}`}</td><td class="num">${n(c)} / ${n(calmResult(s, 'calm', 'phone', 'light'))}</td><td>${all.length && all.every((r) => r.pass && r.glanceOk) ? `${mark('passed')} ${all.length} renders pass` : `${mark('failed')} fails`}</td></tr>`; }).join('')}
  </tbody></table></div>
  ${why('Counts are “coloured · text-heavy”, desktop (Chromium) / iPhone (WebKit), light theme; dark is the same. The full matrix (both engines, both themes, before and calm) is generated into <a href="tokens/CALM-CHECK.md">tokens/CALM-CHECK.md</a>.')}
  ${sub('calm-emphasis', 'Emphasis levels', 'Four levels, and only the loudest may carry colour. At most three loud things per screen (<code>--calm-max-loud</code>).')}
  ${stack(emphasisSheet)}
  ${sub('calm-components', 'Components kept from the calm round', 'Still useful, for what is genuinely secondary: the summary line for long finished lists, the quiet state for days when nothing needs anyone, and the exception card for an approval page header. They live in <code>status/calm.js</code>. The density band stays for long timelines, under the rows, never instead of them.')}
  <h4 class="cap">One loud thing (exception)</h4><p class="lede">The thing that needs you most: a kicker with its mark, a serif title, one sentence, one action. The next few are exception rows: one line each, a 3px edge in the mark’s colour, one action. Anything beyond that folds into “N more need you”.</p>
  ${stack(() => `<div class="calm-demo">${calmException({ state: 'needs', kicker: 'Needs your approval · 7 min ago', title: 'Replace the production web server', sentence: 'platform-sre wants to run terraform apply on production to fix the SEV2 latency incident.', action: '<button class="c-btn primary" tabindex="-1">Review</button>' })}<div style="margin-top:24px">${excRows([{ state: 'failed', title: 'ci-fixer couldn’t fix acme/web#1288', sentence: '2 tests fail; nothing was pushed', age: '52 min ago', action: '<button class="c-btn secondary sm" tabindex="-1">Open</button>' }])}${summaryLine({ lead: '2 more need you', parts: ['pr-review needs your call on a draft', 'approve a sign-in on app.example.com'], label: 'Show' })}</div></div>`)}
  <h4 class="cap">Summary line</h4><p class="lede">Normal work as one quiet line: a count in ink, the parts in secondary, and “Show” in ghost. It expands in place (a native <code>&lt;details&gt;</code>), so the detail is one tap away and never in the way.</p>
  ${stack(() => `<div class="calm-demo">${summaryLine({ lead: '10 finished', parts: ['9 passed', '1 parked'], label: 'Show' })}${summaryLine({ lead: '3 running', parts: ['ci-fixer, pr-review, platform-sre'], label: 'Show' })}${summaryLine({ lead: 'All healthy', parts: ['ops-box', '6 connections', 'up 3 days'], label: 'Show' })}</div>`)}
  <h4 class="cap">Density band</h4><p class="lede">A timeline where passing and parked runs are faint ticks on a track, so twenty normal runs read as texture rather than twenty marks. Only failures, things that need you and running work are drawn as marks, and each lane says in words what they are, so there is no legend. Lanes with exceptions come first; lanes where everything went fine fold into one row.</p>
  ${stack(calmLanesDemo)}
  <h4 class="cap">Quiet state</h4><p class="lede">When nothing needs you, the screen says so in one serif sentence and one supporting line, then the summary lines. The arch is drawn in border colours: present, never loud.</p>
  ${stack(() => `<div class="calm-demo">${quietState({ title: 'All quiet. Nothing needs you.', sub: '46 runs went fine in the last 12 hours. 2 are running now.' })}</div>`)}
  ${sub('calm-tokens', 'Tokens', 'The emphasis levels, the band colours, the loudness budget and the four components’ tokens. Values are the dark theme’s; aliases resolve per theme.')}
  <div class="dens-tbl calm-tok"><span class="h">Token</span><span class="h">Value</span><span class="h">Use</span>${calmTokenRows()}</div>
  `);

// =====================================================================
// 0. Reading at a glance (information design)
// =====================================================================
const PRINCIPLES = [
  ['Summary first, detail on demand', 'Every screen opens with one sentence answering: does anything need me, is anything broken, is everything else fine? Health checks, raw counts and internals sit one step down.',
    'up 3d 4h 12m · v0.71.2 → v0.72.0 · sandbox enforcing · paseo ok · 3 running · 6 need you',
    () => `<p class="ia-lead"><b>5 things need you.</b> Everything else is running normally: 18 of 20 runs in the last 12 hours went fine.</p>`],
  ['A plain headline, the technical detail beneath', 'The title says what will happen in words a newcomer understands. The tool, ids and diff stats follow on a quieter line, in mono, last.',
    'terraform apply · infra@3f2a9c1d / platform-sre · p12 · +1 ~2 -0 · REPLACES aws_instance.web',
    () => `<div class="pt-need" data-s="accent"><span class="ng">${sg('needs-you')}</span><div><b>Replace the production web server</b><span>platform-sre wants to run terraform apply on production, for the SEV2 latency incident</span><code>p12 · infra@3f2a9c1d · +1 ~2 −0</code></div></div>`],
  ['Three levels of type', 'What (title or bold lead-in), why it matters (regular, secondary ink), machine detail (mono, small, tertiary, last). Nothing else.',
    'ci-fixer acme/web#1288 / Failed at step test. go test ./...: 2 failures (TestCheckout, TestRefund)',
    () => `<div class="ia-levels"><b>ci-fixer couldn’t fix the failing check on acme/web#1288</b><span>Its change made 2 tests fail. It stopped before pushing anything.</span><code>r-7ef0 · step test</code></div>`],
  ['Colour only for what needs attention', 'Passing work is quiet: a small green mark and an ink word, never a green row. When eleven of sixteen rows shout “passed”, the two failures hide. A row that needs someone gets a thin leading edge, its reason in words, and a place at the top. No row is ever tinted.',
    '✓ ok · ✓ ok · × failed · ✓ ok · ◎ waiting · ✓ ok (every row coloured)',
    () => `<div class="ia-rows">${[['success', 'pr-review', 'acme/api#480', ''], ['danger', 'ci-fixer', 'acme/web#1288', '2 tests fail after its change'], ['success', 'issue-triage', 'acme/api#486', ''], ['warning', 'pr-review', 'acme/infra#77', 'Waiting for your answer']].map(([st, w, t, n]) => `<div class="ia-row ${['danger', 'warning', 'accent'].includes(st) ? 'attn' : 'quiet'}" data-s="${st}"><span class="c-status" data-s="${st}">${sg(st)}</span><span><b>${w}</b> ${t}${n ? `<em>${n}</em>` : ''}</span></div>`).join('')}</div>`],
  ['Numbers line up', 'Durations, costs and counts are right-aligned in tabular figures with units, so a column scans without reading every cell.',
    '7m · 12m · 1m · … · 9m (left-aligned, mono, no units)',
    () => `<div class="ia-nums">${[['7 min', '$0.41'], ['12 min', '$1.12'], ['1 min', '$0.06'], ['9 min', '$0.82']].map(([a, b]) => `<span>${a}</span><span>${b}</span>`).join('')}</div>`],
  ['Group and chunk', 'Things to act on are separate from things that are merely true. An update that installs itself is not “needing you”.',
    'Needs you · 6 items (2 approvals, a failure, a hand-off, a browser push, an update)',
    () => `<div class="ia-groups"><div><b>Needs you <span class="c-count">5</span></b><span>2 approvals · 1 failed fix · 2 questions</span></div><div><b>For your information <em>2</em></b><span>Update v0.72.0 installs within 10 min · Sandbox blocked pastebin.com</span></div></div>`],
  ['Lead with what changed', 'Coming back after a while, you want the difference, not the full feed. See the “since you last looked” pattern.',
    'Activity · live: r-7f2c pr-review step review started · platform-sre wrote knowledge/db-prod-1.md (commit 8e1f0aa) · …',
    () => digest(true)],
  ['Timelines tell the story', 'A run reads top to bottom: what each step did and what came of it, in sentences, with the duration on the right. Steps after a failure say they weren’t reached.',
    'logs ▬ ✓ · triage ▬ ✓ · fix ▬▬▬▬ ✓ · test ▬ × · push not reached',
    () => story(true)],
  ['Human times and schedules', 'Relative time with the exact time beside it, schedules in words, and the cron string on hover. Ids and hashes are never first on a line.',
    'schedule 0 6 * * 1 · started 10/16/2026, 5:03:45 PM · r-7ef0',
    () => `<dl class="ia-dl"><dt>Runs</dt><dd>Mondays at 06:00</dd><dt>Started</dt><dd>52 min ago <span>(17:03)</span></dd><dt>Run</dt><dd><code>r-7ef0</code></dd></dl>`],
  ['Generous, then compact on request', 'Space does the grouping, so there are fewer boxes. Compact density is the same content in the same order, only tighter (36px rows).',
    'NOW · Overview · Live 3 · Needs you 6 · WORK · Run history · … (dense mono everywhere)',
    () => `<div class="ia-dens"><div data-density="comfortable">${sampleRows()}</div><div data-density="compact">${sampleRows()}</div></div>`],
];
function sampleRows() { return [['danger', 'ci-fixer', 'acme/web#1288'], ['success', 'pr-review', 'acme/web#1291'], ['neutral', 'dep-bump', 'acme/web']].map(([st, w, t]) => `<div class="ia-row ${st === 'danger' ? 'attn' : 'quiet'}" data-s="${st}"><span class="c-status" data-s="${st}">${sg(st)}</span><span><b>${w}</b> ${t}</span></div>`).join(''); }
ch('glance', 'Reading at a glance', 'conductor produces a firehose of runs, cases, approvals, logs and diffs. These ten rules turn it into something a person can read in seconds. Every screen follows them.',
  PRINCIPLES.map(([t], i) => [`p${i + 1}`, t]),
  () => PRINCIPLES.map(([t, rule, before, after], i) => `<article class="principle" id="p${i + 1}"><div class="pr-t"><span class="pn">${String(i + 1).padStart(2, '0')}</span><div><h3>${t}</h3><p>${rule}</p></div></div>
    <div class="pr-ba"><div class="pr-before" data-contrast="exempt"><span class="cap">Before · prototype</span><code>${esc(before)}</code></div><div class="pr-after"><span class="cap">After · Daylight</span>${after()}</div></div></article>`).join(''));

// =====================================================================
// 1. Colour
// =====================================================================
const RAMPS = Object.keys(T.primitive.color).filter((k) => !k.startsWith('$'));
const rampRow = (name) => {
  const steps = Object.entries(T.primitive.color[name]);
  const short = steps.length < 11; const pad = 12 - steps.length;
  return `<div class="ramp ${short ? 'short' : ''}"><b>${name}</b>${steps.map(([s, t]) => {
    const h = t.$value.hex; const L = lumi(h); const ink = (L + 0.05) / 0.05 >= 1.05 / (L + 0.05) ? '#000000' : '#ffffff';
    return `<i style="background:${h};color:${ink}" title="--palette-${name}-${s}: ${h}"><em>${s}</em>${h.slice(1)}</i>`;
  }).join('')}${'<span></span>'.repeat(short ? 1 : Math.max(pad, 0))}</div>`;
};
const SURF = ['canvas', 'sunken', 'rail', 'surface', 'raised', 'overlay'];
const TEXTS = ['primary', 'secondary', 'tertiary', 'accent', 'disabled'];
const textMatrix = (theme) => `<div class="textscale">
  <div class="row hdr"><b>text ↓ / bg →</b>${SURF.map((s) => `<span>${s}</span>`).join('')}</div>
  ${TEXTS.map((tx) => `<div class="row"><b>text.${tx}</b>${SURF.map((s) => {
    const r = conFor(`semantic.color.text.${tx}`, `semantic.color.bg.${s}`, theme);
    return `<div class="cell" style="background:var(--color-bg-${s});color:var(--color-text-${tx})"${tx === 'disabled' ? ' data-contrast="exempt"' : ''}><span>Needs you · 6</span><small>${r ? `${r.ratio.toFixed(2)} ${r.level === 'exempt' ? 'exempt' : r.level}` : ''}</small></div>`;
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
const STAT_GLYPH = { success: 'success', warning: 'warning', danger: 'danger', info: 'shadow', running: 'running', neutral: 'neutral' };
const STAT_EX = {
  success: ['Passed', 'pr-review finished · 31 checks', '2m'], warning: ['Waiting on you', 'hand-off awaiting answer', '1h'], danger: ['Failed', 'ci-fixer failed on acme/web#1288', '52m'],
  info: ['Shadow', 'billing-sre would have posted · not sent', '4m'], running: ['Running', 'issue-triage · step classify', 'now'], neutral: ['Parked', 'dep-bump paused by you', '3d'],
};
const statusCards = (theme) => `<div class="statuses">${STAT.map((s) => `<div class="status-card" data-s="${s}">
  <div class="top-row">${sg(STAT_GLYPH[s], s === 'running' ? 'c-glyph-spin' : '')}<b>${s}</b></div>
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
    <td><span class="pv" style="background:${r.bg};color:${r.fg}"${r.min < 4.5 ? ' data-contrast="exempt" title="Pair checked at its own minimum (UI 3:1 or exempt), not as body text"' : ''}>Aa</span></td><td><code>${s(r.token)}</code></td><td><code>${s(r.on)}</code></td><td>${r.theme}</td>
    <td class="r">${r.ratio.toFixed(2)}</td><td>${r.level === 'exempt' ? '—' : r.min}</td>
    <td><span class="lvl ${r.level === 'AAA' ? 'AAA' : r.level === 'AA' ? 'AA' : r.level === 'exempt' ? 'ex' : 'ui'}">${r.pass ? '' : 'FAIL · '}${r.level}</span></td></tr>`).join('');
}

ch('colour', 'Colour', 'Warm cream and cocoa surfaces, sand neutrals, one ember accent that means “you”, and status hues kept for what needs attention.',
  [['palettes', 'Raw palettes'], ['surfaces', 'Surfaces and text'], ['semantic', 'Semantic tokens'], ['status', 'Status'], ['dataviz', 'Data visualisation'], ['contrast', 'Contrast']],
  () => `
  ${sub('palettes', 'Raw palettes', 'sand is the warm neutral (its extra 975 step is the logo ink); the hues are eleven-step OKLCH ramps (50 lightest, 950 darkest); cream and cocoa are the surface sets. Components never use these directly: semantic tokens map them to roles.')}
  <div class="ramp-head"><span></span>${['50', '100', '200', '300', '400', '500', '600', '700', '800', '900', '950', ''].map((s) => `<span>${s}</span>`).join('')}</div>
  <div class="ramps">${RAMPS.filter((r) => !['cocoa', 'cream'].includes(r)).map(rampRow).join('')}</div>
  <h4 class="cap">Surface sets: 0 sunken · 1 canvas · 2 rail · 3 surface · 4 raised · 5 overlay</h4>
  <div class="ramps">${rampRow('cream')}${rampRow('cocoa')}</div>
  ${why('<b>Why separate surface sets?</b> Surfaces need several fine steps within a narrow lightness band, closer together than a perceptual ramp’s steps. cream (light) and cocoa (dark) are hand-tuned, warm, and frozen as hex in tokens.json, so nothing drifts if a generator changes. sand carries text, borders and icons, and its deepest step (975) is the logo ink.')}

  ${sub('surfaces', 'Surfaces and text', 'Elevation is lightness: each surface sits one step above the one below. Every text role is checked on every surface it can land on, with the ratios shown below.')}
  ${pair((t) => `<div class="surfaces" style="background:var(--color-bg-canvas)">${SURF.map((s) => `<div style="background:var(--color-bg-${s})"><b>bg.${s}</b><span>${resolve('semantic.color.bg.' + s, t).hex}</span></div>`).join('')}</div>`)}
  <div style="height:12px"></div>
  ${stack((t) => textMatrix(t))}
  ${why('<b>Changed from the prototype:</b> graphite and paper became cocoa and cream, and Daylight is light-first. Tertiary text was <code>#807c6f</code> / <code>#847f71</code>, about 4.3:1 and 3.5:1 on cards; it is now <code>#ada291</code> / <code>#6e6457</code>, AA on every surface. Text never uses the light accent fill (3.8:1). It uses <code>text.accent</code> instead.')}

  ${sub('semantic', 'Semantic tokens', 'The names components use. Each resolves to a different raw value per theme.')}
  ${stack((t) => semSwatches(t))}

  ${sub('status', 'Status', 'One vocabulary of marks (<code>status/marks.js</code>), the same in tables, cards, lane charts, legends, the overview, the run page and on phones. The mark always carries its state colour, and the word is coloured only when a person is needed or the thing is live. Passed is a quiet sage dot and parked is slate pause bars, so they never share a hue or a shape. Rows are never tinted: a row that needs someone gets a thin leading edge, its reason and one action. Each state has fg (text), bg, border and solid (mark) tokens.')}
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
    <div class="sample" style="font:var(--type-${k});letter-spacing:var(--type-${k}-tracking);text-transform:var(--type-${k}-transform);font-variation-settings:var(--type-${k}-variation)">${esc(SAMPLE[k])}</div>
    <div class="meta">${x.px}px · ${v.fontSize.value}rem / ${v.lineHeight}<br>${fam} ${w}${x.fontStyle === 'italic' ? ' italic' : ''} · ${v.letterSpacing.value ? (v.letterSpacing.value > 0 ? '+' : '') + v.letterSpacing.value + 'em' : '0'}${x.textTransform === 'uppercase' ? ' · caps' : ''}</div></div>`;
};
ch('type', 'Type', 'A soft, upright serif for short human titles, a typeface built for legibility for everything people read, and mono only for literal code.',
  [['families', 'Families'], ['scale', 'Scale'], ['weights', 'Weights and figures'], ['serif-rule', 'When to use the serif']],
  () => `
  ${sub('families', 'Families', 'All three are open-licensed and self-hosted as latin subsets with <code>font-display: swap</code>. Only the sans and the mono are preloaded, because the serif appears in titles alone.')}
  <div class="families">
    <div class="fam"><div class="glyph" style="font-family:var(--font-family-serif);font-weight:600;font-variation-settings:var(--type-h1-variation)">Aa</div><h4>Fraunces</h4><dl><dt>Role</dt><dd>Page and section titles, one hero number, the wordmark</dd><dt>Style</dt><dd>600, upright, soft axis on (SOFT 100). Never italic.</dd><dt>Licence</dt><dd>SIL OFL 1.1 · Undercase Type</dd><dt>Fallback</dt><dd><code>ui-serif, Georgia, serif</code></dd><dt>File</dt><dd>variable (weight, optical size, soft), preloaded</dd></dl></div>
    <div class="fam"><div class="glyph" style="font-family:var(--font-family-sans);font-weight:600">Aa</div><h4>Atkinson Hyperlegible Next</h4><dl><dt>Role</dt><dd>Everything people read and act on</dd><dt>Weights</dt><dd>400 · 600 · 700 (variable 200–800)</dd><dt>Why</dt><dd>Designed by the Braille Institute so that 1/l/I, 0/O and rn/m stay distinct: right for ids and numbers read at a glance</dd><dt>Licence</dt><dd>SIL OFL 1.1 · Braille Institute</dd><dt>Fallback</dt><dd><code>ui-sans-serif, system-ui, Segoe UI, Roboto</code></dd></dl></div>
    <div class="fam"><div class="glyph" style="font-family:var(--font-family-mono);font-size:64px">Aa</div><h4>Atkinson Hyperlegible Mono</h4><dl><dt>Role</dt><dd>Only literal code, ids, YAML and logs, never a decorative “machine voice”</dd><dt>Weights</dt><dd>400 · 500</dd><dt>Licence</dt><dd>SIL OFL 1.1 · Braille Institute</dd><dt>Fallback</dt><dd><code>ui-monospace, SF Mono, Menlo, Consolas</code></dd><dt>File</dt><dd>loaded on demand</dd></dl></div>
  </div>
  ${sub('scale', 'Scale', '1rem = 16px. The dashboard reads at body-m (15px), and lead sentences, docs and the README read at body-l (17px). Nothing in the product is smaller than 11.5px, and mono is never smaller than 12px.')}
  <div class="specimen">${TYPES.map(typeRow).join('')}</div>
  ${why('<b>Use the shorthand, plus tracking.</b> <code>font: var(--type-h1)</code> sets style, weight, size, line height and family. CSS’s <code>font</code> shorthand can’t carry letter-spacing or text-transform, so add <code>letter-spacing: var(--type-h1-tracking)</code> (and <code>text-transform</code> for overline).')}
  ${sub('weights', 'Weights and figures')}
  <div class="weights">
    <div><b style="font-weight:400">Regular 400</b><span>Body, descriptions, table cells, code</span></div>
    <div><b style="font-weight:600">Semibold 600</b><span>Buttons, tabs, nav, form labels; Fraunces titles</span></div>
    <div><b style="font-weight:700">Bold 700</b><span>Item titles (h3/h4), the bold lead-in of a summary sentence, counts</span></div>
  </div>
  <h4 class="cap">Numbers: tabular in columns, proportional in prose</h4>
  <div class="figs">
    <div class="panel surface"><table style="font-variant-numeric:proportional-nums"><tr><td>proportional (default)</td><td>1,111.10</td></tr><tr><td></td><td>$48.07</td></tr><tr><td></td><td>9,000.00</td></tr></table><p class="c-help" style="margin-top:8px">Digits wobble: wrong for tables.</p></div>
    <div class="panel surface"><table style="font-variant-numeric:tabular-nums"><tr><td>tabular-nums</td><td>1,111.10</td></tr><tr><td></td><td>$48.07</td></tr><tr><td></td><td>9,000.00</td></tr></table><p class="c-help" style="margin-top:8px">Use <code>font-variant-numeric: tabular-nums</code> in tables, timers, counters and axes.</p></div>
  </div>
  ${why('Our Fraunces subset has no tabular figures, so <b>serif numbers are for one hero value per card</b> and never appear in a column. Atkinson Hyperlegible Next has true tabular figures: use them in every table, counter and timer.')}
  ${sub('serif-rule', 'When to use the serif', 'The audits found long italic titles hard to read. Daylight’s serif is upright and soft, and it still stays short and human.')}
  <div class="dodont">
    <div class="dd do">${verdict(true)}<div style="font:var(--type-h1);letter-spacing:var(--type-h1-tracking);font-variation-settings:var(--type-h1-variation)">Good afternoon, ops.</div><div class="note">Page titles of one to four words, section titles, empty-state headlines and a single hero number.</div></div>
    <div class="dd dont">${verdict(false)}<div style="font:var(--type-h2);letter-spacing:var(--type-h2-tracking);font-variation-settings:var(--type-h2-variation)">terraform apply of infra@3f2a9c1d07e4, dir envs/prod, workspace prod: +1 ~2 -0</div><div class="note">Generated or long titles, anything containing ids, paths or numbers, and anything under 20px.</div></div>
    <div class="dd do">${verdict(true)}<div><div style="font:var(--type-h3)">Replace the production web server</div><div class="c-mono" style="color:var(--color-text-tertiary);margin-top:4px">infra@3f2a9c1d · envs/prod · +1 ~2 −0</div></div><div class="note">Generated titles use h3 in the sans, with the machine details on a mono line below.</div></div>
    <div class="dd dont">${verdict(false)}<div><span class="c-btn primary" style="font-family:var(--font-family-serif);font-size:17px">Approve</span></div><div class="note">Never in controls, labels, tables, badges or form fields.</div></div>
  </div>
  ${rules(['<b>Serif (Fraunces, upright):</b> five words or fewer, written by a person, at 20px or larger, in a title role.', '<b>Sans (Atkinson):</b> everything people read or act on, including times, costs and counts (with tabular figures).', '<b>Mono:</b> only literal code, ids, hashes, paths and YAML, placed last on a line. Never a decorative “machine voice” for labels, strips or timestamps.', '<b>Sentence case</b> everywhere. Only the overline style is uppercase.'])}
  `);

// =====================================================================
// 3. Space & layout
// =====================================================================
const SPACES = Object.entries(T.primitive.space).filter(([k]) => !k.startsWith('$')).sort((a, b) => a[1].$value.value - b[1].$value.value);
const densityTokens = [...flat.entries()].filter(([, t]) => t.modes && 'compact' in t.modes);
// The table pattern: rows that need someone are grouped first (edge + reason + one action),
// then what is running, then what finished. Status is a mark plus a word; rows are never tinted.
const TABLE_SAMPLE = [
  ['attn', 'r-7f2b', 'ci-fixer', 'acme/web#1288', 'danger', 'danger', 'Failed', 'Failed at step 4, “Run the tests”: 2 tests fail after its change.', '6 min 40 s', '$0.31', 'Open'],
  ['attn', 'r-7f27', 'pr-review', 'acme/infra#77', 'accent', 'needs-you', 'Needs you', 'Waiting for your answer: approve, revise or discard its draft review.', '—', '$0.18', 'Answer'],
  ['live', 'r-7f29', 'issue-triage', 'acme/api#486', 'running', 'running', 'Running', '', '22 s', '$0.01'],
  ['done', 'r-7f2c', 'pr-review', 'acme/web#1291', 'success', 'success', 'Passed', '', '1 min 12 s', '$0.04'],
  ['done', 'r-7f22', 'dep-bump', 'acme/web', 'neutral', 'neutral', 'Parked', '', '—', '—'],
];
const sampleTable = (extra = '') => {
  const groups = [['attn', 'Needs attention', 'attn-h'], ['live', 'Running now', ''], ['done', 'Finished', '']];
  return `<div class="c-table-wrap"><table class="c-table"><thead><tr><th style="width:28px"></th><th>Run</th><th>Status</th><th class="num">Took</th><th class="num">Cost</th><th>ID</th><th></th></tr></thead>
  ${groups.map(([g, title, cls]) => { const rows = TABLE_SAMPLE.filter((r) => r[0] === g);
    // Calm by default: Finished is one summary line ("2 finished · 1 passed · 1 parked") that opens in place.
    const head = g === 'done'
      ? `<tr class="grp done-sum"><td colspan="7"><b>${rows.length} finished</b><span class="cs-parts">${rows.filter((r) => r[6] === 'Passed').length} passed · ${rows.filter((r) => r[6] === 'Parked').length} parked</span><span class="cs-more">Show</span></td></tr>`
      : `<tr class="grp ${cls}"><td colspan="7"><b>${title}</b><span>${rows.length}</span></td></tr>`;
    return `<tbody class="${g}">${head}${rows.map(([, id, wf, tgt, s, glyph, word, reason, d, c, act], i) => `<tr${g === 'done' ? ' hidden' : ''} class="${g === 'attn' ? 'attn' : ''} ${g === 'live' && i === 0 ? 'is-selected' : ''} ${extra && g === 'live' ? extra : ''}" data-s="${s}"><td><span class="check">${g === 'live' && i === 0 ? lu('check') : ''}</span></td><td class="run"><b>${wf}</b> <span class="sub">${tgt}</span>${reason ? `<span class="reason">${reason}</span>` : ''}</td><td><span class="c-status" data-s="${s}">${sg(glyph, s === 'running' ? 'c-glyph-spin' : '')}${word}</span></td><td class="num">${d}</td><td class="num">${c}</td><td><span class="c-mono sub">${id}</span></td><td class="act">${act ? `<button class="c-btn ${s === 'accent' ? 'primary' : 'secondary'} sm" tabindex="-1">${act}</button>` : ''}</td></tr>`).join('')}</tbody>`; }).join('')}
</table></div>`;
};
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
  ${sub('density', 'Density', 'Comfortable is the default. Compact is for operators at scale: 36px rows, tighter padding and 13.5px tables. It is a token set, not a separate design: set <code>data-density="compact"</code> on any element. Phones always use comfortable.')}
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
      '<b>Stroke:</b> Daylight draws Lucide at 1.75 (rounded, a touch lighter than its default 2) at 16–20px, and 1.5 at 24px.',
      '<b>Colour:</b> <code>icon.default</code> beside text and <code>icon.muted</code> for decoration (still 3:1). An icon takes the status colour only when it is a status glyph.',
      '<b>Never</b> show an icon alone without a label or tooltip (except a universal close ×), never fill outline icons, and never mix icon sets.',
      '<b>Drawing new icons:</b> 24 grid, 2px stroke, round caps and joins, 2px corner radius on rectangles, no fills, and the optical size of the Lucide neighbours.',
    ])}
    <div class="icon-sizes">${[14, 16, 20, 24].map((s) => `<div><span style="width:${s}px;height:${s}px;display:block;color:var(--color-icon-default)">${lu('bot').replace('<svg ', `<svg width="${s}" height="${s}" ${s === 24 ? 'stroke-width="1.5" ' : ''}`)}</span>${s}px</div>`).join('')}</div></div>
  </div>
  ${sub('icon-set', 'Set', 'This is the vocabulary the dashboard uses. Each concept gets exactly one icon.')}
  <div class="icon-grid">${Object.keys(LUCIDE).map((n) => `<div>${lu(n)}<span>${n}</span></div>`).join('')}</div>
  ${sub('glyphs', 'Status glyphs', 'Drawn on a 16-unit grid with a 1.5 stroke and round caps, so each is legible at 13px. Each state has a distinct shape, so they read in one colour and to colour-blind readers. Running is the only one that moves.')}
  ${pair(() => `<div class="glyphs" style="grid-template-columns:1fr 1fr">${Object.entries({ ...STATUS_STATES, ...CHANGE }).map(([k, g]) => `<div>${mark(k, { size: 24 })}<b>${g.word}</b><span>${g.shape} · <code>${g.mark}</code></span></div>`).join('')}</div>`)}
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
const btnSheet = () => `<div class="grid-sheet btn-sheet" style="grid-template-columns:90px repeat(6, minmax(0,1fr))">
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
const listSheet = () => sampleTable('is-focus');
const badgeSheet = () => `<div style="display:grid;gap:var(--space-4)">
  <div style="display:flex;gap:var(--space-2);flex-wrap:wrap">${STAT.map((s) => `<span class="c-badge" data-s="${s}">${sg(STAT_GLYPH[s])}${STAT_EX[s][0]}</span>`).join('')}<span class="c-badge" data-s="accent">${sg('needs-you')}Needs you</span><span class="c-badge">${lu('package')}pack</span><span class="c-badge">read</span></div>
  <div style="display:flex;gap:var(--space-2);flex-wrap:wrap;align-items:center"><span class="c-chip">${lu('folder')}All folders</span><span class="c-chip is-hover">work</span><span class="c-chip is-selected">${lu('check')}personal</span><span class="c-count">6</span>${mark("running")}<span class="c-help">live</span></div>
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
  ['tables', 'Tables and dense lists', 'Rows are grouped: <b>Needs attention</b> (a thin leading edge, the reason in words and one action), then <b>Running now</b>, then <b>Finished</b> in quiet ink. Status is a mark plus a word, and no row is ever tinted. The decision is recorded on the <a href="explore/status-column.html">status column</a> and <a href="status-review.html">labels</a> pages. Text is left-aligned and numbers are right-aligned with tabular figures. The status column always has a glyph and a word, and ids are in mono. Rows are focusable (j/k to move, x to select, Enter to open). Selection uses the accent wash and a 3px bar.', listSheet, true],
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
// Patterns (including the two borrowed from the other directions)
// =====================================================================
function digest(compact = false) {
  return `<div class="pt-digest ${compact ? 'compact' : ''}"><h4>Since you last looked <span>2 h ago</span></h4><ul>
    <li><b>14</b><span>runs passed, including 6 pull-request reviews</span></li>
    <li data-s="danger"><b>1</b><span>new failure: ci-fixer on acme/web#1288</span>${sg('danger')}</li>
    <li data-s="accent"><b>1</b><span>new approval: replace the production web server</span>${sg('needs-you')}</li>
    <li><b>1</b><span>update ready: v0.72.0</span></li></ul></div>`;
}
function story(compact = false) {
  const steps = [['success', 'Get the failing logs', '3 failing checks found', '2 s'], ['success', 'Decide what failed', 'A test failure (91% sure)', '8 s'], ['success', 'Fix it', 'Changed applyFee to round half-even', '7 min 40 s'], ['danger', 'Run the tests', '2 of 148 tests fail', '26 s'], ['skipped', 'Push the fix', 'Not reached', '']];
  return `<ol class="pt-story">${steps.slice(0, compact ? 5 : 5).map(([st, t, d, x]) => `<li data-s="${st === 'skipped' ? 'neutral' : st}" class="${st}"><span class="sg">${sg(st)}</span><div><b>${t}</b><span>${d}</span></div><em>${x}</em></li>`).join('')}</ol>`;
}

const PATTERNS = [
  ['pt-summary', 'Summary header', 'Every page heading is the answer to the page’s question, in plain words: “5 things need you”, “3 runs need attention”, “All quiet. Nothing needs you.” It is the largest text on screen, so the glance lands on it first. One supporting sentence follows; actions belong to the thing that needs you, not to the header.',
    () => `<div class="pt-head"><h1>5 things need you</h1><p>Start with the first. Everything else is running normally.</p></div>`],
  ['pt-needs', 'Needs you', 'The one loud thing first: the most urgent item as an exception card with one sentence and one action. The next few are one-line rows with a 3px edge in their mark’s colour. The rest fold into “N more need you”. Machine details (ids, hashes, diff stats) live on the item’s own page.',
    () => `${calmException({ state: 'needs', kicker: 'Needs your approval · 7 min ago', title: 'Replace the production web server', sentence: 'platform-sre wants to run terraform apply on production to fix the SEV2 latency incident.', action: '<button class="c-btn primary" tabindex="-1">Review</button>' })}<div style="margin-top:24px">${excRows([{ state: 'needs', title: 'Post the monthly forecast to #incidents', sentence: 'asked by someone who isn’t an operator', age: '22 min ago', action: '<button class="c-btn secondary sm" tabindex="-1">Review</button>' }, { state: 'failed', title: 'ci-fixer couldn’t fix acme/web#1288', sentence: '2 tests fail; nothing was pushed', age: '52 min ago', action: '<button class="c-btn secondary sm" tabindex="-1">Open</button>' }])}${summaryLine({ lead: '2 more need you', parts: ['pr-review needs your call on a draft', 'approve a sign-in on app.example.com'], label: 'Show' })}</div>`],
  ['pt-digest', 'Since you last looked (from Broadsheet)', 'Borrowed from the Broadsheet direction. When you come back, the overview leads with what changed since your last visit, as counts and one line each, linked to the detail. New failures and approvals carry their status colour; everything else stays ink.',
    () => digest()],
  ['pt-story', 'Run story', 'A run as a short story, top to bottom: each step’s outcome in a sentence, the duration on the right, and the failing step called out. Steps after a failure say “Not reached”.',
    () => story()],
  ['pt-lanes', 'Activity lanes (from Score)', 'Borrowed from the Score direction, for the Activity page, and made calm: every workflow is a lane and time runs left to right, but passing and parked runs are faint ticks in a density band. Only failures, things that need you and running work are marks, and each lane says in words what they are, so there is no legend to decode. Lanes with exceptions come first; lanes where everything went fine fold into one row.',
    () => calmLanesDemo()],
];
ch('patterns', 'Patterns', 'Recurring screen patterns built from the components, including the two borrowed from the other round-two directions.',
  PATTERNS.map(([id, t]) => [id, t]),
  () => PATTERNS.map(([id, t, lede, fn]) => `${sub(id, t, lede)}${stack(fn)}`).join('') + why('The approval card pattern (why, impact, undo, then approve with passkey) is shown in <a href="#microcopy">Voice and tone › Patterns</a>.'));

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
  ${stack(() => `<div class="micro">${microPatterns()}</div>`)}
  `);

// =====================================================================
// 9. Logo (official: Fermata, strengthened, in assets/; explored marks in logo-kits/)
// =====================================================================
const W = LOGOS.find((L) => L.id === OFFICIAL_LOGO);
const K = (L, f) => `${kitDir(L.id)}/${f}`;
const th = (light, dark, attrs = '') => `<img src="${light}" data-l="${light}" data-d="${dark}" alt="" ${attrs}>`;
export const inlineMark = (L) => `<svg class="logo-mark" viewBox="${L.mark.box.join(' ')}" role="img" aria-label="conductor"><g fill="var(--color-logo-ink)">${L.mark.ink}</g><g fill="var(--color-accent-default)">${L.mark.acc}</g></svg>`;
ch('logo', 'Logo', `The conductor logo is ${W.name}: the hold-until-released arch. The official kit is in assets/.`,
  [['lockups', 'The mark'], ['logo-files', 'Files'], ['logo-space', 'Clear space and size'], ['misuse', 'Misuse'], ['cobrand', 'Co-branding'], ['logo-explored', 'Explored directions']],
  () => `
  ${sub('lockups', 'The mark', `A fermata tells an ensemble to hold until the conductor releases it. conductor works the same way: runs wait behind an approval, a bot pauses on “needs you”, and nothing moves until you sign. ${W.why} The choice is recorded on the <a href="logo-review.html">logo review</a>.`)}
  <div class="logo-tiles">
    <div class="ltile" data-theme="light" style="background:var(--color-bg-canvas)"><img src="${K(W, 'logo-horizontal.svg')}" alt=""><span>horizontal · light</span></div>
    <div class="ltile" data-theme="dark" style="background:var(--color-bg-canvas)"><img src="${K(W, 'logo-horizontal-dark.svg')}" alt=""><span>horizontal · dark</span></div>
    <div class="ltile" data-theme="light" style="background:var(--color-bg-surface)"><img src="${K(W, 'logo-stacked.svg')}" alt="" style="max-height:62%"><span>stacked</span></div>
    <div class="ltile" style="background:#ffffff;color:#000"><img src="${K(W, 'logo-mark-mono.svg')}" alt="" style="height:64px"><span>mono · black</span></div>
    <div class="ltile" data-theme="light" style="background:var(--color-accent-default);color:#fff"><div style="width:150px;height:72px;background:#fff;-webkit-mask:url(${K(W, 'logo-horizontal-mono.svg')}) center/contain no-repeat;mask:url(${K(W, 'logo-horizontal-mono.svg')}) center/contain no-repeat"></div><span>one colour on ember</span></div>
    <div class="ltile" data-theme="dark" style="background:var(--color-bg-canvas)"><img src="${K(W, 'app-icon.svg')}" alt="" style="height:96px"><span>app icon</span></div>
  </div>
  ${why('<b>In app chrome</b> use the inline mark (<code>mark-inline.html</code> in the kit) at 20px tall beside the live-text wordmark (<code>--type-brand</code>: Fraunces SemiBold). The mark’s arch takes <code>--color-logo-ink</code>, which is deeper than body text on cream so the mark holds at small sizes, and the dot takes <code>--color-accent-default</code>. This page’s top bar uses exactly that.')}
  ${sub('logo-files', 'Files', 'One canonical location: <a href="assets/"><code>design/brand/assets/</code></a>. Files with no suffix are for light backgrounds, <code>-dark</code> for dark, and <code>-mono</code> uses <code>currentColor</code>.')}
  <div class="files-grid">${[['logo-horizontal.svg', 'Default lockup: README, docs, sign-in'], ['logo-stacked.svg', 'Square spaces'], ['logo-mark.svg', 'Mark alone (32-unit square)'], ['wordmark.svg', 'Name alone'], ['mark-inline.html', 'Inline SVG for app chrome, coloured by tokens'], ['favicon.svg', '16px cut; follows the browser scheme (+ 16/32/48 PNGs)'], ['apple-touch-icon.png', 'Home screen (+ icon-192, icon-512, app-icon.svg)'], ['social-card.png', '1280×640 social preview'], ['README.md', 'File list and the exact prototype changes']].map(([f, u]) => `<a class="file" href="assets/${f}"><b>${f}</b><span>${u}</span></a>`).join('')}</div>
  ${sub('logo-space', 'Clear space and size', 'Keep one dot diameter clear on every side. Below 24px, use the 16px cut (favicon.svg), not a scaled master.')}
  <div style="display:flex;gap:var(--space-8);flex-wrap:wrap;align-items:center">
    <div class="clear">${th(K(W, 'logo-horizontal.svg'), K(W, 'logo-horizontal-dark.svg'))}</div>
    <div class="mins">
      <div><img src="${K(W, 'favicon.svg')}" width="16" height="16" alt="">mark 16px (16px cut)</div>
      <div>${th(K(W, 'logo-mark.svg'), K(W, 'logo-mark-dark.svg'), 'width="24" height="24"')}mark 24px</div>
      <div>${th(K(W, 'logo-horizontal.svg'), K(W, 'logo-horizontal-dark.svg'), 'style="height:20px"')}horizontal 20px tall</div>
      <div>${th(K(W, 'logo-stacked.svg'), K(W, 'logo-stacked-dark.svg'), 'style="width:72px"')}stacked 72px wide</div>
    </div>
  </div>
  ${sub('misuse', 'Misuse')}
  <div class="logo-tiles" data-contrast="exempt">
    ${[['transform:scaleX(1.35);max-width:52%', 'stretched'], ['filter:hue-rotate(180deg)', 'recoloured'], ['transform:rotate(180deg)', 'rotated'], ['filter:drop-shadow(0 6px 8px rgba(0,0,0,.5))', 'effects and shadows'], ['', 'on a busy image', 'repeating-linear-gradient(45deg,#cc4a0e 0 12px,#3f9a86 12px 24px)'], ['letter-spacing:.4em;font:italic 34px Georgia,serif', 're-typed wordmark', '', 'conductor']]
      .map(([st, l, bg = '', txt = '']) => `<div class="ltile bad" style="background:${bg || '#f7f3ec'};color:#1c1712">${txt ? `<span style="position:static;opacity:1;${st};text-transform:none">${txt}</span>` : `<img src="${K(W, 'logo-horizontal.svg')}" alt="" style="${st}">`}<span>${l}</span></div>`).join('')}
  </div>
  ${sub('cobrand', 'Co-branding', 'Partner logos sit to the right at equal optical height, separated by a hairline rule with two dot-widths of space on each side. conductor comes first on our surfaces, and we never merge the marks.')}
  <div class="cobrand">${th(K(W, 'logo-horizontal.svg'), K(W, 'logo-horizontal-dark.svg'), 'style="height:40px"')}<span class="sep"></span><span class="partner"><i></i>Acme</span></div>
  ${sub('logo-explored', 'Explored directions', 'Each was taken to a complete kit and tested in the same chrome before the choice. They are kept for reference, and are not for use.')}
  <table class="perdir"><thead><tr><th>Mark</th><th>Idea</th><th>Why not</th></tr></thead><tbody>
    ${LOGOS.filter((L) => L.id !== W.id).map((L) => `<tr><td>${th(K(L, 'logo-horizontal.svg'), K(L, 'logo-horizontal-dark.svg'))}</td><td><b>${L.name}</b><br>${L.line}</td><td>${L.whyNot}</td></tr>`).join('')}
    ${[['fermata', 'Fermata (round one)', 'The same idea, drawn thin with an italic wordmark.', 'Weak on light surfaces: the hairline arch, small dot and light italic lost to cream. Strengthened into the official mark.'],
       ['downbeat', 'Downbeat (round one)', 'The prototype’s baton, rebuilt heavier.', 'A diagonal and a dot is a common shape, and it carries the least meaning.'],
       ['signature', 'Signature (round one)', 'The wordmark is the logo, with the full stop as the downbeat.', 'Elegant but not ownable, and its brass accent collided with the warning colour.']]
      .map(([id, n, idea, why]) => `<tr><td>${th(`directions/${id}/logo-horizontal-light.svg`, `directions/${id}/logo-horizontal-dark.svg`)}</td><td><b>${n}</b><br>${idea}</td><td>${why}</td></tr>`).join('')}
  </tbody></table>
  `);

// =====================================================================
// 10. Tokens
// =====================================================================
ch('tokens', 'Tokens', 'There is one file of truth and generated CSS. Raw values feed semantic roles, semantic roles feed components, and an explicit theme always wins.',
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
  ${sub('migration', 'Migrating the dashboard', 'Load fonts/fonts.css, tokens.css and prototype-compat.css, and delete the theme and :root blocks in css/app.css: the prototype takes on Daylight’s colours, radii and fonts at once. Then, in one pass, replace each old name with its token (this table), apply the type shorthands, and move decorative mono to the sans. The notes give the old and new values.')}
  <div class="ctable-wrap" style="max-height:440px"><table class="mig"><thead><tr><th>Prototype</th><th>Token</th><th>Note</th></tr></thead><tbody>${MIG.map((m) => `<tr><td><code>${m.from}</code></td><td><code>${m.to}</code></td><td>${m.note}</td></tr>`).join('')}</tbody></table></div>
  `);

// ---------- mount ----------
document.getElementById('brand-mark').outerHTML = inlineMark(W);
const root = document.getElementById('chapters');
root.innerHTML = chapters.map((c, i) => `<section class="ch" id="${c.id}"><div class="ch-head"><div><span class="num">${String(i + 1).padStart(2, '0')} · ${c.title}</span><h2>${c.title}</h2></div><p>${c.blurb}</p></div>${c.body()}</section>`).join('');
document.getElementById('toc').innerHTML = `<ol><li><a href="#principles"><span class="n">00</span>Principles</a></li>${chapters.map((c, i) => `<li><a href="#${c.id}"><span class="n">${String(i + 1).padStart(2, '0')}</span>${c.title}</a><ol>${c.subs.map(([id, t]) => `<li><a href="#${id}">${t}</a></li>`).join('')}</ol></li>`).join('')}</ol>`;

// calm before/after pairs: viewport and theme toggles swap the screenshots and their check counts
const calmState = { vp: 'desktop', th: 'light' };
for (const [id, k] of [['calm-vp', 'vp'], ['calm-th', 'th']]) document.getElementById(id).addEventListener('click', (e) => {
  const v = e.target.closest('span[data-v]')?.dataset.v; if (!v) return;
  calmState[k] = v;
  for (const x of document.querySelectorAll(`#${id} span`)) x.classList.toggle('is-active', x.dataset.v === v);
  document.querySelector('.calm-pairs').dataset.vp = calmState.vp;
  for (const sec of document.querySelectorAll('.calm-pair')) sec.querySelector('.calm-figs').innerHTML = calmPair(sec.dataset.screen, calmState.vp, calmState.th);
});
// a collapsed Finished group in the sample tables opens in place
document.addEventListener('click', (e) => {
  const sum = e.target.closest('tr.done-sum'); if (!sum) return;
  const open = sum.classList.toggle('open');
  for (const r of sum.parentElement.querySelectorAll('tr:not(.done-sum)')) r.hidden = !open;
});

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
