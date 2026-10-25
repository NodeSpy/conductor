// Small shared chrome for the auth screens: the brand mark and the passkey
// glyph. The passkey glyph means "this signs" (design §3.2) — it appears only on
// a control that starts a WebAuthn signing (sign in, enrol, approve).
import type { JSX } from 'preact'

export function GateMark(): JSX.Element {
  return (
    <svg class="logo-mark" viewBox="2.5 6 27 20" role="img" aria-label="conductor" width="28" height="22">
      <path fill="var(--color-text-primary)" d="M2.5 21a13.5 15 0 0 1 27 0h-5a8.5 9.5 0 0 0-17 0z" />
      <circle cx="16" cy="21" r="5" fill="var(--color-accent-default)" />
    </svg>
  )
}

export function PasskeyGlyph(): JSX.Element {
  return (
    <svg
      class="passkey-glyph"
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="1.75"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <circle cx="9" cy="8" r="4" />
      <path d="M10.5 12.5a6 6 0 0 0-7.5 5.8V20h8" />
      <circle cx="17.5" cy="14.5" r="2.5" />
      <path d="M17.5 17v4l1.5-1.2 1.5 1.2v-4" />
    </svg>
  )
}
