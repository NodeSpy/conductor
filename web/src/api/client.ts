// The dashboard's typed fetch client. Shapes come from the tygo-generated
// types.ts (the Go API is the source of truth). Same-origin only (design §5.1:
// no CORS); errors arrive as {error:{code,message,hint}}.
import type { StatusResponse, RunsResponse, ErrorResponse } from './types'

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as ErrorResponse
      if (body?.error?.message) message = body.error.message
    } catch {
      /* non-JSON error body */
    }
    throw new Error(message)
  }
  return (await res.json()) as T
}

export interface RunsQuery {
  since?: string
  limit?: number
  cursor?: string
}

export const api = {
  status: () => getJSON<StatusResponse>('/api/v1/status'),
  runs: (q: RunsQuery = {}) => {
    const p = new URLSearchParams()
    if (q.since) p.set('since', q.since)
    if (q.limit) p.set('limit', String(q.limit))
    if (q.cursor) p.set('cursor', q.cursor)
    const qs = p.toString()
    return getJSON<RunsResponse>('/api/v1/runs' + (qs ? `?${qs}` : ''))
  },
}
