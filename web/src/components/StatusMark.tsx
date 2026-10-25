// The status vocabulary (design §3.2): a 12px mark plus a word, no capsule.
// Each state is a distinct shape so it reads in one colour. The mark always
// carries its state colour (--mk, via css/marks rules ported into app.css); the
// word is coloured for live/needs-you/passed states and bold when a person is
// needed. Screens never draw status markup by hand — they use this component.
import type { JSX } from 'preact'

function glyph(s: string): JSX.Element {
  switch (s) {
    case 'ok':
    case 'success':
      return (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
          <circle cx="8" cy="8" r="6.5" />
          <path d="M5 8.2l2 2 4-4.4" />
        </svg>
      )
    case 'fail':
      return (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
          <circle cx="8" cy="8" r="6.5" />
          <path d="M5.5 5.5l5 5M10.5 5.5l-5 5" />
        </svg>
      )
    case 'run':
    case 'staged':
      // An open arc; css spins it (design: the running arc turns).
      return (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round">
          <path d="M8 1.6a6.4 6.4 0 1 1-6.4 6.4" />
        </svg>
      )
    case 'ember':
      // Needs you: a filled ember diamond.
      return (
        <svg viewBox="0 0 16 16" fill="currentColor">
          <path d="M8 1.2 14.8 8 8 14.8 1.2 8z" />
        </svg>
      )
    case 'wait':
      // Waiting: a clock.
      return (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
          <circle cx="8" cy="8" r="6.5" />
          <path d="M8 4.4V8l2.6 1.6" />
        </svg>
      )
    case 'idle':
      // Parked / neutral: two pause bars (never the same shape as another state).
      return (
        <svg viewBox="0 0 16 16" fill="currentColor">
          <rect x="4.5" y="4" width="2.4" height="8" rx="1" />
          <rect x="9.1" y="4" width="2.4" height="8" rx="1" />
        </svg>
      )
    default:
      return (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round">
          <path d="M3.5 8h9" />
        </svg>
      )
  }
}

export function StatusMark({
  s,
  word,
  lg,
  mkOnly,
}: {
  s: string
  word?: string
  lg?: boolean
  mkOnly?: boolean
}): JSX.Element {
  const cls = ['st', lg ? 'lg' : '', mkOnly ? 'mk-only' : ''].filter(Boolean).join(' ')
  return (
    <span class={cls} data-s={s}>
      <i>{glyph(s)}</i>
      {!mkOnly && word ? <span>{word}</span> : null}
    </span>
  )
}
