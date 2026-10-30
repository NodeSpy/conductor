// A tiny hash router: `route` tracks the full location.hash path (without the
// leading "#/" and without the query), so the Shell can switch screens on the
// first segment and detail screens (#/runs/:id, #/workflows/:name) can read the
// rest. Hash routing keeps the SPA fallback trivial — every path serves
// index.html and the client resolves the hash (design §8).
import { signal } from '@preact/signals'

function parse(): string {
  const h = location.hash.replace(/^#\/?/, '')
  return h.split('?')[0]
}

// route is the full path ("runs/r-ab12"); screen is just its first segment, which
// is what the Shell nav and the top-level screen switch key off.
export const route = signal<string>(parse())

window.addEventListener('hashchange', () => {
  route.value = parse()
})

// screen returns the first path segment of the current route.
export function screen(): string {
  return route.value.split('/')[0]
}

// seg returns the n-th path segment (decoded), or '' when absent.
export function seg(n: number): string {
  const parts = route.value.split('/')
  const s = parts[n]
  return s ? decodeURIComponent(s) : ''
}

// query reads one search-param off the current hash (#/activity?q=foo).
export function query(key: string): string {
  const q = location.hash.split('?')[1] || ''
  return new URLSearchParams(q).get(key) || ''
}

export function go(path: string): void {
  location.hash = '#/' + path.replace(/^\//, '')
}
