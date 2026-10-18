// The one status vocabulary. Every status mark on every page, chart, table, card and mock comes
// from here, so a state has the same shape, colour and word everywhere. tools/mark-check.mjs
// verifies that every rendered [data-mark] matches its token and appears in its chart's legend.
//
// Colour rules: the mark always carries its state colour. Passed is green, mark and word, in regular
// weight; the states that need a person (failed, needs you, waiting) are bold, so on a mostly-green
// screen the rare failure still comes first. Parked and not-reached words stay ink. Passed is a small
// green dot; parked is slate pause bars: never the same hue or shape.
import { STATUS } from '../icons/icons.js';

export const STATES = {
  passed:  { word: 'Passed',    glyph: 'success',   mark: '--color-status-success-solid', fg: '--color-status-success-fg',    shape: 'filled dot' },
  running: { word: 'Running',   glyph: 'running',   mark: '--color-status-running-solid', fg: '--color-status-running-fg',    shape: 'open arc', spin: true },
  failed:  { word: 'Failed',    glyph: 'danger',    mark: '--color-status-danger-solid',  fg: '--color-status-danger-fg',     shape: 'cross', attn: true },
  needs:   { word: 'Needs you', glyph: 'needs-you', mark: '--color-accent-default',       fg: '--color-text-accent',          shape: 'diamond', attn: true },
  waiting: { word: 'Waiting',   glyph: 'warning',   mark: '--color-status-warning-solid', fg: '--color-status-warning-fg',    shape: 'outlined triangle', attn: true },
  parked:  { word: 'Parked',    glyph: 'neutral',   mark: '--color-status-neutral-solid', fg: null,                           shape: 'pause bars' },
  shadow:  { word: 'Shadow',    glyph: 'shadow',    mark: '--color-status-info-solid',    fg: '--color-status-info-fg',       shape: 'half disc' },
  // A step after a failure, or a cancelled run: the parked slate, but a single dash.
  skipped: { word: 'Not reached', glyph: 'skipped', mark: '--color-status-neutral-solid', fg: null,                           shape: 'dash' },
};
// Not a status: an edit that hasn't been applied. Running blue, but a dashed ring, never an arc.
// Glyph keys used by older helpers → their state.
export const GLYPH_STATE = { success: 'passed', danger: 'failed', warning: 'waiting', running: 'running', neutral: 'parked', skipped: 'skipped', shadow: 'shadow', 'needs-you': 'needs', staged: 'staged' };
export const CHANGE = { staged: { word: 'Not applied', glyph: 'staged', mark: '--color-status-running-solid', fg: '--color-status-running-fg', shape: 'dashed ring' } };
const ALL = { ...STATES, ...CHANGE };

const svgInner = (state) => STATUS[ALL[state].glyph].svg;
/** A mark as an inline <svg>. `size` in px. Running spins unless reduced motion. */
export const mark = (state, { size = 12, label = false } = {}) => {
  const S = ALL[state];
  if (!S) throw new Error(`unknown status mark: ${state}`);
  return `<svg class="mk${S.spin ? ' mk-spin' : ''}" data-mark="${state}" viewBox="0 0 16 16" width="${size}" height="${size}" style="color:var(${S.mark})" ${label ? `role="img" aria-label="${S.word}"` : 'aria-hidden="true"'}>${svgInner(state)}</svg>`;
};
/** A mark placed inside a chart <svg> at (cx, cy). */
export const chartMark = (state, cx, cy, size = 13, title = '') => {
  const S = ALL[state];
  // A nested <svg> can't rotate about its own centre (its CSS transform applies in the parent's
  // coordinates and swings it off the chart), so a spinning mark spins an inner group instead.
  const inner = S.spin ? `<g class="mk-spin">${svgInner(state)}</g>` : svgInner(state);
  return `<svg x="${cx - size / 2}" y="${cy - size / 2}" width="${size}" height="${size}" viewBox="0 0 16 16" class="mk" data-mark="${state}" style="color:var(${S.mark})" overflow="visible">${title ? `<title>${title}</title>` : ''}${inner}</svg>`;
};
/** Mark + word, the status label used in tables, cards and headers. */
export const status = (state, word = ALL[state].word) => {
  const S = ALL[state];
  return `<span class="mk-status${S.fg ? ' live' : ''}${S.attn ? ' attn' : ''}" data-state="${state}"${S.fg ? ` style="color:var(${S.fg})"` : ''}>${mark(state)}<span>${word}</span></span>`;
};
/** A legend for a chart: lists exactly the states given, and declares them for mark-check. */
export const legend = (states) => `<div class="mk-legend" data-legend="${states.join(' ')}">${states.map((s) => `<span>${mark(s)}${ALL[s].word}</span>`).join('')}</div>`;
/** CSS every page using marks needs (inject once). */
export const MARK_CSS = `.mk{flex:none;display:inline-block;vertical-align:-1px}.mk-spin{animation:mk-spin var(--motion-duration-spin,1.2s) linear infinite;transform-origin:center;transform-box:fill-box}
g.mk-spin{transform-origin:8px 8px;transform-box:view-box}@keyframes mk-spin{to{transform:rotate(1turn)}}@media (prefers-reduced-motion:reduce){.mk-spin{animation:none}}[data-motion="reduced"] .mk-spin{animation:none}
.mk-status{display:inline-flex;align-items:center;gap:7px;white-space:nowrap;color:var(--color-text-secondary)}.mk-status.attn{font-weight:700}
.mk-legend{display:flex;flex-wrap:wrap;gap:6px 16px;font:var(--type-caption,400 12.5px/1.4 var(--font-family-sans,system-ui));color:var(--color-text-secondary)}.mk-legend span{display:inline-flex;align-items:center;gap:6px}`;
export const injectMarkCSS = () => { if (document.getElementById('mk-css')) return; const s = document.createElement('style'); s.id = 'mk-css'; s.textContent = MARK_CSS; document.head.append(s); };
