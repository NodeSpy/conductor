// Every string the dashboard receives is shown through safeText: C0 and C1
// controls (other than tab, newline and carriage return), DEL, the bidi
// embeddings, overrides, isolates and marks, the zero-width characters and the
// Unicode line and paragraph separators become visible escapes (\x1b, \u202e).
// Text from the server can come from a bot, a config value or a browser's
// user agent; none of it may reorder or hide what the operator reads. The API
// clients parse every response with parseSafe, so no screen has to remember.
const UNSAFE = /[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f\u061c\u200b-\u200f\u2028-\u202e\u2060\u2066-\u2069\ufeff]/g

const UNSAFE_ONE = new RegExp(UNSAFE.source)

export function safeText(s: string): string {
  return s.replace(UNSAFE, (c) => {
    const n = c.charCodeAt(0)
    return n < 0x100 ? '\\x' + n.toString(16).padStart(2, '0') : '\\u' + n.toString(16).padStart(4, '0')
  })
}

function reviver(_key: string, v: unknown): unknown {
  if (typeof v === 'string') return safeText(v)
  if (v && typeof v === 'object' && !Array.isArray(v)) {
    const o = v as Record<string, unknown>
    if (Object.keys(o).some((k) => UNSAFE_ONE.test(k))) {
      const out: Record<string, unknown> = {}
      for (const [k, x] of Object.entries(o)) out[safeText(k)] = x
      return out
    }
  }
  return v
}

// parseSafe is JSON.parse with every string (keys too) made safe to show.
export function parseSafe<T>(text: string): T {
  return JSON.parse(text, reviver) as T
}

// readJSON reads a response body through parseSafe.
export async function readJSON<T>(res: Response): Promise<T> {
  return parseSafe<T>(await res.text())
}
