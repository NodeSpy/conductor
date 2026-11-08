// The two keyboard overlays the shell carries (prototype js/palette.js
// UI.palette / UI.shortcuts): the ⌘K command line and the shortcut sheet.
// Their open state lives here so either can open the other (the palette's
// "Keyboard shortcuts" action, the sheet's `/` fallback) without an import
// cycle between the two components.
import { signal } from '@preact/signals'

export const paletteOpen = signal(false)
export const shortcutsOpen = signal(false)
