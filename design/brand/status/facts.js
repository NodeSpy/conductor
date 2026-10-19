// Fact types for the facts line: the third line of a list row, a few facts a person scans for, each an
// icon and a value. Every fact has its own icon, so facts are told apart by shape first. Colour only
// where a fact needs attention:
//   update available   the accent, bold: the most noticeable fact in a row (the version stays quiet)
//   custom source      a muted olive-ochre (fact.custom): its trust differs from built in and official
//   risk, graded by what it can do (where a person decides: approvals, proposals, permissions):
//     0 reads, reversible               quiet, outlined icon
//     1 changes something               full ink at 600, outlined icon (quiet caution)
//     2 infrastructure, production      warning amber, bold, its own heavier icon
//     3 spends money, destroys, access  danger red, bold, a solid tile icon
// Held is full ink (you chose it: visible, not alarming); everything else is quiet. Shape and colour,
// never colour alone: each grade changes the icon's weight or form as well as the tone.
// Icons are Lucide (ISC); service glyphs are Simple Icons (CC0; the marks are their owners').
import { LUCIDE, SERVICES } from '../icons/icons.js';

export const FACT_TYPES = {
  // How it updates: one per row.
  update:   { group: 'Updates', icon: 'circle-arrow-up', tone: 'update', word: (v) => `${v} available`, means: 'A newer version can be installed. The most noticeable fact in the row; links to the update.' },
  held:     { group: 'Updates', icon: 'pin', tone: 'held', word: (v) => `Held at ${v}`, means: 'Pinned to a version by a person. Full ink: visible, not alarming.' },
  current:  { group: 'Updates', icon: 'refresh-cw', tone: 'quiet', word: () => 'Stays current', means: 'Updates itself to each new version.' },
  range:    { group: 'Updates', icon: 'brackets', tone: 'quiet', word: (v) => `Updates within ${v}`, means: 'Updates itself within a version range.' },
  version:  { group: 'Updates', icon: 'tag', tone: 'quiet', word: (v) => v, means: 'The installed version. Always quiet, even when an update is available.' },
  // Where it comes from: one per row.
  builtin:  { group: 'Origin', icon: 'conductor', tone: 'quiet', word: () => 'Built in', means: 'Ships with conductor. The conductor mark, its 16px cut, in quiet ink.' },
  official: { group: 'Origin', icon: 'badge-check', tone: 'quiet', word: () => 'Official', means: 'Published and verified by conductor’s maintainers.' },
  custom:   { group: 'Origin', icon: 'git-fork', tone: 'custom', word: (v) => (v ? `Custom · ${v}` : 'Custom source'), means: 'Installed from a third-party or custom source. Its trust differs, so it is toned.' },
  // What and where.
  bot:      { group: 'What and where', icon: 'bot', tone: 'quiet', word: (v) => v, means: 'The bot; a link to it.' },
  workflow: { group: 'What and where', icon: 'workflow', tone: 'quiet', word: (v) => v, means: 'The workflow; a link to it.' },
  pr:       { group: 'What and where', icon: 'git-pull-request', tone: 'quiet', word: (v) => v, means: 'A pull request; a link to it.' },
  channel:  { group: 'What and where', icon: 'message-square', tone: 'quiet', word: (v) => v, means: 'A chat channel.' },
  site:     { group: 'What and where', icon: 'globe', tone: 'quiet', word: (v) => v, means: 'A website a browser works on.' },
  service:  { group: 'What and where', icon: 'plug', tone: 'quiet', word: (v) => v, means: 'A connection: the service’s own glyph and name. The plug only when the service has no glyph.' },
  runtime:  { group: 'What and where', icon: 'terminal', tone: 'quiet', word: (v) => v, means: 'An agent runtime.' },
  language: { group: 'What and where', icon: 'code', tone: 'quiet', word: (v) => v, means: 'A code language.' },
  machine:  { group: 'What and where', icon: 'server', tone: 'quiet', word: (v) => v, means: 'A machine conductor runs work on.' },
  model:    { group: 'What and where', icon: 'cpu', tone: 'quiet', word: (v) => v, means: 'The model a bot uses. Drops first on a phone.' },
  // Risk, graded. Shown where a person decides; the strongest first, at most two per row.
  reads:    { group: 'Risk', grade: 0, icon: 'eye', tone: 'quiet', word: () => 'Reads only', means: 'Looks, changes nothing.' },
  undo:     { group: 'Risk', grade: 0, icon: 'undo-2', tone: 'quiet', word: (v) => v || 'Can be undone', means: 'A change with a one-step undo.' },
  writes:   { group: 'Risk', grade: 1, icon: 'pencil-line', tone: 'change', word: (v) => v || 'Changes files', means: 'Writes or updates something.' },
  posts:    { group: 'Risk', grade: 1, icon: 'message-square', tone: 'change', word: (v) => v || 'Posts a message', means: 'Writes where people read it.' },
  pushes:   { group: 'Risk', grade: 1, icon: 'git-commit-horizontal', tone: 'change', word: (v) => v || 'Pushes code', means: 'Pushes commits to a repository.' },
  infra:    { group: 'Risk', grade: 2, icon: 'hammer', tone: 'warning', word: (v) => v || 'Changes infrastructure', means: 'Applies infrastructure (Terraform, Kubernetes, cloud resources).' },
  production: { group: 'Risk', grade: 2, icon: 'production', tone: 'warning', word: () => 'Production', means: 'Production, after the thing it changes: the solid production square, never plain text.' },
  money:    { group: 'Risk', grade: 3, icon: 'banknote', tone: 'danger', word: (v) => v || 'Spends money', means: 'Commits money; the words carry the amount (“Spends $1,800”).' },
  destroy:  { group: 'Risk', grade: 3, icon: 'trash-2', tone: 'danger', word: (v) => v || 'Destroys', means: 'Deletes or destroys something stateful.' },
  access:   { group: 'Risk', grade: 3, icon: 'lock-open', tone: 'danger', word: (v) => v || 'Widens access', means: 'Changes who may do what so that someone can do more.' },
};
export const RISK_GRADES = [
  [0, 'Reads and reversible', 'Quiet: the value in quiet ink, an outlined icon.'],
  [1, 'Changes something', 'Quiet caution: full ink at 600, the action’s outlined icon.'],
  [2, 'Infrastructure and production', 'Warning: amber, bold, its own heavier icon (the hammer, the solid production square).'],
  [3, 'Spends money, destroys, widens access', 'Danger: red, bold, the icon knocked out of a solid red tile.'],
];

// The conductor mark's 16px cut (assets/favicon16-mono.svg), unaltered, for “Built in” only.
const MARK16 = '<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" class="fx-mark"><path fill="currentColor" d="M1 11a7 8 0 0 1 14 0h-3a4 4.8 0 0 0-8 0z"/><circle cx="8" cy="11" r="2.6" fill="currentColor"/></svg>';
const PROD = '<svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true"><rect x=".5" y=".5" width="9" height="9" rx="1.8" fill="currentColor"/></svg>';
const lucide = (n) => `<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${LUCIDE[n]}</svg>`;
/** A service glyph, filled, in the icon colour (never the brand's own colour). Falls back to the plug. */
export const serviceGlyph = (id) => (SERVICES[id] ? `<svg viewBox="0 0 24 24" width="13" height="13" aria-hidden="true" class="fx-svc">${SERVICES[id].svg.replace('<path ', '<path fill="currentColor" ')}</svg>` : lucide('plug'));
export const hasGlyph = (id) => Boolean(SERVICES[id]);

/** One fact: `fact('update', '2.5.0')`, `fact('service', 'GitHub', { glyph: 'github' })`. */
export function fact(type, value = '', { glyph = '', link = false } = {}) {
  const t = FACT_TYPES[type];
  const icon = type === 'builtin' ? MARK16 : type === 'production' ? PROD : type === 'service' ? serviceGlyph(glyph)
    : t.grade === 3 ? `<span class="fx-tile">${lucide(t.icon)}</span>` : lucide(t.icon);
  return `<span class="fx" data-fact="${type}" data-tone="${t.tone}"${t.grade != null ? ` data-grade="${t.grade}"` : ''}${link ? ' data-link' : ''}>${icon}<span>${t.word(value)}</span></span>`;
}
export const facts = (list) => `<span class="fx-line">${list.map((f) => fact(...f)).join('')}</span>`;

export const FACTS_CSS = `
.fx-line{display:flex;flex-wrap:wrap;align-items:center;gap:3px 14px;font-size:12.5px;line-height:1.35}
.fx{display:inline-flex;align-items:center;gap:5px;color:var(--color-fact-quiet);white-space:nowrap}
.fx svg{flex:none;color:var(--color-icon-default)}
span.fx>svg{width:13px;height:13px}span.fx>svg[viewBox="0 0 10 10"]{width:10px;height:10px}span.fx>svg.fx-mark{width:16px;height:16px}span.fx>.fx-tile>svg{width:10px;height:10px}
.fx .fx-mark{margin:-1.5px -1px}
.fx[data-link]>span{text-decoration:underline;text-decoration-color:var(--color-border-strong);text-underline-offset:3px;color:var(--color-text-primary)}
.fx[data-tone="held"]{color:var(--color-fact-held);font-weight:600}.fx[data-tone="held"] svg{color:var(--color-fact-held)}
.fx[data-tone="update"]{color:var(--color-fact-update);font-weight:700}.fx[data-tone="update"] svg{color:var(--color-fact-update)}
.fx[data-tone="custom"]{color:var(--color-fact-custom)}.fx[data-tone="custom"] svg{color:var(--color-fact-custom)}
.fx[data-tone="change"]{color:var(--color-fact-change);font-weight:600}.fx[data-tone="change"] svg{color:var(--color-fact-change)}
.fx[data-tone="warning"]{color:var(--color-fact-warning);font-weight:700}.fx[data-tone="warning"] svg{color:var(--color-fact-warning);stroke-width:2.6}
.fx[data-tone="danger"]{color:var(--color-fact-danger);font-weight:700}
.fx-tile{display:inline-grid;place-items:center;width:15px;height:15px;border-radius:4px;background:var(--color-fact-danger-tile);flex:none}
.fx-tile svg{width:10px;height:10px;color:var(--color-fact-danger-glyph)!important;stroke-width:2.6}
`;
export const injectFactsCSS = () => { if (document.getElementById('facts-css')) return; const st = document.createElement('style'); st.id = 'facts-css'; st.textContent = FACTS_CSS; document.head.append(st); };
