// The status vocabulary (design §3.2): a 12px mark plus a word, no capsule.
// Each state is a distinct shape so it reads in one colour. The mark always
// carries its state colour (--mk, styles/marks.css); the word is coloured for
// live/needs-you/passed states and bold when a person is needed. Screens never
// draw status markup by hand — they use this component.
//
// Ported verbatim from the prototype: the state → glyph table is ui.js's SG
// (UI.mark / UI.statusMark) and the shapes are icons.js's GLYPHS (Daylight's
// own status marks): passed a dot, waiting a triangle, failed a cross, parked
// pause bars, needs you a diamond, running an open arc, and so on.
import { h } from 'preact'
import type { JSX } from 'preact'

// state → [glyph, default word] (ui.js SG)
const SG: Record<string, [string, string]> = {
  ok: ['success', 'Passed'],
  success: ['success', 'Passed'],
  run: ['running', 'Running'],
  wait: ['warning', 'Waiting'],
  fail: ['danger', 'Failed'],
  idle: ['neutral', 'Parked'],
  shadow: ['shadow', 'Shadow'],
  ember: ['needs-you', 'Needs you'],
  skip: ['skipped', 'Not reached'],
  staged: ['staged', 'Not applied'],
  ready: ['ready', 'Ready'],
  progress: ['in-progress', 'In progress'],
  draft: ['draft', 'Draft'],
}

// icons.js GLYPHS, 16-unit grid, as VNodes: the dashboard's CSP (trusted
// types) forbids innerHTML, so the shapes are built with h(), never a string.
const GLYPHS: Record<string, [string, Record<string, string>][]> = {
  'success': [['circle', { cx: '8', cy: '8', r: '3.4', fill: 'currentColor', stroke: 'none' }]],
  'warning': [['path', { d: 'M8 2.6l5.9 10.4H2.1z', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.7', 'stroke-linejoin': 'round' }], ['circle', { cx: '8', cy: '10.4', r: '1.1', fill: 'currentColor' }]],
  'danger': [['path', { d: 'M4.4 4.4l7.2 7.2M11.6 4.4l-7.2 7.2', fill: 'none', stroke: 'currentColor', 'stroke-width': '2.6', 'stroke-linecap': 'round' }]],
  'info': [['circle', { cx: '8', cy: '8', r: '5.6', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.6' }], ['path', { d: 'M8 7.3v3.9', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.8', 'stroke-linecap': 'round' }], ['circle', { cx: '8', cy: '5.1', r: '1', fill: 'currentColor', stroke: 'none' }]],
  'running': [['circle', { cx: '8', cy: '8', r: '5', fill: 'none', stroke: 'currentColor', 'stroke-width': '2.2', 'stroke-dasharray': '22 9.5', 'stroke-linecap': 'round' }]],
  'neutral': [['path', { d: 'M5.7 3.8v8.4M10.3 3.8v8.4', fill: 'none', stroke: 'currentColor', 'stroke-width': '2.7', 'stroke-linecap': 'round' }]],
  'skipped': [['path', { d: 'M3.6 8h8.8', fill: 'none', stroke: 'currentColor', 'stroke-width': '2.6', 'stroke-linecap': 'round' }]],
  'shadow': [['circle', { cx: '8', cy: '8', r: '5.2', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.7' }], ['path', { d: 'M8 2.8a5.2 5.2 0 0 1 0 10.4z', fill: 'currentColor', stroke: 'none' }]],
  'needs-you': [['path', { d: 'M8 1.9l6.1 6.1L8 14.1 1.9 8z', fill: 'currentColor', stroke: 'none' }]],
  'ready': [['circle', { cx: '8', cy: '8', r: '3.6', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.8' }]],
  'draft': [['rect', { x: '2.2', y: '4.9', width: '11.6', height: '6.2', rx: '3.1', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.7', 'stroke-dasharray': '2.2 1.8' }]],
  'in-progress': [['rect', { x: '2.2', y: '4.9', width: '11.6', height: '6.2', rx: '3.1', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.6' }], ['rect', { x: '3.8', y: '6.5', width: '5', height: '3', rx: '1.5', fill: 'currentColor' }]],
  'staged': [['circle', { cx: '8', cy: '8', r: '5', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.8', 'stroke-dasharray': '2.6 1.9' }]],
}

/** The glyph a state's mark draws (UI.markGlyph). */
export function markGlyph(s: string): string {
  return (SG[s] || SG.idle)[0]
}

/** A Daylight status glyph by name (UI.glyph). */
export function Glyph({ n, cls = '' }: { n: string; cls?: string }): JSX.Element {
  return (
    <svg
      class={`glyph ${cls}`}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      {(GLYPHS[n] || []).map(([tag, attrs]) => h(tag, attrs))}
    </svg>
  )
}

export function StatusMark({
  s,
  word,
  lg,
  sm,
  mkOnly,
  label,
  cls,
  tip,
}: {
  s: string
  // the small mark (UI.statusMark's o.sm), an extra class (o.cls: tpill,
  // val-pill, wf-state…) and a tooltip (o.tip)
  sm?: boolean
  cls?: string
  tip?: string
  word?: string
  lg?: boolean
  mkOnly?: boolean
  // The mark alone still needs a name for assistive tech (UI.mark's o.label).
  label?: string
}): JSX.Element {
  const klass = ['st', sm ? 'sm' : '', lg ? 'lg' : '', mkOnly ? 'mk-only' : '', cls || ''].filter(Boolean).join(' ')
  return (
    <span class={klass} data-s={s} data-tip={tip} role={mkOnly && label ? 'img' : undefined} aria-label={mkOnly && label ? label : undefined}>
      <i>
        <Glyph n={markGlyph(s)} />
      </i>
      {!mkOnly && word ? <span>{word}</span> : null}
    </span>
  )
}
