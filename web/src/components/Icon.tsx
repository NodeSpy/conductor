// The nav and chrome icon set: a small Lucide subset (1.75 stroke), ported
// verbatim from the prototype's js/icons.js (UI.icon) so the rail, top bar and
// Needs you facts read exactly as approved.
//
// The dashboard's CSP sets `require-trusted-types-for 'script'` (internal/
// dashboard, see also lib/avatar.ts's note), which forbids assigning innerHTML
// — so every glyph is a real Preact VNode built with h(...), never an HTML
// string through dangerouslySetInnerHTML. Add to SHAPES as later phases need
// more icons; never hand-draw a new glyph outside this file, and never go back
// to an HTML-string icon set (it renders as a blank page under the real CSP,
// even though it looks fine in a dev server with no CSP).
import { h } from 'preact'
import type { JSX } from 'preact'

const SHAPES: Record<string, [string, Record<string, string>][]> = {
  overview: [['rect', { width: '7', height: '7', x: '3', y: '3', rx: '1' }], ['rect', { width: '7', height: '7', x: '14', y: '3', rx: '1' }], ['rect', { width: '7', height: '7', x: '14', y: '14', rx: '1' }], ['rect', { width: '7', height: '7', x: '3', y: '14', rx: '1' }]],
  message: [['path', { d: 'M22 17a2 2 0 0 1-2 2H6.828a2 2 0 0 0-1.414.586l-2.202 2.202A.71.71 0 0 1 2 21.286V5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2z' }]],
  attention: [['path', { d: 'M10.268 21a2 2 0 0 0 3.464 0' }], ['path', { d: 'M3.262 15.326A1 1 0 0 0 4 17h16a1 1 0 0 0 .74-1.673C19.41 13.956 18 12.499 18 8A6 6 0 0 0 6 8c0 4.499-1.411 5.956-2.738 7.326' }]],
  live: [['path', { d: 'M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2' }]],
  ticket: [['path', { d: 'M2 9a3 3 0 0 1 0 6v2a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-2a3 3 0 0 1 0-6V7a2 2 0 0 0-2-2H4a2 2 0 0 0-2 2Z' }], ['path', { d: 'M13 5v2' }], ['path', { d: 'M13 17v2' }], ['path', { d: 'M13 11v2' }]],
  workflows: [['rect', { width: '8', height: '8', x: '3', y: '3', rx: '2' }], ['path', { d: 'M7 11v4a2 2 0 0 0 2 2h4' }], ['rect', { width: '8', height: '8', x: '13', y: '13', rx: '2' }]],
  bots: [['path', { d: 'M12 8V4H8' }], ['rect', { width: '16', height: '12', x: '4', y: '8', rx: '2' }], ['path', { d: 'M2 14h2' }], ['path', { d: 'M20 14h2' }], ['path', { d: 'M15 13v2' }], ['path', { d: 'M9 13v2' }]],
  globe: [['circle', { cx: '12', cy: '12', r: '10' }], ['path', { d: 'M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20' }], ['path', { d: 'M2 12h20' }]],
  connectors: [['path', { d: 'M12 22v-5' }], ['path', { d: 'M15 8V2' }], ['path', { d: 'M17 8a1 1 0 0 1 1 1v4a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V9a1 1 0 0 1 1-1z' }], ['path', { d: 'M9 8V2' }]],
  extensions: [['path', { d: 'M11 21.73a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73z' }], ['path', { d: 'M12 22V12' }], ['polyline', { points: '3.29 7 12 12 20.71 7' }], ['path', { d: 'm7.5 4.27 9 5.15' }]],
  secrets: [['path', { d: 'M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z' }], ['circle', { cx: '16.5', cy: '7.5', r: '.5', fill: 'currentColor' }]],
  settings: [['path', { d: 'M14 17H5' }], ['path', { d: 'M19 7h-9' }], ['circle', { cx: '17', cy: '17', r: '3' }], ['circle', { cx: '7', cy: '7', r: '3' }]],
  search: [['path', { d: 'm21 21-4.34-4.34' }], ['circle', { cx: '11', cy: '11', r: '8' }]],
  sun: [['circle', { cx: '12', cy: '12', r: '4' }], ['path', { d: 'M12 2v2' }], ['path', { d: 'M12 20v2' }], ['path', { d: 'm4.93 4.93 1.41 1.41' }], ['path', { d: 'm17.66 17.66 1.41 1.41' }], ['path', { d: 'M2 12h2' }], ['path', { d: 'M20 12h2' }], ['path', { d: 'm6.34 17.66-1.41 1.41' }], ['path', { d: 'm19.07 4.93-1.41 1.41' }]],
  moon: [['path', { d: 'M20.985 12.486a9 9 0 1 1-9.473-9.472c.405-.022.617.46.402.803a6 6 0 0 0 8.268 8.268c.344-.215.825-.004.803.401' }]],
  check: [['path', { d: 'M20 6 9 17l-5-5' }]],
  x: [['path', { d: 'M18 6 6 18' }], ['path', { d: 'm6 6 12 12' }]],
  pause: [['rect', { x: '14', y: '3', width: '5', height: '18', rx: '1' }], ['rect', { x: '5', y: '3', width: '5', height: '18', rx: '1' }]],
  chev: [['path', { d: 'm9 18 6-6-6-6' }]],
  hand: [['path', { d: 'M18 11V6a2 2 0 0 0-2-2a2 2 0 0 0-2 2' }], ['path', { d: 'M14 10V4a2 2 0 0 0-2-2a2 2 0 0 0-2 2v2' }], ['path', { d: 'M10 10.5V6a2 2 0 0 0-2-2a2 2 0 0 0-2 2v8' }], ['path', { d: 'M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.86-5.99-2.34l-3.6-3.6a2 2 0 0 1 2.83-2.82L7 15' }]],
  more: [['circle', { cx: '12', cy: '12', r: '1' }], ['circle', { cx: '19', cy: '12', r: '1' }], ['circle', { cx: '5', cy: '12', r: '1' }]],
  'obj-connector': [['path', { d: 'M12 22v-5' }], ['path', { d: 'M15 8V2' }], ['path', { d: 'M17 8a1 1 0 0 1 1 1v4a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V9a1 1 0 0 1 1-1z' }], ['path', { d: 'M9 8V2' }]],
  'obj-update': [['path', { d: 'M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8' }], ['path', { d: 'M21 3v5h-5' }], ['path', { d: 'M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16' }], ['path', { d: 'M8 16H3v5' }]],
  'act-money': [['rect', { width: '20', height: '12', x: '2', y: '6', rx: '2' }], ['circle', { cx: '12', cy: '12', r: '2' }], ['path', { d: 'M6 12h.01M18 12h.01' }]],
  login: [['path', { d: 'm10 17 5-5-5-5' }], ['path', { d: 'M15 12H3' }], ['path', { d: 'M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4' }]],
  'obj-bot': [['path', { d: 'M12 8V4H8' }], ['rect', { width: '16', height: '12', x: '4', y: '8', rx: '2' }], ['path', { d: 'M2 14h2' }], ['path', { d: 'M20 14h2' }], ['path', { d: 'M15 13v2' }], ['path', { d: 'M9 13v2' }]],
  'obj-workflow': [['rect', { width: '8', height: '8', x: '3', y: '3', rx: '2' }], ['path', { d: 'M7 11v4a2 2 0 0 0 2 2h4' }], ['rect', { width: '8', height: '8', x: '13', y: '13', rx: '2' }]],
}

export function Icon({ name, class: cls = '' }: { name: string; class?: string }): JSX.Element {
  const shape = SHAPES[name] || SHAPES.more
  return h(
    'svg',
    {
      class: cls,
      viewBox: '0 0 24 24',
      fill: 'none',
      stroke: 'currentColor',
      'stroke-width': '1.75',
      'stroke-linecap': 'round',
      'stroke-linejoin': 'round',
      'aria-hidden': 'true',
    },
    shape.map(([tag, attrs]) => h(tag, attrs)),
  )
}
