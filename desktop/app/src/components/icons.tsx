// A small subset of Lucide icons (ISC licence) and Daylight's status glyphs,
// copied from the prototype (design/dashboard-prototype/js/icons.js) at a
// 1.75 stroke / 24-unit grid for icons and the 16-unit grid for status
// glyphs, matching `UI.icon` / `UI.glyph` there. Only the names this app
// actually uses are ported.
import type { JSX } from 'preact'

const ICONS: Record<string, string> = {
  hand: '<path d="M18 11V6a2 2 0 0 0-2-2a2 2 0 0 0-2 2"/><path d="M14 10V4a2 2 0 0 0-2-2a2 2 0 0 0-2 2v2"/><path d="M10 10.5V6a2 2 0 0 0-2-2a2 2 0 0 0-2 2v8"/><path d="M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.86-5.99-2.34l-3.6-3.6a2 2 0 0 1 2.83-2.82L7 15"/>',
  message: '<path d="M22 17a2 2 0 0 1-2 2H6.828a2 2 0 0 0-1.414.586l-2.202 2.202A.71.71 0 0 1 2 21.286V5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2z"/>',
  x: '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  send: '<path d="M3.714 3.048a.498.498 0 0 0-.683.627l2.843 7.627a2 2 0 0 1 0 1.396l-2.842 7.627a.498.498 0 0 0 .682.627l18-8.5a.5.5 0 0 0 0-.904z"/><path d="M6 12h16"/>',
  bell: '<path d="M10.268 21a2 2 0 0 0 3.464 0"/><path d="M3.262 15.326A1 1 0 0 0 4 17h16a1 1 0 0 0 .74-1.673C19.41 13.956 18 12.499 18 8A6 6 0 0 0 6 8c0 4.499-1.411 5.956-2.738 7.326"/>',
  clock: '<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>',
  dot: '<circle cx="12" cy="12" r="6"/>',
  chev: '<path d="m9 18 6-6-6-6"/>',
  warn: '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/>',
  external: '<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>',
  users: '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><path d="M16 3.128a4 4 0 0 1 0 7.744"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><circle cx="9" cy="7" r="4"/>',
  bots: '<path d="M12 8V4H8"/><rect width="16" height="12" x="4" y="8" rx="2"/><path d="M2 14h2"/><path d="M20 14h2"/><path d="M15 13v2"/><path d="M9 13v2"/>',
  pin: '<path d="M12 17v5"/><path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"/>',
  attach: '<path d="m16 6-8.414 8.586a2 2 0 0 0 2.829 2.829l8.414-8.586a4 4 0 1 0-5.657-5.657l-8.379 8.551a6 6 0 1 0 8.485 8.485l8.379-8.551" />',
  overview: '<rect width="7" height="7" x="3" y="3" rx="1"/><rect width="7" height="7" x="14" y="3" rx="1"/><rect width="7" height="7" x="14" y="14" rx="1"/><rect width="7" height="7" x="3" y="14" rx="1"/>',
}

export function Icon(props: { name: string; class?: string }): JSX.Element {
  return (
    <svg
      class={props.class}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="1.75"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
      dangerouslySetInnerHTML={{ __html: ICONS[props.name] || ICONS.dot }}
    />
  )
}

// Daylight's status glyphs (UI.glyph), one shape per state so each mark reads
// in a single colour without relying on colour alone.
const GLYPHS: Record<string, string> = {
  success: '<circle cx="8" cy="8" r="3.4" fill="currentColor" stroke="none"/>',
  warning:
    '<path d="M8 2.6l5.9 10.4H2.1z" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round"/><circle cx="8" cy="10.4" r="1.1" fill="currentColor"/>',
  danger: '<path d="M4.4 4.4l7.2 7.2M11.6 4.4l-7.2 7.2" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"/>',
  running: '<circle cx="8" cy="8" r="5" fill="none" stroke="currentColor" stroke-width="2.2" stroke-dasharray="22 9.5" stroke-linecap="round"/>',
  neutral: '<path d="M5.7 3.8v8.4M10.3 3.8v8.4" fill="none" stroke="currentColor" stroke-width="2.7" stroke-linecap="round"/>',
  'needs-you': '<path d="M8 1.9l6.1 6.1L8 14.1 1.9 8z" fill="currentColor" stroke="none"/>',
  shadow:
    '<circle cx="8" cy="8" r="5.2" fill="none" stroke="currentColor" stroke-width="1.7"/><path d="M8 2.8a5.2 5.2 0 0 1 0 10.4z" fill="currentColor" stroke="none"/>',
}

// One status vocabulary (marks.css): a state name ("ok" | "fail" | "run" |
// "wait" | "idle" | "ember" | "shadow") maps to a glyph and a default word.
const SG: Record<string, [string, string]> = {
  ok: ['success', 'Passed'],
  run: ['running', 'Running'],
  wait: ['warning', 'Waiting'],
  fail: ['danger', 'Failed'],
  idle: ['neutral', 'Parked'],
  shadow: ['shadow', 'Shadow'],
  ember: ['needs-you', 'Needs you'],
}

function glyphSvg(shape: string): string {
  return `<svg class="glyph" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${GLYPHS[shape] || ''}</svg>`
}

// Mark: the state shape alone (margins, list rows). `label` sets the
// accessible name; pass `true` to use the state's default word.
export function Mark(props: { s: string; lg?: boolean; label?: string | boolean }): JSX.Element {
  const g = SG[props.s] || SG.idle
  const label = props.label === true ? g[1] : props.label
  return (
    <span class={'st mk-only' + (props.lg ? ' lg' : '')} data-s={props.s} role={label ? 'img' : undefined} aria-label={label || undefined}>
      <i dangerouslySetInnerHTML={{ __html: glyphSvg(g[0]) }} />
    </span>
  )
}

// A status mark with its word beside it (UI.statusMark).
export function StatusMark(props: { s: string; word?: string; sm?: boolean; lg?: boolean }): JSX.Element {
  const g = SG[props.s] || SG.idle
  return (
    <span class={'st' + (props.sm ? ' sm' : '') + (props.lg ? ' lg' : '')} data-s={props.s}>
      <i dangerouslySetInnerHTML={{ __html: glyphSvg(g[0]) }} />
      {props.word ?? g[1]}
    </span>
  )
}
