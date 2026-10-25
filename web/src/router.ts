// A tiny hash router: `route` tracks location.hash (without the leading "#/"),
// so the Shell can switch screens and nav links are plain anchors (#/config).
// Hash routing keeps the SPA fallback trivial — every path serves index.html and
// the client resolves the hash (design §8).
import { signal } from '@preact/signals'

function parse(): string {
  const h = location.hash.replace(/^#\/?/, '')
  return h.split('?')[0]
}

export const route = signal<string>(parse())

window.addEventListener('hashchange', () => {
  route.value = parse()
})

export function go(path: string): void {
  location.hash = '#/' + path.replace(/^\//, '')
}
