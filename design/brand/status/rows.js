// The row vocabulary (current): a visual language that says what each thing is, where it happens
// and how it went, before anyone reads a word, and flags the few things worth stopping for.
//   KINDS    one icon per kind of object or event (approval, run, bot, PR, incident, Terraform…)
//   ACTIONS  one icon per kind of action (reads, writes, posts, pushes code, applies infra, spends…)
//   FLAGS    risk, shown only where a person decides (approvals, proposals, bot permissions) and only
//            for production, money and destructive or access changes: plain words and one mark
//   ENV      production / staging tags, told apart by fill, never by colour alone
// and one row anatomy: kind · title · where · status · time · action.
// Icons are Lucide (ISC, lucide-static 1.59.0, 24 grid, 1.75 stroke); the environment glyphs are ours.
import { LUCIDE } from '../icons/icons.js';
import { mark, status, STATES } from './marks.js';

// ---------- object and event kinds ----------
export const KINDS = {
  approval:   { icon: 'stamp', word: 'Approval', means: 'A request waiting for a person to approve or deny.' },
  run:        { icon: 'circle-play', word: 'Run', means: 'One run of a workflow. Failed, running and finished are the status mark beside it, never a different icon.' },
  bot:        { icon: 'bot', word: 'Bot', means: 'A standing agent with its own permissions, memory and cases.' },
  team:       { icon: 'users', word: 'Team', means: 'A group of bots and operators with shared rules.' },
  workflow:   { icon: 'workflow', word: 'Workflow', means: 'A fixed recipe: when something happens, do these steps.' },
  pr:         { icon: 'git-pull-request', word: 'Pull request', means: 'A pull request on a repository.' },
  incident:   { icon: 'siren', word: 'Incident', means: 'A page or incident, from PagerDuty or an alert.' },
  deploy:     { icon: 'rocket', word: 'Deploy', means: 'A release rolling out to an environment.' },
  infra:      { icon: 'layers', word: 'Terraform', means: 'An infrastructure plan or apply.' },
  spend:      { icon: 'receipt', word: 'Cloud spend', means: 'A bill or a purchase: savings plans, reserved instances, budgets.' },
  connector:  { icon: 'plug', word: 'Connection', means: 'A configured instance of an outside service (your GitHub, Slack, AWS…).' },
  credential: { icon: 'key-round', word: 'Login', means: 'A login, credential or secret.' },
  knowledge:  { icon: 'book-open', word: 'Knowledge', means: 'Memory, notes and runbooks a bot keeps.' },
  config:     { icon: 'settings-2', word: 'Config change', means: 'A change to conductor’s own configuration.' },
};
// ---------- kinds of action (what a verb does to the world) ----------
export const ACTIONS = {
  read:    { icon: 'eye', word: 'Reads', means: 'Looks only: logs, diffs, metrics, plans.' },
  write:   { icon: 'pencil-line', word: 'Writes / updates', means: 'Changes a record: acks a page, sets a key, labels an issue, saves a note.' },
  post:    { icon: 'message-square', word: 'Comments / posts', means: 'Writes where people read it: a PR comment, a Slack message.' },
  push:    { icon: 'git-commit-horizontal', word: 'Pushes code', means: 'Commits to a branch or a pull request.' },
  apply:   { icon: 'hammer', word: 'Applies infra', means: 'Applies an infrastructure change or a deploy.' },
  spend:   { icon: 'banknote', word: 'Spends money', means: 'Commits real money: purchases, reservations.' },
  destroy: { icon: 'trash-2', word: 'Deletes / destroys', means: 'Removes something that may not come back.' },
  perms:   { icon: 'lock-open', word: 'Changes permissions', means: 'Grants or widens access for a person, bot or role.' },
};
// ---------- workflow step kinds (builder only; flow, not actions on the world) ----------
export const STEPS = {
  agent:    { icon: 'brain-circuit', word: 'AI agent', means: 'An agent with a prompt, in the sandbox.' },
  command:  { icon: 'square-terminal', word: 'Command', means: 'A shell command in the sandbox.' },
  decide:   { icon: 'split', word: 'Decision', means: 'Picks a branch: typed questions with probabilities.' },
  wait:     { icon: 'hourglass', word: 'Wait', means: 'Waits for a time or a condition.' },
};

// ---------- risk flags ----------
// Most verbs need no risk label: the action icon and its word already say what it does. Three things
// are worth stopping for, and only where someone decides. Each is plain words with one mark that
// means the same thing everywhere else (the production glyph, the money and the trash icons).
// Never a meter or bars: bars read as signal strength, not danger.
export const FLAGS = {
  prod:    { word: 'Changes production', mark: 'production', means: 'Any change (not a read) to a production environment.' },
  money:   { word: 'Spends money', mark: 'banknote', means: 'A commit_money verb. The words carry the amount.' },
  destroy: { word: 'Destroys', mark: 'trash-2', means: 'Deletes or destroys something stateful, at any tier.' },
  access:  { word: 'Grants access', mark: 'lock-open', means: 'Changes who may do what: IAM, collaborators, grants.' },
};
/** Which flag a verb earns, if any: destroy and access first, then money, then production. */
export function flagOf({ action = 'read', tier = 'propose', env = '' }) {
  if (action === 'destroy') return 'destroy';
  if (action === 'perms') return 'access';
  if (tier === 'commit_money' || action === 'spend') return 'money';
  if (env === 'production' && action !== 'read' && tier !== 'observe') return 'prod';
  return null;
}
export const flag = (k, word = FLAGS[k].word) => `<span class="flag" data-flag="${k}">${k === 'prod' ? envGlyph('production') : lucide(FLAGS[k].mark)}<span>${word}</span></span>`;

// ---------- environments ----------
export const ENV = {
  production: { word: 'Production', means: 'Real users and real data. A solid square; the tag has a strong outline and bold type.' },
  staging:    { word: 'Staging', means: 'A copy for testing. An outlined square with a centre; plain outline.' },
  local:      { word: 'Local', means: 'This machine or a sandbox. A dashed circle; dashed outline.' },
};
// Production is solid, staging an outline with a centre, local a dashed circle: told apart by fill and shape.
const envGlyph = (e) => `<svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true">${e === 'production' ? '<rect x=".5" y=".5" width="9" height="9" rx="1.8" fill="currentColor"/>' : e === 'staging' ? '<rect x="1" y="1" width="8" height="8" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.5"/><rect x="3.6" y="3.6" width="2.8" height="2.8" rx=".4" fill="currentColor"/>' : '<circle cx="5" cy="5" r="3.9" fill="none" stroke="currentColor" stroke-width="1.5" stroke-dasharray="2 1.6"/>'}</svg>`;
export const env = (e) => (e ? `<span class="env" data-env="${e}">${envGlyph(e)}${ENV[e].word}</span>` : '');

// ---------- icons ----------
export const lucide = (name, size = 16) => `<svg class="ki" viewBox="0 0 24 24" width="${size}" height="${size}" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE[name]}</svg>`;
export const kind = (k, { word = true, size = 16 } = {}) => `<span class="kind">${lucide(KINDS[k].icon, size)}${word ? `<span>${KINDS[k].word}</span>` : ''}</span>`;
export const act = (a, word = ACTIONS[a].word) => `<span class="actn">${lucide(ACTIONS[a].icon)}<span>${word}</span></span>`;

// ---------- the row anatomy ----------
// kind (icon + word) · title (+ one plain sentence, after the flag if any) · where (bot, target, environment) · status · time · action
export const COLS = ['What', 'Title', 'Where', 'Status', 'When', ''];
const STATE_EDGE = { needs: 'accent', failed: 'danger' };
export function row(it) {
  // Flags only on things you decide (approvals); runs and finished work carry none.
  // Production isn't flagged on a row: its Production tag already says so.
  const f0 = it.kind === 'approval' && it.state === 'needs' ? flagOf(it) : null, f = f0 === 'prod' ? null : f0;
  const st = it.state ?? 'passed';
  return `<div class="srow g-${it.group ?? 'fine'}" role="row" data-item data-s="${STATE_EDGE[st] ?? ''}"${it.id ? ` title="${it.id}"` : ''}>
    <span class="c-kind" role="cell">${kind(it.kind)}</span>
    <span class="c-title" role="cell"><b>${it.title}</b>${f || it.why ? `<span class="c-why">${f ? flag(f, it.flagWord) : ''}${it.why ?? ''}</span>` : ''}</span>
    <span class="c-ctx" role="cell"><span class="kw">${KINDS[it.kind].word}</span><i class="kw">·</i>${(it.ctx ?? []).map((c, i) => `<span${i ? '' : ' class="who"'}>${c}</span>`).join('<i>·</i>')}${env(it.env)}</span>
    <span class="c-st" role="cell">${status(st, it.stateWord)}</span>
    <span class="c-time" role="cell">${it.time ?? ''}</span>
    <span class="c-act" role="cell">${it.act ? `<button class="btn ${it.primary ? 'primary' : 'secondary'} sm" tabindex="-1">${it.act}</button>` : ''}</span></div>`;
}
const GROUP = { needs: 'Needs you', attn: 'Needs attention', failing: 'Failing', running: 'Running now', fine: 'Finished' };
export const rows = (groups, { head = true } = {}) => `<div class="srows" role="table">${head ? `<div class="srow shead" role="row">${COLS.map((c) => `<span role="columnheader">${c}</span>`).join('')}</div>` : ''}
  ${groups.map(([g, items, extra = '', count = items.length]) => `<div class="sgroup" role="rowgroup"><div class="sgh g-${g}"><b>${GROUP[g]}</b><span>${count}</span>${extra}</div>${items.map((it) => row({ ...it, group: g })).join('')}</div>`).join('')}</div>`;

export const SCAN_CSS = `
.ki { flex: none; display: inline-block; vertical-align: -3px; }
.kind, .actn { display: inline-flex; align-items: center; gap: 7px; color: var(--color-text-primary); white-space: nowrap; }
.kind > span, .actn > span { font-size: 13.5px; color: var(--color-text-secondary); }
/* risk flags: plain words and one mark; only destroys and grants access take colour */
.flag { display: inline-flex; align-items: center; gap: 5px; font-weight: 700; color: var(--color-text-primary); white-space: nowrap; }
.flag .ki { width: 15px; height: 15px; }
.flag[data-flag="destroy"], .flag[data-flag="access"] { color: var(--color-status-danger-fg); }
.c-why .flag { margin-right: 8px; }
/* environments: told apart by fill and outline */
.env { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; line-height: 1; padding: 3px 7px 3px 5px; border-radius: 5px; color: var(--color-text-secondary); border: 1px solid var(--color-border-default); white-space: nowrap; }
.env[data-env="production"] { color: var(--color-text-primary); font-weight: 700; border: 1.5px solid var(--color-text-primary); }
.env[data-env="local"] { border-style: dashed; }
/* the row anatomy: fixed columns, aligned */
.srows { --cols: 118px minmax(0, 1fr) minmax(160px, 240px) 112px 50px 80px; container-type: inline-size; }
.srow { display: grid; grid-template-columns: var(--cols); gap: 14px; align-items: center; min-height: 52px; padding: 7px 10px 7px 14px; border-top: 1px solid var(--color-border-subtle); }
.srow[data-s] { box-shadow: inset 3px 0 0 var(--s-solid, transparent); }
.srow.shead { min-height: 0; padding-top: 0; padding-bottom: 6px; border-top: 0; font-size: 12px; font-weight: 600; color: var(--color-text-tertiary); }
.srow > span { min-width: 0; }
.c-title { display: grid; gap: 1px; }
.c-title b { font-weight: 600; color: var(--color-text-primary); line-height: 1.3; }
.c-title .c-why { font-size: 13.5px; color: var(--color-text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.c-ctx { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 6px; font-size: 13px; color: var(--color-text-secondary); }
.c-ctx .who { color: var(--color-text-primary); font-weight: 600; }
.c-ctx i { font-style: normal; color: var(--color-text-tertiary); }
.c-ctx .kw { display: none; }
.c-time { font-size: 13px; color: var(--color-text-tertiary); text-align: right; white-space: nowrap; }
.c-act { text-align: right; }
.sgroup + .sgroup { margin-top: 18px; }
.sgh { display: flex; align-items: baseline; gap: 8px; padding: 0 0 8px 2px; border-bottom: 1px solid var(--color-border-default); }
.sgh b { font-size: 15px; font-weight: 700; color: var(--color-text-primary); }
.sgh span { font-size: 13px; color: var(--color-text-tertiary); }
.sgh em { margin-left: auto; font-style: normal; font-size: 13px; color: var(--color-text-tertiary); }
/* attention order by weight: needs you and failing are bold, finished titles step back to regular */
.srow.g-running .c-title b { font-weight: 500; }
.srow.g-fine .c-title b { font-weight: 400; }
/* The phone layout applies wherever the list is narrow (a phone, a side panel, a book column). */
@container (max-width: 640px) {
  .srow.shead { display: none; }
  .srow { grid-template-columns: 22px minmax(0, 1fr) auto; grid-template-areas: "k t time" "k m m" "k a a"; gap: 4px 10px; align-items: start; padding: 10px 6px 10px 12px; }
  .srow .c-kind { grid-area: k; padding-top: 2px; } .srow .c-kind .kind > span { display: none; }
  .srow .c-title { grid-area: t; } .srow .c-title b, .srow .c-title .c-why { white-space: normal; }
  .srow .c-time { grid-area: time; padding-top: 2px; }
  .srow .c-ctx { grid-area: m; } .srow .c-ctx .kw { display: inline; } .srow .c-ctx span.kw { color: var(--color-text-primary); }
  .srow .c-st { grid-area: a; }
  .srow .c-act { display: none; }
}`;
export const injectScanCSS = () => { if (document.getElementById('scan-css')) return; const s = document.createElement('style'); s.id = 'scan-css'; s.textContent = SCAN_CSS; document.head.append(s); };
export { mark, status, STATES };
