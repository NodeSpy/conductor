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
  plug: [['path',{d:'M12 22v-5'}],['path',{d:'M9 8V2'}],['path',{d:'M15 8V2'}],['path',{d:'M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z'}]],
  'badge-check': [['path',{d:'M3.85 8.62a4 4 0 0 1 4.78-4.77 4 4 0 0 1 6.74 0 4 4 0 0 1 4.78 4.78 4 4 0 0 1 0 6.74 4 4 0 0 1-4.77 4.78 4 4 0 0 1-6.75 0 4 4 0 0 1-4.78-4.77 4 4 0 0 1 0-6.76Z'}],['path',{d:'m16 9-5.5 5.5L8 12'}]],
  'git-fork': [['circle',{cx:'12',cy:'18',r:'3'}],['circle',{cx:'6',cy:'6',r:'3'}],['circle',{cx:'18',cy:'6',r:'3'}],['path',{d:'M18 9v2c0 .6-.4 1-1 1H7c-.6 0-1-.4-1-1V9'}],['path',{d:'M12 12v3'}]],
  'refresh-cw': [['path',{d:'M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8'}],['path',{d:'M21 3v5h-5'}],['path',{d:'M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16'}],['path',{d:'M8 16H3v5'}]],
  'circle-arrow-up': [['circle',{cx:'12',cy:'12',r:'10'}],['path',{d:'m16 12-4-4-4 4'}],['path',{d:'M12 16V8'}]],
  server: [['rect',{width:'20',height:'8',x:'2',y:'2',rx:'2',ry:'2'}],['rect',{width:'20',height:'8',x:'2',y:'14',rx:'2',ry:'2'}],['line',{x1:'6',x2:'6.01',y1:'6',y2:'6'}],['line',{x1:'6',x2:'6.01',y1:'18',y2:'18'}]],
  'obj-host': [['rect',{width:'20',height:'8',x:'2',y:'2',rx:'2',ry:'2'}],['rect',{width:'20',height:'8',x:'2',y:'14',rx:'2',ry:'2'}],['line',{x1:'6',x2:'6.01',y1:'6',y2:'6'}],['line',{x1:'6',x2:'6.01',y1:'18',y2:'18'}]],
  members: [['path',{d:'M12 8V4H8'}],['rect',{width:'16',height:'12',x:'4',y:'8',rx:'2'}],['path',{d:'M2 14h2'}],['path',{d:'M20 14h2'}],['path',{d:'M15 13v2'}],['path',{d:'M9 13v2'}]],
  'obj-sandbox': [['path',{d:'M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z'}],['path',{d:'m9 12 2 2 4-4'}]],
  plugtest: [['path',{d:'M6.3 20.3a2.4 2.4 0 0 0 3.4 0L12 18l-6-6-2.3 2.3a2.4 2.4 0 0 0 0 3.4Z'}],['path',{d:'m2 22 3-3'}],['path',{d:'M7.5 13.5 10 11'}],['path',{d:'M10.5 16.5 13 14'}],['path',{d:'m18 3-4 4h6l-4 4'}]],
  brackets: [['path',{d:'M16 3h3a1 1 0 0 1 1 1v16a1 1 0 0 1-1 1h-3'}],['path',{d:'M8 21H5a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h3'}]],
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
  shield: [['path', { d: 'M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z' }], ['path', { d: 'm9 12 2 2 4-4' }]],
  secrets: [['path', { d: 'M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z' }], ['circle', { cx: '16.5', cy: '7.5', r: '.5', fill: 'currentColor' }]],
  key: [['path', { d: 'M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z' }], ['circle', { cx: '16.5', cy: '7.5', r: '.5', fill: 'currentColor' }]],
  lock: [['rect', { width: '18', height: '11', x: '3', y: '11', rx: '2', ry: '2' }], ['path', { d: 'M7 11V7a5 5 0 0 1 10 0v4' }]],
  terminal: [['polyline', { points: '4 17 10 11 4 5' }], ['line', { x1: '12', x2: '20', y1: '19', y2: '19' }]],
  config: [['path', { d: 'M14 17H5' }], ['path', { d: 'M19 7h-9' }], ['circle', { cx: '17', cy: '17', r: '3' }], ['circle', { cx: '7', cy: '7', r: '3' }]],
  store: [['path', { d: 'M15 21v-5a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v5' }], ['path', { d: 'M17.774 10.31a1.12 1.12 0 0 0-1.549 0 2.5 2.5 0 0 1-3.451 0 1.12 1.12 0 0 0-1.548 0 2.5 2.5 0 0 1-3.452 0 1.12 1.12 0 0 0-1.549 0 2.5 2.5 0 0 1-3.77-3.248l2.889-4.184A2 2 0 0 1 7 2h10a2 2 0 0 1 1.653.873l2.895 4.192a2.5 2.5 0 0 1-3.774 3.244' }], ['path', { d: 'M4 10.95V19a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8.05' }]],
  freeze: [['path', { d: 'm10 20-1.25-2.5L6 18' }], ['path', { d: 'M10 4 8.75 6.5 6 6' }], ['path', { d: 'm14 20 1.25-2.5L18 18' }], ['path', { d: 'm14 4 1.25 2.5L18 6' }], ['path', { d: 'm17 21-3-6h-4' }], ['path', { d: 'm17 3-3 6 1.5 3' }], ['path', { d: 'M2 12h6.5L10 9' }], ['path', { d: 'm20 10-1.5 2 1.5 2' }], ['path', { d: 'M22 12h-6.5L14 15' }], ['path', { d: 'm4 10 1.5 2L4 14' }], ['path', { d: 'm7 21 3-6-1.5-3' }], ['path', { d: 'm7 3 3 6h4' }]],
  'obj-card': [['rect', { width: '20', height: '14', x: '2', y: '5', rx: '2' }], ['line', { x1: '2', x2: '22', y1: '10', y2: '10' }], ['path', { d: 'M6 14h2' }]],
  'obj-profile': [['path', { d: 'M13 19a4 4 0 0 0-8 0' }], ['path', { d: 'M16 10h2' }], ['path', { d: 'M16 14h2' }], ['circle', { cx: '9', cy: '12', r: '3' }], ['rect', { x: '2', y: '5', width: '20', height: '14', rx: '2' }]],
  cookie: [['path', { d: 'M12 2a10 10 0 1 0 10 10 4 4 0 0 1-5-5 4 4 0 0 1-5-5' }], ['path', { d: 'M8.5 8.5v.01' }], ['path', { d: 'M16 15.5v.01' }], ['path', { d: 'M12 12v.01' }], ['path', { d: 'M11 17v.01' }], ['path', { d: 'M7 14v.01' }]],
  eyeoff: [['path', { d: 'M9.88 9.88a3 3 0 1 0 4.24 4.24' }], ['path', { d: 'M10.73 5.08A10.43 10.43 0 0 1 12 5c7 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68' }], ['path', { d: 'M6.61 6.61A13.526 13.526 0 0 0 2 12s3 7 10 7a9.74 9.74 0 0 0 5.39-1.61' }], ['line', { x1: '2', x2: '22', y1: '2', y2: '22' }]],
  upload: [['path', { d: 'M12 3v12' }], ['path', { d: 'm17 8-5-5-5 5' }], ['path', { d: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4' }]],
  download: [['path', { d: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4' }], ['polyline', { points: '7 10 12 15 17 10' }], ['line', { x1: '12', x2: '12', y1: '15', y2: '3' }]],
  hold: [['circle', { cx: '12', cy: '12', r: '10' }], ['path', { d: 'M12 6v6l4 2' }]],
  hash: [['line', { x1: '4', x2: '20', y1: '9', y2: '9' }], ['line', { x1: '4', x2: '20', y1: '15', y2: '15' }], ['line', { x1: '10', x2: '8', y1: '3', y2: '21' }], ['line', { x1: '16', x2: '14', y1: '3', y2: '21' }]],
  banknote: [['rect', { width: '20', height: '12', x: '2', y: '6', rx: '2' }], ['circle', { cx: '12', cy: '12', r: '2' }], ['path', { d: 'M6 12h.01M18 12h.01' }]],
  settings: [['path', { d: 'M14 17H5' }], ['path', { d: 'M19 7h-9' }], ['circle', { cx: '17', cy: '17', r: '3' }], ['circle', { cx: '7', cy: '7', r: '3' }]],
  search: [['path', { d: 'm21 21-4.34-4.34' }], ['circle', { cx: '11', cy: '11', r: '8' }]],
  sun: [['circle', { cx: '12', cy: '12', r: '4' }], ['path', { d: 'M12 2v2' }], ['path', { d: 'M12 20v2' }], ['path', { d: 'm4.93 4.93 1.41 1.41' }], ['path', { d: 'm17.66 17.66 1.41 1.41' }], ['path', { d: 'M2 12h2' }], ['path', { d: 'M20 12h2' }], ['path', { d: 'm6.34 17.66-1.41 1.41' }], ['path', { d: 'm19.07 4.93-1.41 1.41' }]],
  moon: [['path', { d: 'M20.985 12.486a9 9 0 1 1-9.473-9.472c.405-.022.617.46.402.803a6 6 0 0 0 8.268 8.268c.344-.215.825-.004.803.401' }]],
  check: [['path', { d: 'M20 6 9 17l-5-5' }]],
  info: [['circle', { cx: '12', cy: '12', r: '10' }], ['path', { d: 'M12 16v-4' }], ['path', { d: 'M12 8h.01' }]],
  x: [['path', { d: 'M18 6 6 18' }], ['path', { d: 'm6 6 12 12' }]],
  pause: [['rect', { x: '14', y: '3', width: '5', height: '18', rx: '1' }], ['rect', { x: '5', y: '3', width: '5', height: '18', rx: '1' }]],
  chev: [['path', { d: 'm9 18 6-6-6-6' }]],
  chevd: [['path', { d: 'm6 9 6 6 6-6' }]],
  clock: [['circle', { cx: '12', cy: '12', r: '10' }], ['path', { d: 'M12 6v6l4 2' }]],
  filter: [['path', { d: 'M10 20a1 1 0 0 0 .553.895l2 1A1 1 0 0 0 14 21v-7a2 2 0 0 1 .517-1.341L21.74 4.67A1 1 0 0 0 21 3H3a1 1 0 0 0-.742 1.67l7.225 7.989A2 2 0 0 1 10 14z' }]],
  zap: [['path', { d: 'M15.914 4a1.5 1.5 0 00-2.474-1.561l-9 9A1.5 1.5 0 005.5 14h4.002a.5.5 0 01.471.666L8.086 20a1.5 1.5 0 002.475 1.56l9-9A1.5 1.5 0 0018.5 10h-3.997a.5.5 0 01-.472-.667z' }]],
  warn: [['path', { d: 'm21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3' }], ['path', { d: 'M12 9v4' }], ['path', { d: 'M12 17h.01' }]],
  users: [['path', { d: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2' }], ['path', { d: 'M16 3.128a4 4 0 0 1 0 7.744' }], ['path', { d: 'M22 21v-2a4 4 0 0 0-3-3.87' }], ['circle', { cx: '9', cy: '7', r: '4' }]],
  passkey: [['path', { d: 'M12 10a2 2 0 0 0-2 2c0 1.02-.1 2.51-.26 4' }], ['path', { d: 'M14 13.12c0 2.38 0 6.38-1 8.88' }], ['path', { d: 'M17.29 21.02c.12-.6.43-2.3.5-3.02' }], ['path', { d: 'M2 12a10 10 0 0 1 18-6' }], ['path', { d: 'M2 16h.01' }], ['path', { d: 'M21.8 16c.2-2 .131-5.354 0-6' }], ['path', { d: 'M5 19.5C5.5 18 6 15 6 12a6 6 0 0 1 .34-2' }], ['path', { d: 'M8.65 22c.21-.66.45-1.32.57-2' }], ['path', { d: 'M9 6.8a6 6 0 0 1 9 5.2v2' }]],
  hand: [['path', { d: 'M18 11V6a2 2 0 0 0-2-2a2 2 0 0 0-2 2' }], ['path', { d: 'M14 10V4a2 2 0 0 0-2-2a2 2 0 0 0-2 2v2' }], ['path', { d: 'M10 10.5V6a2 2 0 0 0-2-2a2 2 0 0 0-2 2v8' }], ['path', { d: 'M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.86-5.99-2.34l-3.6-3.6a2 2 0 0 1 2.83-2.82L7 15' }]],
  more: [['circle', { cx: '12', cy: '12', r: '1' }], ['circle', { cx: '19', cy: '12', r: '1' }], ['circle', { cx: '5', cy: '12', r: '1' }]],
  'obj-connector': [['path', { d: 'M12 22v-5' }], ['path', { d: 'M15 8V2' }], ['path', { d: 'M17 8a1 1 0 0 1 1 1v4a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V9a1 1 0 0 1 1-1z' }], ['path', { d: 'M9 8V2' }]],
  'obj-update': [['path', { d: 'M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8' }], ['path', { d: 'M21 3v5h-5' }], ['path', { d: 'M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16' }], ['path', { d: 'M8 16H3v5' }]],
  // updates: the design's icon('updates') -> Lucide refresh-cw (System's
  // Check now button, a pack/plugin's Update to vX…) — same glyph as
  // obj-update, kept as its own key so the SHAPES lookup never silently
  // falls back to the ellipsis "more" icon for it.
  updates: [['path', { d: 'M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8' }], ['path', { d: 'M21 3v5h-5' }], ['path', { d: 'M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16' }], ['path', { d: 'M8 16H3v5' }]],
  'act-money': [['rect', { width: '20', height: '12', x: '2', y: '6', rx: '2' }], ['circle', { cx: '12', cy: '12', r: '2' }], ['path', { d: 'M6 12h.01M18 12h.01' }]],
  login: [['path', { d: 'm10 17 5-5-5-5' }], ['path', { d: 'M15 12H3' }], ['path', { d: 'M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4' }]],
  'obj-bot': [['path', { d: 'M12 8V4H8' }], ['rect', { width: '16', height: '12', x: '4', y: '8', rx: '2' }], ['path', { d: 'M2 14h2' }], ['path', { d: 'M20 14h2' }], ['path', { d: 'M15 13v2' }], ['path', { d: 'M9 13v2' }]],
  'obj-workflow': [['rect', { width: '8', height: '8', x: '3', y: '3', rx: '2' }], ['path', { d: 'M7 11v4a2 2 0 0 0 2 2h4' }], ['rect', { width: '8', height: '8', x: '13', y: '13', rx: '2' }]],
  plus: [['path', { d: 'M5 12h14' }], ['path', { d: 'M12 5v14' }]],
  send: [['path', { d: 'M3.714 3.048a.498.498 0 0 0-.683.627l2.843 7.627a2 2 0 0 1 0 1.396l-2.842 7.627a.498.498 0 0 0 .682.627l18-8.5a.5.5 0 0 0 0-.904z' }], ['path', { d: 'M6 12h16' }]],
  grid: [['rect', { width: '7', height: '7', x: '3', y: '3', rx: '1' }], ['rect', { width: '7', height: '7', x: '14', y: '3', rx: '1' }], ['rect', { width: '7', height: '7', x: '14', y: '14', rx: '1' }], ['rect', { width: '7', height: '7', x: '3', y: '14', rx: '1' }]],
  logs: [['path', { d: 'M15 12h-5' }], ['path', { d: 'M15 8h-5' }], ['path', { d: 'M19 17V5a2 2 0 0 0-2-2H4' }], ['path', { d: 'M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3' }]],
  calendar: [['path', { d: 'M16 14v2.2l1.6 1' }], ['path', { d: 'M16 2v3' }], ['path', { d: 'M21 7.338V5a2 2 0 00-2-2H5a2 2 0 00-2 2v14a2 2 0 002 2h2.338' }], ['path', { d: 'M3 9h5.859' }], ['path', { d: 'M8 2v3' }], ['circle', { cx: '16', cy: '16', r: '6' }]],
  undo: [['path', { d: 'M9 14 4 9l5-5' }], ['path', { d: 'M4 9h10.5a5.5 5.5 0 0 1 5.5 5.5a5.5 5.5 0 0 1-5.5 5.5H11' }]],
  edit: [['path', { d: 'M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497z' }], ['path', { d: 'm15 5 4 4' }]],
  file: [['path', { d: 'M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z' }], ['path', { d: 'M14 2v5a1 1 0 0 0 1 1h5' }], ['path', { d: 'M10 9H8' }], ['path', { d: 'M16 13H8' }], ['path', { d: 'M16 17H8' }]],
  pin: [['path', { d: 'M12 17v5' }], ['path', { d: 'M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z' }]],
  attach: [['path', { d: 'm16 6-8.414 8.586a2 2 0 0 0 2.829 2.829l8.414-8.586a4 4 0 1 0-5.657-5.657l-8.379 8.551a6 6 0 1 0 8.485 8.485l8.379-8.551' }]],
  tag: [['path', { d: 'M12.586 2.586A2 2 0 0 0 11.172 2H4a2 2 0 0 0-2 2v7.172a2 2 0 0 0 .586 1.414l8.704 8.704a2.426 2.426 0 0 0 3.42 0l6.58-6.58a2.426 2.426 0 0 0 0-3.42z' }], ['circle', { cx: '7.5', cy: '7.5', r: '.5', fill: 'currentColor' }]],
  image: [['rect', { width: '18', height: '18', x: '3', y: '3', rx: '2', ry: '2' }], ['circle', { cx: '9', cy: '9', r: '2' }], ['path', { d: 'm21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21' }]],
  play: [['path', { d: 'M5 5a2 2 0 0 1 3.008-1.728l11.997 6.998a2 2 0 0 1 .003 3.458l-12 7A2 2 0 0 1 5 19z' }]],
  trash: [['path', { d: 'M10 11v6' }], ['path', { d: 'M14 11v6' }], ['path', { d: 'M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6' }], ['path', { d: 'M3 6h18' }], ['path', { d: 'M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2' }]],
  copy: [['rect', { width: '14', height: '14', x: '8', y: '8', rx: '2', ry: '2' }], ['path', { d: 'M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2' }]],
  link: [['path', { d: 'M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71' }], ['path', { d: 'M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71' }]],
  code: [['path', { d: 'm16 18 6-6-6-6' }], ['path', { d: 'm8 6-6 6 6 6' }]],
  dot: [['circle', { cx: '12', cy: '12', r: '6', fill: 'currentColor' }]],
  'obj-pack': [['path', { d: 'M11 21.73a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73z' }], ['path', { d: 'M12 22V12' }], ['polyline', { points: '3.29 7 12 12 20.71 7' }], ['path', { d: 'm7.5 4.27 9 5.15' }]],
  sparkle: [['path', { d: 'M11.017 2.814a1 1 0 0 1 1.966 0l1.051 5.558a2 2 0 0 0 1.594 1.594l5.558 1.051a1 1 0 0 1 0 1.966l-5.558 1.051a2 2 0 0 0-1.594 1.594l-1.051 5.558a1 1 0 0 1-1.966 0l-1.051-5.558a2 2 0 0 0-1.594-1.594l-5.558-1.051a1 1 0 0 1 0-1.966l5.558-1.051a2 2 0 0 0 1.594-1.594z' }], ['path', { d: 'M20 2v4' }], ['path', { d: 'M22 4h-4' }], ['circle', { cx: '4', cy: '20', r: '2' }]],
  'obj-question': [['path', { d: 'M2.992 16.342a2 2 0 0 1 .094 1.167l-1.065 3.29a1 1 0 0 0 1.236 1.168l3.413-.998a2 2 0 0 1 1.099.092 10 10 0 1 0-4.777-4.719' }], ['path', { d: 'M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3' }], ['path', { d: 'M12 17h.01' }]],
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
