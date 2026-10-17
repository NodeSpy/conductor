// scannable-review.html: the proposal page. Everything is rendered live from scannable/scan.js, except
// the before / calm / proposed comparisons, which are screenshots (v2/render-scan.mjs).
import { KINDS, ACTIONS, STEPS, RISK, ENV, risk, env, lucide, kind, rows, riskOf, injectScanCSS, status, mark } from './scan.js';
import { injectMarkCSS } from '../status/marks.js';
import * as S from './data.js';
injectMarkCSS(); injectScanCSS();

const [ICONCHK] = await Promise.all(['scannable/icon-check.json'].map((u) => fetch(u).then((r) => r.json()).catch(() => null)));
const ink = (word) => ICONCHK?.rows.find((r) => r.word === word);
const THEMES = ['light', 'dark'];
const pair = (fn, cls = '') => `<div class="pair two ${cls}">${THEMES.map((t) => `<div class="panel" data-theme="${t}"><span class="tag">${t}</span>${fn(t)}</div>`).join('')}</div>`;
const sec = (id, title, lede, body) => `<section class="var" id="${id}"><h2>${title}</h2>${lede ? `<p class="why-p">${lede}</p>` : ''}${body}</section>`;

// ---------- principles ----------
const PRINCIPLES = [
  ['Identify before reading', 'Every item shows what it is (an icon and a word), how risky it is (a marker on a fixed ladder), where it happens (an environment tag) and its state (the status mark), in the same place on every screen.'],
  ['Keep the information', 'Nothing a person uses to understand or decide is hidden: titles, the one-line why, bot, repo, environment, risk, status and time stay on the row. Only ids, hashes and raw payloads go behind a disclosure or a hover.'],
  ['Order by attention', 'Needs you, then failing, then running, then finished: by position (groups), weight (bold to regular) and the status marks. Within a group, the most consequential first.'],
  ['Danger is unmistakable, routine is quiet', 'Reads only and reversible changes are quiet ink. Production, money and destructive or access changes are solid bands. A production Terraform apply never looks like a bot posting a comment.'],
  ['One meaning per icon, always with a word', 'An icon stands for one kind of thing or one kind of action, everywhere, and never for anything else. It is never decorative and never alone.'],
  ['Dense, but structured', 'Fixed columns that line up, so the eye learns where to look: what · title · where · risk · status · when · action. Comfortable density by default, compact on request.'],
];
const principles = () => `<ol class="sr-principles">${PRINCIPLES.map(([t, d]) => `<li><b>${t}</b><span>${d}</span></li>`).join('')}</ol>`;

// ---------- icons ----------
const sizes = (name) => [16, 20, 24].map((s) => `<span class="sz">${lucide(name, s)}</span>`).join('');
const iconTable = (title, set, note) => `<h3 class="sr-h3">${title}</h3>${note ? `<p class="sr-note">${note}</p>` : ''}
  <div class="sr-icons"><div class="sr-ih"><span>Light · 16 / 20 / 24</span><span>Dark · 16 / 20 / 24</span><span>Name</span><span>Meaning</span><span>Source</span><span>Ink at 16px</span></div>
  ${Object.values(set).map((v) => { const c = ink(v.word); return `<div class="sr-irow"><span class="sw" data-theme="light">${sizes(v.icon)}</span><span class="sw" data-theme="dark">${sizes(v.icon)}</span><b>${v.word}</b><span class="m">${v.means}</span><code>${v.icon}</code><span class="ck">${c ? `${mark('passed')} ${(c.ink * 100).toFixed(0)}%` : ''}</span></div>`; }).join('')}</div>`;
const runStates = () => `<div class="sr-runstates">${[['failed', 'Failed run'], ['running', 'Running run'], ['passed', 'Finished run']].map(([st, w]) => `<span>${kind('run')}${status(st)}<em>= ${w}</em></span>`).join('')}</div>`;

// ---------- risk ----------
const EXAMPLES = ['Read logs, diffs, metrics; plan Terraform', 'Ack a page, label an issue, set a key in staging', 'Push to a PR, post to Slack, apply to staging', 'Apply Terraform to prod, deploy to prod', 'Buy a savings plan or reserved instances', 'Delete a repo or a database; change IAM; add a collaborator'];
const TREAT = ['1 bar · tertiary ink · regular', '2 bars · secondary ink · regular', '3 bars · primary ink · semibold', '4 bars · solid ink band · bold', '5 bars · solid amber band · bold · shows the amount', '6 bars · solid red band · bold · says “Destroys” or “Grants access”'];
const ladder = () => `<div class="sr-ladder"><div class="sr-lh"><span>Step</span><span>Light</span><span>Dark</span><span>Means</span><span>Usually from</span><span>Examples</span><span>Treatment</span></div>
  ${[1, 2, 3, 4, 5, 6].map((l) => `<div class="sr-lrow"><b>${l}</b><span class="sw" data-theme="light">${risk(l)}</span><span class="sw" data-theme="dark">${risk(l)}</span><span>${RISK[l].means}</span><code>${RISK[l].tiers}</code><span class="m">${EXAMPLES[l - 1]}</span><span class="m">${TREAT[l - 1]}</span></div>`).join('')}</div>`;
const TIERS = [['observe', 'Runs on its own'], ['reversible', 'A proposal until promoted'], ['propose', 'Asks you first'], ['commit_money', 'Asks you, with an amount'], ['never', 'Refused']];
const mapping = () => `<table class="sr-map"><thead><tr><th>Autonomy tier (who decides)</th><th>Local</th><th>Staging</th><th>Production</th></tr></thead><tbody>
  ${TIERS.map(([t, w]) => `<tr><td><code>${t}</code> <span class="m">${w}</span></td>${['local', 'staging', 'production'].map((e) => `<td>${t === 'never' ? '<span class="m">refused; the verb keeps its own step</span>' : risk(riskOf({ tier: t, env: e, action: t === 'observe' ? 'read' : t === 'commit_money' ? 'spend' : 'apply' }))}</td>`).join('')}</tr>`).join('')}
  <tr><td><span class="m">Any tier, if the verb deletes, destroys or changes permissions</span></td><td colspan="3">${risk(6, 'Destroys')} ${risk(6, 'Grants access')}</td></tr></tbody></table>`;

// ---------- environments ----------
const envs = () => `<div class="sr-envs">${Object.entries(ENV).map(([k, v]) => `<div><span class="sw" data-theme="light">${env(k)}</span><span class="sw" data-theme="dark">${env(k)}</span><span class="m">${v.means}</span></div>`).join('')}</div>`;

// ---------- anatomy ----------
const ANAT = [['1', 'What', 'Kind icon and its word'], ['2', 'Title', 'Plain words, then one line of why'], ['3', 'Where', 'Bot first, then repo or target, then the environment tag'], ['4', 'Risk', 'The ladder marker'], ['5', 'Status', 'The status mark and word'], ['6', 'When', 'Relative time; exact time on hover'], ['7', 'Action', 'One verb, primary only for decisions']];
const anatomy = () => `<div class="sr-anat">${ANAT.map(([n, t, d]) => `<span><b>${n}</b> ${t}<em>${d}</em></span>`).join('')}</div>
  ${pair(() => rows([['needs', [S.NEEDS[0], S.NEEDS[3]]], ['running', [S.RUNNING[2]]], ['fine', [S.FINISHED[0]]]]))}
  <h3 class="sr-h3">On a phone</h3><p class="sr-note">The same order in two lines: icon and title (with time), then the kind word, bot and environment, then risk and status. The action is the whole row.</p>
  <div class="pair phones">${THEMES.map((t) => `<div class="panel phone" data-theme="${t}"><span class="tag">${t} · 390px</span>${rows([['needs', S.NEEDS.slice(0, 3)], ['running', [S.RUNNING[0]]]], { head: false })}</div>`).join('')}</div>`;

// ---------- audit ----------
const AUDIT = [
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
  ['overview', 'Overview: the needs-you list', 'Before, every needs-you row had the same shape and weight, whatever it would do. Proposed: each says what it is, the production apply, the access grant and the $1,800 purchase carry solid bands, and nothing was removed.'],
  ['history', 'Run history', 'Proposed: grouped by attention, every run kept, with its kind, bot, environment and risk in fixed columns. The filter chip finds the runs that touched production, money or access.'],
  ['approval', 'An approval', 'Proposed: the header strip says Approval · Terraform · Production · risk · needs you before the title. What it will do and why this risk are visible; only hashes are folded.'],
  ['perms', 'A bot’s permissions', 'Proposed: grouped by who decides (the autonomy tier), each verb with its action icon, environment and risk, so “what can it do without asking me?” is answered at a glance.'],
  ['picker', 'The workflow step picker', 'Proposed: each step shows its action icon and its risk before you add it. Before, every GitHub verb had the same branch icon.'],
];
const VERS = [['before', 'Before (the dashboard prototype)'], ['calm', 'Calm by default (the over-correction)'], ['scan', 'Proposed: scannable by default']];
const cmpFigs = (s, vp, th) => VERS.map(([v, label]) => {
  if (v === 'calm' && s === 'picker') return `<figure class="none"><figcaption><b>${label}</b></figcaption><p>The calm round didn’t change the step picker.</p></figure>`;
  const live = v === 'before' ? '' : `<a href="v2/mock.html?d=daylight&s=${v === 'calm' ? { perms: 'bot' }[s] ?? s : s}&t=${th}${v === 'scan' ? '&v=scan' : ''}">live</a>`;
  return `<figure class="${v === 'scan' ? 'chosen' : ''}"><figcaption><b>${label}</b>${live}</figcaption><img src="scannable/shots/${s}-${v}-${vp}-${th}.png" alt="${label}: ${s}, ${vp}, ${th}"></figure>`;
}).join('');
const compare = () => `<div class="sr-ctl"><div class="c-seg" id="cmp-vp"><span data-v="desktop" class="is-active">Desktop</span><span data-v="phone">Phone</span></div><div class="c-seg" id="cmp-th"><span data-v="light" class="is-active">Light</span><span data-v="dark">Dark</span></div></div>
  <div class="sr-cmp" data-vp="desktop">${CMP.map(([s, t, d]) => `<section data-screen="${s}"><h3 class="sr-h3">${t}</h3><p class="sr-note">${d}</p><div class="figs">${cmpFigs(s, 'desktop', 'light')}</div></section>`).join('')}</div>`;

// ---------- checks ----------
const checks = () => `<div class="sr-checks">
  <div><h3 class="sr-h3">Icons at 16px</h3><p><b>${ICONCHK ? `${ICONCHK.rows.length} glyphs: ${ICONCHK.inkBad} outside the ink range, ${ICONCHK.alikeBad} pairs too alike.` : 'Not run.'}</b> <code>tools/icon-check.mjs</code> renders every icon at 16 CSS px on a 1× screen and measures its real pixels: the inked share of the cell, and how much any two icons overlap (best of every 1px shift). It flagged two status marks, now refined system-wide: <b>passed</b> is a smaller dot (r 3.4, was 4.6), so it no longer reads as the needs-you diamond, and <b>waiting</b> is an outlined triangle, so it no longer reads as the diamond either. Report: <a href="scannable/ICON-CHECK.md">ICON-CHECK.md</a>.</p>
  <figure><img src="scannable/icon-16px-sheet.png" alt="Every icon at its real 16px pixels, enlarged six times"><figcaption>The real 16px pixels, enlarged 6× with no smoothing.</figcaption></figure></div>
  <div><h3 class="sr-h3">Risk markers for colour-blind readers</h3><p><code>tools/risk-cvd.mjs</code> runs the dataviz validator (protanopia and deuteranopia) on the three bands in both themes. Every pair is distinct (worst: money vs destructive in dark, CVD ΔE 9.9 against a target of 8). Every band is at least 3:1 on the surface, and its words at least 4.5:1 on the band. Colour is the third signal: each step also has its own meter fill and its own word. Report: <a href="scannable/RISK-CVD.md">RISK-CVD.md</a>.</p>
  ${pair(() => `<div class="sr-ladder-mini">${[1, 2, 3, 4, 5, 6].map((l) => risk(l)).join('')}</div>`)}</div></div>`;

const OPEN = [
  'Adopt the six steps and their words? (Reads only · Reversible · Changes things · Changes production · Spends money · Destructive.)',
  'Money uses amber, which waiting also uses. They never share a column and differ in shape (a band vs an outlined triangle). Is that acceptable, or should money get its own hue?',
  'Should the overview keep its four most recent finished runs, as proposed, or only link to the history?',
  'Once approved: roll this into the tokens, the book (a “Scannable by default” chapter replacing “Calm by default”), the mocks and the dashboard migration.',
];

document.getElementById('review').innerHTML = [
  sec('principles', 'Principles', 'Calm by default asked “how little can we show?”. This asks “how fast can you tell what each thing is, and whether it matters?”.', principles()),
  sec('icons', 'The icon set', 'One icon per kind of thing and per kind of action. Each has one meaning, is used for nothing else, appears with its word and is legible at 16px. Lucide (ISC) at a 1.75 stroke on its 24 grid; the risk meters, environment glyphs and status marks are ours, on the 16 grid.',
    iconTable('Objects and events', KINDS, 'A run’s state is not part of its icon: the run icon says what it is, and the status mark beside it says how it went. Two signals that can never disagree.') + runStates()
    + iconTable('Actions: what a verb does to the world', ACTIONS, 'Used on permissions, approvals (“what it will do”) and the step picker. Writes / updates was added to the brief’s seven: acknowledging a page or setting a key is neither a post nor code.')
    + iconTable('Workflow step kinds (builder only)', STEPS, 'Flow steps, not actions on the world. A hand-off uses the Approval icon; calling a workflow uses Workflow.')),
  sec('risk', 'The risk ladder', 'Six steps by consequence, shown before reading: a meter filled to the step and the step’s word. The bottom three are quiet ink, and the top three are solid bands you can’t miss. The step comes from the verb’s autonomy tier, its action and its environment.', ladder() + `<h3 class="sr-h3">Mapped to the autonomy tiers and environments</h3><p class="sr-note">The tier says <b>who decides</b>; the risk says <b>what happens</b>. They are shown together on permissions and approvals. Reads are always step 1, even in production (the environment tag still says where).</p>` + mapping()),
  sec('env', 'Environment tags', 'Told apart by fill and outline, never by colour alone. Production is the only bold one.', envs()),
  sec('anatomy', 'One row anatomy', 'The same seven columns, in the same order, on every list: needs you, run history, activity, proposals and search results. The eye learns where to look.', anatomy()),
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
