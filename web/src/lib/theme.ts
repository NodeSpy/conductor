// The one piece of shared theme state: Shell's account menu and the
// Preferences page both read and set it, so switching the theme in either
// place is reflected in the other without a remount (prototype parity:
// UI.theme / UI.themeIcon update the one place the icon is drawn).
import { signal } from '@preact/signals'

export type Theme = 'auto' | 'light' | 'dark'

function current(): Theme {
  const v = document.documentElement.getAttribute('data-theme')
  return v === 'light' || v === 'dark' ? v : 'auto'
}

export const themeSignal = signal<Theme>(current())

export function setTheme(next: Theme): void {
  const root = document.documentElement
  if (next === 'auto') {
    root.removeAttribute('data-theme')
    localStorage.removeItem('theme')
  } else {
    root.setAttribute('data-theme', next)
    localStorage.setItem('theme', next)
  }
  themeSignal.value = next
}
