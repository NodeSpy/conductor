// scannable-review.html: the proposal page. Everything is rendered live from scannable/scan.js, except
// the before / calm / proposed comparisons, which are screenshots (v2/render-scan.mjs).
import { KINDS, ACTIONS, STEPS, FLAGS, ENV, flag, env, lucide, rows, injectScanCSS } from '../status/rows.js';
import { injectMarkCSS } from '../status/marks.js';
import * as S from '../v2/rows-data.js';
injectMarkCSS(); injectScanCSS();

const [ICONCHK] = await Promise.all(['scannable/icon-check.json'].map((u) => fetch(u).then((r) => r.json()).catch(() => null)));
const THEMES = ['light', 'dark'];
const pair = (fn, cls = '') => `<div class="pair two ${cls}">${THEMES.map((t) => `<div class="panel" data-theme="${t}"><span class="tag">${t}</span>${fn(t)}</div>`).join('')}</div>`;
const sec = (id, title, lede, body) => `<section class="var" id="${id}"><h2>${title}</h2>${lede ? `<p class="why-p">${lede}</p>` : ''}${body}</section>`;

// ---------- principles ----------
const PRINCIPLES = [
  ['Identify before reading', 'Every item shows what it is (an icon and a word), where it happens and how it went, in the same place on every screen.'],
  ['Keep the information', 'What a person uses to understand or decide stays on the row. Only ids, hashes and raw payloads are folded away.'],
  ['Flag only what is worth stopping for', 'Production changes, money and destructive or access changes, in plain words with one mark, and only where someone decides: approvals and a bot’s permissions. Everything else carries no risk label.'],
  ['One meaning per icon', 'An icon stands for one kind of thing or action, everywhere, always next to its word.'],
];
const principles = () => `<ol class="sr-principles">${PRINCIPLES.map(([t, d]) => `<li><b>${t}</b><span>${d}</span></li>`).join('')}</ol>`;

// ---------- icons ----------
const sizes = (name) => [16, 20, 24].map((s) => `<span class="sz">${lucide(name, s)}</span>`).join('');
const iconTable = (title, set, note) => `<h3 class="sr-h3">${title}</h3>${note ? `<p class="sr-note">${note}</p>` : ''}
  <div class="sr-icons"><div class="sr-ih"><span>Light · 16 / 20 / 24</span><span>Dark · 16 / 20 / 24</span><span>Name</span><span>Meaning</span><span>Lucide</span></div>
  ${Object.values(set).map((v) => `<div class="sr-irow"><span class="sw" data-theme="light">${sizes(v.icon)}</span><span class="sw" data-theme="dark">${sizes(v.icon)}</span><b>${v.word}</b><span class="m">${v.means}</span><code>${v.icon}</code></div>`).join('')}</div>`;
// ---------- risk ----------
const WHERE = { prod: 'On an approval for any change to a production environment. In lists the Production tag already says it.', money: 'On approvals and permissions, for commit_money verbs, with the amount.', destroy: 'Wherever a verb deletes or destroys something stateful, at any tier.', access: 'Wherever a verb changes IAM, collaborators or grants, at any tier.' };
const flags = () => `<div class="sr-flags">${Object.keys(FLAGS).map((k) => `<div><span class="sw" data-theme="light">${flag(k)}</span><span class="sw" data-theme="dark">${flag(k)}</span><span class="m">${WHERE[k]}</span></div>`).join('')}</div>`;

// ---------- environments ----------
const envs = () => `<div class="sr-envs">${Object.entries(ENV).map(([k, v]) => `<div><span class="sw" data-theme="light">${env(k)}</span><span class="sw" data-theme="dark">${env(k)}</span><span class="m">${v.means}</span></div>`).join('')}</div>`;

// ---------- anatomy ----------
const anatomy = () => `
  ${pair(() => rows([['needs', [S.NEEDS[0], S.NEEDS[3]]], ['running', [S.RUNNING[2]]], ['fine', [S.FINISHED[0]]]]))}
  <h3 class="sr-h3">On a phone</h3>
  <div class="pair phones">${THEMES.map((t) => `<div class="panel phone" data-theme="${t}"><span class="tag">${t} · 390px</span>${rows([['needs', S.NEEDS.slice(0, 3)], ['running', [S.RUNNING[0]]]], { head: false })}</div>`).join('')}</div>`;

// ---------- audit ----------
const AUDIT = [
  ['risk / tier ladder', 'bars (ours)', 'A signal-strength meter standing for risk and autonomy tiers.', 'wrong', 'Plain words; one mark only for production, money and destructive or access changes'],
  ['Lists (needs you, run history, proposals, activity)', '—', 'No icon at all: an approval, a failed run and a PR review look the same until read.', 'missing', 'A kind icon and word on every row'],
  ['members', 'bot', 'Shown on team names and people.', 'wrong', '<code>users</code> for teams; <code>bot</code> only for bots'],
  ['shield', 'shield-check', 'Four meanings: a TLS certificate, “sandbox on”, “used only on work you started”, approved execution.', 'overloaded', 'Sandbox only; the others in words'],
  ['key', 'key-round', 'Secrets, and the passkey fallback.', 'overloaded', 'Login / credential only; passkeys keep <code>fingerprint</code>'],
  ['updates', 'refresh-cw', '“Update to v…”, “Replace” a login, retry.', 'overloaded', 'Run again only; updates and replacements in words'],
  ['commit', 'git-commit-horizontal', '“Approve with changes” and “Import”.', 'wrong', 'Now means “Pushes code” only'],
  ['sparkle', 'sparkles', '“Get started” and the demo-data control.', 'decorative', 'Removed'],
  ['cookie', 'cookie', 'A logged-in browser session.', 'unclear', '<code>key-round</code> (Login) and the words “logged in”'],
  ['lock', 'lock', '“Lock” meaning sign out.', 'wrong', '<code>log-out</code>; locks never mean sign-out'],
  ['terminal', 'square-terminal', '“Open in paseo” (not a terminal).', 'wrong', '<code>external-link</code>; <code>square-terminal</code> only for Command steps'],
  ['canvas', 'panels-top-left', '“Open in the builder”.', 'unclear', '<code>workflow</code> and the word'],
  ['clock', 'clock', 'A repo history hit, a bot’s own sweep, and in the step picker both “Wait” and “Wait for a condition”.', 'overloaded', 'Time and schedules only; Wait steps use <code>hourglass</code>'],
  ['step picker: GitHub verbs', 'git-branch', 'The same branch icon on every GitHub verb: reading a diff looks like pushing code.', 'wrong', 'Each verb’s action icon: reads, comments, pushes, deletes'],
  ['step picker: Code / Run a command', 'code', 'The same icon for two different steps.', 'duplicate', '<code>square-terminal</code> for commands; inline code keeps <code>code</code> in the editor only'],
  ['step picker: Agent', 'sparkles', 'The “AI” cliché.', 'unclear', '<code>brain-circuit</code> “AI agent”'],
  ['dollar', 'circle-dollar-sign', 'Cloud spend, once.', 'keep, rename', 'Cloud spend <code>receipt</code>; spends money <code>banknote</code>; amounts in the risk band'],
  ['diamond, ghost, dot', 'diamond, ghost, circle-small', 'Status shapes drawn as icons.', 'duplicate', 'The status marks (needs you, shadow); the dot is removed'],
  ['warn', 'triangle-alert', 'Twelve warnings of different kinds.', 'keep', 'Warnings in callouts only, always with words'],
  ['zap, folder, search, plus, x, chevrons, trash, edit, link, download, upload', 'same', 'Controls and navigation.', 'keep', 'Unchanged; trash is “delete” everywhere'],
];
const audit = () => `<div class="c-table-wrap"><table class="c-table sr-audit"><thead><tr><th>Prototype icon</th><th>Lucide</th><th>Where it is used</th><th>Verdict</th><th>Proposal</th></tr></thead><tbody>${AUDIT.map(([a, l, w, v, p]) => `<tr><td><b>${a}</b></td><td><code>${l}</code></td><td>${w}</td><td><span class="vd vd-${v.split(',')[0].replace(/\s.*/, '')}">${v}</span></td><td>${p}</td></tr>`).join('')}</tbody></table></div>`;

// ---------- comparisons ----------
const CMP = [
  ['overview', 'Overview: the needs-you list', 'Before, every needs-you row had the same shape and weight, whatever it would do. Proposed: each says what it is and where; the production apply, the access grant and the $1,800 purchase say so in words, and nothing was removed.'],
  ['history', 'Run history', 'Proposed: grouped by attention, every run kept, with its kind, bot and environment in fixed columns.'],
  ['approval', 'An approval', 'Proposed: the line above the title says Approval · Terraform · Changes production · Needs you. What it will do stays visible; only hashes are folded.'],
  ['perms', 'A bot’s permissions', 'Proposed: grouped by who decides, each verb with its action icon and environment; only production, money and destructive or access verbs carry a flag.'],
  ['picker', 'The workflow step picker', 'Proposed: each step has the icon of what it does. Before, every GitHub verb had the same branch icon.'],
];
const VERS = [['before', 'Before (the dashboard prototype)'], ['calm', 'Calm by default (the over-correction)'], ['scan', 'Proposed: scannable by default']];
const cmpFigs = (s, vp, th) => VERS.map(([v, label]) => {
  if (v === 'calm' && s === 'picker') return `<figure class="none"><figcaption><b>${label}</b></figcaption><p>The calm round didn’t change the step picker.</p></figure>`;
  const live = v === 'before' ? '' : `<a href="v2/mock.html?d=daylight&s=${v === 'calm' ? { perms: 'bot' }[s] ?? s : s}&t=${th}${v === 'scan' ? '&v=scan' : '&v=calm'}">live</a>`;
  return `<figure class="${v === 'scan' ? 'chosen' : ''}"><figcaption><b>${label}</b>${live}</figcaption><img src="scannable/shots/${s}-${v}-${vp}-${th}.png" alt="${label}: ${s}, ${vp}, ${th}"></figure>`;
}).join('');
const compare = () => `<div class="sr-ctl"><div class="c-seg" id="cmp-vp"><span data-v="desktop" class="is-active">Desktop</span><span data-v="phone">Phone</span></div><div class="c-seg" id="cmp-th"><span data-v="light" class="is-active">Light</span><span data-v="dark">Dark</span></div></div>
  <div class="sr-cmp" data-vp="desktop">${CMP.map(([s, t, d]) => `<section data-screen="${s}"><h3 class="sr-h3">${t}</h3><p class="sr-note">${d}</p><div class="figs">${cmpFigs(s, 'desktop', 'light')}</div></section>`).join('')}</div>`;

// ---------- checks ----------
const checks = () => `<div class="sr-checks">
  <div><h3 class="sr-h3">Icons at 16px</h3><p><b>${ICONCHK ? `${ICONCHK.rows.length} glyphs: ${ICONCHK.inkBad} outside the ink range, ${ICONCHK.alikeBad} pairs too alike.` : 'Not run.'}</b> <code>tools/icon-check.mjs</code> renders every icon at 16 CSS px on a 1× screen and measures its real pixels: the inked share of the cell, and how much any two icons overlap (best of every 1px shift). It led to two refinements of the status marks, applied everywhere: <b>passed</b> is a smaller dot and <b>waiting</b> an outlined triangle, so neither reads as the needs-you diamond. Report: <a href="scannable/ICON-CHECK.md">ICON-CHECK.md</a>.</p>
  <figure><img src="scannable/icon-16px-sheet.png" alt="Every icon at its real 16px pixels, enlarged six times"><figcaption>The real 16px pixels, enlarged 6× with no smoothing.</figcaption></figure></div>
</div></div>`;

const OPEN = [
  'The three flags and their words: Changes production · Spends money · Destroys / Grants access.',
  'Should the overview keep its four most recent finished runs, or only link to the history?',
  'Once approved: roll this into the tokens, the book, the mocks and the dashboard migration.',
];

document.getElementById('review').innerHTML = [
  sec('principles', 'Principles', '', principles()),
  sec('icons', 'The icon set', 'One icon per kind of thing and per kind of action, each with one meaning, always next to its word. Lucide (ISC), 1.75 stroke.',
    iconTable('Objects and events', KINDS, 'A run’s state is the status mark beside it, not part of its icon.')
    + iconTable('Actions', ACTIONS, 'Used where a verb is listed: permissions, “what it will do” on an approval, the step picker.')
    + iconTable('Workflow steps (builder only)', STEPS)),
  sec('risk', 'Risk, only where you decide', 'No meter, no ladder. Most verbs need no risk label: their icon and word say what they do. Three things are worth stopping for, shown in plain words with one mark, on approvals and a bot’s permissions. Only destroying and granting access take colour.', flags()),
  sec('env', 'Environment tags', 'Told apart by fill, never by colour alone. No tag means the item isn’t tied to an environment.', envs()),
  sec('anatomy', 'One row anatomy', 'What · title and why · where · status · when · action, in the same columns on every list.', anatomy()),
  sec('audit', 'Icon audit: the dashboard prototype', 'Every icon the prototype uses, where, and what this proposal does with it.', audit()),
  sec('compare', 'Before · calm · proposed', 'Five screens. Before is the dashboard prototype itself, with its own demo data; calm (the previous round) and proposed share one data set, which mixes routine and dangerous work. Every calm and proposed screen has a live mock.', compare()),
  sec('checks', 'Checks', '', checks()),
  sec('open', 'For review', '', `<ol class="sr-open">${OPEN.map((o) => `<li>${o}</li>`).join('')}</ol>`),
].join('');

const st = { vp: 'desktop', th: 'light' };
for (const [id, k] of [['cmp-vp', 'vp'], ['cmp-th', 'th']]) document.getElementById(id).addEventListener('click', (e) => {
  const v = e.target.closest('span[data-v]')?.dataset.v; if (!v) return;
  st[k] = v;
  for (const x of document.querySelectorAll(`#${id} span`)) x.classList.toggle('is-active', x.dataset.v === v);
  document.querySelector('.sr-cmp').dataset.vp = st.vp;
  for (const s of document.querySelectorAll('.sr-cmp section')) s.querySelector('.figs').innerHTML = cmpFigs(s.dataset.screen, st.vp, st.th);
});
