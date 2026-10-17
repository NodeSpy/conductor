// explore/status-review.html: the whole label vocabulary (every pill, tag and badge the dashboard
// prototype renders through UI.st / UI.tag / UI.tier), redesigned as one system. Same decision
// structure as status-column.html: before, alternatives, a recommendation, rules.
import { LUCIDE } from '../icons/icons.js';
import { mark, status, legend, injectMarkCSS } from '../status/marks.js';
injectMarkCSS();

const lu = (n) => `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE[n]}</svg>`;
// Every state mark comes from the one vocabulary (status/marks.js).
const KEY = { ok: 'passed', failed: 'failed', needs: 'needs', waiting: 'waiting', running: 'running', parked: 'parked', shadow: 'shadow', staged: 'staged' };
const shape = (k) => mark(KEY[k]);
// Autonomy tiers as an ordinal ladder (one hue, more filled = more autonomy) plus plain words.
const TIERS = { observe: [1, 'Reads only'], reversible: [2, 'Acts, undoable'], propose: [3, 'Asks you first'], commit_money: [4, 'Spends money'], never: [0, 'Never'] };
const ladder = (t) => { const [n] = TIERS[t]; return `<span class="ladder ${t}" aria-hidden="true">${[1, 2, 3, 4].map((i) => `<i class="${i <= n ? 'on' : ''}" style="height:${3 + i * 2.5}px"></i>`).join('')}</span>`; };

// ---------- the labels, as the prototype has them: [text, job, prototype hue, extra] ----------
const SURFACES = [
  ['Bot header', 'bots/platform-sre', () => ({ kind: 'bot', name: 'platform-sre', labels: [
    ['home s7 live', 'state', 'run', { k: 'running', word: 'Live in home s7' }],
    ['team platform · lead', 'role', 'ember', { word: 'Lead of team platform' }],
    ['41/100 turns', 'count', '', { n: 41, of: 100, word: '41 of 100 turns' }],
    ['approved execution', 'fact', 'ok', { key: 'Commands', word: 'approved only' }],
    ['namespace', 'fact', 'ok', { key: 'Sandbox', word: 'namespace', icon: 'shield-check' }],
    ['operators: ops', 'role', '', { word: 'Operators: ops' }],
  ] })],
  ['Proposals', 'bots/platform-sre/proposals', () => ({ kind: 'rows', rows: [
    ['Replace the production web server', 'p12 · tf-apply.apply', [['pending', 'state', 'ember', { k: 'needs', word: 'Needs you' }], ['propose', 'tier', '', { t: 'propose' }]]],
    ['Post the monthly forecast to #incidents', 'p4 · slack-ops.post', [['shadow', 'state', 'wait', { k: 'shadow', word: 'Shadow: would have posted' }], ['reversible', 'tier', '', { t: 'reversible' }]]],
    ['Roll back the canary', 'p9 · deploy.rollback', [['executed', 'state', 'ok', { k: 'ok', word: 'Done · approved by ops' }], ['propose', 'tier', '', { t: 'propose' }]]],
    ['Raise the RDS instance size', 'p5 · aws.rds.modify', [['refused', 'state', 'fail', { k: 'failed', word: 'Refused by policy' }], ['commit_money', 'tier', '', { t: 'commit_money' }]]],
  ] })],
  ['Packs', 'extensions/packs', () => ({ kind: 'rows', rows: [
    ['acme/oncall', 'v2.4.1 · 6 workflows', [['official · trusted by default', 'trust', 'ok', { icon: 'shield-check', word: 'Official' }], ['installed', 'state', 'ok', { k: 'ok', word: 'Installed' }], ['pinned', 'fact', '', { key: 'Version', word: 'pinned' }]]],
    ['house-style', 'local folder · 2 workflows', [['local · yours', 'trust', 'ok', { icon: 'folder', word: 'Yours (local)' }], ['hot reload', 'fact', 'ok', { key: 'Reload', word: 'on save' }]]],
    ['community/pr-digest', 'v0.3.0 · 1 workflow', [['third party · pack_trust', 'trust', 'wait', { icon: 'globe', word: 'Third party', warn: true }], ['not tested', 'state', '', { k: 'waiting', word: 'Not tested yet' }]]],
  ] })],
  ['Staged changes', 'config/apply', () => ({ kind: 'rows', rows: [
    ['alert-watcher', 'workflows · new', [['new · staged', 'change', 'run', { word: 'New, not applied' }]]],
    ['platform-sre', 'bots · moved to work/platform', [['move staged', 'change', 'run', { word: 'Move, not applied' }]]],
    ['usage display', 'settings · changed', [['change staged', 'change', 'run', { word: 'Changed, not applied' }], ['restart', 'fact', '', { key: 'Applies', word: 'on restart' }]]],
  ] })],
  ['Browser logins', 'browser/logins', () => ({ kind: 'rows', rows: [
    ['nas.home.example', 'from 1Password · Home NAS', [['logged-in browser · narrowed to its sites', 'fact', 'wait', { key: 'Scope', word: 'its own sites only' }], ['works · 5h ago', 'state', 'ok', { k: 'ok', word: 'Works · checked 5 h ago' }]]],
    ['app.example.com', 'saved session', [['needs you: push approval', 'state', 'wait', { k: 'needs', word: 'Needs you: approve the push' }], ['device-bound', 'fact', '', { key: 'Session', word: 'this device only' }]]],
    ['grafana.internal', 'anonymous', [['anonymous · ephemeral', 'fact', 'ok', { key: 'Session', word: 'anonymous, discarded after' }], ['flagged as wrong', 'state', 'fail', { k: 'failed', word: 'Flagged as wrong' }]]],
  ] })],
];

// ---------- one renderer per variant: label → HTML ----------
const BEFORE_S = { ok: 'success', run: 'running', wait: 'warning', fail: 'danger', ember: 'accent', '': '' };
const R = {
  before: ([t, job, hue, x]) => job === 'tier' ? `<span class="b-tier">${x.t}</span>` : `<span class="b-tag" data-s="${BEFORE_S[hue]}">${t}</span>`,
  outline: ([t, job, hue, x]) => {
    const attn = (job === 'state' && ['needs', 'failed', 'waiting'].includes(x.k)) || x.warn;
    const SC = { needs: 'accent', failed: 'danger', waiting: 'warning' };
    return `<span class="o-tag ${attn ? 'attn' : ''}" data-s="${attn ? SC[x.k] ?? 'warning' : ''}">${job === 'tier' ? TIERS[x.t][1] : t}</span>`;
  },
  typed: ([t, job, hue, x]) => {
    switch (job) {
      case 'state': return status(KEY[x.k], x.word);
      case 'tier': return `<span class="ty-tier">${ladder(x.t)}<span>${TIERS[x.t][1]}</span></span>`;
      case 'change': return status('staged', x.word);
      case 'trust': return `<span class="ty-trust ${x.warn ? 'warn' : ''}">${lu(x.icon)}<span>${x.word}</span></span>`;
      case 'count': return `<span class="ty-count"><i class="bar"><b style="width:${(x.n / x.of) * 100}%"></b></i><span>${x.word}</span></span>`;
      case 'fact': return `<span class="ty-fact">${x.icon ? lu(x.icon) : ''}<span class="k">${x.key}</span> ${x.word}</span>`;
      default: return `<span class="ty-role">${x.word}</span>`;
    }
  },
  quiet: null, // the quiet variant writes sentences per surface instead of labels
};
const QUIET = (labels) => {
  const st = labels.find((l) => l[1] === 'state'), rest = labels.filter((l) => l !== st);
  const words = rest.map(([t, job, , x]) => job === 'tier' ? TIERS[x.t][1].toLowerCase() : job === 'fact' ? `${x.key.toLowerCase()}: ${x.word}` : x.word.charAt(0).toLowerCase() + x.word.slice(1));
  return `${st ? status(KEY[st[3].k], st[3].word) : ''}${words.length ? `<span class="q-line">${words.join(' · ')}</span>` : ''}`;
};

// Calm by default: a row shows only the labels that need someone or are live (failed, needs you,
// waiting, running, an unapplied change, an untrusted source). Everything else (tiers, facts, roles,
// counts, ids, and states that are simply fine) sits behind a quiet “Details” disclosure.
const loud = ([, job, , x]) => (job === 'state' && ['needs', 'failed', 'waiting', 'running'].includes(x.k)) || job === 'change' || x.warn;
const CALM = (labels, sub = '') => {
  const shown = labels.filter(loud), rest = labels.filter((l) => !loud(l));
  return `${shown.map(R.typed).join('')}${rest.length || sub ? `<details class="calm-more"><summary>Details</summary><div class="labels">${rest.map(R.typed).join('')}${sub ? `<code>${sub}</code>` : ''}</div></details>` : ''}`;
};

function surface(v, [title, route, fn]) {
  if (v === 'calm') {
    const s = fn();
    const body = s.kind === 'bot'
      ? `<div class="bot"><span class="av">ps</span><div><b class="nm">${s.name}</b><div class="labels">${CALM(s.labels)}</div></div></div>`
      : `<ul class="rows calm">${s.rows.map(([t, sub, labels]) => `<li${labels.some(loud) ? ' class="has-loud"' : ''}><div class="rt"><b>${t}</b></div><div class="labels">${CALM(labels, sub)}</div></li>`).join('')}</ul>`;
    return `<div class="surf"><div class="sh-t">${title} <code>#/${route}</code></div>${body}</div>`;
  }
  const s = fn(), render = (labels) => v === 'quiet' ? QUIET(labels) : labels.map(R[v]).join('');
  const body = s.kind === 'bot'
    ? `<div class="bot"><span class="av">ps</span><div><b class="nm">${s.name}</b><div class="labels">${render(s.labels)}</div></div></div>`
    : `<ul class="rows">${s.rows.map(([t, sub, labels]) => `<li><div class="rt"><b>${t}</b><code>${sub}</code></div><div class="labels">${render(labels)}</div></li>`).join('')}</ul>`;
  return `<div class="surf"><div class="sh-t">${title} <code>#/${route}</code></div>${body}</div>`;
}

const VARIANTS = [
  ['calm', 'Chosen: typed labels, calm by default', 'The typed vocabulary (B, below), shown the calm way. A row shows only what needs someone or is live: a failure, “needs you”, a waiting sign-in, a running bot, an unapplied change, an untrusted pack. Everything that is simply true (tiers, scopes, roles, counts, ids, and states that are fine) moves behind a quiet <b>Details</b>, in the same typed forms when opened. Most rows are now one line, and the eye lands on the three or four that matter. See <a href="system.html#scannable">Scannable by default (it replaces Calm by default)</a>.'],
  ['before', 'Before: a tinted pill for everything', 'The prototype renders states, autonomy tiers, staged edits, trust, roles and plain facts with the same tinted pill. Only the hue changes, and the hues were picked per call site, so “approved execution”, “official” and “installed” are all as green as “Passed”. Nothing tells a state from a fact, and the page reads as confetti.'],
  ['outline', 'A · One quiet tag', 'Every static label keeps a tag, but a single neutral outline with no fill. Only labels that need a person (failed, needs you, an untrusted third party) take colour, on the text and the outline. It is the smallest change, and calmer, but still a row of boxes, and states, tiers and facts still look alike.'],
  ['typed', 'B · Typed labels (the vocabulary, every label shown)', 'Chosen as the vocabulary, and shown calm by default (above). Each job gets exactly one form, and pills are kept only for things you can click (filters):<br><b>State</b> is the shape mark from the status column plus a word; it is coloured only when someone is needed or it is live. <b>Tier</b> is a four-step ladder plus the plain risk words. A <b>staged change</b> is a dashed ring and “not applied”. <b>Trust</b> is an icon plus a word, in warning colour only for untrusted third parties. A <b>count</b> is a thin meter and “41 of 100”. A <b>fact</b> is “Key value” text. A <b>role</b> is plain text.'],
  ['quiet', 'C · Quiet metadata', 'No labels at all: one state mark, then everything else as a single line of metadata in words (“lead of team platform · 41 of 100 turns · sandbox: namespace”). It is the calmest and reads well, but the facts can’t be scanned down a column, and it is long on a phone.'],
];

document.getElementById('variants').innerHTML = VARIANTS.map(([id, title, why]) => `<section class="var" id="${id}"><h2>${title}</h2><p class="why-p">${why}</p>
  <div class="pair">${['light', 'dark'].map((t) => `<div class="panel v-${id}" data-theme="${t}"><span class="tag">${t}</span><div class="surfs">${SURFACES.map((s) => surface(id, s)).join('')}</div></div>`).join('')}</div></section>`).join('');

// The recommended system, as a reference table: every label job, its form, and its colour rule.
const JOBS = [
  ['State', 'ok, passed, installed, works, running, live, waiting, needs you, pending, failed, refused, flagged as wrong, parked, idle, shadow, not tested', ['needs', 'failed', 'running', 'waiting', 'ok', 'parked', 'shadow'], 'Mark + word, from <code>status/marks.js</code>: the same marks in tables, cards, lanes, legends, the overview, the run page and phones.', 'The mark always carries its state colour: passed a quiet sage dot, parked slate pause bars (never the same hue or shape), running blue arc, failed red cross, needs you ember diamond, waiting amber triangle, shadow violet half disc. The word is coloured only when a person is needed or the thing is live.'],
  ['Autonomy tier', 'observe, reversible, propose, commit_money, never', null, 'Four-step ladder + plain risk words: Reads only · Acts, undoable · Asks you first · Spends money · Never.', 'One hue (ink), filled by level. The tier name appears on hover and in docs, never as the label.'],
  ['Staged change', 'staged, “new · staged”, move staged, change staged, uses repointed', ['staged'], 'Dashed ring + “New / Changed / Move, not applied”.', 'Running blue on the ring only: it is pending, not wrong.'],
  ['Trust', '“official · trusted by default”, official registry, built in, “local · yours”, “third party · pack_trust”, trusted third party', null, 'Icon + word: shield Official, folder Yours, globe Third party.', 'Ink. Warning colour only for an untrusted third party.'],
  ['Count', '41/100 turns, usage, quotas', null, 'Thin meter + “41 of 100 turns”.', 'Ink meter; amber over 80%, red at the limit.'],
  ['Fact', 'namespace, read-only, writable, approved execution, hot reload, pinned, device-bound, anonymous, restart, default, plain text, free paths', null, '“Key value” text: Sandbox namespace · Version pinned · Applies on restart.', 'Secondary ink. Never coloured: a fact is not a state.'],
  ['Role', 'lead, you, this session, team X, “team X · lead”, solo, no team, worker X, operators', null, 'Plain text in the metadata line: “Lead of team platform · Operators: ops”.', 'Ink. “You” and “this session” get the accent dot: the accent means you.'],
  ['Filter', 'status, folder and workflow filters', null, 'Pill chip: the only pill left, because it is the only label you can click.', 'Ink outline; selected = accent outline and wash.'],
];
document.getElementById('jobs').innerHTML = `<table class="jobs"><thead><tr><th>Job</th><th>Prototype labels it replaces</th><th>Form</th><th>Colour</th></tr></thead><tbody>${JOBS.map(([j, labels, shapes, form, col]) => `<tr><td><b>${j}</b>${shapes ? `<div class="jshapes">${shapes.map((k) => shape(k)).join('')}</div>` : ''}</td><td class="lbls">${labels}</td><td>${form}</td><td>${col}</td></tr>`).join('')}</tbody></table>`;
