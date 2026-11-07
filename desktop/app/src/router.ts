// A tiny hash router, same shape as web/src/router.ts: `route` is the full
// path after "#/" (no query); `screen` is its first segment, which is what
// the rail nav and the top-level screen switch key off. The tray and
// notifications navigate in-app by setting `location.hash` (see
// src/lib/navigate.ts), never by loading a remote origin.
import { signal } from '@preact/signals'

function parse(): string {
  const h = location.hash.replace(/^#\/?/, '')
  return h.split('?')[0]
}

export const route = signal<string>(parse())

window.addEventListener('hashchange', () => {
  route.value = parse()
})

export function screen(): string {
  return route.value.split('/')[0]
}

export function seg(n: number): string {
  const parts = route.value.split('/')
  const s = parts[n]
  return s ? decodeURIComponent(s) : ''
}

export function go(path: string): void {
  location.hash = '#/' + path.replace(/^\//, '')
}
