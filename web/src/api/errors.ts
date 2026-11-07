// One way to turn any API answer into data or an error a person can act on.
// Every client (client.ts, bots.ts, browser.ts, auth.ts) reads responses with
// readResponse, and every screen shows a caught error with humanError — never
// String(e), which would show a parser's "SyntaxError: JSON.parse: …".
import { parseSafe } from './safe'

// ApiError is a failed or unreadable answer: its message is the server's own
// (or a plain one for its status), with the server's hint when it gave one.
export class ApiError extends Error {
  status: number
  code: string
  hint: string
  constructor(status: number, code: string, message: string, hint = '') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.hint = hint
  }
}

// The two error shapes the daemon writes: the dashboard's {error:{code,
// message,hint}} and the bot API's {error:"…"}.
interface ErrorBody {
  error?: string | { code?: string; message?: string; hint?: string }
}

// byStatus is what to say when the server's answer carries no message.
function byStatus(status: number): { message: string; hint: string } {
  if (status === 401) return { message: 'You are signed out', hint: 'sign in again to continue' }
  if (status === 403) return { message: "You don't have access to this", hint: '' }
  if (status === 404) return { message: "conductor doesn't have this", hint: 'reload the page; if it keeps happening, the dashboard and the daemon may be different versions' }
  if (status === 502 || status === 503 || status === 504)
    return { message: "conductor isn't answering right now", hint: 'check that the daemon is running, then try again' }
  if (status >= 500) return { message: `conductor hit a problem answering (error ${status})`, hint: 'the daemon log says why; try again' }
  return { message: `conductor turned this down (error ${status})`, hint: '' }
}

// readResponse returns the body of a successful answer as data, or throws an
// ApiError. A 2xx whose body isn't JSON (an HTML page where data belonged) is
// an error too, worded for a person rather than as the parser's exception.
export async function readResponse<T>(res: Response): Promise<T> {
  const text = await res.text().catch(() => '')
  let body: unknown
  let parsed = false
  try {
    body = parseSafe(text)
    parsed = true
  } catch {
    /* not JSON: worded below */
  }
  if (!res.ok) {
    const fallback = byStatus(res.status)
    const e = (parsed ? (body as ErrorBody)?.error : undefined) ?? ''
    if (typeof e === 'string') throw new ApiError(res.status, '', e || fallback.message, e ? '' : fallback.hint)
    throw new ApiError(res.status, e.code || '', e.message || fallback.message, e.hint ?? (e.message ? '' : fallback.hint))
  }
  if (!parsed) {
    throw new ApiError(
      res.status,
      'bad_reply',
      'conductor sent back something the dashboard could not read',
      'reload the page; if it keeps happening, the dashboard and the daemon may be different versions',
    )
  }
  return body as T
}

// humanError is what a screen shows for a caught error: the message and what
// to do about it. A network failure (fetch's TypeError) and a stray parser
// exception get plain words instead of the browser's own text.
export function humanError(e: unknown): string {
  if (e instanceof ApiError) return e.hint ? `${e.message} — ${e.hint}` : e.message
  if (e instanceof TypeError && /fetch|load failed|network/i.test(e.message))
    return "Couldn't reach conductor — check your connection and that the daemon is running, then try again"
  if (e instanceof SyntaxError) return 'conductor sent back something the dashboard could not read — reload the page'
  if (e instanceof Error) return e.message
  return String(e)
}
